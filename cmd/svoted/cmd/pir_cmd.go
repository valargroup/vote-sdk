package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/valargroup/vote-sdk/app"
	"github.com/valargroup/vote-sdk/ffi/pir"
)

// PIRCmd returns the `svoted pir` command tree, exposing the embedded nf-server
// binary for ingest, export, and serve operations.
func PIRCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pir",
		Short: "Nullifier PIR server operations (ingest, export, serve)",
		Long: `Extract and run the embedded nf-server binary for Private Information
Retrieval operations on nullifier data. The binary is bundled inside
svoted at build time.`,
	}

	cmd.AddCommand(
		pirSubcommand("ingest", "Ingest nullifier data from the chain"),
		pirSubcommand("export", "Export PIR tier data from ingested nullifiers"),
		pirSubcommand("serve", "Run the PIR query server"),
	)

	return cmd
}

func pirSubcommand(name, short string) *cobra.Command {
	return &cobra.Command{
		Use:                name + " [flags]",
		Short:              short,
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			homeDir := app.DefaultNodeHome
			binPath, err := pir.ExtractTo(homeDir)
			if err != nil {
				return fmt.Errorf("pir: extract embedded binary: %w", err)
			}

			nfArgs := append([]string{name}, args...)
			return execBinary(binPath, nfArgs)
		},
	}
}

// execBinary replaces the current process with the extracted nf-server binary
// on platforms that support it, or falls back to exec.Command + Wait.
func execBinary(binPath string, args []string) error {
	absPath, err := filepath.Abs(binPath)
	if err != nil {
		return err
	}

	child := exec.Command(absPath, args...)
	child.Stdin = os.Stdin
	child.Stdout = os.Stdout
	child.Stderr = os.Stderr
	return child.Run()
}
