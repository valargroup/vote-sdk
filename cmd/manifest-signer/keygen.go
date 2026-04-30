package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

func newKeygenCmd() *cobra.Command {
	var (
		signerID string
		outPriv  string
		outPub   string
		force    bool
	)
	cmd := &cobra.Command{
		Use:   "keygen",
		Short: "Generate a fresh ed25519 keypair for a manifest signer",
		Long: `Generate a fresh ed25519 keypair for a new manifest signer.

The private key is written as base64(32-byte seed) — the same format the
sign-* commands expect via --privkey-file. The public key is written as
base64(32 bytes), the same format wallet bundles embed in manifest_signers[].pubkey.

Print SHA-256 fingerprints of both files; communicate the pubkey fingerprint
to wallet maintainers via two independent channels before shipping it in a
release. See vote-sdk/docs/runbooks/key-rotation.md.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if signerID == "" {
				return errors.New("--signer-id is required")
			}
			if outPriv == "" || outPub == "" {
				return errors.New("--out-priv and --out-pub are required")
			}
			if err := refuseOverwrite(outPriv, force); err != nil {
				return err
			}
			if err := refuseOverwrite(outPub, force); err != nil {
				return err
			}

			pub, priv, err := ed25519.GenerateKey(rand.Reader)
			if err != nil {
				return fmt.Errorf("generate ed25519 key: %w", err)
			}
			seed := priv.Seed()
			privB64 := base64.StdEncoding.EncodeToString(seed)
			pubB64 := base64.StdEncoding.EncodeToString(pub)

			if err := writeKeyFile(outPriv, privB64, 0o600); err != nil {
				return err
			}
			if err := writeKeyFile(outPub, pubB64, 0o644); err != nil {
				return err
			}

			privFP := sha256.Sum256([]byte(privB64))
			pubFP := sha256.Sum256(pub)

			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "signer_id:        %s\n", signerID)
			fmt.Fprintf(out, "pubkey:           %s\n", pubB64)
			fmt.Fprintf(out, "pubkey_sha256:    %s\n", hex.EncodeToString(pubFP[:]))
			fmt.Fprintf(out, "privkey_sha256:   %s  (file fingerprint, NOT the key itself)\n", hex.EncodeToString(privFP[:]))
			fmt.Fprintf(out, "out_priv:         %s  (mode 0600)\n", outPriv)
			fmt.Fprintf(out, "out_pub:          %s\n", outPub)
			fmt.Fprintln(out)
			fmt.Fprintln(out, "Communicate `pubkey` and `pubkey_sha256` to wallet maintainers via two")
			fmt.Fprintln(out, "independent channels (e.g. signed email + voice readout). The wallet")
			fmt.Fprintln(out, "bundle's manifest_signers[].pubkey MUST equal the value above byte-for-byte.")
			return nil
		},
	}
	cmd.Flags().StringVar(&signerID, "signer-id", "", "Signer identifier (matches manifest_signers[].id in the wallet bundle)")
	cmd.Flags().StringVar(&outPriv, "out-priv", "", "Path to write the private key (base64 seed, mode 0600)")
	cmd.Flags().StringVar(&outPub, "out-pub", "", "Path to write the public key (base64 32 bytes)")
	cmd.Flags().BoolVar(&force, "force", false, "Overwrite existing files (default: refuse)")
	return cmd
}

func refuseOverwrite(path string, force bool) error {
	if path == "" {
		return nil
	}
	if _, err := os.Stat(path); err == nil && !force {
		return fmt.Errorf("refusing to overwrite existing file %q (pass --force to allow)", path)
	}
	return nil
}

func writeKeyFile(path, content string, mode os.FileMode) error {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("mkdir %q: %w", dir, err)
		}
	}
	if err := os.WriteFile(path, []byte(content+"\n"), mode); err != nil {
		return fmt.Errorf("write %q: %w", path, err)
	}
	return nil
}
