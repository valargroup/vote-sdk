package halo2

import (
	"os"
	"testing"
)

func TestWarmVerifierCachesCompiles(t *testing.T) {
	if !IsMock && os.Getenv("SVOTE_RUN_HALO2_WARMUP_TEST") != "1" {
		t.Skip("set SVOTE_RUN_HALO2_WARMUP_TEST=1 to run the real Halo2 cache warm-up integration test")
	}

	if err := WarmVerifierCaches(); err != nil {
		t.Fatalf("WarmVerifierCaches() error = %v", err)
	}
}
