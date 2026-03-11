package elgamal

import (
	"crypto/rand"
	"math/big"
	"testing"

	"github.com/mikelodder7/curvey"
	"github.com/stretchr/testify/require"
)

// ===========================================================================
// Pallas curve constants from the Pasta specification
// (https://electriccoin.co/blog/the-pasta-curves-for-halo-2-and-beyond/)
//
// These are hardcoded here so we detect if the library ever drifts from the
// canonical values — whether by a bug, a dependency update, or a supply-chain
// compromise.
// ===========================================================================

// pallasP is the Pallas base field modulus.
//
//	p = 0x40000000000000000000000000000000224698fc094cf91b992d30ed00000001
func pallasP() *big.Int {
	p, ok := new(big.Int).SetString(
		"40000000000000000000000000000000224698fc094cf91b992d30ed00000001", 16)
	if !ok {
		panic("bad Pallas p constant")
	}
	return p
}

// pallasQ is the Pallas scalar field order (group order).
//
//	q = 0x40000000000000000000000000000000224698fc0994a8dd8c46eb2100000001
func pallasQ() *big.Int {
	q, ok := new(big.Int).SetString(
		"40000000000000000000000000000000224698fc0994a8dd8c46eb2100000001", 16)
	if !ok {
		panic("bad Pallas q constant")
	}
	return q
}

// ---------------------------------------------------------------------------
// A. Curve constants match the Pasta specification
// ---------------------------------------------------------------------------

// TestPallasBaseFieldModulus verifies the Pallas base field prime p used by
// curvey matches the canonical value from the Pasta curves specification.
func TestPallasBaseFieldModulus(t *testing.T) {
	got := curvey.Pallas().Params().P
	require.Equal(t, pallasP(), got,
		"Pallas base field modulus p does not match specification")
}

// TestPallasGroupOrder verifies the Pallas scalar field order q used by curvey
// matches the canonical value from the Pasta curves specification.
func TestPallasGroupOrder(t *testing.T) {
	got := curvey.Pallas().Params().N
	require.Equal(t, pallasQ(), got,
		"Pallas group order q does not match specification")
}

// TestPallasBitSize verifies the curve reports 255-bit field elements.
func TestPallasBitSize(t *testing.T) {
	require.Equal(t, 255, curvey.Pallas().Params().BitSize,
		"Pallas BitSize should be 255")
}

// TestPallasGeneratorOnCurve verifies the generator coordinates returned by
// curvey satisfy the curve equation y^2 = x^3 + 5 (mod p).
func TestPallasGeneratorOnCurve(t *testing.T) {
	params := curvey.Pallas().Params()
	gx := params.Gx
	gy := params.Gy
	p := params.P

	// y^2 mod p
	lhs := new(big.Int).Mul(gy, gy)
	lhs.Mod(lhs, p)

	// x^3 + 5 mod p  (Pallas: y^2 = x^3 + 5)
	rhs := new(big.Int).Mul(gx, gx)
	rhs.Mul(rhs, gx)
	rhs.Add(rhs, big.NewInt(5))
	rhs.Mod(rhs, p)

	require.Equal(t, lhs, rhs,
		"Generator (Gx,Gy) does not satisfy the Pallas curve equation y^2 = x^3 + 5")
}

// ---------------------------------------------------------------------------
// B. Known-answer group-order tests
// ---------------------------------------------------------------------------

// TestGroupOrderTimesGenerator verifies q * G = identity (point at infinity).
// This is the fundamental group law test: multiplying the generator by the
// group order must yield the identity element. A wrong order constant or
// broken scalar multiplication would cause this to fail.
func TestGroupOrderTimesGenerator(t *testing.T) {
	G := new(curvey.PointPallas).Generator()
	q := pallasQ()
	qScalar, err := new(curvey.ScalarPallas).SetBigInt(q)
	require.NoError(t, err)
	result := G.Mul(qScalar)
	require.True(t, result.IsIdentity(),
		"q * G must be the identity point")
}

