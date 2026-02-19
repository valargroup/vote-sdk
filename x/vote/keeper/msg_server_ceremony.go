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
// On first call (state nil or REGISTERING with no validators), creates the
// ceremony state. Then appends the validator's Pallas public key to the
// ceremony's validator list. REGISTERING has no timeout — it persists until
// a deal is submitted or the ceremony is re-initialized.
func (ms msgServer) RegisterPallasKey(goCtx context.Context, msg *types.MsgRegisterPallasKey) (*types.MsgRegisterPallasKeyResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)
	kvStore := ms.k.OpenKVStore(ctx)

	state, err := ms.k.GetCeremonyState(kvStore)
	if err != nil {
		return nil, err
	}

	// First registration or post-reset: create ceremony state.
	if state == nil || (state.Status == types.CeremonyStatus_CEREMONY_STATUS_REGISTERING && len(state.Validators) == 0) {
		state = &types.CeremonyState{
			Status: types.CeremonyStatus_CEREMONY_STATUS_REGISTERING,
		}
	}

	// Only accept registrations while REGISTERING.
	if state.Status != types.CeremonyStatus_CEREMONY_STATUS_REGISTERING {
		return nil, fmt.Errorf("%w: ceremony is %s", types.ErrCeremonyWrongStatus, state.Status)
	}

	// Validate pallas_pk: 32 bytes, valid Pallas point, not identity.
	if _, err := elgamal.UnmarshalPublicKey(msg.PallasPk); err != nil {
		return nil, fmt.Errorf("%w: %v", types.ErrInvalidPallasPoint, err)
	}

	// Derive the validator operator address from the sender's account address.
	// PrepareProposal identifies the proposer by val.OperatorAddress (valoper
	// bech32), so the ceremony state must use the same format for lookups.
	accAddr, err := sdk.AccAddressFromBech32(msg.Creator)
	if err != nil {
		return nil, fmt.Errorf("invalid creator address %q: %w", msg.Creator, err)
	}
	valAddr := sdk.ValAddress(accAddr).String()

	// Reject duplicate registration.
	if _, found := FindValidatorInCeremony(state, valAddr); found {
		return nil, fmt.Errorf("%w: %s", types.ErrDuplicateRegistration, valAddr)
	}

	// Append validator key.
	state.Validators = append(state.Validators, &types.ValidatorPallasKey{
		ValidatorAddress: valAddr,
		PallasPk:         msg.PallasPk,
	})

	if err := ms.k.SetCeremonyState(kvStore, state); err != nil {
		return nil, err
	}

	ctx.EventManager().EmitEvent(sdk.NewEvent(
		types.EventTypeRegisterPallasKey,
		sdk.NewAttribute(types.AttributeKeyValidatorAddress, valAddr),
		sdk.NewAttribute(types.AttributeKeyCeremonyStatus, state.Status.String()),
	))

	return &types.MsgRegisterPallasKeyResponse{}, nil
}

