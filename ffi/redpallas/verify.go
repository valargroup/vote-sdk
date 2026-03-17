// Package redpallas provides an interface for RedPallas signature verification.
//
// RedPallas is the signature scheme used in Zcash Orchard for spend authorization.
// When built with the "redpallas" build tag, NewVerifier() returns a real verifier
// backed by the Rust reddsa crate via CGo. Without the tag, it returns a MockVerifier.
package redpallas

// Verifier defines the interface for RedPallas signature verification.
//
// Parameters:
//   - rk:      Randomized spend authorization key (32 bytes, Pallas point)
//   - sighash: The hash of the data that was signed (computed from raw tx bytes)
//   - sig:     The RedPallas signature bytes
type Verifier interface {
	Verify(rk, sighash, sig []byte) error
}

// MockVerifier is a mock implementation that always returns nil (success).
// Used during development and testing when the Rust library is not available.
type MockVerifier struct{}

// Verify always returns nil. The real implementation will verify the RedPallas
// signature over sighash using the randomized key rk.
func (MockVerifier) Verify(rk, sighash, sig []byte) error {
	return nil
}

// NewMockVerifier returns a new mock RedPallas verifier.
// Prefer NewVerifier() which returns the real verifier when the "redpallas"
// build tag is set, and MockVerifier otherwise.
func NewMockVerifier() Verifier {
	return MockVerifier{}
}
