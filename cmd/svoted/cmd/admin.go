package cmd

import (
	"context"
	"fmt"

	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/server"
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

// adminPostSetup initializes the admin config proxy and optionally enables UI serving.
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
				distPath = svrCtx.Viper.GetString("ui.dist_path")
			}
			if distPath != "" {
				(*svoteApp).SetUIDistPath(distPath)
				logger.Info("UI serving enabled", "dist", distPath)
			} else {
				logger.Error("--serve-ui specified but no dist path provided (use --ui-dist or [ui].dist_path)")
			}
		}

		// Admin config proxy.
		cfg := readAdminConfig(svrCtx.Viper)

		if v, _ := svrCtx.Viper.Get("no-admin").(bool); v {
			cfg.Disable = true
		}
		if cfg.Disable {
			logger.Info("admin server disabled")
			return nil
		}

		homeDir := clientCtx.HomeDir
		checkBonded := func(valoper string) bool {
			return (*svoteApp).ValidatorValoperBonded(valoper)
		}

		a, err := admin.New(cfg, homeDir, checkBonded, logger)
		if err != nil {
			return fmt.Errorf("admin: %w", err)
		}
		if a == nil {
			return nil
		}

		(*svoteApp).SetAdmin(a)

		g.Go(func() error {
			admin.RunConfigRefresher(ctx, a, admin.VotingConfigRefreshInterval, logger)
			return nil
		})

		g.Go(func() error {
			admin.RunPendingSweeper(ctx, a, admin.PendingEvictionSweepInterval, logger)
			return nil
		})

		g.Go(func() error {
			admin.RunVoteServerHealthPoller(ctx, a, admin.VoteServerHealthProbeInterval, logger)
			return nil
		})

		logger.Info("admin config proxy initialized")
		return nil
	}
}

// readAdminConfig reads the [admin] section from app.toml via viper.
func readAdminConfig(v *viper.Viper) admin.Config {
	cfg := admin.DefaultConfig()

	if v.IsSet("admin.disable") {
		cfg.Disable = v.GetBool("admin.disable")
	}
	if v.IsSet("admin.config_url") {
		cfg.ConfigURL = v.GetString("admin.config_url")
	}
	if v.IsSet("admin.db_path") {
		cfg.DBPath = v.GetString("admin.db_path")
	}

	return cfg
}
