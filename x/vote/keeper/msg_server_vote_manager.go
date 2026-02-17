package keeper

import (
	"context"
	"fmt"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/z-cale/zally/x/vote/types"
)

// SetVoteManager handles MsgSetVoteManager.
// Sets or changes the vote manager address. Only callable by the current
// vote manager or any bonded validator. On bootstrap (no vote manager set),
// accepts any bonded validator.
func (ms msgServer) SetVoteManager(goCtx context.Context, msg *types.MsgSetVoteManager) (*types.MsgSetVoteManagerResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	if msg.NewManager == "" {
		return nil, fmt.Errorf("%w: new_manager cannot be empty", types.ErrInvalidField)
	}

	// Authorization: current vote manager or any validator.
	if err := ms.k.ValidateVoteManagerOrValidator(goCtx, msg.Creator); err != nil {
		return nil, err
	}

	kvStore := ms.k.OpenKVStore(ctx)
	if err := ms.k.SetVoteManager(kvStore, &types.VoteManagerState{
		Address: msg.NewManager,
	}); err != nil {
		return nil, err
	}

	ctx.EventManager().EmitEvent(sdk.NewEvent(
		types.EventTypeSetVoteManager,
		sdk.NewAttribute(types.AttributeKeyVoteManager, msg.NewManager),
		sdk.NewAttribute(types.AttributeKeyCreator, msg.Creator),
	))

	return &types.MsgSetVoteManagerResponse{}, nil
}
