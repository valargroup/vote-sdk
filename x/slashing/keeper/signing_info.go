package keeper

import (
	"context"
	"time"

	"cosmossdk.io/errors"
	storetypes "cosmossdk.io/store/types"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/slashing/types"
)

// GetValidatorSigningInfo retruns the ValidatorSigningInfo for a specific validator
// ConsAddress. If not found it returns ErrNoSigningInfoFound, but other errors
// may be returned if there is an error reading from the store.
func (k Keeper) GetValidatorSigningInfo(ctx context.Context, address sdk.ConsAddress) (types.ValidatorSigningInfo, error) {
	store := k.storeService.OpenKVStore(ctx)
	var info types.ValidatorSigningInfo
	bz, err := store.Get(types.ValidatorSigningInfoKey(address))
	if err != nil {
		return info, err
	}

	if bz == nil {
		return info, types.ErrNoSigningInfoFound
	}

	err = k.cdc.Unmarshal(bz, &info)
	return info, err
}

// HasValidatorSigningInfo returns if a given validator has signing information
// persisted.
func (k Keeper) HasValidatorSigningInfo(ctx context.Context, consAddr sdk.ConsAddress) bool {
	_, err := k.GetValidatorSigningInfo(ctx, consAddr)
	return err == nil
}

// SetValidatorSigningInfo sets the validator signing info to a consensus address key
func (k Keeper) SetValidatorSigningInfo(ctx context.Context, address sdk.ConsAddress, info types.ValidatorSigningInfo) error {
	store := k.storeService.OpenKVStore(ctx)
	bz, err := k.cdc.Marshal(&info)
	if err != nil {
		return err
	}

	return store.Set(types.ValidatorSigningInfoKey(address), bz)
}

// IterateValidatorSigningInfos iterates over the stored ValidatorSigningInfo
func (k Keeper) IterateValidatorSigningInfos(ctx context.Context,
	handler func(address sdk.ConsAddress, info types.ValidatorSigningInfo) (stop bool),
) error {
	store := k.storeService.OpenKVStore(ctx)
	iter, err := store.Iterator(types.ValidatorSigningInfoKeyPrefix, storetypes.PrefixEndBytes(types.ValidatorSigningInfoKeyPrefix))
	if err != nil {
		return err
	}
	defer iter.Close()
	for ; iter.Valid(); iter.Next() {
		address := types.ValidatorSigningInfoAddress(iter.Key())
		var info types.ValidatorSigningInfo
		err = k.cdc.Unmarshal(iter.Value(), &info)
		if err != nil {
			return err
		}

		if handler(address, info) {
			break
		}
	}
	return nil
}

// JailUntil attempts to set a validator's JailedUntil attribute in its signing
// info. It will panic if the signing info does not exist for the validator.
func (k Keeper) JailUntil(ctx context.Context, consAddr sdk.ConsAddress, jailTime time.Time) error {
	signInfo, err := k.GetValidatorSigningInfo(ctx, consAddr)
	if err != nil {
		return errors.Wrap(err, "cannot jail validator that does not have any signing information")
	}

	signInfo.JailedUntil = jailTime
	return k.SetValidatorSigningInfo(ctx, consAddr, signInfo)
}

// Tombstone attempts to tombstone a validator. It will panic if signing info for
// the given validator does not exist.
func (k Keeper) Tombstone(ctx context.Context, consAddr sdk.ConsAddress) error {
	signInfo, err := k.GetValidatorSigningInfo(ctx, consAddr)
	if err != nil {
		return types.ErrNoSigningInfoFound.Wrap("cannot tombstone validator that does not have any signing information")
	}

	if signInfo.Tombstoned {
		return types.ErrValidatorTombstoned.Wrap("cannot tombstone validator that is already tombstoned")
	}

	signInfo.Tombstoned = true
	return k.SetValidatorSigningInfo(ctx, consAddr, signInfo)
}

// IsTombstoned returns if a given validator by consensus address is tombstoned.
func (k Keeper) IsTombstoned(ctx context.Context, consAddr sdk.ConsAddress) bool {
	signInfo, err := k.GetValidatorSigningInfo(ctx, consAddr)
	if err != nil {
		return false
	}

	return signInfo.Tombstoned
}

// HasMissedBlockAtHeight returns true when the validator missed the absolute
// block height. Sparse height markers replace the SDK bitmap write path so
// signed blocks do not dirty slashing state.
func (k Keeper) HasMissedBlockAtHeight(ctx context.Context, addr sdk.ConsAddress, height int64) (bool, error) {
	store := k.storeService.OpenKVStore(ctx)
	bz, err := store.Get(missedBlockByValidatorKey(addr, height))
	if err != nil {
		return false, err
	}

	return bz != nil, nil
}

// SetMissedBlockAtHeight records a missed signature at an absolute block height
// in both validator-order and height-order indexes.
func (k Keeper) SetMissedBlockAtHeight(ctx context.Context, addr sdk.ConsAddress, height int64) error {
	store := k.storeService.OpenKVStore(ctx)
	value := []byte{1}
	if err := store.Set(missedBlockByValidatorKey(addr, height), value); err != nil {
		return err
	}

	return store.Set(missedBlockByHeightKey(height, addr), value)
}

