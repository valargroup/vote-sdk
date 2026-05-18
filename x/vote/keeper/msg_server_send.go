package keeper

import (
	"context"
	"fmt"

	sdkmath "cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"

	"github.com/valargroup/vote-sdk/x/vote/types"
)

// validateAuthorizedSendFields checks the MsgAuthorizedSend payload fields
// before proposal storage and again before execution.
func validateAuthorizedSendFields(msg *types.MsgAuthorizedSend) (sdk.AccAddress, sdk.AccAddress, sdkmath.Int, error) {
	if err := msg.ValidateBasic(); err != nil {
		return nil, nil, sdkmath.Int{}, err
	}
	creatorAddr, err := sdk.AccAddressFromBech32(msg.Creator)
	if err != nil {
		return nil, nil, sdkmath.Int{}, fmt.Errorf("%w: creator %q is not a valid bech32 address: %v", types.ErrInvalidField, msg.Creator, err)
	}
	toAddr, err := sdk.AccAddressFromBech32(msg.ToAddress)
	if err != nil {
		return nil, nil, sdkmath.Int{}, fmt.Errorf("%w: invalid to_address: %v", types.ErrInvalidField, err)
	}

	amt, ok := sdkmath.NewIntFromString(msg.Amount)
	if !ok || !amt.IsPositive() {
		return nil, nil, sdkmath.Int{}, fmt.Errorf("%w: amount must be a positive integer string", types.ErrInvalidField)
	}
	return creatorAddr, toAddr, amt, nil
}

func (ms msgServer) executeAuthorizedSend(goCtx context.Context, msg *types.MsgAuthorizedSend) (*types.MsgAuthorizedSendResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	_, toAddr, amt, err := validateAuthorizedSendFields(msg)
	if err != nil {
		return nil, err
	}

	coins := sdk.NewCoins(sdk.NewCoin(sdk.DefaultBondDenom, amt))
	if err := ms.k.bankKeeper.SendCoinsFromModuleToAccount(ctx, types.VoteFundingModuleName, toAddr, coins); err != nil {
		return nil, fmt.Errorf("send from %s module failed: %w", types.VoteFundingModuleName, err)
	}

	moduleAddr := authtypes.NewModuleAddress(types.VoteFundingModuleName).String()

	ctx.EventManager().EmitEvent(sdk.NewEvent(
		types.EventTypeAuthorizedSend,
		sdk.NewAttribute(types.AttributeKeyCreator, msg.Creator),
		sdk.NewAttribute(types.AttributeKeySender, moduleAddr),
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
