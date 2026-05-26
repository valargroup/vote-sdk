package app

import (
	"context"

	"cosmossdk.io/x/upgrade/types"
	"github.com/cosmos/cosmos-sdk/types/module"
)

const (
	// MinCeremonyValidatorsUpgradeName is the x/upgrade plan name that raises
	// the minimum eligible ceremony validator count for new voting rounds.
	MinCeremonyValidatorsUpgradeName = "raise-min-ceremony-validators"

	minCeremonyValidatorsAfterUpgrade uint32 = 6
)

func (app *SvoteApp) registerMinCeremonyValidatorsUpgrade() {
	app.UpgradeKeeper.SetUpgradeHandler(
		MinCeremonyValidatorsUpgradeName,
		func(ctx context.Context, _ types.Plan, vm module.VersionMap) (module.VersionMap, error) {
			kvStore := app.VoteKeeper.OpenKVStore(ctx)
			if err := app.VoteKeeper.SetMinCeremonyValidators(kvStore, minCeremonyValidatorsAfterUpgrade); err != nil {
				return nil, err
			}
			return vm, nil
		},
	)
}
