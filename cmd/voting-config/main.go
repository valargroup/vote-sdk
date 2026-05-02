package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/valargroup/vote-sdk/internal/votingconfig"
)

func main() {
	if err := newRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "voting-config",
		Short:         "Sign and verify shielded-vote round configuration",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(newKeygenCmd(), newSignCmd(), newVerifyCmd())
	return root
}

func newKeygenCmd() *cobra.Command {
	var (
		signerID string
		outPath  string
		force    bool
	)
	cmd := &cobra.Command{
		Use:   "keygen",
		Short: "Generate a fresh Ed25519 keypair",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if signerID == "" {
				return errors.New("--signer-id is required")
			}
			if outPath != "" {
				if err := refuseOverwrite(outPath, force); err != nil {
					return err
				}
			}

			pub, priv, err := ed25519.GenerateKey(rand.Reader)
			if err != nil {
				return fmt.Errorf("generate ed25519 key: %w", err)
			}
			seedB64 := base64.StdEncoding.EncodeToString(priv.Seed())
			pubB64 := base64.StdEncoding.EncodeToString(pub)

			if outPath != "" {
				if err := writeFile(outPath, []byte(seedB64+"\n"), 0o600); err != nil {
					return err
				}
			}

			trustedKey := votingconfig.TrustedKey{
				KeyID:  signerID,
				Alg:    votingconfig.AlgEd25519,
				Pubkey: pubB64,
			}
			trustedKeyJSON, err := json.Marshal(trustedKey)
			if err != nil {
				return fmt.Errorf("marshal trusted key: %w", err)
			}

			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "signer_id: %s\n", signerID)
			fmt.Fprintf(out, "public_key_b64: %s\n", pubB64)
			fmt.Fprintf(out, "trusted_keys_entry: %s\n", trustedKeyJSON)
			if outPath != "" {
				fmt.Fprintf(out, "private_key_file: %s\n", outPath)
			} else {
				fmt.Fprintf(out, "private_key_seed_b64: %s\n", seedB64)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&signerID, "signer-id", "", "Signer identifier matching trusted_keys.json[].key_id")
	cmd.Flags().StringVar(&outPath, "out", "", "Write base64(seed) private key to this path instead of stdout")
	cmd.Flags().BoolVar(&force, "force", false, "Overwrite --out if it already exists")
	return cmd
}

func newSignCmd() *cobra.Command {
	var (
		roundID   string
		eaPKB64   string
		signerID  string
		privFile  string
		privStdin bool
		mergePath string
	)
	cmd := &cobra.Command{
		Use:   "sign",
		Short: "Sign one voting-config-v2 round entry",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := votingconfig.ValidateRoundID(roundID); err != nil {
				return err
			}
			if signerID == "" {
				return errors.New("--signer-id is required")
			}
			eaPK, err := parseEaPK(eaPKB64)
			if err != nil {
				return err
			}
			priv, err := loadPrivateKey(privFile, privStdin, os.Stdin)
			if err != nil {
				return err
			}
			sig := votingconfig.SignV1(priv, eaPK)
			entry := votingconfig.RoundEntry{
				AuthVersion: votingconfig.AuthVersionV1,
				EaPK:        base64.StdEncoding.EncodeToString(eaPK[:]),
				Signatures: []votingconfig.Signature{{
					KeyID: signerID,
					Alg:   votingconfig.AlgEd25519,
					Sig:   base64.StdEncoding.EncodeToString(sig),
				}},
			}

			if mergePath != "" {
				if err := mergeEntry(mergePath, roundID, entry); err != nil {
					return err
				}
				fmt.Fprintf(cmd.ErrOrStderr(), "merged: %s\n", mergePath)
			} else {
				data, err := json.MarshalIndent(entry, "", "  ")
				if err != nil {
					return fmt.Errorf("marshal round entry: %w", err)
				}
				if _, err := cmd.OutOrStdout().Write(append(data, '\n')); err != nil {
					return err
				}
			}

			hash := votingconfig.SignedPayloadHash(votingconfig.CanonicalPayloadV1(eaPK))
			fmt.Fprintf(cmd.ErrOrStderr(), "signed_payload_hash: %s\n", hex.EncodeToString(hash[:]))
			return nil
		},
	}
	cmd.Flags().StringVar(&roundID, "round-id", "", "Round id (64 lowercase hex characters)")
	cmd.Flags().StringVar(&eaPKB64, "ea-pk", "", "Election authority public key (base64-encoded 32 bytes)")
	cmd.Flags().StringVar(&signerID, "signer-id", "", "Signer identifier matching trusted_keys.json[].key_id")
	cmd.Flags().StringVar(&privFile, "privkey-file", "", "Read base64(seed) Ed25519 private key from this file")
	cmd.Flags().BoolVar(&privStdin, "privkey-stdin", false, "Read base64(seed) Ed25519 private key from stdin")
	cmd.Flags().StringVar(&mergePath, "merge", "", "Rewrite this dynamic-voting-config.json with the signed entry merged in")
	return cmd
}

