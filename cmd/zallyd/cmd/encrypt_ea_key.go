package cmd

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/spf13/cobra"

	"github.com/z-cale/zally/crypto/ecies"
	"github.com/z-cale/zally/crypto/elgamal"
)

const defaultRESTAddr = "http://localhost:1318"

// EncryptEAKeyCmd produces a payloads.json for use with "zallyd tx vote deal-ea-key".
// It reads ea.sk from disk, fetches all registered validators and their Pallas
// public keys from the chain's ceremony state, and ECIES-encrypts the secret key
// for each validator.
func EncryptEAKeyCmd() *cobra.Command {
	var (
		nodeAddr string
		outPath  string
	)

	cmd := &cobra.Command{
		Use:   "encrypt-ea-key <ea-sk-path>",
		Short: "Encrypt the EA secret key for all registered validators",
		Long: `Reads the Election Authority secret key, queries the running chain for all
validators that have registered a Pallas public key, and ECIES-encrypts the
secret key for each one.

The resulting JSON array is written to --output (default: payloads.json) and
can be passed directly to "zallyd tx vote deal-ea-key".

Example:
  zallyd encrypt-ea-key ~/.zallyd/ea.sk \
    --node http://localhost:1318 \
    --output /tmp/payloads.json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			eaSkBytes, err := os.ReadFile(args[0])
			if err != nil {
				return fmt.Errorf("reading ea.sk: %w", err)
			}
			if len(eaSkBytes) == 0 {
				return fmt.Errorf("ea.sk is empty")
			}

			validators, err := fetchRegisteredValidators(nodeAddr)
			if err != nil {
				return fmt.Errorf("fetching ceremony state from %s: %w", nodeAddr, err)
			}
			if len(validators) == 0 {
				return fmt.Errorf("no validators have registered a Pallas key yet")
			}

			type payload struct {
				ValidatorAddress string `json:"validator_address"`
				EphemeralPk      string `json:"ephemeral_pk"`
				Ciphertext       string `json:"ciphertext"`
			}
			payloads := make([]payload, 0, len(validators))

			G := elgamal.PallasGenerator()
			for _, v := range validators {
				pk, err := elgamal.UnmarshalPublicKey(v.PallasPk)
				if err != nil {
					return fmt.Errorf("validator %s: invalid Pallas PK: %w", v.ValidatorAddress, err)
				}

				env, err := ecies.Encrypt(G, pk.Point, eaSkBytes, rand.Reader)
				if err != nil {
					return fmt.Errorf("validator %s: ECIES encrypt: %w", v.ValidatorAddress, err)
				}

				payloads = append(payloads, payload{
					ValidatorAddress: v.ValidatorAddress,
					EphemeralPk:      hex.EncodeToString(env.Ephemeral.ToAffineCompressed()),
					Ciphertext:       hex.EncodeToString(env.Ciphertext),
				})
			}

			out, err := json.MarshalIndent(payloads, "", "  ")
			if err != nil {
				return fmt.Errorf("marshalling payloads: %w", err)
			}

			if outPath == "-" {
				fmt.Println(string(out))
				return nil
			}

			if err := os.WriteFile(outPath, out, 0644); err != nil {
				return fmt.Errorf("writing %s: %w", outPath, err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Wrote %d payload(s) to %s\n", len(payloads), outPath)
			return nil
		},
	}

	cmd.Flags().StringVar(&nodeAddr, "node", defaultRESTAddr, "REST endpoint of the running zallyd node")
	cmd.Flags().StringVarP(&outPath, "output", "o", "payloads.json", `Output file path (use "-" for stdout)`)

	return cmd
}

// validatorPallasEntry holds a registered validator's address and decoded
// Pallas public key bytes, derived from the /zally/v1/ceremony REST response.
type validatorPallasEntry struct {
	ValidatorAddress string
	PallasPk         []byte
}

// fetchRegisteredValidators GETs the ceremony state and returns all validators
// that have registered a Pallas public key.
func fetchRegisteredValidators(restAddr string) ([]validatorPallasEntry, error) {
	url := restAddr + "/zally/v1/ceremony"

	resp, err := http.Get(url) //nolint:noctx // simple CLI call
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", url, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, body)
	}

	// The REST handler serialises with proto-JSON. The top-level wrapper is:
	// { "ceremony": { "validators": [ { "validator_address": "...", "pallas_pk": "<base64>" } ] } }
	var wrapper struct {
		Ceremony struct {
			Validators []struct {
				ValidatorAddress string `json:"validator_address"`
				PallasPk         string `json:"pallas_pk"`
			} `json:"validators"`
		} `json:"ceremony"`
	}
	if err := json.Unmarshal(body, &wrapper); err != nil {
		return nil, fmt.Errorf("parsing ceremony JSON: %w", err)
	}

	entries := make([]validatorPallasEntry, 0, len(wrapper.Ceremony.Validators))
	for _, v := range wrapper.Ceremony.Validators {
		pkBytes, err := base64.StdEncoding.DecodeString(v.PallasPk)
		if err != nil {
			// proto-JSON may use standard or URL-safe base64; try URL-safe too.
			pkBytes, err = base64.RawStdEncoding.DecodeString(v.PallasPk)
			if err != nil {
				return nil, fmt.Errorf("validator %s: decoding pallas_pk: %w", v.ValidatorAddress, err)
			}
		}
		entries = append(entries, validatorPallasEntry{
			ValidatorAddress: v.ValidatorAddress,
			PallasPk:         pkBytes,
		})
	}
	return entries, nil
}
