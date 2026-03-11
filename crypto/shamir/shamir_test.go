package shamir

import (
	"crypto/rand"
	"testing"

	"github.com/mikelodder7/curvey"
	"github.com/stretchr/testify/require"
)

func TestSplitReconstructRoundTrip(t *testing.T) {
	secret := new(curvey.ScalarPallas).Random(rand.Reader)

	for _, tc := range []struct {
		name string
		t, n int
	}{
		{"2-of-3", 2, 3},
		{"3-of-5", 3, 5},
		{"2-of-2", 2, 2},
		{"5-of-5", 5, 5},
		{"4-of-10", 4, 10},
		{"7-of-20", 7, 20},
	} {
		t.Run(tc.name, func(t *testing.T) {
			shares, coeffs, err := Split(secret, tc.t, tc.n)
			require.NoError(t, err)
			require.Len(t, shares, tc.n)
			require.Len(t, coeffs, tc.t)

			// coeffs[0] must equal the secret.
			require.Equal(t, 0, coeffs[0].Cmp(secret))

			// Reconstruct with exactly t shares.
			recovered, err := Reconstruct(shares[:tc.t], tc.t)
			require.NoError(t, err)
			require.Equal(t, 0, recovered.Cmp(secret), "reconstructed secret should match")
		})
	}
}

func TestReconstructAnySubset(t *testing.T) {
	secret := new(curvey.ScalarPallas).Random(rand.Reader)
	threshold := 3
	n := 7

	shares, _, err := Split(secret, threshold, n)
	require.NoError(t, err)

	// Pick several different t-sized subsets and verify each reconstructs.
	subsets := [][]int{
		{0, 1, 2},
		{0, 3, 6},
		{2, 4, 5},
		{1, 5, 6},
		{4, 5, 6},
	}

	for _, subset := range subsets {
		picked := make([]Share, len(subset))
		for i, idx := range subset {
			picked[i] = shares[idx]
		}

		recovered, err := Reconstruct(picked, threshold)
		require.NoError(t, err)
		require.Equal(t, 0, recovered.Cmp(secret), "subset %v should reconstruct correctly", subset)
	}
}

func TestReconstructInsufficientShares(t *testing.T) {
	secret := new(curvey.ScalarPallas).Random(rand.Reader)
	shares, _, err := Split(secret, 3, 5)
	require.NoError(t, err)

	// t-1 = 2 shares should (almost certainly) NOT reconstruct the secret.
	recovered, err := Reconstruct(shares[:2], 2)
	require.NoError(t, err)
	require.NotEqual(t, 0, recovered.Cmp(secret), "2 shares should not reveal a 3-of-5 secret")
}

func TestReconstructTooFewSharesErrors(t *testing.T) {
	secret := new(curvey.ScalarPallas).Random(rand.Reader)
	shares, _, err := Split(secret, 3, 5)
	require.NoError(t, err)

	_, err = Reconstruct(shares[:2], 3)
	require.Error(t, err)
	require.Contains(t, err.Error(), "need at least 3 shares")
}

func TestSplitValidation(t *testing.T) {
	secret := new(curvey.ScalarPallas).Random(rand.Reader)

	_, _, err := Split(nil, 2, 3)
	require.Error(t, err)
	require.Contains(t, err.Error(), "secret must not be nil")

	_, _, err = Split(secret, 1, 3)
	require.Error(t, err)
	require.Contains(t, err.Error(), "threshold t must be >= 2")

	_, _, err = Split(secret, 0, 3)
	require.Error(t, err)
	require.Contains(t, err.Error(), "threshold t must be >= 2")

	_, _, err = Split(secret, 3, 2)
	require.Error(t, err)
	require.Contains(t, err.Error(), "n must be >= t")
}

func TestLagrangeCoefficientsValidation(t *testing.T) {
	_, err := LagrangeCoefficients([]int{}, 0)
	require.Error(t, err)
	require.Contains(t, err.Error(), "must not be empty")

	_, err = LagrangeCoefficients([]int{0, 1}, 0)
	require.Error(t, err)
	require.Contains(t, err.Error(), "must be > 0")

	_, err = LagrangeCoefficients([]int{-1, 2}, 0)
	require.Error(t, err)
	require.Contains(t, err.Error(), "must be > 0")

	_, err = LagrangeCoefficients([]int{1, 2, 1}, 0)
	require.Error(t, err)
	require.Contains(t, err.Error(), "duplicate index")
}

func TestEvalPolynomial(t *testing.T) {
	// f(x) = 3 + 2x + x^2. f(0)=3, f(1)=6, f(2)=11, f(5)=38.
	a0 := new(curvey.ScalarPallas).New(3)
	a1 := new(curvey.ScalarPallas).New(2)
	a2 := new(curvey.ScalarPallas).New(1)
	coeffs := []curvey.Scalar{a0, a1, a2}

	cases := []struct {
		x    int
		want int
	}{
		{0, 3},
		{1, 6},
		{2, 11},
		{5, 38},
	}

	for _, tc := range cases {
		result := evalPolynomial(coeffs, tc.x)
		expected := new(curvey.ScalarPallas).New(tc.want)
		require.Equal(t, 0, result.Cmp(expected), "f(%d) should be %d", tc.x, tc.want)
	}
}

func TestLagrangeCoefficientsPartition(t *testing.T) {
	// For any set of indices, sum of lambda_i * f(i) at target=0 should give f(0).
	// Use a known polynomial: f(x) = 7 + 5x (degree-1), so f(0)=7.
	a0 := new(curvey.ScalarPallas).New(7)
	a1 := new(curvey.ScalarPallas).New(5)
	coeffs := []curvey.Scalar{a0, a1}

	indices := []int{1, 3}
	lambdas, err := LagrangeCoefficients(indices, 0)
	require.NoError(t, err)

	result := new(curvey.ScalarPallas).Zero()
	for j, idx := range indices {
		fIdx := evalPolynomial(coeffs, idx)
		result = result.Add(lambdas[j].Mul(fIdx))
	}

	expected := new(curvey.ScalarPallas).New(7)
	require.Equal(t, 0, result.Cmp(expected), "Lagrange interpolation should recover f(0)")
}

func TestIntToScalar(t *testing.T) {
	s1 := intToScalar(0)
	require.True(t, s1.IsZero())

	s2 := intToScalar(1)
	require.True(t, s2.IsOne())

	s3 := intToScalar(42)
	expected := new(curvey.ScalarPallas).New(42)
	require.Equal(t, 0, s3.Cmp(expected))
}
