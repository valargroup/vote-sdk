package votetree

import (
	"encoding/binary"
	"testing"

	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Golden test vectors
// ---------------------------------------------------------------------------
// These values are computed by the Rust vote-commitment-tree crate and hardcoded
// here to catch encoding mismatches between Go KV storage and Rust Fp
// representation. The same values are asserted in sdk/circuits/src/votetree.rs
// (Rust side) and sdk/circuits/tests/golden_vectors.rs.

// goldenLeaves returns the 3 golden leaves: Fp(1), Fp(2), Fp(3) in 32-byte LE.
func goldenLeaves() [][]byte {
	return [][]byte{
		{0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00},
		{0x02, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00},
		{0x03, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00},
	}
}

// goldenRoot is the Poseidon Merkle root for [Fp(1), Fp(2), Fp(3)] at depth 24.
func goldenRoot() []byte {
	return []byte{0xc9, 0x56, 0xdb, 0x06, 0xf7, 0x77, 0x41, 0xb1, 0x08, 0x3a, 0x8a, 0xa4, 0x9a, 0xe8, 0x67, 0xba, 0x16, 0x66, 0xf7, 0x93, 0x54, 0xef, 0xd0, 0x77, 0x33, 0xd7, 0x15, 0xed, 0xa1, 0x0e, 0x6a, 0x12}
}

// emptyRoot is the Poseidon Merkle root of an empty tree (depth 24).
func emptyRoot() []byte {
	return []byte{0x58, 0xb1, 0x67, 0x4a, 0x79, 0xc3, 0xe3, 0x37, 0xe4, 0x9f, 0x5e, 0x91, 0x0a, 0x38, 0xcc, 0xfd, 0xb4, 0xa2, 0xc3, 0xde, 0x47, 0xe8, 0x77, 0x8a, 0x02, 0x33, 0x03, 0x5f, 0xb8, 0xff, 0xc9, 0x33}
}

// singleLeaf42Root is the Poseidon Merkle root for a single leaf Fp(42) at depth 24.
func singleLeaf42Root() []byte {
	return []byte{0x6c, 0x83, 0x0c, 0x87, 0x72, 0x81, 0x96, 0x26, 0x20, 0x78, 0xd5, 0x6d, 0xe0, 0x0d, 0x22, 0x80, 0x1f, 0x62, 0xc1, 0x69, 0x01, 0x5c, 0xc0, 0x6c, 0xd6, 0x18, 0x89, 0xb4, 0x22, 0x86, 0x60, 0x04}
}

// fpLE returns a 32-byte little-endian Pallas Fp encoding of a small integer.
func fpLE(v uint64) []byte {
	buf := make([]byte, 32)
	binary.LittleEndian.PutUint64(buf[:8], v)
	return buf
}

// treeRoot builds an ephemeral tree from leaves and returns its root.
// Convenience helper for golden-vector tests.
func treeRoot(t *testing.T, leaves [][]byte) []byte {
	t.Helper()
	h := NewTreeHandle()
	defer h.Close()
	if len(leaves) > 0 {
		require.NoError(t, h.AppendBatch(leaves))
	}
	require.NoError(t, h.Checkpoint(1))
	root, err := h.Root()
	require.NoError(t, err)
	return root
}

// ---------------------------------------------------------------------------
// Root golden-vector tests (stateful API)
// ---------------------------------------------------------------------------

// TestTreeRoot_GoldenVector verifies the 3-leaf golden root matches the
// hardcoded Rust value.
func TestTreeRoot_GoldenVector(t *testing.T) {
	root := treeRoot(t, goldenLeaves())
	require.Equal(t, goldenRoot(), root, "golden 3-leaf root must match Rust")
}

// TestTreeRoot_Empty verifies that an empty tree returns the deterministic
// empty-tree Poseidon root.
func TestTreeRoot_Empty(t *testing.T) {
	root := treeRoot(t, nil)
	require.Equal(t, emptyRoot(), root, "empty tree root must match Rust")
}

// TestTreeRoot_SingleLeaf verifies a single-leaf tree.
func TestTreeRoot_SingleLeaf(t *testing.T) {
	root := treeRoot(t, [][]byte{fpLE(42)})
	require.Equal(t, singleLeaf42Root(), root, "single leaf (42) root must match Rust")
}

// TestTreeRoot_Deterministic verifies that the same leaves always produce the
// same root.
func TestTreeRoot_Deterministic(t *testing.T) {
	leaves := goldenLeaves()
	root1 := treeRoot(t, leaves)
	root2 := treeRoot(t, leaves)
	require.Equal(t, root1, root2)
}

// TestTreeRoot_DifferentLeaves verifies that different leaves produce
// different roots.
func TestTreeRoot_DifferentLeaves(t *testing.T) {
	root1 := treeRoot(t, [][]byte{fpLE(1), fpLE(2)})
	root2 := treeRoot(t, [][]byte{fpLE(1), fpLE(3)})
	require.NotEqual(t, root1, root2, "different leaves must produce different roots")
}

// TestTreeRoot_BadLeafSize verifies that AppendBatch rejects leaves with
// wrong sizes before calling the FFI.
func TestTreeRoot_BadLeafSize(t *testing.T) {
	tests := []struct {
		name   string
		leaves [][]byte
		errMsg string
	}{
		{
			name:   "short leaf",
			leaves: [][]byte{make([]byte, 16)},
			errMsg: "leaf 0 must be 32 bytes",
		},
		{
			name:   "long leaf",
			leaves: [][]byte{make([]byte, 64)},
			errMsg: "leaf 0 must be 32 bytes",
		},
		{
			name:   "empty leaf",
			leaves: [][]byte{{}},
			errMsg: "leaf 0 must be 32 bytes",
		},
		{
			name:   "second leaf bad",
			leaves: [][]byte{fpLE(1), make([]byte, 10)},
			errMsg: "leaf 1 must be 32 bytes",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := NewTreeHandle()
			defer h.Close()
			err := h.AppendBatch(tc.leaves)
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.errMsg)
		})
	}
}

