package halo2

import (
	"os"
	"testing"
)

// skipUnlessWarmupTest skips a real warm-up, which pays every circuit's
// keygen, unless SVOTE_RUN_HALO2_WARMUP_TEST is 1.
func skipUnlessWarmupTest(t *testing.T) {
	t.Helper()
	if !IsMock && os.Getenv("SVOTE_RUN_HALO2_WARMUP_TEST") != "1" {
		t.Skip("set SVOTE_RUN_HALO2_WARMUP_TEST=1 to run the real Halo2 cache warm-up integration test")
	}
}

func TestWarmVerifierCachesCompiles(t *testing.T) {
	skipUnlessWarmupTest(t)

	if err := WarmVerifierCaches(); err != nil {
		t.Fatalf("WarmVerifierCaches() error = %v", err)
	}
}

// TestWarmShareRevealProver checks the helper's ZKP #3 prover warm-up.
func TestWarmShareRevealProver(t *testing.T) {
	skipUnlessWarmupTest(t)

	if err := WarmShareRevealProver(); err != nil {
		t.Fatalf("WarmShareRevealProver() error = %v", err)
	}
}
