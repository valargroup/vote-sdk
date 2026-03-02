//go:build halo2

package ante_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/z-cale/zally/crypto/redpallas"
	"github.com/z-cale/zally/crypto/zkp"
	"github.com/z-cale/zally/crypto/zkp/halo2"
	"github.com/z-cale/zally/x/vote/ante"
	"github.com/z-cale/zally/x/vote/types"
)

// repoRoot returns the absolute path to the repository root by walking up
// from this test file's location (x/vote/ante/).
func repoRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok, "runtime.Caller failed")
	// thisFile = .../x/vote/ante/validate_halo2_test.go → go up 4 levels
	return filepath.Join(filepath.Dir(thisFile), "..", "..", "..")
}

// mustReadFixture reads a binary fixture from crypto/zkp/testdata/.
func mustReadFixture(t *testing.T, name string) []byte {
	t.Helper()
	path := filepath.Join(repoRoot(t), "crypto", "zkp", "testdata", name)
	data, err := os.ReadFile(path)
	require.NoError(t, err, "failed to read fixture %s", path)
	return data
}

// toyAsDelegationVerifier uses the toy circuit verifier for delegation so that
// the full ante pipeline can be tested with the only real proof fixture we have
// (toy_valid_proof.bin). The real delegation circuit expects a different proof
// format; once a delegation proof fixture exists, tests can switch to
// halo2.NewVerifier() and that fixture.
type toyAsDelegationVerifier struct{}

func (toyAsDelegationVerifier) VerifyDelegation(proof []byte, inputs zkp.DelegationInputs) error {
	return halo2.VerifyToyProof(proof, inputs.VanCmx)
}

func (toyAsDelegationVerifier) VerifyVoteCommitment(proof []byte, _ zkp.VoteCommitmentInputs) error {
	return nil
}

func (toyAsDelegationVerifier) VerifyVoteShare(proof []byte, _ zkp.VoteShareInputs) error {
	return nil
}

// TestHalo2DelegationValidProof runs the full ante validation pipeline with a
// real Halo2 toy proof. The MsgDelegateVote.Proof carries the real proof
// bytes and VanCmx carries the 32-byte public input (toy circuit convention).
func TestHalo2DelegationValidProof(t *testing.T) {
	proof := mustReadFixture(t, "toy_valid_proof.bin")
	publicInput := mustReadFixture(t, "toy_valid_input.bin")

	// Build a MsgDelegateVote with the real proof.
	// VanCmx carries the toy circuit public input; Rk is a dummy 32-byte value
	// (not used by the toy circuit, but required by ValidateBasic).
	msg := &types.MsgDelegateVote{
		Rk:                  make([]byte, 32),
		SpendAuthSig:        make([]byte, 64),
		SignedNoteNullifier: make([]byte, 32),
		CmxNew:              make([]byte, 32),
		VanCmx:              publicInput, // 32-byte toy circuit public input
		GovNullifiers: [][]byte{
			make([]byte, 32),
		},
		Proof:       proof,
		VoteRoundId: testRoundID,
	}
	msg.Sighash = make([]byte, 32) // any 32 bytes; chain checks length + sig only, mock sig verifier accepts any sig

	// Use toy-as-delegation verifier so the toy proof fixture passes; mock the
	// signature verifier (RedPallas is not under test here).
	opts := ante.ValidateOpts{
		SigVerifier: redpallas.NewMockVerifier(),
		ZKPVerifier: toyAsDelegationVerifier{},
	}

	// Create a test suite for the keeper/context setup, then run through
	// the full ValidateVoteTx pipeline.
	s := new(ValidateTestSuite)
	s.SetT(t)
	s.SetupTest()
	s.setupActiveRound()

	err := ante.ValidateVoteTx(s.ctx, msg, s.keeper, opts)
	require.NoError(t, err, "valid Halo2 toy proof should pass the ante handler")
}

// TestHalo2DelegationWrongInput verifies that a real Halo2 proof fails when
// paired with the wrong public input (i.e. the full pipeline returns
// ErrInvalidProof).
func TestHalo2DelegationWrongInput(t *testing.T) {
	proof := mustReadFixture(t, "toy_valid_proof.bin")
	wrongInput := mustReadFixture(t, "toy_wrong_input.bin")

	msg := &types.MsgDelegateVote{
		Rk:                  make([]byte, 32),
		SpendAuthSig:        make([]byte, 64),
		SignedNoteNullifier: make([]byte, 32),
		CmxNew:              make([]byte, 32),
		VanCmx:              wrongInput, // wrong public input
		GovNullifiers: [][]byte{
			make([]byte, 32),
		},
		Proof:       proof,
		VoteRoundId: testRoundID,
	}
	msg.Sighash = make([]byte, 32) // any 32 bytes; mock sig accepts; ZKP will fail

	opts := ante.ValidateOpts{
		SigVerifier: redpallas.NewMockVerifier(),
		ZKPVerifier: toyAsDelegationVerifier{},
	}

	s := new(ValidateTestSuite)
	s.SetT(t)
	s.SetupTest()
	s.setupActiveRound()

	err := ante.ValidateVoteTx(s.ctx, msg, s.keeper, opts)
	require.Error(t, err, "wrong public input should fail verification")
	require.ErrorIs(t, err, types.ErrInvalidProof, "should wrap ErrInvalidProof")
}
