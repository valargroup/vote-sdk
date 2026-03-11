//go:build debug_tree

package keeper

// debug_verify.go — runtime tree consistency checker, compiled only with
// -tags debug_tree.
//
// Enable with: go build/test -tags debug_tree
//
// When active, ComputeTreeRoot rebuilds a fresh ephemeral tree from all
// commitment leaves stored in KV and compares its root to the KV-backed
// ShardTree root on every block. Any discrepancy is returned as an error,
// halting block processing immediately rather than silently producing wrong
// proofs.
//
// Catches:
//   - Shard blob serialization/deserialization bugs
//   - KV store corruption
//   - Append ordering bugs
//   - Rollback handling bugs
//
// Cost: O(nextIndex) leaf reads + one full tree rebuild per block. Not safe
// for production use; intended for development, integration testing, and
// debugging suspected tree corruption.

import (
	"fmt"

	"cosmossdk.io/core/store"

	"github.com/valargroup/shielded-vote/crypto/votetree"
	"github.com/valargroup/shielded-vote/x/vote/types"
)

// debugVerifyConsistency reads all nextIndex commitment leaves from kvStore,
// builds a fresh ephemeral tree, and verifies that its root equals expectedRoot.
func (k *Keeper) debugVerifyConsistency(kvStore store.KVStore, nextIndex uint64, expectedRoot []byte) error {
	if nextIndex == 0 {
		return nil
	}

	leaves := make([][]byte, nextIndex)
	for i := uint64(0); i < nextIndex; i++ {
		leaf, err := kvStore.Get(types.CommitmentLeafKey(i))
		if err != nil {
			return fmt.Errorf("debug tree verify: read leaf %d: %w", i, err)
		}
		if len(leaf) == 0 {
			return fmt.Errorf("debug tree verify: leaf %d missing in KV", i)
		}
		leaves[i] = leaf
	}

	if err := votetree.VerifyRootFromLeaves(leaves, expectedRoot); err != nil {
		return fmt.Errorf("debug tree verify: CONSISTENCY FAILURE (nextIndex=%d): %w", nextIndex, err)
	}

	k.logger.Info("debug tree verify: root consistent", "leaf_count", nextIndex)
	return nil
}
