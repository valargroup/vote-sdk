package keeper

import (
	"context"
	"time"

	sdkmath "cosmossdk.io/math"

	sdk "github.com/cosmos/cosmos-sdk/types"
	slashingtypes "github.com/cosmos/cosmos-sdk/x/slashing/types"
	"github.com/valargroup/vote-sdk/x/liveness/types"
)

type Hooks struct {
	k Keeper
}

func (k Keeper) Hooks() Hooks {
	return Hooks{k}
}

func (h Hooks) AfterValidatorBonded(ctx context.Context, consAddr sdk.ConsAddress, _ sdk.ValAddress) error {
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	signingInfo, err := h.k.GetValidatorSigningInfo(ctx, consAddr)
	if err == nil {
		signingInfo.StartHeight = sdkCtx.BlockHeight()
		signingInfo.MissedBlocksCounter = 0
		signingInfo.IndexOffset = 0
	} else {
		signingInfo = slashingtypes.NewValidatorSigningInfo(
			consAddr,
			sdkCtx.BlockHeight(),
			0,
			time.Unix(0, 0),
			false,
			0,
		)
	}
	if err := h.k.DeleteMissedBlocks(ctx, consAddr); err != nil {
		return err
	}
	return h.k.SetValidatorSigningInfo(ctx, consAddr, signingInfo)
}

func (h Hooks) AfterValidatorRemoved(ctx context.Context, consAddr sdk.ConsAddress, _ sdk.ValAddress) error {
	if err := h.k.DeleteMissedBlocks(ctx, consAddr); err != nil {
		return err
	}
	store := h.k.storeService.OpenKVStore(ctx)
	return store.Delete(types.ValidatorSigningInfoKey(consAddr))
}

func (h Hooks) AfterValidatorCreated(_ context.Context, _ sdk.ValAddress) error {
	return nil
}

func (h Hooks) AfterValidatorBeginUnbonding(_ context.Context, _ sdk.ConsAddress, _ sdk.ValAddress) error {
	return nil
}

func (h Hooks) BeforeValidatorModified(_ context.Context, _ sdk.ValAddress) error {
	return nil
}

func (h Hooks) BeforeDelegationCreated(_ context.Context, _ sdk.AccAddress, _ sdk.ValAddress) error {
	return nil
}

func (h Hooks) BeforeDelegationSharesModified(_ context.Context, _ sdk.AccAddress, _ sdk.ValAddress) error {
	return nil
}

func (h Hooks) BeforeDelegationRemoved(_ context.Context, _ sdk.AccAddress, _ sdk.ValAddress) error {
	return nil
}

func (h Hooks) AfterDelegationModified(_ context.Context, _ sdk.AccAddress, _ sdk.ValAddress) error {
	return nil
}

func (h Hooks) BeforeValidatorSlashed(_ context.Context, _ sdk.ValAddress, _ sdkmath.LegacyDec) error {
	return nil
}

func (h Hooks) AfterUnbondingInitiated(_ context.Context, _ uint64) error {
	return nil
}
