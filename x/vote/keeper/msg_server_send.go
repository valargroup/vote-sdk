package keeper

import (
	"context"
	"fmt"

	sdkmath "cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/valargroup/vote-sdk/x/vote/types"
)

// validateAuthorizedSendFields checks the MsgAuthorizedSend payload fields
// before proposal storage and again before execution.
func validateAuthorizedSendFields(msg *types.MsgAuthorizedSend) (sdk.AccAddress, sdk.AccAddress, sdkmath.Int, error) {
	fromAddr, err := sdk.AccAddressFromBech32(msg.FromAddress)
	if err != nil {
		return nil, nil, sdkmath.Int{}, fmt.Errorf("%w: invalid from_address: %v", types.ErrInvalidField, err)
	}
	toAddr, err := sdk.AccAddressFromBech32(msg.ToAddress)
	if err != nil {
		return nil, nil, sdkmath.Int{}, fmt.Errorf("%w: invalid to_address: %v", types.ErrInvalidField, err)
	}

	amt, ok := sdkmath.NewIntFromString(msg.Amount)
	if !ok || !amt.IsPositive() {
		return nil, nil, sdkmath.Int{}, fmt.Errorf("%w: amount must be a positive integer string", types.ErrInvalidField)
	}
	if msg.Denom == "" {
		return nil, nil, sdkmath.Int{}, fmt.Errorf("%w: denom cannot be empty", types.ErrInvalidField)
	}
	if err := sdk.ValidateDenom(msg.Denom); err != nil {
		return nil, nil, sdkmath.Int{}, fmt.Errorf("%w: invalid denom: %v", types.ErrInvalidField, err)
	}
	return fromAddr, toAddr, amt, nil
}

func (ms msgServer) executeAuthorizedSend(goCtx context.Context, msg *types.MsgAuthorizedSend, approvals []string) (*types.MsgAuthorizedSendResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	fromAddr, toAddr, amt, err := validateAuthorizedSendFields(msg)
	if err != nil {
		return nil, err
	}

	coins := sdk.NewCoins(sdk.NewCoin(msg.Denom, amt))

	senderIsVoteManager, err := ms.k.IsVoteManager(ctx, msg.FromAddress)
	if err != nil {
		return nil, err
	}
	if !senderIsVoteManager {
		return nil, fmt.Errorf("%w: coordinator-funded sends must originate from a vote manager", types.ErrUnauthorizedSend)
	}
	if !addressInList(fromAddr.String(), approvals) {
		return nil, fmt.Errorf("%w: source funding account %s must approve the coordinator action", types.ErrNotAuthorized, fromAddr.String())
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

func addressInList(addr string, addrs []string) bool {
	for _, candidate := range addrs {
		if candidate == addr {
			return true
		}
	}
	return false
}