// DealExecutiveAuthorityKey handles MsgDealExecutiveAuthorityKey.
// The dealer distributes encrypted ea_sk shares to all registered validators
// and publishes the ea_pk. Ceremony transitions REGISTERING -> DEALT.
func (ms msgServer) DealExecutiveAuthorityKey(goCtx context.Context, msg *types.MsgDealExecutiveAuthorityKey) (*types.MsgDealExecutiveAuthorityKeyResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)
	kvStore := ms.k.OpenKVStore(ctx)

	state, err := ms.k.GetCeremonyState(kvStore)
	if err != nil {
		return nil, err
	}
	if state == nil {
		return nil, fmt.Errorf("%w: no ceremony exists", types.ErrCeremonyWrongStatus)
	}

	// Only accept deals while REGISTERING.
	if state.Status != types.CeremonyStatus_CEREMONY_STATUS_REGISTERING {
		return nil, fmt.Errorf("%w: ceremony is %s", types.ErrCeremonyWrongStatus, state.Status)
	}

	// Need at least one registered validator.
	if len(state.Validators) == 0 {
		return nil, fmt.Errorf("%w: no validators registered", types.ErrCeremonyWrongStatus)
	}

	// Validate ea_pk is a valid Pallas point.
	if _, err := elgamal.UnmarshalPublicKey(msg.EaPk); err != nil {
		return nil, fmt.Errorf("%w: ea_pk: %v", types.ErrInvalidPallasPoint, err)
	}

	// Validate payload count matches validator count.
	if len(msg.Payloads) != len(state.Validators) {
		return nil, fmt.Errorf("%w: got %d payloads, expected %d",
			types.ErrPayloadMismatch, len(msg.Payloads), len(state.Validators))
	}

	// Validate each payload maps 1:1 to a registered validator.
	covered := make(map[string]bool, len(state.Validators))
	for _, p := range msg.Payloads {
		if _, found := FindValidatorInCeremony(state, p.ValidatorAddress); !found {
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

	// Store deal data and transition to DEALT.
	state.EaPk = msg.EaPk
	state.Payloads = msg.Payloads
	state.Dealer = msg.Creator
	state.PhaseStart = uint64(ctx.BlockTime().Unix())
	state.PhaseTimeout = types.DefaultDealTimeout
	state.Status = types.CeremonyStatus_CEREMONY_STATUS_DEALT

	if err := ms.k.SetCeremonyState(kvStore, state); err != nil {
		return nil, err
	}

	ctx.EventManager().EmitEvent(sdk.NewEvent(
		types.EventTypeDealExecutiveAuthorityKey,
		sdk.NewAttribute(types.AttributeKeyValidatorAddress, msg.Creator),
		sdk.NewAttribute(types.AttributeKeyEAPK, hex.EncodeToString(msg.EaPk)),
		sdk.NewAttribute(types.AttributeKeyCeremonyStatus, state.Status.String()),
	))

	return &types.MsgDealExecutiveAuthorityKeyResponse{}, nil
}

// AckExecutiveAuthorityKey handles MsgAckExecutiveAuthorityKey.
// A registered validator acknowledges receipt of their ea_sk share.
// When all validators have acked, ceremony transitions DEALT -> CONFIRMED.
//
// This message can only be injected by the block proposer via PrepareProposal;
// direct submission through the mempool is rejected by ValidateAckSubmitter.
func (ms msgServer) AckExecutiveAuthorityKey(goCtx context.Context, msg *types.MsgAckExecutiveAuthorityKey) (*types.MsgAckExecutiveAuthorityKeyResponse, error) {
	// Block mempool submission — acks must arrive via PrepareProposal only.
	if err := ms.k.ValidateAckSubmitter(goCtx); err != nil {
		return nil, err
	}

	ctx := sdk.UnwrapSDKContext(goCtx)
	kvStore := ms.k.OpenKVStore(ctx)

	state, err := ms.k.GetCeremonyState(kvStore)
	if err != nil {
		return nil, err
	}
	if state == nil {
		return nil, fmt.Errorf("%w: no ceremony exists", types.ErrCeremonyWrongStatus)
	}

	// Only accept acks while DEALT.
	if state.Status != types.CeremonyStatus_CEREMONY_STATUS_DEALT {
		return nil, fmt.Errorf("%w: ceremony is %s", types.ErrCeremonyWrongStatus, state.Status)
	}

	// Validate creator is a registered validator.
	if _, found := FindValidatorInCeremony(state, msg.Creator); !found {
		return nil, fmt.Errorf("%w: %s", types.ErrNotRegisteredValidator, msg.Creator)
	}

	// Reject duplicate ack.
	if _, found := FindAckForValidator(state, msg.Creator); found {
		return nil, fmt.Errorf("%w: %s", types.ErrDuplicateAck, msg.Creator)
	}

	// Record ack.
	state.Acks = append(state.Acks, &types.AckEntry{
		ValidatorAddress: msg.Creator,
		AckSignature:     msg.AckSignature,
		AckHeight:        uint64(ctx.BlockHeight()),
	})

	// Check if all validators have acked -> transition to CONFIRMED.
	if AllValidatorsAcked(state) {
		state.Status = types.CeremonyStatus_CEREMONY_STATUS_CONFIRMED
	}

	if err := ms.k.SetCeremonyState(kvStore, state); err != nil {
		return nil, err
	}

	ctx.EventManager().EmitEvent(sdk.NewEvent(
		types.EventTypeAckExecutiveAuthorityKey,
		sdk.NewAttribute(types.AttributeKeyValidatorAddress, msg.Creator),
		sdk.NewAttribute(types.AttributeKeyCeremonyStatus, state.Status.String()),
	))

	return &types.MsgAckExecutiveAuthorityKeyResponse{}, nil
}

// ReInitializeElectionAuthority handles MsgReInitializeElectionAuthority.
// Resets the ceremony state back to REGISTERING so a new key ceremony can
// begin. Only rejected during DEALT (awaiting acks) or when a voting session
// is active or tallying.
func (ms msgServer) ReInitializeElectionAuthority(goCtx context.Context, msg *types.MsgReInitializeElectionAuthority) (*types.MsgReInitializeElectionAuthorityResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)
	kvStore := ms.k.OpenKVStore(ctx)

	state, err := ms.k.GetCeremonyState(kvStore)
	if err != nil {
		return nil, err
	}

	// Reject if a ceremony session is in DEALT state (awaiting acks).
	// REGISTERING is allowed — reinit provides an escape hatch for stuck registrations.
	if state != nil && state.Status == types.CeremonyStatus_CEREMONY_STATUS_DEALT {
		return nil, fmt.Errorf("%w: ceremony is %s", types.ErrCeremonySessionActive, state.Status)
	}

	// Reject if any voting session is active or tallying — resetting the
	// ceremony would orphan in-flight sessions that depend on the current ea_pk.
	hasActive, err := ms.k.HasActiveOrTallyingRound(kvStore)
	if err != nil {
		return nil, err
	}
	if hasActive {
		return nil, fmt.Errorf("%w: cannot reset ceremony while voting sessions are in progress", types.ErrVotingSessionActive)
	}

	// Reset to empty REGISTERING state.
	state = &types.CeremonyState{
		Status: types.CeremonyStatus_CEREMONY_STATUS_REGISTERING,
	}

	if err := ms.k.SetCeremonyState(kvStore, state); err != nil {
		return nil, err
	}

	ctx.EventManager().EmitEvent(sdk.NewEvent(
		types.EventTypeReInitializeElectionAuthority,
		sdk.NewAttribute(types.AttributeKeyValidatorAddress, msg.Creator),
		sdk.NewAttribute(types.AttributeKeyCeremonyStatus, state.Status.String()),
	))

	return &types.MsgReInitializeElectionAuthorityResponse{}, nil
}

