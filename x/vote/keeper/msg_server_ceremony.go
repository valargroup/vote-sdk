package keeper

import (
	"context"
	"encoding/hex"
	"fmt"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/z-cale/zally/crypto/elgamal"
	"github.com/z-cale/zally/x/vote/types"
)

// RegisterPallasKey handles MsgRegisterPallasKey.
// On first call (state nil or INITIALIZING), transitions to REGISTERING and
// starts the registration phase timer. Then appends the validator's Pallas
// public key to the ceremony's validator list.
func (ms msgServer) RegisterPallasKey(goCtx context.Context, msg *types.MsgRegisterPallasKey) (*types.MsgRegisterPallasKeyResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)
	kvStore := ms.k.OpenKVStore(ctx)

	state, err := ms.k.GetCeremonyState(kvStore)
	if err != nil {
		return nil, err
	}

	// First registration: create ceremony and transition to REGISTERING.
	if state == nil || state.Status == types.CeremonyStatus_CEREMONY_STATUS_INITIALIZING {
		state = &types.CeremonyState{
			Status:       types.CeremonyStatus_CEREMONY_STATUS_REGISTERING,
			PhaseStart:   uint64(ctx.BlockTime().Unix()),
			PhaseTimeout: types.DefaultRegistrationTimeout,
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

	// Reject duplicate registration.
	if _, found := FindValidatorInCeremony(state, msg.Creator); found {
		return nil, fmt.Errorf("%w: %s", types.ErrDuplicateRegistration, msg.Creator)
	}

	// Append validator key.
	state.Validators = append(state.Validators, &types.ValidatorPallasKey{
		ValidatorAddress: msg.Creator,
		PallasPk:         msg.PallasPk,
	})

	if err := ms.k.SetCeremonyState(kvStore, state); err != nil {
		return nil, err
	}

	ctx.EventManager().EmitEvent(sdk.NewEvent(
		types.EventTypeRegisterPallasKey,
		sdk.NewAttribute(types.AttributeKeyValidatorAddress, msg.Creator),
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
