package cmd

import (
	"encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/client/flags"
	"github.com/cosmos/cosmos-sdk/crypto/keyring"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/z-cale/zally/app"
)

// signArbitraryDoc constructs the amino sign doc used by Keplr's signArbitrary
// (ADR-036 format). The data is base64-encoded in the sign doc.
func signArbitraryDoc(signer, data string) []byte {
	doc := map[string]interface{}{
		"account_number": "0",
		"chain_id":       "",
		"fee":            map[string]interface{}{"amount": []interface{}{}, "gas": "0"},
		"memo":           "",
		"msgs": []interface{}{
			map[string]interface{}{
				"type": "sign/MsgSignData",
				"value": map[string]interface{}{
					"data":   base64.StdEncoding.EncodeToString([]byte(data)),
					"signer": signer,
				},
			},
		},
		"sequence": "0",
	}
	// json.Marshal produces sorted keys by default for map[string]interface{},
	// which matches the amino signing convention.
	b, _ := json.Marshal(doc)
	return b
}

// SignArbitraryCmd signs arbitrary data using a keyring key in ADR-036 format.
// Output: JSON with "signature" and "pub_key" (both base64).
func SignArbitraryCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sign-arbitrary <data>",
		Short: "Sign arbitrary data with a keyring key (ADR-036 amino format)",
		Long: `Signs the given data string using the amino signArbitrary format
(ADR-036), compatible with Keplr's signArbitrary and the Vercel edge
function signature verification.

Outputs JSON to stdout:
  { "signature": "base64...", "pub_key": "base64..." }

The data argument is the raw string to sign (e.g. a JSON payload).
It will be base64-encoded inside the amino sign doc before hashing.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			data := args[0]

			homeDir, _ := cmd.Flags().GetString(flags.FlagHome)
			keyName, _ := cmd.Flags().GetString(flags.FlagFrom)
			keyringBackend, _ := cmd.Flags().GetString(flags.FlagKeyringBackend)

			clientCtx, err := client.GetClientQueryContext(cmd)
			if err != nil {
				return fmt.Errorf("getting client context: %w", err)
			}

			kb, err := keyring.New(
				sdk.KeyringServiceName(),
				keyringBackend,
				homeDir,
				cmd.InOrStdin(),
				clientCtx.Codec,
			)
			if err != nil {
				return fmt.Errorf("opening keyring: %w", err)
			}

			// Get the key record to extract the address and public key.
			record, err := kb.Key(keyName)
			if err != nil {
				return fmt.Errorf("key %q not found in keyring: %w", keyName, err)
			}

			addr, err := record.GetAddress()
			if err != nil {
				return fmt.Errorf("getting address: %w", err)
			}

			pubKey, err := record.GetPubKey()
			if err != nil {
				return fmt.Errorf("getting public key: %w", err)
			}

			// Construct the amino sign doc and sign it.
			signBytes := signArbitraryDoc(addr.String(), data)

			sig, _, err := kb.Sign(keyName, signBytes, 0)
			if err != nil {
				return fmt.Errorf("signing: %w", err)
			}

			// Output as JSON.
			out := map[string]string{
				"signature": base64.StdEncoding.EncodeToString(sig),
				"pub_key":   base64.StdEncoding.EncodeToString(pubKey.Bytes()),
			}
			enc, _ := json.Marshal(out)
			fmt.Fprintln(cmd.OutOrStdout(), string(enc))
			return nil
		},
	}

	cmd.Flags().String(flags.FlagHome, app.DefaultNodeHome, "The application home directory")
	cmd.Flags().String(flags.FlagFrom, "validator", "Name of the key in the keyring to sign with")
	cmd.Flags().String(flags.FlagKeyringBackend, keyring.BackendTest, "Keyring backend (os|file|kwallet|pass|test|memory)")
	return cmd
}