// CreateValidatorWithPallasKey handles MsgCreateValidatorWithPallasKey.
// It atomically creates a validator via the staking module and registers
// the validator's Pallas public key in the ceremony state. This replaces
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

	// Register the Pallas key in ceremony state (same logic as RegisterPallasKey).
	state, err := ms.k.GetCeremonyState(kvStore)
	if err != nil {
		return nil, err
	}

	// First registration or post-reset: create ceremony state.
	if state == nil || (state.Status == types.CeremonyStatus_CEREMONY_STATUS_REGISTERING && len(state.Validators) == 0) {
		state = &types.CeremonyState{
			Status: types.CeremonyStatus_CEREMONY_STATUS_REGISTERING,
		}
	}

	// Only accept registrations while REGISTERING.
	if state.Status != types.CeremonyStatus_CEREMONY_STATUS_REGISTERING {
		return nil, fmt.Errorf("%w: ceremony is %s", types.ErrCeremonyWrongStatus, state.Status)
	}

	// Use the validator operator address from the staking message as the key.
	validatorAddr := stakingMsg.ValidatorAddress

	// Reject duplicate registration.
	if _, found := FindValidatorInCeremony(state, validatorAddr); found {
		return nil, fmt.Errorf("%w: %s", types.ErrDuplicateRegistration, validatorAddr)
	}

	// Append validator key.
	state.Validators = append(state.Validators, &types.ValidatorPallasKey{
		ValidatorAddress: validatorAddr,
		PallasPk:         msg.PallasPk,
	})

	if err := ms.k.SetCeremonyState(kvStore, state); err != nil {
		return nil, err
	}

	ctx.EventManager().EmitEvent(sdk.NewEvent(
		types.EventTypeRegisterPallasKey,
		sdk.NewAttribute(types.AttributeKeyValidatorAddress, validatorAddr),
		sdk.NewAttribute(types.AttributeKeyCeremonyStatus, state.Status.String()),
	))

	return &types.MsgCreateValidatorWithPallasKeyResponse{}, nil
}

