package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/valargroup/vote-sdk/internal/votingconfig"
)

// vizorVector is the vizor_list case of the shared wallet_list_payload
// vectors, alice on X at index 17 and bob on GitHub at index 23: its
// published document, its signed payload and the key that signs it as
// "valargroup".
type vizorVector struct {
	Document string
	Payload  []byte
	Signer   ed25519.PrivateKey
}

// loadVizorVector reads the vizor_list case from vote-sdk's copy of the
// client vector file. It is read by path, since testutil links the circuits
// library that the release job's voting-config tests run without.
func loadVizorVector(t *testing.T) vizorVector {
	t.Helper()
	raw, err := os.ReadFile("../../testutil/testdata/proxy_delegation_v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Cases []struct {
			Name   string `json:"name"`
			Inputs struct {
				Seed string `json:"signer_secret_seed"`
			} `json:"inputs"`
			Expected struct {
				Payload  string `json:"payload"`
				Document string `json:"document"`
			} `json:"expected"`
		} `json:"wallet_list_payload"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	for _, c := range file.Cases {
		if c.Name != "vizor_list" {
			continue
		}
		seed, err := hex.DecodeString(c.Inputs.Seed)
		if err != nil || len(seed) != ed25519.SeedSize {
			t.Fatalf("vizor_list signer seed: %v", err)
		}
		payload, err := hex.DecodeString(c.Expected.Payload)
		if err != nil {
			t.Fatal(err)
		}
		return vizorVector{Document: c.Expected.Document, Payload: payload, Signer: ed25519.NewKeyFromSeed(seed)}
	}
	t.Fatal("no vizor_list vector")
	return vizorVector{}
}

// allowedKey is v's signer as an --allowed-key value.
func (v vizorVector) allowedKey() string {
	return "valargroup=" + base64.StdEncoding.EncodeToString(v.Signer.Public().(ed25519.PublicKey))
}

// writeDraft writes the list without its signatures and returns the path.
func (v vizorVector) writeDraft(t *testing.T, dir string) string {
	t.Helper()
	list, _, _, err := votingconfig.ParseWalletList([]byte(v.Document))
	if err != nil {
		t.Fatal(err)
	}
	draft, err := list.MarshalDocument(nil)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "draft.json")
	if err := os.WriteFile(path, draft, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestWalletListPayloadAndSign builds the payload of an unsigned draft and
// signs it into the vector's published document, byte for byte.
func TestWalletListPayloadAndSign(t *testing.T) {
	v := loadVizorVector(t)
	dir := t.TempDir()
	draft := v.writeDraft(t, dir)

	out, err := run(t, "wallet-list", "payload", "--list", draft)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(out); got != base64.StdEncoding.EncodeToString(v.Payload) {
		t.Fatalf("payload = %s", got)
	}

	t.Setenv("VOTING_CONFIG_PRIVKEY", seedB64(v.Signer))
	signed := filepath.Join(dir, "list.json")
	if _, err := run(t, "wallet-list", "sign", "--list", draft, "--signer-id", "valargroup", "--out", signed); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(signed)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != v.Document {
		t.Fatalf("signed document differs from the vector:\n%s", got)
	}

	// A second key's signature joins the first.
	t.Setenv("VOTING_CONFIG_PRIVKEY", seedB64(fixtureSigner))
	out, err = run(t, "wallet-list", "sign", "--list", signed, "--signer-id", "other")
	if err != nil {
		t.Fatal(err)
	}
	_, sigs, _, err := votingconfig.ParseWalletList([]byte(out))
	if err != nil || len(sigs) != 2 || sigs[0].KeyID != "valargroup" || sigs[1].KeyID != "other" {
		t.Fatalf("co-signed list: %v, %+v", err, sigs)
	}
}

// TestWalletListSignRefusesUnreadableOutput checks that sign writes nothing
// for a signer id over 64 bytes or a ninth signature, which the parser
// would refuse.
func TestWalletListSignRefusesUnreadableOutput(t *testing.T) {
	v := loadVizorVector(t)
	dir := t.TempDir()
	draft := v.writeDraft(t, dir)
	t.Setenv("VOTING_CONFIG_PRIVKEY", seedB64(v.Signer))
	out := filepath.Join(dir, "list.json")
	if _, err := run(t, "wallet-list", "sign", "--list", draft, "--signer-id", strings.Repeat("k", 65), "--out", out); err == nil {
		t.Fatal("signed with a 65-byte signer id")
	}
	for i := range 8 {
		if _, err := run(t, "wallet-list", "sign", "--list", draft, "--signer-id", fmt.Sprintf("signer-%d", i), "--out", draft); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := run(t, "wallet-list", "sign", "--list", draft, "--signer-id", "signer-8", "--out", out); err == nil || !strings.Contains(err.Error(), "Malformed") {
		t.Fatalf("ninth signature: %v", err)
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatalf("sign wrote %s: %v", out, err)
	}
}

// TestWalletListVerify checks verify's verdicts on the list.
func TestWalletListVerify(t *testing.T) {
	v := loadVizorVector(t)
	dir := t.TempDir()
	list := filepath.Join(dir, "list.json")
	if err := os.WriteFile(list, []byte(v.Document), 0o644); err != nil {
		t.Fatal(err)
	}
	base := []string{"wallet-list", "verify", "--list", list, "--wallet-id", "vizor", "--chain-id", "zvote-1"}

	out, err := run(t, append(base, "--allowed-key", v.allowedKey())...)
	if err != nil || !strings.Contains(out, "OK: vizor list version 1 for zvote-1, 2 delegates, signed by valargroup") {
		t.Fatalf("verify: %q, %v", out, err)
	}

	other := "valargroup=" + base64.StdEncoding.EncodeToString(fixtureSigner.Public().(ed25519.PublicKey))
	if _, err := run(t, append(base, "--allowed-key", other)...); err == nil || !strings.Contains(err.Error(), "NoPinnedSignature") {
		t.Fatalf("another pinned key: %v", err)
	}
	if _, err := run(t, append(base, "--allowed-key", "valargroup")...); err == nil {
		t.Fatal("an --allowed-key without a public key was accepted")
	}

	// The version rule against the list it replaces: the same list passes,
	// a different list at the same version does not.
	if _, err := run(t, append(base, "--allowed-key", v.allowedKey(), "--previous", list)...); err != nil {
		t.Fatalf("unchanged list: %v", err)
	}
	previous := filepath.Join(dir, "previous.json")
	edited := strings.Replace(v.Document, `"name": "Alice Example"`, `"name": "Alice Old"`, 1)
	if err := os.WriteFile(previous, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, append(base, "--allowed-key", v.allowedKey(), "--previous", previous)...); err == nil || !strings.Contains(err.Error(), "version 1 is not above") {
		t.Fatalf("changed list at the same version: %v", err)
	}

	if err := os.WriteFile(previous, []byte("not a list"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, append(base, "--allowed-key", v.allowedKey(), "--previous", previous)...); err != nil {
		t.Fatalf("a previous list that does not parse sets no version: %v", err)
	}
	missing := filepath.Join(dir, "missing.json")
	if _, err := run(t, append(base, "--allowed-key", v.allowedKey(), "--previous", missing)...); err == nil || !strings.Contains(err.Error(), "read previous wallet list") {
		t.Fatalf("an unreadable --previous: %v", err)
	}

	out, err = run(t, append(base, "--allowed-key", other, "--json")...)
	var result walletListResult
	if err == nil || json.Unmarshal([]byte(out), &result) != nil || result.OK || !strings.HasPrefix(result.Error, "NoPinnedSignature") {
		t.Fatalf("--json failure: %q, %v", out, err)
	}
}

// fakeRegistry serves node_info and the delegates a chain check reads.
type fakeRegistry struct {
	chainID   string
	delegates map[uint32]string
}

// ServeHTTP answers node_info, a delegate's entry, or the chain's NotFound.
func (f fakeRegistry) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/cosmos/base/tendermint/v1beta1/node_info" {
		fmt.Fprintf(w, `{"default_node_info":{"network":%q}}`, f.chainID)
		return
	}
	var index uint32
	if _, err := fmt.Sscanf(r.URL.Path, "/shielded-vote/v1/delegates/%d", &index); err != nil {
		http.NotFound(w, r)
		return
	}
	entry, ok := f.delegates[index]
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprintf(w, `{"code":5,"message":"rpc error: code = NotFound desc = delegate %d not found"}`, index)
		return
	}
	fmt.Fprintf(w, `{"delegate":%s}`, entry)
}

// TestWalletListChainCheck checks each kind of re-review finding: a row is
// reviewed only while its entry is ACTIVE with the row's provider, handle
// and key.
func TestWalletListChainCheck(t *testing.T) {
	v := loadVizorVector(t)
	dir := t.TempDir()
	list := filepath.Join(dir, "list.json")
	if err := os.WriteFile(list, []byte(v.Document), 0o644); err != nil {
		t.Fatal(err)
	}
	rows, _, _, err := votingconfig.ParseWalletList([]byte(v.Document))
	if err != nil {
		t.Fatal(err)
	}
	// entry is a registry entry with the key of the list's row, or with
	// another key when row is negative.
	entry := func(row int, provider int, handle string, status int) string {
		var dk [32]byte
		if row >= 0 {
			dk, _ = votingconfig.DecodeDelegateKey(rows.Delegates[row].DK)
		}
		return fmt.Sprintf(`{"index":1,"dk":%q,"provider":%d,"handle":%q,"status":%d}`,
			base64.StdEncoding.EncodeToString(dk[:]), provider, handle, status)
	}
	alice, bob := entry(0, 1, "alice", 0), entry(1, 2, "bob-dev", 0)

	for _, c := range []struct {
		name      string
		chainID   string
		delegates map[uint32]string
		kinds     []string
		err       string
	}{
		{name: "reviewed", delegates: map[uint32]string{17: alice, 23: bob}},
		{name: "not registered", delegates: map[uint32]string{17: alice}, kinds: []string{"not_registered"}},
		{name: "suspended", delegates: map[uint32]string{17: entry(0, 1, "alice", 1), 23: bob}, kinds: []string{"suspended"}},
		{name: "retired", delegates: map[uint32]string{17: alice, 23: entry(1, 2, "bob-dev", 2)}, kinds: []string{"retired"}},
		{name: "another key", delegates: map[uint32]string{17: entry(-1, 1, "alice", 0), 23: bob}, kinds: []string{"key_mismatch"}},
		{name: "renamed", delegates: map[uint32]string{17: entry(0, 1, "alicia", 0), 23: bob}, kinds: []string{"handle_mismatch"}},
		{name: "another account's entry", delegates: map[uint32]string{17: alice, 23: entry(-1, 1, "carol", 0)}, kinds: []string{"provider_mismatch", "handle_mismatch", "key_mismatch"}},
		{name: "other chain", chainID: "svote-1", err: `serves chain "svote-1"`},
	} {
		t.Run(c.name, func(t *testing.T) {
			chainID := c.chainID
			if chainID == "" {
				chainID = "zvote-1"
			}
			server := httptest.NewServer(fakeRegistry{chainID: chainID, delegates: c.delegates})
			defer server.Close()
			out, err := run(t, "wallet-list", "verify", "--list", list, "--wallet-id", "vizor", "--chain-id", "zvote-1",
				"--allowed-key", v.allowedKey(), "--chain-check", server.URL, "--json")
			var result walletListResult
			if jerr := json.Unmarshal([]byte(out), &result); jerr != nil {
				t.Fatalf("output %q: %v", out, jerr)
			}
			if c.err != "" {
				if err == nil || !strings.Contains(result.Error, c.err) {
					t.Fatalf("error %q, want %q", result.Error, c.err)
				}
				return
			}
			var kinds []string
			for _, f := range result.Findings {
				kinds = append(kinds, f.Kind)
			}
			if strings.Join(kinds, ",") != strings.Join(c.kinds, ",") || (err == nil) != (len(c.kinds) == 0) || result.OK != (len(c.kinds) == 0) {
				t.Fatalf("findings %v (err %v), want %v", result.Findings, err, c.kinds)
			}
		})
	}
}
