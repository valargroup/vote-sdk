package elgamal

import (
	"crypto/rand"
	"fmt"
	"io"

	"github.com/mikelodder7/curvey"
	"golang.org/x/crypto/blake2b"
)

const (
	// DLEQProofSize is the serialized size of a DLEQ proof: e (32 bytes) || z (32 bytes).
	DLEQProofSize = 2 * CompressedPointSize // 64 bytes

	// dleqDomainTag is the domain separator for the Fiat-Shamir challenge hash.
	dleqDomainTag = "zally-dleq-v1"
)

// GenerateDLEQProof generates a Chaum-Pedersen DLEQ proof that the EA correctly
// decrypted the ciphertext ct to totalValue using secret key sk.
//
// Proves: log_G(ea_pk) = log_C1(C2 - totalValue*G)
//
// Returns a 64-byte proof: e || z.
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

	// Sample random k following the same pattern as Encrypt (elgamal.go:77-83)
	var seed [64]byte
	if _, err := io.ReadFull(rand.Reader, seed[:]); err != nil {
		return nil, fmt.Errorf("elgamal: GenerateDLEQProof: failed to read randomness: %w", err)
	}
	k := new(curvey.ScalarPallas).Hash(seed[:])

	// R1 = k*G, R2 = k*C1
	R1 := G.Mul(k)
	R2 := ct.C1.Mul(k)

	// Fiat-Shamir challenge
	e := dleqChallenge(G, eaPk, ct.C1, D, R1, R2)

	// z = k + e * sk
	z := e.Mul(sk.Scalar).Add(k) // e*sk + k

	// Serialize: e || z
	eBytes := e.Bytes()
	zBytes := z.Bytes()
	proof := make([]byte, DLEQProofSize)
	copy(proof[:CompressedPointSize], eBytes)
	copy(proof[CompressedPointSize:], zBytes)
	return proof, nil
}

// VerifyDLEQProof verifies a Chaum-Pedersen DLEQ proof that the EA correctly
// decrypted the ciphertext ct to totalValue.
//
// Verifies: log_G(pk) = log_C1(C2 - totalValue*G)
//
// Security assumptions on inputs:
//   - pk and ct points must be on the Pallas curve. This is enforced at
//     deserialization time by UnmarshalPublicKey and UnmarshalCiphertext,
//     which call FromAffineCompressed (rejects off-curve points).
//   - The proof contains only scalars (e, z), not points. SetBytes validates
//     each scalar is a canonical encoding in the Pallas scalar field Fq.
//
// Returns nil on success, an error on failure.
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

	// Deserialize e and z. SetBytes rejects non-canonical encodings and
	// values outside the scalar field, so no further validation is needed.
	e, err := new(curvey.ScalarPallas).SetBytes(proof[:CompressedPointSize])
	if err != nil {
		return fmt.Errorf("elgamal: VerifyDLEQProof: invalid challenge scalar: %w", err)
	}
	z, err := new(curvey.ScalarPallas).SetBytes(proof[CompressedPointSize:])
	if err != nil {
		return fmt.Errorf("elgamal: VerifyDLEQProof: invalid response scalar: %w", err)
	}

	G := PallasGenerator()

	// D = C2 - totalValue*G
	vG := G.Mul(scalarFromUint64(totalValue))
	D := ct.C2.Sub(vG)

	// R1 = z*G - e*pk
	R1 := G.Mul(z).Sub(pk.Point.Mul(e))

	// R2 = z*C1 - e*D
	R2 := ct.C1.Mul(z).Sub(D.Mul(e))

	// Recompute challenge
	ePrime := dleqChallenge(G, pk.Point, ct.C1, D, R1, R2)

	// Check e' == e
	if e.Cmp(ePrime) != 0 {
		return fmt.Errorf("elgamal: VerifyDLEQProof: proof verification failed")
	}
	return nil
}

// dleqChallenge computes the Fiat-Shamir challenge hash for Chaum-Pedersen:
//
//	e = HashToScalar("zally-dleq-v1" || G || pk || C1 || D || R1 || R2)
//
// The hash binds the challenge to the full proof statement (G, pk, C1, D) and
// the prover's commitments (R1, R2), with a domain separator tag to prevent
// cross-protocol replays. This is the standard Fiat-Shamir transform for DLEQ;
func dleqChallenge(G, pk, C1, D, R1, R2 curvey.Point) curvey.Scalar {
	h, _ := blake2b.New256(nil) // unkeyed; never errors
	h.Write([]byte(dleqDomainTag))
	h.Write(G.ToAffineCompressed())
	h.Write(pk.ToAffineCompressed())
	h.Write(C1.ToAffineCompressed())
	h.Write(D.ToAffineCompressed())
	h.Write(R1.ToAffineCompressed())
	h.Write(R2.ToAffineCompressed())
	digest := h.Sum(nil) // 32 bytes
	return new(curvey.ScalarPallas).Hash(digest)
}
