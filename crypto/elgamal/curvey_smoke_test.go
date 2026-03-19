package elgamal

import (
	"crypto/rand"
	"testing"

	"github.com/mikelodder7/curvey"
	"github.com/stretchr/testify/require"
)

// pallasPoint returns a properly initialized PointPallas suitable for use as
// a method receiver (e.g. for FromAffineCompressed). A bare new(curvey.PointPallas)
// has a nil inner EllipticPoint4 and will panic on methods that access curve params.
func pallasPoint() *curvey.PointPallas {
	return new(curvey.PointPallas).Identity().(*curvey.PointPallas)
}

// TestCurveySmokeTest verifies that the curvey library's Pallas curve
// implementation works: scalar multiplication, point identity,
// on-curve checks, and compressed serialization round-trip.
func TestCurveySmokeTest(t *testing.T) {
	G := new(curvey.PointPallas).Generator()
	s := new(curvey.ScalarPallas).New(7)
	p := G.Mul(s)
	require.False(t, p.IsIdentity())
	require.True(t, p.IsOnCurve())

	// Serialize round-trip
	bs := p.ToAffineCompressed()
	require.Len(t, bs, 32)
	p2, err := pallasPoint().FromAffineCompressed(bs)
	require.NoError(t, err)
	require.True(t, p.Equal(p2))
}

// TestPallasGeneratorNotIdentity verifies the generator is a valid non-identity point.
func TestPallasGeneratorNotIdentity(t *testing.T) {
	G := new(curvey.PointPallas).Generator()
	require.False(t, G.IsIdentity())
	require.True(t, G.IsOnCurve())
}

// TestPallasIdentity verifies the identity (point at infinity) behaves correctly.
func TestPallasIdentity(t *testing.T) {
	id := new(curvey.PointPallas).Identity()
	require.True(t, id.IsIdentity())

	G := new(curvey.PointPallas).Generator()
	// G + identity = G
	sum := G.Add(id)
	require.True(t, sum.Equal(G))
}

// TestPallasScalarArithmetic verifies basic scalar field operations.
func TestPallasScalarArithmetic(t *testing.T) {
	a := new(curvey.ScalarPallas).New(5)
	b := new(curvey.ScalarPallas).New(3)

	// a + b = 8
	sum := a.Add(b)
	expected := new(curvey.ScalarPallas).New(8)
	G := new(curvey.PointPallas).Generator()
	// Verify via point multiplication: (a+b)*G == a*G + b*G
	lhs := G.Mul(sum)
	rhs := G.Mul(a).Add(G.Mul(b))
	require.True(t, lhs.Equal(rhs))

	// Also check with expected
	lhsExpected := G.Mul(expected)
	require.True(t, lhs.Equal(lhsExpected))
}

// TestPallasPointAddition verifies that point addition is consistent
// with scalar multiplication: (a+b)*G == a*G + b*G.
func TestPallasPointAddition(t *testing.T) {
	G := new(curvey.PointPallas).Generator()
	a := new(curvey.ScalarPallas).New(42)
	b := new(curvey.ScalarPallas).New(58)
	sum := a.Add(b) // 100

	lhs := G.Mul(sum)
	rhs := G.Mul(a).Add(G.Mul(b))
	require.True(t, lhs.Equal(rhs))
}

// TestPallasNegation verifies that P + (-P) = identity.
func TestPallasNegation(t *testing.T) {
	G := new(curvey.PointPallas).Generator()
	s := new(curvey.ScalarPallas).New(123)
	p := G.Mul(s)
	neg := p.Neg()
	sum := p.Add(neg)
	require.True(t, sum.IsIdentity())
}

// TestPallasRandomScalar verifies that random scalar generation works
// and produces non-zero values.
func TestPallasRandomScalar(t *testing.T) {
	s := new(curvey.ScalarPallas).Random(rand.Reader)
	require.False(t, s.IsZero())

	G := new(curvey.PointPallas).Generator()
	p := G.Mul(s)
	require.False(t, p.IsIdentity())
	require.True(t, p.IsOnCurve())
}

// TestPallasIdentitySerialization verifies identity point serialization.
// The identity (point at infinity) serializes to 32 zero bytes; round-trip
// through FromAffineCompressed is not expected to work because (0,0) is not
// on the curve in affine form. This is standard for projective-coordinate
// elliptic curve libraries.
func TestPallasIdentitySerialization(t *testing.T) {
	id := new(curvey.PointPallas).Identity()
	bs := id.ToAffineCompressed()
	require.Len(t, bs, 32)

	// Identity serializes to all-zero bytes
	var zero [32]byte
	require.Equal(t, zero[:], bs)

	// Verify we can detect identity via the all-zeros pattern
	// (deserialization of all-zeros is not valid affine, so we detect
	// identity by checking the bytes directly)
}

// TestPallasDoubling verifies that 2*G = G + G.
func TestPallasDoubling(t *testing.T) {
	G := new(curvey.PointPallas).Generator()
	two := new(curvey.ScalarPallas).New(2)
	doubled := G.Mul(two)
	added := G.Add(G)
	require.True(t, doubled.Equal(added))
}
