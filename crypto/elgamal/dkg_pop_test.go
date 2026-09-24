package elgamal

import (
	"bytes"
	"testing"

	"github.com/mikelodder7/curvey"
	"github.com/stretchr/testify/require"
)

func TestConstantTermProof(t *testing.T) {
	G := PallasGenerator()
	secret := new(curvey.ScalarPallas).New(17)
	commitments := [][]byte{
		G.Mul(secret).ToAffineCompressed(),
		G.Mul(new(curvey.ScalarPallas).New(23)).ToAffineCompressed(),
	}
	roundID := bytes.Repeat([]byte{0x42}, 32)
	proof, err := GenerateConstantTermProof(secret, "svote-1", roundID,
		"svvaloper1dealer", commitments)
	require.NoError(t, err)
	require.Len(t, proof, constantTermProofSize)
	require.NoError(t, VerifyConstantTermProof(proof, "svote-1", roundID,
		"svvaloper1dealer", commitments))

	tests := []struct {
		name        string
		proof       []byte
		chainID     string
		roundID     []byte
		dealer      string
		commitments [][]byte
	}{
		{"missing proof", nil, "svote-1", roundID, "svvaloper1dealer", commitments},
		{"other chain", proof, "svote-2", roundID, "svvaloper1dealer", commitments},
		{"other round", proof, "svote-1", bytes.Repeat([]byte{0x43}, 32), "svvaloper1dealer", commitments},
		{"other dealer", proof, "svote-1", roundID, "svvaloper1other", commitments},
		{"changed coefficient", proof, "svote-1", roundID, "svvaloper1dealer", [][]byte{commitments[0], G.ToAffineCompressed()}},
		{"changed constant", proof, "svote-1", roundID, "svvaloper1dealer", [][]byte{G.ToAffineCompressed(), commitments[1]}},
		{"identity nonce", append(make([]byte, 32), proof[32:]...), "svote-1", roundID, "svvaloper1dealer", commitments},
		{"invalid scalar", append(append([]byte(nil), proof[:32]...), bytes.Repeat([]byte{0xff}, 32)...), "svote-1", roundID, "svvaloper1dealer", commitments},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.Error(t, VerifyConstantTermProof(tc.proof, tc.chainID,
				tc.roundID, tc.dealer, tc.commitments))
		})
	}

	_, err = GenerateConstantTermProof(new(curvey.ScalarPallas).New(18),
		"svote-1", roundID, "svvaloper1dealer", commitments)
	require.ErrorContains(t, err, "does not match")
}
