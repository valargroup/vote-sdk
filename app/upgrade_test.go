package app_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	sdkmath "cosmossdk.io/math"
	"cosmossdk.io/x/upgrade/types"
	abci "github.com/cometbft/cometbft/abci/types"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/cosmos/cosmos-sdk/client/flags"
	"github.com/cosmos/cosmos-sdk/testutil/sims"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/module"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"

	"github.com/valargroup/vote-sdk/testutil"
	votetypes "github.com/valargroup/vote-sdk/x/vote/types"
)

func TestSetupTestApp_WiresUpgradeKeeper(t *testing.T) {
	ta := testutil.SetupTestApp(t)
	require.NotNil(t, ta.SvoteApp.UpgradeKeeper)

	ctx := ta.NewUncachedContext(false, cmtproto.Header{Height: ta.Height})
	_, err := ta.SvoteApp.UpgradeKeeper.GetModuleVersionMap(ctx)
	require.NoError(t, err)

	res, err := ta.SvoteApp.UpgradeKeeper.Authority(ctx, &types.QueryAuthorityRequest{})
	require.NoError(t, err)
	require.Equal(t, authtypes.NewModuleAddress(votetypes.ModuleName).String(), res.Address)
}

func TestSetupTestApp_FundsVoteFundingModuleAccount(t *testing.T) {
	ta := testutil.SetupTestApp(t)
	ctx := ta.NewUncachedContext(false, cmtproto.Header{Height: ta.Height})

	moduleAddr := authtypes.NewModuleAddress(votetypes.VoteFundingModuleName)
	account := ta.SvoteApp.AccountKeeper.GetAccount(ctx, moduleAddr)
	require.NotNil(t, account)
	_, isModuleAccount := account.(sdk.ModuleAccountI)
	require.True(t, isModuleAccount)
	require.Equal(t, moduleAddr.String(), account.GetAddress().String())

	balance := ta.SvoteApp.BankKeeper.GetBalance(ctx, account.GetAddress(), sdk.DefaultBondDenom)
	require.Equal(t, sdkmath.NewInt(1_000_000_000), balance.Amount)
}

func TestVoteManagerScheduleUpgradeStoresPlan(t *testing.T) {
	ta := testutil.SetupTestApp(t)
	voteManager := ta.ValidatorAccAddr()
	ta.SeedVoteManagers(voteManager)

	planHeight := ta.Height + 10
	txBytes := ta.MustBuildSignedCoordinatorActionTx(voteManager, &votetypes.MsgScheduleUpgrade{
		Creator: voteManager,
		Name:    "stored-plan-test",
		Height:  planHeight,
		Info:    `{"tag":"v9.9.9"}`,
	})
	result := ta.DeliverVoteTx(txBytes)
	require.Equal(t, uint32(0), result.Code, result.Log)

	ctx := ta.NewUncachedContext(false, cmtproto.Header{Height: ta.Height})
	plan, err := ta.SvoteApp.UpgradeKeeper.GetUpgradePlan(ctx)
	require.NoError(t, err)
	require.Equal(t, "stored-plan-test", plan.Name)
	require.Equal(t, planHeight, plan.Height)
	require.Equal(t, `{"tag":"v9.9.9"}`, plan.Info)
}

func TestScheduledUpgradeWithoutHandlerHaltsAtDueHeight(t *testing.T) {
	ta := testutil.SetupTestAppWithAppOptions(t, sims.AppOptionsMap{
		flags.FlagHome: t.TempDir(),
	})
	voteManager := ta.ValidatorAccAddr()
	ta.SeedVoteManagers(voteManager)

	dueHeight := ta.Height + 2
	txBytes := ta.MustBuildSignedCoordinatorActionTx(voteManager, &votetypes.MsgScheduleUpgrade{
		Creator: voteManager,
		Name:    "halt-test",
		Height:  dueHeight,
		Info:    "install the next binary",
	})
	result := ta.DeliverVoteTx(txBytes)
	require.Equal(t, uint32(0), result.Code, result.Log)

	ta.Height++
	ta.Time = ta.Time.Add(5 * time.Second)
	_, err := ta.FinalizeBlock(&abci.RequestFinalizeBlock{
		Height:          ta.Height,
		Time:            ta.Time,
		ProposerAddress: ta.ProposerAddress,
	})
	require.ErrorContains(t, err, `UPGRADE "halt-test" NEEDED at height`)
}

func TestScheduledUpgradeWithHandlerAppliesAtDueHeight(t *testing.T) {
	ta := testutil.SetupTestAppWithAppOptions(t, sims.AppOptionsMap{
		flags.FlagHome: t.TempDir(),
	})
	voteManager := ta.ValidatorAccAddr()
	ta.SeedVoteManagers(voteManager)

	var applied bool
	ta.SvoteApp.UpgradeKeeper.SetUpgradeHandler("handled-test", func(ctx context.Context, plan types.Plan, vm module.VersionMap) (module.VersionMap, error) {
		applied = true
		return vm, nil
	})

	dueHeight := ta.Height + 2
	txBytes := ta.MustBuildSignedCoordinatorActionTx(voteManager, &votetypes.MsgScheduleUpgrade{
		Creator: voteManager,
		Name:    "handled-test",
		Height:  dueHeight,
	})
	result := ta.DeliverVoteTx(txBytes)
	require.Equal(t, uint32(0), result.Code, result.Log)

	ta.Height++
	ta.Time = ta.Time.Add(5 * time.Second)
	_, err := ta.FinalizeBlock(&abci.RequestFinalizeBlock{
		Height:          ta.Height,
		Time:            ta.Time,
		ProposerAddress: ta.ProposerAddress,
	})
	require.NoError(t, err)
	_, err = ta.Commit()
	require.NoError(t, err)

	require.True(t, applied)
	ctx := ta.NewUncachedContext(false, cmtproto.Header{Height: ta.Height})
	doneHeight, err := ta.SvoteApp.UpgradeKeeper.GetDoneHeight(ctx, "handled-test")
	require.NoError(t, err)
	require.Equal(t, dueHeight, doneHeight)
}
