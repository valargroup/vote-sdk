package keeper

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	sdk "github.com/cosmos/cosmos-sdk/types"
	stakingkeeper "github.com/cosmos/cosmos-sdk/x/staking/keeper"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"

	"github.com/mikelodder7/curvey"

	"github.com/valargroup/vote-sdk/crypto/elgamal"
	"github.com/valargroup/vote-sdk/crypto/shamir"
	"github.com/valargroup/vote-sdk/x/vote/types"
)

// RegisterPallasKey handles MsgRegisterPallasKey.
// Registers the validator's Pallas public key in the global registry (prefix 0x0C).
// This is decoupled from ceremony state — keys persist across rounds and are
// snapshotted into each round's ceremony_validators when a round is created.
func (ms msgServer) RegisterPallasKey(goCtx context.Context, msg *types.MsgRegisterPallasKey) (*types.MsgRegisterPallasKeyResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)
	kvStore := ms.k.OpenKVStore(ctx)

	// Validate pallas_pk: 32 bytes, valid Pallas point, not identity.
	if _, err := elgamal.UnmarshalPublicKey(msg.PallasPk); err != nil {
		return nil, fmt.Errorf("%w: %v", types.ErrInvalidPallasPoint, err)
	}

	// Derive the validator operator address from the sender's account address.
	// PrepareProposal identifies the proposer by val.OperatorAddress (valoper
	// bech32), so the registry must use the same format for lookups.
	accAddr, err := sdk.AccAddressFromBech32(msg.Creator)
	if err != nil {
		return nil, fmt.Errorf("invalid creator address %q: %w", msg.Creator, err)
	}
	valAddr := sdk.ValAddress(accAddr).String()

	if err := ms.k.RegisterPallasKeyCore(kvStore, valAddr, msg.PallasPk); err != nil {
		return nil, err
	}

	ctx.EventManager().EmitEvent(sdk.NewEvent(
		types.EventTypeRegisterPallasKey,
		sdk.NewAttribute(types.AttributeKeyValidatorAddress, valAddr),
	))

	return &types.MsgRegisterPallasKeyResponse{}, nil
}

