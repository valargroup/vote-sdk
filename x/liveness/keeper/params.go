package keeper

import (
	"context"
	"time"

	sdkmath "cosmossdk.io/math"

	slashingtypes "github.com/cosmos/cosmos-sdk/x/slashing/types"
	"github.com/valargroup/vote-sdk/x/liveness/types"
)

func (k Keeper) SignedBlocksWindow(ctx context.Context) (int64, error) {
	params, err := k.GetParams(ctx)
	return params.SignedBlocksWindow, err
}

func (k Keeper) MinSignedPerWindow(ctx context.Context) (int64, error) {
	params, err := k.GetParams(ctx)
	if err != nil {
		return 0, err
	}
	return params.MinSignedPerWindow.MulInt64(params.SignedBlocksWindow).RoundInt64(), nil
}

func (k Keeper) DowntimeJailDuration(ctx context.Context) (time.Duration, error) {
	params, err := k.GetParams(ctx)
	return params.DowntimeJailDuration, err
}

func (k Keeper) SlashFractionDowntime(ctx context.Context) (sdkmath.LegacyDec, error) {
	params, err := k.GetParams(ctx)
	return params.SlashFractionDowntime, err
}

func (k Keeper) GetParams(ctx context.Context) (params slashingtypes.Params, err error) {
	store := k.storeService.OpenKVStore(ctx)
	bz, err := store.Get(types.ParamsKey)
	if err != nil {
		return params, err
	}
	if bz == nil {
		return slashingtypes.DefaultParams(), nil
	}

	err = k.cdc.Unmarshal(bz, &params)
	return params, err
}

func (k Keeper) SetParams(ctx context.Context, params slashingtypes.Params) error {
	store := k.storeService.OpenKVStore(ctx)
	bz, err := k.cdc.Marshal(&params)
	if err != nil {
		return err
	}
	return store.Set(types.ParamsKey, bz)
}
