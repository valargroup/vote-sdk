package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

func newSignRoundCmd() *cobra.Command {
	var (
		chainID    string
		signerID   string
		roundID    string
		eaPK       string
		valsetHash string
		privFile   string
		privStdin  bool
		outFile    string
		mergeFile  string
	)
	cmd := &cobra.Command{
		Use:   "sign-round",
		Short: "Sign (round_id, ea_pk, valset_hash) for token-holder-voting-config",
		Long: `Construct the canonical round-manifest payload (see vote-sdk/docs/config.md
§"round_signatures schema"), sign it with the operator's ed25519 key, and
write a complete or partial round_signatures.json.

By default, --output writes a single-signer round_signatures.json. To merge
this signer's signature into an already-signed file (multi-signer rounds), pass
--merge <path>: the existing fields (round_id, ea_pk, valset_hash,
signed_payload_hash) MUST already match what we would compute, and this
signer's entry is appended to signatures[]. A duplicate signer id is replaced.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			args, err := parseRoundArgs(chainID, signerID, roundID, eaPK, valsetHash)
			if err != nil {
				return err
			}

			payload, err := EncodeRoundManifest(RoundManifestPayload{
				ChainID:    args.chainID,
				RoundID:    args.roundID,
				EaPK:       args.eaPK,
				ValsetHash: args.valsetHash,
			})
			if err != nil {
				return err
			}
			digest := PayloadDigest(payload)

			priv, err := loadPrivateKey(privFile, privStdin, os.Stdin)
			if err != nil {
				return err
			}
			sig := ed25519.Sign(priv, payload)

			out, err := buildRoundSignaturesJSON(args, digest, signerID, sig, mergeFile)
			if err != nil {
				return err
			}

			data, err := json.MarshalIndent(out, "", "  ")
			if err != nil {
				return fmt.Errorf("marshal round_signatures: %w", err)
			}
			data = append(data, '\n')

			if outFile == "" || outFile == "-" {
				if _, err := cmd.OutOrStdout().Write(data); err != nil {
					return err
				}
			} else if err := os.WriteFile(outFile, data, 0o644); err != nil {
				return fmt.Errorf("write %q: %w", outFile, err)
			}

			stderr := cmd.ErrOrStderr()
			fmt.Fprintf(stderr, "signed_payload_hash: %s\n", hex.EncodeToString(digest[:]))
			fmt.Fprintf(stderr, "signature:           %s\n", base64.StdEncoding.EncodeToString(sig))
			fmt.Fprintf(stderr, "signer:              %s\n", signerID)
			if outFile != "" && outFile != "-" {
				fmt.Fprintf(stderr, "wrote:               %s\n", outFile)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&chainID, "chain-id", "svote-1", "Cosmos chain id")
	cmd.Flags().StringVar(&signerID, "signer-id", "", "Signer identifier (matches manifest_signers[].id)")
	cmd.Flags().StringVar(&roundID, "round-id", "", "Round id (64-char lowercase hex)")
	cmd.Flags().StringVar(&eaPK, "ea-pk", "", "Election authority pubkey (base64-encoded 32 bytes)")
	cmd.Flags().StringVar(&valsetHash, "valset-hash", "", "CometBFT validators_hash at created_at_height (64-char hex)")
	cmd.Flags().StringVar(&privFile, "privkey-file", "", "Read base64(seed) ed25519 private key from this file")
	cmd.Flags().BoolVar(&privStdin, "privkey-stdin", false, "Read base64(seed) ed25519 private key from stdin")
	cmd.Flags().StringVar(&outFile, "output", "-", "Write resulting round_signatures.json here ('-' for stdout)")
	cmd.Flags().StringVar(&mergeFile, "merge", "", "Append this signer's entry into an existing round_signatures.json (multi-signer rounds)")
	return cmd
}

type roundArgs struct {
	chainID    string
	roundID    []byte
	eaPK       []byte
	valsetHash []byte
}

func parseRoundArgs(chainID, signerID, roundID, eaPK, valsetHash string) (roundArgs, error) {
	if signerID == "" {
		return roundArgs{}, errors.New("--signer-id is required")
	}
	if chainID == "" {
		return roundArgs{}, errors.New("--chain-id is required")
	}
	rid, err := decodeHex(roundID, 32, "round-id")
	if err != nil {
		return roundArgs{}, err
	}
	pk, err := decodeBase64(eaPK, 32, "ea-pk")
	if err != nil {
		return roundArgs{}, err
	}
	vh, err := decodeHex(valsetHash, 32, "valset-hash")
	if err != nil {
		return roundArgs{}, err
	}
	return roundArgs{chainID: chainID, roundID: rid, eaPK: pk, valsetHash: vh}, nil
}

func buildRoundSignaturesJSON(args roundArgs, digest [32]byte, signerID string, sig []byte, mergeFile string) (RoundSignaturesJSON, error) {
	expected := RoundSignaturesJSON{
		RoundID:           hex.EncodeToString(args.roundID),
		EaPK:              base64.StdEncoding.EncodeToString(args.eaPK),
		ValsetHash:        hex.EncodeToString(args.valsetHash),
		SignedPayloadHash: hex.EncodeToString(digest[:]),
	}
	entry := SignatureRef{
		Signer:    signerID,
		Alg:       "ed25519",
		Signature: base64.StdEncoding.EncodeToString(sig),
	}

	if mergeFile == "" {
		expected.Signatures = []SignatureRef{entry}
		return expected, nil
	}

	raw, err := os.ReadFile(mergeFile)
	if err != nil {
		return RoundSignaturesJSON{}, fmt.Errorf("read merge file %q: %w", mergeFile, err)
	}
	var existing RoundSignaturesJSON
	if err := json.Unmarshal(raw, &existing); err != nil {
		return RoundSignaturesJSON{}, fmt.Errorf("parse merge file %q: %w", mergeFile, err)
	}
	if !strings.EqualFold(existing.RoundID, expected.RoundID) {
		return RoundSignaturesJSON{}, fmt.Errorf("merge round_id mismatch: existing=%s, computed=%s", existing.RoundID, expected.RoundID)
	}
	if existing.EaPK != expected.EaPK {
		return RoundSignaturesJSON{}, fmt.Errorf("merge ea_pk mismatch: existing=%s, computed=%s", existing.EaPK, expected.EaPK)
	}
	if !strings.EqualFold(existing.ValsetHash, expected.ValsetHash) {
		return RoundSignaturesJSON{}, fmt.Errorf("merge valset_hash mismatch: existing=%s, computed=%s", existing.ValsetHash, expected.ValsetHash)
	}
	if !strings.EqualFold(existing.SignedPayloadHash, expected.SignedPayloadHash) {
		return RoundSignaturesJSON{}, fmt.Errorf("merge signed_payload_hash mismatch: existing=%s, computed=%s", existing.SignedPayloadHash, expected.SignedPayloadHash)
	}

	merged := existing
	replaced := false
	for i := range merged.Signatures {
		if merged.Signatures[i].Signer == signerID {
			merged.Signatures[i] = entry
			replaced = true
			break
		}
	}
	if !replaced {
		merged.Signatures = append(merged.Signatures, entry)
	}
	return merged, nil
}
