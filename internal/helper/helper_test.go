package helper

import (
	"testing"

	"cosmossdk.io/log"
	"github.com/stretchr/testify/require"
)

func TestNewRequiresValidationDependencies(t *testing.T) {
	cfg := DefaultConfig()
	h, err := New(cfg, nil, nil, nil, nil, nil, nil, nil, nil, nil, t.TempDir(), log.NewNopLogger())
	require.Nil(t, h)
	require.ErrorIs(t, err, ErrShareValidationUnavailable)
	require.ErrorContains(t, err, "commitment tree")
}

func TestNewDisabledDoesNotRequireValidationDependencies(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Disable = true
	h, err := New(cfg, nil, nil, nil, nil, nil, nil, nil, nil, nil, t.TempDir(), log.NewNopLogger())
	require.NoError(t, err)
	require.Nil(t, h)
}
