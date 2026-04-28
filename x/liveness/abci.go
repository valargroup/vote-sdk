package liveness

import (
	"context"

	"cosmossdk.io/core/comet"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/valargroup/vote-sdk/x/liveness/keeper"
)

func BeginBlocker(ctx context.Context, k keeper.Keeper) error {
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	for _, voteInfo := range sdkCtx.VoteInfos() {
		if err := k.HandleValidatorSignature(ctx, voteInfo.Validator.Address, voteInfo.Validator.Power, comet.BlockIDFlag(voteInfo.BlockIdFlag)); err != nil {
			return err
		}
	}
	return nil
}
