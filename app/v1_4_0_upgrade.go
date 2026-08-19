package app

import (
	"context"

	upgradetypes "cosmossdk.io/x/upgrade/types"
	"github.com/cosmos/cosmos-sdk/types/module"
)

const (
	// V140UpgradeName coordinates the five MiB consensus block limit with the
	// binary release that enforces bounded vote share submission proposals.
	V140UpgradeName = "v1.4.0"
)

// registerV140Upgrade applies the consensus byte limit when the coordinated
// binary upgrade executes.
func (app *SvoteApp) registerV140Upgrade() {
	app.UpgradeKeeper.SetUpgradeHandler(
		V140UpgradeName,
		func(ctx context.Context, _ upgradetypes.Plan, vm module.VersionMap) (module.VersionMap, error) {
			_, err := app.applyConsensusLimits(ctx)
			return vm, err
		},
	)
}
