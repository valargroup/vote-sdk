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

func TestKeeperTreeReaderValidatesShareChoice(t *testing.T) {
	app := testutil.SetupTestApp(t)
	roundID := app.SeedVotingSession(testutil.ValidCreateVotingSessionAt(app.Time))
	reader := &keeperTreeReader{app: app.SvoteApp, logger: log.NewNopLogger()}
	roundIDHex := hex.EncodeToString(roundID)

	require.NoError(t, reader.ValidateShareChoice(roundIDHex, 1, 0))

	err := reader.ValidateShareChoice(roundIDHex, 1, 2)
	require.ErrorIs(t, err, helper.ErrInvalidRoundChoice)

	err = reader.ValidateShareChoice(roundIDHex, 3, 0)
	require.ErrorIs(t, err, helper.ErrInvalidRoundChoice)

	err = reader.ValidateShareChoice(hex.EncodeToString(make([]byte, 32)), 1, 0)
	require.ErrorIs(t, err, helper.ErrUnknownRound)
}
