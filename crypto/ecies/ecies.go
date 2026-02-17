package ecies

import (
	"crypto/sha256"
	"fmt"
	"io"

	"github.com/mikelodder7/curvey"
	"golang.org/x/crypto/chacha20poly1305"
)

// CompressedPointSize is the size of a compressed Pallas curve point (32 bytes).
const CompressedPointSize = 32

// Envelope holds an ECIES ciphertext: an ephemeral public key and an
// authenticated ciphertext produced by ChaCha20-Poly1305.
type Envelope struct {
	Ephemeral  curvey.Point // E = e * G (ephemeral public key)
	Ciphertext []byte       // ChaCha20-Poly1305 output (len = plaintext_len + 16)
}

// Encrypt performs ECIES encryption of plaintext to recipientPK using the
// Pallas curve. The caller provides the generator point G (typically
// elgamal.PallasGenerator()) and a cryptographic random source.
//
// The scheme:
//  1. e ← random scalar
//  2. E = e * G                            (ephemeral public key)
//  3. S = e * recipientPK                  (ECDH shared secret)
//  4. k = SHA256(E_compressed || S.x)      (32-byte symmetric key)
//  5. ct = ChaCha20-Poly1305(k, nonce=0, plaintext)
//
// The zero nonce is safe because each ephemeral key e is fresh, making the
// derived symmetric key k unique per encryption.
func Encrypt(G, recipientPK curvey.Point, plaintext []byte, rng io.Reader) (*Envelope, error) {
	if err := validatePoint(G, "generator"); err != nil {
		return nil, fmt.Errorf("ecies: Encrypt: %w", err)
	}
	if err := validatePoint(recipientPK, "recipient public key"); err != nil {
		return nil, fmt.Errorf("ecies: Encrypt: %w", err)
	}
	if len(plaintext) == 0 {
		return nil, fmt.Errorf("ecies: Encrypt: plaintext must not be empty")
	}
	if rng == nil {
		return nil, fmt.Errorf("ecies: Encrypt: rng must not be nil")
	}

	// Generate ephemeral scalar. Read seed ourselves and hash it, matching
	// the pattern in elgamal.go (curvey's Random() silently returns nil on
	// reader failure).
	var seed [64]byte
	if _, err := io.ReadFull(rng, seed[:]); err != nil {
		return nil, fmt.Errorf("ecies: Encrypt: failed to read randomness: %w", err)
	}
	e := new(curvey.ScalarPallas).Hash(seed[:])

	// E = e * G (ephemeral public key)
	E := G.Mul(e)

	// S = e * recipientPK (ECDH shared secret)
	S := recipientPK.Mul(e)

	// Derive symmetric key
	key := deriveKey(E, S)

	// Encrypt with ChaCha20-Poly1305, zero nonce
	aead, err := chacha20poly1305.New(key[:])
	if err != nil {
		return nil, fmt.Errorf("ecies: Encrypt: failed to create AEAD: %w", err)
	}
	nonce := make([]byte, chacha20poly1305.NonceSize) // all zeros
	ct := aead.Seal(nil, nonce, plaintext, nil)

	return &Envelope{
		Ephemeral:  E,
		Ciphertext: ct,
	}, nil
}

// Decrypt performs ECIES decryption using the recipient's secret key.
//
// The scheme:
//  1. S = recipientSK * E                  (ECDH shared secret)
//  2. k = SHA256(E_compressed || S.x)      (derive same symmetric key)
//  3. plaintext = ChaCha20-Poly1305.Open(k, nonce=0, ciphertext)
func Decrypt(recipientSK curvey.Scalar, env *Envelope) ([]byte, error) {
	if recipientSK == nil || recipientSK.IsZero() {
		return nil, fmt.Errorf("ecies: Decrypt: secret key must not be nil or zero")
	}
	if env == nil {
		return nil, fmt.Errorf("ecies: Decrypt: envelope must not be nil")
	}
	if err := validatePoint(env.Ephemeral, "ephemeral public key"); err != nil {
		return nil, fmt.Errorf("ecies: Decrypt: %w", err)
	}
	if len(env.Ciphertext) < chacha20poly1305.Overhead {
		return nil, fmt.Errorf("ecies: Decrypt: ciphertext too short")
	}

	// S = sk * E (ECDH shared secret)
	S := env.Ephemeral.Mul(recipientSK)

	// Derive symmetric key
	key := deriveKey(env.Ephemeral, S)

	// Decrypt with ChaCha20-Poly1305, zero nonce
	aead, err := chacha20poly1305.New(key[:])
	if err != nil {
		return nil, fmt.Errorf("ecies: Decrypt: failed to create AEAD: %w", err)
	}
	nonce := make([]byte, chacha20poly1305.NonceSize) // all zeros
	plaintext, err := aead.Open(nil, nonce, env.Ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("ecies: Decrypt: authentication failed: %w", err)
	}
	return plaintext, nil
}

// deriveKey computes the ECIES symmetric key:
//
//	k = SHA256(E_compressed || S.x)
//
// where E is the ephemeral public key and S.x is the x-coordinate of
// the ECDH shared secret point.
func deriveKey(E, S curvey.Point) [32]byte {
	eBytes := E.ToAffineCompressed()
	sX := xCoordinate(S)

	h := sha256.New()
	h.Write(eBytes)
	h.Write(sX)

	var key [32]byte
	copy(key[:], h.Sum(nil))
	return key
}

// xCoordinate extracts the raw x-coordinate from a Pallas point.
// The compressed encoding is 32 bytes: little-endian x with the sign bit
// in bit 7 of byte[31]. We clear that bit to get the canonical x value.
func xCoordinate(p curvey.Point) []byte {
	compressed := p.ToAffineCompressed()
	x := make([]byte, CompressedPointSize)
	copy(x, compressed)
	x[31] &= 0x7F // clear sign bit
	return x
}

// validatePoint checks that a point is non-nil, on the Pallas curve,
// and not the identity.
func validatePoint(p curvey.Point, label string) error {
	if p == nil {
		return fmt.Errorf("%s must not be nil", label)
	}
	if p.IsIdentity() {
		return fmt.Errorf("%s must not be the identity point", label)
	}
	if !p.IsOnCurve() {
		return fmt.Errorf("%s is not on the Pallas curve", label)
	}
	return nil
}