// TestGroupOrderMinusOneTimesGenerator verifies (q-1) * G + G = identity.
// This is equivalent to testing that the generator has exactly order q.
func TestGroupOrderMinusOneTimesGenerator(t *testing.T) {
	G := new(curvey.PointPallas).Generator()
	q := pallasQ()
	qMinus1 := new(big.Int).Sub(q, big.NewInt(1))
	qMinus1Scalar, err := new(curvey.ScalarPallas).SetBigInt(qMinus1)
	require.NoError(t, err)

	P := G.Mul(qMinus1Scalar)

	// P should be -G (the negation of the generator)
	require.False(t, P.IsIdentity(), "(q-1)*G should not be identity")

	// P + G should be identity
	sum := P.Add(G)
	require.True(t, sum.IsIdentity(),
		"(q-1)*G + G must be the identity point")
}

// TestScalarReductionModOrder verifies that the scalar field wraps at q.
// Setting a scalar to q should yield zero, and q+1 should yield 1.
func TestScalarReductionModOrder(t *testing.T) {
	q := pallasQ()

	// q mod q = 0
	sQ, err := new(curvey.ScalarPallas).SetBigInt(q)
	require.NoError(t, err)
	require.True(t, sQ.IsZero(), "scalar(q) should reduce to zero")

	// q+1 mod q = 1
	qPlus1 := new(big.Int).Add(q, big.NewInt(1))
	sQPlus1, err := new(curvey.ScalarPallas).SetBigInt(qPlus1)
	require.NoError(t, err)
	require.True(t, sQPlus1.IsOne(), "scalar(q+1) should reduce to one")
}

// ---------------------------------------------------------------------------
// C. Serialization round-trip fuzz (100 random points)
// ---------------------------------------------------------------------------

// TestPallasCompressedSerializationRoundTrip verifies that 100 random curve
// points survive compressed serialization and deserialization without data
// loss or corruption.
func TestPallasCompressedSerializationRoundTrip(t *testing.T) {
	for i := 0; i < 100; i++ {
		s := new(curvey.ScalarPallas).Random(rand.Reader)
		P := new(curvey.PointPallas).Generator().Mul(s)

		compressed := P.ToAffineCompressed()
		require.Len(t, compressed, 32, "compressed point should be 32 bytes")

		restored, err := pallasPoint().FromAffineCompressed(compressed)
		require.NoError(t, err, "deserialization should not error (iteration %d)", i)
		require.True(t, P.Equal(restored),
			"round-trip failed at iteration %d", i)
	}
}

// TestPallasUncompressedSerializationRoundTrip verifies that 100 random curve
// points survive uncompressed serialization and deserialization.
func TestPallasUncompressedSerializationRoundTrip(t *testing.T) {
	for i := 0; i < 100; i++ {
		s := new(curvey.ScalarPallas).Random(rand.Reader)
		P := new(curvey.PointPallas).Generator().Mul(s)

		uncompressed := P.ToAffineUncompressed()
		require.Len(t, uncompressed, 64, "uncompressed point should be 64 bytes")

		restored, err := pallasPoint().FromAffineUncompressed(uncompressed)
		require.NoError(t, err, "deserialization should not error (iteration %d)", i)
		require.True(t, P.Equal(restored),
			"round-trip failed at iteration %d", i)
	}
}

// TestPallasScalarSerializationRoundTrip verifies that 100 random scalars
// survive byte serialization and deserialization.
func TestPallasScalarSerializationRoundTrip(t *testing.T) {
	for i := 0; i < 100; i++ {
		s := new(curvey.ScalarPallas).Random(rand.Reader)
		bs := s.Bytes()
		require.Len(t, bs, 32, "scalar bytes should be 32 bytes")

		restored, err := new(curvey.ScalarPallas).SetBytes(bs)
		require.NoError(t, err, "scalar deserialization should not error (iteration %d)", i)
		require.Equal(t, 0, s.Cmp(restored),
			"scalar round-trip failed at iteration %d", i)
	}
}

// ---------------------------------------------------------------------------
// D. Group law stress tests (distributivity, associativity, commutativity)
// ---------------------------------------------------------------------------

// TestPallasDistributivity verifies (a+b)*G == a*G + b*G for 50 random pairs.
// This catches subtle bugs in either scalar addition or point multiplication.
func TestPallasDistributivity(t *testing.T) {
	G := new(curvey.PointPallas).Generator()
	for i := 0; i < 50; i++ {
		a := new(curvey.ScalarPallas).Random(rand.Reader)
		b := new(curvey.ScalarPallas).Random(rand.Reader)

		// (a+b)*G
		lhs := G.Mul(a.Add(b))
		// a*G + b*G
		rhs := G.Mul(a).Add(G.Mul(b))

		require.True(t, lhs.Equal(rhs),
			"distributivity (a+b)*G == a*G + b*G failed at iteration %d", i)
	}
}

