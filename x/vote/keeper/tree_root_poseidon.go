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
// On cold start (treeHandle == nil) a KV-backed handle is created whose
// KvShardStore calls back into Go for every shard read/write. ShardTree
// lazily loads only the data it needs — O(1) cold start.
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
	// Only checkpoint when new leaves were appended. Skipping the checkpoint
	// on cold start avoids a duplicate-checkpoint panic: TreeServer::new
	// restores latest_checkpoint from KV, so re-checkpointing at the same
	// height would violate the monotonicity invariant.
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
