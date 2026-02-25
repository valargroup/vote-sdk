// Package votetree provides Go CGO bindings to the Poseidon Merkle tree
// exported by the zally-circuits Rust static library (libzally_circuits.a).
//
// # Overview
//
// The vote commitment tree is an append-only, depth-24 Poseidon Merkle tree
// that holds Vote Authority Notes (VANs) and Vote Commitments (VCs). It is
// maintained by the vote chain's EndBlocker and serves as the anchor for ZKP
// #2 (VAN membership) and ZKP #3 (VC membership). See vote-commitment-tree/
// for the full tree implementation and protocol context.
//
// This package exposes two APIs:
//
//   - Stateless API: ComputePoseidonRoot / ComputeMerklePath — build a fresh
//     tree from a flat leaf slice on every call. Simple, but O(n) per call.
//     Used in tests and for backward-compatible callers.
//
//   - Stateful API: TreeHandle — wraps a Rust-side ShardTree that persists
//     across calls. AppendBatch adds only the delta leaves since the last call;
//     Checkpoint + Root return the correct root for that block height. Reduces
//     EndBlocker from O(n) per block to O(k) where k = new leaves that block.
//
// # Memory layout
//
// The Rust static library owns the tree data. TreeHandle holds an opaque C
// pointer (*ZallyTreeHandle) into Rust-managed heap memory. The Go GC does not
// manage this memory; callers must call Close() to free it.
//
//	Go Keeper
//	  └─ *votetree.TreeHandle (Go struct on Go heap)
//	       └─ ptr unsafe.Pointer ──► ZallyTreeHandle (Rust Box<TreeHandle> on Rust heap)
//	                                     └─ TreeServer (ShardTree<MemoryShardStore, 32, 4>)
//
// # CGO boundary
//
// Leaves cross the CGO boundary as a flat byte array: all leaves are
// concatenated into a single []byte before the C call. This avoids per-leaf
// CGO overhead (CGO calls have ~50–100 ns overhead each; batching amortises it
// to one call per block regardless of how many leaves were appended).
//
// # Leaf encoding
//
// All leaves and roots are 32-byte little-endian canonical Pallas Fp values
// (the same encoding the Go KV store uses: 0x02 || index → 32-byte leaf).
// Non-canonical encodings (byte patterns that exceed the Pallas field modulus)
// are rejected by the Rust deserializer with error code -3.
//
// # Checkpoint semantics
//
// ShardTree (from Zcash's incrementalmerkletree crate) only materializes
// Merkle roots at checkpoint boundaries. After AppendBatch, the tree has the
// leaves but no root is accessible until Checkpoint(height) is called. Root()
// returns the root at the most recent checkpoint; Path(pos, height) returns the
// Merkle authentication path for a leaf anchored to a specific checkpoint
// height. Callers must always call Checkpoint before calling Root or Path.
//
// # Build requirement
//
// The Rust static library must be built before any CGO linking:
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
	// LeafBytes is the byte size of a single tree leaf: a Pallas Fp element in
	// canonical 32-byte little-endian representation. This matches the encoding
	// the Go KV store uses (key 0x02 || big-endian index → 32-byte value) and
	// the Rust Fp::to_repr() / Fp::from_repr() round-trip.
	LeafBytes = 32

	// MerklePathBytes is the byte size of a serialized Merkle authentication
	// path: 4 bytes (leaf position as u32 LE) + TREE_DEPTH×32 bytes (sibling
	// hashes from leaf to root, leaf-first order) = 4 + 24×32 = 772 bytes.
	// Consumed by the share-reveal proof generator (zally_generate_share_reveal)
	// and by ZKP #2 / ZKP #3 circuit inputs.
	MerklePathBytes = 772
)

// ComputePoseidonRoot computes the Poseidon Merkle root from a complete slice
// of commitment leaves. Each leaf must be exactly LeafBytes (32) bytes in
// canonical Pallas Fp little-endian encoding.
//
// This is the stateless API: a fresh depth-24 ShardTree is built from
// scratch on every call, checkpointed, and the root is returned. The cost is
// O(n) in the number of leaves. Use TreeHandle for repeated calls across
// blocks.
//
// A nil or empty slice returns the deterministic empty-tree root (the Poseidon
// hash of the all-empty depth-24 tree).
//
// Returns a 32-byte root, or an error if any leaf has the wrong size or
// contains a non-canonical Fp encoding (a bit-pattern ≥ the Pallas field
// modulus).
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

