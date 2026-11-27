package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/valargroup/vote-sdk/internal/votingconfig"
	votetypes "github.com/valargroup/vote-sdk/x/vote/types"
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
	configPath := filepath.Join(dir, "dynamic-voting-config.json")
	staticConfigPath := filepath.Join(dir, "static-voting-config-sample.json")
	writeJSON(t, configPath, votingconfig.SignedConfig{
		ConfigVersion: votingconfig.ConfigVersionV1,
		VoteServers:   []votingconfig.Endpoint{{URL: "https://vote.example", Label: "vote"}},
		PIREndpoints:  []votingconfig.Endpoint{{URL: "https://pir.example", Label: "pir"}},
		PIRLayout:     testVotingConfigPIRLayout(),
		SupportedVersions: votingconfig.SupportedVersions{
			PIR:          []string{"v0"},
			VoteProtocol: "v0",
			Tally:        "v0",
			VoteServer:   "v1",
		},
		Rounds: map[string]votingconfig.RoundEntry{},
	})
	writeJSON(t, staticConfigPath, votingconfig.StaticConfig{
		StaticConfigVersion: votingconfig.StaticConfigVersionV1,
		DynamicConfigURL:    "https://example.com/dynamic-voting-config.json",
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
		"--pir-depth", "19",
		"--tier0-layers", "12",
		"--tier1-layers", "7",
		"--poly-len", "4096",
		"--merge", configPath,
	})
	if err := signCmd.Execute(); err != nil {
		t.Fatalf("sign command failed: %v", err)
	}

	verifyCmd := newRootCmd()
	var out bytes.Buffer
	verifyCmd.SetOut(&out)
	verifyCmd.SetArgs([]string{"verify", "--config", configPath, "--static-config", staticConfigPath})
	if err := verifyCmd.Execute(); err != nil {
		t.Fatalf("verify command failed: %v", err)
	}
	if !strings.Contains(out.String(), "OK: verified 1 round entries") {
		t.Fatalf("unexpected verify output: %q", out.String())
	}
}

func TestVerifyRejectsLegacyTrustedKeysArray(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "dynamic-voting-config.json")
	staticConfigPath := filepath.Join(dir, "trusted_keys.json")
	writeJSON(t, configPath, votingconfig.SignedConfig{
		ConfigVersion: votingconfig.ConfigVersionV1,
		VoteServers:   []votingconfig.Endpoint{{URL: "https://vote.example", Label: "vote"}},
		PIREndpoints:  []votingconfig.Endpoint{{URL: "https://pir.example", Label: "pir"}},
		PIRLayout:     testVotingConfigPIRLayout(),
		Rounds:        map[string]votingconfig.RoundEntry{},
	})
	writeJSON(t, staticConfigPath, []votingconfig.TrustedKey{
		{KeyID: "key-1", Alg: votingconfig.AlgEd25519, Pubkey: base64.StdEncoding.EncodeToString(make([]byte, ed25519.PublicKeySize))},
	})

	cmd := newRootCmd()
	cmd.SetArgs([]string{"verify", "--config", configPath, "--static-config", staticConfigPath})
	err := cmd.Execute()
	if err == nil {
		t.Fatalf("expected legacy trusted_keys array to be rejected")
	}
	if !strings.Contains(err.Error(), "flat array") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func testVotingConfigPIRLayout() votingconfig.PIRLayout {
	return votingconfig.PIRLayout{PIRDepth: 19, Tier0Layers: 12, Tier1Layers: 7, PolyLen: votingconfig.PolyLen4096}
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
		t.Fatalf("expected trusted key JSON in output, got %q", out.String())
	}
}

