//go:build halo2

package halo2

import (
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestCircuitFingerprints checks capability field 5's fingerprints against
// the values voting-circuits' vk_fingerprint_unchanged tests pin.
func TestCircuitFingerprints(t *testing.T) {
	require.False(t, IsMock, "this test requires the real Halo2 verifier")
	fingerprints, err := CircuitFingerprints()
	require.NoError(t, err)

	got := make(map[string]string, len(fingerprints))
	var names []string
	for _, f := range fingerprints {
		names = append(names, f.Name)
		got[f.Name] = hex.EncodeToString(f.VkBlake2B)
	}
	require.Equal(t, []string{"zkp1", "zkp2", "zkp3", "zkp4"}, names)
	require.Equal(t, map[string]string{
		"zkp1": "e1773ecd2eb8531cc54c1df5b935a978788ef4eea4949cb45a00edf550ecee81",
		"zkp2": "95d9d97b0818ffe3f355bf0656aed154dbbffd9bfde4158766aac0bc4977595d",
		"zkp3": "e7165da431d5c6f8396d082aba6abfd0213c567071ca4435b38d092242cc4837",
		"zkp4": "4ec7e24b9bbf275807a613d3d3ea94cfae215083b7aa91374b79de52bf7d35aa",
	}, got)
}
