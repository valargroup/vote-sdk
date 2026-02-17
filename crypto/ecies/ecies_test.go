package ecies

import (
	"bytes"
	"crypto/rand"
	"testing"

	"github.com/mikelodder7/curvey"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/chacha20poly1305"
)

// testGenerator returns the standard Pallas generator point.
func testGenerator() curvey.Point {
	return new(curvey.PointPallas).Generator()
}

// testKeypair generates a random Pallas keypair (sk, pk = sk * G).
func testKeypair() (curvey.Scalar, curvey.Point) {
	G := testGenerator()
	sk := new(curvey.ScalarPallas).Random(rand.Reader)
	pk := G.Mul(sk)
	return sk, pk
}

// requirePallasPoint asserts that p is concretely a *curvey.PointPallas.
func requirePallasPoint(t *testing.T, p curvey.Point, label string) {
	t.Helper()
	if _, ok := p.(*curvey.PointPallas); !ok {
		t.Fatalf("%s: expected *curvey.PointPallas, got %T", label, p)
	}
}

// ---------------------------------------------------------------------------
// Round-trip tests
// ---------------------------------------------------------------------------

// TestEncryptDecryptRoundTrip verifies that encrypting then decrypting
// recovers the original plaintext for various sizes.
func TestEncryptDecryptRoundTrip(t *testing.T) {
	G := testGenerator()
	sk, pk := testKeypair()

	plaintexts := [][]byte{
		{0x42},
		bytes.Repeat([]byte{0xAB}, 16),
		bytes.Repeat([]byte{0xCD}, 32), // ea_sk size
		bytes.Repeat([]byte{0xEF}, 64),
		bytes.Repeat([]byte{0x01}, 1024),
	}

	for _, pt := range plaintexts {
		env, err := Encrypt(G, pk, pt, rand.Reader)
		require.NoError(t, err)
		require.NotNil(t, env)
		require.NotNil(t, env.Ephemeral)
		requirePallasPoint(t, env.Ephemeral, "ephemeral")
		require.Len(t, env.Ciphertext, len(pt)+chacha20poly1305.Overhead)

		got, err := Decrypt(sk, env)
		require.NoError(t, err)
		require.Equal(t, pt, got, "plaintext mismatch for len=%d", len(pt))
	}
}

// TestEncryptDecryptScalarRoundTrip tests the primary use case: encrypting
// and decrypting a 32-byte Pallas scalar (ea_sk).
func TestEncryptDecryptScalarRoundTrip(t *testing.T) {
	G := testGenerator()
	sk, pk := testKeypair()

	// Simulate ea_sk
	eaSk := new(curvey.ScalarPallas).Random(rand.Reader)
	plaintext := eaSk.Bytes()
	require.Len(t, plaintext, 32)

	env, err := Encrypt(G, pk, plaintext, rand.Reader)
	require.NoError(t, err)

	got, err := Decrypt(sk, env)
	require.NoError(t, err)
	require.Equal(t, plaintext, got)

	// Verify the decrypted bytes deserialize back to the same scalar.
	restored, err := new(curvey.ScalarPallas).SetBytes(got)
	require.NoError(t, err)
	require.Equal(t, 0, eaSk.Cmp(restored), "scalar round-trip mismatch")
}

// ---------------------------------------------------------------------------
// Wrong key tests
// ---------------------------------------------------------------------------

// TestDecryptWithWrongKeyFails verifies that decrypting with a different
// secret key fails with an authentication error.
func TestDecryptWithWrongKeyFails(t *testing.T) {
	G := testGenerator()
	_, pk := testKeypair()
	wrongSK, _ := testKeypair()

	plaintext := []byte("secret election authority key data")
	env, err := Encrypt(G, pk, plaintext, rand.Reader)
	require.NoError(t, err)

	_, err = Decrypt(wrongSK, env)
	require.Error(t, err)
	require.Contains(t, err.Error(), "authentication failed")
}

// TestDecryptWithTamperedCiphertextFails verifies that modifying the
// ciphertext causes authentication to fail.
func TestDecryptWithTamperedCiphertextFails(t *testing.T) {
	G := testGenerator()
	sk, pk := testKeypair()

	plaintext := []byte("secret data that must not be tampered with")
	env, err := Encrypt(G, pk, plaintext, rand.Reader)
	require.NoError(t, err)

	// Flip a bit in the ciphertext.
	env.Ciphertext[0] ^= 0x01

	_, err = Decrypt(sk, env)
	require.Error(t, err)
	require.Contains(t, err.Error(), "authentication failed")
}

