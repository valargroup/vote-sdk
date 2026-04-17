package keeper

import (
	"context"
	"fmt"

	sdkmath "cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/valargroup/vote-sdk/x/vote/types"
)

// AuthorizedSend handles MsgAuthorizedSend — the only coin-transfer path on
// this chain. Bank MsgSend/MsgMultiSend are blocked at the ante handler because
// unrestricted transfers would allow anyone to accumulate stake and create a
// validator, undermining the controlled validator set.
//
// Authorization rules (admin membership = any-of-N):
//   - Any admin can send to anyone (used to distribute stake to new validators).
//   - Bonded validators can send to any admin or to other bonded validators
//     (allows operational redistribution within the trusted set).
//   - All other senders are rejected.
func (ms msgServer) AuthorizedSend(goCtx context.Context, msg *types.MsgAuthorizedSend) (*types.MsgAuthorizedSendResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	fromAddr, err := sdk.AccAddressFromBech32(msg.FromAddress)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid from_address: %v", types.ErrInvalidField, err)
	}
	toAddr, err := sdk.AccAddressFromBech32(msg.ToAddress)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid to_address: %v", types.ErrInvalidField, err)
	}

	amt, ok := sdkmath.NewIntFromString(msg.Amount)
	if !ok || !amt.IsPositive() {
		return nil, fmt.Errorf("%w: amount must be a positive integer string", types.ErrInvalidField)
	}
	if msg.Denom == "" {
		return nil, fmt.Errorf("%w: denom cannot be empty", types.ErrInvalidField)
	}

	coins := sdk.NewCoins(sdk.NewCoin(msg.Denom, amt))

	// Fetch the admin set once and do both membership checks in-memory.
	adminSet, err := ms.k.GetAdmins(ms.k.OpenKVStore(ctx))
	if err != nil {
		return nil, err
	}
	isAdmin := func(addr string) bool {
		if adminSet == nil {
			return false
		}
		for _, a := range adminSet.Addresses {
			if a == addr {
				return true
			}
		}
		return false
	}

	if !isAdmin(msg.FromAddress) {
		senderValAddr := sdk.ValAddress(fromAddr).String()
		if !ms.k.IsValidator(ctx, senderValAddr) {
			return nil, fmt.Errorf("%w: %s is neither an admin nor a bonded validator",
				types.ErrUnauthorizedSend, msg.FromAddress)
		}

		recipientValAddr := sdk.ValAddress(toAddr).String()
		if !isAdmin(msg.ToAddress) && !ms.k.IsValidator(ctx, recipientValAddr) {
			return nil, fmt.Errorf("%w: validator %s can only send to an admin or another bonded validator",
				types.ErrUnauthorizedSend, msg.FromAddress)
		}
	}

	if err := ms.k.bankKeeper.SendCoins(ctx, fromAddr, toAddr, coins); err != nil {
		return nil, fmt.Errorf("send failed: %w", err)
	}

	ctx.EventManager().EmitEvent(sdk.NewEvent(
		types.EventTypeAuthorizedSend,
		sdk.NewAttribute(types.AttributeKeySender, msg.FromAddress),
		sdk.NewAttribute(types.AttributeKeyRecipient, msg.ToAddress),
		sdk.NewAttribute(types.AttributeKeyAmount, coins.String()),
	))

	return &types.MsgAuthorizedSendResponse{}, nil
}
