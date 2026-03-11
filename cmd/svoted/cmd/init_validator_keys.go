package cmd

import (
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/client/flags"
	"github.com/cosmos/cosmos-sdk/crypto/hd"
	"github.com/cosmos/cosmos-sdk/crypto/keyring"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/valargroup/shielded-vote/app"
	"github.com/valargroup/shielded-vote/crypto/elgamal"
)

// InitValidatorKeysCmd generates all cryptographic keys needed for a new validator
// in a single step: a Cosmos account key, a Pallas keypair, and an EA keypair.
func InitValidatorKeysCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "init-validator-keys",
		Short: "Generate all cryptographic keys needed for a new validator",
		Long: `Generates all cryptographic keys required for joining the network:

  1. Cosmos account key  — stored in the keyring (for signing transactions)
  2. Pallas keypair      — written to <home>/pallas.sk and <home>/pallas.pk
  3. EA keypair          — written to <home>/ea.sk and <home>/ea.pk

This is equivalent to running:
  svoted keys add <key-name> --keyring-backend <backend>
  svoted pallas-keygen
  svoted ea-keygen`,
		RunE: func(cmd *cobra.Command, args []string) error {
			homeDir, _ := cmd.Flags().GetString(flags.FlagHome)
			keyName, _ := cmd.Flags().GetString("key-name")
			keyringBackend, _ := cmd.Flags().GetString(flags.FlagKeyringBackend)

			clientCtx, err := client.GetClientQueryContext(cmd)
			if err != nil {
				return fmt.Errorf("getting client context: %w", err)
			}

			// Step 1: Cosmos account key
			kb, err := keyring.New(
				sdk.KeyringServiceName(),
				keyringBackend,
				homeDir,
				cmd.InOrStdin(),
				clientCtx.Codec,
			)
			if err != nil {
				return fmt.Errorf("creating keyring: %w", err)
			}

			record, mnemonic, err := kb.NewMnemonic(
				keyName,
				keyring.English,
				sdk.FullFundraiserPath,
				keyring.DefaultBIP39Passphrase,
				hd.Secp256k1,
			)
			if err != nil {
				return fmt.Errorf("generating cosmos key %q: %w", keyName, err)
			}

			addr, err := record.GetAddress()
			if err != nil {
				return fmt.Errorf("getting address for %q: %w", keyName, err)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Cosmos account key %q created\n", keyName)
			fmt.Fprintf(cmd.OutOrStdout(), "  Address:  %s\n", addr.String())
			fmt.Fprintf(cmd.OutOrStdout(), "  Mnemonic: %s\n\n", mnemonic)
			fmt.Fprintln(cmd.OutOrStdout(), "  \u26a0\ufe0f  Write down the mnemonic — it cannot be recovered.")

			// Step 2: Pallas keypair
			fmt.Fprintln(cmd.OutOrStdout())
			pallasSK, pallasPK := elgamal.KeyGen(rand.Reader)
			pallasSkBytes, err := elgamal.MarshalSecretKey(pallasSK)
			if err != nil {
				return fmt.Errorf("marshalling Pallas secret key: %w", err)
			}
			pallasPkBytes := pallasPK.Point.ToAffineCompressed()

			pallasSkPath := filepath.Join(homeDir, "pallas.sk")
			pallasPkPath := filepath.Join(homeDir, "pallas.pk")
			if err := os.WriteFile(pallasSkPath, pallasSkBytes, 0600); err != nil {
				return fmt.Errorf("writing Pallas secret key: %w", err)
			}
			if err := os.WriteFile(pallasPkPath, pallasPkBytes, 0644); err != nil {
				return fmt.Errorf("writing Pallas public key: %w", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Pallas keypair generated\n  SK: %s\n  PK: %s\n", pallasSkPath, pallasPkPath)

			// Step 3: EA keypair
			fmt.Fprintln(cmd.OutOrStdout())
			eaSK, eaPK := elgamal.KeyGen(rand.Reader)
			eaSkBytes, err := elgamal.MarshalSecretKey(eaSK)
			if err != nil {
				return fmt.Errorf("marshalling EA secret key: %w", err)
			}
			eaPkBytes := eaPK.Point.ToAffineCompressed()

			eaSkPath := filepath.Join(homeDir, "ea.sk")
			eaPkPath := filepath.Join(homeDir, "ea.pk")
			if err := os.WriteFile(eaSkPath, eaSkBytes, 0600); err != nil {
				return fmt.Errorf("writing EA secret key: %w", err)
			}
			if err := os.WriteFile(eaPkPath, eaPkBytes, 0644); err != nil {
				return fmt.Errorf("writing EA public key: %w", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "EA keypair generated\n  SK: %s\n  PK: %s\n", eaSkPath, eaPkPath)

			return nil
		},
	}

	cmd.Flags().String(flags.FlagHome, app.DefaultNodeHome, "The application home directory")
	cmd.Flags().String("key-name", "validator", "Name for the Cosmos account key in the keyring")
	cmd.Flags().String(flags.FlagKeyringBackend, keyring.BackendTest, "Keyring backend (os|file|kwallet|pass|test|memory)")
	return cmd
}
