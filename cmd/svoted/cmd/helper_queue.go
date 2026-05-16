package cmd

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cosmos/cosmos-sdk/client/flags"
	"github.com/spf13/cobra"

	"github.com/valargroup/vote-sdk/app"
	"github.com/valargroup/vote-sdk/internal/helper"
)

// helperQueueCmd builds local helper queue rescue subcommands.
func helperQueueCmd() *cobra.Command {
	var dbPath string

	cmd := &cobra.Command{
		Use:   "helper",
		Short: "Helper queue maintenance commands",
	}
	cmd.PersistentFlags().StringVar(&dbPath, "db-path", "", "Path to helper SQLite database (default: <home>/helper.db)")
	cmd.PersistentFlags().String(flags.FlagHome, app.DefaultNodeHome, "The application home directory")

	cmd.AddCommand(
		helperExportQueueCmd(&dbPath),
		helperImportQueueCmd(&dbPath),
	)
	return cmd
}

// helperExportQueueCmd builds the command that exports one round's helper queue.
func helperExportQueueCmd(dbPath *string) *cobra.Command {
	var roundID string
	var outPath string

	cmd := &cobra.Command{
		Use:   "export-queue --round-id <hex> --out <file>",
		Short: "Export one round's local helper share queue",
		RunE: func(cmd *cobra.Command, _ []string) error {
			normalizedRoundID, err := normalizeRoundID(roundID)
			if err != nil {
				return err
			}
			if strings.TrimSpace(outPath) == "" {
				return fmt.Errorf("--out is required")
			}

			store, err := helper.NewShareStore(resolveHelperDBPath(cmd, *dbPath), nil)
			if err != nil {
				return err
			}
			defer store.Close()

			export, err := store.ExportQueue(normalizedRoundID, time.Now())
			if err != nil {
				return err
			}

			f, err := os.OpenFile(outPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
			if err != nil {
				return fmt.Errorf("open export file: %w", err)
			}
			enc := json.NewEncoder(f)
			enc.SetIndent("", "  ")
			if err := enc.Encode(export); err != nil {
				f.Close()
				return fmt.Errorf("write export file: %w", err)
			}
			if err := f.Close(); err != nil {
				return fmt.Errorf("close export file: %w", err)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "exported %d queue rows for round %s\n", len(export.Rows), normalizedRoundID)
			return nil
		},
	}
	cmd.Flags().StringVar(&roundID, "round-id", "", "Voting round ID as 32-byte hex")
	cmd.Flags().StringVar(&outPath, "out", "", "Output JSON file path")
	return cmd
}

// helperImportQueueCmd builds the command that imports processable queue rows.
func helperImportQueueCmd(dbPath *string) *cobra.Command {
	var inPath string
	var forceReady bool

	cmd := &cobra.Command{
		Use:   "import-queue --in <file>",
		Short: "Import processable rows from a local helper queue export",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if strings.TrimSpace(inPath) == "" {
				return fmt.Errorf("--in is required")
			}

			raw, err := os.ReadFile(inPath)
			if err != nil {
				return fmt.Errorf("read import file: %w", err)
			}
			var export helper.QueueExport
			if err := json.Unmarshal(raw, &export); err != nil {
				return fmt.Errorf("decode import file: %w", err)
			}
			normalizedRoundID, err := normalizeRoundID(export.RoundID)
			if err != nil {
				return fmt.Errorf("invalid import round_id: %w", err)
			}
			export.RoundID = normalizedRoundID

			store, err := helper.NewShareStore(resolveHelperDBPath(cmd, *dbPath), nil)
			if err != nil {
				return err
			}
			defer store.Close()

			result, err := store.ImportQueue(export, helper.QueueImportOptions{ForceReady: forceReady})
			if err != nil {
				return err
			}
			fmt.Fprintf(
				cmd.OutOrStdout(),
				"inserted=%d duplicates=%d conflicts=%d skipped_terminal=%d\n",
				result.Inserted,
				result.Duplicates,
				result.Conflicts,
				result.SkippedTerminal,
			)
			if result.Conflicts > 0 {
				return fmt.Errorf("import completed with %d conflicting rows", result.Conflicts)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&inPath, "in", "", "Input queue export JSON file path")
	cmd.Flags().BoolVar(&forceReady, "force-ready", false, "Schedule processable imported rows immediately instead of respecting submit_at")
	return cmd
}

// normalizeRoundID validates a 32-byte hex round ID and returns lowercase hex.
func normalizeRoundID(roundID string) (string, error) {
	roundID = strings.ToLower(strings.TrimSpace(roundID))
	decoded, err := hex.DecodeString(roundID)
	if err != nil || len(decoded) != 32 {
		return "", fmt.Errorf("round id must be 32-byte hex")
	}
	return roundID, nil
}

// resolveHelperDBPath returns the explicit DB path or the helper DB under home.
func resolveHelperDBPath(cmd *cobra.Command, dbPath string) string {
	if strings.TrimSpace(dbPath) != "" {
		return dbPath
	}
	homeDir, err := cmd.Flags().GetString(flags.FlagHome)
	if err != nil || homeDir == "" {
		homeDir = app.DefaultNodeHome
	}
	return filepath.Join(homeDir, "helper.db")
}
