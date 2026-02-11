//go:build !halo2

package halo2

import "github.com/z-cale/zally/crypto/zkp"

// NewVerifier returns a mock ZKP verifier when built without the "halo2" tag.
// This allows code that imports halo2.NewVerifier() to compile without the
// Rust static library. Use `go build -tags halo2` for real verification.
func NewVerifier() zkp.Verifier {
	return zkp.NewMockVerifier()
}
