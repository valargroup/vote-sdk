package elgamal

import (
	"crypto/rand"
	"testing"

	"github.com/mikelodder7/curvey"
	"github.com/stretchr/testify/require"
)

func TestDLEQRoundTrip(t *testing.T) {
	values := []uint64{0, 1, 42, 1 << 24, 1 << 28}

	for _, v := range values {
		sk, pk := KeyGen(rand.Reader)
		ct, err := Encrypt(pk, v, rand.Reader)
		require.NoError(t, err)

		proof, err := GenerateDLEQProof(sk, ct, v)
		require.NoError(t, err)
		require.Len(t, proof, DLEQProofSize)

		err = VerifyDLEQProof(proof, pk, ct, v)
		require.NoError(t, err, "DLEQ verification failed for value %d", v)
	}
}

func TestDLEQWrongValue(t *testing.T) {
	sk, pk := KeyGen(rand.Reader)
	ct, err := Encrypt(pk, 42, rand.Reader)
	require.NoError(t, err)

	// Generate proof for the correct value.
	proof, err := GenerateDLEQProof(sk, ct, 42)
	require.NoError(t, err)

	// Verify with wrong value should fail.
	err = VerifyDLEQProof(proof, pk, ct, 43)
	require.Error(t, err)
	require.Contains(t, err.Error(), "verification failed")
}

func TestDLEQWrongPublicKey(t *testing.T) {
	sk, pk := KeyGen(rand.Reader)
	ct, err := Encrypt(pk, 100, rand.Reader)
	require.NoError(t, err)

	proof, err := GenerateDLEQProof(sk, ct, 100)
	require.NoError(t, err)

	// Verify with a different public key should fail.
	_, pk2 := KeyGen(rand.Reader)
	err = VerifyDLEQProof(proof, pk2, ct, 100)
	require.Error(t, err)
	require.Contains(t, err.Error(), "verification failed")
}

func TestDLEQWrongCiphertext(t *testing.T) {
	sk, pk := KeyGen(rand.Reader)
	ct, err := Encrypt(pk, 100, rand.Reader)
	require.NoError(t, err)

	proof, err := GenerateDLEQProof(sk, ct, 100)
	require.NoError(t, err)

	// Verify with a different ciphertext (same value, different randomness) should fail.
	ct2, err := Encrypt(pk, 100, rand.Reader)
	require.NoError(t, err)
	err = VerifyDLEQProof(proof, pk, ct2, 100)
	require.Error(t, err)
	require.Contains(t, err.Error(), "verification failed")
}

func TestDLEQTamperedProof(t *testing.T) {
	sk, pk := KeyGen(rand.Reader)
	ct, err := Encrypt(pk, 55, rand.Reader)
	require.NoError(t, err)

	proof, err := GenerateDLEQProof(sk, ct, 55)
	require.NoError(t, err)

	// Flip a byte in the proof.
	tampered := make([]byte, len(proof))
	copy(tampered, proof)
	tampered[0] ^= 0x01

	err = VerifyDLEQProof(tampered, pk, ct, 55)
	require.Error(t, err)
}

func TestDLEQHomomorphicAccumulator(t *testing.T) {
	// Mimics the real tally flow: encrypt multiple values, HomomorphicAdd them,
	// decrypt the aggregate, prove + verify.
	sk, pk := KeyGen(rand.Reader)

	values := []uint64{100, 200, 300, 400}
	var totalValue uint64

	// Encrypt and accumulate.
	var acc *Ciphertext
	for _, v := range values {
		ct, err := Encrypt(pk, v, rand.Reader)
		require.NoError(t, err)
		if acc == nil {
			acc = ct
		} else {
			acc = HomomorphicAdd(acc, ct)
		}
		totalValue += v
	}

	// Decrypt to verify the sum matches.
	vG := DecryptToPoint(sk, acc)
	G := PallasGenerator()
	expectedVG := G.Mul(scalarFromUint64(totalValue))
	require.True(t, vG.Equal(expectedVG), "decrypted point should match totalValue*G")

	// Generate and verify DLEQ proof for the accumulated ciphertext.
	proof, err := GenerateDLEQProof(sk, acc, totalValue)
	require.NoError(t, err)

	err = VerifyDLEQProof(proof, pk, acc, totalValue)
	require.NoError(t, err, "DLEQ proof should verify for accumulated ciphertext")

	// Wrong total should fail.
	err = VerifyDLEQProof(proof, pk, acc, totalValue+1)
	require.Error(t, err)
}

