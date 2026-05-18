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
	// VoteFundingMigrationUpgradeName is the x/upgrade plan name for the
	// staging vote_funding migration.
	VoteFundingMigrationUpgradeName = "stage-vote-funding-module"

	stageVoteFundingMigrationChainID       = "svote-1"
	stageVoteFundingModuleBalanceCapAmount = int64(1_000_000_000)
)

// registerStageVoteFundingMigrationUpgrade registers an upgrade that only runs
// on the staging testnet. Do not use this file as the template for future
// mainnet upgrades.
func (app *SvoteApp) registerStageVoteFundingMigrationUpgrade() {
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
	// This is a staging chain repair only. It must never change production or
	// local test chains that happen to schedule the same handler name.
	if sdkCtx.ChainID() != stageVoteFundingMigrationChainID {
		return nil
	}

	// GetModuleAccount creates the registered module account when old staging
	// state predates the vote_funding genesis entry.
	moduleAccount := app.AccountKeeper.GetModuleAccount(ctx, votetypes.VoteFundingModuleName)
	if moduleAccount == nil {
		return fmt.Errorf("module account %s is not registered", votetypes.VoteFundingModuleName)
	}

	// Treat the fresh genesis funding amount as a cap, not a required final
	// balance. This keeps the migration idempotent and prevents overfilling the
	// module account if it already holds funds.
	fundingCap := sdk.NewInt64Coin(sdk.DefaultBondDenom, stageVoteFundingModuleBalanceCapAmount)
	current := app.BankKeeper.GetBalance(ctx, moduleAccount.GetAddress(), sdk.DefaultBondDenom)
	remainingCapacity := fundingCap.Amount.Sub(current.Amount)
	if !remainingCapacity.IsPositive() {
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
		if !remainingCapacity.IsPositive() {
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

		// Move only as much as the module account can still accept under the
		// cap. Any excess vote-manager balance is intentionally left in place.
		transferAmount := managerBalance.Amount
		if transferAmount.GT(remainingCapacity) {
			transferAmount = remainingCapacity
		}
		coins := sdk.NewCoins(sdk.NewCoin(sdk.DefaultBondDenom, transferAmount))
		if err := app.BankKeeper.SendCoinsFromAccountToModule(ctx, managerAddr, votetypes.VoteFundingModuleName, coins); err != nil {
			return fmt.Errorf("move vote-manager balance from %s to %s: %w", manager, votetypes.VoteFundingModuleName, err)
		}
		remainingCapacity = remainingCapacity.Sub(transferAmount)
	}

	return nil
}
