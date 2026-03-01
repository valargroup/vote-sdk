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
		// n < 2: legacy mode
		{n: 0, want: 0},
		{n: 1, want: 0},
		// n=2: t = ceil(2/3)+1 = 1+1 = 2; clamp(2, n=2) = 2
		{n: 2, want: 2},
		// n=3: t = ceil(3/3)+1 = 1+1 = 2
		{n: 3, want: 2},
		// n=4: t = ceil(4/3)+1 = 2+1 = 3
		{n: 4, want: 3},
		// n=6: t = ceil(6/3)+1 = 2+1 = 3
		{n: 6, want: 3},
		// n=9: t = ceil(9/3)+1 = 3+1 = 4
		{n: 9, want: 4},
		// n=10: t = ceil(10/3)+1 = 4+1 = 5
		{n: 10, want: 5},
		// n=100: t = ceil(100/3)+1 = 34+1 = 35
		{n: 100, want: 35},
	}

	for _, tc := range tests {
		t.Run("", func(t *testing.T) {
			require.Equal(t, tc.want, thresholdForN(tc.n), "n=%d", tc.n)
		})
	}
}

// t must always be at least 2 when positive, and never exceed n.
func TestThresholdForN_Invariants(t *testing.T) {
	for n := 0; n <= 50; n++ {
		got := thresholdForN(n)
		if n < 2 {
			require.Equal(t, 0, got, "n=%d: expected legacy (0)", n)
		} else {
			require.GreaterOrEqual(t, got, 2, "n=%d: t must be >= 2", n)
			require.LessOrEqual(t, got, n, "n=%d: t must not exceed n", n)
		}
	}
}

// ---------------------------------------------------------------------------
// path helpers
// ---------------------------------------------------------------------------

func TestEaSkPathForRound(t *testing.T) {
	roundID := []byte{0xde, 0xad, 0xbe, 0xef}
	got := eaSkPathForRound("/tmp/keys", roundID)
	require.Equal(t, "/tmp/keys/ea_sk."+hex.EncodeToString(roundID), got)
}

func TestSharePathForRound(t *testing.T) {
	roundID := []byte{0xca, 0xfe, 0xba, 0xbe}
	got := sharePathForRound("/tmp/keys", roundID)
	require.Equal(t, "/tmp/keys/share."+hex.EncodeToString(roundID), got)
}

// share path and ea_sk path for the same round must differ so they don't
// clobber each other when both modes run against the same directory.
func TestShareAndEaSkPathsAreDistinct(t *testing.T) {
	roundID := []byte{0x01, 0x02, 0x03, 0x04}
	require.NotEqual(t, eaSkPathForRound("/d", roundID), sharePathForRound("/d", roundID))
}
