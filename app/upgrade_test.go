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

	svoteapp "github.com/valargroup/vote-sdk/app"
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

func TestVoteFundingMigrationUpgradeMovesStageVoteManagerBalances(t *testing.T) {
	ta := testutil.SetupTestAppWithChainID(t, "svote-1")
	manager1 := testutil.TestAccAddr(0x61)
	manager2 := testutil.TestAccAddr(0x62)
	ta.SeedVoteManagers(manager1, manager2)
	moveVoteFundingModuleBalanceToManagers(t, ta, map[string]int64{
		manager1: 600_000_000,
		manager2: 400_000_000,
	})

	ctx := ta.NewUncachedContext(false, cmtproto.Header{Height: ta.Height})
	moduleAddr := authtypes.NewModuleAddress(votetypes.VoteFundingModuleName)
	supplyBefore := ta.SvoteApp.BankKeeper.GetSupply(ctx, sdk.DefaultBondDenom)
	require.Equal(t, sdkmath.ZeroInt(), ta.SvoteApp.BankKeeper.GetBalance(ctx, moduleAddr, sdk.DefaultBondDenom).Amount)

	runVoteFundingMigrationUpgrade(t, ta)

	ctx = ta.NewUncachedContext(false, cmtproto.Header{Height: ta.Height})
	require.Equal(t, supplyBefore, ta.SvoteApp.BankKeeper.GetSupply(ctx, sdk.DefaultBondDenom))
	require.Equal(t, sdkmath.NewInt(1_000_000_000), ta.SvoteApp.BankKeeper.GetBalance(ctx, moduleAddr, sdk.DefaultBondDenom).Amount)
	require.Equal(t, sdkmath.ZeroInt(), balanceForAddress(t, ta, ctx, manager1).Amount)
	require.Equal(t, sdkmath.ZeroInt(), balanceForAddress(t, ta, ctx, manager2).Amount)
}

