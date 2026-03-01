package shamir

import (
	"crypto/rand"
	"testing"

	"github.com/mikelodder7/curvey"
	"github.com/stretchr/testify/require"
	"github.com/z-cale/zally/crypto/elgamal"
)

// TestPartialDecryptEndToEnd verifies that threshold partial decryption
// produces the same result as full-key decryption via elgamal.DecryptToPoint.
func TestPartialDecryptEndToEnd(t *testing.T) {
	sk, pk := elgamal.KeyGen(rand.Reader)
	G := elgamal.PallasGenerator()

	for _, tc := range []struct {
		name  string
		t, n  int
		value uint64
	}{
		{"2-of-3 v=0", 2, 3, 0},
		{"2-of-3 v=42", 2, 3, 42},
		{"3-of-5 v=1000", 3, 5, 1000},
		{"5-of-5 v=1", 5, 5, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ct, err := elgamal.Encrypt(pk, tc.value, rand.Reader)
			require.NoError(t, err)

			shares, _, err := Split(sk.Scalar, tc.t, tc.n)
			require.NoError(t, err)

			// Compute all partial decryptions, use exactly t.
			partials := make([]PartialDecryption, tc.t)
			for i := 0; i < tc.t; i++ {
				di, err := PartialDecrypt(shares[i].Value, ct.C1)
				require.NoError(t, err)
				partials[i] = PartialDecryption{Index: shares[i].Index, Di: di}
			}

			combined, err := CombinePartials(partials, tc.t)
			require.NoError(t, err)

			// ct.C2 - combined should equal v*G
			got := ct.C2.Sub(combined)
			want := elgamal.DecryptToPoint(sk, ct)

			// Compare compressed representations.
			require.Equal(t, want.ToAffineCompressed(), got.ToAffineCompressed())

			// Also verify against v*G directly for v > 0.
			if tc.value > 0 {
				vScalar := new(curvey.ScalarPallas).New(int(tc.value))
				vG := G.Mul(vScalar)
				require.Equal(t, vG.ToAffineCompressed(), got.ToAffineCompressed())
			} else {
				require.True(t, got.IsIdentity())
			}
		})
	}
}

// TestPartialDecryptAnySubset verifies that any t-sized subset of n partial
// decryptions produces the same combined result.
func TestPartialDecryptAnySubset(t *testing.T) {
	sk, pk := elgamal.KeyGen(rand.Reader)
	threshold := 3
	n := 7
	value := uint64(777)

	ct, err := elgamal.Encrypt(pk, value, rand.Reader)
	require.NoError(t, err)

	shares, _, err := Split(sk.Scalar, threshold, n)
	require.NoError(t, err)

	// Compute all n partial decryptions.
	allPartials := make([]PartialDecryption, n)
	for i := 0; i < n; i++ {
		di, err := PartialDecrypt(shares[i].Value, ct.C1)
		require.NoError(t, err)
		allPartials[i] = PartialDecryption{Index: shares[i].Index, Di: di}
	}

	want := elgamal.DecryptToPoint(sk, ct).ToAffineCompressed()

	subsets := [][]int{
		{0, 1, 2},
		{0, 3, 6},
		{2, 4, 5},
		{1, 5, 6},
		{4, 5, 6},
	}

	for _, subset := range subsets {
		picked := make([]PartialDecryption, len(subset))
		for i, idx := range subset {
			picked[i] = allPartials[idx]
		}

		combined, err := CombinePartials(picked, threshold)
		require.NoError(t, err)

		got := ct.C2.Sub(combined).ToAffineCompressed()
		require.Equal(t, want, got, "subset %v should produce correct decryption", subset)
	}
}

// TestPartialDecryptValidation checks error handling for invalid inputs.
func TestPartialDecryptValidation(t *testing.T) {
	G := elgamal.PallasGenerator()
	share := new(curvey.ScalarPallas).Random(rand.Reader)

	_, err := PartialDecrypt(nil, G)
	require.Error(t, err)
	require.Contains(t, err.Error(), "share must not be nil")

	_, err = PartialDecrypt(share, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "C1 must not be nil")
}

// TestCombinePartialsValidation checks error handling for CombinePartials.
func TestCombinePartialsValidation(t *testing.T) {
	G := elgamal.PallasGenerator()
	share := new(curvey.ScalarPallas).Random(rand.Reader)
	di := G.Mul(share)

	// Fewer than t partials.
	_, err := CombinePartials([]PartialDecryption{{Index: 1, Di: di}}, 2)
	require.Error(t, err)
	require.Contains(t, err.Error(), "need at least 2 partials")

	// Nil Di in one of the partials.
	_, err = CombinePartials([]PartialDecryption{
		{Index: 1, Di: di},
		{Index: 2, Di: nil},
	}, 2)
	require.Error(t, err)
	require.Contains(t, err.Error(), "nil Di")

	// Duplicate indices.
	_, err = CombinePartials([]PartialDecryption{
		{Index: 1, Di: di},
		{Index: 1, Di: di},
	}, 2)
	require.Error(t, err)
	require.Contains(t, err.Error(), "duplicate index")
}
