package app

// RegisterUpgradeHandlers is the single place to register named x/upgrade
// handlers for future state-breaking releases. It is called before app.Load so
// future handlers can also install an UpgradeStoreLoader from the dumped
// upgrade-info file when stores are added, renamed, or deleted.
//
// No upgrade handlers are currently pending. Future state-breaking releases must
// register their scheduled plan name here before the old binary schedules it.
func (app *SvoteApp) RegisterUpgradeHandlers() {
}
