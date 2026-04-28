package keeper

import (
	"context"
	"time"

	"cosmossdk.io/errors"
	storetypes "cosmossdk.io/store/types"

	sdk "github.com/cosmos/cosmos-sdk/types"
	slashingtypes "github.com/cosmos/cosmos-sdk/x/slashing/types"
	"github.com/valargroup/vote-sdk/x/liveness/types"
)

func (k Keeper) GetValidatorSigningInfo(ctx context.Context, address sdk.ConsAddress) (slashingtypes.ValidatorSigningInfo, error) {
	store := k.storeService.OpenKVStore(ctx)
	var info slashingtypes.ValidatorSigningInfo
	bz, err := store.Get(types.ValidatorSigningInfoKey(address))
	if err != nil {
		return info, err
	}
	if bz == nil {
		return info, slashingtypes.ErrNoSigningInfoFound
	}

	err = k.cdc.Unmarshal(bz, &info)
	return info, err
}

func (k Keeper) SetValidatorSigningInfo(ctx context.Context, address sdk.ConsAddress, info slashingtypes.ValidatorSigningInfo) error {
	store := k.storeService.OpenKVStore(ctx)
	bz, err := k.cdc.Marshal(&info)
	if err != nil {
		return err
	}
	return store.Set(types.ValidatorSigningInfoKey(address), bz)
}

func (k Keeper) IterateValidatorSigningInfos(ctx context.Context, handler func(address sdk.ConsAddress, info slashingtypes.ValidatorSigningInfo) (stop bool)) error {
	store := k.storeService.OpenKVStore(ctx)
	iter, err := store.Iterator(types.ValidatorSigningInfoKeyPrefix, storetypes.PrefixEndBytes(types.ValidatorSigningInfoKeyPrefix))
	if err != nil {
		return err
	}
	defer iter.Close()

	for ; iter.Valid(); iter.Next() {
		var info slashingtypes.ValidatorSigningInfo
		if err := k.cdc.Unmarshal(iter.Value(), &info); err != nil {
			return err
		}
		if handler(types.ValidatorSigningInfoAddress(iter.Key()), info) {
			break
		}
	}
	return nil
}

func (k Keeper) JailUntil(ctx context.Context, consAddr sdk.ConsAddress, jailTime time.Time) error {
	signInfo, err := k.GetValidatorSigningInfo(ctx, consAddr)
	if err != nil {
		return errors.Wrap(err, "cannot jail validator that does not have any signing information")
	}
	signInfo.JailedUntil = jailTime
	return k.SetValidatorSigningInfo(ctx, consAddr, signInfo)
}

func (k Keeper) HasMissedBlockAtHeight(ctx context.Context, addr sdk.ConsAddress, height int64) (bool, error) {
	store := k.storeService.OpenKVStore(ctx)
	bz, err := store.Get(types.MissByValidatorKey(addr, height))
	if err != nil {
		return false, err
	}
	return bz != nil, nil
}

func (k Keeper) SetMissedBlockAtHeight(ctx context.Context, addr sdk.ConsAddress, height int64) error {
	store := k.storeService.OpenKVStore(ctx)
	value := []byte{1}
	if err := store.Set(types.MissByValidatorKey(addr, height), value); err != nil {
		return err
	}
	return store.Set(types.MissByHeightKey(height, addr), value)
}

func (k Keeper) DeleteMissedBlockAtHeight(ctx context.Context, addr sdk.ConsAddress, height int64) error {
	store := k.storeService.OpenKVStore(ctx)
	if err := store.Delete(types.MissByValidatorKey(addr, height)); err != nil {
		return err
	}
	return store.Delete(types.MissByHeightKey(height, addr))
}

func (k Keeper) CountMissedBlocksInWindow(ctx context.Context, addr sdk.ConsAddress, startHeight, endHeight int64) (int64, error) {
	if endHeight < startHeight {
		return 0, nil
	}
	if startHeight < 0 {
		startHeight = 0
	}

	store := k.storeService.OpenKVStore(ctx)
	iter, err := store.Iterator(types.MissByValidatorKey(addr, startHeight), types.MissByValidatorKey(addr, endHeight+1))
	if err != nil {
		return 0, err
	}
	defer iter.Close()

	var count int64
	for ; iter.Valid(); iter.Next() {
		count++
	}
	return count, nil
}

func (k Keeper) PruneMissedBlocksBeforeHeight(ctx context.Context, beforeHeight int64) error {
	if beforeHeight <= 0 {
		return nil
	}

	store := k.storeService.OpenKVStore(ctx)
	iter, err := store.Iterator(types.MissByHeightKey(0, nil), types.MissByHeightKey(beforeHeight, nil))
	if err != nil {
		return err
	}
	defer iter.Close()

	type missedBlock struct {
		addr   sdk.ConsAddress
		height int64
	}
	var oldMisses []missedBlock
	for ; iter.Valid(); iter.Next() {
		oldMisses = append(oldMisses, missedBlock{
			addr:   types.MissAddressFromByHeightKey(iter.Key()),
			height: types.MissHeightFromByHeightKey(iter.Key()),
		})
	}

	for _, missed := range oldMisses {
		if err := k.DeleteMissedBlockAtHeight(ctx, missed.addr, missed.height); err != nil {
			return err
		}
	}
	return nil
}

func (k Keeper) DeleteMissedBlocks(ctx context.Context, addr sdk.ConsAddress) error {
	store := k.storeService.OpenKVStore(ctx)
	iter, err := store.Iterator(types.MissByValidatorPrefixKey(addr), storetypes.PrefixEndBytes(types.MissByValidatorPrefixKey(addr)))
	if err != nil {
		return err
	}
	defer iter.Close()

	var heights []int64
	for ; iter.Valid(); iter.Next() {
		heights = append(heights, types.MissHeightFromByValidatorKey(iter.Key()))
	}

	for _, height := range heights {
		if err := k.DeleteMissedBlockAtHeight(ctx, addr, height); err != nil {
			return err
		}
	}
	return nil
}

func (k Keeper) GetValidatorMissedBlocks(ctx context.Context, addr sdk.ConsAddress) ([]slashingtypes.MissedBlock, error) {
	store := k.storeService.OpenKVStore(ctx)
	iter, err := store.Iterator(types.MissByValidatorPrefixKey(addr), storetypes.PrefixEndBytes(types.MissByValidatorPrefixKey(addr)))
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	missedBlocks := make([]slashingtypes.MissedBlock, 0)
	for ; iter.Valid(); iter.Next() {
		missedBlocks = append(missedBlocks, slashingtypes.NewMissedBlock(types.MissHeightFromByValidatorKey(iter.Key()), true))
	}
	return missedBlocks, nil
}