func TestConfigAttestationKeygenMatchesParityVector(t *testing.T) {
	const mnemonic = "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon art"

	t.Setenv("VOTING_CONFIG_MNEMONIC", mnemonic)
	cmd := newRootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"config-attestation-keygen", "--chain-id", "shielded-vote-1"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("config-attestation-keygen failed: %v", err)
	}
	output := out.String()
	for _, want := range []string{
		"derived_address: sv1r5v5srda7xfth3hn2s26txvrcrntldju3n5lv5",
		"signer_id: keplr:sv1r5v5srda7xfth3hn2s26txvrcrntldju3n5lv5",
		"public_key_b64: L9mWw81NjPpERmbT7qOhg7PrioccEQgghUntIsEEZV0=",
		`"key_id":"keplr:sv1r5v5srda7xfth3hn2s26txvrcrntldju3n5lv5"`,
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("config-attestation-keygen output missing %q:\n%s", want, output)
		}
	}
}

func TestConfigAttestationKeygenRequiresChainID(t *testing.T) {
	t.Setenv("VOTING_CONFIG_MNEMONIC", "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon art")
	cmd := newRootCmd()
	cmd.SetArgs([]string{"config-attestation-keygen"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "--chain-id is required") {
		t.Fatalf("expected missing chain-id error, got %v", err)
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

func TestSignWithRoundFileAttestsBallotAndVerifies(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	var eaPK [32]byte
	for i := range eaPK {
		eaPK[i] = 7
	}
	roundID := strings.Repeat("01", 32)
	dir := t.TempDir()
	configPath, staticConfigPath := writeTestConfigs(t, dir, pub)
	roundPath := filepath.Join(dir, "round.json")
	writeServedRound(t, roundPath, roundID, eaPK, "Support")

	t.Setenv("VOTING_CONFIG_PRIVKEY", base64.StdEncoding.EncodeToString(priv.Seed()))
	var stderr bytes.Buffer
	signCmd := newRootCmd()
	signCmd.SetErr(&stderr)
	signCmd.SetArgs(signArgs(roundID, eaPK, "--round-file", roundPath, "--merge", configPath))
	if err := signCmd.Execute(); err != nil {
		t.Fatalf("sign command failed: %v", err)
	}
	for _, want := range []string{"Support", "Oppose", "ballot_digest:"} {
		if !strings.Contains(stderr.String(), want) {
			t.Fatalf("sign output must show the ballot for review; missing %q in:\n%s", want, stderr.String())
		}
	}

	cfg := readTestConfig(t, configPath)
	attestation := cfg.Rounds[roundID].BallotAttestation
	if attestation == nil || len(attestation.Signatures) != 1 {
		t.Fatalf("expected a signed ballot_attestation, got %+v", attestation)
	}

	verifyCmd := newRootCmd()
	var out bytes.Buffer
	verifyCmd.SetOut(&out)
	verifyCmd.SetArgs([]string{"verify", "--config", configPath, "--static-config", staticConfigPath})
	if err := verifyCmd.Execute(); err != nil {
		t.Fatalf("verify command failed: %v", err)
	}
	if !strings.Contains(out.String(), "1 with ballot attestation") {
		t.Fatalf("unexpected verify output: %q", out.String())
	}

	// Re-signing the v2 entry without a round file keeps the attestation.
	resign := newRootCmd()
	resign.SetErr(&bytes.Buffer{})
	resign.SetArgs(signArgs(roundID, eaPK, "--merge", configPath))
	if err := resign.Execute(); err != nil {
		t.Fatalf("re-sign failed: %v", err)
	}
	if readTestConfig(t, configPath).Rounds[roundID].BallotAttestation == nil {
		t.Fatalf("merging a v2-only signature dropped the ballot attestation")
	}
}

func TestSignRejectsRoundFileForAnotherRound(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(nil)
	var eaPK [32]byte
	dir := t.TempDir()
	roundPath := filepath.Join(dir, "round.json")
	writeServedRound(t, roundPath, strings.Repeat("02", 32), eaPK, "Support")

	t.Setenv("VOTING_CONFIG_PRIVKEY", base64.StdEncoding.EncodeToString(priv.Seed()))
	cmd := newRootCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs(signArgs(strings.Repeat("01", 32), eaPK, "--round-file", roundPath))
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "vote_round_id") {
		t.Fatalf("expected a round id mismatch error, got %v", err)
	}
}

func TestSignRefusesToMergeADifferentBallot(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	var eaPK [32]byte
	roundID := strings.Repeat("01", 32)
	dir := t.TempDir()
	configPath, _ := writeTestConfigs(t, dir, pub)
	roundPath := filepath.Join(dir, "round.json")

	t.Setenv("VOTING_CONFIG_PRIVKEY", base64.StdEncoding.EncodeToString(priv.Seed()))
	writeServedRound(t, roundPath, roundID, eaPK, "Support")
	first := newRootCmd()
	first.SetErr(&bytes.Buffer{})
	first.SetArgs(signArgs(roundID, eaPK, "--round-file", roundPath, "--merge", configPath))
	if err := first.Execute(); err != nil {
		t.Fatalf("first sign failed: %v", err)
	}

	writeServedRound(t, roundPath, roundID, eaPK, "Endorse")
	second := newRootCmd()
	second.SetErr(&bytes.Buffer{})
	second.SetArgs(signArgs(roundID, eaPK, "--round-file", roundPath, "--merge", configPath))
	err := second.Execute()
	if err == nil || !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("expected a digest mismatch, got %v", err)
	}
}

func TestVerifyRejectsAnUnverifiableBallotAttestation(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	var eaPK [32]byte
	roundID := strings.Repeat("01", 32)
	dir := t.TempDir()
	configPath, staticConfigPath := writeTestConfigs(t, dir, pub)
	roundPath := filepath.Join(dir, "round.json")
	writeServedRound(t, roundPath, roundID, eaPK, "Support")

	t.Setenv("VOTING_CONFIG_PRIVKEY", base64.StdEncoding.EncodeToString(priv.Seed()))
	signCmd := newRootCmd()
	signCmd.SetErr(&bytes.Buffer{})
	signCmd.SetArgs(signArgs(roundID, eaPK, "--round-file", roundPath, "--merge", configPath))
	if err := signCmd.Execute(); err != nil {
		t.Fatalf("sign failed: %v", err)
	}
	cfg := readTestConfig(t, configPath)
	entry := cfg.Rounds[roundID]
	entry.BallotAttestation.BallotDigest = strings.Repeat("0", 64)
	cfg.Rounds[roundID] = entry
	writeJSON(t, configPath, cfg)

	verifyCmd := newRootCmd()
	verifyCmd.SetArgs([]string{"verify", "--config", configPath, "--static-config", staticConfigPath})
	err := verifyCmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "ballot_attestation") {
		t.Fatalf("expected ballot attestation verification failure, got %v", err)
	}
}

