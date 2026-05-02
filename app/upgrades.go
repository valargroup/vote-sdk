package app

// RegisterUpgradeHandlers is the single place to register named x/upgrade
// handlers for future state-breaking releases. It is called before app.Load so
// future handlers can also install an UpgradeStoreLoader from the dumped
// upgrade-info file when stores are added, renamed, or deleted.
//
// The first release that wires x/upgrade has no completed on-chain upgrade
// plans yet, so this intentionally starts empty and must be rolled out by
// resetting the chain from genesis.
func (app *SvoteApp) RegisterUpgradeHandlers() {
}
