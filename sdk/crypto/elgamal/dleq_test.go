package elgamal

import (
	"crypto/rand"
	"testing"

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