func newVerifyCmd() *cobra.Command {
	var (
		configPath       string
		staticConfigPath string
		jsonOut          bool
	)
	cmd := &cobra.Command{
		Use:   "verify",
		Short: "Verify dynamic voting config round signatures",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if configPath == "" {
				return errors.New("--config is required")
			}
			if staticConfigPath == "" {
				return errors.New("--static-config is required")
			}
			cfg, err := readConfig(configPath)
			if err != nil {
				return err
			}
			if err := votingconfig.ValidateWrapper(cfg); err != nil {
				return err
			}
			staticConfig, err := readStaticConfig(staticConfigPath)
			if err != nil {
				return err
			}
			for roundID, entry := range cfg.Rounds {
				if entry.AuthVersion != votingconfig.AuthVersionV1 {
					return fmt.Errorf("round %s: unsupported auth_version %d", roundID, entry.AuthVersion)
				}
				if !votingconfig.VerifyEntrySignatures(entry, staticConfig.TrustedKeys) {
					return fmt.Errorf("round %s: no valid signature", roundID)
				}
			}
			if jsonOut {
				result := struct {
					OK     bool `json:"ok"`
					Rounds int  `json:"rounds"`
				}{OK: true, Rounds: len(cfg.Rounds)}
				data, err := json.MarshalIndent(result, "", "  ")
				if err != nil {
					return fmt.Errorf("marshal verify result: %w", err)
				}
				_, err = cmd.OutOrStdout().Write(append(data, '\n'))
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "OK: verified %d round entries\n", len(cfg.Rounds))
			return nil
		},
	}
	cmd.Flags().StringVar(&configPath, "config", "", "Path to dynamic-voting-config.json")
	cmd.Flags().StringVar(&staticConfigPath, "static-config", "", "Path to static-voting-config.json")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Write machine-readable verification result")
	return cmd
}

func mergeEntry(path, roundID string, entry votingconfig.RoundEntry) error {
	cfg, err := readConfig(path)
	if err != nil {
		return err
	}
	if cfg.Rounds == nil {
		cfg.Rounds = map[string]votingconfig.RoundEntry{}
	}
	existing, ok := cfg.Rounds[roundID]
	if ok {
		if existing.AuthVersion != votingconfig.AuthVersionV1 {
			return fmt.Errorf("round %s: cannot merge into auth_version %d", roundID, existing.AuthVersion)
		}
		if existing.EaPK != entry.EaPK {
			return fmt.Errorf("round %s: ea_pk mismatch in merge target", roundID)
		}
		entry.Signatures = mergeSignatures(existing.Signatures, entry.Signatures[0])
	}
	cfg.Rounds[roundID] = entry

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal merged config: %w", err)
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

func mergeSignatures(existing []votingconfig.Signature, sig votingconfig.Signature) []votingconfig.Signature {
	merged := append([]votingconfig.Signature(nil), existing...)
	for i := range merged {
		if merged[i].KeyID == sig.KeyID {
			merged[i] = sig
			return merged
		}
	}
	return append(merged, sig)
}

func readConfig(path string) (*votingconfig.SignedConfig, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %q: %w", path, err)
	}
	var cfg votingconfig.SignedConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("parse config %q: %w", path, err)
	}
	return &cfg, nil
}

func readStaticConfig(path string) (*votingconfig.StaticConfig, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read static config %q: %w", path, err)
	}
	trimmed := strings.TrimSpace(string(raw))
	if len(trimmed) > 0 && trimmed[0] == '[' {
		return nil, errors.New("input is a flat array; --static-config now requires the static-config object shape (see static-voting-config-sample.json)")
	}
	var cfg votingconfig.StaticConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("parse static config %q: %w", path, err)
	}
	if err := votingconfig.ValidateStaticConfig(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func parseEaPK(eaPKB64 string) ([32]byte, error) {
	var eaPK [32]byte
	raw, err := votingconfig.DecodeBase64Fixed(eaPKB64, 32, "ea_pk")
	if err != nil {
		return eaPK, err
	}
	copy(eaPK[:], raw)
	return eaPK, nil
}

func loadPrivateKey(file string, stdin bool, stdinReader io.Reader) (ed25519.PrivateKey, error) {
	envKey := os.Getenv("VOTING_CONFIG_PRIVKEY")
	sources := 0
	if file != "" {
		sources++
	}
	if envKey != "" {
		sources++
	}
	if stdin {
		sources++
	}
	if sources == 0 {
		return nil, errors.New("no signer key provided: use --privkey-file, VOTING_CONFIG_PRIVKEY, or --privkey-stdin")
	}
	if sources > 1 {
		return nil, errors.New("multiple signer keys provided: pick exactly one of --privkey-file, VOTING_CONFIG_PRIVKEY, --privkey-stdin")
	}

	var seedB64 string
	switch {
	case file != "":
		raw, err := os.ReadFile(file)
		if err != nil {
			return nil, fmt.Errorf("read privkey-file %q: %w", file, err)
		}
		seedB64 = strings.TrimSpace(string(raw))
	case envKey != "":
		seedB64 = strings.TrimSpace(envKey)
	case stdin:
		raw, err := io.ReadAll(stdinReader)
		if err != nil {
			return nil, fmt.Errorf("read privkey from stdin: %w", err)
		}
		seedB64 = strings.TrimSpace(string(raw))
	}

	seed, err := base64.StdEncoding.DecodeString(seedB64)
	if err != nil {
		return nil, fmt.Errorf("decode privkey base64: %w", err)
	}
	if len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("decoded privkey is %d bytes, expected %d", len(seed), ed25519.SeedSize)
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

func refuseOverwrite(path string, force bool) error {
	if _, err := os.Stat(path); err == nil && !force {
		return fmt.Errorf("refusing to overwrite existing file %q (pass --force to allow)", path)
	}
	return nil
}

func writeFile(path string, data []byte, mode os.FileMode) error {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("mkdir %q: %w", dir, err)
		}
	}
	if err := os.WriteFile(path, data, mode); err != nil {
		return fmt.Errorf("write %q: %w", path, err)
	}
	return nil
}
