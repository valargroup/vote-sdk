package cmd

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/valargroup/vote-sdk/ffi/zkp/halo2"
)

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
