//go:build redpallas

package ante_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/z-cale/zally/crypto/redpallas"
	"github.com/z-cale/zally/crypto/zkp"
	"github.com/z-cale/zally/x/vote/ante"
	"github.com/z-cale/zally/x/vote/types"
)

// rpRepoRoot returns the absolute path to the repository root by walking up
// from this test file's location (x/vote/ante/).
// Named rpRepoRoot to avoid collision with the halo2 test's repoRoot when
// both build tags are active.
func rpRepoRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok, "runtime.Caller failed")
	// thisFile = .../x/vote/ante/validate_redpallas_test.go → go up 4 levels
	return filepath.Join(filepath.Dir(thisFile), "..", "..", "..")
}

// rpMustReadFixture reads a binary fixture from crypto/redpallas/testdata/.
func rpMustReadFixture(t *testing.T, name string) []byte {
	t.Helper()
	path := filepath.Join(rpRepoRoot(t), "crypto", "redpallas", "testdata", name)
	data, err := os.ReadFile(path)
	require.NoError(t, err, "failed to read fixture %s", path)
	return data
}

// TestRedPallasDelegationValidSig runs the full ante validation pipeline with a
// real RedPallas SpendAuth signature. The ZKP verifier is mocked since only the
// signature verification is under test here.
//
// The message layout must match the canonical encoding in sdk/circuits/tests/generate_fixtures.rs
// (canonical_delegation_payload_for_fixture) so that types.ComputeDelegationSighash(msg) equals
// the fixture sighash. Fields: testRoundID (32×0x01), rk from fixture, rest zeros, gov_nullifiers
// one element of 32 zeros (chain pads to 4×32 when computing sighash).
func TestRedPallasDelegationValidSig(t *testing.T) {
	rk := rpMustReadFixture(t, "valid_rk.bin")
	sighash := rpMustReadFixture(t, "valid_sighash.bin")
	sig := rpMustReadFixture(t, "valid_sig.bin")

	// Build a MsgDelegateVote that matches the canonical payload used in generate_fixtures.
	msg := &types.MsgDelegateVote{
		Rk:                  rk,      // 32-byte real verification key
		SpendAuthSig:        sig,     // 64-byte real signature
		Sighash:             sighash, // 32-byte sighash the signature covers
		SignedNoteNullifier: make([]byte, 32),
		CmxNew:              make([]byte, 32),
		EncMemo:             make([]byte, 64),
		VanCmx:              make([]byte, 32),
		GovNullifiers: [][]byte{
			make([]byte, 32),
		},
		Proof:       make([]byte, 192), // dummy proof (ZKP is mocked)
		VoteRoundId: testRoundID,
	}

	// Use the real RedPallas verifier but mock the ZKP verifier
	// (ZKP is not under test here).
	opts := ante.ValidateOpts{
		SigVerifier: redpallas.NewVerifier(),
		ZKPVerifier: zkp.NewMockVerifier(),
	}

	// Create a test suite for the keeper/context setup, then run through
	// the full ValidateVoteTx pipeline.
	s := new(ValidateTestSuite)
	s.SetT(t)
	s.SetupTest()
	s.setupActiveRound()

	err := ante.ValidateVoteTx(s.ctx, msg, s.keeper, opts)
	require.NoError(t, err, "valid RedPallas signature should pass the ante handler")
}

// TestRedPallasDelegationWrongSig verifies that a real RedPallas signature
// over the wrong message fails verification when run through the full ante
// pipeline (i.e. returns ErrInvalidSignature).
// Message layout matches the canonical encoding so sighash check passes; only the sig is wrong.
func TestRedPallasDelegationWrongSig(t *testing.T) {
	rk := rpMustReadFixture(t, "valid_rk.bin")
	sighash := rpMustReadFixture(t, "valid_sighash.bin")
	wrongSig := rpMustReadFixture(t, "wrong_sig.bin")

	msg := &types.MsgDelegateVote{
		Rk:                  rk,       // correct verification key
		SpendAuthSig:        wrongSig, // signature over a different message
		Sighash:             sighash,  // same sighash — wrong sig should still fail
		SignedNoteNullifier: make([]byte, 32),
		CmxNew:              make([]byte, 32),
		EncMemo:             make([]byte, 64),
		VanCmx:              make([]byte, 32),
		GovNullifiers: [][]byte{
			make([]byte, 32),
		},
		Proof:       make([]byte, 192),
		VoteRoundId: testRoundID,
	}

	opts := ante.ValidateOpts{
		SigVerifier: redpallas.NewVerifier(),
		ZKPVerifier: zkp.NewMockVerifier(),
	}

	s := new(ValidateTestSuite)
	s.SetT(t)
	s.SetupTest()
	s.setupActiveRound()

	err := ante.ValidateVoteTx(s.ctx, msg, s.keeper, opts)
	require.Error(t, err, "wrong signature should fail verification")
	require.ErrorIs(t, err, types.ErrInvalidSignature, "should wrap ErrInvalidSignature")
}
