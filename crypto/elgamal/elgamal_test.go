package elgamal

import (
	"crypto/rand"
	"testing"

	"github.com/mikelodder7/curvey"
	"github.com/stretchr/testify/require"
)

// vG computes v * G on the Pallas curve, used as the expected decryption result.
func vG(v uint64) curvey.Point {
	return PallasGenerator().Mul(scalarFromUint64(v))
}

// mustEncrypt is a test helper that calls Encrypt and fails the test on error.
func mustEncrypt(t *testing.T, pk *PublicKey, v uint64) *Ciphertext {
	t.Helper()
	ct, err := Encrypt(pk, v, rand.Reader)
	require.NoError(t, err)
	return ct
}

// TestKeyGen verifies that key generation produces valid, non-degenerate keys.
func TestKeyGen(t *testing.T) {
	sk, pk := KeyGen(rand.Reader)

	require.False(t, sk.Scalar.IsZero(), "secret key should not be zero")
	require.False(t, pk.Point.IsIdentity(), "public key should not be identity")
	require.True(t, pk.Point.IsOnCurve(), "public key should be on curve")
	requirePallasPoint(t, pk.Point, "pk")

	// pk == sk * G
	expected := PallasGenerator().Mul(sk.Scalar)
	require.True(t, pk.Point.Equal(expected), "pk should equal sk*G")
}

// TestDecryptRoundTrip verifies DecryptToPoint(Encrypt(v)) == v*G for various values.
func TestDecryptRoundTrip(t *testing.T) {
	sk, pk := KeyGen(rand.Reader)

	values := []uint64{0, 1, 2, 7, 42, 100, 255, 1000, 65535, 1 << 20, 1 << 24}
	for _, v := range values {
		ct := mustEncrypt(t, pk, v)
		requirePallasPoint(t, ct.C1, "ct.C1")
		requirePallasPoint(t, ct.C2, "ct.C2")

		got := DecryptToPoint(sk, ct)
		requirePallasPoint(t, got, "DecryptToPoint result")
		expected := vG(v)
		require.True(t, got.Equal(expected), "decrypt(encrypt(%d)) should equal %d*G", v, v)
	}
}

// TestDecryptZero verifies that encrypting 0 decrypts to the identity point.
func TestDecryptZero(t *testing.T) {
	sk, pk := KeyGen(rand.Reader)
	ct := mustEncrypt(t, pk, 0)
	got := DecryptToPoint(sk, ct)
	requirePallasPoint(t, got, "DecryptToPoint(0)")
	require.True(t, got.IsIdentity(), "decrypt(encrypt(0)) should be identity")
}

// TestEncryptWithRandomness verifies deterministic encryption with known randomness.
func TestEncryptWithRandomness(t *testing.T) {
	sk, pk := KeyGen(rand.Reader)
	r := new(curvey.ScalarPallas).Random(rand.Reader)

	ct1, err := EncryptWithRandomness(pk, 42, r)
	require.NoError(t, err)
	ct2, err := EncryptWithRandomness(pk, 42, r)
	require.NoError(t, err)

	// Same randomness, same value → identical ciphertext
	require.True(t, ct1.C1.Equal(ct2.C1), "C1 should be identical with same randomness")
	require.True(t, ct1.C2.Equal(ct2.C2), "C2 should be identical with same randomness")
	requirePallasPoint(t, ct1.C1, "ct1.C1")
	requirePallasPoint(t, ct1.C2, "ct1.C2")

	// Decryption still works
	got := DecryptToPoint(sk, ct1)
	requirePallasPoint(t, got, "DecryptToPoint result")
	require.True(t, got.Equal(vG(42)))
}