func signArgs(roundID string, eaPK [32]byte, extra ...string) []string {
	return append([]string{
		"sign",
		"--round-id", roundID,
		"--ea-pk", base64.StdEncoding.EncodeToString(eaPK[:]),
		"--signer-id", "key-1",
		"--pir-depth", "19",
		"--tier0-layers", "12",
		"--tier1-layers", "7",
		"--poly-len", "4096",
	}, extra...)
}

func writeTestConfigs(t *testing.T, dir string, pub ed25519.PublicKey) (string, string) {
	t.Helper()
	configPath := filepath.Join(dir, "dynamic-voting-config.json")
	staticConfigPath := filepath.Join(dir, "static-voting-config-sample.json")
	writeJSON(t, configPath, votingconfig.SignedConfig{
		ConfigVersion: votingconfig.ConfigVersionV1,
		VoteServers:   []votingconfig.Endpoint{{URL: "https://vote.example", Label: "vote"}},
		PIREndpoints:  []votingconfig.Endpoint{{URL: "https://pir.example", Label: "pir"}},
		PIRLayout:     testVotingConfigPIRLayout(),
		SupportedVersions: votingconfig.SupportedVersions{
			PIR: []string{"v0"}, VoteProtocol: "v0", Tally: "v0", VoteServer: "v1",
		},
		Rounds: map[string]votingconfig.RoundEntry{},
	})
	writeJSON(t, staticConfigPath, votingconfig.StaticConfig{
		StaticConfigVersion: votingconfig.StaticConfigVersionV1,
		DynamicConfigURL:    "https://example.com/dynamic-voting-config.json",
		TrustedKeys: []votingconfig.TrustedKey{{
			KeyID: "key-1", Alg: votingconfig.AlgEd25519, Pubkey: base64.StdEncoding.EncodeToString(pub),
		}},
	})
	return configPath, staticConfigPath
}

