package cmd

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/server"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"golang.org/x/sync/errgroup"

	"github.com/valargroup/vote-sdk/app"
	"github.com/valargroup/vote-sdk/ffi/pir"
)

// pirStartupWindow is the max time we wait for nf-server to bind its listen
// port after spawn. nf-server's real startup is dominated by Tier 2 YPIR
// offline precomputation which runs for ~40s on an Apple M-series laptop and
// can be longer on smaller VMs, so we give it a generous ceiling. Any exit
// (or still-not-listening state) before this deadline is treated as a
// startup failure and tears down svoted; exits after the deadline are
// logged as runtime crashes without bringing down the chain.
const pirStartupWindow = 5 * time.Minute

// pirReadinessPoll is how often we probe the PIR listen port while waiting
// for readiness during the startup window.
const pirReadinessPoll = 500 * time.Millisecond

// pirDisableHint is appended to every PIR startup error so the operator
// always has an unambiguous opt-out.
const pirDisableHint = "To disable PIR, remove --serve-pir from the svoted start command " +
	"(or stop the service and edit the unit file; see docs/svoted-val1.service)."

// addPIRFlags registers PIR-related CLI flags on the start command.
func addPIRFlags(cmd *cobra.Command) {
	cmd.Flags().Bool("serve-pir", false, "Enable the embedded PIR server")
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

// pirPostSetup starts the embedded PIR server when --serve-pir is passed.
//
// Startup is fail-fast: if the nf-server binary isn't embedded, if required
// tier files are missing, or if the child process exits within
// pirStartupWindow of spawn, the error is propagated to the errgroup so
// svoted shuts down cleanly rather than silently running without PIR. After
// the startup window elapses a subsequent crash is logged but does not tear
// down the chain.
func pirPostSetup(
	_ **app.SvoteApp,
) func(svrCtx *server.Context, clientCtx client.Context, ctx context.Context, g *errgroup.Group) error {
	return func(svrCtx *server.Context, _ client.Context, ctx context.Context, g *errgroup.Group) error {
		if !svrCtx.Viper.GetBool("serve-pir") {
			svrCtx.Logger.Info("embedded PIR disabled (pass --serve-pir to enable)")
			return nil
		}

		logger := svrCtx.Logger.With("module", "pir")

		cfg := readPIRConfig(svrCtx.Viper, svrCtx.Config.RootDir)
		if err := cfg.checkPortConflicts(svrCtx.Viper); err != nil {
			return fmt.Errorf("pir: %w\n\n%s", err, pirDisableHint)
		}

		binPath, err := pir.ExtractTo(svrCtx.Config.RootDir)
		if err != nil {
			if errors.Is(err, pir.ErrNotEmbedded) {
				return fmt.Errorf("pir: %w\n\n"+
					"This svoted binary was built without the nf-server payload. To fix, either:\n"+
					"  • rebuild with PIR embedded: `mise run install` (EMBED_PIR=1 is the default), or\n"+
					"  • %s",
					err, lowerFirst(pirDisableHint))
			}
			return fmt.Errorf("pir: extract nf-server: %w\n\n%s", err, pirDisableHint)
		}

		if err := preflightPIRData(cfg); err != nil {
			return err
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

		// The nf-server child is supervised by two concurrent goroutines
		// sharing the errgroup:
		//   1. runner:    spawns the process, waits for it to exit, classifies
		//                 the exit as startup-failure (< readiness) or runtime
		//                 crash (post-readiness) and propagates accordingly.
		//   2. watchdog:  polls the listen port during the startup window.
		//                 If readiness doesn't land within the deadline, it
		//                 kills the child via ctx cancellation so runner
		//                 observes the failure and reports it.
		// The ready channel is closed by the watchdog once the port binds;
		// runner uses it to decide whether a subsequent child exit should be
		// fatal.
		//
		// A note on shutdown propagation: Cosmos SDK's ListenForQuitSignals
		// sits in the same errgroup and blocks on os.Signal, not ctx. That
		// means returning a non-nil error from g.Go here will NOT cause
		// startInProcess to return until SIGINT/SIGTERM arrives, and the
		// error itself is stored silently on the errgroup. To make startup
		// failure visible and terminal, we therefore also log the error at
		// ERROR level and raise SIGTERM on the current process so the
		// quit-signal goroutine cancels the parent context and svoted
		// unwinds with a non-zero exit code.
		startedAt := time.Now()
		ready := make(chan struct{})
		childCtx, cancelChild := context.WithCancel(ctx)

		// startupFail surfaces an error as both a logged message and a
		// SIGTERM to self, then returns the error so it's stored on the
		// errgroup for the caller of g.Wait(). Must be called at most once
		// per supervisor lifecycle; subsequent invocations are harmless
		// but noisy.
		startupFail := func(err error) error {
			logger.Error("pir startup failed", "error", err.Error())
			_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)
			return err
		}

		g.Go(func() error {
			defer cancelChild()
			cmd := pir.Run(childCtx, binPath, logger, args...)
			logger.Info("starting nf-server",
				"port", cfg.Port,
				"data-dir", cfg.DataDir,
				"pir-data-dir", cfg.PIRDataDir,
				"args", strings.Join(args, " "),
			)
			// Start + Wait (rather than Run) so we can surface spawn
			// errors distinct from runtime errors, and so we can log
			// the child pid immediately for operator visibility.
			if err := cmd.Start(); err != nil {
				return startupFail(fmt.Errorf("pir: failed to spawn nf-server at %s: %w\n\n%s",
					binPath, err, pirDisableHint))
			}
			logger.Info("nf-server spawned", "pid", cmd.Process.Pid, "port", cfg.Port)
			runErr := cmd.Wait()
			if ctx.Err() != nil {
				// Parent svoted is shutting down; child exit is expected.
				return nil
			}
			ranFor := time.Since(startedAt)
			select {
			case <-ready:
				// Child was healthy at some point — treat exit as a
				// runtime crash so the chain keeps going. The operator
				// sees it in the log and must intervene manually; PIR
				// queries will fail from clients until they do.
				logger.Error("nf-server crashed after becoming ready",
					"ran_for", ranFor, "error", runErr)
				return nil
			default:
			}
			// Never became ready — this IS a startup failure regardless
			// of whether runErr is nil (clean exit 0 is still wrong at
			// this point) or non-nil.
			return startupFail(fmt.Errorf("pir: nf-server failed to become ready on port %d within %s "+
				"(child exited after %s, err=%v)\n"+
				"This usually means the tier data at %s is corrupt or size-mismatched, "+
				"the port is held by another process, or the host is too slow to complete "+
				"Tier 2 YPIR precomputation inside the window.\n"+
				"To fix, either:\n"+
				"  • re-provision tier data: `mise run pir:bootstrap` (rebuilds from lwd_url, ~6 GB), or\n"+
				"  • free port %d / increase svoted's available CPU, or\n"+
				"  • %s",
				cfg.Port, pirStartupWindow, ranFor.Round(time.Millisecond), runErr,
				cfg.PIRDataDir, cfg.Port, lowerFirst(pirDisableHint)))
		})

		g.Go(func() error {
			deadline := time.NewTimer(pirStartupWindow)
			defer deadline.Stop()
			ticker := time.NewTicker(pirReadinessPoll)
			defer ticker.Stop()
			for {
				if isPIRPortListening(cfg.Port) {
					logger.Info("nf-server ready",
						"port", cfg.Port,
						"elapsed", time.Since(startedAt).Round(time.Millisecond),
					)
					close(ready)
					return nil
				}
				select {
				case <-ctx.Done():
					return nil
				case <-childCtx.Done():
					// Child exited before readiness — runner goroutine
					// will produce the startup-failure error.
					return nil
				case <-deadline.C:
					logger.Error("nf-server did not become ready within startup window; killing child",
						"window", pirStartupWindow, "port", cfg.Port)
					cancelChild()
					return nil
				case <-ticker.C:
				}
			}
		})

		return nil
	}
}

// isPIRPortListening returns true when a TCP connection to localhost:port
// succeeds, indicating nf-server has bound its listen socket and is ready
// to answer PIR queries.
func isPIRPortListening(port int) bool {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 200*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// preflightPIRData verifies the tier files nf-server needs at startup are
// present. It's a cheap deterministic check that catches the overwhelmingly
// most common failure mode (missing data after a chain reset) with a clear
// error before we spawn the child process.
//
// Mirrors what pir_server::load_serving_state loads:
//
//	{pir_data_dir}/tier0.bin
//	{pir_data_dir}/tier1.bin
//	{pir_data_dir}/tier2.bin
//	{pir_data_dir}/pir_root.json
//
// See .cache/vote-nullifier-pir/pir/server/src/lib.rs::load_serving_state.
// Size/content validation is left to nf-server itself; if tier files exist
// but are corrupt the startup-window watchdog in pirPostSetup catches it.
func preflightPIRData(cfg pirConfig) error {
	required := []string{"tier0.bin", "tier1.bin", "tier2.bin", "pir_root.json"}
	var missing []string
	for _, name := range required {
		p := filepath.Join(cfg.PIRDataDir, name)
		info, err := os.Stat(p)
		if err != nil || info.Size() == 0 {
			missing = append(missing, p)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("pir: required tier files missing or empty under %s:\n    %s\n\n"+
		"To fix, either:\n"+
		"  • provision tier data: `mise run pir:bootstrap` (ingest + export from %s, ~6 GB), or\n"+
		"  • copy from a peer that already has it: `rsync -avh root@<host>:/opt/nf-ingest/ %s/`, or\n"+
		"  • %s",
		cfg.PIRDataDir,
		strings.Join(missing, "\n    "),
		fallbackString(cfg.LWDURL, "the configured [pir].lwd_url"),
		cfg.DataDir,
		lowerFirst(pirDisableHint),
	)
}

// lowerFirst lowercases the first rune so a sentence-cased hint can be
// interpolated as a continuation of a bulleted list ("... or, <hint>").
func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToLower(s[:1]) + s[1:]
}

// fallbackString returns s when it's non-empty, otherwise fallback.
func fallbackString(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}