func TestVoteFundingMigrationUpgradeSkipsNonStageChain(t *testing.T) {
	ta := testutil.SetupTestApp(t)
	manager := testutil.TestAccAddr(0x61)
	ta.SeedVoteManagers(manager)
	moveVoteFundingModuleBalanceToManagers(t, ta, map[string]int64{
		manager: 500_000_000,
	})

	ctx := ta.NewUncachedContext(false, cmtproto.Header{Height: ta.Height})
	moduleAddr := authtypes.NewModuleAddress(votetypes.VoteFundingModuleName)
	supplyBefore := ta.SvoteApp.BankKeeper.GetSupply(ctx, sdk.DefaultBondDenom)

	runVoteFundingMigrationUpgrade(t, ta)

	ctx = ta.NewUncachedContext(false, cmtproto.Header{Height: ta.Height})
	require.Equal(t, supplyBefore, ta.SvoteApp.BankKeeper.GetSupply(ctx, sdk.DefaultBondDenom))
	require.Equal(t, sdkmath.NewInt(500_000_000), ta.SvoteApp.BankKeeper.GetBalance(ctx, moduleAddr, sdk.DefaultBondDenom).Amount)
	require.Equal(t, sdkmath.NewInt(500_000_000), balanceForAddress(t, ta, ctx, manager).Amount)
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

func TestV1UpgradeAppliesAcrossSupportedChains(t *testing.T) {
	testNoopUpgradeAppliesAcrossSupportedChains(t, svoteapp.V1UpgradeName)
}

func TestIronwoodUpgradeAppliesAcrossSupportedChains(t *testing.T) {
	require.NotEqual(t, svoteapp.V1UpgradeName, svoteapp.IronwoodUpgradeName)
	testNoopUpgradeAppliesAcrossSupportedChains(t, svoteapp.IronwoodUpgradeName)
}

func TestStagingIronwoodUpgradeRemainsRegistered(t *testing.T) {
	require.NotEqual(t, svoteapp.IronwoodUpgradeName, svoteapp.StagingIronwoodUpgradeName)
	testNoopUpgradeAppliesAcrossSupportedChains(t, svoteapp.StagingIronwoodUpgradeName)
}

func TestV120RC1UpgradeAppliesAcrossSupportedChains(t *testing.T) {
	require.NotEqual(t, svoteapp.StagingIronwoodUpgradeName, svoteapp.V120RC1UpgradeName)
	require.NotEqual(t, svoteapp.IronwoodUpgradeName, svoteapp.V120RC1UpgradeName)
	testNoopUpgradeAppliesAcrossSupportedChains(t, svoteapp.V120RC1UpgradeName)
}

func TestV120UpgradeAppliesAcrossSupportedChains(t *testing.T) {
	require.NotEqual(t, svoteapp.V120RC1UpgradeName, svoteapp.V120UpgradeName)
	require.NotEqual(t, svoteapp.IronwoodUpgradeName, svoteapp.V120UpgradeName)
	testNoopUpgradeAppliesAcrossSupportedChains(t, svoteapp.V120UpgradeName)
}

func TestV130RC1UpgradeAppliesAcrossSupportedChains(t *testing.T) {
	require.NotEqual(t, svoteapp.V120UpgradeName, svoteapp.V130RC1UpgradeName)
	testNoopUpgradeAppliesAcrossSupportedChains(t, svoteapp.V130RC1UpgradeName)
}

func TestV130UpgradeAppliesAcrossSupportedChains(t *testing.T) {
	require.NotEqual(t, svoteapp.V130RC1UpgradeName, svoteapp.V130UpgradeName)
	require.NotEqual(t, svoteapp.V120UpgradeName, svoteapp.V130UpgradeName)
	testNoopUpgradeAppliesAcrossSupportedChains(t, svoteapp.V130UpgradeName)
}

func TestV131UpgradeAppliesAcrossSupportedChains(t *testing.T) {
	require.NotEqual(t, svoteapp.V130UpgradeName, svoteapp.V131UpgradeName)
	testNoopUpgradeAppliesAcrossSupportedChains(t, svoteapp.V131UpgradeName)
}

func TestV150UpgradeAppliesAcrossSupportedChains(t *testing.T) {
	require.NotEqual(t, svoteapp.V140UpgradeName, svoteapp.V150UpgradeName)
	require.NotEqual(t, svoteapp.V131UpgradeName, svoteapp.V150UpgradeName)
	testNoopUpgradeAppliesAcrossSupportedChains(t, svoteapp.V150UpgradeName)
}

func TestV160UpgradeAppliesAcrossSupportedChains(t *testing.T) {
	require.NotEqual(t, svoteapp.V150UpgradeName, svoteapp.V160UpgradeName)
	testNoopUpgradeAppliesAcrossSupportedChains(t, svoteapp.V160UpgradeName)
}

func testNoopUpgradeAppliesAcrossSupportedChains(t *testing.T, upgradeName string) {
	t.Helper()
	testChains := []string{"svote-1", "zvote-1", "upgrade-test-1"}
	for _, chainID := range testChains {
		t.Run(chainID, func(t *testing.T) {
			ta := testutil.SetupTestAppWithChainID(t, chainID)
			voteManager := ta.ValidatorAccAddr()
			ta.SeedVoteManagers(voteManager)

			dueHeight := ta.Height + 2
			txBytes := ta.MustBuildSignedCoordinatorActionTx(voteManager, &votetypes.MsgScheduleUpgrade{
				Creator: voteManager,
				Name:    upgradeName,
				Height:  dueHeight,
				Info:    `{"purpose":"no-op-binary-cutover"}`,
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

			ctx := ta.NewUncachedContext(false, cmtproto.Header{Height: ta.Height})
			doneHeight, err := ta.SvoteApp.UpgradeKeeper.GetDoneHeight(ctx, upgradeName)
			require.NoError(t, err)
			require.Equal(t, dueHeight, doneHeight)
		})
	}
}

func TestIsolatedRehearsalUpgradeAppliesAtDueHeight(t *testing.T) {
	ta := testutil.SetupTestAppWithChainID(t, "upgrade-test-1")
	voteManager := ta.ValidatorAccAddr()
	ta.SeedVoteManagers(voteManager)

	dueHeight := ta.Height + 2
	txBytes := ta.MustBuildSignedCoordinatorActionTx(voteManager, &votetypes.MsgScheduleUpgrade{
		Creator: voteManager,
		Name:    svoteapp.IsolatedRehearsalUpgradeName,
		Height:  dueHeight,
		Info:    `{"purpose":"isolated-rehearsal"}`,
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

	ctx := ta.NewUncachedContext(false, cmtproto.Header{Height: ta.Height})
	doneHeight, err := ta.SvoteApp.UpgradeKeeper.GetDoneHeight(ctx, svoteapp.IsolatedRehearsalUpgradeName)
	require.NoError(t, err)
	require.Equal(t, dueHeight, doneHeight)
}

// moveVoteFundingModuleBalanceToManagers simulates the old genesis shape where
// vote-manager addresses held the funding pool directly.
func moveVoteFundingModuleBalanceToManagers(t *testing.T, ta *testutil.TestApp, balances map[string]int64) {
	t.Helper()
	ctx := ta.NewUncachedContext(false, cmtproto.Header{Height: ta.Height})
	for manager, amount := range balances {
		managerAddr, err := sdk.AccAddressFromBech32(manager)
		require.NoError(t, err)
		coins := sdk.NewCoins(sdk.NewInt64Coin(sdk.DefaultBondDenom, amount))
		require.NoError(t, ta.SvoteApp.BankKeeper.SendCoinsFromModuleToAccount(ctx, votetypes.VoteFundingModuleName, managerAddr, coins))
	}
	ta.NextBlock()
}

// runVoteFundingMigrationUpgrade schedules and executes the staging funding
// migration upgrade handler.
func runVoteFundingMigrationUpgrade(t *testing.T, ta *testutil.TestApp) {
	t.Helper()
	ctx := ta.NewUncachedContext(false, cmtproto.Header{Height: ta.Height})
	dueHeight := ta.Height + 1
	require.NoError(t, ta.SvoteApp.UpgradeKeeper.ScheduleUpgrade(ctx, types.Plan{
		Name:   svoteapp.VoteFundingMigrationUpgradeName,
		Height: dueHeight,
	}))

	ta.NextBlock()
	require.Equal(t, dueHeight, ta.Height)
}

// balanceForAddress returns the native denom balance for a bech32 account.
func balanceForAddress(t *testing.T, ta *testutil.TestApp, ctx context.Context, address string) sdk.Coin {
	t.Helper()
	addr, err := sdk.AccAddressFromBech32(address)
	require.NoError(t, err)
	return ta.SvoteApp.BankKeeper.GetBalance(ctx, addr, sdk.DefaultBondDenom)
}
