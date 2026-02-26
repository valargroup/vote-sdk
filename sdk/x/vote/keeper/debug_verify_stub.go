//go:build !debug_tree

package keeper

import "cosmossdk.io/core/store"

// debugVerifyConsistency is a no-op in production builds.
// Compile with -tags debug_tree to enable full O(N) tree consistency checks.
func (k *Keeper) debugVerifyConsistency(_ store.KVStore, _ uint64, _ []byte) error {
	return nil
}
