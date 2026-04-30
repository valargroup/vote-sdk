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

func newVerifyCmd() *cobra.Command {
	var (
		chainID    string
		input      string
		pubFile    string
		signerID   string
		roundID    string
		eaPK       string
		valsetHash string
		// checkpoint mode flags (mutually exclusive with round flags)
		ckpt       bool
		height     uint64
		issuedAt   int64
		headerHash string
		appHash    string
		kRequired  int
	)
	cmd := &cobra.Command{
		Use:   "verify",
		Short: "Verify a signed round_signatures.json or checkpoint JSON",
		Long: `Verify the ed25519 signature(s) on a round_signatures.json (default) or
checkpoint JSON (--checkpoint). The trusted pubkey must be supplied via
--pubkey-file (single signer) and the expected signer id via --signer-id.

For multi-signer files, run --signer-id once per signer (looping through the
distinct entries) and consider the file accepted only if every required signer
verifies. The --k-required flag treats the verification as passing if at least
that many distinct signers in the file's signatures[] match the supplied
pubkey/signer-id combo (degenerate for single-signer mode).

Exit code is 0 on success, non-zero on any verification failure.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if input == "" {
				return errors.New("--input is required")
			}
			if pubFile == "" {
				return errors.New("--pubkey-file is required")
			}
			if signerID == "" {
				return errors.New("--signer-id is required")
			}
			pub, err := loadPublicKey(pubFile)
			if err != nil {
				return err
			}

			raw, err := os.ReadFile(input)
			if err != nil {
				return fmt.Errorf("read input %q: %w", input, err)
			}

			var (
				payload   []byte
				digest    [32]byte
				sigsArray []SignatureRef
			)
			if ckpt {
				p, err := buildCheckpointPayload(raw, chainID, height, headerHash, valsetHash, appHash, issuedAt)
				if err != nil {
					return err
				}
				payload = p.payload
				digest = p.digest
				sigsArray = p.sigs
			} else {
				p, err := buildRoundPayload(raw, chainID, roundID, eaPK, valsetHash)
				if err != nil {
					return err
				}
				payload = p.payload
				digest = p.digest
				sigsArray = p.sigs
			}

			matched := 0
			seen := map[string]bool{}
			for _, sig := range sigsArray {
				if sig.Signer != signerID {
					continue
				}
				if seen[sig.Signer] {
					return fmt.Errorf("duplicate signer entry for id %q", sig.Signer)
				}
				seen[sig.Signer] = true
				if sig.Alg != "ed25519" {
					return fmt.Errorf("unsupported alg %q for signer %q", sig.Alg, sig.Signer)
				}
				sigBytes, err := base64.StdEncoding.DecodeString(sig.Signature)
				if err != nil {
					return fmt.Errorf("decode signature for %q: %w", sig.Signer, err)
				}
				if len(sigBytes) != ed25519.SignatureSize {
					return fmt.Errorf("signature for %q is %d bytes, expected %d", sig.Signer, len(sigBytes), ed25519.SignatureSize)
				}
				if !ed25519.Verify(pub, payload, sigBytes) {
					return fmt.Errorf("signature for %q does NOT verify against pubkey", sig.Signer)
				}
				matched++
			}
			if matched < kRequired {
				return fmt.Errorf("only %d of %d required signatures verified for signer-id=%q", matched, kRequired, signerID)
			}

			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "OK\n")
			fmt.Fprintf(out, "input:              %s\n", input)
			fmt.Fprintf(out, "signed_payload_hash: %s\n", hex.EncodeToString(digest[:]))
			fmt.Fprintf(out, "signer_id:          %s\n", signerID)
			fmt.Fprintf(out, "verified:           %d signature(s) over %d-byte canonical payload\n", matched, len(payload))
			return nil
		},
	}
	cmd.Flags().StringVar(&chainID, "chain-id", "svote-1", "Cosmos chain id")
	cmd.Flags().StringVar(&input, "input", "", "Path to round_signatures.json (or checkpoint with --checkpoint)")
	cmd.Flags().StringVar(&pubFile, "pubkey-file", "", "Path to base64(32-byte) ed25519 pubkey")
	cmd.Flags().StringVar(&signerID, "signer-id", "", "Verify entries with this signer id")
	cmd.Flags().IntVar(&kRequired, "k-required", 1, "Minimum number of valid signatures matching --signer-id")

	cmd.Flags().StringVar(&roundID, "round-id", "", "Expected round id (64-char hex) — required without --checkpoint")
	cmd.Flags().StringVar(&eaPK, "ea-pk", "", "Expected ea_pk (base64 32 bytes) — required without --checkpoint")
	cmd.Flags().StringVar(&valsetHash, "valset-hash", "", "Expected valset_hash (64-char hex)")

	cmd.Flags().BoolVar(&ckpt, "checkpoint", false, "Verify a checkpoint JSON instead of a round_signatures.json")
	cmd.Flags().Uint64Var(&height, "height", 0, "Expected checkpoint height (with --checkpoint)")
	cmd.Flags().Int64Var(&issuedAt, "issued-at", 0, "Expected issued_at (with --checkpoint; 0 = trust file)")
	cmd.Flags().StringVar(&headerHash, "header-hash", "", "Expected header_hash hex (with --checkpoint)")
	cmd.Flags().StringVar(&appHash, "app-hash", "", "Expected app_hash hex (with --checkpoint)")
	return cmd
}

type verifyPayload struct {
	payload []byte
	digest  [32]byte
	sigs    []SignatureRef
}

func buildRoundPayload(raw []byte, chainID, expRoundID, expEaPK, expValsetHash string) (verifyPayload, error) {
	var doc RoundSignaturesJSON
	if err := json.Unmarshal(raw, &doc); err != nil {
		return verifyPayload{}, fmt.Errorf("parse round_signatures.json: %w", err)
	}

	if expRoundID != "" && !strings.EqualFold(doc.RoundID, expRoundID) {
		return verifyPayload{}, fmt.Errorf("round_id mismatch: file=%s, expected=%s", doc.RoundID, expRoundID)
	}
	if expEaPK != "" && doc.EaPK != expEaPK {
		return verifyPayload{}, fmt.Errorf("ea_pk mismatch: file=%s, expected=%s", doc.EaPK, expEaPK)
	}
	if expValsetHash != "" && !strings.EqualFold(doc.ValsetHash, expValsetHash) {
		return verifyPayload{}, fmt.Errorf("valset_hash mismatch: file=%s, expected=%s", doc.ValsetHash, expValsetHash)
	}

	rid, err := decodeHex(doc.RoundID, 32, "file.round_id")
	if err != nil {
		return verifyPayload{}, err
	}
	pk, err := decodeBase64(doc.EaPK, 32, "file.ea_pk")
	if err != nil {
		return verifyPayload{}, err
	}
	vh, err := decodeHex(doc.ValsetHash, 32, "file.valset_hash")
	if err != nil {
		return verifyPayload{}, err
	}
	payload, err := EncodeRoundManifest(RoundManifestPayload{
		ChainID:    chainID,
		RoundID:    rid,
		EaPK:       pk,
		ValsetHash: vh,
	})
	if err != nil {
		return verifyPayload{}, err
	}
	digest := PayloadDigest(payload)

	if doc.SignedPayloadHash != "" && !strings.EqualFold(doc.SignedPayloadHash, hex.EncodeToString(digest[:])) {
		return verifyPayload{}, fmt.Errorf("signed_payload_hash mismatch: file=%s, computed=%s", doc.SignedPayloadHash, hex.EncodeToString(digest[:]))
	}
	return verifyPayload{payload: payload, digest: digest, sigs: doc.Signatures}, nil
}

func buildCheckpointPayload(raw []byte, chainID string, expHeight uint64, expHeader, expValset, expApp string, expIssued int64) (verifyPayload, error) {
	var doc CheckpointJSON
	if err := json.Unmarshal(raw, &doc); err != nil {
		return verifyPayload{}, fmt.Errorf("parse checkpoint.json: %w", err)
	}
	if doc.ChainID != chainID {
		return verifyPayload{}, fmt.Errorf("chain_id mismatch: file=%s, expected=%s", doc.ChainID, chainID)
	}
	if expHeight != 0 && doc.Height != expHeight {
		return verifyPayload{}, fmt.Errorf("height mismatch: file=%d, expected=%d", doc.Height, expHeight)
	}
	if expHeader != "" && !strings.EqualFold(doc.HeaderHash, expHeader) {
		return verifyPayload{}, fmt.Errorf("header_hash mismatch")
	}
	if expValset != "" && !strings.EqualFold(doc.ValsetHash, expValset) {
		return verifyPayload{}, fmt.Errorf("valset_hash mismatch")
	}
	if expApp != "" && !strings.EqualFold(doc.AppHash, expApp) {
		return verifyPayload{}, fmt.Errorf("app_hash mismatch")
	}
	if expIssued != 0 && doc.IssuedAt != uint64(expIssued) {
		return verifyPayload{}, fmt.Errorf("issued_at mismatch: file=%d, expected=%d", doc.IssuedAt, expIssued)
	}

	hh, err := decodeHex(doc.HeaderHash, 32, "file.header_hash")
	if err != nil {
		return verifyPayload{}, err
	}
	vh, err := decodeHex(doc.ValsetHash, 32, "file.valset_hash")
	if err != nil {
		return verifyPayload{}, err
	}
	ah, err := decodeHex(doc.AppHash, 32, "file.app_hash")
	if err != nil {
		return verifyPayload{}, err
	}
	payload, err := EncodeCheckpoint(CheckpointPayload{
		ChainID:    chainID,
		Height:     doc.Height,
		HeaderHash: hh,
		ValsetHash: vh,
		AppHash:    ah,
		IssuedAt:   doc.IssuedAt,
	})
	if err != nil {
		return verifyPayload{}, err
	}
	return verifyPayload{payload: payload, digest: PayloadDigest(payload), sigs: doc.Signatures}, nil
}
