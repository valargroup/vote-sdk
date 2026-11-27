package types

import (
	"errors"

	cmted25519 "github.com/cometbft/cometbft/crypto/ed25519"
	"github.com/oasisprotocol/curve25519-voi/curve"
	"github.com/oasisprotocol/curve25519-voi/curve/scalar"
)

// Ed25519 key-rule and signature failures. They carry no ABCI code, so a
// caller wraps them in its path's registered error: ErrInvalidDelegateKey for
// the key rule and ErrInvalidSignature for a signature
// (docs/proxy-delegation/contracts-v1.md §6).
var (
	ErrEd25519KeyLength            = errors.New("ed25519 public key must be 32 bytes")
	ErrEd25519KeyNotOnCurve        = errors.New("ed25519 public key is not a curve point")
	ErrEd25519KeyNonCanonical      = errors.New("ed25519 public key encoding is not canonical")
	ErrEd25519KeySmallOrder        = errors.New("ed25519 public key has small order")
	ErrEd25519KeyNotTorsionFree    = errors.New("ed25519 public key has a torsion component")
	ErrEd25519SignatureLength      = errors.New("ed25519 signature must be 64 bytes")
	ErrEd25519ScalarNotCanonical   = errors.New("ed25519 signature scalar is not below the group order")
	ErrEd25519CommitmentNotOnCurve = errors.New("ed25519 signature commitment is not a curve point")
	ErrEd25519EquationMismatch     = errors.New("ed25519 signature does not verify")
)

// ValidateEd25519Key applies the delegate and verifier key rule
// (contracts-v1 §0): key must decode to a curve point, be that point's
// canonical encoding, not be of small order, and be torsion-free
// ([ℓ]A = O). It checks in that order and returns the first failure.
func ValidateEd25519Key(key []byte) error {
	var encoded curve.CompressedEdwardsY
	if _, err := encoded.SetBytes(key); err != nil {
		return ErrEd25519KeyLength
	}
	var point curve.EdwardsPoint
	if _, err := point.SetCompressedY(&encoded); err != nil {
		return ErrEd25519KeyNotOnCurve
	}
	var canonical curve.CompressedEdwardsY
	if *canonical.SetEdwardsPoint(&point) != encoded {
		return ErrEd25519KeyNonCanonical
	}
	if point.IsSmallOrder() {
		return ErrEd25519KeySmallOrder
	}
	if !point.IsTorsionFree() {
		return ErrEd25519KeyNotTorsionFree
	}
	return nil
}

// VerifyEd25519ZIP215 verifies sig over msg under pub with the ZIP-215 rules
// that CometBFT applies to validator signatures (contracts-v1 §0): A and R may
// be non-canonical or of small order, S must be below the group order, and
// the cofactored equation [8][S]B = [8]R + [8][k]A must hold. It does not
// apply the key rule; see ValidateEd25519Key.
func VerifyEd25519ZIP215(pub, msg, sig []byte) error {
	var encoded curve.CompressedEdwardsY
	if _, err := encoded.SetBytes(pub); err != nil {
		return ErrEd25519KeyLength
	}
	if len(sig) != cmted25519.SignatureSize {
		return ErrEd25519SignatureLength
	}
	// These three checks mirror CometBFT's own, in its order, so a failure
	// reports its cause instead of a bare mismatch.
	var point curve.EdwardsPoint
	if _, err := point.SetCompressedY(&encoded); err != nil {
		return ErrEd25519KeyNotOnCurve
	}
	if !scalar.ScMinimalVartime(sig[32:]) {
		return ErrEd25519ScalarNotCanonical
	}
	var r curve.CompressedEdwardsY
	copy(r[:], sig[:32])
	if _, err := point.SetCompressedY(&r); err != nil {
		return ErrEd25519CommitmentNotOnCurve
	}
	if !cmted25519.PubKey(pub).VerifySignature(msg, sig) {
		return ErrEd25519EquationMismatch
	}
	return nil
}
