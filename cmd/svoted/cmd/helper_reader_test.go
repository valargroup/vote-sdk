package cmd

import (
	"encoding/hex"
	"testing"

	"cosmossdk.io/log"
	"github.com/stretchr/testify/require"

	"github.com/valargroup/vote-sdk/internal/helper"
	"github.com/valargroup/vote-sdk/testutil"
)

func TestKeeperTreeReaderWaitsForPostRestartBlockTime(t *testing.T) {
	app := testutil.SetupTestApp(t)
	roundID := app.SeedVotingSession(testutil.ValidCreateVotingSessionAt(app.Time))
	roundIDHex := hex.EncodeToString(roundID)
	reader := &keeperTreeReader{app: app.SvoteApp, logger: log.NewNopLogger()}

	active, err := reader.GetRoundIsActive(roundIDHex)
	require.NoError(t, err)
	require.True(t, active)

	app.RestartBeforeNextBlock()
	reader.app = app.SvoteApp
	active, err = reader.GetRoundIsActive(roundIDHex)
	require.ErrorIs(t, err, helper.ErrCheckTxNotReady)
	require.False(t, active)

	app.NextBlock()
	active, err = reader.GetRoundIsActive(roundIDHex)
	require.NoError(t, err)
	require.True(t, active)
}