// DeleteMissedBlockAtHeight deletes a sparse missed signature marker from both
// indexes.
func (k Keeper) DeleteMissedBlockAtHeight(ctx context.Context, addr sdk.ConsAddress, height int64) error {
	store := k.storeService.OpenKVStore(ctx)
	if err := store.Delete(missedBlockByValidatorKey(addr, height)); err != nil {
		return err
	}

	return store.Delete(missedBlockByHeightKey(height, addr))
}

// CountMissedBlocksInWindow counts sparse missed signature markers for a
// validator in [startHeight, endHeight].
func (k Keeper) CountMissedBlocksInWindow(ctx context.Context, addr sdk.ConsAddress, startHeight, endHeight int64) (int64, error) {
	if endHeight < startHeight {
		return 0, nil
	}
	if startHeight < 0 {
		startHeight = 0
	}

	store := k.storeService.OpenKVStore(ctx)
	startKey := missedBlockByValidatorKey(addr, startHeight)
	endKey := missedBlockByValidatorKey(addr, endHeight+1)
	iter, err := store.Iterator(startKey, endKey)
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

// PruneMissedBlocksBeforeHeight deletes sparse missed signature markers older
// than beforeHeight. It is deterministic and only runs from the missed-block
// path, preserving zero slashing writes on fully signed blocks.
func (k Keeper) PruneMissedBlocksBeforeHeight(ctx context.Context, beforeHeight int64) error {
	if beforeHeight <= 0 {
		return nil
	}

	store := k.storeService.OpenKVStore(ctx)
	startKey := missedBlockByHeightKey(0, nil)
	endKey := missedBlockByHeightKey(beforeHeight, nil)
	iter, err := store.Iterator(startKey, endKey)
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
			addr:   missedBlockAddressFromByHeightKey(iter.Key()),
			height: missedBlockHeightFromByHeightKey(iter.Key()),
		})
	}

	for _, missed := range oldMisses {
		if err := k.DeleteMissedBlockAtHeight(ctx, missed.addr, missed.height); err != nil {
			return err
		}
	}

	return nil
}

// GetMissedBlockBitmapValue returns true if a validator missed signing the
// sparse height/index. The method name is kept for SDK API compatibility.
func (k Keeper) GetMissedBlockBitmapValue(ctx context.Context, addr sdk.ConsAddress, index int64) (bool, error) {
	return k.HasMissedBlockAtHeight(ctx, addr, index)
}

// SetMissedBlockBitmapValue records or deletes a sparse missed height. The
// method name is kept for SDK API compatibility.
func (k Keeper) SetMissedBlockBitmapValue(ctx context.Context, addr sdk.ConsAddress, index int64, missed bool) error {
	if missed {
		return k.SetMissedBlockAtHeight(ctx, addr, index)
	}

	return k.DeleteMissedBlockAtHeight(ctx, addr, index)
}

// DeleteMissedBlockBitmap removes a validator's sparse missed block markers
// from state. The method name is kept for SDK API compatibility.
func (k Keeper) DeleteMissedBlockBitmap(ctx context.Context, addr sdk.ConsAddress) error {
	store := k.storeService.OpenKVStore(ctx)
	prefix := missedBlockByValidatorPrefixKey(addr)
	iter, err := store.Iterator(prefix, storetypes.PrefixEndBytes(prefix))
	if err != nil {
		return err
	}
	defer iter.Close()

	var heights []int64
	for ; iter.Valid(); iter.Next() {
		heights = append(heights, missedBlockHeightFromByValidatorKey(iter.Key()))
	}

	for _, height := range heights {
		if err := k.DeleteMissedBlockAtHeight(ctx, addr, height); err != nil {
			return err
		}
	}
	return nil
}

// IterateMissedBlockBitmap iterates over sparse missed heights for a validator.
// The method name is kept for SDK API compatibility.
func (k Keeper) IterateMissedBlockBitmap(ctx context.Context, addr sdk.ConsAddress, cb func(index int64, missed bool) (stop bool)) error {
	store := k.storeService.OpenKVStore(ctx)
	prefix := missedBlockByValidatorPrefixKey(addr)
	iter, err := store.Iterator(prefix, storetypes.PrefixEndBytes(prefix))
	if err != nil {
		return err
	}
	defer iter.Close()

	for ; iter.Valid(); iter.Next() {
		if cb(missedBlockHeightFromByValidatorKey(iter.Key()), true) {
			break
		}
	}
	return nil
}

// GetValidatorMissedBlocks returns array of missed blocks for given validator.
func (k Keeper) GetValidatorMissedBlocks(ctx context.Context, addr sdk.ConsAddress) ([]types.MissedBlock, error) {
	signedBlocksWindow, err := k.SignedBlocksWindow(ctx)
	if err != nil {
		return nil, err
	}

	missedBlocks := make([]types.MissedBlock, 0, signedBlocksWindow)
	err = k.IterateMissedBlockBitmap(ctx, addr, func(index int64, missed bool) (stop bool) {
		if missed {
			missedBlocks = append(missedBlocks, types.NewMissedBlock(index, missed))
		}

		return false
	})

	return missedBlocks, err
}
