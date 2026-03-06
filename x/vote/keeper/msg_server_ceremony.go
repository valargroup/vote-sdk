package keeper

import (
	"context"
	"encoding/hex"
	"fmt"

	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	sdk "github.com/cosmos/cosmos-sdk/types"
	stakingkeeper "github.com/cosmos/cosmos-sdk/x/staking/keeper"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"

	"github.com/z-cale/zally/crypto/elgamal"
	"github.com/z-cale/zally/x/vote/types"
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

	// Reject duplicate registration.
	has, err := ms.k.HasPallasKey(kvStore, valAddr)
	if err != nil {
		return nil, err
	}
	if has {
		return nil, fmt.Errorf("%w: %s", types.ErrDuplicateRegistration, valAddr)
	}

	// Store in global registry.
	vpk := &types.ValidatorPallasKey{
		ValidatorAddress: valAddr,
		PallasPk:         msg.PallasPk,
	}
	if err := ms.k.SetPallasKey(kvStore, vpk); err != nil {
		return nil, err
	}

	ctx.EventManager().EmitEvent(sdk.NewEvent(
		types.EventTypeRegisterPallasKey,
		sdk.NewAttribute(types.AttributeKeyValidatorAddress, valAddr),
	))

	return &types.MsgRegisterPallasKeyResponse{}, nil
}

