package keeper

// This file provides the ComputeTreeRoot implementation using the stateful
// Poseidon Merkle tree via Rust FFI. Requires the Rust static library:
//
//	cargo build --release --manifest-path sdk/circuits/Cargo.toml

import (
	"cosmossdk.io/core/store"
)

// ComputeTreeRoot returns the Poseidon Merkle root for the current tree state
// at the given block height.
//
// On the first call (cold start) or after a rollback, ensureTreeLoaded reads
// all leaves from KV and rebuilds the tree — O(n). On subsequent calls it
// appends only the leaves added since the last call — O(k) per block.
//
// After loading new leaves (including cold-start and rollback rebuild), a
// checkpoint is created at blockHeight so that Root() returns the correct
// post-append root, and so that path_stateful queries anchored to this height
// work via the stateful handle.
// Blocks with no new leaves re-use the existing checkpoint — no-op.
func (k *Keeper) ComputeTreeRoot(kvStore store.KVStore, nextIndex, blockHeight uint64) ([]byte, error) {
	if nextIndex == 0 {
		return nil, nil
	}
	prevCursor := k.treeCursor
	if err := k.ensureTreeLoaded(kvStore, nextIndex); err != nil {
		return nil, err
	}
	// Checkpoint whenever the tree cursor changed — covers three cases:
	//   1. Cold start: prevCursor=0 → treeCursor=nextIndex (normal increase)
	//   2. Delta append: prevCursor < treeCursor (normal increase)
	//   3. Rollback rebuild: prevCursor > nextIndex → treeCursor=nextIndex (decrease)
	// A block with no new leaves leaves treeCursor unchanged, so no checkpoint.
	if k.treeCursor != prevCursor {
		if err := k.treeHandle.Checkpoint(uint32(blockHeight)); err != nil {
			return nil, err
		}
	}
	return k.treeHandle.Root()
}