// TestHomomorphicAdd verifies the core homomorphic property:
// Decrypt(Enc(a) + Enc(b)) == (a+b)*G
func TestHomomorphicAdd(t *testing.T) {
	sk, pk := KeyGen(rand.Reader)

	testCases := []struct {
		a, b uint64
	}{
		{0, 0},
		{1, 1},
		{3, 5},
		{42, 58},
		{100, 200},
		{1 << 16, 1 << 16},
		{999, 1},
		{0, 100},
		{100, 0},
	}

	for _, tc := range testCases {
		ctA := mustEncrypt(t, pk, tc.a)
		ctB := mustEncrypt(t, pk, tc.b)
		ctSum := HomomorphicAdd(ctA, ctB)
		requirePallasPoint(t, ctSum.C1, "ctSum.C1")
		requirePallasPoint(t, ctSum.C2, "ctSum.C2")

		got := DecryptToPoint(sk, ctSum)
		requirePallasPoint(t, got, "DecryptToPoint result")
		expected := vG(tc.a + tc.b)
		require.True(t, got.Equal(expected),
			"decrypt(enc(%d) + enc(%d)) should equal %d*G", tc.a, tc.b, tc.a+tc.b)
	}
}

// TestHomomorphicAddMultiple verifies homomorphic accumulation across many ciphertexts.
func TestHomomorphicAddMultiple(t *testing.T) {
	sk, pk := KeyGen(rand.Reader)

	shares := []uint64{10, 20, 30, 40, 50}
	acc, err := EncryptZero(pk, rand.Reader)
	require.NoError(t, err)
	var total uint64
	for _, v := range shares {
		ct := mustEncrypt(t, pk, v)
		acc = HomomorphicAdd(acc, ct)
		total += v
	}

	requirePallasPoint(t, acc.C1, "acc.C1")
	requirePallasPoint(t, acc.C2, "acc.C2")

	got := DecryptToPoint(sk, acc)
	requirePallasPoint(t, got, "DecryptToPoint result")
	expected := vG(total) // 150
	require.True(t, got.Equal(expected),
		"accumulated sum should decrypt to %d*G", total)
}

// TestHomomorphicAddCommutative verifies Enc(a)+Enc(b) decrypts the same as Enc(b)+Enc(a).
func TestHomomorphicAddCommutative(t *testing.T) {
	sk, pk := KeyGen(rand.Reader)

	ctA := mustEncrypt(t, pk, 17)
	ctB := mustEncrypt(t, pk, 29)

	sumAB := HomomorphicAdd(ctA, ctB)
	sumBA := HomomorphicAdd(ctB, ctA)

	gotAB := DecryptToPoint(sk, sumAB)
	gotBA := DecryptToPoint(sk, sumBA)
	requirePallasPoint(t, gotAB, "gotAB")
	requirePallasPoint(t, gotBA, "gotBA")

	expected := vG(46)
	require.True(t, gotAB.Equal(expected), "a+b should decrypt correctly")
	require.True(t, gotBA.Equal(expected), "b+a should decrypt correctly")
}

// TestHomomorphicAddAssociative verifies (a+b)+c decrypts the same as a+(b+c).
func TestHomomorphicAddAssociative(t *testing.T) {
	sk, pk := KeyGen(rand.Reader)

	ctA := mustEncrypt(t, pk, 11)
	ctB := mustEncrypt(t, pk, 22)
	ctC := mustEncrypt(t, pk, 33)

	// (a + b) + c
	left := HomomorphicAdd(HomomorphicAdd(ctA, ctB), ctC)
	// a + (b + c)
	right := HomomorphicAdd(ctA, HomomorphicAdd(ctB, ctC))

	gotLeft := DecryptToPoint(sk, left)
	gotRight := DecryptToPoint(sk, right)
	requirePallasPoint(t, gotLeft, "gotLeft")
	requirePallasPoint(t, gotRight, "gotRight")
	expected := vG(66)

	require.True(t, gotLeft.Equal(expected), "(a+b)+c should decrypt to 66*G")
	require.True(t, gotRight.Equal(expected), "a+(b+c) should decrypt to 66*G")
}

// TestHomomorphicAddIdentity verifies Enc(v) + EncryptZero(pk) decrypts to v*G.
func TestHomomorphicAddIdentity(t *testing.T) {
	sk, pk := KeyGen(rand.Reader)

	for _, v := range []uint64{0, 1, 42, 1000, 1 << 24} {
		ct := mustEncrypt(t, pk, v)
		zero, err := EncryptZero(pk, rand.Reader)
		require.NoError(t, err)

		sum := HomomorphicAdd(ct, zero)
		got := DecryptToPoint(sk, sum)
		requirePallasPoint(t, got, "DecryptToPoint result")
		expected := vG(v)
		require.True(t, got.Equal(expected),
			"enc(%d) + enc(0) should decrypt to %d*G", v, v)
	}
}