// ---------------------------------------------------------------------------
// Semantic security tests
// ---------------------------------------------------------------------------

// TestEncryptProducesDifferentEnvelopes verifies that encrypting the same
// plaintext twice with different randomness produces different envelopes.
func TestEncryptProducesDifferentEnvelopes(t *testing.T) {
	G := testGenerator()
	_, pk := testKeypair()

	plaintext := []byte("same plaintext both times")

	env1, err := Encrypt(G, pk, plaintext, rand.Reader)
	require.NoError(t, err)
	env2, err := Encrypt(G, pk, plaintext, rand.Reader)
	require.NoError(t, err)

	// Ephemeral keys must differ (with overwhelming probability).
	require.False(t, env1.Ephemeral.Equal(env2.Ephemeral),
		"two encryptions should produce different ephemeral keys")

	// Ciphertexts must differ.
	require.False(t, bytes.Equal(env1.Ciphertext, env2.Ciphertext),
		"two encryptions should produce different ciphertexts")
}

// ---------------------------------------------------------------------------
// Input validation tests
// ---------------------------------------------------------------------------

// TestEncryptRejectsIdentityPublicKey verifies that encrypting to the
// identity point is rejected. With pk = O, the ECDH shared secret is always
// the identity regardless of the ephemeral key, leaking the symmetric key.
func TestEncryptRejectsIdentityPublicKey(t *testing.T) {
	G := testGenerator()
	identity := new(curvey.PointPallas).Identity()
	plaintext := []byte("test")

	_, err := Encrypt(G, identity, plaintext, rand.Reader)
	require.Error(t, err)
	require.Contains(t, err.Error(), "identity point")
}

// TestEncryptRejectsIdentityGenerator verifies that using the identity
// point as the generator is rejected.
func TestEncryptRejectsIdentityGenerator(t *testing.T) {
	identity := new(curvey.PointPallas).Identity()
	_, pk := testKeypair()
	plaintext := []byte("test")

	_, err := Encrypt(identity, pk, plaintext, rand.Reader)
	require.Error(t, err)
	require.Contains(t, err.Error(), "identity point")
}

// TestEncryptRejectsNilPublicKey verifies nil public key handling.
func TestEncryptRejectsNilPublicKey(t *testing.T) {
	G := testGenerator()
	plaintext := []byte("test")

	_, err := Encrypt(G, nil, plaintext, rand.Reader)
	require.Error(t, err)
	require.Contains(t, err.Error(), "recipient public key must not be nil")
}

// TestEncryptRejectsNilGenerator verifies nil generator handling.
func TestEncryptRejectsNilGenerator(t *testing.T) {
	_, pk := testKeypair()
	plaintext := []byte("test")

	_, err := Encrypt(nil, pk, plaintext, rand.Reader)
	require.Error(t, err)
	require.Contains(t, err.Error(), "generator must not be nil")
}

// TestEncryptRejectsEmptyPlaintext verifies that empty plaintext is rejected.
func TestEncryptRejectsEmptyPlaintext(t *testing.T) {
	G := testGenerator()
	_, pk := testKeypair()

	_, err := Encrypt(G, pk, []byte{}, rand.Reader)
	require.Error(t, err)
	require.Contains(t, err.Error(), "plaintext must not be empty")

	_, err = Encrypt(G, pk, nil, rand.Reader)
	require.Error(t, err)
	require.Contains(t, err.Error(), "plaintext must not be empty")
}

// TestEncryptRejectsNilRng verifies nil rng handling.
func TestEncryptRejectsNilRng(t *testing.T) {
	G := testGenerator()
	_, pk := testKeypair()
	plaintext := []byte("test")

	_, err := Encrypt(G, pk, plaintext, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "rng must not be nil")
}

// TestDecryptRejectsNilSecretKey verifies nil secret key handling.
func TestDecryptRejectsNilSecretKey(t *testing.T) {
	G := testGenerator()
	_, pk := testKeypair()

	env, err := Encrypt(G, pk, []byte("test"), rand.Reader)
	require.NoError(t, err)

	_, err = Decrypt(nil, env)
	require.Error(t, err)
	require.Contains(t, err.Error(), "secret key must not be nil or zero")
}

// TestDecryptRejectsZeroSecretKey verifies zero scalar secret key handling.
func TestDecryptRejectsZeroSecretKey(t *testing.T) {
	G := testGenerator()
	_, pk := testKeypair()

	env, err := Encrypt(G, pk, []byte("test"), rand.Reader)
	require.NoError(t, err)

	zero := new(curvey.ScalarPallas).Zero()
	_, err = Decrypt(zero, env)
	require.Error(t, err)
	require.Contains(t, err.Error(), "secret key must not be nil or zero")
}

