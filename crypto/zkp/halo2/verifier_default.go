//go:build !halo2

package halo2

import "github.com/z-cale/zally/crypto/zkp"

// IsMock is true when the binary was built without the "halo2" tag,
// meaning NewVerifier() returns a mock that accepts all ZK proofs.
// Use this sentinel to detect and reject misconfigured production builds.
const IsMock = true

// NewVerifier returns a mock ZKP verifier when built without the "halo2" tag.
// This allows code that imports halo2.NewVerifier() to compile without the
// Rust static library. Use `go build -tags halo2` for real verification.
func NewVerifier() zkp.Verifier {
	return zkp.NewMockVerifier()
}
