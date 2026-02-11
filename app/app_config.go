package app

import (
	runtimev1alpha1 "cosmossdk.io/api/cosmos/app/runtime/v1alpha1"
	appv1alpha1 "cosmossdk.io/api/cosmos/app/v1alpha1"
	authmodulev1 "cosmossdk.io/api/cosmos/auth/module/v1"
	bankmodulev1 "cosmossdk.io/api/cosmos/bank/module/v1"
	consensusmodulev1 "cosmossdk.io/api/cosmos/consensus/module/v1"
	distrmodulev1 "cosmossdk.io/api/cosmos/distribution/module/v1"
	genutilmodulev1 "cosmossdk.io/api/cosmos/genutil/module/v1"
	stakingmodulev1 "cosmossdk.io/api/cosmos/staking/module/v1"
	txconfigv1 "cosmossdk.io/api/cosmos/tx/config/v1"
	"cosmossdk.io/core/appconfig"
	"cosmossdk.io/depinject"

	"github.com/cosmos/cosmos-sdk/runtime"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/module"
	_ "github.com/cosmos/cosmos-sdk/x/auth"           // import for side-effects (depinject registration)
	_ "github.com/cosmos/cosmos-sdk/x/auth/tx/config" // import for side-effects
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	_ "github.com/cosmos/cosmos-sdk/x/bank"         // import for side-effects
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	_ "github.com/cosmos/cosmos-sdk/x/consensus" // import for side-effects
	consensustypes "github.com/cosmos/cosmos-sdk/x/consensus/types"
	_ "github.com/cosmos/cosmos-sdk/x/distribution" // import for side-effects
	distrtypes "github.com/cosmos/cosmos-sdk/x/distribution/types"
	"github.com/cosmos/cosmos-sdk/x/genutil"
	genutiltypes "github.com/cosmos/cosmos-sdk/x/genutil/types"
	_ "github.com/cosmos/cosmos-sdk/x/staking" // import for side-effects
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"

	// Vote module: import for depinject side-effects (registers module provider).
	_ "github.com/z-cale/zally/x/vote"
	votemodulelv1 "github.com/z-cale/zally/x/vote/module/v1"
	votetypes "github.com/z-cale/zally/x/vote/types"
)

func init() {
	// Set the global bech32 prefixes BEFORE depinject resolves module providers.
	// This is needed because the runtime module computes module authority addresses
	// using the global SDK config, and these must match the auth module's Bech32Prefix.
	cfg := sdk.GetConfig()
	cfg.SetBech32PrefixForAccount("zvote", "zvotepub")
	cfg.SetBech32PrefixForValidator("zvotevaloper", "zvotevaloperpub")
	cfg.SetBech32PrefixForConsensusNode("zvotevalcons", "zvotevalconspub")
}

var (
	// Module account permissions for the minimal module set.
	moduleAccPerms = []*authmodulev1.ModuleAccountPermission{
		{Account: authtypes.FeeCollectorName},
		{Account: distrtypes.ModuleName},
		{Account: stakingtypes.BondedPoolName, Permissions: []string{authtypes.Burner, stakingtypes.ModuleName}},
		{Account: stakingtypes.NotBondedPoolName, Permissions: []string{authtypes.Burner, stakingtypes.ModuleName}},
	}

	// Blocked account addresses (cannot receive funds).
	blockAccAddrs = []string{
		authtypes.FeeCollectorName,
		distrtypes.ModuleName,
		stakingtypes.BondedPoolName,
		stakingtypes.NotBondedPoolName,
	}

	// ModuleConfig is the module configuration for the stripped-down Zally chain.
	// Only the minimal modules needed for block production are included:
	// auth, bank, staking, distribution, consensus, genutil, tx.
	ModuleConfig = []*appv1alpha1.ModuleConfig{
		{
			Name: runtime.ModuleName,
			Config: appconfig.WrapAny(&runtimev1alpha1.Module{
				AppName: "Zally",
				PreBlockers: []string{
					authtypes.ModuleName,
				},
				// Distribution runs before staking in BeginBlock so that
				// validator fee pool is empty before staking updates.
				BeginBlockers: []string{
					distrtypes.ModuleName,
					stakingtypes.ModuleName,
				},
				EndBlockers: []string{
					stakingtypes.ModuleName,
					votetypes.ModuleName, // vote: commitment tree root computation
				},
				OverrideStoreKeys: []*runtimev1alpha1.StoreKeyConfig{
					{
						ModuleName: authtypes.ModuleName,
						KvStoreKey: "acc",
					},
				},
				SkipStoreKeys: []string{
					"tx",
				},
				// genutil must occur after staking (pools initialized from genesis accounts)
				// and after auth (access auth params).
				// vote module added last -- has no genesis dependencies.
				InitGenesis: []string{
					authtypes.ModuleName,
					banktypes.ModuleName,
					distrtypes.ModuleName,
					stakingtypes.ModuleName,
					genutiltypes.ModuleName,
					votetypes.ModuleName,
				},
				ExportGenesis: []string{
					consensustypes.ModuleName,
					authtypes.ModuleName,
					banktypes.ModuleName,
					distrtypes.ModuleName,
					stakingtypes.ModuleName,
					genutiltypes.ModuleName,
					votetypes.ModuleName,
				},
			}),
		},
		{
			Name: authtypes.ModuleName,
			Config: appconfig.WrapAny(&authmodulev1.Module{
				Bech32Prefix:             "zvote",
				ModuleAccountPermissions: moduleAccPerms,
			}),
		},
		{
			Name: banktypes.ModuleName,
			Config: appconfig.WrapAny(&bankmodulev1.Module{
				BlockedModuleAccountsOverride: blockAccAddrs,
			}),
		},
		{
			Name: stakingtypes.ModuleName,
			Config: appconfig.WrapAny(&stakingmodulev1.Module{
				Bech32PrefixValidator: "zvotevaloper",
				Bech32PrefixConsensus: "zvotevalcons",
			}),
		},
		{
			Name: distrtypes.ModuleName,
			Config: appconfig.WrapAny(&distrmodulev1.Module{}),
		},
		{
			Name: consensustypes.ModuleName,
			Config: appconfig.WrapAny(&consensusmodulev1.Module{}),
		},
		{
			Name:   genutiltypes.ModuleName,
			Config: appconfig.WrapAny(&genutilmodulev1.Module{}),
		},
		{
			Name:   votetypes.ModuleName,
			Config: appconfig.WrapAny(&votemodulelv1.Module{}),
		},
		{
			// Skip the built-in ante handler -- we wire our own in app.go.
			// In Phase 3 this will be replaced with custom ZKP/RedPallas validation.
			Name: "tx",
			Config: appconfig.WrapAny(&txconfigv1.Config{
				SkipAnteHandler: true,
			}),
		},
	}

	// AppConfig is the application configuration used by depinject.
	AppConfig = depinject.Configs(appconfig.Compose(&appv1alpha1.Config{
		Modules: ModuleConfig,
	}),
		depinject.Supply(
			// Supply custom module basics for genutil.
			map[string]module.AppModuleBasic{
				genutiltypes.ModuleName: genutil.NewAppModuleBasic(genutiltypes.DefaultMessageValidator),
			},
		),
	)
)
