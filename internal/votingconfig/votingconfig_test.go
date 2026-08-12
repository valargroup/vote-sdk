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

func TestCanonicalPayloadV2Determinism(t *testing.T) {
	roundID := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	layout := testPIRLayout()
	var eaPK [32]byte
	for i := range eaPK {
		eaPK[i] = byte(i)
	}

	payload, err := CanonicalPayloadV2(roundID, eaPK, layout)
	if err != nil {
		t.Fatalf("CanonicalPayloadV2: %v", err)
	}
	roundIDBytes, _ := hex.DecodeString(roundID)
	want := append([]byte(DomainTagV2), roundIDBytes...)
	want = append(want, eaPK[:]...)
	// 19/12/7 as u32 little-endian.
	want = append(want, 19, 0, 0, 0, 12, 0, 0, 0, 7, 0, 0, 0)
	if string(payload) != string(want) {
		t.Fatalf("unexpected canonical payload: %s", hex.EncodeToString(payload))
	}
	if len(payload) != len(DomainTagV2)+32+32+12 {
		t.Fatalf("unexpected payload length %d", len(payload))
	}

	if _, err := CanonicalPayloadV2("not-hex", eaPK, layout); err == nil {
		t.Fatalf("expected error for invalid round id")
	}
	if _, err := CanonicalPayloadV2("ABCDEF", eaPK, layout); err == nil {
		t.Fatalf("expected error for non-lowercase/short round id")
	}
	if _, err := CanonicalPayloadV2(roundID, eaPK, PIRLayout{PIRDepth: 19, Tier0Layers: 12, Tier1Layers: 8}); err == nil {
		t.Fatalf("expected error for inconsistent pir layout")
	}
}

func TestSignVerifyV2RoundTripAndReplay(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	roundID := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	otherRoundID := "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	layout := testPIRLayout()
	var eaPK [32]byte
	for i := range eaPK {
		eaPK[i] = byte(255 - i)
	}

	sig, err := SignV2(priv, roundID, eaPK, layout)
	if err != nil {
		t.Fatalf("SignV2: %v", err)
	}
	if !VerifyV2(pub, roundID, eaPK, layout, sig) {
		t.Fatalf("signature did not verify")
	}
	if VerifyV2(pub, otherRoundID, eaPK, layout, sig) {
		t.Fatalf("signature verified when replayed under a different round id")
	}
	otherLayout := PIRLayout{PIRDepth: 19, Tier0Layers: 11, Tier1Layers: 8}
	if VerifyV2(pub, roundID, eaPK, otherLayout, sig) {
		t.Fatalf("signature verified against a swapped pir layout")
	}
	tampered := eaPK
	tampered[31] ^= 1
	if VerifyV2(pub, roundID, tampered, layout, sig) {
		t.Fatalf("signature verified against tampered ea_pk")
	}
	// A v1 signature over the bare ea_pk must not satisfy the v2 preimage.
	if VerifyV2(pub, roundID, eaPK, layout, SignV1(priv, eaPK)) {
		t.Fatalf("v1 signature verified against the v2 preimage")
	}
}

