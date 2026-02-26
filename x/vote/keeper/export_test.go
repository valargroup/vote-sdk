package keeper

import "cosmossdk.io/core/store"

// TreeCursorForTest exposes the internal treeCursor for testing.
func (k *Keeper) TreeCursorForTest() uint64 {
	return k.treeCursor
}

// StoreServiceForTest exposes the store service so tests can create a second
// Keeper backed by the same underlying store (simulating node restart).
func (k *Keeper) StoreServiceForTest() store.KVStoreService {
	return k.storeService
}

// SetStakingKeeper replaces the staking keeper. Used in tests.
func (k *Keeper) SetStakingKeeper(sk StakingKeeper) {
	k.stakingKeeper = sk
}
