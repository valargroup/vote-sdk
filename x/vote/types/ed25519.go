package types

import (
	"errors"

	cmted25519 "github.com/cometbft/cometbft/crypto/ed25519"
	"github.com/oasisprotocol/curve25519-voi/curve"
)

// ValidateEd25519Key applies the delegate and verifier key rule
// (contracts-v1 §0): key must decode to a curve point, be that point's
// canonical encoding, not be of small order, and be torsion-free
// ([ℓ]A = O). It checks in that order and returns the first failure. Its
// errors carry no ABCI code, so a caller wraps them in its path's registered
// error, ErrInvalidDelegateKey for the key rule (§6).
func ValidateEd25519Key(key []byte) error {
	var encoded curve.CompressedEdwardsY
	if _, err := encoded.SetBytes(key); err != nil {
		return errors.New("ed25519 public key must be 32 bytes")
	}
	var point curve.EdwardsPoint
	if _, err := point.SetCompressedY(&encoded); err != nil {
		return errors.New("ed25519 public key is not a curve point")
	}
	var canonical curve.CompressedEdwardsY
	if *canonical.SetEdwardsPoint(&point) != encoded {
		return errors.New("ed25519 public key encoding is not canonical")
	}
	if point.IsSmallOrder() {
		return errors.New("ed25519 public key has small order")
	}
	if !point.IsTorsionFree() {
		return errors.New("ed25519 public key has a torsion component")
	}
	return nil
}

// VerifyEd25519ZIP215 verifies sig over msg under pub with the ZIP-215 rules
// that CometBFT applies to validator signatures (contracts-v1 §0): A and R may
// be non-canonical or of small order, S must be below the group order, and
// the cofactored equation [8][S]B = [8]R + [8][k]A must hold. It does not
// apply the key rule; see ValidateEd25519Key. Its errors carry no ABCI code,
// so a caller wraps them in ErrInvalidSignature or ErrInvalidAttestation.
func VerifyEd25519ZIP215(pub, msg, sig []byte) error {
	if len(pub) != cmted25519.PubKeySize {
		return errors.New("ed25519 public key must be 32 bytes")
	}
	if !cmted25519.PubKey(pub).VerifySignature(msg, sig) {
		return errors.New("ed25519 signature does not verify")
	}
	return nil
}