// DealExecutiveAuthorityKey handles MsgDealExecutiveAuthorityKey.
// The dealer distributes encrypted ea_sk shares to all validators in the
// round's ceremony. Per-round ceremony: REGISTERING -> DEALT.
//
// This message can only be injected by the block proposer via PrepareProposal;
// direct submission through the mempool is rejected by ValidateProposerIsCreator.
func (ms msgServer) DealExecutiveAuthorityKey(goCtx context.Context, msg *types.MsgDealExecutiveAuthorityKey) (*types.MsgDealExecutiveAuthorityKeyResponse, error) {
	// Block mempool submission and verify creator is the block proposer.
	if err := ms.k.ValidateProposerIsCreator(goCtx, msg.Creator, "MsgDealExecutiveAuthorityKey"); err != nil {
		return nil, err
	}

	ctx := sdk.UnwrapSDKContext(goCtx)
	kvStore := ms.k.OpenKVStore(ctx)

	round, err := ms.k.GetPendingRoundWithCeremony(kvStore, msg.VoteRoundId, types.CeremonyStatus_CEREMONY_STATUS_REGISTERING)
	if err != nil {
		return nil, err
	}

	// Need at least one registered validator.
	if len(round.CeremonyValidators) == 0 {
		return nil, fmt.Errorf("%w: no validators in round ceremony", types.ErrCeremonyWrongStatus)
	}

	// Validate creator is a registered ceremony validator.
	if _, found := FindValidatorInRoundCeremony(round, msg.Creator); !found {
		return nil, fmt.Errorf("%w: %s is not a ceremony validator", types.ErrNotRegisteredValidator, msg.Creator)
	}

	// Validate ea_pk is a valid Pallas point.
	if _, err := elgamal.UnmarshalPublicKey(msg.EaPk); err != nil {
		return nil, fmt.Errorf("%w: ea_pk: %v", types.ErrInvalidPallasPoint, err)
	}

	// Validate payload count matches validator count.
	if len(msg.Payloads) != len(round.CeremonyValidators) {
		return nil, fmt.Errorf("%w: got %d payloads, expected %d",
			types.ErrPayloadMismatch, len(msg.Payloads), len(round.CeremonyValidators))
	}

	// Validate each payload maps 1:1 to a round ceremony validator.
	covered := make(map[string]bool, len(round.CeremonyValidators))
	for _, p := range msg.Payloads {
		if _, found := FindValidatorInRoundCeremony(round, p.ValidatorAddress); !found {
			return nil, fmt.Errorf("%w: payload references unknown validator %s",
				types.ErrNotRegisteredValidator, p.ValidatorAddress)
		}
		if covered[p.ValidatorAddress] {
			return nil, fmt.Errorf("%w: duplicate payload for validator %s",
				types.ErrPayloadMismatch, p.ValidatorAddress)
		}
		covered[p.ValidatorAddress] = true

		// Validate ephemeral_pk is a valid Pallas point.
		if _, err := elgamal.UnmarshalPublicKey(p.EphemeralPk); err != nil {
			return nil, fmt.Errorf("%w: ephemeral_pk for %s: %v",
				types.ErrInvalidPallasPoint, p.ValidatorAddress, err)
		}
	}

	// Validate threshold and verification keys.
	nValidators := len(round.CeremonyValidators)
	if nValidators >= 2 {
		if msg.Threshold < 2 {
			return nil, fmt.Errorf("%w: threshold must be >= 2 when n=%d, got %d",
				types.ErrInvalidThreshold, nValidators, msg.Threshold)
		}
		if len(msg.VerificationKeys) != nValidators {
			return nil, fmt.Errorf("%w: expected %d verification keys, got %d",
				types.ErrInvalidThreshold, nValidators, len(msg.VerificationKeys))
		}
		for i, vk := range msg.VerificationKeys {
			if _, err := elgamal.UnmarshalPublicKey(vk); err != nil {
				return nil, fmt.Errorf("%w: verification_key[%d]: %v",
					types.ErrInvalidPallasPoint, i, err)
			}
		}
	} else {
		if msg.Threshold != 0 {
			return nil, fmt.Errorf("%w: threshold must be 0 when n=%d, got %d",
				types.ErrInvalidThreshold, nValidators, msg.Threshold)
		}
		if len(msg.VerificationKeys) != 0 {
			return nil, fmt.Errorf("%w: verification_keys must be empty when n=%d, got %d",
				types.ErrInvalidThreshold, nValidators, len(msg.VerificationKeys))
		}
	}

	// Store deal data on the round and transition ceremony to DEALT.
	round.EaPk = msg.EaPk
	round.CeremonyPayloads = msg.Payloads
	round.CeremonyDealer = msg.Creator
	round.CeremonyPhaseStart = uint64(ctx.BlockTime().Unix())
	round.CeremonyPhaseTimeout = types.DefaultDealTimeout
	round.CeremonyStatus = types.CeremonyStatus_CEREMONY_STATUS_DEALT
	round.Threshold = msg.Threshold
	round.VerificationKeys = msg.VerificationKeys

	AppendCeremonyLog(round, uint64(ctx.BlockHeight()),
		fmt.Sprintf("deal from %s, ea_pk=%s", msg.Creator, hex.EncodeToString(msg.EaPk)[:16]))

	if err := ms.k.SetVoteRound(kvStore, round); err != nil {
		return nil, err
	}

	ctx.EventManager().EmitEvent(sdk.NewEvent(
		types.EventTypeDealExecutiveAuthorityKey,
		sdk.NewAttribute(types.AttributeKeyRoundID, hex.EncodeToString(msg.VoteRoundId)),
		sdk.NewAttribute(types.AttributeKeyValidatorAddress, msg.Creator),
		sdk.NewAttribute(types.AttributeKeyEAPK, hex.EncodeToString(msg.EaPk)),
		sdk.NewAttribute(types.AttributeKeyCeremonyStatus, round.CeremonyStatus.String()),
	))

	return &types.MsgDealExecutiveAuthorityKeyResponse{}, nil
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

	// Use the validator operator address from the staking message as the key.
	validatorAddr := stakingMsg.ValidatorAddress

	// Reject duplicate registration.
	has, err := ms.k.HasPallasKey(kvStore, validatorAddr)
	if err != nil {
		return nil, err
	}
	if has {
		return nil, fmt.Errorf("%w: %s", types.ErrDuplicateRegistration, validatorAddr)
	}

	// Store in global registry.
	vpk := &types.ValidatorPallasKey{
		ValidatorAddress: validatorAddr,
		PallasPk:         msg.PallasPk,
	}
	if err := ms.k.SetPallasKey(kvStore, vpk); err != nil {
		return nil, err
	}

	ctx.EventManager().EmitEvent(sdk.NewEvent(
		types.EventTypeRegisterPallasKey,
		sdk.NewAttribute(types.AttributeKeyValidatorAddress, validatorAddr),
	))

	return &types.MsgCreateValidatorWithPallasKeyResponse{}, nil
}


