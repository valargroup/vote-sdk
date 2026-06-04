package app

// RegisterUpgradeHandlers is the single place to register named x/upgrade
// handlers for future mainnet state-breaking releases. It is called before
// app.Load so future handlers can also install an UpgradeStoreLoader from the
// dumped upgrade-info file when stores are added, renamed, or deleted.
func (app *SvoteApp) RegisterUpgradeHandlers() {
	// Future mainnet upgrades should be registered here. Testnet repair
	// handlers should live in their own files with clear chain ID guards.
	app.registerV1Upgrade()
	app.registerStageVoteFundingMigrationUpgrade()
	app.registerIsolatedRehearsalUpgrade()
}
