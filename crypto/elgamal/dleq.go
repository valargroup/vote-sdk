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
)

// ---------------------------------------------------------------------------
// Per-validator partial-decryption DLEQ
// ---------------------------------------------------------------------------
//
// These functions prove/verify that a validator's partial decryption D_i was
// computed with the same secret scalar as their on-chain verification key VK_i:
//
//   log_G(VK_i) == log_{C1}(D_i)
//
// A distinct domain tag ("svote-pd-dleq-v1") is used so that proofs from the
// tally-level DLEQ (which proves log_G(ea_pk) == log_{C1}(D) under
// "svote-dleq-v1") cannot be replayed against this protocol, and vice versa.

const pdDleqDomainTag = "svote-pd-dleq-v1"

// GeneratePartialDecryptDLEQ generates a Chaum-Pedersen DLEQ proof that
// D_i = share * C1, where VK_i = share * G is the validator's committed
// verification key.
//
// Proves: log_G(VK_i) = log_C1(D_i)
//
// Returns a 64-byte proof: e || z.
func GeneratePartialDecryptDLEQ(share curvey.Scalar, C1 curvey.Point) ([]byte, error) {
	if share == nil || share.IsZero() {
		return nil, fmt.Errorf("elgamal: GeneratePartialDecryptDLEQ: share must not be nil or zero")
	}
	if C1 == nil {
		return nil, fmt.Errorf("elgamal: GeneratePartialDecryptDLEQ: C1 must not be nil")
	}
	if !C1.IsOnCurve() {
		return nil, fmt.Errorf("elgamal: GeneratePartialDecryptDLEQ: C1 is not on the Pallas curve")
	}

	G := PallasGenerator()
	VKi := G.Mul(share)
	Di := C1.Mul(share)

	var seed [64]byte
	if _, err := io.ReadFull(rand.Reader, seed[:]); err != nil {
		return nil, fmt.Errorf("elgamal: GeneratePartialDecryptDLEQ: failed to read randomness: %w", err)
	}
	k := new(curvey.ScalarPallas).Hash(seed[:])

	R1 := G.Mul(k)
	R2 := C1.Mul(k)

	e := pdDleqChallenge(G, VKi, C1, Di, R1, R2)

	// z = k + e * share
	z := e.Mul(share).Add(k)

	eBytes := e.Bytes()
	zBytes := z.Bytes()
	proof := make([]byte, DLEQProofSize)
	copy(proof[:CompressedPointSize], eBytes)
	copy(proof[CompressedPointSize:], zBytes)
	return proof, nil
}

// VerifyPartialDecryptDLEQ verifies a Chaum-Pedersen DLEQ proof that the
// partial decryption D_i was computed with the same scalar as VK_i.
//
// Verifies: log_G(VK_i) = log_C1(D_i)
//
// Returns nil on success, an error on failure.
func VerifyPartialDecryptDLEQ(proof []byte, VKi, C1, Di curvey.Point) error {
	if len(proof) != DLEQProofSize {
		return fmt.Errorf("elgamal: VerifyPartialDecryptDLEQ: expected %d bytes, got %d", DLEQProofSize, len(proof))
	}
	if VKi == nil {
		return fmt.Errorf("elgamal: VerifyPartialDecryptDLEQ: VK_i must not be nil")
	}
	if !VKi.IsOnCurve() || VKi.IsIdentity() {
		return fmt.Errorf("elgamal: VerifyPartialDecryptDLEQ: VK_i must be a valid non-identity point on the Pallas curve")
	}
	if C1 == nil {
		return fmt.Errorf("elgamal: VerifyPartialDecryptDLEQ: C1 must not be nil")
	}
	if !C1.IsOnCurve() || C1.IsIdentity() {
		return fmt.Errorf("elgamal: VerifyPartialDecryptDLEQ: C1 must be a valid non-identity point on the Pallas curve")
	}
	if Di == nil {
		return fmt.Errorf("elgamal: VerifyPartialDecryptDLEQ: D_i must not be nil")
	}
	if !Di.IsOnCurve() {
		return fmt.Errorf("elgamal: VerifyPartialDecryptDLEQ: D_i must be on the Pallas curve")
	}

	e, err := new(curvey.ScalarPallas).SetBytes(proof[:CompressedPointSize])
	if err != nil {
		return fmt.Errorf("elgamal: VerifyPartialDecryptDLEQ: invalid challenge scalar: %w", err)
	}
	z, err := new(curvey.ScalarPallas).SetBytes(proof[CompressedPointSize:])
	if err != nil {
		return fmt.Errorf("elgamal: VerifyPartialDecryptDLEQ: invalid response scalar: %w", err)
	}

	G := PallasGenerator()

	// R1 = z*G - e*VK_i
	R1 := G.Mul(z).Sub(VKi.Mul(e))

	// R2 = z*C1 - e*D_i
	R2 := C1.Mul(z).Sub(Di.Mul(e))

	ePrime := pdDleqChallenge(G, VKi, C1, Di, R1, R2)

	if e.Cmp(ePrime) != 0 {
		return fmt.Errorf("elgamal: VerifyPartialDecryptDLEQ: proof verification failed")
	}
	return nil
}

// pdDleqChallenge computes the Fiat-Shamir challenge for the partial-decryption
// DLEQ variant:
//
//	e = HashToScalar("svote-pd-dleq-v1" || G || VK_i || C1 || D_i || R1 || R2)
func pdDleqChallenge(G, VKi, C1, Di, R1, R2 curvey.Point) curvey.Scalar {
	h, _ := blake2b.New256(nil)
	h.Write([]byte(pdDleqDomainTag))
	h.Write(G.ToAffineCompressed())
	h.Write(VKi.ToAffineCompressed())
	h.Write(C1.ToAffineCompressed())
	h.Write(Di.ToAffineCompressed())
	h.Write(R1.ToAffineCompressed())
	h.Write(R2.ToAffineCompressed())
	digest := h.Sum(nil)
	return new(curvey.ScalarPallas).Hash(digest)
}
