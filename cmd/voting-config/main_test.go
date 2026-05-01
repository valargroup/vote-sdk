package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/valargroup/vote-sdk/internal/votingconfig"
)

func TestSignAndVerifyConfig(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	var eaPK [32]byte
	for i := range eaPK {
		eaPK[i] = byte(i)
	}
	roundID := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	dir := t.TempDir()
	configPath := filepath.Join(dir, "voting-config-v2.json")
	keysPath := filepath.Join(dir, "admin-keys.json")
	writeJSON(t, configPath, votingconfig.SignedConfig{
		ConfigVersion: votingconfig.ConfigVersionV1,
		VoteServers:   []votingconfig.Endpoint{{URL: "https://vote.example", Label: "vote"}},
		PIREndpoints:  []votingconfig.Endpoint{{URL: "https://pir.example", Label: "pir"}},
		SupportedVersions: votingconfig.SupportedVersions{
			PIR:          []string{"v0"},
			VoteProtocol: "v0",
			Tally:        "v0",
			VoteServer:   "v1",
		},
		Rounds: map[string]votingconfig.RoundEntry{},
	})
	writeJSON(t, keysPath, votingconfig.TrustedKeysFile{
		TrustedKeys: []votingconfig.TrustedKey{{
			KeyID:  "key-1",
			Alg:    votingconfig.AlgEd25519,
			Pubkey: base64.StdEncoding.EncodeToString(pub),
		}},
	})

	t.Setenv("VOTING_CONFIG_PRIVKEY", base64.StdEncoding.EncodeToString(priv.Seed()))
	signCmd := newRootCmd()
	signCmd.SetArgs([]string{
		"sign",
		"--round-id", roundID,
		"--ea-pk", base64.StdEncoding.EncodeToString(eaPK[:]),
		"--signer-id", "key-1",
		"--merge", configPath,
	})
	if err := signCmd.Execute(); err != nil {
		t.Fatalf("sign command failed: %v", err)
	}

	verifyCmd := newRootCmd()
	var out bytes.Buffer
	verifyCmd.SetOut(&out)
	verifyCmd.SetArgs([]string{"verify", "--config", configPath, "--keys", keysPath})
	if err := verifyCmd.Execute(); err != nil {
		t.Fatalf("verify command failed: %v", err)
	}
	if !strings.Contains(out.String(), "OK: verified 1 round entries") {
		t.Fatalf("unexpected verify output: %q", out.String())
	}
}

func TestKeygenWritesSeedFile(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "key.seed")
	cmd := newRootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"keygen", "--signer-id", "key-1", "--out", keyPath})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("keygen failed: %v", err)
	}
	raw, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("read key file: %v", err)
	}
	seed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatalf("decode seed: %v", err)
	}
	if len(seed) != ed25519.SeedSize {
		t.Fatalf("seed length = %d, want %d", len(seed), ed25519.SeedSize)
	}
	if !strings.Contains(out.String(), `"key_id":"key-1"`) {
		t.Fatalf("expected admin key JSON in output, got %q", out.String())
	}
}

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatalf("marshal %s: %v", path, err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
