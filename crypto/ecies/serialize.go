package ecies

import (
	"fmt"

	"github.com/valargroup/vote-sdk/crypto/elgamal"
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
	if len(eBytes) != elgamal.CompressedPointSize {
		return nil, fmt.Errorf("ecies: MarshalEnvelope: ephemeral point compressed to %d bytes, expected %d", len(eBytes), elgamal.CompressedPointSize)
	}

	out := make([]byte, elgamal.CompressedPointSize+len(env.Ciphertext))
	copy(out[:elgamal.CompressedPointSize], eBytes)
	copy(out[elgamal.CompressedPointSize:], env.Ciphertext)
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
	expectedLen := elgamal.CompressedPointSize + ciphertextLen
	if len(data) != expectedLen {
		return nil, fmt.Errorf("ecies: UnmarshalEnvelope: expected %d bytes, got %d", expectedLen, len(data))
	}
	if ciphertextLen < 1 {
		return nil, fmt.Errorf("ecies: UnmarshalEnvelope: ciphertext length must be positive")
	}

	E, err := elgamal.DecompressPallasPoint(data[:elgamal.CompressedPointSize])
	if err != nil {
		return nil, fmt.Errorf("ecies: UnmarshalEnvelope: failed to decompress ephemeral point: %w", err)
	}
	if E.IsIdentity() {
		return nil, fmt.Errorf("ecies: UnmarshalEnvelope: ephemeral point must not be the identity point")
	}

	ct := make([]byte, ciphertextLen)
	copy(ct, data[elgamal.CompressedPointSize:])

	return &Envelope{
		Ephemeral:  E,
		Ciphertext: ct,
	}, nil
}
