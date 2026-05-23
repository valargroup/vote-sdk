package elgamal

import (
	"crypto/rand"
	"fmt"
	"io"

	"github.com/mikelodder7/curvey"
	"golang.org/x/crypto/blake2b"
)

const dleqDomainTag = "svote-dleq-v1"

// GenerateDLEQProof generates a test-only aggregate Chaum-Pedersen DLEQ proof
// that the EA correctly decrypted ct to totalValue using secret key sk.
func GenerateDLEQProof(sk *SecretKey, ct *Ciphertext, totalValue uint64) ([]byte, error) {
	if sk == nil || sk.Scalar == nil {
		return nil, fmt.Errorf("elgamal: GenerateDLEQProof: secret key must not be nil")
	}
	if ct == nil || ct.C1 == nil || ct.C2 == nil {
		return nil, fmt.Errorf("elgamal: GenerateDLEQProof: ciphertext must not be nil")
	}

	G := PallasGenerator()
	eaPk := G.Mul(sk.Scalar)

	// D = C2 - totalValue*G (should equal sk*C1 if decryption is correct)
	vG := G.Mul(scalarFromUint64(totalValue))
	D := ct.C2.Sub(vG)

	var seed [64]byte
	if _, err := io.ReadFull(rand.Reader, seed[:]); err != nil {
		return nil, fmt.Errorf("elgamal: GenerateDLEQProof: failed to read randomness: %w", err)
	}
	k := new(curvey.ScalarPallas).Hash(seed[:])

	R1 := G.Mul(k)
	R2 := ct.C1.Mul(k)

	e := dleqChallenge(G, eaPk, ct.C1, D, R1, R2)
	z := e.Mul(sk.Scalar).Add(k)

	eBytes := e.Bytes()
	zBytes := z.Bytes()
	proof := make([]byte, DLEQProofSize)
	copy(proof[:CompressedPointSize], eBytes)
	copy(proof[CompressedPointSize:], zBytes)
	return proof, nil
}

// VerifyDLEQProof verifies a test-only aggregate Chaum-Pedersen DLEQ proof
// that ct decrypts to totalValue under pk.
func VerifyDLEQProof(proof []byte, pk *PublicKey, ct *Ciphertext, totalValue uint64) error {
	if len(proof) != DLEQProofSize {
		return fmt.Errorf("elgamal: VerifyDLEQProof: expected %d bytes, got %d", DLEQProofSize, len(proof))
	}
	if pk == nil || pk.Point == nil {
		return fmt.Errorf("elgamal: VerifyDLEQProof: public key must not be nil")
	}
	if ct == nil || ct.C1 == nil || ct.C2 == nil {
		return fmt.Errorf("elgamal: VerifyDLEQProof: ciphertext must not be nil")
	}

	e, err := new(curvey.ScalarPallas).SetBytes(proof[:CompressedPointSize])
	if err != nil {
		return fmt.Errorf("elgamal: VerifyDLEQProof: invalid challenge scalar: %w", err)
	}
	z, err := new(curvey.ScalarPallas).SetBytes(proof[CompressedPointSize:])
	if err != nil {
		return fmt.Errorf("elgamal: VerifyDLEQProof: invalid response scalar: %w", err)
	}

	G := PallasGenerator()

	vG := G.Mul(scalarFromUint64(totalValue))
	D := ct.C2.Sub(vG)

	R1 := G.Mul(z).Sub(pk.Point.Mul(e))
	R2 := ct.C1.Mul(z).Sub(D.Mul(e))

	ePrime := dleqChallenge(G, pk.Point, ct.C1, D, R1, R2)
	if e.Cmp(ePrime) != 0 {
		return fmt.Errorf("elgamal: VerifyDLEQProof: proof verification failed")
	}
	return nil
}

// dleqChallenge computes the Fiat-Shamir challenge for the test-only aggregate
// DLEQ proof variant.
func dleqChallenge(G, pk, C1, D, R1, R2 curvey.Point) curvey.Scalar {
	h, _ := blake2b.New256(nil)
	h.Write([]byte(dleqDomainTag))
	h.Write(G.ToAffineCompressed())
	h.Write(pk.ToAffineCompressed())
	h.Write(C1.ToAffineCompressed())
	h.Write(D.ToAffineCompressed())
	h.Write(R1.ToAffineCompressed())
	h.Write(R2.ToAffineCompressed())
	digest := h.Sum(nil)
	return new(curvey.ScalarPallas).Hash(digest)
}