// TestPallasScalarAssociativity verifies a*(b*G) == (a*b)*G for 50 random pairs.
// This catches bugs in multi-scalar multiplication or scalar field arithmetic.
func TestPallasScalarAssociativity(t *testing.T) {
	G := new(curvey.PointPallas).Generator()
	for i := 0; i < 50; i++ {
		a := new(curvey.ScalarPallas).Random(rand.Reader)
		b := new(curvey.ScalarPallas).Random(rand.Reader)

		// a * (b * G)
		lhs := G.Mul(b).Mul(a)
		// (a * b) * G
		rhs := G.Mul(a.Mul(b))

		require.True(t, lhs.Equal(rhs),
			"scalar associativity a*(b*G) == (a*b)*G failed at iteration %d", i)
	}
}

// TestPallasPointAdditionCommutativity verifies P + Q == Q + P for 50 random
// point pairs. This catches bugs in the point addition formula.
func TestPallasPointAdditionCommutativity(t *testing.T) {
	G := new(curvey.PointPallas).Generator()
	for i := 0; i < 50; i++ {
		a := new(curvey.ScalarPallas).Random(rand.Reader)
		b := new(curvey.ScalarPallas).Random(rand.Reader)
		P := G.Mul(a)
		Q := G.Mul(b)

		pq := P.Add(Q)
		qp := Q.Add(P)

		require.True(t, pq.Equal(qp),
			"point addition commutativity P+Q == Q+P failed at iteration %d", i)
	}
}

// TestPallasPointAdditionAssociativity verifies (P+Q)+R == P+(Q+R) for 30
// random point triples. This catches bugs in incomplete addition formulas.
func TestPallasPointAdditionAssociativity(t *testing.T) {
	G := new(curvey.PointPallas).Generator()
	for i := 0; i < 30; i++ {
		P := G.Mul(new(curvey.ScalarPallas).Random(rand.Reader))
		Q := G.Mul(new(curvey.ScalarPallas).Random(rand.Reader))
		R := G.Mul(new(curvey.ScalarPallas).Random(rand.Reader))

		left := P.Add(Q).Add(R)  // (P+Q)+R
		right := P.Add(Q.Add(R)) // P+(Q+R)

		require.True(t, left.Equal(right),
			"point addition associativity (P+Q)+R == P+(Q+R) failed at iteration %d", i)
	}
}

// TestPallasDoubleEqualsAdd verifies 2*P == P+P for 50 random points.
func TestPallasDoubleEqualsAdd(t *testing.T) {
	G := new(curvey.PointPallas).Generator()
	two := new(curvey.ScalarPallas).New(2)
	for i := 0; i < 50; i++ {
		s := new(curvey.ScalarPallas).Random(rand.Reader)
		P := G.Mul(s)

		doubled := P.Mul(two) // 2 * P via scalar mul
		added := P.Add(P)     // P + P via point add

		require.True(t, doubled.Equal(added),
			"2*P == P+P failed at iteration %d", i)
	}
}

// TestPallasSubIsAddNeg verifies P - Q == P + (-Q) for 50 random pairs.
func TestPallasSubIsAddNeg(t *testing.T) {
	G := new(curvey.PointPallas).Generator()
	for i := 0; i < 50; i++ {
		a := new(curvey.ScalarPallas).Random(rand.Reader)
		b := new(curvey.ScalarPallas).Random(rand.Reader)
		P := G.Mul(a)
		Q := G.Mul(b)

		sub := P.Sub(Q)
		addNeg := P.Add(Q.Neg())

		require.True(t, sub.Equal(addNeg),
			"P-Q == P+(-Q) failed at iteration %d", i)
	}
}

// ---------------------------------------------------------------------------
// E. Cross-verify scalar field arithmetic with math/big
// ---------------------------------------------------------------------------

