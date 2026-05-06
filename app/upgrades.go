package app

import (
	"context"

	upgradetypes "cosmossdk.io/x/upgrade/types"
	"github.com/cosmos/cosmos-sdk/types/module"
)

const preprodUpgradeTestPlanName = "preprod-upgrade-test-v0.5.70"

// RegisterUpgradeHandlers is the single place to register named x/upgrade
// handlers for future state-breaking releases. It is called before app.Load so
// future handlers can also install an UpgradeStoreLoader from the dumped
// upgrade-info file when stores are added, renamed, or deleted.
func (app *SvoteApp) RegisterUpgradeHandlers() {
	// Temporary no-op handler for the pre-prod halt/resume upgrade test.
	// Remove this after the test chain has advanced past the scheduled height.
	app.UpgradeKeeper.SetUpgradeHandler(preprodUpgradeTestPlanName, func(_ context.Context, _ upgradetypes.Plan, vm module.VersionMap) (module.VersionMap, error) {
		return vm, nil
	})
}
