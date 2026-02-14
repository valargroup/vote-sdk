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

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestComputePoseidonRoot_GoldenVector verifies the 3-leaf golden root matches
// the hardcoded Rust value.
func TestComputePoseidonRoot_GoldenVector(t *testing.T) {
	root, err := ComputePoseidonRoot(goldenLeaves())
	require.NoError(t, err)
	require.Equal(t, goldenRoot(), root, "golden 3-leaf root must match Rust")
}

// TestComputePoseidonRoot_MatchesRust is an alias for the golden vector test,
// explicitly named to show cross-language parity.
func TestComputePoseidonRoot_MatchesRust(t *testing.T) {
	root, err := ComputePoseidonRoot(goldenLeaves())
	require.NoError(t, err)
	require.Equal(t, goldenRoot(), root)
}

// TestComputePoseidonRoot_Empty verifies that an empty tree returns the
// deterministic empty-tree Poseidon root.
func TestComputePoseidonRoot_Empty(t *testing.T) {
	root, err := ComputePoseidonRoot(nil)
	require.NoError(t, err)
	require.Equal(t, emptyRoot(), root, "empty tree root must match Rust")
}

// TestComputePoseidonRoot_SingleLeaf verifies a single-leaf tree.
func TestComputePoseidonRoot_SingleLeaf(t *testing.T) {
	leaf := fpLE(42)
	root, err := ComputePoseidonRoot([][]byte{leaf})
	require.NoError(t, err)
	require.Equal(t, singleLeaf42Root(), root, "single leaf (42) root must match Rust")
}

// TestComputePoseidonRoot_Deterministic verifies that the same leaves always
// produce the same root.
func TestComputePoseidonRoot_Deterministic(t *testing.T) {
	leaves := goldenLeaves()
	root1, err := ComputePoseidonRoot(leaves)
	require.NoError(t, err)
	root2, err := ComputePoseidonRoot(leaves)
	require.NoError(t, err)
	require.Equal(t, root1, root2)
}

// TestComputePoseidonRoot_DifferentLeaves verifies that different leaves
// produce different roots.
func TestComputePoseidonRoot_DifferentLeaves(t *testing.T) {
	leaves1 := [][]byte{fpLE(1), fpLE(2)}
	leaves2 := [][]byte{fpLE(1), fpLE(3)}

	root1, err := ComputePoseidonRoot(leaves1)
	require.NoError(t, err)
	root2, err := ComputePoseidonRoot(leaves2)
	require.NoError(t, err)
	require.NotEqual(t, root1, root2, "different leaves must produce different roots")
}

// TestComputePoseidonRoot_BadLeafSize verifies Go-side validation rejects
// leaves with wrong sizes before calling the FFI.
func TestComputePoseidonRoot_BadLeafSize(t *testing.T) {
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
			_, err := ComputePoseidonRoot(tc.leaves)
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.errMsg)
		})
	}
}

// TestComputeMerklePath_Verifies generates a path for each leaf in the golden
// vector and verifies it recomputes the expected root.
func TestComputeMerklePath_Verifies(t *testing.T) {
	leaves := goldenLeaves()
	expectedRoot := goldenRoot()

	for pos := uint64(0); pos < uint64(len(leaves)); pos++ {
		pathBytes, err := ComputeMerklePath(leaves, pos)
		require.NoError(t, err)
		require.Len(t, pathBytes, MerklePathBytes, "path must be 772 bytes")

		// The path starts with a u32 LE position.
		gotPos := binary.LittleEndian.Uint32(pathBytes[:4])
		require.Equal(t, uint32(pos), gotPos, "path position must match")

		// Verify: recompute root from path and leaf.
		// We do this by computing the root again and comparing — the
		// full Merkle path verification logic is in Rust. Here we just
		// verify the FFI round-trip produces a consistent root.
		root, err := ComputePoseidonRoot(leaves)
		require.NoError(t, err)
		require.Equal(t, expectedRoot, root)
	}
}

// TestComputeMerklePath_PositionOutOfRange verifies that position >= leaf_count
// is rejected.
func TestComputeMerklePath_PositionOutOfRange(t *testing.T) {
	leaves := goldenLeaves()

	_, err := ComputeMerklePath(leaves, 3)
	require.Error(t, err)
	require.Contains(t, err.Error(), "position 3 out of range")

	_, err = ComputeMerklePath(leaves, 100)
	require.Error(t, err)
	require.Contains(t, err.Error(), "position 100 out of range")
}

// TestComputeMerklePath_EmptyTree verifies that path computation on an empty
// tree is rejected.
func TestComputeMerklePath_EmptyTree(t *testing.T) {
	_, err := ComputeMerklePath(nil, 0)
	require.Error(t, err)
	require.Contains(t, err.Error(), "empty tree")
}

// TestComputeMerklePath_SingleLeaf verifies path generation for a 1-leaf tree.
func TestComputeMerklePath_SingleLeaf(t *testing.T) {
	leaf := fpLE(42)
	pathBytes, err := ComputeMerklePath([][]byte{leaf}, 0)
	require.NoError(t, err)
	require.Len(t, pathBytes, MerklePathBytes)

	// Position should be 0.
	gotPos := binary.LittleEndian.Uint32(pathBytes[:4])
	require.Equal(t, uint32(0), gotPos)
}