// TestScalarAddCrossCheck verifies curvey scalar addition against independent
// math/big modular addition for 100 random scalar pairs.
func TestScalarAddCrossCheck(t *testing.T) {
	q := pallasQ()
	for i := 0; i < 100; i++ {
		a := new(curvey.ScalarPallas).Random(rand.Reader)
		b := new(curvey.ScalarPallas).Random(rand.Reader)

		aBig := a.BigInt()
		bBig := b.BigInt()

		// Expected: (a + b) mod q via math/big
		expected := new(big.Int).Add(aBig, bBig)
		expected.Mod(expected, q)

		// Got: a.Add(b) via curvey
		got := a.Add(b).(*curvey.ScalarPallas).BigInt()

		require.Equal(t, expected, got,
			"scalar add cross-check failed at iteration %d", i)
	}
}

// TestScalarMulCrossCheck verifies curvey scalar multiplication against
// independent math/big modular multiplication for 100 random scalar pairs.
func TestScalarMulCrossCheck(t *testing.T) {
	q := pallasQ()
	for i := 0; i < 100; i++ {
		a := new(curvey.ScalarPallas).Random(rand.Reader)
		b := new(curvey.ScalarPallas).Random(rand.Reader)

		aBig := a.BigInt()
		bBig := b.BigInt()

		// Expected: (a * b) mod q via math/big
		expected := new(big.Int).Mul(aBig, bBig)
		expected.Mod(expected, q)

		// Got: a.Mul(b) via curvey
		got := a.Mul(b).(*curvey.ScalarPallas).BigInt()

		require.Equal(t, expected, got,
			"scalar mul cross-check failed at iteration %d", i)
	}
}

// TestScalarSubCrossCheck verifies curvey scalar subtraction against
// independent math/big modular subtraction for 100 random scalar pairs.
func TestScalarSubCrossCheck(t *testing.T) {
	q := pallasQ()
	for i := 0; i < 100; i++ {
		a := new(curvey.ScalarPallas).Random(rand.Reader)
		b := new(curvey.ScalarPallas).Random(rand.Reader)

		aBig := a.BigInt()
		bBig := b.BigInt()

		// Expected: (a - b) mod q via math/big
		expected := new(big.Int).Sub(aBig, bBig)
		expected.Mod(expected, q)

		// Got: a.Sub(b) via curvey
		got := a.Sub(b).(*curvey.ScalarPallas).BigInt()

		require.Equal(t, expected, got,
			"scalar sub cross-check failed at iteration %d", i)
	}
}

// TestScalarNegCrossCheck verifies curvey scalar negation against
// independent math/big modular negation for 100 random scalars.
func TestScalarNegCrossCheck(t *testing.T) {
	q := pallasQ()
	for i := 0; i < 100; i++ {
		a := new(curvey.ScalarPallas).Random(rand.Reader)
		aBig := a.BigInt()

		// Expected: (-a) mod q = (q - a) mod q via math/big
		expected := new(big.Int).Sub(q, aBig)
		expected.Mod(expected, q)

		// Got: a.Neg() via curvey
		got := a.Neg().(*curvey.ScalarPallas).BigInt()

		require.Equal(t, expected, got,
			"scalar neg cross-check failed at iteration %d", i)
	}
}

// TestScalarInvertCrossCheck verifies curvey scalar inversion against
// independent math/big modular inversion for 50 random non-zero scalars.
func TestScalarInvertCrossCheck(t *testing.T) {
	q := pallasQ()
	for i := 0; i < 50; i++ {
		a := new(curvey.ScalarPallas).Random(rand.Reader)
		require.False(t, a.IsZero(), "random scalar should not be zero")

		aBig := a.BigInt()

		// Expected: a^{-1} mod q via math/big
		expected := new(big.Int).ModInverse(aBig, q)
		require.NotNil(t, expected, "modular inverse should exist for non-zero scalar")

		// Got: a.Invert() via curvey
		inv, err := a.Invert()
		require.NoError(t, err)
		got := inv.(*curvey.ScalarPallas).BigInt()

		require.Equal(t, expected, got,
			"scalar invert cross-check failed at iteration %d", i)

		// Verify: a * a^{-1} == 1
		product := a.Mul(inv)
		require.True(t, product.IsOne(),
			"a * a^{-1} should be 1 at iteration %d", i)
	}
}
