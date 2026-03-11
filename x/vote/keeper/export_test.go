package keeper

import "cosmossdk.io/core/store"

// TreeSizeForTest exposes treeHandle.Size() for testing. Returns 0 if the
// handle has not been initialized yet.
func (k *Keeper) TreeSizeForTest() uint64 {
	if k.treeHandle == nil {
		return 0
	}
	return k.treeHandle.Size()
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

// SetBankKeeper replaces the bank keeper. Used in tests.
func (k *Keeper) SetBankKeeper(bk BankKeeper) {
	k.bankKeeper = bk
}
