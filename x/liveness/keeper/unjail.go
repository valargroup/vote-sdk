package keeper

import (
	"context"

	"cosmossdk.io/errors"

	sdk "github.com/cosmos/cosmos-sdk/types"
	slashingtypes "github.com/cosmos/cosmos-sdk/x/slashing/types"
)

func (k Keeper) Unjail(ctx context.Context, validatorAddr sdk.ValAddress) error {
	validator, err := k.sk.Validator(ctx, validatorAddr)
	if err != nil {
		return err
	}
	if validator == nil {
		return slashingtypes.ErrNoValidatorForAddress
	}

	selfDel, err := k.sk.Delegation(ctx, sdk.AccAddress(validatorAddr), validatorAddr)
	if err != nil {
		return err
	}
	if selfDel == nil {
		return slashingtypes.ErrMissingSelfDelegation
	}

	tokens := validator.TokensFromShares(selfDel.GetShares()).TruncateInt()
	minSelfBond := validator.GetMinSelfDelegation()
	if tokens.LT(minSelfBond) {
		return errors.Wrapf(slashingtypes.ErrSelfDelegationTooLowToUnjail, "%s less than %s", tokens, minSelfBond)
	}

	if !validator.IsJailed() {
		return slashingtypes.ErrValidatorNotJailed
	}

	consAddr, err := validator.GetConsAddr()
	if err != nil {
		return err
	}
	info, err := k.GetValidatorSigningInfo(ctx, consAddr)
	if err == nil {
		if info.Tombstoned {
			return slashingtypes.ErrValidatorJailed
		}
		sdkCtx := sdk.UnwrapSDKContext(ctx)
		if sdkCtx.BlockHeader().Time.Before(info.JailedUntil) {
			return slashingtypes.ErrValidatorJailed
		}
		info.StartHeight = sdkCtx.BlockHeight()
		info.MissedBlocksCounter = 0
		info.IndexOffset = 0
		if err := k.DeleteMissedBlocks(ctx, consAddr); err != nil {
			return err
		}
		if err := k.SetValidatorSigningInfo(ctx, consAddr, info); err != nil {
			return err
		}
	}

	return k.sk.Unjail(ctx, consAddr)
}
