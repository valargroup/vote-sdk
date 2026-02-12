package elgamal

import (
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
//
//	Enc(v, r) = (r*G, v*G + r*pk)
func Encrypt(pk *PublicKey, v uint64, rng io.Reader) *Ciphertext {
	r := new(curvey.ScalarPallas).Random(rng)
	return EncryptWithRandomness(pk, v, r)
}

// EncryptWithRandomness encrypts with explicit randomness r.
// This is useful for ZKP witness reproduction where the prover needs to
// re-derive the ciphertext from a known randomness value.
func EncryptWithRandomness(pk *PublicKey, v uint64, r curvey.Scalar) *Ciphertext {
	G := new(curvey.PointPallas).Generator()
	vScalar := scalarFromUint64(v)
	C1 := G.Mul(r)                          // r * G
	C2 := G.Mul(vScalar).Add(pk.Point.Mul(r)) // v*G + r*pk
	return &Ciphertext{C1: C1, C2: C2}
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

// EncryptZero returns an encryption of zero using the identity point for both
// components. This serves as the additive identity for HomomorphicAdd and is
// used to initialize on-chain tally accumulators.
func EncryptZero() *Ciphertext {
	id := new(curvey.PointPallas).Identity()
	return &Ciphertext{C1: id, C2: id}
}

// scalarFromUint64 converts a uint64 value to a Pallas scalar.
// Uses big.Int to safely handle the full uint64 range without truncation.
func scalarFromUint64(v uint64) curvey.Scalar {
	bi := new(big.Int).SetUint64(v)
	s, _ := new(curvey.ScalarPallas).SetBigInt(bi)
	return s
}