// TestEncryptZeroDecryptsToIdentity verifies that EncryptZero() decrypts to the identity point.
func TestEncryptZeroDecryptsToIdentity(t *testing.T) {
	sk, pk := KeyGen(rand.Reader)
	ct, err := EncryptZero(pk, rand.Reader)
	require.NoError(t, err)
	requirePallasPoint(t, ct.C1, "ct.C1")
	requirePallasPoint(t, ct.C2, "ct.C2")
	got := DecryptToPoint(sk, ct)
	requirePallasPoint(t, got, "DecryptToPoint(EncryptZero)")
	require.True(t, got.IsIdentity(), "EncryptZero should decrypt to identity")
}

// TestEncryptProducesDifferentCiphertexts verifies that encrypting the same value
// twice with different randomness produces different ciphertexts (semantic security).
func TestEncryptProducesDifferentCiphertexts(t *testing.T) {
	_, pk := KeyGen(rand.Reader)

	ct1 := mustEncrypt(t, pk, 42)
	ct2 := mustEncrypt(t, pk, 42)

	// With overwhelming probability, different randomness → different ciphertext
	require.False(t, ct1.C1.Equal(ct2.C1), "two encryptions of same value should have different C1")
	require.False(t, ct1.C2.Equal(ct2.C2), "two encryptions of same value should have different C2")
	requirePallasPoint(t, ct1.C1, "ct1.C1")
	requirePallasPoint(t, ct2.C1, "ct2.C1")
}

// TestDifferentKeysCannotDecrypt verifies that a ciphertext encrypted under one
// key pair cannot be correctly decrypted by a different secret key.
func TestDifferentKeysCannotDecrypt(t *testing.T) {
	sk1, pk1 := KeyGen(rand.Reader)
	sk2, _ := KeyGen(rand.Reader)

	ct := mustEncrypt(t, pk1, 42)

	// Decrypt with wrong key
	wrong := DecryptToPoint(sk2, ct)
	correct := DecryptToPoint(sk1, ct)
	requirePallasPoint(t, wrong, "wrong-key decrypt")
	requirePallasPoint(t, correct, "correct-key decrypt")

	require.True(t, correct.Equal(vG(42)), "correct key should decrypt properly")
	require.False(t, wrong.Equal(vG(42)), "wrong key should not decrypt properly")
}

// TestLargeValueEncryptDecrypt verifies encryption works for values near the
// upper bound that BSGS will later need to recover.
func TestLargeValueEncryptDecrypt(t *testing.T) {
	sk, pk := KeyGen(rand.Reader)

	// 2^24 - 1 = max per-share value per ZKP #2 spec
	maxShare := uint64((1 << 24) - 1)
	ct := mustEncrypt(t, pk, maxShare)
	got := DecryptToPoint(sk, ct)
	requirePallasPoint(t, got, "DecryptToPoint(maxShare)")
	require.True(t, got.Equal(vG(maxShare)),
		"should handle max share value 2^24-1")

	// Aggregate: several max shares summed
	acc, err := EncryptZero(pk, rand.Reader)
	require.NoError(t, err)
	n := 10
	for i := 0; i < n; i++ {
		acc = HomomorphicAdd(acc, mustEncrypt(t, pk, maxShare))
	}
	aggGot := DecryptToPoint(sk, acc)
	requirePallasPoint(t, aggGot, "DecryptToPoint(aggregated)")
	aggExpected := vG(maxShare * uint64(n))
	require.True(t, aggGot.Equal(aggExpected),
		"should handle aggregated large values")
}

// TestEncryptNilReaderReturnsError verifies that Encrypt returns an error
// when given a nil reader instead of silently producing a corrupted ciphertext.
func TestEncryptNilReaderReturnsError(t *testing.T) {
	_, pk := KeyGen(rand.Reader)
	ct, err := Encrypt(pk, 42, nil)
	require.Error(t, err, "nil reader should return an error")
	require.Nil(t, ct, "ciphertext should be nil on error")
}