// TestDecryptRejectsNilEnvelope verifies nil envelope handling.
func TestDecryptRejectsNilEnvelope(t *testing.T) {
	sk, _ := testKeypair()
	_, err := Decrypt(sk, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "envelope must not be nil")
}

// TestDecryptRejectsIdentityEphemeral verifies that an envelope with an
// identity ephemeral key is rejected.
func TestDecryptRejectsIdentityEphemeral(t *testing.T) {
	sk, _ := testKeypair()
	env := &Envelope{
		Ephemeral:  new(curvey.PointPallas).Identity(),
		Ciphertext: make([]byte, 48), // 32 + 16 overhead
	}

	_, err := Decrypt(sk, env)
	require.Error(t, err)
	require.Contains(t, err.Error(), "identity point")
}

// TestDecryptRejectsTruncatedCiphertext verifies that a ciphertext shorter
// than the Poly1305 overhead is rejected.
func TestDecryptRejectsTruncatedCiphertext(t *testing.T) {
	sk, _ := testKeypair()
	G := testGenerator()
	e := new(curvey.ScalarPallas).Random(rand.Reader)
	E := G.Mul(e)

	env := &Envelope{
		Ephemeral:  E,
		Ciphertext: make([]byte, chacha20poly1305.Overhead-1),
	}

	_, err := Decrypt(sk, env)
	require.Error(t, err)
	require.Contains(t, err.Error(), "ciphertext too short")
}

// ---------------------------------------------------------------------------
// Serialization round-trip tests
// ---------------------------------------------------------------------------

// TestMarshalUnmarshalEnvelopeRoundTrip verifies that serializing and
// deserializing an envelope produces an equivalent envelope.
func TestMarshalUnmarshalEnvelopeRoundTrip(t *testing.T) {
	G := testGenerator()
	sk, pk := testKeypair()

	plaintext := bytes.Repeat([]byte{0xAA}, 32) // 32-byte scalar
	env, err := Encrypt(G, pk, plaintext, rand.Reader)
	require.NoError(t, err)

	data, err := MarshalEnvelope(env)
	require.NoError(t, err)
	require.Len(t, data, CompressedPointSize+len(env.Ciphertext))

	ctLen := len(plaintext) + chacha20poly1305.Overhead
	env2, err := UnmarshalEnvelope(data, ctLen)
	require.NoError(t, err)

	// Ephemeral points must be equal.
	require.True(t, env.Ephemeral.Equal(env2.Ephemeral),
		"ephemeral point mismatch after round-trip")
	requirePallasPoint(t, env2.Ephemeral, "deserialized ephemeral")

	// Ciphertexts must be equal.
	require.Equal(t, env.Ciphertext, env2.Ciphertext,
		"ciphertext mismatch after round-trip")

	// The deserialized envelope must still decrypt correctly.
	got, err := Decrypt(sk, env2)
	require.NoError(t, err)
	require.Equal(t, plaintext, got)
}

// TestUnmarshalEnvelopeRejectsWrongLength verifies length validation.
func TestUnmarshalEnvelopeRejectsWrongLength(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		ctLen int
	}{
		{"empty", []byte{}, 48},
		{"too short", make([]byte, 32), 48},
		{"too long", make([]byte, 128), 48},
		{"off by one short", make([]byte, CompressedPointSize+48-1), 48},
		{"off by one long", make([]byte, CompressedPointSize+48+1), 48},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := UnmarshalEnvelope(tc.data, tc.ctLen)
			require.Error(t, err)
		})
	}
}

// TestUnmarshalEnvelopeRejectsIdentityEphemeral verifies that deserializing
// an envelope with an all-zeros ephemeral key (identity point) is rejected.
func TestUnmarshalEnvelopeRejectsIdentityEphemeral(t *testing.T) {
	ctLen := 48
	data := make([]byte, CompressedPointSize+ctLen) // all zeros = identity for E
	// Fill ciphertext portion with non-zero to avoid confusion.
	for i := CompressedPointSize; i < len(data); i++ {
		data[i] = 0xFF
	}

	_, err := UnmarshalEnvelope(data, ctLen)
	require.Error(t, err)
	require.Contains(t, err.Error(), "identity point")
}