// ContributeDKG handles MsgContributeDKG.
// Each ceremony validator contributes their Feldman commitment vector and
// ECIES-encrypted shares for all other validators. When the final (n-th)
// contribution arrives, the handler combines all commitment vectors via
// CombineCommitments, derives the joint ea_pk, and transitions REGISTERING → DEALT.
//
// This message can only be injected by the block proposer via PrepareProposal;
// direct submission through the mempool is rejected by ValidateProposerIsCreator.
func (ms msgServer) ContributeDKG(goCtx context.Context, msg *types.MsgContributeDKG) (*types.MsgContributeDKGResponse, error) {
	if err := ms.k.ValidateProposerIsCreator(goCtx, msg.Creator, "MsgContributeDKG"); err != nil {
		return nil, err
	}

	ctx := sdk.UnwrapSDKContext(goCtx)
	kvStore := ms.k.OpenKVStore(ctx)

	round, err := ms.k.GetPendingRoundWithCeremony(kvStore, msg.VoteRoundId, types.CeremonyStatus_CEREMONY_STATUS_REGISTERING)
	if err != nil {
		return nil, err
	}

	nValidators := len(round.CeremonyValidators)
	if nValidators == 0 {
		return nil, fmt.Errorf("%w: no validators in round ceremony", types.ErrCeremonyWrongStatus)
	}

	if _, found := FindValidatorInRoundCeremony(round, msg.Creator); !found {
		return nil, fmt.Errorf("%w: %s is not a ceremony validator", types.ErrNotRegisteredValidator, msg.Creator)
	}

	if _, found := FindContributionInRound(round, msg.Creator); found {
		return nil, fmt.Errorf("%w: %s", types.ErrDuplicateContribution, msg.Creator)
	}

	expectedThreshold, err := ThresholdForN(nValidators)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", types.ErrInvalidThreshold, err)
	}
	if len(msg.FeldmanCommitments) != expectedThreshold {
		return nil, fmt.Errorf("%w: expected %d Feldman commitments, got %d",
			types.ErrInvalidThreshold, expectedThreshold, len(msg.FeldmanCommitments))
	}
	for i, c := range msg.FeldmanCommitments {
		if _, err := elgamal.UnmarshalPublicKey(c); err != nil {
			return nil, fmt.Errorf("%w: feldman_commitment[%d]: %v",
				types.ErrInvalidPallasPoint, i, err)
		}
	}

	expectedPayloads := nValidators - 1
	if len(msg.Payloads) != expectedPayloads {
		return nil, fmt.Errorf("%w: got %d payloads, expected %d (all validators except contributor)",
			types.ErrPayloadMismatch, len(msg.Payloads), expectedPayloads)
	}

	covered := make(map[string]bool, expectedPayloads)
	for _, p := range msg.Payloads {
		if p.ValidatorAddress == msg.Creator {
			return nil, fmt.Errorf("%w: payload must not include contributor's own address %s",
				types.ErrPayloadMismatch, msg.Creator)
		}
		if _, found := FindValidatorInRoundCeremony(round, p.ValidatorAddress); !found {
			return nil, fmt.Errorf("%w: payload references unknown validator %s",
				types.ErrNotRegisteredValidator, p.ValidatorAddress)
		}
		if covered[p.ValidatorAddress] {
			return nil, fmt.Errorf("%w: duplicate payload for validator %s",
				types.ErrPayloadMismatch, p.ValidatorAddress)
		}
		covered[p.ValidatorAddress] = true

		if _, err := elgamal.UnmarshalPublicKey(p.EphemeralPk); err != nil {
			return nil, fmt.Errorf("%w: ephemeral_pk for %s: %v",
				types.ErrInvalidPallasPoint, p.ValidatorAddress, err)
		}
	}

	round.DkgContributions = append(round.DkgContributions, &types.DKGContribution{
		ValidatorAddress:   msg.Creator,
		FeldmanCommitments: msg.FeldmanCommitments,
		Payloads:           msg.Payloads,
	})

	if len(round.DkgContributions) == nValidators {
		if err := ms.finalizeDKG(ctx, round, nValidators, expectedThreshold); err != nil {
			return nil, err
		}
	} else {
		AppendCeremonyLog(round, uint64(ctx.BlockHeight()),
			fmt.Sprintf("DKG contribution from %s (%d/%d)",
				msg.Creator, len(round.DkgContributions), nValidators))
	}

	if err := ms.k.SetVoteRound(kvStore, round); err != nil {
		return nil, err
	}

	ctx.EventManager().EmitEvent(sdk.NewEvent(
		types.EventTypeContributeDKG,
		sdk.NewAttribute(types.AttributeKeyRoundID, hex.EncodeToString(msg.VoteRoundId)),
		sdk.NewAttribute(types.AttributeKeyValidatorAddress, msg.Creator),
		sdk.NewAttribute(types.AttributeKeyCeremonyStatus, round.CeremonyStatus.String()),
	))

	return &types.MsgContributeDKGResponse{}, nil
}

// finalizeDKG deserializes all per-contributor commitment vectors, combines them
// via CombineCommitments, and transitions the round to DEALT. Called when the
// n-th DKG contribution arrives.
func (ms msgServer) finalizeDKG(ctx sdk.Context, round *types.VoteRound, nValidators, threshold int) error {
	allCommitments := make([][]curvey.Point, nValidators)
	for i, contrib := range round.DkgContributions {
		vec := make([]curvey.Point, len(contrib.FeldmanCommitments))
		for j, raw := range contrib.FeldmanCommitments {
			pt, err := elgamal.UnmarshalPublicKey(raw)
			if err != nil {
				return fmt.Errorf("failed to unmarshal contribution %d commitment %d: %w", i, j, err)
			}
			vec[j] = pt.Point
		}
		allCommitments[i] = vec
	}

	combined, err := shamir.CombineCommitments(allCommitments)
	if err != nil {
		return fmt.Errorf("failed to combine commitments: %w", err)
	}

	round.EaPk = combined[0].ToAffineCompressed()

	round.FeldmanCommitments = make([][]byte, len(combined))
	for j, c := range combined {
		round.FeldmanCommitments[j] = c.ToAffineCompressed()
	}

	round.Threshold = uint32(threshold)

	for i := range round.CeremonyValidators {
		round.CeremonyValidators[i].ShamirIndex = uint32(i + 1)
	}

	round.CeremonyPhaseStart = uint64(ctx.BlockTime().Unix())
	round.CeremonyPhaseTimeout = types.DefaultDealTimeout
	round.CeremonyStatus = types.CeremonyStatus_CEREMONY_STATUS_DEALT

	AppendCeremonyLog(round, uint64(ctx.BlockHeight()),
		fmt.Sprintf("DKG complete (%d/%d contributions), ea_pk=%s",
			nValidators, nValidators, hex.EncodeToString(round.EaPk)[:16]))

	return nil
}

