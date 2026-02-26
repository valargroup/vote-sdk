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
// On cold start (treeHandle == nil) the behaviour depends on state.Height:
//   - Height > 0 (restart): shard data exists in KV. Handle is created at
//     nextIndex and ShardTree restores lazily from KV — O(1).
//   - Height == 0 (first boot): no shard data yet. Handle is created at 0
//     and all leaves are replayed via AppendFromKV — O(N) but unavoidable.
//
// On subsequent calls only the delta leaves added since the last call are
// appended — O(k) per block where k = new leaves that block.
//
// A checkpoint is created only when delta leaves were actually appended.
// Cold start and no-new-leaves blocks skip the checkpoint; latest_checkpoint
// is restored from KV on handle creation so Root() is always correct.
func (k *Keeper) ComputeTreeRoot(kvStore store.KVStore, nextIndex, blockHeight uint64) ([]byte, error) {
	if nextIndex == 0 {
		return nil, nil
	}

	// Update the KV proxy so Rust callbacks reach the current block's store.
	k.kvProxy.Current = kvStore

	appended, err := k.ensureTreeLoaded(kvStore, nextIndex)
	if err != nil {
		return nil, err
	}
	// Checkpoint only when new leaves were appended (appended=true). For
	// no-new-leaves blocks (Size() == nextIndex) appended is false and we
	// skip the checkpoint, returning the root from the last existing checkpoint.
	if appended {
		if err := k.treeHandle.Checkpoint(uint32(blockHeight)); err != nil {
			return nil, err
		}
	}
	root, err := k.treeHandle.Root()
	if err != nil {
		return nil, err
	}
	if err := k.debugVerifyConsistency(kvStore, nextIndex, root); err != nil {
		return nil, err
	}
	return root, nil
}
