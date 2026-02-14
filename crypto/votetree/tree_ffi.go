// Package votetree provides Go bindings to the Poseidon Merkle tree functions
// exported by the zally-circuits Rust static library.
//
// These functions compute Poseidon Merkle roots and authentication paths for
// the vote commitment tree (Gov Steps V1).
//
// It requires the Rust static library to be built first:
//
//	cargo build --release --manifest-path sdk/circuits/Cargo.toml
package votetree

/*
#cgo LDFLAGS: -L${SRCDIR}/../../circuits/target/release -lzally_circuits -ldl -lm -lpthread
#cgo darwin LDFLAGS: -framework Security -framework CoreFoundation
#include "../../circuits/include/zally_circuits.h"
#include <stdlib.h>
*/
import "C"

import (
	"fmt"
	"unsafe"
)

const (
	// LeafBytes is the size of a single leaf (Pallas Fp, 32-byte LE).
	LeafBytes = 32

	// MerklePathBytes is the serialized size of a Merkle authentication path:
	// 4 bytes (position u32 LE) + 24 * 32 bytes (auth path) = 772.
	// Tree depth is 24 (2^24 ≈ 16.7M leaf capacity).
	MerklePathBytes = 772
)

// ComputePoseidonRoot computes the Poseidon Merkle root from a slice of
// commitment leaves. Each leaf must be exactly 32 bytes (Pallas Fp in
// canonical little-endian representation).
//
// Returns a 32-byte root, or an error if inputs are invalid or a leaf
// contains a non-canonical field element encoding.
func ComputePoseidonRoot(leaves [][]byte) ([]byte, error) {
	// Validate individual leaf sizes.
	for i, leaf := range leaves {
		if len(leaf) != LeafBytes {
			return nil, fmt.Errorf("votetree: leaf %d must be %d bytes, got %d", i, LeafBytes, len(leaf))
		}
	}

	// Flatten leaves into a contiguous byte array for the C call.
	var flatPtr *C.uint8_t
	leafCount := C.size_t(len(leaves))

	if len(leaves) > 0 {
		flat := make([]byte, len(leaves)*LeafBytes)
		for i, leaf := range leaves {
			copy(flat[i*LeafBytes:], leaf)
		}
		flatPtr = (*C.uint8_t)(unsafe.Pointer(&flat[0]))
	}

	// Allocate output buffer.
	var rootBuf [LeafBytes]byte
	rootOut := (*C.uint8_t)(unsafe.Pointer(&rootBuf[0]))

	rc := C.zally_vote_tree_root(flatPtr, leafCount, rootOut)

	switch rc {
	case 0:
		result := make([]byte, LeafBytes)
		copy(result, rootBuf[:])
		return result, nil
	case -1:
		return nil, fmt.Errorf("votetree: invalid inputs")
	case -3:
		return nil, fmt.Errorf("votetree: leaf deserialization error (non-canonical Fp)")
	default:
		return nil, fmt.Errorf("votetree: unknown error code %d", rc)
	}
}

// ComputeMerklePath computes the Poseidon Merkle authentication path for the
// leaf at the given position. Each leaf must be exactly 32 bytes.
//
// Returns a 772-byte serialized path:
//   - Bytes [0..4):    position (u32 LE)
//   - Bytes [4..772):  auth path (24 sibling hashes, 32 bytes each, leaf→root)
func ComputeMerklePath(leaves [][]byte, position uint64) ([]byte, error) {
	if len(leaves) == 0 {
		return nil, fmt.Errorf("votetree: cannot compute path for empty tree")
	}
	for i, leaf := range leaves {
		if len(leaf) != LeafBytes {
			return nil, fmt.Errorf("votetree: leaf %d must be %d bytes, got %d", i, LeafBytes, len(leaf))
		}
	}

	// Flatten leaves.
	flat := make([]byte, len(leaves)*LeafBytes)
	for i, leaf := range leaves {
		copy(flat[i*LeafBytes:], leaf)
	}
	flatPtr := (*C.uint8_t)(unsafe.Pointer(&flat[0]))
	leafCount := C.size_t(len(leaves))

	// Allocate output buffer.
	var pathBuf [MerklePathBytes]byte
	pathOut := (*C.uint8_t)(unsafe.Pointer(&pathBuf[0]))

	rc := C.zally_vote_tree_path(flatPtr, leafCount, C.uint64_t(position), pathOut)

	switch rc {
	case 0:
		result := make([]byte, MerklePathBytes)
		copy(result, pathBuf[:])
		return result, nil
	case -1:
		return nil, fmt.Errorf("votetree: invalid inputs")
	case -2:
		return nil, fmt.Errorf("votetree: position %d out of range (leaf_count=%d)", position, len(leaves))
	case -3:
		return nil, fmt.Errorf("votetree: leaf deserialization error (non-canonical Fp)")
	default:
		return nil, fmt.Errorf("votetree: unknown error code %d", rc)
	}
}
