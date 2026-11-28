package votingconfig

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
)

// extensionsFixturePath is a canonical dynamic config whose "extensions"
// object a typed rewrite would change: an unsorted extensions object and an
// unmodeled extension with non-canonical numbers and escapes.
const extensionsFixturePath = "testdata/dynamic-config-extensions.json"

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
// expects its exact bytes back: no extension byte is lost or reordered.
func TestSignedConfigRoundTripIsByteForByte(t *testing.T) {
	raw, cfg := readFixture(t)
	if got := writeCanonical(t, cfg); !bytes.Equal(got, raw) {
		t.Fatalf("round trip changed the config\n got: %s\nwant: %s", got, raw)
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
