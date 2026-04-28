package keeper

import (
	"context"

	storetypes "cosmossdk.io/core/store"
	"cosmossdk.io/log"

	"github.com/cosmos/cosmos-sdk/codec"
	sdk "github.com/cosmos/cosmos-sdk/types"
	slashingtypes "github.com/cosmos/cosmos-sdk/x/slashing/types"
	"github.com/valargroup/vote-sdk/x/liveness/types"
)

type Keeper struct {
	storeService storetypes.KVStoreService
	cdc          codec.BinaryCodec
	sk           slashingtypes.StakingKeeper
	authority    string
}

func NewKeeper(cdc codec.BinaryCodec, storeService storetypes.KVStoreService, sk slashingtypes.StakingKeeper, authority string) Keeper {
	return Keeper{
		storeService: storeService,
		cdc:          cdc,
		sk:           sk,
		authority:    authority,
	}
}

func (k Keeper) Logger(ctx context.Context) log.Logger {
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	return sdkCtx.Logger().With("module", "x/"+types.ModuleName)
}

func (k Keeper) GetAuthority() string {
	return k.authority
}
