package cmd

import (
	"context"
	"fmt"

	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/server"
	sdk "github.com/cosmos/cosmos-sdk/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"golang.org/x/sync/errgroup"

	"github.com/valargroup/vote-sdk/app"
	"github.com/valargroup/vote-sdk/internal/admin"
)

// addAdminFlags registers admin + UI CLI flags on the start command.
func addAdminFlags(cmd *cobra.Command) {
	cmd.Flags().Bool("no-admin", false, "Disable the admin server")
	cmd.Flags().Bool("serve-ui", false, "Serve the admin UI from the built dist directory")
	cmd.Flags().String("ui-dist", "", "Path to the UI dist directory (requires --serve-ui)")
}

// adminPostSetup starts the admin server and optionally enables UI serving.
func adminPostSetup(
	svoteApp **app.SvoteApp,
) func(svrCtx *server.Context, clientCtx client.Context, ctx context.Context, g *errgroup.Group) error {
	return func(svrCtx *server.Context, clientCtx client.Context, ctx context.Context, g *errgroup.Group) error {
		if *svoteApp == nil {
			return fmt.Errorf("admin: app not initialized")
		}

		logger := svrCtx.Logger.With("module", "admin")

		// UI serving (independent of admin server).
		if svrCtx.Viper.GetBool("serve-ui") {
			distPath := svrCtx.Viper.GetString("ui-dist")
			if distPath == "" {
				// Check app.toml [ui] section.
				distPath = svrCtx.Viper.GetString("ui.dist_path")
			}
			if distPath != "" {
				(*svoteApp).SetUIDistPath(distPath)
				logger.Info("UI serving enabled", "dist", distPath)
			} else {
				logger.Error("--serve-ui specified but no dist path provided (use --ui-dist or [ui].dist_path)")
			}
		}

		// Admin server.
		cfg := readAdminConfig(svrCtx.Viper, logger)

		if v, _ := svrCtx.Viper.Get("no-admin").(bool); v {
			cfg.Disable = true
		}
		if cfg.Disable {
			logger.Info("admin server disabled")
			return nil
		}

		bondingChecker := func(valoperAddress string) bool {
			valAddr, err := sdk.ValAddressFromBech32(valoperAddress)
			if err != nil {
				return false
			}
			ctx := (*svoteApp).NewUncachedContext(false, cmtproto.Header{})
			val, err := (*svoteApp).StakingKeeper.GetValidator(ctx, valAddr)
			if err != nil {
				return false
			}
			return val.GetStatus() == stakingtypes.Bonded
		}

		voteManagerGetter := func() string {
			ctx := (*svoteApp).NewUncachedContext(false, cmtproto.Header{})
			kvStore := (*svoteApp).VoteKeeper.OpenKVStore(ctx)
			mgr, err := (*svoteApp).VoteKeeper.GetVoteManager(kvStore)
			if err != nil || mgr == nil {
				return ""
			}
			return mgr.Address
		}

		homeDir := svrCtx.Config.RootDir
		a, err := admin.New(cfg, bondingChecker, voteManagerGetter, homeDir, logger)
		if err != nil {
			return fmt.Errorf("admin: %w", err)
		}
		if a == nil {
			return nil
		}

		(*svoteApp).SetAdmin(a)

		g.Go(func() error {
			err := a.Start(ctx, cfg)
			a.Close()
			return err
		})

		logger.Info("admin server started")
		return nil
	}
}

// readAdminConfig reads the [admin] section from app.toml via viper.
func readAdminConfig(v *viper.Viper, logger interface{ Info(string, ...interface{}) }) admin.Config {
	cfg := admin.DefaultConfig()

	if v.IsSet("admin.disable") {
		cfg.Disable = v.GetBool("admin.disable")
	}
	if v.IsSet("admin.db_path") {
		cfg.DBPath = v.GetString("admin.db_path")
	}
	if v.IsSet("admin.admin_address") {
		cfg.AdminAddress = v.GetString("admin.admin_address")
	}
	if v.IsSet("admin.probe_interval") {
		cfg.ProbeInterval = v.GetInt("admin.probe_interval")
	}
	if v.IsSet("admin.evict_interval") {
		cfg.EvictInterval = v.GetInt("admin.evict_interval")
	}
	if v.IsSet("admin.stale_threshold") {
		cfg.StaleThreshold = v.GetInt("admin.stale_threshold")
	}
	if v.IsSet("admin.pir_servers") {
		cfg.PIRServers = v.GetString("admin.pir_servers")
	}

	return cfg
}
