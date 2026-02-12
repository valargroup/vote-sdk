package elgamal

import (
	"fmt"
	"io"
	"math/big"

	"github.com/mikelodder7/curvey"
)

// PublicKey is the election authority's public key: ea_pk = ea_sk * G.
type PublicKey struct {
	Point curvey.Point // *PointPallas
}

// SecretKey is the election authority's secret key.
type SecretKey struct {
	Scalar curvey.Scalar // *ScalarPallas
}

// Ciphertext is an El Gamal ciphertext: (C1, C2) = (r*G, v*G + r*pk).
type Ciphertext struct {
	C1 curvey.Point // r * G
	C2 curvey.Point // v * G + r * pk
}

// KeyGen generates an election authority keypair.
// The secret key sk is a random scalar in Fq and the public key is pk = sk * G.
func KeyGen(rng io.Reader) (*SecretKey, *PublicKey) {
	sk := new(curvey.ScalarPallas).Random(rng)
	pk := new(curvey.PointPallas).Generator().Mul(sk)
	return &SecretKey{Scalar: sk}, &PublicKey{Point: pk}
}

// Encrypt encrypts a value v under pk with fresh randomness from rng.
// Returns an error if the public key is invalid or randomness generation fails.
//
//	Enc(v, r) = (r*G, v*G + r*pk)
func Encrypt(pk *PublicKey, v uint64, rng io.Reader) (*Ciphertext, error) {
	if err := validatePublicKey(pk); err != nil {
		return nil, fmt.Errorf("elgamal: Encrypt: %w", err)
	}
	if rng == nil {
		return nil, fmt.Errorf("elgamal: Encrypt: rng must not be nil")
	}
	// curvey's Random() silently returns nil on reader failure, so we
	// read the seed ourselves and check for errors before hashing.
	var seed [64]byte
	if _, err := io.ReadFull(rng, seed[:]); err != nil {
		return nil, fmt.Errorf("elgamal: Encrypt: failed to read randomness: %w", err)
	}
	r := new(curvey.ScalarPallas).Hash(seed[:])
	return encryptCore(pk, v, r), nil
}

// EncryptWithRandomness encrypts with explicit randomness r.
// Returns an error if the public key is invalid or if r is nil/zero.
// This is useful for ZKP witness reproduction where the prover needs to
// re-derive the ciphertext from a known randomness value.
func EncryptWithRandomness(pk *PublicKey, v uint64, r curvey.Scalar) (*Ciphertext, error) {
	if err := validatePublicKey(pk); err != nil {
		return nil, fmt.Errorf("elgamal: EncryptWithRandomness: %w", err)
	}
	if r == nil || r.IsZero() {
		return nil, fmt.Errorf("elgamal: EncryptWithRandomness: randomness must not be nil or zero")
	}
	return encryptCore(pk, v, r), nil
}

// encryptCore performs the actual encryption after all validation has passed.
func encryptCore(pk *PublicKey, v uint64, r curvey.Scalar) *Ciphertext {
	G := new(curvey.PointPallas).Generator()
	vScalar := scalarFromUint64(v)
	C1 := G.Mul(r)                            // r * G
	C2 := G.Mul(vScalar).Add(pk.Point.Mul(r)) // v*G + r*pk
	return &Ciphertext{C1: C1, C2: C2}
}

// validatePublicKey checks that a public key is non-nil, on the Pallas curve,
// and not the identity point. An identity public key would cause every
// ciphertext to leak the plaintext as C2 = v*G (the r*pk term vanishes).
func validatePublicKey(pk *PublicKey) error {
	if pk == nil || pk.Point == nil {
		return fmt.Errorf("public key must not be nil")
	}
	if pk.Point.IsIdentity() {
		return fmt.Errorf("public key must not be the identity point")
	}
	if !pk.Point.IsOnCurve() {
		return fmt.Errorf("public key is not on the Pallas curve")
	}
	return nil
}

// DecryptToPoint decrypts a ciphertext to the embedded value point v*G.
// It does NOT recover the plaintext v; use BSGS (baby-step giant-step) for that.
//
//	C2 - sk * C1 = (v*G + r*pk) - sk*(r*G) = v*G
func DecryptToPoint(sk *SecretKey, ct *Ciphertext) curvey.Point {
	skC1 := ct.C1.Mul(sk.Scalar) // sk * C1 = sk * r * G
	return ct.C2.Sub(skC1)       // C2 - sk*C1 = v*G
}

// HomomorphicAdd sums two ciphertexts component-wise.
// Given Enc(a, r_a) and Enc(b, r_b), the result encrypts a+b:
//
//	(r_a*G + r_b*G, a*G + b*G + (r_a+r_b)*pk) = Enc(a+b, r_a+r_b)
func HomomorphicAdd(a, b *Ciphertext) *Ciphertext {
	return &Ciphertext{
		C1: a.C1.Add(b.C1),
		C2: a.C2.Add(b.C2),
	}
}

// EncryptZero returns a semantically secure (IND-CPA) encryption of zero.
// It uses fresh randomness so the ciphertext is indistinguishable from any
// other encryption, which is critical when used as an on-chain tally
// accumulator whose intermediate states are publicly observable.
//
//	EncryptZero(pk, rng) = Encrypt(pk, 0, rng) = (r*G, r*pk)
func EncryptZero(pk *PublicKey, rng io.Reader) (*Ciphertext, error) {
	return Encrypt(pk, 0, rng)
}

// scalarFromUint64 converts a uint64 value to a Pallas scalar.
// Uses big.Int to safely handle the full uint64 range without truncation.
// Panics if the conversion fails.
func scalarFromUint64(v uint64) curvey.Scalar {
	bi := new(big.Int).SetUint64(v)
	s, err := new(curvey.ScalarPallas).SetBigInt(bi)
	if err != nil {
		panic("elgamal: scalarFromUint64: failed to convert uint64 to Pallas scalar: " + err.Error())
	}
	return s
}
