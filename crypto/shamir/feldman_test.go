package shamir

import (
	"crypto/rand"
	"testing"

	"github.com/mikelodder7/curvey"
	"github.com/mikelodder7/curvey/native/pasta"
	"github.com/stretchr/testify/require"
)

// offCurvePallas returns a PointPallas that is NOT on the Pallas curve.
// Pallas equation: y^2 = x^3 + 5. With x=1, y=1, z=1: 1 != 6.
func offCurvePallas() curvey.Point {
	ep := pasta.PointNew()
	ep.X.SetOne()
	ep.Y.SetOne()
	ep.Z.SetOne()
	return &curvey.PointPallas{EllipticPoint4: ep}
}

// pallasG returns the Pallas SpendAuthG generator used by the rest of the
// codebase. Duplicated here to avoid importing crypto/elgamal from tests.
func pallasG() curvey.Point {
	b := []byte{
		0x63, 0xc9, 0x75, 0xb8, 0x84, 0x72, 0x1a, 0x8d,
		0x0c, 0xa1, 0x70, 0x7b, 0xe3, 0x0c, 0x7f, 0x0c,
		0x5f, 0x44, 0x5f, 0x3e, 0x7c, 0x18, 0x8d, 0x3b,
		0x06, 0xd6, 0xf1, 0x28, 0xb3, 0x23, 0x55, 0xb7,
	}
	g, err := new(curvey.PointPallas).Identity().FromAffineCompressed(b)
	if err != nil {
		panic(err)
	}
	return g
}

func TestFeldmanCommitBasic(t *testing.T) {
	G := pallasG()
	secret := new(curvey.ScalarPallas).Random(rand.Reader)
	threshold := 3
	n := 5

	shares, coeffs, err := Split(secret, threshold, n)
	require.NoError(t, err)

	commitments, err := FeldmanCommit(G, coeffs)
	require.NoError(t, err)
	require.Len(t, commitments, threshold)

	// C_0 must equal secret * G (the public key).
	expectedPk := G.Mul(secret)
	require.True(t, commitments[0].Equal(expectedPk), "C_0 should equal secret*G")

	// Every share must verify against the commitments.
	for _, s := range shares {
		ok, err := VerifyFeldmanShare(G, commitments, s.Index, s.Value)
		require.NoError(t, err)
		require.True(t, ok, "share %d should verify", s.Index)
	}
}

func TestFeldmanVerifyRejectsTamperedShare(t *testing.T) {
	G := pallasG()
	secret := new(curvey.ScalarPallas).Random(rand.Reader)

	shares, coeffs, err := Split(secret, 3, 5)
	require.NoError(t, err)

	commitments, err := FeldmanCommit(G, coeffs)
	require.NoError(t, err)

	// Tamper with the share value.
	tampered := shares[2].Value.Add(new(curvey.ScalarPallas).New(1))
	ok, err := VerifyFeldmanShare(G, commitments, shares[2].Index, tampered)
	require.NoError(t, err)
	require.False(t, ok, "tampered share should not verify")
}

func TestFeldmanVerifyRejectsWrongIndex(t *testing.T) {
	G := pallasG()
	secret := new(curvey.ScalarPallas).Random(rand.Reader)

	shares, coeffs, err := Split(secret, 2, 4)
	require.NoError(t, err)

	commitments, err := FeldmanCommit(G, coeffs)
	require.NoError(t, err)

	// Use share_1's value at index 2 — should fail.
	ok, err := VerifyFeldmanShare(G, commitments, 2, shares[0].Value)
	require.NoError(t, err)
	require.False(t, ok, "share at wrong index should not verify")
}

func TestEvalCommitmentPolynomialMatchesShareTimesG(t *testing.T) {
	G := pallasG()
	secret := new(curvey.ScalarPallas).Random(rand.Reader)

	for _, tc := range []struct {
		name string
		t, n int
	}{
		{"2-of-2", 2, 2},
		{"2-of-3", 2, 3},
		{"3-of-5", 3, 5},
		{"5-of-10", 5, 10},
	} {
		t.Run(tc.name, func(t *testing.T) {
			shares, coeffs, err := Split(secret, tc.t, tc.n)
			require.NoError(t, err)

			commitments, err := FeldmanCommit(G, coeffs)
			require.NoError(t, err)

			for _, s := range shares {
				vk, err := EvalCommitmentPolynomial(commitments, s.Index)
				require.NoError(t, err)

				expected := G.Mul(s.Value)
				require.True(t, vk.Equal(expected),
					"EvalCommitmentPolynomial(%d) should equal share*G", s.Index)
			}
		})
	}
}

