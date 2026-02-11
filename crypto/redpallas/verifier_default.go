//go:build !redpallas

package redpallas

// NewVerifier returns a mock RedPallas verifier when built without the
// "redpallas" tag. This allows code that imports redpallas.NewVerifier()
// to compile without the Rust static library.
// Use `go build -tags redpallas` for real signature verification.
func NewVerifier() Verifier {
	return MockVerifier{}
}
