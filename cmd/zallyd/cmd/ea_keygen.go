package cmd

import (
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/z-cale/zally/crypto/elgamal"
)

// EAKeygenCmd generates an ElGamal keypair for the Election Authority.
// The secret key is written to <home>/ea.sk and the public key to <home>/ea.pk.
func EAKeygenCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ea-keygen",
		Short: "Generate ElGamal keypair for the Election Authority",
		Long: `Generates an ElGamal keypair and writes:
  - <home>/ea.sk  (32-byte secret key)
  - <home>/ea.pk  (32-byte compressed public key)

The secret key path should be configured in app.toml as vote.ea_sk_path
so that PrepareProposal can auto-decrypt tallies.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			homeDir, _ := cmd.Flags().GetString("home")
			if homeDir == "" {
				return fmt.Errorf("--home flag is required")
			}

			sk, pk := elgamal.KeyGen(rand.Reader)

			skBytes, err := elgamal.MarshalSecretKey(sk)
			if err != nil {
				return fmt.Errorf("failed to marshal secret key: %w", err)
			}

			pkBytes := pk.Point.ToAffineCompressed()

			skPath := filepath.Join(homeDir, "ea.sk")
			pkPath := filepath.Join(homeDir, "ea.pk")

			if err := os.WriteFile(skPath, skBytes, 0600); err != nil {
				return fmt.Errorf("failed to write secret key: %w", err)
			}

			if err := os.WriteFile(pkPath, pkBytes, 0644); err != nil {
				return fmt.Errorf("failed to write public key: %w", err)
			}

			fmt.Printf("EA keypair generated:\n  SK: %s\n  PK: %s\n", skPath, pkPath)
			return nil
		},
	}
	return cmd
}
