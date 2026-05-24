package cli

import (
	"crypto/rand"
	"math/big"
	"testing"

	"github.com/mikelodder7/curvey"
	"github.com/stretchr/testify/require"

	"github.com/valargroup/vote-sdk/crypto/elgamal"
	"github.com/valargroup/vote-sdk/crypto/shamir"
	"github.com/valargroup/vote-sdk/x/vote/types"
)

func TestVerifyTallyEntry(t *testing.T) {
	secret := scalarFromUint64ForTest(t, 7)
	shares, coeffs, err := shamir.Split(secret, 2, 3)
	require.NoError(t, err)
	commitments, err := shamir.FeldmanCommit(elgamal.PallasGenerator(), coeffs)
	require.NoError(t, err)
	serializedCommitments := make([][]byte, len(commitments))
	for i, commitment := range commitments {
		serializedCommitments[i] = commitment.ToAffineCompressed()
	}

	pk := &elgamal.PublicKey{Point: elgamal.PallasGenerator().Mul(secret)}
	ct, err := elgamal.Encrypt(pk, 42, rand.Reader)
	require.NoError(t, err)
	accumulator, err := elgamal.MarshalCiphertext(ct)
	require.NoError(t, err)

	partials := make([]*types.StoredPartialDecryption, 2)
	for i, share := range shares[:2] {
		partialPoint, err := shamir.PartialDecrypt(share.Value, ct.C1)
		require.NoError(t, err)
		dleqProof, err := elgamal.GeneratePartialDecryptDLEQ(share.Value, ct.C1)
		require.NoError(t, err)
		partials[i] = &types.StoredPartialDecryption{
			ValidatorIndex: uint32(share.Index),
			ProposalId:     1,
			VoteDecision:   0,
			PartialDecrypt: partialPoint.ToAffineCompressed(),
			DleqProof:      dleqProof,
		}
	}

	round := &types.VoteRound{Threshold: 2, FeldmanCommitments: serializedCommitments}
	key := tallyKey{proposalID: 1, voteDecision: 0}
	result := &types.TallyResult{ProposalId: 1, VoteDecision: 0, TotalValue: 42}

	check := verifyTallyEntry(round, result, accumulator, partials, key)
	require.True(t, check.Verified, check.Failure)

	result.TotalValue = 43
	check = verifyTallyEntry(round, result, accumulator, partials, key)
	require.False(t, check.Verified)
	require.Contains(t, check.Failure, "combined_partial")

	result.TotalValue = 42
	partials[0].DleqProof = append([]byte(nil), partials[0].DleqProof...)
	partials[0].DleqProof[0] ^= 0x01
	check = verifyTallyEntry(round, result, accumulator, partials, key)
	require.False(t, check.Verified)
	require.Contains(t, check.Failure, "invalid DLEQ proof")
}

func scalarFromUint64ForTest(t *testing.T, v uint64) curvey.Scalar {
	t.Helper()
	scalar, err := new(curvey.ScalarPallas).SetBigInt(new(big.Int).SetUint64(v))
	require.NoError(t, err)
	return scalar
}
