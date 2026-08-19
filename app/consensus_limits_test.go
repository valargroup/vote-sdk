package app_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	upgradetypes "cosmossdk.io/x/upgrade/types"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	cmttypes "github.com/cometbft/cometbft/types"
	simtestutil "github.com/cosmos/cosmos-sdk/testutil/sims"

	svoteapp "github.com/valargroup/vote-sdk/app"
	"github.com/valargroup/vote-sdk/testutil"
)

func TestInitChainPreservesGenesisBlockLimit(t *testing.T) {
	ta := testutil.SetupTestApp(t)
	ctx := ta.NewUncachedContext(false, cmtproto.Header{Height: ta.Height})

	params, err := ta.SvoteApp.ConsensusParamsKeeper.ParamsStore.Get(ctx)
	require.NoError(t, err)
	require.Equal(t, simtestutil.DefaultConsensusParams.Block.MaxBytes, params.Block.MaxBytes)
	require.NotEqual(t, svoteapp.MaxBlockBytes, params.Block.MaxBytes)
}

func TestV140UpgradeReducesExistingChainBlockLimit(t *testing.T) {
	for _, chainID := range []string{"svote-1", "zvote-1", "upgrade-test-1"} {
		t.Run(chainID, func(t *testing.T) {
			ta := testutil.SetupTestAppWithChainID(t, chainID)
			ctx := ta.NewUncachedContext(false, cmtproto.Header{Height: ta.Height})

			params, err := ta.SvoteApp.ConsensusParamsKeeper.ParamsStore.Get(ctx)
			require.NoError(t, err)
			params.Block.MaxBytes = cmttypes.DefaultConsensusParams().Block.MaxBytes
			require.NoError(t, ta.SvoteApp.ConsensusParamsKeeper.ParamsStore.Set(ctx, params))

			dueHeight := ta.Height + 1
			require.NoError(t, ta.SvoteApp.UpgradeKeeper.ScheduleUpgrade(ctx, upgradetypes.Plan{
				Name:   svoteapp.V140UpgradeName,
				Height: dueHeight,
			}))

			finalizeResp := ta.NextBlockResponse()
			require.Equal(t, dueHeight, ta.Height)
			require.NotNil(t, finalizeResp.ConsensusParamUpdates)
			require.Equal(t, svoteapp.MaxBlockBytes, finalizeResp.ConsensusParamUpdates.Block.MaxBytes)

			ctx = ta.NewUncachedContext(false, cmtproto.Header{Height: ta.Height})
			params, err = ta.SvoteApp.ConsensusParamsKeeper.ParamsStore.Get(ctx)
			require.NoError(t, err)
			require.Equal(t, svoteapp.MaxBlockBytes, params.Block.MaxBytes)

			doneHeight, err := ta.SvoteApp.UpgradeKeeper.GetDoneHeight(ctx, svoteapp.V140UpgradeName)
			require.NoError(t, err)
			require.Equal(t, dueHeight, doneHeight)
		})
	}
}
