package votingconfig

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"testing"
)

func TestCanonicalPayloadV1Determinism(t *testing.T) {
	var eaPK [32]byte
	for i := range eaPK {
		eaPK[i] = byte(i)
	}

	first := CanonicalPayloadV1(eaPK)
	second := CanonicalPayloadV1(eaPK)
	if hex.EncodeToString(first) != "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f" {
		t.Fatalf("unexpected canonical payload: %s", hex.EncodeToString(first))
	}
	if string(first) != string(second) {
		t.Fatalf("canonical payload changed between calls")
	}
	first[0] = 0xff
	if CanonicalPayloadV1(eaPK)[0] != 0x00 {
		t.Fatalf("canonical payload aliases caller-visible state")
	}
}

func TestSignVerifyV1RoundTrip(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	var eaPK [32]byte
	for i := range eaPK {
		eaPK[i] = byte(255 - i)
	}

	sig := SignV1(priv, eaPK)
	if !VerifyV1(pub, eaPK, sig) {
		t.Fatalf("signature did not verify")
	}
	eaPK[31] ^= 1
	if VerifyV1(pub, eaPK, sig) {
		t.Fatalf("signature verified against tampered ea_pk")
	}
}

func TestAuthenticateStatuses(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	var eaPK [32]byte
	for i := range eaPK {
		eaPK[i] = byte(i + 1)
	}

	roundID := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	cfg := baseConfig(roundID, eaPK, SignV1(priv, eaPK))
	trusted := []TrustedKey{{
		KeyID:  "key-1",
		Alg:    AlgEd25519,
		Pubkey: base64.StdEncoding.EncodeToString(pub),
	}}

	tests := []struct {
		name    string
		cfg     *SignedConfig
		roundID string
		chainPK [32]byte
		want    AuthStatus
	}{
		{
			name:    "authenticated",
			cfg:     cfg,
			roundID: roundID,
			chainPK: eaPK,
			want:    AuthAuthenticated,
		},
		{
			name:    "missing round",
			cfg:     cfg,
			roundID: "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff",
			chainPK: eaPK,
			want:    AuthMissingRound,
		},
		{
			name: "unknown auth version",
			cfg: withEntry(cfg, roundID, func(entry RoundEntry) RoundEntry {
				entry.AuthVersion = 99
				return entry
			}),
			roundID: roundID,
			chainPK: eaPK,
			want:    AuthUnknownVersion,
		},
		{
			name: "invalid signatures",
			cfg: withEntry(cfg, roundID, func(entry RoundEntry) RoundEntry {
				entry.Signatures[0].Sig = base64.StdEncoding.EncodeToString(make([]byte, ed25519.SignatureSize))
				return entry
			}),
			roundID: roundID,
			chainPK: eaPK,
			want:    AuthInvalidSignatures,
		},
		{
			name:    "ea pk mismatch",
			cfg:     cfg,
			roundID: roundID,
			chainPK: func() [32]byte {
				tampered := eaPK
				tampered[0] ^= 1
				return tampered
			}(),
			want: AuthEaPKMismatch,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Authenticate(tt.cfg, trusted, tt.roundID, tt.chainPK); got != tt.want {
				t.Fatalf("Authenticate() = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestValidateWrapper(t *testing.T) {
	roundID := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	cfg := &SignedConfig{
		ConfigVersion: ConfigVersionV1,
		VoteServers:   []Endpoint{{URL: "https://vote.example", Label: "vote"}},
		PIREndpoints:  []Endpoint{{URL: "https://pir.example", Label: "pir"}},
		Rounds:        map[string]RoundEntry{roundID: {AuthVersion: AuthVersionV1}},
	}
	if err := ValidateWrapper(cfg); err != nil {
		t.Fatalf("ValidateWrapper() unexpected error: %v", err)
	}

	cfg.ConfigVersion = 99
	if err := ValidateWrapper(cfg); err == nil {
		t.Fatalf("expected unsupported config_version error")
	}

	var malformed SignedConfig
	raw := []byte(`{"config_version":1,"vote_servers":[],"pir_endpoints":[],"rounds":[]}`)
	if err := json.Unmarshal(raw, &malformed); err == nil {
		t.Fatalf("expected JSON unmarshal to reject non-object rounds")
	}
}

func baseConfig(roundID string, eaPK [32]byte, sig []byte) *SignedConfig {
	return &SignedConfig{
		ConfigVersion: ConfigVersionV1,
		VoteServers:   []Endpoint{{URL: "https://vote.example", Label: "vote"}},
		PIREndpoints:  []Endpoint{{URL: "https://pir.example", Label: "pir"}},
		Rounds: map[string]RoundEntry{
			roundID: {
				AuthVersion: AuthVersionV1,
				EaPK:        base64.StdEncoding.EncodeToString(eaPK[:]),
				Signatures: []Signature{{
					KeyID: "key-1",
					Alg:   AlgEd25519,
					Sig:   base64.StdEncoding.EncodeToString(sig),
				}},
			},
		},
	}
}

func withEntry(cfg *SignedConfig, roundID string, update func(RoundEntry) RoundEntry) *SignedConfig {
	clone := *cfg
	clone.Rounds = map[string]RoundEntry{}
	for id, entry := range cfg.Rounds {
		copied := entry
		copied.Signatures = append([]Signature(nil), entry.Signatures...)
		clone.Rounds[id] = copied
	}
	clone.Rounds[roundID] = update(clone.Rounds[roundID])
	return &clone
}
