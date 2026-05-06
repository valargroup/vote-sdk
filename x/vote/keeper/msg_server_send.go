package keeper

import (
	"context"
	"fmt"

	sdkmath "cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/valargroup/vote-sdk/x/vote/types"
)

// AuthorizedSend handles MsgAuthorizedSend. Top-level external sends are not
// an authority path; the message is only executable as a coordinator action
// payload. Bank MsgSend/MsgMultiSend are blocked at the ante handler because
// unrestricted transfers would allow anyone to accumulate stake and create a
// validator, undermining the controlled validator set.
//
// Authorization rules:
//   - Direct top-level MsgAuthorizedSend is rejected.
//   - Coordinator-action sends must originate from a current coordinator.
//   - The source funding account must be one of the approving coordinators.
func (ms msgServer) AuthorizedSend(goCtx context.Context, msg *types.MsgAuthorizedSend) (*types.MsgAuthorizedSendResponse, error) {
	return ms.executeAuthorizedSend(goCtx, msg, false, nil)
}

func (ms msgServer) executeAuthorizedSend(goCtx context.Context, msg *types.MsgAuthorizedSend, allowCoordinatorSender bool, approvals []string) (*types.MsgAuthorizedSendResponse, error) {
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

	if !allowCoordinatorSender {
		return nil, fmt.Errorf("%w: sends require coordinator action approval", types.ErrCoordinatorActionRequired)
	}

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
