package helper

import (
	"bytes"
	"fmt"
	"testing"

	"cosmossdk.io/log"
	"github.com/stretchr/testify/require"
)

func TestDefaultConfigUsesTwoProofWorkers(t *testing.T) {
	require.Equal(t, 2, DefaultConfig().MaxConcurrentProofs)
}

func TestNewRequiresValidationDependencies(t *testing.T) {
	cfg := DefaultConfig()
	h, err := New(cfg, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, t.TempDir(), log.NewNopLogger())
	require.Nil(t, h)
	require.ErrorIs(t, err, ErrShareValidationUnavailable)
	require.ErrorContains(t, err, "commitment tree")
}

func TestNewDisabledDoesNotRequireValidationDependencies(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Disable = true
	h, err := New(cfg, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, t.TempDir(), log.NewNopLogger())
	require.NoError(t, err)
	require.Nil(t, h)
}

func TestNewLogsEffectiveProofConcurrency(t *testing.T) {
	for _, tt := range []struct {
		configured int
		effective  int
	}{
		{configured: 3, effective: 3},
		{configured: 0, effective: 2},
	} {
		t.Run(fmt.Sprintf("configured_%d", tt.configured), func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.DBPath = ":memory:"
			cfg.MaxConcurrentProofs = tt.configured

			var logs bytes.Buffer
			logger := log.NewLogger(&logs, log.ColorOption(false))
			h, err := New(
				cfg,
				newMockTreeReader(),
				&mockProver{},
				func(string) (RoundInfo, error) { return RoundInfo{}, nil },
				func(string) (bool, error) { return true, nil },
				func(string) (bool, error) { return false, nil },
				func() bool { return true },
				func([32]byte, [32]byte, uint32, uint32) ([32]byte, error) {
					return [32]byte{}, nil
				},
				func([32]byte, [16][32]byte, [32]byte, [32]byte, [32]byte, uint32) (bool, error) {
					return true, nil
				},
				func(string, uint32, uint32) error { return nil },
				nil,
				nil,
				t.TempDir(),
				logger,
			)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, h.Close()) })
			require.Contains(t, logs.String(), "helper constructed")
			require.Contains(t, logs.String(), fmt.Sprintf("proof_concurrency=%d", tt.effective))
		})
	}
}
