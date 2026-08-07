package app

import (
	"context"

	upgradetypes "cosmossdk.io/x/upgrade/types"
	"github.com/cosmos/cosmos-sdk/types/module"
)

// RegisterUpgradeHandlers is the single place to register named x/upgrade
// handlers for future mainnet state-breaking releases. It is called before
// app.Load so future handlers can also install an UpgradeStoreLoader from the
// dumped upgrade-info file when stores are added, renamed, or deleted.
func (app *SvoteApp) RegisterUpgradeHandlers() {
	// Future mainnet upgrades should be registered here. Testnet repair
	// handlers should live in their own files with clear chain ID guards.
	app.registerV1Upgrade()
	app.registerIronwoodUpgrade()
	app.registerV120Upgrade()
	app.registerStageVoteFundingMigrationUpgrade()
	app.registerIsolatedRehearsalUpgrade()
}

// registerNoopUpgrade registers a coordinated binary cutover with no store migration.
func (app *SvoteApp) registerNoopUpgrade(name string) {
	app.UpgradeKeeper.SetUpgradeHandler(
		name,
		func(_ context.Context, _ upgradetypes.Plan, vm module.VersionMap) (module.VersionMap, error) {
			return vm, nil
		},
	)
}
