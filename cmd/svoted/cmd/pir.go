package cmd

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"

	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/server"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"golang.org/x/sync/errgroup"

	"github.com/valargroup/vote-sdk/app"
	"github.com/valargroup/vote-sdk/ffi/pir"
)

// addPIRFlags registers PIR-related CLI flags on the start command.
func addPIRFlags(cmd *cobra.Command) {
	cmd.Flags().Bool("pir", false, "Enable the embedded PIR server")
	cmd.Flags().Int("pir-port", 0, "PIR server listen port (default: from [pir].port or 3000)")
	cmd.Flags().String("pir-data-dir", "", "Directory containing nullifiers.bin, .checkpoint, .index")
	cmd.Flags().String("pir-pir-data-dir", "", "Directory containing PIR tier files")
	cmd.Flags().String("pir-lwd-url", "", "Lightwalletd URL for /snapshot/prepare rebuilds")
}

// pirConfig holds the merged [pir] + CLI configuration.
type pirConfig struct {
	Port       int
	DataDir    string
	PIRDataDir string
	LWDURL     string
	ChainURL   string
}

// readPIRConfig merges the [pir] app.toml section with CLI flag overrides.
func readPIRConfig(v *viper.Viper, homeDir string) pirConfig {
	cfg := pirConfig{
		Port:       3000,
		DataDir:    filepath.Join(homeDir, "nullifiers"),
		PIRDataDir: filepath.Join(homeDir, "nullifiers", "pir-data"),
	}

	if v.IsSet("pir.port") {
		cfg.Port = v.GetInt("pir.port")
	}
	if v.IsSet("pir.data_dir") {
		if d := v.GetString("pir.data_dir"); d != "" {
			cfg.DataDir = d
		}
	}
	if v.IsSet("pir.pir_data_dir") {
		if d := v.GetString("pir.pir_data_dir"); d != "" {
			cfg.PIRDataDir = d
		}
	}
	if v.IsSet("pir.lwd_url") {
		cfg.LWDURL = v.GetString("pir.lwd_url")
	}
	if v.IsSet("pir.chain_url") {
		cfg.ChainURL = v.GetString("pir.chain_url")
	}

	// CLI flags take precedence over config file.
	if p := v.GetInt("pir-port"); p != 0 {
		cfg.Port = p
	}
	if d := v.GetString("pir-data-dir"); d != "" {
		cfg.DataDir = d
	}
	if d := v.GetString("pir-pir-data-dir"); d != "" {
		cfg.PIRDataDir = d
	}
	if u := v.GetString("pir-lwd-url"); u != "" {
		cfg.LWDURL = u
	}

	return cfg
}

// checkPortConflicts returns an error if the PIR port clashes with any known
// chain port read from the viper config.
func (c pirConfig) checkPortConflicts(v *viper.Viper) error {
	type portEntry struct {
		key  string
		port int
	}
	var known []portEntry

	if p := extractPort(v.GetString("api.address")); p != 0 {
		known = append(known, portEntry{"api.address", p})
	}
	if p := extractPort(v.GetString("grpc.address")); p != 0 {
		known = append(known, portEntry{"grpc.address", p})
	}
	if p := extractPort(v.GetString("grpc-web.address")); p != 0 {
		known = append(known, portEntry{"grpc-web.address", p})
	}

	for _, e := range known {
		if e.port == c.Port {
			return fmt.Errorf("PIR port %d conflicts with %s", c.Port, e.key)
		}
	}
	return nil
}

// extractPort parses the port from an address string like "tcp://0.0.0.0:1418"
// or "localhost:9090".
func extractPort(addr string) int {
	for i := len(addr) - 1; i >= 0; i-- {
		if addr[i] == ':' {
			p, err := strconv.Atoi(addr[i+1:])
			if err == nil {
				return p
			}
			return 0
		}
	}
	return 0
}

// pirPostSetup starts the embedded PIR server when --pir is passed.
func pirPostSetup(
	_ **app.SvoteApp,
) func(svrCtx *server.Context, clientCtx client.Context, ctx context.Context, g *errgroup.Group) error {
	return func(svrCtx *server.Context, _ client.Context, ctx context.Context, g *errgroup.Group) error {
		if !svrCtx.Viper.GetBool("pir") {
			svrCtx.Logger.Info("embedded PIR disabled (pass --pir to enable)")
			return nil
		}

		logger := svrCtx.Logger.With("module", "pir")

		cfg := readPIRConfig(svrCtx.Viper, svrCtx.Config.RootDir)
		if err := cfg.checkPortConflicts(svrCtx.Viper); err != nil {
			return fmt.Errorf("pir: %w", err)
		}

		binPath, err := pir.ExtractTo(svrCtx.Config.RootDir)
		if err != nil {
			return fmt.Errorf("pir: %w", err)
		}

		args := []string{
			"serve",
			"--port", strconv.Itoa(cfg.Port),
			"--data-dir", cfg.DataDir,
			"--pir-data-dir", cfg.PIRDataDir,
		}
		if cfg.LWDURL != "" {
			args = append(args, "--lwd-url", cfg.LWDURL)
		}
		if cfg.ChainURL != "" {
			args = append(args, "--chain-url", cfg.ChainURL)
		}

		g.Go(func() error {
			cmd := pir.Run(ctx, binPath, logger, args...)
			logger.Info("starting nf-server", "port", cfg.Port, "data-dir", cfg.DataDir)
			err := cmd.Run()
			if ctx.Err() != nil {
				return nil
			}
			return err
		})

		return nil
	}
}