// TestMarshalEnvelopeRejectsNil verifies nil envelope handling.
func TestMarshalEnvelopeRejectsNil(t *testing.T) {
	_, err := MarshalEnvelope(nil)
	require.Error(t, err)

	_, err = MarshalEnvelope(&Envelope{Ephemeral: nil, Ciphertext: []byte{0x01}})
	require.Error(t, err)

	G := testGenerator()
	E := G.Mul(new(curvey.ScalarPallas).Random(rand.Reader))
	_, err = MarshalEnvelope(&Envelope{Ephemeral: E, Ciphertext: nil})
	require.Error(t, err)
}

// ---------------------------------------------------------------------------
// ECDH correctness test
// ---------------------------------------------------------------------------

// TestECDHSharedSecretConsistency verifies that both parties derive the same
// shared secret: e * pk == sk * E.
func TestECDHSharedSecretConsistency(t *testing.T) {
	G := testGenerator()

	for i := 0; i < 50; i++ {
		sk := new(curvey.ScalarPallas).Random(rand.Reader)
		pk := G.Mul(sk)

		e := new(curvey.ScalarPallas).Random(rand.Reader)
		E := G.Mul(e)

		// Sender computes S = e * pk
		senderS := pk.Mul(e)
		// Recipient computes S = sk * E
		recipientS := E.Mul(sk)

		require.True(t, senderS.Equal(recipientS),
			"ECDH shared secret mismatch at iteration %d", i)
	}
}

// ---------------------------------------------------------------------------
// KDF determinism test
// ---------------------------------------------------------------------------

// TestDeriveKeyDeterministic verifies that deriveKey produces the same output
// for the same inputs.
func TestDeriveKeyDeterministic(t *testing.T) {
	G := testGenerator()
	s := new(curvey.ScalarPallas).Random(rand.Reader)
	E := G.Mul(s)
	S := G.Mul(new(curvey.ScalarPallas).Random(rand.Reader))

	k1 := deriveKey(E, S)
	k2 := deriveKey(E, S)
	require.Equal(t, k1, k2, "deriveKey should be deterministic")
}

// TestDeriveKeyDifferentInputs verifies that different inputs produce
// different keys.
func TestDeriveKeyDifferentInputs(t *testing.T) {
	G := testGenerator()

	E1 := G.Mul(new(curvey.ScalarPallas).Random(rand.Reader))
	E2 := G.Mul(new(curvey.ScalarPallas).Random(rand.Reader))
	S := G.Mul(new(curvey.ScalarPallas).Random(rand.Reader))

	k1 := deriveKey(E1, S)
	k2 := deriveKey(E2, S)
	require.NotEqual(t, k1, k2, "different E should produce different keys")

	S2 := G.Mul(new(curvey.ScalarPallas).Random(rand.Reader))
	k3 := deriveKey(E1, S)
	k4 := deriveKey(E1, S2)
	require.NotEqual(t, k3, k4, "different S should produce different keys")
}

// ---------------------------------------------------------------------------
// x-coordinate extraction test
// ---------------------------------------------------------------------------

// TestXCoordinateStripsSignBit verifies that xCoordinate clears the sign
// bit in byte[31] of the compressed encoding.
func TestXCoordinateStripsSignBit(t *testing.T) {
	G := testGenerator()

	for i := 0; i < 100; i++ {
		s := new(curvey.ScalarPallas).Random(rand.Reader)
		P := G.Mul(s)

		compressed := P.ToAffineCompressed()
		x := xCoordinate(P)

		require.Len(t, x, CompressedPointSize)

		// Bytes 0..30 must be identical.
		require.Equal(t, compressed[:31], x[:31],
			"bytes 0..30 should match at iteration %d", i)

		// Byte 31: sign bit must be cleared.
		require.Equal(t, compressed[31]&0x7F, x[31],
			"sign bit should be cleared at iteration %d", i)
		require.Zero(t, x[31]&0x80,
			"bit 7 of byte[31] should be zero at iteration %d", i)
	}
}

// TestXCoordinateSameForNegatedPoint verifies that a point and its negation
// (which share the same x-coordinate) produce the same xCoordinate output.
func TestXCoordinateSameForNegatedPoint(t *testing.T) {
	G := testGenerator()

	for i := 0; i < 50; i++ {
		s := new(curvey.ScalarPallas).Random(rand.Reader)
		P := G.Mul(s)
		negP := P.Neg()

		xP := xCoordinate(P)
		xNeg := xCoordinate(negP)

		require.Equal(t, xP, xNeg,
			"P and -P should have the same x-coordinate at iteration %d", i)
	}
}
