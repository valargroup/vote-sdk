package app_test

import (
	"testing"

	"cosmossdk.io/core/comet"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/stretchr/testify/require"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/valargroup/vote-sdk/testutil"
)

func TestLivenessSignedBlocksDoNotUpdateSigningInfo(t *testing.T) {
	ta := testutil.SetupTestApp(t)
	consAddr := sdk.ConsAddress(ta.ProposerAddress)
	ctx := ta.NewUncachedContext(false, cmtproto.Header{Height: ta.Height, Time: ta.Time})

	before, err := ta.LivenessKeeper.GetValidatorSigningInfo(ctx, consAddr)
	require.NoError(t, err)

	for height := ta.Height + 1; height <= ta.Height+10; height++ {
		blockCtx := ctx.WithBlockHeight(height)
		err := ta.LivenessKeeper.HandleValidatorSignature(blockCtx, ta.ProposerAddress, 1, comet.BlockIDFlagCommit)
		require.NoError(t, err)
	}

	after, err := ta.LivenessKeeper.GetValidatorSigningInfo(ctx, consAddr)
	require.NoError(t, err)
	require.Equal(t, before.IndexOffset, after.IndexOffset)
	require.Equal(t, before.MissedBlocksCounter, after.MissedBlocksCounter)
	require.True(t, before.JailedUntil.Equal(after.JailedUntil))
}

func TestLivenessMissesJailValidator(t *testing.T) {
	ta := testutil.SetupTestApp(t)
	consAddr := sdk.ConsAddress(ta.ProposerAddress)
	ctx := ta.NewUncachedContext(false, cmtproto.Header{Height: ta.Height, Time: ta.Time})

	window, err := ta.LivenessKeeper.SignedBlocksWindow(ctx)
	require.NoError(t, err)
	minSigned, err := ta.LivenessKeeper.MinSignedPerWindow(ctx)
	require.NoError(t, err)
	maxMissed := window - minSigned

	for i := int64(1); i <= window+maxMissed+1; i++ {
		blockCtx := ctx.WithBlockHeight(i)
		err := ta.LivenessKeeper.HandleValidatorSignature(blockCtx, ta.ProposerAddress, 1, comet.BlockIDFlagAbsent)
		require.NoError(t, err)
	}

	jailed, err := ta.StakingKeeper.IsValidatorJailed(ctx, consAddr)
	require.NoError(t, err)
	require.True(t, jailed)

	info, err := ta.LivenessKeeper.GetValidatorSigningInfo(ctx, consAddr)
	require.NoError(t, err)
	require.Zero(t, info.MissedBlocksCounter)
	require.False(t, info.JailedUntil.IsZero())
}