// AckExecutiveAuthorityKey handles MsgAckExecutiveAuthorityKey.
// A registered validator acknowledges receipt of their ea_sk share.
// When all validators have acked (fast path), ceremony transitions DEALT -> CONFIRMED
// and the round transitions PENDING -> ACTIVE.
//
// This message can only be injected by the block proposer via PrepareProposal;
// direct submission through the mempool is rejected by ValidateProposerIsCreator.
func (ms msgServer) AckExecutiveAuthorityKey(goCtx context.Context, msg *types.MsgAckExecutiveAuthorityKey) (*types.MsgAckExecutiveAuthorityKeyResponse, error) {
	// Block mempool submission and verify creator is the block proposer.
	if err := ms.k.ValidateProposerIsCreator(goCtx, msg.Creator, "MsgAckExecutiveAuthorityKey"); err != nil {
		return nil, err
	}

	ctx := sdk.UnwrapSDKContext(goCtx)
	kvStore := ms.k.OpenKVStore(ctx)

	round, err := ms.k.GetPendingRoundWithCeremony(kvStore, msg.VoteRoundId, types.CeremonyStatus_CEREMONY_STATUS_DEALT)
	if err != nil {
		return nil, err
	}

	// Validate creator is a registered validator.
	if _, found := FindValidatorInRoundCeremony(round, msg.Creator); !found {
		return nil, fmt.Errorf("%w: %s", types.ErrNotRegisteredValidator, msg.Creator)
	}

	// Reject duplicate ack.
	if _, found := FindAckInRoundCeremony(round, msg.Creator); found {
		return nil, fmt.Errorf("%w: %s", types.ErrDuplicateAck, msg.Creator)
	}

	// Verify ack_signature = SHA256("ack" || ea_pk || validator_address).
	expectedSig := sha256AckSig(round.EaPk, msg.Creator)
	if !bytes.Equal(msg.AckSignature, expectedSig) {
		return nil, fmt.Errorf("%w: ack_signature mismatch", types.ErrInvalidField)
	}

	// Record ack.
	round.CeremonyAcks = append(round.CeremonyAcks, &types.AckEntry{
		ValidatorAddress: msg.Creator,
		AckSignature:     msg.AckSignature,
		AckHeight:        uint64(ctx.BlockHeight()),
	})

	AppendCeremonyLog(round, uint64(ctx.BlockHeight()),
		fmt.Sprintf("ack from %s (%d/%d acked)", msg.Creator, len(round.CeremonyAcks), len(round.CeremonyValidators)))

	// Fast path: confirm only when ALL validators have acked. This gives
	// every validator a chance to ack via PrepareProposal before the ceremony
	// closes. If some validators are offline, the timeout path in EndBlocker
	// handles confirmation with >= 1/2 acks and strips non-ackers.
	if len(round.CeremonyAcks) == len(round.CeremonyValidators) {
		round.CeremonyStatus = types.CeremonyStatus_CEREMONY_STATUS_CONFIRMED
		round.Status = types.SessionStatus_SESSION_STATUS_ACTIVE
		AppendCeremonyLog(round, uint64(ctx.BlockHeight()),
			fmt.Sprintf("ceremony confirmed (%d/%d acked), round ACTIVE", len(round.CeremonyAcks), len(round.CeremonyValidators)))
	}

	if err := ms.k.SetVoteRound(kvStore, round); err != nil {
		return nil, err
	}

	ctx.EventManager().EmitEvent(sdk.NewEvent(
		types.EventTypeAckExecutiveAuthorityKey,
		sdk.NewAttribute(types.AttributeKeyRoundID, hex.EncodeToString(msg.VoteRoundId)),
		sdk.NewAttribute(types.AttributeKeyValidatorAddress, msg.Creator),
		sdk.NewAttribute(types.AttributeKeyCeremonyStatus, round.CeremonyStatus.String()),
	))

	return &types.MsgAckExecutiveAuthorityKeyResponse{}, nil
}

