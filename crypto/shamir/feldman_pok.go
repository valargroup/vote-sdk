package shamir

import (
	"encoding/binary"
	"fmt"
	"io"

	"github.com/mikelodder7/curvey"
	"golang.org/x/crypto/blake2b"
)

const (
	// FeldmanOpeningProofPerCommitmentSize is the serialized size of one
	// Schnorr proof: challenge e (32 bytes) || response z (32 bytes).
	FeldmanOpeningProofPerCommitmentSize = 64

	feldmanOpeningProofScalarSize = 32
	feldmanOpeningProofDomainTag  = "svote-dkg-feldman-pok-v1"
)

// GenerateFeldmanOpeningProof proves knowledge of each scalar opening a_j for
// the ordered Feldman commitment vector C_j = a_j * G. The transcript binds the
// proof to the round, validator, and full commitment vector so proofs cannot be
// replayed across DKG contexts or reordered commitments.
func GenerateFeldmanOpeningProof(
	G curvey.Point,
	coeffs []curvey.Scalar,
	commitments []curvey.Point,
	roundID []byte,
	validatorAddress string,
	rng io.Reader,
) ([]byte, error) {
	if err := validateOpeningProofInputs(G, commitments, roundID, validatorAddress); err != nil {
		return nil, fmt.Errorf("shamir: GenerateFeldmanOpeningProof: %w", err)
	}
	if rng == nil {
		return nil, fmt.Errorf("shamir: GenerateFeldmanOpeningProof: rng must not be nil")
	}
	if len(coeffs) != len(commitments) {
		return nil, fmt.Errorf("shamir: GenerateFeldmanOpeningProof: got %d coefficients for %d commitments",
			len(coeffs), len(commitments))
	}

	proof := make([]byte, 0, len(commitments)*FeldmanOpeningProofPerCommitmentSize)
	for i, coeff := range coeffs {
		if coeff == nil || coeff.IsZero() {
			return nil, fmt.Errorf("shamir: GenerateFeldmanOpeningProof: coefficient %d must not be nil or zero", i)
		}
		if !G.Mul(coeff).Equal(commitments[i]) {
			return nil, fmt.Errorf("shamir: GenerateFeldmanOpeningProof: coefficient %d does not open commitment", i)
		}

		var seed [64]byte
		if _, err := io.ReadFull(rng, seed[:]); err != nil {
			return nil, fmt.Errorf("shamir: GenerateFeldmanOpeningProof: failed to read randomness: %w", err)
		}
		k := new(curvey.ScalarPallas).Hash(seed[:])
		R := G.Mul(k)

		e := feldmanOpeningChallenge(roundID, validatorAddress, commitments, i, R)
		z := e.Mul(coeff).Add(k)

		proof = append(proof, e.Bytes()...)
		proof = append(proof, z.Bytes()...)
	}

	return proof, nil
}

// VerifyFeldmanOpeningProof verifies a per-commitment Schnorr proof over the
// ordered Feldman commitment vector.
func VerifyFeldmanOpeningProof(
	G curvey.Point,
	commitments []curvey.Point,
	roundID []byte,
	validatorAddress string,
	proof []byte,
) error {
	if err := validateOpeningProofInputs(G, commitments, roundID, validatorAddress); err != nil {
		return fmt.Errorf("shamir: VerifyFeldmanOpeningProof: %w", err)
	}
	expectedLen := len(commitments) * FeldmanOpeningProofPerCommitmentSize
	if len(proof) != expectedLen {
		return fmt.Errorf("shamir: VerifyFeldmanOpeningProof: expected %d proof bytes, got %d", expectedLen, len(proof))
	}

	for i, commitment := range commitments {
		off := i * FeldmanOpeningProofPerCommitmentSize
		e, err := new(curvey.ScalarPallas).SetBytes(proof[off : off+feldmanOpeningProofScalarSize])
		if err != nil {
			return fmt.Errorf("shamir: VerifyFeldmanOpeningProof: proof %d invalid challenge scalar: %w", i, err)
		}
		z, err := new(curvey.ScalarPallas).SetBytes(proof[off+feldmanOpeningProofScalarSize : off+FeldmanOpeningProofPerCommitmentSize])
		if err != nil {
			return fmt.Errorf("shamir: VerifyFeldmanOpeningProof: proof %d invalid response scalar: %w", i, err)
		}

		R := G.Mul(z).Sub(commitment.Mul(e))
		ePrime := feldmanOpeningChallenge(roundID, validatorAddress, commitments, i, R)
		if e.Cmp(ePrime) != 0 {
			return fmt.Errorf("shamir: VerifyFeldmanOpeningProof: proof %d verification failed", i)
		}
	}

	return nil
}

func validateOpeningProofInputs(G curvey.Point, commitments []curvey.Point, roundID []byte, validatorAddress string) error {
	if G == nil {
		return fmt.Errorf("generator G must not be nil")
	}
	if !G.IsOnCurve() || G.IsIdentity() {
		return fmt.Errorf("generator G must be a valid non-identity point")
	}
	if len(roundID) == 0 {
		return fmt.Errorf("round_id must not be empty")
	}
	if validatorAddress == "" {
		return fmt.Errorf("validator_address must not be empty")
	}
	if len(commitments) == 0 {
		return fmt.Errorf("commitments must not be empty")
	}
	for i, commitment := range commitments {
		if commitment == nil {
			return fmt.Errorf("commitment %d must not be nil", i)
		}
		if !commitment.IsOnCurve() || commitment.IsIdentity() {
			return fmt.Errorf("commitment %d must be a valid non-identity point", i)
		}
	}
	return nil
}

func feldmanOpeningChallenge(
	roundID []byte,
	validatorAddress string,
	commitments []curvey.Point,
	index int,
	R curvey.Point,
) curvey.Scalar {
	h, _ := blake2b.New256(nil) // unkeyed; never errors
	h.Write([]byte(feldmanOpeningProofDomainTag))
	writeLengthPrefixed(h, roundID)
	writeLengthPrefixed(h, []byte(validatorAddress))

	var buf [4]byte
	binary.BigEndian.PutUint32(buf[:], uint32(len(commitments)))
	h.Write(buf[:])
	for _, commitment := range commitments {
		h.Write(commitment.ToAffineCompressed())
	}
	binary.BigEndian.PutUint32(buf[:], uint32(index))
	h.Write(buf[:])
	h.Write(R.ToAffineCompressed())

	digest := h.Sum(nil)
	return new(curvey.ScalarPallas).Hash(digest)
}

func writeLengthPrefixed(w io.Writer, b []byte) {
	var lenBuf [4]byte
	binary.BigEndian.PutUint32(lenBuf[:], uint32(len(b)))
	w.Write(lenBuf[:])
	w.Write(b)
}
