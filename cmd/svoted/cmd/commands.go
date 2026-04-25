package cmd

import (
	"context"
	"errors"
	"io"
	"time"

	cmtcfg "github.com/cometbft/cometbft/config"
	dbm "github.com/cosmos/cosmos-db"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"golang.org/x/sync/errgroup"

	"cosmossdk.io/log"

	"github.com/valargroup/vote-sdk/app"

	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/client/debug"
	"github.com/cosmos/cosmos-sdk/client/keys"
	"github.com/cosmos/cosmos-sdk/client/pruning"
	"github.com/cosmos/cosmos-sdk/client/rpc"
	"github.com/cosmos/cosmos-sdk/client/snapshot"
	"github.com/cosmos/cosmos-sdk/server"
	serverconfig "github.com/cosmos/cosmos-sdk/server/config"
	servertypes "github.com/cosmos/cosmos-sdk/server/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/module"
	"github.com/cosmos/cosmos-sdk/version"
	authcmd "github.com/cosmos/cosmos-sdk/x/auth/client/cli"
	genutilcli "github.com/cosmos/cosmos-sdk/x/genutil/client/cli"

	votecli "github.com/valargroup/vote-sdk/x/vote/client/cli"
)

// initCometBFTConfig overrides CometBFT defaults to reduce end-to-end block
// time, following the Osmosis approach. See docs/blocktimes.md for the full
// rationale, Osmosis comparison, and benchmark results.
func initCometBFTConfig() *cmtcfg.Config {
	cfg := cmtcfg.DefaultConfig()

	cfg.Consensus.TimeoutPropose = 1800 * time.Millisecond
	cfg.Consensus.TimeoutCommit = 400 * time.Millisecond
	cfg.Consensus.PeerGossipSleepDuration = 50 * time.Millisecond

	cfg.P2P.FlushThrottleTimeout = 80 * time.Millisecond

	return cfg
}

// VoteConfig holds the [vote] section of app.toml.
type VoteConfig struct {
	EASkPath     string `mapstructure:"ea_sk_path"`
	PallasSkPath string `mapstructure:"pallas_sk_path"`
	CometRPC     string `mapstructure:"comet_rpc"`
}

// CustomAppConfig embeds the standard server config and adds [vote].
type CustomAppConfig struct {
	serverconfig.Config `mapstructure:",squash"`
	Vote                VoteConfig `mapstructure:"vote"`
}

const voteConfigTemplate = `
###############################################################################
###                         Vote Configuration                              ###
###############################################################################

[vote]

# Path to the Election Authority secret key file.
ea_sk_path = "{{ .Vote.EASkPath }}"

# Path to the Pallas secret key file.
pallas_sk_path = "{{ .Vote.PallasSkPath }}"

# CometBFT RPC endpoint. Adjust to the node's RPC port.
comet_rpc = "{{ .Vote.CometRPC }}"
`

const adminConfigTemplate = `
###############################################################################
###                         Admin Configuration                             ###
###############################################################################

[admin]

# When true, disables the join-queue API (register-validator, pending-validators,
# server-heartbeat), the watchdog, and the cached /api/voting-config endpoint.
# Default true so only the designated admin host runs the join-queue SQLite DB.
# Enable (false) only on the node that serves the admin UI: production primary
# (SVOTE_ADMIN_DISABLE=false in init.sh / reset workflow), or val1 from init_multi.sh.
disable = true

# Voting-config JSON polled by the admin watchdog (and re-served on
# GET /api/voting-config as a cached copy). Same canonical CDN URL that
# wallets and join.sh fetch directly — override only for staging mirrors.
config_url = "https://valargroup.github.io/token-holder-voting-config/voting-config.json"

# How often to probe vote_servers and pir_endpoints (0 = disabled).
watchdog_interval = "5m"

# SQLite database path for pending validator join requests (empty = $HOME/.svoted/admin.db).
# db_path = ""
`

// initAppConfig helps to override default appConfig template and configs.
func initAppConfig() (string, interface{}) {
	srvCfg := serverconfig.DefaultConfig()
	// Set default min gas prices to 0 for the vote chain (no fees needed).
	srvCfg.MinGasPrices = "0usvote"

	customConfig := CustomAppConfig{
		Config: *srvCfg,
		Vote: VoteConfig{
			EASkPath:     "$HOME/.svoted/ea.sk",
			PallasSkPath: "$HOME/.svoted/pallas.sk",
			CometRPC:     "http://localhost:26657",
		},
	}

	return serverconfig.DefaultConfigTemplate + voteConfigTemplate + adminConfigTemplate, customConfig
}

