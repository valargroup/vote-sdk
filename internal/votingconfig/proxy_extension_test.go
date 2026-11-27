package votingconfig

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// extensionsFixturePath is a canonical dynamic config whose "extensions"
// object and unknown top-level members a typed rewrite would change: an
// unsorted extensions object, an unmodeled extension with
// non-canonical numbers and escapes, and members after "extensions".
const extensionsFixturePath = "testdata/dynamic-config-extensions.json"

// fixtureRound is a round id for configs built in tests.
const fixtureRound = "9c66c38026e3778ca449712ed1af5a6743ab6c5a11d71ff98d23a56f68f369f4"

// readFixture returns the fixture's bytes and its decoded config.
func readFixture(t *testing.T) ([]byte, *SignedConfig) {
	t.Helper()
	raw, err := os.ReadFile(extensionsFixturePath)
	if err != nil {
		t.Fatal(err)
	}
	var cfg SignedConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	return raw, &cfg
}

// writeCanonical encodes cfg as the config tools write it.
func writeCanonical(t *testing.T, cfg *SignedConfig) []byte {
	t.Helper()
	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(out, '\n')
}

// TestSignedConfigRoundTripIsByteForByte reads and rewrites the fixture and
// expects its exact bytes back: no extension byte or unknown member is lost
// or reordered.
func TestSignedConfigRoundTripIsByteForByte(t *testing.T) {
	raw, cfg := readFixture(t)
	if got := writeCanonical(t, cfg); !bytes.Equal(got, raw) {
		t.Fatalf("round trip changed the config\n got: %s\nwant: %s", got, raw)
	}
	if len(cfg.Unknown) != 2 || cfg.Unknown[0].Key != "x_rollout" || cfg.Unknown[1].Key != "operator_note" {
		t.Fatalf("unknown members = %+v", cfg.Unknown)
	}
}

// TestSignedConfigWithoutExtrasEncodesAsBefore checks that a config with no
// extensions or unknown members encodes exactly as the plain struct did.
func TestSignedConfigWithoutExtrasEncodesAsBefore(t *testing.T) {
	cfg := SignedConfig{
		ConfigVersion: ConfigVersionV1,
		VoteServers:   []Endpoint{{URL: "https://vote.example/?a=1&b=<2>", Label: "vote"}},
		PIRLayout:     PIRLayout{PIRDepth: 19, Tier0Layers: 12, Tier1Layers: 7, PolyLen: PolyLen4096},
		Rounds:        map[string]RoundEntry{fixtureRound: {AuthVersion: AuthVersionV2, EaPK: "AA=="}},
	}
	type plain SignedConfig
	want, err := json.MarshalIndent(plain(cfg), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if got := writeCanonical(t, &cfg); !bytes.Equal(got, append(want, '\n')) {
		t.Fatalf("encoding changed\n got: %s\nwant: %s", got, want)
	}
}

// TestSignedConfigTopLevelKeys checks exact-key matching and that a repeated
// top-level key is rejected.
func TestSignedConfigTopLevelKeys(t *testing.T) {
	var cfg SignedConfig
	if err := json.Unmarshal([]byte(`{"config_version":1,"Rounds":{}}`), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Rounds != nil || len(cfg.Unknown) != 1 || cfg.Unknown[0].Key != "Rounds" {
		t.Fatalf("a miscased key must stay unknown: rounds=%v unknown=%+v", cfg.Rounds, cfg.Unknown)
	}
	err := json.Unmarshal([]byte(`{"config_version":1,"rounds":{},"rounds":{}}`), &cfg)
	if err == nil || !strings.Contains(err.Error(), "repeated key") {
		t.Fatalf("repeated top-level key: err = %v", err)
	}
	cfg = SignedConfig{Unknown: []RawMember{{Key: "rounds", Value: json.RawMessage(`{}`)}}}
	if _, err := json.Marshal(cfg); err == nil {
		t.Fatal("an unknown member named like a modeled field must not encode")
	}
}

// TestProxyDelegationV1DecodesFixture checks the typed view of the fixture's
// extension.
func TestProxyDelegationV1DecodesFixture(t *testing.T) {
	_, cfg := readFixture(t)
	ext, err := cfg.ProxyDelegationV1()
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateProxyDelegationV1(ext); err != nil {
		t.Fatal(err)
	}
	if len(ext.VerifierAPIURLs) != 1 || len(ext.ProofRPCURLs) != 2 {
		t.Fatalf("unexpected extension: %+v", ext)
	}

	var none SignedConfig
	if ext, err := none.ProxyDelegationV1(); ext != nil || err != nil {
		t.Fatalf("no extensions: ext=%v err=%v", ext, err)
	}
	none.Extensions = json.RawMessage(`{"other_v1":{}}`)
	if ext, err := none.ProxyDelegationV1(); ext != nil || err != nil {
		t.Fatalf("no proxy extension: ext=%v err=%v", ext, err)
	}
}

// TestProxyDelegationV1IsStrict checks that keys a wallet would read
// differently, and members the extension does not define, are rejected.
func TestProxyDelegationV1IsStrict(t *testing.T) {
	for name, ext := range map[string]string{
		"null":           `null`,
		"array":          `[]`,
		"unknown field":  `{"verifier_api_urls":[],"extra":1}`,
		"miscased field": `{"Verifier_API_URLs":[]}`,
		"repeated field": `{"proof_rpc_urls":[],"proof_rpc_urls":[]}`,
		"not a string":   `{"verifier_api_urls":[1]}`,
	} {
		t.Run(name, func(t *testing.T) {
			cfg := SignedConfig{Extensions: json.RawMessage(`{"proxy_delegation_v1":` + ext + `}`)}
			if _, err := cfg.ProxyDelegationV1(); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}

// TestValidateProxyDelegationV1URLs checks the URL list rules.
func TestValidateProxyDelegationV1URLs(t *testing.T) {
	_, cfg := readFixture(t)
	for name, edit := range map[string]func(*ProxyDelegationV1){
		"no verifier URL":   func(x *ProxyDelegationV1) { x.VerifierAPIURLs = []string{} },
		"one proof RPC URL": func(x *ProxyDelegationV1) { x.ProofRPCURLs = x.ProofRPCURLs[:1] },
		"repeated RPC URL":  func(x *ProxyDelegationV1) { x.ProofRPCURLs = []string{"https://a.example", "https://a.example"} },
		"http URL":          func(x *ProxyDelegationV1) { x.VerifierAPIURLs = []string{"http://verifier.example"} },
		"relative URL":      func(x *ProxyDelegationV1) { x.VerifierAPIURLs = []string{"/attest"} },
		"URL with userinfo": func(x *ProxyDelegationV1) { x.VerifierAPIURLs = []string{"https://u:p@verifier.example"} },
	} {
		t.Run(name, func(t *testing.T) {
			ext, err := cfg.ProxyDelegationV1()
			if err != nil {
				t.Fatal(err)
			}
			edit(ext)
			if err := ValidateProxyDelegationV1(ext); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}