func TestVerifyEntrySignaturesDispatchesOnAuthVersion(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	roundID := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	otherRoundID := "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	layout := testPIRLayout()
	var eaPK [32]byte
	for i := range eaPK {
		eaPK[i] = byte(i + 1)
	}
	trusted := []TrustedKey{{
		KeyID:  "key-1",
		Alg:    AlgEd25519,
		Pubkey: base64.StdEncoding.EncodeToString(pub),
	}}
	entryFor := func(authVersion int, sig []byte) RoundEntry {
		return RoundEntry{
			AuthVersion: authVersion,
			EaPK:        base64.StdEncoding.EncodeToString(eaPK[:]),
			Signatures: []Signature{{
				KeyID: "key-1",
				Alg:   AlgEd25519,
				Sig:   base64.StdEncoding.EncodeToString(sig),
			}},
		}
	}

	sigV2, err := SignV2(priv, roundID, eaPK, layout)
	if err != nil {
		t.Fatalf("SignV2: %v", err)
	}
	if !VerifyEntrySignatures(roundID, entryFor(AuthVersionV2, sigV2), trusted, layout) {
		t.Fatalf("v2 entry did not verify")
	}
	if VerifyEntrySignatures(otherRoundID, entryFor(AuthVersionV2, sigV2), trusted, layout) {
		t.Fatalf("v2 entry verified under a different round id")
	}
	swappedLayout := PIRLayout{PIRDepth: 19, Tier0Layers: 11, Tier1Layers: 8}
	if VerifyEntrySignatures(roundID, entryFor(AuthVersionV2, sigV2), trusted, swappedLayout) {
		t.Fatalf("v2 entry verified after pir layout swap")
	}
	if !VerifyEntrySignatures(roundID, entryFor(AuthVersionV1, SignV1(priv, eaPK)), trusted, layout) {
		t.Fatalf("legacy v1 entry did not verify")
	}
	if VerifyEntrySignatures(roundID, entryFor(AuthVersionV1, sigV2), trusted, layout) {
		t.Fatalf("v2 signature verified as a v1 entry")
	}
	if VerifyEntrySignatures(roundID, entryFor(99, sigV2), trusted, layout) {
		t.Fatalf("unknown auth_version verified")
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
			name: "authenticated v2",
			cfg: withEntry(cfg, roundID, func(entry RoundEntry) RoundEntry {
				sig, err := SignV2(priv, roundID, eaPK, testPIRLayout())
				if err != nil {
					t.Fatalf("SignV2: %v", err)
				}
				entry.AuthVersion = AuthVersionV2
				entry.Signatures[0].Sig = base64.StdEncoding.EncodeToString(sig)
				return entry
			}),
			roundID: roundID,
			chainPK: eaPK,
			want:    AuthAuthenticated,
		},
		{
			name: "v2 signature under wrong version",
			cfg: withEntry(cfg, roundID, func(entry RoundEntry) RoundEntry {
				sig, err := SignV2(priv, roundID, eaPK, testPIRLayout())
				if err != nil {
					t.Fatalf("SignV2: %v", err)
				}
				entry.Signatures[0].Sig = base64.StdEncoding.EncodeToString(sig)
				return entry
			}),
			roundID: roundID,
			chainPK: eaPK,
			want:    AuthInvalidSignatures,
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
		PIRLayout:     testPIRLayout(),
		Rounds:        map[string]RoundEntry{roundID: {AuthVersion: AuthVersionV1}},
	}
	if err := ValidateWrapper(cfg); err != nil {
		t.Fatalf("ValidateWrapper() unexpected error: %v", err)
	}

	cfg.ConfigVersion = 99
	if err := ValidateWrapper(cfg); err == nil {
		t.Fatalf("expected unsupported config_version error")
	}
	cfg.ConfigVersion = ConfigVersionV1

	cfg.PIRLayout = PIRLayout{}
	if err := ValidateWrapper(cfg); err == nil {
		t.Fatalf("expected missing pir_layout error")
	}

	cfg.PIRLayout = PIRLayout{PIRDepth: 19, Tier0Layers: 12, Tier1Layers: 8}
	if err := ValidateWrapper(cfg); err == nil {
		t.Fatalf("expected inconsistent pir_layout error")
	}

	var malformed SignedConfig
	raw := []byte(`{"config_version":1,"vote_servers":[],"pir_endpoints":[],"rounds":[]}`)
	if err := json.Unmarshal(raw, &malformed); err == nil {
		t.Fatalf("expected JSON unmarshal to reject non-object rounds")
	}
}

func TestValidateStaticConfig(t *testing.T) {
	cfg := &StaticConfig{
		StaticConfigVersion: StaticConfigVersionV1,
		DynamicConfigURL:    "https://example.com/dynamic-voting-config.json",
		TrustedKeys: []TrustedKey{{
			KeyID:  "key-1",
			Alg:    AlgEd25519,
			Pubkey: base64.StdEncoding.EncodeToString(make([]byte, ed25519.PublicKeySize)),
		}},
	}
	if err := ValidateStaticConfig(cfg); err != nil {
		t.Fatalf("ValidateStaticConfig() unexpected error: %v", err)
	}

	cfg.StaticConfigVersion = 99
	if err := ValidateStaticConfig(cfg); err == nil {
		t.Fatalf("expected unsupported static_config_version error")
	}
	cfg.StaticConfigVersion = StaticConfigVersionV1

	cfg.DynamicConfigURL = " "
	if err := ValidateStaticConfig(cfg); err == nil {
		t.Fatalf("expected dynamic_config_url error")
	}
	cfg.DynamicConfigURL = "https://example.com/dynamic-voting-config.json"

	cfg.TrustedKeys = nil
	if err := ValidateStaticConfig(cfg); err == nil {
		t.Fatalf("expected trusted_keys error")
	}
}

func baseConfig(roundID string, eaPK [32]byte, sig []byte) *SignedConfig {
	return &SignedConfig{
		ConfigVersion: ConfigVersionV1,
		VoteServers:   []Endpoint{{URL: "https://vote.example", Label: "vote"}},
		PIREndpoints:  []Endpoint{{URL: "https://pir.example", Label: "pir"}},
		PIRLayout:     testPIRLayout(),
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

func testPIRLayout() PIRLayout {
	return PIRLayout{PIRDepth: 19, Tier0Layers: 12, Tier1Layers: 7}
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
