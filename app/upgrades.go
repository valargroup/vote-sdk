package app

import (
	"context"
	"fmt"

	"cosmossdk.io/x/upgrade/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/module"

	votetypes "github.com/valargroup/vote-sdk/x/vote/types"
)

const (
	// VoteFundingMigrationUpgradeName is the x/upgrade plan name that migrates
	// existing staging vote-manager balances into the vote_funding module account.
	VoteFundingMigrationUpgradeName = "stage-vote-funding-module"

	stageVoteFundingMigrationChainID = "svote-1"
	voteFundingMigrationTargetAmount = int64(1_000_000_000)
)

// RegisterUpgradeHandlers is the single place to register named x/upgrade
// handlers for future state-breaking releases. It is called before app.Load so
// future handlers can also install an UpgradeStoreLoader from the dumped
// upgrade-info file when stores are added, renamed, or deleted.
func (app *SvoteApp) RegisterUpgradeHandlers() {
	app.UpgradeKeeper.SetUpgradeHandler(
		VoteFundingMigrationUpgradeName,
		func(ctx context.Context, _ types.Plan, vm module.VersionMap) (module.VersionMap, error) {
			if err := app.migrateStageVoteFunding(ctx); err != nil {
				return nil, err
			}
			return vm, nil
		},
	)
}

// migrateStageVoteFunding moves existing staging vote-manager funds into the
// vote_funding module account without minting new supply.
func (app *SvoteApp) migrateStageVoteFunding(ctx context.Context) error {
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	if sdkCtx.ChainID() != stageVoteFundingMigrationChainID {
		return nil
	}

	moduleAccount := app.AccountKeeper.GetModuleAccount(ctx, votetypes.VoteFundingModuleName)
	if moduleAccount == nil {
		return fmt.Errorf("module account %s is not registered", votetypes.VoteFundingModuleName)
	}

	target := sdk.NewInt64Coin(sdk.DefaultBondDenom, voteFundingMigrationTargetAmount)
	current := app.BankKeeper.GetBalance(ctx, moduleAccount.GetAddress(), sdk.DefaultBondDenom)
	remaining := target.Amount.Sub(current.Amount)
	if !remaining.IsPositive() {
		return nil
	}

	kvStore := app.VoteKeeper.OpenKVStore(ctx)
	voteManagers, err := app.VoteKeeper.GetVoteManagers(kvStore)
	if err != nil {
		return err
	}
	if voteManagers == nil || len(voteManagers.Addresses) == 0 {
		return nil
	}

	for _, manager := range voteManagers.Addresses {
		if !remaining.IsPositive() {
			return nil
		}

		managerAddr, err := sdk.AccAddressFromBech32(manager)
		if err != nil {
			return fmt.Errorf("invalid vote manager address %q: %w", manager, err)
		}
		managerBalance := app.BankKeeper.GetBalance(ctx, managerAddr, sdk.DefaultBondDenom)
		if !managerBalance.Amount.IsPositive() {
			continue
		}

		transferAmount := managerBalance.Amount
		if transferAmount.GT(remaining) {
			transferAmount = remaining
		}
		coins := sdk.NewCoins(sdk.NewCoin(sdk.DefaultBondDenom, transferAmount))
		if err := app.BankKeeper.SendCoinsFromAccountToModule(ctx, managerAddr, votetypes.VoteFundingModuleName, coins); err != nil {
			return fmt.Errorf("move vote-manager balance from %s to %s: %w", manager, votetypes.VoteFundingModuleName, err)
		}
		remaining = remaining.Sub(transferAmount)
	}

	return nil
}