func TestFeldmanCommitValidation(t *testing.T) {
	G := pallasG()
	oneCoeff := []curvey.Scalar{new(curvey.ScalarPallas).New(1)}

	_, err := FeldmanCommit(nil, oneCoeff)
	require.Error(t, err)
	require.Contains(t, err.Error(), "generator G must not be nil")

	identity := new(curvey.PointPallas).Identity()
	_, err = FeldmanCommit(identity, oneCoeff)
	require.Error(t, err)
	require.Contains(t, err.Error(), "generator G must not be the identity point")

	_, err = FeldmanCommit(G, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "must not be empty")

	_, err = FeldmanCommit(G, []curvey.Scalar{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "must not be empty")

	_, err = FeldmanCommit(G, []curvey.Scalar{nil})
	require.Error(t, err)
	require.Contains(t, err.Error(), "coefficient 0 is nil")
}

func TestVerifyFeldmanShareValidation(t *testing.T) {
	G := pallasG()
	s := new(curvey.ScalarPallas).New(42)
	c := []curvey.Point{G.Mul(s)}

	_, err := VerifyFeldmanShare(nil, c, 1, s)
	require.Error(t, err)
	require.Contains(t, err.Error(), "generator G must not be nil")

	identity := new(curvey.PointPallas).Identity()
	_, err = VerifyFeldmanShare(identity, c, 1, s)
	require.Error(t, err)
	require.Contains(t, err.Error(), "generator G must not be the identity point")

	_, err = VerifyFeldmanShare(G, c, 1, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "share must not be nil")

	_, err = VerifyFeldmanShare(G, c, 0, s)
	require.Error(t, err)
	require.Contains(t, err.Error(), "index must be > 0")

	_, err = VerifyFeldmanShare(G, c, -1, s)
	require.Error(t, err)
	require.Contains(t, err.Error(), "index must be > 0")
}

func TestEvalCommitmentPolynomialValidation(t *testing.T) {
	_, err := EvalCommitmentPolynomial(nil, 1)
	require.Error(t, err)
	require.Contains(t, err.Error(), "must not be empty")

	_, err = EvalCommitmentPolynomial([]curvey.Point{}, 1)
	require.Error(t, err)
	require.Contains(t, err.Error(), "must not be empty")

	G := pallasG()
	_, err = EvalCommitmentPolynomial([]curvey.Point{G}, 0)
	require.Error(t, err)
	require.Contains(t, err.Error(), "index must be > 0")

	_, err = EvalCommitmentPolynomial([]curvey.Point{nil}, 1)
	require.Error(t, err)
	require.Contains(t, err.Error(), "commitment 0 is nil")

	bad := offCurvePallas()
	require.False(t, bad.IsOnCurve(), "test helper must produce off-curve point")

	_, err = EvalCommitmentPolynomial([]curvey.Point{bad}, 1)
	require.Error(t, err)
	require.Contains(t, err.Error(), "commitment 0 is not on the curve")

	_, err = EvalCommitmentPolynomial([]curvey.Point{G, bad}, 1)
	require.Error(t, err)
	require.Contains(t, err.Error(), "commitment 1 is not on the curve")
}

func TestFeldmanRejectsOffCurveCommitments(t *testing.T) {
	G := pallasG()
	secret := new(curvey.ScalarPallas).Random(rand.Reader)

	shares, coeffs, err := Split(secret, 2, 3)
	require.NoError(t, err)

	commitments, err := FeldmanCommit(G, coeffs)
	require.NoError(t, err)

	bad := offCurvePallas()

	// Replacing a legitimate commitment with an off-curve point must be caught.
	poisoned := []curvey.Point{commitments[0], bad}
	ok, err := VerifyFeldmanShare(G, poisoned, shares[0].Index, shares[0].Value)
	require.Error(t, err)
	require.False(t, ok)
	require.Contains(t, err.Error(), "not on the curve")

	// Off-curve in the first position.
	poisoned = []curvey.Point{bad, commitments[1]}
	ok, err = VerifyFeldmanShare(G, poisoned, shares[0].Index, shares[0].Value)
	require.Error(t, err)
	require.False(t, ok)
	require.Contains(t, err.Error(), "not on the curve")
}

func TestFeldmanRejectsIdentityGenerator(t *testing.T) {
	identity := new(curvey.PointPallas).Identity()
	secret := new(curvey.ScalarPallas).Random(rand.Reader)

	shares, coeffs, err := Split(secret, 2, 3)
	require.NoError(t, err)

	_, err = FeldmanCommit(identity, coeffs)
	require.Error(t, err)
	require.Contains(t, err.Error(), "identity point")

	G := pallasG()
	commitments, err := FeldmanCommit(G, coeffs)
	require.NoError(t, err)

	_, err = VerifyFeldmanShare(identity, commitments, shares[0].Index, shares[0].Value)
	require.Error(t, err)
	require.Contains(t, err.Error(), "identity point")

	bogusShare := new(curvey.ScalarPallas).New(999)
	_, err = VerifyFeldmanShare(identity, commitments, shares[0].Index, bogusShare)
	require.Error(t, err)
	require.Contains(t, err.Error(), "identity point")
}

func TestFeldmanDegreeOnePolynomial(t *testing.T) {
	G := pallasG()

	// f(x) = 7 + 5x  →  C_0 = 7*G, C_1 = 5*G
	a0 := new(curvey.ScalarPallas).New(7)
	a1 := new(curvey.ScalarPallas).New(5)
	coeffs := []curvey.Scalar{a0, a1}

	commitments, err := FeldmanCommit(G, coeffs)
	require.NoError(t, err)

	// f(1) = 12, f(2) = 17, f(3) = 22
	cases := []struct {
		index int
		value int
	}{
		{1, 12},
		{2, 17},
		{3, 22},
	}
	for _, tc := range cases {
		share := new(curvey.ScalarPallas).New(tc.value)
		ok, err := VerifyFeldmanShare(G, commitments, tc.index, share)
		require.NoError(t, err)
		require.True(t, ok, "f(%d)=%d should verify", tc.index, tc.value)

		// Wrong value should fail.
		wrong := new(curvey.ScalarPallas).New(tc.value + 1)
		ok, err = VerifyFeldmanShare(G, commitments, tc.index, wrong)
		require.NoError(t, err)
		require.False(t, ok, "f(%d)=%d should not verify", tc.index, tc.value+1)
	}
}