// writeServedRound writes a round in the vote server's REST shape.
func writeServedRound(t *testing.T, path, roundID string, eaPK [32]byte, firstLabel string) {
	t.Helper()
	roundIDBytes, err := hex.DecodeString(roundID)
	if err != nil {
		t.Fatalf("decode round id: %v", err)
	}
	writeJSON(t, path, map[string]any{"round": map[string]any{
		"vote_round_id": base64.StdEncoding.EncodeToString(roundIDBytes),
		"ea_pk":         base64.StdEncoding.EncodeToString(eaPK[:]),
		"title":         "Round 1",
		"proposals": []any{map[string]any{
			"id":    1,
			"title": "Question",
			"options": []any{
				map[string]any{"label": firstLabel},
				map[string]any{"index": 1, "label": "Oppose"},
			},
		}},
	}})
}

func readTestConfig(t *testing.T, path string) votingconfig.SignedConfig {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	var cfg votingconfig.SignedConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("parse config: %v", err)
	}
	return cfg
}

// The vote server writes the proto response with encoding/json; parse that
// exact output rather than a hand-written approximation of it.
func TestParseServedRoundReadsTheVoteServersResponseType(t *testing.T) {
	roundIDBytes := bytes.Repeat([]byte{1}, 32)
	eaPK := bytes.Repeat([]byte{7}, 32)
	raw, err := json.Marshal(&votetypes.QueryVoteRoundResponse{Round: &votetypes.VoteRound{
		VoteRoundId:    roundIDBytes,
		EaPk:           eaPK,
		Title:          "Round 1",
		DiscussionUrl:  "https://forum.example.org/t/1",
		SnapshotHeight: 3000000,
		Proposals: []*votetypes.Proposal{{
			Id: 1, Title: "Adopt ZIP-1", ZipNumber: "ZIP-1", ForumUrl: "https://forum.example.org/t/2",
			Options: []*votetypes.VoteOption{{Index: 0, Label: "Support", Description: "In favor"}, {Index: 1, Label: "Oppose"}},
		}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	served, err := votingconfig.ParseServedRound(raw)
	if err != nil {
		t.Fatal(err)
	}
	want := votingconfig.Ballot{
		Title:         "Round 1",
		DiscussionURL: "https://forum.example.org/t/1",
		Proposals: []votingconfig.BallotProposal{{
			ID: 1, Title: "Adopt ZIP-1", ZipNumber: "ZIP-1", ForumURL: "https://forum.example.org/t/2",
			Options: []votingconfig.BallotOption{{Index: 0, Label: "Support", Description: "In favor"}, {Index: 1, Label: "Oppose"}},
		}},
	}
	if served.Ballot.Digest() != want.Digest() {
		t.Fatalf("parsed ballot = %+v, want %+v", served.Ballot, want)
	}
	if served.RoundID != hex.EncodeToString(roundIDBytes) || !bytes.Equal(served.EaPK[:], eaPK) {
		t.Fatalf("parsed identity = %s / %x", served.RoundID, served.EaPK)
	}
}