// ---------------------------------------------------------------------------
// Path tests (stateful API)
// ---------------------------------------------------------------------------

// TestTreePath_Verifies generates a path for each leaf in the golden vector
// and verifies the position field is correct.
func TestTreePath_Verifies(t *testing.T) {
	leaves := goldenLeaves()

	h := NewTreeHandle()
	defer h.Close()
	require.NoError(t, h.AppendBatch(leaves))
	require.NoError(t, h.Checkpoint(1))

	for pos := uint64(0); pos < uint64(len(leaves)); pos++ {
		pathBytes, err := h.Path(pos, 1)
		require.NoError(t, err)
		require.Len(t, pathBytes, MerklePathBytes, "path must be 772 bytes")

		gotPos := binary.LittleEndian.Uint32(pathBytes[:4])
		require.Equal(t, uint32(pos), gotPos, "path position must match")
	}
}

// TestTreePath_PositionOutOfRange verifies that position >= leaf_count is
// rejected.
func TestTreePath_PositionOutOfRange(t *testing.T) {
	leaves := goldenLeaves()

	h := NewTreeHandle()
	defer h.Close()
	require.NoError(t, h.AppendBatch(leaves))
	require.NoError(t, h.Checkpoint(1))

	_, err := h.Path(3, 1)
	require.Error(t, err, "position == len(leaves) must fail")

	_, err = h.Path(100, 1)
	require.Error(t, err, "position >> len(leaves) must fail")
}

// TestTreePath_EmptyTree verifies that path computation on a tree with no
// leaves returns an error.
func TestTreePath_EmptyTree(t *testing.T) {
	h := NewTreeHandle()
	defer h.Close()
	require.NoError(t, h.Checkpoint(1))

	_, err := h.Path(0, 1)
	require.Error(t, err, "path on empty tree must fail")
}

// TestTreePath_SingleLeaf verifies path generation for a 1-leaf tree.
func TestTreePath_SingleLeaf(t *testing.T) {
	leaf := fpLE(42)
	h := NewTreeHandle()
	defer h.Close()
	require.NoError(t, h.AppendBatch([][]byte{leaf}))
	require.NoError(t, h.Checkpoint(1))

	pathBytes, err := h.Path(0, 1)
	require.NoError(t, err)
	require.Len(t, pathBytes, MerklePathBytes)

	gotPos := binary.LittleEndian.Uint32(pathBytes[:4])
	require.Equal(t, uint32(0), gotPos)
}

// ---------------------------------------------------------------------------
// Stateful TreeHandle tests
// ---------------------------------------------------------------------------

