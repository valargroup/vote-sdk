package keeper

import (
	"context"
	"fmt"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/z-cale/zally/crypto/elgamal"
	"github.com/z-cale/zally/x/vote/types"
)

// RegisterPallasKey handles MsgRegisterPallasKey.
// Creates a new ceremony in REGISTERING status on first call, then appends
// the validator's Pallas public key to the ceremony's validator list.
func (ms msgServer) RegisterPallasKey(goCtx context.Context, msg *types.MsgRegisterPallasKey) (*types.MsgRegisterPallasKeyResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)
	kvStore := ms.k.OpenKVStore(ctx)

	state, err := ms.k.GetCeremonyState(kvStore)
	if err != nil {
		return nil, err
	}

	// First registration: create ceremony in REGISTERING status.
	if state == nil {
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
