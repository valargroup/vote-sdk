package ecies

import (
	"fmt"

	"github.com/mikelodder7/curvey"
)

// MarshalEnvelope serializes an Envelope to bytes:
//
//	E_compressed (32 bytes) || ciphertext (variable)
func MarshalEnvelope(env *Envelope) ([]byte, error) {
	if env == nil {
		return nil, fmt.Errorf("ecies: MarshalEnvelope: envelope must not be nil")
	}
	if env.Ephemeral == nil {
		return nil, fmt.Errorf("ecies: MarshalEnvelope: ephemeral point must not be nil")
	}
	if len(env.Ciphertext) == 0 {
		return nil, fmt.Errorf("ecies: MarshalEnvelope: ciphertext must not be empty")
	}

	eBytes := env.Ephemeral.ToAffineCompressed()
	if len(eBytes) != CompressedPointSize {
		return nil, fmt.Errorf("ecies: MarshalEnvelope: ephemeral point compressed to %d bytes, expected %d", len(eBytes), CompressedPointSize)
	}

	out := make([]byte, CompressedPointSize+len(env.Ciphertext))
	copy(out[:CompressedPointSize], eBytes)
	copy(out[CompressedPointSize:], env.Ciphertext)
	return out, nil
}

// UnmarshalEnvelope deserializes bytes into an Envelope. The caller must
// provide the expected ciphertext length (plaintext length + 16 bytes for
// the Poly1305 tag). This is necessary because the wire format is a simple
// concatenation with no length prefix.
//
// For the key setup ceremony where plaintext is a 32-byte Pallas scalar,
// ciphertextLen = 48 (32 + 16).
func UnmarshalEnvelope(data []byte, ciphertextLen int) (*Envelope, error) {
	expectedLen := CompressedPointSize + ciphertextLen
	if len(data) != expectedLen {
		return nil, fmt.Errorf("ecies: UnmarshalEnvelope: expected %d bytes, got %d", expectedLen, len(data))
	}
	if ciphertextLen < 1 {
		return nil, fmt.Errorf("ecies: UnmarshalEnvelope: ciphertext length must be positive")
	}

	E, err := decompressPallasPoint(data[:CompressedPointSize])
	if err != nil {
		return nil, fmt.Errorf("ecies: UnmarshalEnvelope: failed to decompress ephemeral point: %w", err)
	}
	if E.IsIdentity() {
		return nil, fmt.Errorf("ecies: UnmarshalEnvelope: ephemeral point must not be the identity point")
	}

	ct := make([]byte, ciphertextLen)
	copy(ct, data[CompressedPointSize:])

	return &Envelope{
		Ephemeral:  E,
		Ciphertext: ct,
	}, nil
}

// decompressPallasPoint decompresses a 32-byte Pallas point. Rejects the
// identity (all-zeros) since ephemeral keys must never be identity.
func decompressPallasPoint(data []byte) (curvey.Point, error) {
	if len(data) != CompressedPointSize {
		return nil, fmt.Errorf("expected %d bytes, got %d", CompressedPointSize, len(data))
	}

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
	receiver := new(curvey.PointPallas).Identity()
	return receiver.FromAffineCompressed(data)
}
