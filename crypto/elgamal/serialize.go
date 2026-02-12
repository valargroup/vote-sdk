package elgamal

import (
	"fmt"

	"github.com/mikelodder7/curvey"
)

const (
	// CompressedPointSize is the size of a compressed Pallas curve point (32 bytes).
	CompressedPointSize = 32
	// CiphertextSize is the serialized size of an ElGamal ciphertext (C1 || C2).
	CiphertextSize = 2 * CompressedPointSize // 64 bytes
)

// MarshalCiphertext serializes (C1, C2) to 64 bytes (two 32-byte compressed Pallas points).
func MarshalCiphertext(ct *Ciphertext) ([]byte, error) {
	if ct == nil {
		return nil, fmt.Errorf("elgamal: MarshalCiphertext: ciphertext must not be nil")
	}
	if ct.C1 == nil || ct.C2 == nil {
		return nil, fmt.Errorf("elgamal: MarshalCiphertext: C1 and C2 must not be nil")
	}

	c1Bytes := ct.C1.ToAffineCompressed()
	c2Bytes := ct.C2.ToAffineCompressed()

	if len(c1Bytes) != CompressedPointSize {
		return nil, fmt.Errorf("elgamal: MarshalCiphertext: C1 compressed to %d bytes, expected %d", len(c1Bytes), CompressedPointSize)
	}
	if len(c2Bytes) != CompressedPointSize {
		return nil, fmt.Errorf("elgamal: MarshalCiphertext: C2 compressed to %d bytes, expected %d", len(c2Bytes), CompressedPointSize)
	}

	out := make([]byte, CiphertextSize)
	copy(out[:CompressedPointSize], c1Bytes)
	copy(out[CompressedPointSize:], c2Bytes)
	return out, nil
}

// UnmarshalCiphertext deserializes 64 bytes back into a Ciphertext.
func UnmarshalCiphertext(data []byte) (*Ciphertext, error) {
	if len(data) != CiphertextSize {
		return nil, fmt.Errorf("elgamal: UnmarshalCiphertext: expected %d bytes, got %d", CiphertextSize, len(data))
	}

	c1, err := decompressPallasPoint(data[:CompressedPointSize])
	if err != nil {
		return nil, fmt.Errorf("elgamal: UnmarshalCiphertext: failed to decompress C1: %w", err)
	}

	c2, err := decompressPallasPoint(data[CompressedPointSize:])
	if err != nil {
		return nil, fmt.Errorf("elgamal: UnmarshalCiphertext: failed to decompress C2: %w", err)
	}

	return &Ciphertext{C1: c1, C2: c2}, nil
}

// decompressPallasPoint decompresses a 32-byte Pallas point. The identity
// point (point at infinity) serializes to 32 zero bytes but cannot round-trip
// through FromAffineCompressed (standard for projective-coordinate EC libs),
// so we detect the all-zeros sentinel and return the identity directly.
func decompressPallasPoint(data []byte) (curvey.Point, error) {
	// Check for the identity sentinel (all zeros).
	allZero := true
	for _, b := range data {
		if b != 0 {
			allZero = false
			break
		}
	}
	if allZero {
		return new(curvey.PointPallas).Identity(), nil
	}

	// Initialize a proper receiver: bare new(curvey.PointPallas) has a nil
	// inner EllipticPoint4 and will panic on FromAffineCompressed.
	receiver := new(curvey.PointPallas).Identity().(*curvey.PointPallas)
	return receiver.FromAffineCompressed(data)
}

// IdentityCiphertextBytes returns 64 bytes representing Enc(0) = (O, O),
// where O is the identity (zero) point on the Pallas curve.
// Used as the initial accumulator value.
func IdentityCiphertextBytes() []byte {
	identity := new(curvey.PointPallas).Identity()
	ct := &Ciphertext{C1: identity, C2: identity}
	// Identity points always serialize cleanly; panic on error is acceptable.
	bz, err := MarshalCiphertext(ct)
	if err != nil {
		panic("elgamal: IdentityCiphertextBytes: " + err.Error())
	}
	return bz
}
