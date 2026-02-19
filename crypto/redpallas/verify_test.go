package redpallas

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

// testdataDir returns the absolute path to the redpallas testdata directory.
func testdataDir(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok, "runtime.Caller failed")
	return filepath.Join(filepath.Dir(thisFile), "testdata")
}

// mustReadFixture reads a binary fixture from the testdata directory.
func mustReadFixture(t *testing.T, name string) []byte {
	t.Helper()
	path := filepath.Join(testdataDir(t), name)
	data, err := os.ReadFile(path)
	require.NoError(t, err, "failed to read fixture %s", path)
	return data
}

// TestVerifyValidSignature loads the generated fixtures and verifies that
// a valid RedPallas SpendAuth signature passes verification.
func TestVerifyValidSignature(t *testing.T) {
	rk := mustReadFixture(t, "valid_rk.bin")
	sighash := mustReadFixture(t, "valid_sighash.bin")
	sig := mustReadFixture(t, "valid_sig.bin")

	require.Len(t, rk, 32, "rk should be 32 bytes")
	require.Len(t, sighash, 32, "sighash should be 32 bytes")
	require.Len(t, sig, 64, "sig should be 64 bytes")

	err := VerifySpendAuthSig(rk, sighash, sig)
	require.NoError(t, err, "valid signature should pass verification")
}

// TestVerifyWrongSignature verifies that a signature over a different message
// fails verification against the original sighash.
func TestVerifyWrongSignature(t *testing.T) {
	rk := mustReadFixture(t, "valid_rk.bin")
	sighash := mustReadFixture(t, "valid_sighash.bin")
	wrongSig := mustReadFixture(t, "wrong_sig.bin")

	err := VerifySpendAuthSig(rk, sighash, wrongSig)
	require.Error(t, err, "wrong signature should fail verification")
	require.Contains(t, err.Error(), "verification failed")
}

// TestVerifyBadInputs tests that the Go-side validation rejects inputs with
// incorrect lengths before even calling the Rust FFI.
func TestVerifyBadInputs(t *testing.T) {
	tests := []struct {
		name    string
		rk      []byte
		sighash []byte
		sig     []byte
		errMsg  string
	}{
		{
			name:    "empty rk",
			rk:      []byte{},
			sighash: make([]byte, 32),
			sig:     make([]byte, 64),
			errMsg:  "rk must be 32 bytes",
		},
		{
			name:    "short rk",
			rk:      make([]byte, 16),
			sighash: make([]byte, 32),
			sig:     make([]byte, 64),
			errMsg:  "rk must be 32 bytes",
		},
		{
			name:    "empty sighash",
			rk:      make([]byte, 32),
			sighash: []byte{},
			sig:     make([]byte, 64),
			errMsg:  "sighash must be 32 bytes",
		},
		{
			name:    "empty sig",
			rk:      make([]byte, 32),
			sighash: make([]byte, 32),
			sig:     []byte{},
			errMsg:  "sig must be 64 bytes",
		},
		{
			name:    "short sig",
			rk:      make([]byte, 32),
			sighash: make([]byte, 32),
			sig:     make([]byte, 32),
			errMsg:  "sig must be 64 bytes",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := VerifySpendAuthSig(tc.rk, tc.sighash, tc.sig)
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.errMsg)
		})
	}
}

// TestVerifierInterface verifies that RedPallasVerifier satisfies the Verifier
// interface and correctly delegates to VerifySpendAuthSig.
func TestVerifierInterface(t *testing.T) {
	rk := mustReadFixture(t, "valid_rk.bin")
	sighash := mustReadFixture(t, "valid_sighash.bin")
	sig := mustReadFixture(t, "valid_sig.bin")

	var v Verifier = NewVerifier()
	err := v.Verify(rk, sighash, sig)
	require.NoError(t, err, "valid signature should pass via Verifier interface")

	wrongSig := mustReadFixture(t, "wrong_sig.bin")
	err = v.Verify(rk, sighash, wrongSig)
	require.Error(t, err, "wrong signature should fail via Verifier interface")
}
