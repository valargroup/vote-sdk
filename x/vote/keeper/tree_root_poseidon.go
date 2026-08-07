package keeper

// This file provides the ComputeTreeRoot implementation using the stateful
// Poseidon Merkle tree via Rust FFI. Requires the Rust static library:
//
//	cargo build --release --manifest-path sdk/circuits/Cargo.toml

import (
	"fmt"

	"cosmossdk.io/core/store"

	"github.com/valargroup/vote-sdk/x/vote/types"
)

// ComputeTreeRoot returns the Poseidon Merkle root for a round's tree at the
// given block height.
//
// On cold start (handle == nil) the behaviour depends on persisted state:
//   - Genesis replay pending: no shard data was included in genesis. The
//     handle is created at 0 and all imported leaves are replayed.
//   - Height > 0 (restart): shard data exists in KV. Handle is created at the
//     last checkpointed leaf count and any post-checkpoint leaves are appended
//     from KV before checkpointing.
//   - Height == 0 (first boot): no shard data yet. Handle is created at 0
//     and all leaves are replayed via AppendFromKV — O(N) but unavoidable.
//
// On subsequent calls only the delta leaves added since the last call are
// appended — O(k) per block where k = new leaves that block.
//
// A checkpoint is created only when delta leaves were actually appended.
// No-new-leaves restart blocks skip the checkpoint; latest_checkpoint is
// restored from KV on handle creation so Root() is still correct.
func (k *Keeper) ComputeTreeRoot(kvStore store.KVStore, roundID []byte, nextIndex, blockHeight uint64) ([]byte, error) {
	if nextIndex == 0 {
		return nil, nil
	}

	appended, replayPending, err := k.ensureRoundTreeLoaded(kvStore, roundID, nextIndex)
	if err != nil {
		return nil, err
	}

	rt := k.getOrCreateRoundTree(roundID)

	if appended {
		if err := rt.handle.Checkpoint(uint32(blockHeight)); err != nil {
			return nil, err
		}
	}
	root, err := rt.handle.Root()
	if err != nil {
		return nil, err
	}
	if err := k.debugVerifyConsistency(kvStore, roundID, nextIndex, root); err != nil {
		return nil, err
	}
	if replayPending {
		if err := kvStore.Delete(types.RoundTreeReplayPendingKey(roundID)); err != nil {
			return nil, fmt.Errorf("clear genesis tree replay marker: %w", err)
		}
	}
	return root, nil
}
