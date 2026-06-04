package app

import (
	"context"

	upgradetypes "cosmossdk.io/x/upgrade/types"
	"github.com/cosmos/cosmos-sdk/types/module"
)

const (
	// V1UpgradeName is the canonical x/upgrade plan name for the first
	// mainnet major upgrade.
	V1UpgradeName = "v1"
)

// registerV1Upgrade registers a chain-agnostic no-op upgrade scaffold that can
// be safely scheduled on svote-1, zvote-1, and test chains.
func (app *SvoteApp) registerV1Upgrade() {
	app.UpgradeKeeper.SetUpgradeHandler(
		V1UpgradeName,
		func(_ context.Context, _ upgradetypes.Plan, vm module.VersionMap) (module.VersionMap, error) {
			// Intentional no-op: this establishes a shared handler name and validates
			// coordinated scheduling/apply flow without state changes.
			return vm, nil
		},
	)
}
