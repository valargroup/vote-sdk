package app

import (
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// thresholdForN
// ---------------------------------------------------------------------------

func TestThresholdForN(t *testing.T) {
	tests := []struct {
		n    int
		want int
	}{
		{n: 2, want: 2},
		{n: 3, want: 2},
		{n: 4, want: 2},
		{n: 6, want: 3},
		{n: 9, want: 5},
		{n: 10, want: 5},
		{n: 100, want: 50},
	}

	for _, tc := range tests {
		t.Run("", func(t *testing.T) {
			require.Equal(t, tc.want, thresholdForN(tc.n), "n=%d", tc.n)
		})
	}
}

func TestThresholdForN_PanicsBelow2(t *testing.T) {
	require.Panics(t, func() { thresholdForN(0) })
	require.Panics(t, func() { thresholdForN(1) })
}

func TestThresholdForN_Invariants(t *testing.T) {
	for n := 2; n <= 50; n++ {
		got := thresholdForN(n)
		require.GreaterOrEqual(t, got, 2, "n=%d: t must be >= 2", n)
		require.LessOrEqual(t, got, n, "n=%d: t must not exceed n", n)
	}
}

// ---------------------------------------------------------------------------
// path helpers
// ---------------------------------------------------------------------------

func TestSharePathForRound(t *testing.T) {
	roundID := []byte{0xca, 0xfe, 0xba, 0xbe}
	got := sharePathForRound("/tmp/keys", roundID)
	require.Equal(t, "/tmp/keys/share."+hex.EncodeToString(roundID), got)
}
