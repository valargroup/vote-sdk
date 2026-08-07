//go:build !redpallas

package tx1

import "crypto/sha256"

const testDigestDomain = "SVOTE_TX1_EFFECTS_TEST_V1"

// ComputeDelegationSighash returns a deterministic stand-in digest for Go-only
// tests. Production binaries require the redpallas build tag and use the Rust
// implementation in sighash_ffi.go.
func ComputeDelegationSighash(effects []byte) ([]byte, error) {
	if err := ValidateEffectsFraming(effects); err != nil {
		return nil, err
	}
	h := sha256.New()
	_, _ = h.Write([]byte(testDigestDomain))
	_, _ = h.Write(effects)
	return h.Sum(nil), nil
}
