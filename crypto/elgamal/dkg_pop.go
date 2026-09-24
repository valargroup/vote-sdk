package elgamal

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"hash"
	"io"

	"github.com/mikelodder7/curvey"
	"golang.org/x/crypto/blake2b"
)

const (
	constantTermProofDomain = "svote-dkg-constant-term-pok-v1"
	constantTermProofSize   = 2 * CompressedPointSize
)

// GenerateConstantTermProof proves knowledge of the scalar behind the first
// Feldman commitment. The proof is bound to this dealer and round's complete
// commitment vector, and is encoded as R || z.
func GenerateConstantTermProof(
	secret curvey.Scalar,
	chainID string,
	roundID []byte,
	dealer string,
	commitments [][]byte,
) ([]byte, error) {
	if secret == nil || secret.IsZero() {
		return nil, fmt.Errorf("constant-term proof: secret must be nonzero")
	}
	constant, err := constantTermPoint(commitments)
	if err != nil {
		return nil, err
	}
	G := PallasGenerator()
	if !G.Mul(secret).Equal(constant) {
		return nil, fmt.Errorf("constant-term proof: secret does not match commitment")
	}

	var nonce curvey.Scalar
	for nonce == nil || nonce.IsZero() {
		var seed [64]byte
		if _, err := io.ReadFull(rand.Reader, seed[:]); err != nil {
			return nil, fmt.Errorf("constant-term proof: read nonce: %w", err)
		}
		nonce = new(curvey.ScalarPallas).Hash(seed[:])
	}
	R := G.Mul(nonce)
	challenge := constantTermChallenge(chainID, roundID, dealer, commitments, R)
	z := nonce.Add(challenge.Mul(secret))

	proof := make([]byte, 0, constantTermProofSize)
	proof = append(proof, R.ToAffineCompressed()...)
	proof = append(proof, z.Bytes()...)
	return proof, nil
}

// VerifyConstantTermProof checks that a dealer knows the discrete log of the
// first Feldman commitment for the specified round and commitment vector.
func VerifyConstantTermProof(
	proof []byte,
	chainID string,
	roundID []byte,
	dealer string,
	commitments [][]byte,
) error {
	if len(proof) != constantTermProofSize {
		return fmt.Errorf("constant-term proof: expected %d bytes, got %d", constantTermProofSize, len(proof))
	}
	constant, err := constantTermPoint(commitments)
	if err != nil {
		return err
	}
	Rpk, err := UnmarshalPublicKey(proof[:CompressedPointSize])
	if err != nil {
		return fmt.Errorf("constant-term proof: invalid nonce point: %w", err)
	}
	z, err := new(curvey.ScalarPallas).SetBytes(proof[CompressedPointSize:])
	if err != nil {
		return fmt.Errorf("constant-term proof: invalid response scalar: %w", err)
	}
	challenge := constantTermChallenge(chainID, roundID, dealer, commitments, Rpk.Point)
	G := PallasGenerator()
	if !G.Mul(z).Equal(Rpk.Point.Add(constant.Mul(challenge))) {
		return fmt.Errorf("constant-term proof: verification failed")
	}
	return nil
}

func constantTermPoint(commitments [][]byte) (curvey.Point, error) {
	if len(commitments) == 0 {
		return nil, fmt.Errorf("constant-term proof: empty commitment vector")
	}
	pk, err := UnmarshalPublicKey(commitments[0])
	if err != nil {
		return nil, fmt.Errorf("constant-term proof: invalid constant commitment: %w", err)
	}
	return pk.Point, nil
}

func constantTermChallenge(
	chainID string,
	roundID []byte,
	dealer string,
	commitments [][]byte,
	R curvey.Point,
) curvey.Scalar {
	h, _ := blake2b.New256(nil)
	writeProofField(h, []byte(constantTermProofDomain))
	writeProofField(h, []byte(chainID))
	writeProofField(h, roundID)
	writeProofField(h, []byte(dealer))
	writeProofField(h, PallasGenerator().ToAffineCompressed())
	var count [8]byte
	binary.BigEndian.PutUint64(count[:], uint64(len(commitments)))
	h.Write(count[:])
	for _, commitment := range commitments {
		writeProofField(h, commitment)
	}
	writeProofField(h, R.ToAffineCompressed())
	return new(curvey.ScalarPallas).Hash(h.Sum(nil))
}

func writeProofField(h hash.Hash, field []byte) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(field)))
	h.Write(length[:])
	h.Write(field)
}
