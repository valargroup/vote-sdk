package votetree

import (
	"bytes"
	"fmt"
)

// SingleLeafRoot returns the canonical depth-24 vote commitment tree root for
// a tree whose only leaf is leaf at position zero. Batch vote verification uses
// this root for every action after the first, anchoring that proof to the
// previous action's public successor VAN without appending intermediate VANs to
// global chain state.
func SingleLeafRoot(leaf []byte) ([]byte, error) {
	if len(leaf) != LeafBytes {
		return nil, fmt.Errorf("votetree.SingleLeafRoot: leaf must be %d bytes, got %d", LeafBytes, len(leaf))
	}

	h, err := NewEphemeralTreeHandle()
	if err != nil {
		return nil, fmt.Errorf("votetree.SingleLeafRoot: create ephemeral handle: %w", err)
	}
	defer h.Close()

	if err := h.AppendBatch([][]byte{leaf}); err != nil {
		return nil, fmt.Errorf("votetree.SingleLeafRoot: append leaf: %w", err)
	}
	if err := h.Checkpoint(1); err != nil {
		return nil, fmt.Errorf("votetree.SingleLeafRoot: checkpoint: %w", err)
	}
	root, err := h.Root()
	if err != nil {
		return nil, fmt.Errorf("votetree.SingleLeafRoot: get root: %w", err)
	}
	return root, nil
}

// VerifyRootFromLeaves rebuilds a fresh ephemeral tree from leaves and checks
// that its root matches expectedRoot.
//
// This is an O(N) operation intended exclusively for debug-mode consistency
// checks; never call it on the hot EndBlocker path in production builds.
//
// It catches:
//   - Shard serialization/deserialization bugs (KV-backed root differs from a
//     clean rebuild of the same leaves through the in-memory code path)
//   - KV shard corruption
//   - Append-ordering bugs
//
// Returns nil on a matching root, a descriptive error on any mismatch or failure.
func VerifyRootFromLeaves(leaves [][]byte, expectedRoot []byte) error {
	if len(leaves) == 0 {
		return nil
	}

	h, err := NewEphemeralTreeHandle()
	if err != nil {
		return fmt.Errorf("votetree.VerifyRootFromLeaves: create ephemeral handle: %w", err)
	}
	defer h.Close()

	if err := h.AppendBatch(leaves); err != nil {
		return fmt.Errorf("votetree.VerifyRootFromLeaves: append batch: %w", err)
	}

	// Ephemeral tree is single-use; checkpoint ID is arbitrary.
	if err := h.Checkpoint(1); err != nil {
		return fmt.Errorf("votetree.VerifyRootFromLeaves: checkpoint: %w", err)
	}

	root, err := h.Root()
	if err != nil {
		return fmt.Errorf("votetree.VerifyRootFromLeaves: get root: %w", err)
	}

	if !bytes.Equal(root, expectedRoot) {
		return fmt.Errorf(
			"votetree.VerifyRootFromLeaves: root mismatch: ephemeral=%x kv-backed=%x (leaf_count=%d)",
			root, expectedRoot, len(leaves),
		)
	}
	return nil
}