// TreeHandle is the stateful API: a Poseidon Merkle tree handle backed by a
// Rust ShardTree that lives across multiple blocks. The Go Keeper holds one
// instance for the lifetime of the process.
//
// # Lifecycle
//
// Create once with NewTreeHandle, load existing leaves with AppendBatch,
// snapshot each block with Checkpoint, read the root with Root. Call Close
// exactly once when done (node shutdown or explicit teardown).
//
//	h := NewTreeHandle()
//	defer h.Close()
//	h.AppendBatch(existingLeaves)   // cold-start load from KV
//	h.Checkpoint(blockHeight)
//	root, _ := h.Root()
//
// # Incremental update pattern (used by Keeper.ensureTreeLoaded)
//
//	// Each block: only the delta leaves [cursor, nextIndex) are fetched.
//	h.AppendBatch(deltaLeaves)
//	h.Checkpoint(blockHeight)    // called inside ComputeTreeRoot
//	root, _ := h.Root()          // root at latest checkpoint
//
// # Memory ownership
//
// ptr is an opaque pointer into Rust-managed heap memory (a Box<TreeHandle>
// allocated by zally_vote_tree_create). The Go GC does not track this memory.
// Close must be called to free it; failing to do so leaks the Rust allocation.
// Close is idempotent: a second call after the first is a no-op.
//
// # Concurrency
//
// TreeHandle is NOT safe for concurrent use. The Keeper holds it under
// single-threaded EndBlocker execution; no external locking is needed.
type TreeHandle struct {
	ptr unsafe.Pointer // *C.ZallyTreeHandle — Rust heap allocation, not GC-tracked
}

// NewTreeHandle allocates a new, empty Rust-side tree handle. The returned
// handle contains zero leaves and no checkpoints. Call AppendBatch to load
// existing leaves before using Root or Path.
func NewTreeHandle() *TreeHandle {
	ptr := C.zally_vote_tree_create()
	return &TreeHandle{ptr: unsafe.Pointer(ptr)}
}

// AppendBatch appends leaves to the tree in a single CGO call. Each leaf must
// be exactly LeafBytes (32) bytes in canonical Pallas Fp little-endian
// encoding.
//
// Leaves are flattened into one contiguous byte array before crossing the CGO
// boundary, so the cost is one C call regardless of batch size.
//
// After AppendBatch the tree has the new leaves internally, but no root is
// accessible until Checkpoint is called — ShardTree only materialises roots at
// checkpoint boundaries.
func (h *TreeHandle) AppendBatch(leaves [][]byte) error {
	if len(leaves) == 0 {
		return nil
	}
	for i, leaf := range leaves {
		if len(leaf) != LeafBytes {
			return fmt.Errorf("votetree: leaf %d must be %d bytes, got %d", i, LeafBytes, len(leaf))
		}
	}

	flat := make([]byte, len(leaves)*LeafBytes)
	for i, leaf := range leaves {
		copy(flat[i*LeafBytes:], leaf)
	}

	rc := C.zally_vote_tree_append_batch(
		(*C.ZallyTreeHandle)(h.ptr),
		(*C.uint8_t)(unsafe.Pointer(&flat[0])),
		C.size_t(len(leaves)),
	)
	switch rc {
	case 0:
		return nil
	case -1:
		return fmt.Errorf("votetree: invalid inputs to append_batch")
	case -3:
		return fmt.Errorf("votetree: leaf deserialization error (non-canonical Fp)")
	default:
		return fmt.Errorf("votetree: append_batch returned error code %d", rc)
	}
}

