package cmd

import (
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/cosmos/cosmos-sdk/client/flags"
	"github.com/valargroup/vote-sdk/app"
	"github.com/valargroup/vote-sdk/crypto/elgamal"
)

// PallasKeygenCmd generates a Pallas keypair for ECIES encryption.
// The secret key is written to <home>/pallas.sk and the public key to <home>/pallas.pk.
func PallasKeygenCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pallas-keygen",
		Short: "Generate Pallas keypair for ECIES (ceremony key distribution)",
		Long: `Generates a Pallas keypair and writes:
  - <home>/pallas.sk  (32-byte secret key)
  - <home>/pallas.pk  (32-byte compressed public key)

The secret key path should be configured in app.toml as vote.pallas_sk_path
so that PrepareProposal can auto-decrypt the EA key share during the ceremony
and inject MsgAckExecutiveAuthorityKey.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			homeDir, _ := cmd.Flags().GetString(flags.FlagHome)
			if homeDir == "" {
				return fmt.Errorf("--home flag is required")
			}

			sk, pk := elgamal.KeyGen(rand.Reader)

			skBytes, err := elgamal.MarshalSecretKey(sk)
			if err != nil {
				return fmt.Errorf("failed to marshal secret key: %w", err)
			}

			pkBytes := pk.Point.ToAffineCompressed()

			skPath := filepath.Join(homeDir, "pallas.sk")
			pkPath := filepath.Join(homeDir, "pallas.pk")

			if err := os.WriteFile(skPath, skBytes, 0600); err != nil {
				return fmt.Errorf("failed to write secret key: %w", err)
			}

			if err := os.WriteFile(pkPath, pkBytes, 0644); err != nil {
				return fmt.Errorf("failed to write public key: %w", err)
			}

			fmt.Printf("Pallas keypair generated:\n  SK: %s\n  PK: %s\n", skPath, pkPath)
			return nil
		},
	}
	cmd.Flags().String(flags.FlagHome, app.DefaultNodeHome, "The application home directory")
	return cmd
}