func TestDLEQWrongSecretKey(t *testing.T) {
	// Proof generated with a random sk that doesn't match the pk used for encryption.
	_, pk := KeyGen(rand.Reader)
	ct, err := Encrypt(pk, 50, rand.Reader)
	require.NoError(t, err)

	// Generate proof with a completely unrelated secret key.
	skFake, _ := KeyGen(rand.Reader)
	proof, err := GenerateDLEQProof(skFake, ct, 50)
	require.NoError(t, err)

	err = VerifyDLEQProof(proof, pk, ct, 50)
	require.Error(t, err)
	require.Contains(t, err.Error(), "verification failed")
}

func TestDLEQAllZeroProof(t *testing.T) {
	_, pk := KeyGen(rand.Reader)
	ct, err := Encrypt(pk, 10, rand.Reader)
	require.NoError(t, err)

	// 64 zero bytes: e=0, z=0.
	zeroProof := make([]byte, DLEQProofSize)
	err = VerifyDLEQProof(zeroProof, pk, ct, 10)
	require.Error(t, err)
}

func TestDLEQWrongProofLength(t *testing.T) {
	_, pk := KeyGen(rand.Reader)
	ct, err := Encrypt(pk, 10, rand.Reader)
	require.NoError(t, err)

	// Truncated proof.
	err = VerifyDLEQProof(make([]byte, DLEQProofSize-1), pk, ct, 10)
	require.Error(t, err)
	require.Contains(t, err.Error(), "expected")

	// Oversized proof.
	err = VerifyDLEQProof(make([]byte, DLEQProofSize+1), pk, ct, 10)
	require.Error(t, err)
	require.Contains(t, err.Error(), "expected")

	// Empty proof.
	err = VerifyDLEQProof([]byte{}, pk, ct, 10)
	require.Error(t, err)

	// Nil proof.
	err = VerifyDLEQProof(nil, pk, ct, 10)
	require.Error(t, err)
}

func TestDLEQNilInputs(t *testing.T) {
	sk, pk := KeyGen(rand.Reader)
	ct, err := Encrypt(pk, 10, rand.Reader)
	require.NoError(t, err)

	proof, err := GenerateDLEQProof(sk, ct, 10)
	require.NoError(t, err)

	// Nil public key.
	err = VerifyDLEQProof(proof, nil, ct, 10)
	require.Error(t, err)
	require.Contains(t, err.Error(), "public key must not be nil")

	// Public key with nil point.
	err = VerifyDLEQProof(proof, &PublicKey{Point: nil}, ct, 10)
	require.Error(t, err)
	require.Contains(t, err.Error(), "public key must not be nil")

	// Nil ciphertext.
	err = VerifyDLEQProof(proof, pk, nil, 10)
	require.Error(t, err)
	require.Contains(t, err.Error(), "ciphertext must not be nil")

	// Ciphertext with nil C1.
	err = VerifyDLEQProof(proof, pk, &Ciphertext{C1: nil, C2: ct.C2}, 10)
	require.Error(t, err)
	require.Contains(t, err.Error(), "ciphertext must not be nil")

	// Ciphertext with nil C2.
	err = VerifyDLEQProof(proof, pk, &Ciphertext{C1: ct.C1, C2: nil}, 10)
	require.Error(t, err)
	require.Contains(t, err.Error(), "ciphertext must not be nil")
}

func TestDLEQIdentityInputs(t *testing.T) {
	sk, pk := KeyGen(rand.Reader)
	ct, err := Encrypt(pk, 10, rand.Reader)
	require.NoError(t, err)

	proof, err := GenerateDLEQProof(sk, ct, 10)
	require.NoError(t, err)

	identity := new(curvey.PointPallas).Identity()

	// Identity public key should not verify (proof was generated for a real pk).
	err = VerifyDLEQProof(proof, &PublicKey{Point: identity}, ct, 10)
	require.Error(t, err)
	require.Contains(t, err.Error(), "verification failed")

	// Identity C1 should not verify.
	err = VerifyDLEQProof(proof, pk, &Ciphertext{C1: identity, C2: ct.C2}, 10)
	require.Error(t, err)
	require.Contains(t, err.Error(), "verification failed")
}

func TestDLEQGenerateNilInputs(t *testing.T) {
	sk, pk := KeyGen(rand.Reader)
	ct, err := Encrypt(pk, 10, rand.Reader)
	require.NoError(t, err)

	// Nil secret key.
	_, err = GenerateDLEQProof(nil, ct, 10)
	require.Error(t, err)
	require.Contains(t, err.Error(), "secret key must not be nil")

	// Secret key with nil scalar.
	_, err = GenerateDLEQProof(&SecretKey{Scalar: nil}, ct, 10)
	require.Error(t, err)
	require.Contains(t, err.Error(), "secret key must not be nil")

	// Nil ciphertext.
	_, err = GenerateDLEQProof(sk, nil, 10)
	require.Error(t, err)
	require.Contains(t, err.Error(), "ciphertext must not be nil")
}