func initRootCmd(
	rootCmd *cobra.Command,
	txConfig client.TxConfig,
	basicManager module.BasicManager,
) {
	cfg := sdk.GetConfig()
	cfg.Seal()

	// Capture a reference to the app created by newApp, so the helper
	// PostSetup can access the VoteKeeper for reading tree leaves.
	var svoteAppRef *app.SvoteApp
	newAppFn := func(
		logger log.Logger,
		db dbm.DB,
		traceStore io.Writer,
		appOpts servertypes.AppOptions,
	) servertypes.Application {
		baseappOptions := server.DefaultBaseappOptions(appOpts)
		svoteAppRef = app.NewSvoteApp(
			logger, db, traceStore, true,
			appOpts,
			baseappOptions...,
		)
		return svoteAppRef
	}

	rootCmd.AddCommand(
		genutilcli.InitCmd(basicManager, app.DefaultNodeHome),
		debug.Cmd(),
		pruning.Cmd(newApp, app.DefaultNodeHome),
		snapshot.Cmd(newApp),
		version.NewVersionCommand(),
		EAKeygenCmd(),
		PallasKeygenCmd(),
		EncryptEAKeyCmd(),
		InitValidatorKeysCmd(),
		SignArbitraryCmd(),
	)

	helperSetup := helperPostSetup(&svoteAppRef)
	adminSetup := adminPostSetup(&svoteAppRef)
	server.AddCommandsWithStartCmdOptions(rootCmd, app.DefaultNodeHome, newAppFn, appExport, server.StartCmdOptions{
		PostSetup: func(svrCtx *server.Context, clientCtx client.Context, ctx context.Context, g *errgroup.Group) error {
			if err := helperSetup(svrCtx, clientCtx, ctx, g); err != nil {
				return err
			}
			return adminSetup(svrCtx, clientCtx, ctx, g)
		},
		AddFlags: func(cmd *cobra.Command) {
			addHelperFlags(cmd)
			addAdminFlags(cmd)
		},
	})

	// add keybase, auxiliary RPC, query, genesis, and tx child commands
	rootCmd.AddCommand(
		server.StatusCommand(),
		genesisCommand(txConfig, basicManager),
		queryCommand(),
		txCommand(),
		keys.Commands(),
	)
}

// genesisCommand builds genesis-related `svoted genesis` command.
func genesisCommand(txConfig client.TxConfig, basicManager module.BasicManager, cmds ...*cobra.Command) *cobra.Command {
	cmd := genutilcli.Commands(txConfig, basicManager, app.DefaultNodeHome)

	for _, subCmd := range cmds {
		cmd.AddCommand(subCmd)
	}
	return cmd
}

func queryCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:                        "query",
		Aliases:                    []string{"q"},
		Short:                      "Querying subcommands",
		DisableFlagParsing:         false,
		SuggestionsMinimumDistance: 2,
		RunE:                       client.ValidateCmd,
	}

	cmd.AddCommand(
		rpc.WaitTxCmd(),
		server.QueryBlockCmd(),
		authcmd.QueryTxsByEventsCmd(),
		server.QueryBlocksCmd(),
		authcmd.QueryTxCmd(),
		server.QueryBlockResultsCmd(),
	)

	return cmd
}

func txCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:                        "tx",
		Short:                      "Transactions subcommands",
		DisableFlagParsing:         false,
		SuggestionsMinimumDistance: 2,
		RunE:                       client.ValidateCmd,
	}

	cmd.AddCommand(
		authcmd.GetSignCommand(),
		authcmd.GetSignBatchCommand(),
		authcmd.GetMultiSignCommand(),
		authcmd.GetMultiSignBatchCmd(),
		authcmd.GetValidateSignaturesCommand(),
		authcmd.GetBroadcastCommand(),
		authcmd.GetEncodeCommand(),
		authcmd.GetDecodeCommand(),
		authcmd.GetSimulateCmd(),
		votecli.GetTxCmd(),
	)

	return cmd
}

// newApp creates the application.
func newApp(
	logger log.Logger,
	db dbm.DB,
	traceStore io.Writer,
	appOpts servertypes.AppOptions,
) servertypes.Application {
	baseappOptions := server.DefaultBaseappOptions(appOpts)
	return app.NewSvoteApp(
		logger, db, traceStore, true,
		appOpts,
		baseappOptions...,
	)
}

// appExport creates a new SvoteApp (optionally at a given height) and exports state.
func appExport(
	logger log.Logger,
	db dbm.DB,
	traceStore io.Writer,
	height int64,
	forZeroHeight bool,
	jailAllowedAddrs []string,
	appOpts servertypes.AppOptions,
	modulesToExport []string,
) (servertypes.ExportedApp, error) {
	viperAppOpts, ok := appOpts.(*viper.Viper)
	if !ok {
		return servertypes.ExportedApp{}, errors.New("appOpts is not viper.Viper")
	}

	// overwrite the FlagInvCheckPeriod
	viperAppOpts.Set(server.FlagInvCheckPeriod, 1)
	appOpts = viperAppOpts

	var svoteApp *app.SvoteApp
	if height != -1 {
		svoteApp = app.NewSvoteApp(logger, db, traceStore, false, appOpts)

		if err := svoteApp.LoadHeight(height); err != nil {
			return servertypes.ExportedApp{}, err
		}
	} else {
		svoteApp = app.NewSvoteApp(logger, db, traceStore, true, appOpts)
	}

	return svoteApp.ExportAppStateAndValidators(forZeroHeight, jailAllowedAddrs, modulesToExport)
}