// Checkpoint snapshots the current tree state tagged to the given block height.
//
// Must be called after every AppendBatch and before Root or Path. The height
// becomes the checkpoint ID: Path(pos, height) generates a Merkle witness
// anchored to the tree state at that exact block, and Root returns the root
// at the most recently created checkpoint.
//
// Calling Checkpoint at the same height twice is harmless. Calling Root or
// Path before any Checkpoint returns the empty-tree root or an error.
func (h *TreeHandle) Checkpoint(height uint32) error {
	rc := C.zally_vote_tree_checkpoint((*C.ZallyTreeHandle)(h.ptr), C.uint32_t(height))
	if rc != 0 {
		return fmt.Errorf("votetree: checkpoint returned error code %d", rc)
	}
	return nil
}

// Root returns the 32-byte Poseidon Merkle root at the most recently created
// checkpoint. Checkpoint must have been called at least once after the last
// AppendBatch; otherwise Root returns the root at the previous checkpoint (or
// the deterministic empty-tree root if no checkpoint has ever been made).
func (h *TreeHandle) Root() ([]byte, error) {
	var rootBuf [LeafBytes]byte
	rc := C.zally_vote_tree_root_stateful(
		(*C.ZallyTreeHandle)(h.ptr),
		(*C.uint8_t)(unsafe.Pointer(&rootBuf[0])),
	)
	if rc != 0 {
		return nil, fmt.Errorf("votetree: root_stateful returned error code %d", rc)
	}
	result := make([]byte, LeafBytes)
	copy(result, rootBuf[:])
	return result, nil
}

// Size returns the total number of leaves appended since the handle was
// created. Used by Keeper.ensureTreeLoaded to detect rollbacks: if
// Size() > KV nextIndex, the chain rolled back and the handle must be
// discarded and rebuilt.
func (h *TreeHandle) Size() uint64 {
	return uint64(C.zally_vote_tree_size((*C.ZallyTreeHandle)(h.ptr)))
}

// Path returns the MerklePathBytes (772) serialized Merkle authentication path
// for the leaf at position, anchored to the checkpoint at height.
//
// The path format is: 4 bytes (position u32 LE) || 24×32 bytes (sibling
// hashes from leaf level up to the root, leaf-first).
//
// This is the stateful equivalent of ComputeMerklePath: it does not rebuild
// the tree, and it can generate paths for any checkpoint height still held in
// the ShardTree's checkpoint window (up to MAX_CHECKPOINTS = 1000 blocks).
// Returns an error if position ≥ Size() or height has no checkpoint.
func (h *TreeHandle) Path(position uint64, height uint32) ([]byte, error) {
	var pathBuf [MerklePathBytes]byte
	rc := C.zally_vote_tree_path_stateful(
		(*C.ZallyTreeHandle)(h.ptr),
		C.uint64_t(position),
		C.uint32_t(height),
		(*C.uint8_t)(unsafe.Pointer(&pathBuf[0])),
	)
	switch rc {
	case 0:
		result := make([]byte, MerklePathBytes)
		copy(result, pathBuf[:])
		return result, nil
	case -1:
		return nil, fmt.Errorf("votetree: invalid inputs to path_stateful")
	case -2:
		return nil, fmt.Errorf("votetree: position %d out of range or height %d has no checkpoint", position, height)
	default:
		return nil, fmt.Errorf("votetree: path_stateful returned error code %d", rc)
	}
}

// Close frees the Rust-side heap allocation by calling zally_vote_tree_free,
// which reconstructs the Box<TreeHandle> and drops it. After Close, the
// handle is zeroed (ptr = nil) and all further method calls are no-ops or
// will return errors. Close is idempotent.
func (h *TreeHandle) Close() {
	if h.ptr != nil {
		C.zally_vote_tree_free((*C.ZallyTreeHandle)(h.ptr))
		h.ptr = nil
	}
}

// ComputeMerklePath computes the Poseidon Merkle authentication path for the
// leaf at position. Each leaf must be exactly LeafBytes (32) bytes.
//
// This is the stateless API: like ComputePoseidonRoot, it builds a fresh tree
// from all leaves on every call (O(n)). Use TreeHandle.Path for repeated
// calls across blocks.
//
// Returns a MerklePathBytes (772) byte slice:
//   - Bytes [0..4):    leaf position (u32 LE)
//   - Bytes [4..772):  24 sibling hashes, 32 bytes each, leaf→root order
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
