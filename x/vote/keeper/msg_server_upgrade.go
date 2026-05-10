package keeper

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	upgradetypes "cosmossdk.io/x/upgrade/types"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/valargroup/vote-sdk/x/vote/types"
)

func (ms msgServer) executeScheduleUpgrade(goCtx context.Context, msg *types.MsgScheduleUpgrade) (*types.MsgScheduleUpgradeResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	if err := msg.ValidateBasic(); err != nil {
		return nil, err
	}
	if ms.k.upgradeKeeper == nil {
		return nil, fmt.Errorf("%w", types.ErrUpgradeUnavailable)
	}

	if !msg.ReplaceExisting {
		existing, err := ms.k.upgradeKeeper.GetUpgradePlan(goCtx)
		if err == nil {
			return nil, fmt.Errorf("%w: %q at height %d", types.ErrUpgradePlanExists, existing.Name, existing.Height)
		}
		if !errors.Is(err, upgradetypes.ErrNoUpgradePlanFound) {
			return nil, err
		}
	}

	plan := upgradetypes.Plan{
		Name:   strings.TrimSpace(msg.Name),
		Height: msg.Height,
		Info:   msg.Info,
	}
	if err := ms.k.upgradeKeeper.ScheduleUpgrade(goCtx, plan); err != nil {
		return nil, err
	}

	ctx.EventManager().EmitEvent(sdk.NewEvent(
		types.EventTypeScheduleUpgrade,
		sdk.NewAttribute(types.AttributeKeyCreator, msg.Creator),
		sdk.NewAttribute(types.AttributeKeyUpgradeName, plan.Name),
		sdk.NewAttribute(types.AttributeKeyBlockHeight, strconv.FormatInt(plan.Height, 10)),
		sdk.NewAttribute(types.AttributeKeyReplaceExisting, strconv.FormatBool(msg.ReplaceExisting)),
		sdk.NewAttribute(types.AttributeKeyUpgradeInfo, plan.Info),
	))

	return &types.MsgScheduleUpgradeResponse{}, nil
}

// executeCancelUpgrade clears the currently scheduled x/upgrade plan.
// x/upgrade treats cancelling with no plan as a no-op, and this handler
// preserves that behavior.
func (ms msgServer) executeCancelUpgrade(goCtx context.Context, msg *types.MsgCancelUpgrade) (*types.MsgCancelUpgradeResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	if err := msg.ValidateBasic(); err != nil {
		return nil, err
	}
	if ms.k.upgradeKeeper == nil {
		return nil, fmt.Errorf("%w", types.ErrUpgradeUnavailable)
	}

	if err := ms.k.upgradeKeeper.ClearUpgradePlan(goCtx); err != nil {
		return nil, err
	}

	ctx.EventManager().EmitEvent(sdk.NewEvent(
		types.EventTypeCancelUpgrade,
		sdk.NewAttribute(types.AttributeKeyCreator, msg.Creator),
	))

	return &types.MsgCancelUpgradeResponse{}, nil
}
