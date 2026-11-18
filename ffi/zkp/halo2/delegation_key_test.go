//go:build halo2

package halo2

import (
	"encoding/hex"
	"testing"
)

// Clients send this value for the key that made their delegation proof, so it
// must stay equal to the fingerprint the voting-circuits crate pins for the
// delegation verifying key.
func TestDelegationKeyFingerprintMatchesPinnedValue(t *testing.T) {
	const pinned = "e1773ecd2eb8531cc54c1df5b935a978788ef4eea4949cb45a00edf550ecee81"

	fingerprint, err := DelegationKeyFingerprint()
	if err != nil {
		t.Fatalf("DelegationKeyFingerprint() error = %v", err)
	}
	if got := hex.EncodeToString(fingerprint[:]); got != pinned {
		t.Fatalf("delegation key fingerprint = %s, want %s", got, pinned)
	}

	again, err := DelegationKeyFingerprint()
	if err != nil {
		t.Fatalf("second DelegationKeyFingerprint() error = %v", err)
	}
	if again != fingerprint {
		t.Fatalf("fingerprint changed between calls: %x then %x", fingerprint, again)
	}
}
