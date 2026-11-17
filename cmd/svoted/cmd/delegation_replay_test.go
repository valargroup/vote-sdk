package cmd

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
	"github.com/valargroup/vote-sdk/ffi/zkp/halo2"
)

func TestDelegationReplayConfigControlsNodeHomeCapture(t *testing.T) {
	nodeHome := filepath.Join(t.TempDir(), "custom-node-home")
	for _, tc := range []struct {
		name   string
		config string
		want   string
	}{
		{"existing config without setting", "[vote]\n", ""},
		{"explicitly disabled", "[vote]\ndelegation_replay_capture = false\n", ""},
		{"enabled", "[vote]\ndelegation_replay_capture = true\n", filepath.Join(nodeHome, "debug")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config := viper.New()
			config.SetConfigType("toml")
			require.NoError(t, config.ReadConfig(strings.NewReader(tc.config)))
			require.Equal(t, tc.want, delegationReplayDirectory(config, nodeHome))
		})
	}
}

func TestReplayDelegationRequiresRealVerifier(t *testing.T) {
	if !halo2.IsMock {
		t.Skip("mock-build refusal is tested without the halo2 tag")
	}
	cmd := replayDelegationCmd()
	cmd.SetArgs([]string{"does-not-need-to-exist.json"})
	require.ErrorContains(t, cmd.Execute(), "mock verification is not supported")
}

func TestDebugRegistersOfflineReplay(t *testing.T) {
	cmd, _, err := delegationDebugCmd().Find([]string{"replay-delegation"})
	require.NoError(t, err)
	require.Equal(t, "replay-delegation", cmd.Name())
	require.NotNil(t, cmd.PersistentPreRunE)
	require.NotNil(t, cmd.Flags().Lookup("compare"))
}
