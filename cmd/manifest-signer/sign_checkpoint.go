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
	"time"

	"github.com/spf13/cobra"
)

func newSignCheckpointCmd() *cobra.Command {
	var (
		chainID    string
		signerID   string
		height     uint64
		issuedAt   int64
		headerHash string
		valsetHash string
		appHash    string
		privFile   string
		privStdin  bool
		outFile    string
		mergeFile  string
	)
	cmd := &cobra.Command{
		Use:   "sign-checkpoint",
		Short: "Sign a CometBFT checkpoint for checkpoints/latest.json",
		Long: `Construct the canonical checkpoint payload (see vote-sdk/docs/config.md
§"Signed checkpoint schema"), sign it with the operator's ed25519 key, and
write checkpoints/<height>.json — to be promoted to checkpoints/latest.json by
the publisher script.

--issued-at defaults to now (unix seconds). Use --merge to append this signer
to an existing multi-signer checkpoint file; the existing fields must match
byte-for-byte (chain_id, height, header_hash, valset_hash, app_hash,
issued_at).`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if signerID == "" {
				return errors.New("--signer-id is required")
			}
			if chainID == "" {
				return errors.New("--chain-id is required")
			}
			if height == 0 {
				return errors.New("--height is required and must be > 0")
			}
			hh, err := decodeHex(headerHash, 32, "header-hash")
			if err != nil {
				return err
			}
			vh, err := decodeHex(valsetHash, 32, "valset-hash")
			if err != nil {
				return err
			}
			ah, err := decodeHex(appHash, 32, "app-hash")
			if err != nil {
				return err
			}
			if issuedAt == 0 {
				issuedAt = time.Now().Unix()
			}
			if issuedAt < 0 {
				return fmt.Errorf("--issued-at must be > 0 (got %d)", issuedAt)
			}

			payload, err := EncodeCheckpoint(CheckpointPayload{
				ChainID:    chainID,
				Height:     height,
				HeaderHash: hh,
				ValsetHash: vh,
				AppHash:    ah,
				IssuedAt:   uint64(issuedAt),
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
			entry := SignatureRef{
				Signer:    signerID,
				Alg:       "ed25519",
				Signature: base64.StdEncoding.EncodeToString(sig),
			}

			expected := CheckpointJSON{
				ChainID:    chainID,
				Height:     height,
				HeaderHash: strings.ToLower(headerHash),
				ValsetHash: strings.ToLower(valsetHash),
				AppHash:    strings.ToLower(appHash),
				IssuedAt:   uint64(issuedAt),
			}

			out, err := mergeCheckpoint(expected, entry, mergeFile, signerID)
			if err != nil {
				return err
			}

			data, err := json.MarshalIndent(out, "", "  ")
			if err != nil {
				return fmt.Errorf("marshal checkpoint: %w", err)
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
			fmt.Fprintf(stderr, "signature:           %s\n", entry.Signature)
			fmt.Fprintf(stderr, "signer:              %s\n", signerID)
			if outFile != "" && outFile != "-" {
				fmt.Fprintf(stderr, "wrote:               %s\n", outFile)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&chainID, "chain-id", "svote-1", "Cosmos chain id")
	cmd.Flags().StringVar(&signerID, "signer-id", "", "Signer identifier (matches manifest_signers[].id)")
	cmd.Flags().Uint64Var(&height, "height", 0, "Checkpoint block height")
	cmd.Flags().Int64Var(&issuedAt, "issued-at", 0, "Unix seconds (defaults to now)")
	cmd.Flags().StringVar(&headerHash, "header-hash", "", "Block header hash (64-char hex)")
	cmd.Flags().StringVar(&valsetHash, "valset-hash", "", "Validators hash (64-char hex)")
	cmd.Flags().StringVar(&appHash, "app-hash", "", "App hash (64-char hex)")
	cmd.Flags().StringVar(&privFile, "privkey-file", "", "Read base64(seed) ed25519 private key from this file")
	cmd.Flags().BoolVar(&privStdin, "privkey-stdin", false, "Read base64(seed) ed25519 private key from stdin")
	cmd.Flags().StringVar(&outFile, "output", "-", "Write resulting checkpoint JSON here ('-' for stdout)")
	cmd.Flags().StringVar(&mergeFile, "merge", "", "Append this signer's entry into an existing checkpoint file (multi-signer)")
	return cmd
}

func mergeCheckpoint(expected CheckpointJSON, entry SignatureRef, mergeFile, signerID string) (CheckpointJSON, error) {
	if mergeFile == "" {
		expected.Signatures = []SignatureRef{entry}
		return expected, nil
	}
	raw, err := os.ReadFile(mergeFile)
	if err != nil {
		return CheckpointJSON{}, fmt.Errorf("read merge file %q: %w", mergeFile, err)
	}
	var existing CheckpointJSON
	if err := json.Unmarshal(raw, &existing); err != nil {
		return CheckpointJSON{}, fmt.Errorf("parse merge file %q: %w", mergeFile, err)
	}
	if existing.ChainID != expected.ChainID {
		return CheckpointJSON{}, fmt.Errorf("merge chain_id mismatch: existing=%s, computed=%s", existing.ChainID, expected.ChainID)
	}
	if existing.Height != expected.Height {
		return CheckpointJSON{}, fmt.Errorf("merge height mismatch: existing=%d, computed=%d", existing.Height, expected.Height)
	}
	if !strings.EqualFold(existing.HeaderHash, expected.HeaderHash) {
		return CheckpointJSON{}, fmt.Errorf("merge header_hash mismatch")
	}
	if !strings.EqualFold(existing.ValsetHash, expected.ValsetHash) {
		return CheckpointJSON{}, fmt.Errorf("merge valset_hash mismatch")
	}
	if !strings.EqualFold(existing.AppHash, expected.AppHash) {
		return CheckpointJSON{}, fmt.Errorf("merge app_hash mismatch")
	}
	if existing.IssuedAt != expected.IssuedAt {
		return CheckpointJSON{}, fmt.Errorf("merge issued_at mismatch: existing=%d, computed=%d", existing.IssuedAt, expected.IssuedAt)
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
