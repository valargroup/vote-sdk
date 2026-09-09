package cmd

import (
	"encoding/hex"
	"testing"

	"cosmossdk.io/log"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/stretchr/testify/require"

	"github.com/valargroup/vote-sdk/internal/helper"
	"github.com/valargroup/vote-sdk/testutil"
	votetypes "github.com/valargroup/vote-sdk/x/vote/types"
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
	closed, closureErr := reader.GetRoundIsClosed(roundIDHex)
	require.ErrorIs(t, closureErr, helper.ErrCheckTxNotReady)
	require.False(t, closed)

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

func TestKeeperTreeReaderRequiresClosedStatus(t *testing.T) {
	app := testutil.SetupTestApp(t)
	roundID := app.SeedVotingSession(testutil.ValidCreateVotingSessionAt(app.Time))
	reader := &keeperTreeReader{app: app.SvoteApp, logger: log.NewNopLogger()}
	ctx := app.NewUncachedContext(false, cmtproto.Header{})
	kv := app.VoteKeeper().OpenKVStore(ctx)
	for _, tc := range []struct {
		status votetypes.SessionStatus
		closed bool
	}{
		{votetypes.SessionStatus_SESSION_STATUS_UNSPECIFIED, false},
		{votetypes.SessionStatus_SESSION_STATUS_PENDING, false},
		{votetypes.SessionStatus_SESSION_STATUS_ACTIVE, false},
		{votetypes.SessionStatus_SESSION_STATUS_TALLYING, true},
		{votetypes.SessionStatus_SESSION_STATUS_FINALIZED, true},
		{votetypes.SessionStatus_SESSION_STATUS_CEREMONY_FAILED, true},
	} {
		t.Run(tc.status.String(), func(t *testing.T) {
			require.NoError(t, app.VoteKeeper().UpdateVoteRoundStatus(kv, roundID, tc.status))
			closed, err := reader.GetRoundIsClosed(hex.EncodeToString(roundID))
			require.NoError(t, err)
			require.Equal(t, tc.closed, closed)
		})
	}
	closed, err := reader.GetRoundIsClosed(hex.EncodeToString(make([]byte, 32)))
	require.ErrorIs(t, err, helper.ErrUnknownRound)
	require.False(t, closed)
}
