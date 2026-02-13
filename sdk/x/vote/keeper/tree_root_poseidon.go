package keeper

// This file provides the ComputeTreeRoot implementation using the Poseidon
// Merkle tree via Rust FFI. Requires the Rust static library:
//
//	cargo build --release --manifest-path sdk/circuits/Cargo.toml

import (
	"cosmossdk.io/core/store"

	"github.com/z-cale/zally/crypto/votetree"
	"github.com/z-cale/zally/x/vote/types"
)

// ComputeTreeRoot computes the Poseidon Merkle root over all leaves in the
// commitment tree via Rust FFI. The root matches what ZKP circuits expect.
func (k Keeper) ComputeTreeRoot(kvStore store.KVStore, nextIndex uint64) ([]byte, error) {
	if nextIndex == 0 {
		return nil, nil
	}

	// Read all leaves from KV into a slice.
	leaves := make([][]byte, nextIndex)
	for i := uint64(0); i < nextIndex; i++ {
		leaf, err := kvStore.Get(types.CommitmentLeafKey(i))
		if err != nil {
			return nil, err
		}
		leaves[i] = leaf
	}

	return votetree.ComputePoseidonRoot(leaves)
}