// CreateValidatorWithPallasKey handles MsgCreateValidatorWithPallasKey.
// It atomically creates a validator via the staking module and registers
// the validator's Pallas public key in the global registry. This replaces
// the two-step flow of MsgCreateValidator + MsgRegisterPallasKey for
// post-genesis validators.
func (ms msgServer) CreateValidatorWithPallasKey(goCtx context.Context, msg *types.MsgCreateValidatorWithPallasKey) (*types.MsgCreateValidatorWithPallasKeyResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)
	kvStore := ms.k.OpenKVStore(ctx)

	// Decode the embedded staking MsgCreateValidator (gogoproto binary format).
	stakingMsg := &stakingtypes.MsgCreateValidator{}
	if err := stakingMsg.Unmarshal(msg.StakingMsg); err != nil {
		return nil, fmt.Errorf("failed to decode staking_msg: %w", err)
	}

	// Unpack the Any-wrapped consensus pubkey so the staking module can
	// access it via GetCachedValue(). Without this, the pubkey field is
	// raw bytes and staking's CreateValidator fails with "got <nil>".
	if stakingMsg.Pubkey != nil {
		registry := codectypes.NewInterfaceRegistry()
		cryptocodec.RegisterInterfaces(registry)
		if err := stakingMsg.UnpackInterfaces(registry); err != nil {
			return nil, fmt.Errorf("failed to unpack staking_msg pubkey: %w", err)
		}
	}

	// Validate pallas_pk: 32 bytes, valid Pallas point, not identity.
	if _, err := elgamal.UnmarshalPublicKey(msg.PallasPk); err != nil {
		return nil, fmt.Errorf("%w: %v", types.ErrInvalidPallasPoint, err)
	}

	// Call through to the staking module's MsgServer to create the validator.
	// The stakingKeeper is injected as the concrete *stakingkeeper.Keeper via depinject.
	concreteKeeper, ok := ms.k.stakingKeeper.(*stakingkeeper.Keeper)
	if !ok {
		return nil, fmt.Errorf("staking keeper is not *stakingkeeper.Keeper (got %T); cannot create validator", ms.k.stakingKeeper)
	}
	stakingMsgServer := stakingkeeper.NewMsgServerImpl(concreteKeeper)
	if _, err := stakingMsgServer.CreateValidator(goCtx, stakingMsg); err != nil {
		return nil, fmt.Errorf("staking CreateValidator failed: %w", err)
	}

	validatorAddr := stakingMsg.ValidatorAddress

	if err := ms.k.RegisterPallasKeyCore(kvStore, validatorAddr, msg.PallasPk); err != nil {
		return nil, err
	}

	ctx.EventManager().EmitEvent(sdk.NewEvent(
		types.EventTypeRegisterPallasKey,
		sdk.NewAttribute(types.AttributeKeyValidatorAddress, validatorAddr),
	))

	return &types.MsgCreateValidatorWithPallasKeyResponse{}, nil
}

// sha256AckSig computes SHA256(AckSigDomain || eaPk || validatorAddress).
func sha256AckSig(eaPk []byte, validatorAddress string) []byte {
	h := sha256.New()
	h.Write([]byte(types.AckSigDomain))
	h.Write(eaPk)
	h.Write([]byte(validatorAddress))
	return h.Sum(nil)
}