// TestTreeHandle_GoldenVector verifies that the stateful TreeHandle produces
// the expected golden root for the three golden leaves.
func TestTreeHandle_GoldenVector(t *testing.T) {
	h := NewTreeHandle()
	defer h.Close()

	require.NoError(t, h.AppendBatch(goldenLeaves()))
	require.NoError(t, h.Checkpoint(1))

	root, err := h.Root()
	require.NoError(t, err)
	require.Equal(t, goldenRoot(), root)
}

// TestTreeHandle_IncrementalMatchesFull verifies that appending leaves one at a
// time and in a batch produces the same root.
func TestTreeHandle_IncrementalMatchesFull(t *testing.T) {
	// Full batch.
	hBatch := NewTreeHandle()
	defer hBatch.Close()
	require.NoError(t, hBatch.AppendBatch(goldenLeaves()))
	require.NoError(t, hBatch.Checkpoint(1))
	batchRoot, err := hBatch.Root()
	require.NoError(t, err)

	// One leaf at a time.
	hIncr := NewTreeHandle()
	defer hIncr.Close()
	leaves := goldenLeaves()
	for i, leaf := range leaves {
		require.NoError(t, hIncr.AppendBatch([][]byte{leaf}))
		require.NoError(t, hIncr.Checkpoint(uint32(i+1)))
	}
	incrRoot, err := hIncr.Root()
	require.NoError(t, err)

	require.Equal(t, batchRoot, incrRoot, "incremental and batch must produce same root")
}

// TestTreeHandle_SizeTracking verifies that Size() reflects appended leaves.
func TestTreeHandle_SizeTracking(t *testing.T) {
	h := NewTreeHandle()
	defer h.Close()

	require.Equal(t, uint64(0), h.Size())

	require.NoError(t, h.AppendBatch(goldenLeaves()[:1]))
	require.Equal(t, uint64(1), h.Size())

	require.NoError(t, h.AppendBatch(goldenLeaves()[1:]))
	require.Equal(t, uint64(3), h.Size())
}

// TestTreeHandle_PathForEachLeaf verifies that Path() generates a valid
// 772-byte path for each leaf position.
func TestTreeHandle_PathForEachLeaf(t *testing.T) {
	leaves := goldenLeaves()

	h := NewTreeHandle()
	defer h.Close()
	require.NoError(t, h.AppendBatch(leaves))
	require.NoError(t, h.Checkpoint(1))

	for pos := uint64(0); pos < uint64(len(leaves)); pos++ {
		pathBytes, err := h.Path(pos, 1)
		require.NoError(t, err, "path for position %d must not error", pos)
		require.Len(t, pathBytes, MerklePathBytes, "path must be 772 bytes")

		gotPos := binary.LittleEndian.Uint32(pathBytes[:4])
		require.Equal(t, uint32(pos), gotPos, "path position must match")
	}
}

// TestTreeHandle_PathOutOfRange verifies that Path() returns an error for
// positions beyond the number of appended leaves.
func TestTreeHandle_PathOutOfRange(t *testing.T) {
	h := NewTreeHandle()
	defer h.Close()
	require.NoError(t, h.AppendBatch(goldenLeaves()[:1]))
	require.NoError(t, h.Checkpoint(1))

	_, err := h.Path(5, 1)
	require.Error(t, err, "out-of-range position must return error")
}

// TestTreeHandle_CloseIsIdempotent verifies that calling Close() twice does
// not panic.
func TestTreeHandle_CloseIsIdempotent(t *testing.T) {
	h := NewTreeHandle()
	h.Close()
	h.Close() // second call should be safe
}

// TestTreeHandle_DeltaAppendMatchesFull verifies that appending in two batches
// (simulating two blocks) produces the same root as a single full-batch append.
func TestTreeHandle_DeltaAppendMatchesFull(t *testing.T) {
	all := goldenLeaves()

	// Full batch in one shot.
	fullRoot := treeRoot(t, all)

	// Delta: first 2 leaves in block 1, then 1 more in block 2.
	h := NewTreeHandle()
	defer h.Close()
	require.NoError(t, h.AppendBatch(all[:2]))
	require.NoError(t, h.Checkpoint(1))
	require.NoError(t, h.AppendBatch(all[2:]))
	require.NoError(t, h.Checkpoint(2))
	incrRoot, err := h.Root()
	require.NoError(t, err)

	require.Equal(t, fullRoot, incrRoot, "stateful delta root must match full-batch root")
}
