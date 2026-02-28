//go:build halo2

package halo2

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/z-cale/zally/crypto/zkp"
)

// mustReadFixture reads a fixture file from crypto/zkp/testdata/.
// The testdata path is relative to the repo root; we resolve it from
// the package directory (crypto/zkp/halo2/) by going up one level.
func mustReadFixture(t *testing.T, name string) []byte {
	t.Helper()
	path := filepath.Join("..", "testdata", name)
	data, err := os.ReadFile(path)
	require.NoError(t, err, "failed to read fixture %s", path)
	return data
}

func TestToyProofValid(t *testing.T) {
	proof := mustReadFixture(t, "toy_valid_proof.bin")
	input := mustReadFixture(t, "toy_valid_input.bin")
	err := VerifyToyProof(proof, input)
	require.NoError(t, err, "valid proof should verify successfully")
}

func TestToyProofWrongInput(t *testing.T) {
	proof := mustReadFixture(t, "toy_valid_proof.bin")
	wrongInput := mustReadFixture(t, "toy_wrong_input.bin")
	err := VerifyToyProof(proof, wrongInput)
	require.Error(t, err, "proof should fail against wrong public input")
}

func TestToyProofCorrupted(t *testing.T) {
	proof := mustReadFixture(t, "toy_valid_proof.bin")
	input := mustReadFixture(t, "toy_valid_input.bin")

	// Corrupt the proof by flipping a byte.
	corrupted := make([]byte, len(proof))
	copy(corrupted, proof)
	corrupted[0] ^= 0xFF

	err := VerifyToyProof(corrupted, input)
	require.Error(t, err, "corrupted proof should fail verification")
}

func TestToyProofEmptyProof(t *testing.T) {
	input := mustReadFixture(t, "toy_valid_input.bin")
	err := VerifyToyProof([]byte{}, input)
	require.Error(t, err, "empty proof should fail")
}

func TestToyProofWrongInputLength(t *testing.T) {
	proof := mustReadFixture(t, "toy_valid_proof.bin")
	err := VerifyToyProof(proof, []byte{0x01, 0x02, 0x03})
	require.Error(t, err, "wrong input length should fail")
}

// validFp32 returns a 32-byte canonical Pallas Fp element (the value 1).
func validFp32() []byte {
	b := make([]byte, 32)
	b[0] = 1
	return b
}

// TestVerifyDelegationProofMalformedRk ensures that VerifyDelegationProof
// rejects a malformed rk (all 0xFF bytes) before reaching the Rust FFI layer,
// preventing undefined deserialization behavior. The error must name "rk".
func TestVerifyDelegationProofMalformedRk(t *testing.T) {
	malformedRk := make([]byte, 32)
	for i := range malformedRk {
		malformedRk[i] = 0xFF
	}

	inputs := zkp.DelegationInputs{
		SignedNoteNullifier: validFp32(),
		Rk:                  malformedRk,
		CmxNew:              validFp32(),
		VanCmx:              validFp32(),
		VoteRoundId:         validFp32(),
		NcRoot:              validFp32(),
		NullifierImtRoot:    validFp32(),
	}

	// A non-empty dummy proof is required to pass the early length guard.
	dummyProof := make([]byte, 64)
	err := VerifyDelegationProof(dummyProof, inputs)
	require.Error(t, err, "malformed rk should be rejected before reaching Rust FFI")
	require.True(t, strings.Contains(err.Error(), "rk"),
		"error should identify the rk field, got: %v", err)
}