// TestEncryptZeroIsSemanticallySeure verifies that EncryptZero produces
// randomized ciphertexts (IND-CPA): two encryptions of zero must differ,
// and C1 must not be the identity point (which would be trivially distinguishable).
func TestEncryptZeroIsSemanticallySeure(t *testing.T) {
	_, pk := KeyGen(rand.Reader)

	ct1, err := EncryptZero(pk, rand.Reader)
	require.NoError(t, err)
	ct2, err := EncryptZero(pk, rand.Reader)
	require.NoError(t, err)

	// Fresh randomness means different ciphertexts (with overwhelming probability).
	require.False(t, ct1.C1.Equal(ct2.C1),
		"two EncryptZero calls should produce different C1 (semantic security)")
	require.False(t, ct1.C2.Equal(ct2.C2),
		"two EncryptZero calls should produce different C2 (semantic security)")

	// C1 = r*G must NOT be identity — that would reveal the plaintext.
	require.False(t, ct1.C1.IsIdentity(),
		"C1 must not be identity (would break IND-CPA)")
	requirePallasPoint(t, ct1.C1, "ct1.C1")
	requirePallasPoint(t, ct2.C1, "ct2.C1")
}

// ---------------------------------------------------------------------------
// Randomness validation tests
// ---------------------------------------------------------------------------

// TestEncryptWithRandomnessRejectsZeroScalar verifies that r = 0 is rejected.
// With r = 0: C1 = O, C2 = v*G — anyone can recover v via BSGS without the secret key.
func TestEncryptWithRandomnessRejectsZeroScalar(t *testing.T) {
	_, pk := KeyGen(rand.Reader)
	zero := new(curvey.ScalarPallas).Zero()
	ct, err := EncryptWithRandomness(pk, 42, zero)
	require.Error(t, err, "zero randomness must be rejected")
	require.Nil(t, ct)
	require.Contains(t, err.Error(), "randomness must not be nil or zero")
}

// TestEncryptWithRandomnessRejectsNilScalar verifies that r = nil is rejected.
func TestEncryptWithRandomnessRejectsNilScalar(t *testing.T) {
	_, pk := KeyGen(rand.Reader)
	ct, err := EncryptWithRandomness(pk, 42, nil)
	require.Error(t, err, "nil randomness must be rejected")
	require.Nil(t, ct)
	require.Contains(t, err.Error(), "randomness must not be nil or zero")
}

// ---------------------------------------------------------------------------
// Public key validation tests
// ---------------------------------------------------------------------------

// TestEncryptRejectsIdentityPublicKey verifies that Encrypt returns an error
// when given pk = identity (point at infinity). With pk = O, every ciphertext
// leaks the plaintext as C2 = v*G.
func TestEncryptRejectsIdentityPublicKey(t *testing.T) {
	badPK := &PublicKey{Point: new(curvey.PointPallas).Identity()}
	ct, err := Encrypt(badPK, 42, rand.Reader)
	require.Error(t, err, "identity public key must be rejected")
	require.Nil(t, ct)
	require.Contains(t, err.Error(), "identity point")
}

// TestEncryptWithRandomnessRejectsIdentityPublicKey verifies the same
// validation applies to the explicit-randomness variant.
func TestEncryptWithRandomnessRejectsIdentityPublicKey(t *testing.T) {
	badPK := &PublicKey{Point: new(curvey.PointPallas).Identity()}
	r := new(curvey.ScalarPallas).Random(rand.Reader)
	ct, err := EncryptWithRandomness(badPK, 42, r)
	require.Error(t, err, "identity public key must be rejected")
	require.Nil(t, ct)
	require.Contains(t, err.Error(), "identity point")
}

// TestEncryptRejectsNilPublicKey verifies that Encrypt returns an error
// for nil public key inputs instead of panicking.
func TestEncryptRejectsNilPublicKey(t *testing.T) {
	ct, err := Encrypt(nil, 42, rand.Reader)
	require.Error(t, err, "nil public key must be rejected")
	require.Nil(t, ct)

	ct, err = Encrypt(&PublicKey{Point: nil}, 42, rand.Reader)
	require.Error(t, err, "nil point in public key must be rejected")
	require.Nil(t, ct)
}
