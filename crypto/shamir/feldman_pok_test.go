package shamir

import (
	"crypto/rand"
	"testing"

	"github.com/mikelodder7/curvey"
	"github.com/stretchr/testify/require"

	"github.com/valargroup/vote-sdk/crypto/elgamal"
)

func TestFeldmanOpeningProofRoundTrip(t *testing.T) {
	G, coeffs, commitments, roundID, validator := feldmanOpeningProofFixture(3)

	proof, err := GenerateFeldmanOpeningProof(G, coeffs, commitments, roundID, validator, rand.Reader)
	require.NoError(t, err)
	require.Len(t, proof, len(commitments)*FeldmanOpeningProofPerCommitmentSize)

	err = VerifyFeldmanOpeningProof(G, commitments, roundID, validator, proof)
	require.NoError(t, err)
}

func TestFeldmanOpeningProofRejectsMissingProof(t *testing.T) {
	G, _, commitments, roundID, validator := feldmanOpeningProofFixture(2)

	err := VerifyFeldmanOpeningProof(G, commitments, roundID, validator, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "expected 128 proof bytes, got 0")
}

func TestFeldmanOpeningProofRejectsWrongRound(t *testing.T) {
	G, coeffs, commitments, roundID, validator := feldmanOpeningProofFixture(2)
	proof, err := GenerateFeldmanOpeningProof(G, coeffs, commitments, roundID, validator, rand.Reader)
	require.NoError(t, err)

	err = VerifyFeldmanOpeningProof(G, commitments, []byte("other-round"), validator, proof)
	require.Error(t, err)
	require.Contains(t, err.Error(), "verification failed")
}

func TestFeldmanOpeningProofRejectsWrongValidator(t *testing.T) {
	G, coeffs, commitments, roundID, validator := feldmanOpeningProofFixture(2)
	proof, err := GenerateFeldmanOpeningProof(G, coeffs, commitments, roundID, validator, rand.Reader)
	require.NoError(t, err)

	err = VerifyFeldmanOpeningProof(G, commitments, roundID, "svvaloper1other", proof)
	require.Error(t, err)
	require.Contains(t, err.Error(), "verification failed")
}

func TestFeldmanOpeningProofRejectsTamperedCommitment(t *testing.T) {
	G, coeffs, commitments, roundID, validator := feldmanOpeningProofFixture(2)
	proof, err := GenerateFeldmanOpeningProof(G, coeffs, commitments, roundID, validator, rand.Reader)
	require.NoError(t, err)

	tampered := append([]curvey.Point(nil), commitments...)
	tampered[0] = G.Mul(new(curvey.ScalarPallas).New(42))

	err = VerifyFeldmanOpeningProof(G, tampered, roundID, validator, proof)
	require.Error(t, err)
	require.Contains(t, err.Error(), "verification failed")
}

func TestFeldmanOpeningProofRejectsReorderedCommitments(t *testing.T) {
	G, coeffs, commitments, roundID, validator := feldmanOpeningProofFixture(2)
	proof, err := GenerateFeldmanOpeningProof(G, coeffs, commitments, roundID, validator, rand.Reader)
	require.NoError(t, err)

	reordered := []curvey.Point{commitments[1], commitments[0]}
	err = VerifyFeldmanOpeningProof(G, reordered, roundID, validator, proof)
	require.Error(t, err)
	require.Contains(t, err.Error(), "verification failed")
}

func TestFeldmanOpeningProofRejectsUnknownOpeningAtGeneration(t *testing.T) {
	G, coeffs, commitments, roundID, validator := feldmanOpeningProofFixture(2)
	commitments[0] = G.Mul(new(curvey.ScalarPallas).New(99))

	_, err := GenerateFeldmanOpeningProof(G, coeffs, commitments, roundID, validator, rand.Reader)
	require.Error(t, err)
	require.Contains(t, err.Error(), "does not open commitment")
}

func feldmanOpeningProofFixture(n int) (curvey.Point, []curvey.Scalar, []curvey.Point, []byte, string) {
	G := elgamal.PallasGenerator()
	coeffs := make([]curvey.Scalar, n)
	commitments := make([]curvey.Point, n)
	for i := range coeffs {
		coeffs[i] = new(curvey.ScalarPallas).New(i + 1)
		commitments[i] = G.Mul(coeffs[i])
	}
	return G, coeffs, commitments, []byte("round-1"), "svvaloper1validator"
}
