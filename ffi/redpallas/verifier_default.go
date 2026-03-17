//go:build !redpallas

package redpallas

// IsMock is true when the binary was built without the "redpallas" tag,
// meaning NewVerifier() returns a mock that accepts all signatures.
// Use this sentinel to detect and reject misconfigured production builds.
const IsMock = true

// NewVerifier returns a mock RedPallas verifier when built without the
// "redpallas" tag. This allows code that imports redpallas.NewVerifier()
// to compile without the Rust static library.
// Use `go build -tags redpallas` for real signature verification.
func NewVerifier() Verifier {
	return MockVerifier{}
}
