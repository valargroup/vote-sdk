package app

import (
	"context"

	upgradetypes "cosmossdk.io/x/upgrade/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/module"
)

const (
	// IsolatedRehearsalUpgradeName is the x/upgrade plan name reserved for
	// isolated-network upgrade rehearsals.
	IsolatedRehearsalUpgradeName = "isolated-rehearsal-v1"

	// isolatedRehearsalChainID is the separate-network chain ID used during the
	// operator validation program.
	isolatedRehearsalChainID = "upgrade-test-1"
)

// registerIsolatedRehearsalUpgrade registers a rehearsal-only handler so
// validators on the isolated network can fully exercise the x/upgrade flow.
func (app *SvoteApp) registerIsolatedRehearsalUpgrade() {
	app.UpgradeKeeper.SetUpgradeHandler(
		IsolatedRehearsalUpgradeName,
		func(ctx context.Context, _ upgradetypes.Plan, vm module.VersionMap) (module.VersionMap, error) {
			// Isolated rehearsal guardrail: never run this handler logic outside the
			// dedicated separate-network chain.
			if sdk.UnwrapSDKContext(ctx).ChainID() != isolatedRehearsalChainID {
				return vm, nil
			}

			// Intentional no-op: this handler validates upgrade plumbing (schedule,
			// halt, switch, resume, applied-plan bookkeeping) without state changes.
			return vm, nil
		},
	)
}
