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
// # Stateful API: TreeHandle
//
// TreeHandle wraps a Rust-side ShardTree<KvShardStore> that persists across
// blocks. Rust reads and writes shards, the cap, and checkpoints directly to
// the Cosmos KV store through Go callbacks registered at handle creation time.
// AppendBatch appends only the delta leaves since the last call — O(k) per
// block. Cold start is O(1): no leaf replay, no explicit restore loop.
//
// For temporary use (e.g. the helper server computing a single path from a
// flat leaf slice), use NewEphemeralTreeHandle which backs the same stateful
// handle with an in-memory KV store.
//
// # Architecture
//
// The stateful path uses reverse FFI: Rust calls back into Go for KV I/O.
//
//	Go Keeper
//	  ├─ kvProxy *KvStoreProxy  ← stable pointer; Current updated each block
//	  └─ treeHandle *TreeHandle
//	       └─ ptr ──────────► ZallyTreeHandle  (Rust Box<T>, Rust heap)
//	                               └─ TreeServer
//	                                    └─ ShardTree<KvShardStore, 24, 4>
//	                                         └─ KvCallbacks { ctx=kvProxy, ... }
//	                                              └─ zallyKv* //export functions
//	                                                   └─ kvProxy.Current (KVStore)
//
// # KV key schema
//
// ShardTree state occupies three key prefixes (same byte values as keys.go):
//
//	0x0F || u64 BE shard_index  →  shard blob   (written on every put_shard)
//	0x10                        →  cap blob      (written when cap changes)
//	0x11 || u32 BE checkpoint_id → checkpoint blob (written on Checkpoint)
//
// # CGO boundary for leaf batches
//
// Leaf appends cross the CGO boundary as a single flat byte array regardless
// of batch size. CGO calls carry ~50–100 ns overhead each; batching amortises
// that to one call per block.
//
// # Leaf encoding
//
// All leaves and roots are 32-byte little-endian canonical Pallas Fp values
// (the same encoding the Go KV store uses: 0x02 || index → 32-byte leaf).
// Non-canonical encodings are rejected with error code -3.
//
// # Checkpoint semantics
//
// ShardTree only materializes Merkle roots at checkpoint boundaries. After
// AppendBatch the tree has the new leaves internally but no root is accessible
// until Checkpoint(height) is called. Root() returns the root at the most
// recent checkpoint; Path(pos, height) returns a witness anchored to a
// specific checkpoint height.
//
// # Build requirement
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
	"runtime/cgo"
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

// TreeHandle is the stateful API: a Poseidon Merkle tree handle backed by a
// Rust ShardTree<KvShardStore> whose shard reads/writes go directly to the
// Cosmos KV store through reverse-FFI callbacks. The Go Keeper holds one
// instance for the lifetime of the process.
//
// # Lifecycle
//
// Create once with NewTreeHandleWithKV, passing the Keeper's stable KvStoreProxy
// and the current leaf count. Call AppendBatch for new leaves each block,
// Checkpoint to snapshot state, Root to read the root. Call Close exactly
// once when done (node shutdown or rollback).
//
//	proxy := &votetree.KvStoreProxy{}
//	h, err := votetree.NewTreeHandleWithKV(proxy, nextIndex)
//	if err != nil { ... }
//	defer h.Close()
//
//	// Each block:
//	proxy.Current = kvStore
//	h.AppendBatch(deltaLeaves)
//	h.Checkpoint(blockHeight)
//	root, _ := h.Root()
//
// # Memory ownership
//
// ptr is an opaque pointer into Rust-managed heap memory (a Box<ZallyTreeHandle>
// allocated by zally_vote_tree_create_with_kv). The Go GC does not track this
// memory. Close must be called to free it. Close is idempotent.
//
// # Concurrency
//
// TreeHandle is NOT safe for concurrent use. The Keeper holds it under
// single-threaded EndBlocker execution; no external locking is needed.
type TreeHandle struct {
	ptr         unsafe.Pointer // *C.ZallyTreeHandle — Rust heap allocation, not GC-tracked
	proxyHandle cgo.Handle     // keeps the KvStoreProxy reachable and lets callbacks recover it
	ctxPtr      unsafe.Pointer // C-malloc'd uintptr_t holding proxyHandle; freed in Close
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
	case -4:
		return fmt.Errorf("votetree: KV store or ShardTree storage error in append_batch")
	default:
		return fmt.Errorf("votetree: append_batch returned error code %d", rc)
	}
}

// AppendFromKV appends count leaves starting at cursor directly from the
// Cosmos KV store via the KV callbacks registered at handle creation time.
//
// This is the optimised delta-append path for ensureTreeLoaded: instead of
// reading each leaf individually in Go, serializing them, and passing them
// over CGO, the Rust side reads the leaves directly using the reverse-FFI KV
// callbacks. One CGO call regardless of how many leaves were added.
//
// Each leaf is stored at CommitmentLeafKey(cursor+i) = 0x02 || (cursor+i as
// uint64 big-endian) in the Cosmos KV store.
func (h *TreeHandle) AppendFromKV(cursor, count uint64) error {
	if count == 0 {
		return nil
	}
	rc := C.zally_vote_tree_append_from_kv(
		(*C.ZallyTreeHandle)(h.ptr),
		C.uint64_t(cursor),
		C.uint64_t(count),
	)
	switch rc {
	case 0:
		return nil
	case -1:
		return fmt.Errorf("votetree: null handle in append_from_kv")
	case -4:
		return fmt.Errorf("votetree: leaf missing, malformed, or KV error in append_from_kv (cursor=%d count=%d)", cursor, count)
	default:
		return fmt.Errorf("votetree: append_from_kv returned error code %d", rc)
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
	switch rc {
	case 0:
		return nil
	case -4:
		return fmt.Errorf("votetree: KV store storage error during checkpoint at height %d", height)
	default:
		return fmt.Errorf("votetree: checkpoint returned error code %d", rc)
	}
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

// ClearCheckpoint resets the in-memory latest_checkpoint to None without
// touching the KV store. Used on rollback: after a new handle is created with
// stale KV checkpoints from the pre-rollback state, calling ClearCheckpoint
// allows a subsequent Checkpoint(M) call to establish the correct root for
// the rolled-back height M without triggering the monotonicity assertion.
func (h *TreeHandle) ClearCheckpoint() {
	C.zally_vote_tree_clear_checkpoint((*C.ZallyTreeHandle)(h.ptr))
}

// TruncateKVData deletes all tree-related KV entries (shards, cap,
// checkpoints) through this handle's KV callbacks. Must be called on the OLD
// handle just before Close() on rollback, so that the fresh handle created at
// next_position=0 starts with an empty KV state. Without this, ShardTree
// would read stale pre-rollback shard data and place new leaves at wrong
// positions, producing an incorrect root.
func (h *TreeHandle) TruncateKVData() error {
	rc := C.zally_vote_tree_truncate_kv_data((*C.ZallyTreeHandle)(h.ptr))
	if rc != 0 {
		return fmt.Errorf("votetree: truncate_kv_data failed (rc=%d)", rc)
	}
	return nil
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
// Unlike a fresh ephemeral tree, this does not rebuild the tree from scratch,
// and it can generate paths for any checkpoint height still held in
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
		h.proxyHandle.Delete()
		if h.ctxPtr != nil {
			C.free(h.ctxPtr)
			h.ctxPtr = nil
		}
	}
}

