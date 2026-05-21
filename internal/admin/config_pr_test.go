package admin

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cosmossdk.io/log"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	"github.com/gorilla/mux"
	"github.com/valargroup/vote-sdk/internal/votingconfig"
)

func TestHandleCreateConfigPRTokenMissing(t *testing.T) {
	t.Parallel()

	r := mux.NewRouter()
	a := &Admin{logger: log.NewNopLogger()}
	RegisterRoutes(r, func() *Admin { return a }, log.NewNopLogger())

	req := httptest.NewRequest(http.MethodPost, "/api/config-prs", bytes.NewBufferString(`{}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestNewAdminFollowsStaticConfigDynamicURL(t *testing.T) {
	t.Parallel()

	var sawStaticFetch, sawDynamicFetch bool
	var cdn *httptest.Server
	cdn = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/prod/static-voting-config.json":
			sawStaticFetch = true
			writeJSON(t, w, map[string]any{
				"static_config_version": 1,
				"dynamic_config_url":    cdn.URL + "/prod/dynamic-voting-config.json",
			})
			return
		case "/prod/dynamic-voting-config.json":
			sawDynamicFetch = true
			writeJSON(t, w, VotingConfig{
				Version: 1,
				VoteServers: []ServiceEntry{{
					URL:   "https://prod.vote-chain-primary.example",
					Label: "primary",
				}},
				PIRServers: []ServiceEntry{{
					URL:   "https://prod.pir.example",
					Label: "pir",
				}},
			})
			return
		default:
			http.Error(w, "unexpected path: "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer cdn.Close()

	a, err := New(Config{
		ConfigURL: cdn.URL + "/",
	}, t.TempDir(), nil, nil, log.NewNopLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	if !sawStaticFetch || !sawDynamicFetch {
		t.Fatalf("expected static and dynamic config fetches, got static=%v dynamic=%v", sawStaticFetch, sawDynamicFetch)
	}
	if a.configURL != cdn.URL+"/prod/static-voting-config.json" {
		t.Fatalf("unexpected resolved config URL: %q", a.configURL)
	}
	if got := a.configPR.dynamicConfigPath(); got != "prod/dynamic-voting-config.json" {
		t.Fatalf("unexpected dynamic PR path: %q", got)
	}
	if got := a.configPR.staticConfigPath(); got != "prod/static-voting-config.json" {
		t.Fatalf("unexpected static PR path: %q", got)
	}
}

func TestHandleCreateConfigPRRejectsBadSignature(t *testing.T) {
	t.Parallel()

	body := validCreateConfigPRRequest(t)
	body.Auth.Signature = base64.StdEncoding.EncodeToString([]byte("bad"))

	r := mux.NewRouter()
	a := &Admin{
		configPR:         testConfigPRAutomation("https://example.invalid"),
		logger:           log.NewNopLogger(),
		checkVoteManager: func(string) bool { return true },
	}
	RegisterRoutes(r, func() *Admin { return a }, log.NewNopLogger())

	w := serveCreateConfigPR(t, r, body)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestHandleCreateConfigPRRejectsNonVoteManager(t *testing.T) {
	t.Parallel()

	body := validCreateConfigPRRequest(t)

	r := mux.NewRouter()
	a := &Admin{
		configPR:         testConfigPRAutomation("https://example.invalid"),
		logger:           log.NewNopLogger(),
		checkVoteManager: func(string) bool { return false },
	}
	RegisterRoutes(r, func() *Admin { return a }, log.NewNopLogger())

	w := serveCreateConfigPR(t, r, body)
	if w.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestHandleCreateConfigPRCreatesPullRequest(t *testing.T) {
	t.Parallel()

	body := validCreateConfigPRRequest(t)
	dynamicConfig, staticConfig := configDocuments(t, nil)

	var sawCreateRef, sawUpdateContent, sawCreatePR bool
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Fatalf("missing authorization header: %q", got)
		}
		path := r.URL.Path
		switch {
		case r.Method == http.MethodGet && path == "/repos/valargroup/token-holder-voting-config/git/ref/heads/main":
			writeJSON(t, w, map[string]any{"object": map[string]string{"sha": "main-sha"}})
		case r.Method == http.MethodPost && path == "/repos/valargroup/token-holder-voting-config/git/refs":
			sawCreateRef = true
			writeJSON(t, w, map[string]any{"ref": "refs/heads/config-production-round-" + body.RoundID[:12]})
		case r.Method == http.MethodGet && path == "/repos/valargroup/token-holder-voting-config/contents/prod/dynamic-voting-config.json" && r.URL.Query().Get("ref") == "main":
			writeContent(t, w, dynamicConfig, "dynamic-main-sha")
		case r.Method == http.MethodGet && path == "/repos/valargroup/token-holder-voting-config/contents/prod/static-voting-config.json" && r.URL.Query().Get("ref") == "main":
			writeContent(t, w, staticConfig, "static-main-sha")
		case r.Method == http.MethodGet && path == "/repos/valargroup/token-holder-voting-config/contents/prod/dynamic-voting-config.json" && strings.HasPrefix(r.URL.Query().Get("ref"), "config-production-round-"):
			http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
		case r.Method == http.MethodPut && path == "/repos/valargroup/token-holder-voting-config/contents/prod/dynamic-voting-config.json":
			sawUpdateContent = true
			var req struct {
				Content string `json:"content"`
				Branch  string `json:"branch"`
				SHA     string `json:"sha"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatal(err)
			}
			if req.Branch != "config-production-round-"+body.RoundID[:12] || req.SHA != "dynamic-main-sha" {
				t.Fatalf("unexpected update request: %+v", req)
			}
			updated, err := base64.StdEncoding.DecodeString(req.Content)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(updated), body.RoundID) {
				t.Fatalf("updated config missing round id: %s", updated)
			}
			writeJSON(t, w, map[string]any{"commit": map[string]string{"sha": "commit-sha"}})
		case r.Method == http.MethodPost && path == "/repos/valargroup/token-holder-voting-config/pulls":
			sawCreatePR = true
			writeJSON(t, w, map[string]string{"html_url": "https://github.com/valargroup/token-holder-voting-config/pull/123"})
		default:
			t.Fatalf("unexpected GitHub request: %s %s?%s", r.Method, path, r.URL.RawQuery)
		}
	}))
	defer gh.Close()

	r := mux.NewRouter()
	a := &Admin{
		configPR:         testConfigPRAutomation(gh.URL),
		logger:           log.NewNopLogger(),
		checkVoteManager: func(string) bool { return true },
	}
	RegisterRoutes(r, func() *Admin { return a }, log.NewNopLogger())

	w := serveCreateConfigPR(t, r, body)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", w.Code, w.Body.String())
	}
	var resp createConfigPRResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.HTMLURL == "" || resp.CommitSHA != "commit-sha" {
		t.Fatalf("unexpected response: %+v", resp)
	}
	if !sawCreateRef || !sawUpdateContent || !sawCreatePR {
		t.Fatalf("missing GitHub calls: ref=%v update=%v pr=%v", sawCreateRef, sawUpdateContent, sawCreatePR)
	}
}

func TestMergeConfigPREntryRejectsEaPKMismatch(t *testing.T) {
	t.Parallel()

	body := validCreateConfigPRRequest(t)
	existing := body.Entry
	existing.EaPK = base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{9}, 32))
	dynamicConfig, staticConfig := configDocuments(t, map[string]votingconfig.RoundEntry{
		body.RoundID: existing,
	})

	_, _, _, err := mergeConfigPREntry(dynamicConfig, staticConfig, body.RoundID, body.Entry)
	if err == nil || !strings.Contains(err.Error(), "ea_pk mismatch") {
		t.Fatalf("want ea_pk mismatch, got %v", err)
	}
}

func TestMergeConfigPREntryPreservesOtherSignatures(t *testing.T) {
	t.Parallel()

	body := validCreateConfigPRRequest(t)
	existing := body.Entry
	existing.Signatures = append([]votingconfig.Signature{
		{KeyID: "other", Alg: votingconfig.AlgEd25519, Sig: body.Entry.Signatures[0].Sig},
	}, existing.Signatures...)
	dynamicConfig, staticConfig := configDocuments(t, map[string]votingconfig.RoundEntry{
		body.RoundID: existing,
	})

	merged, mergedExisting, _, err := mergeConfigPREntry(dynamicConfig, staticConfig, body.RoundID, body.Entry)
	if err != nil {
		t.Fatal(err)
	}
	if !mergedExisting {
		t.Fatalf("expected mergedExisting")
	}
	var cfg votingconfig.SignedConfig
	if err := json.Unmarshal(merged, &cfg); err != nil {
		t.Fatal(err)
	}
	if got := len(cfg.Rounds[body.RoundID].Signatures); got != 2 {
		t.Fatalf("want 2 signatures, got %d", got)
	}
}

func TestMergeConfigPREntryResolvesTrustedKeyID(t *testing.T) {
	t.Parallel()

	body := validCreateConfigPRRequest(t)
	body.Entry.Signatures[0].KeyID = "keplr:sv1example"
	dynamicConfig, staticConfig := configDocuments(t, nil)

	merged, _, resolvedKeyIDs, err := mergeConfigPREntry(dynamicConfig, staticConfig, body.RoundID, body.Entry)
	if err != nil {
		t.Fatal(err)
	}
	var cfg votingconfig.SignedConfig
	if err := json.Unmarshal(merged, &cfg); err != nil {
		t.Fatal(err)
	}
	got := cfg.Rounds[body.RoundID].Signatures[0].KeyID
	if got != "valar-test" {
		t.Fatalf("want trusted key id, got %q", got)
	}
	if len(resolvedKeyIDs) != 1 || resolvedKeyIDs[0] != "valar-test" {
		t.Fatalf("want resolved key ids [valar-test], got %v", resolvedKeyIDs)
	}
}

func TestConfigPRBodyUsesResolvedKeyIDs(t *testing.T) {
	t.Parallel()

	body := validCreateConfigPRRequest(t)
	body.Entry.Signatures[0].KeyID = "keplr:sv1example"

	got := configPRBody(body, false, []string{"valar-test"}, configPRAutomation{})
	if !strings.Contains(got, "Trusted key IDs (from static-voting-config.json) attesting this entry: valar-test.") {
		t.Fatalf("PR body missing resolved trusted key ID line:\n%s", got)
	}
	if strings.Contains(got, "keplr:sv1example") {
		t.Fatalf("PR body should not surface raw keplr placeholder key ID:\n%s", got)
	}
}

func TestConfigPRPathForConfigURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		configURL  string
		configPath string
		label      string
		dynamic    string
		static     string
		branchName string
	}{
		{
			name:       "production base URL",
			configURL:  "https://raw.githubusercontent.com/valargroup/token-holder-voting-config/main/",
			configPath: "prod",
			label:      "production",
			dynamic:    "prod/dynamic-voting-config.json",
			static:     "prod/static-voting-config.json",
			branchName: "config-production-round-aaaaaaaaaaaa",
		},
		{
			name:       "staging base URL",
			configURL:  "https://raw.githubusercontent.com/valargroup/token-holder-voting-config/main/stage/",
			configPath: "stage",
			label:      "staging",
			dynamic:    "stage/dynamic-voting-config.json",
			static:     "stage/static-voting-config.json",
			branchName: "config-staging-round-aaaaaaaaaaaa",
		},
		{
			name:       "root base URL defaults to production",
			configURL:  "https://raw.githubusercontent.com/valargroup/token-holder-voting-config/main/",
			configPath: "prod",
			label:      "production",
			dynamic:    "prod/dynamic-voting-config.json",
			static:     "prod/static-voting-config.json",
			branchName: "config-production-round-aaaaaaaaaaaa",
		},
		{
			name:       "root dynamic file URL defaults to production",
			configURL:  "https://voting.valargroup.org/prod/dynamic-voting-config.json",
			configPath: "prod",
			label:      "production",
			dynamic:    "prod/dynamic-voting-config.json",
			static:     "prod/static-voting-config.json",
			branchName: "config-production-round-aaaaaaaaaaaa",
		},
		{
			name:       "prod file URL maps to production folder",
			configURL:  "https://voting.valargroup.org/prod/dynamic-voting-config.json",
			configPath: "prod",
			label:      "production",
			dynamic:    "prod/dynamic-voting-config.json",
			static:     "prod/static-voting-config.json",
			branchName: "config-production-round-aaaaaaaaaaaa",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			configPath, label := configPRPathForConfigURL(tt.configURL)
			if configPath != tt.configPath || label != tt.label {
				t.Fatalf("unexpected target: configPath=%q label=%q", configPath, label)
			}
			automation := configPRAutomation{
				ConfigPath:       configPath,
				EnvironmentLabel: label,
			}
			if dynamic := automation.dynamicConfigPath(); dynamic != tt.dynamic {
				t.Fatalf("want dynamic path %q, got %q", tt.dynamic, dynamic)
			}
			if static := automation.staticConfigPath(); static != tt.static {
				t.Fatalf("want static path %q, got %q", tt.static, static)
			}
			if got := automation.branchName(strings.Repeat("a", 64)); got != tt.branchName {
				t.Fatalf("want branch %q, got %q", tt.branchName, got)
			}
		})
	}
}

func TestConfigURLForFile(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		configURL string
		fileName  string
		want      string
	}{
		{
			name:      "production base URL",
			configURL: "https://raw.githubusercontent.com/valargroup/token-holder-voting-config/main/",
			fileName:  dynamicConfigName,
			want:      "https://raw.githubusercontent.com/valargroup/token-holder-voting-config/main/prod/dynamic-voting-config.json",
		},
		{
			name:      "staging base URL",
			configURL: "https://raw.githubusercontent.com/valargroup/token-holder-voting-config/main/stage/",
			fileName:  staticConfigName,
			want:      "https://raw.githubusercontent.com/valargroup/token-holder-voting-config/main/stage/static-voting-config.json",
		},
		{
			name:      "legacy prod base URL",
			configURL: "https://raw.githubusercontent.com/valargroup/token-holder-voting-config/main/prod/",
			fileName:  dynamicConfigName,
			want:      "https://raw.githubusercontent.com/valargroup/token-holder-voting-config/main/prod/dynamic-voting-config.json",
		},
		{
			name:      "dynamic file URL",
			configURL: "https://raw.githubusercontent.com/valargroup/token-holder-voting-config/main/prod/dynamic-voting-config.json",
			fileName:  staticConfigName,
			want:      "https://raw.githubusercontent.com/valargroup/token-holder-voting-config/main/prod/static-voting-config.json",
		},
		{
			name:      "legacy prod dynamic file URL",
			configURL: "https://raw.githubusercontent.com/valargroup/token-holder-voting-config/main/prod/dynamic-voting-config.json",
			fileName:  staticConfigName,
			want:      "https://raw.githubusercontent.com/valargroup/token-holder-voting-config/main/prod/static-voting-config.json",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := configURLForFile(tt.configURL, tt.fileName); got != tt.want {
				t.Fatalf("want %q, got %q", tt.want, got)
			}
		})
	}
}

func validCreateConfigPRRequest(t *testing.T) createConfigPRRequest {
	t.Helper()
	roundID := strings.Repeat("a", 64)
	eaPK := bytes.Repeat([]byte{1}, 32)
	entry := signedRoundEntry(t, eaPK, "valar-test")
	var eaPKArray [32]byte
	copy(eaPKArray[:], eaPK)
	hash := votingconfig.SignedPayloadHash(votingconfig.CanonicalPayloadV1(eaPKArray))
	body := createConfigPRRequest{
		RoundID:           roundID,
		Entry:             entry,
		SignedPayloadHash: hexString(hash[:]),
		Title:             "Test round",
	}
	body.Auth = signedConfigPRAuth(t, body)
	return body
}

func signedRoundEntry(t *testing.T, eaPK []byte, keyID string) votingconfig.RoundEntry {
	t.Helper()
	priv := testEd25519PrivateKey()
	var eaPKArray [32]byte
	copy(eaPKArray[:], eaPK)
	sig := votingconfig.SignV1(priv, eaPKArray)
	return votingconfig.RoundEntry{
		AuthVersion: votingconfig.AuthVersionV1,
		EaPK:        base64.StdEncoding.EncodeToString(eaPK),
		Signatures: []votingconfig.Signature{{
			KeyID: keyID,
			Alg:   votingconfig.AlgEd25519,
			Sig:   base64.StdEncoding.EncodeToString(sig),
		}},
	}
}

func signedConfigPRAuth(t *testing.T, body createConfigPRRequest) configPRAuth {
	t.Helper()
	priv := secp256k1.GenPrivKey()
	pub := priv.PubKey().(*secp256k1.PubKey)
	address, err := pubKeyToAddress(pub.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	entryHash, err := hashRoundEntry(body.Entry)
	if err != nil {
		t.Fatal(err)
	}
	payloadBytes, err := marshalConfigPRIntentPayload(configPRIntentPayload{
		Action:            configPRAction,
		RoundID:           body.RoundID,
		SignedPayloadHash: body.SignedPayloadHash,
		EntrySHA256:       entryHash,
		Timestamp:         time.Now().Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	signDoc := makeSignArbitraryDoc(address, string(payloadBytes))
	sig, err := priv.Sign(signDoc)
	if err != nil {
		t.Fatal(err)
	}
	return configPRAuth{
		SignerAddress: address,
		Payload:       string(payloadBytes),
		Signature:     base64.StdEncoding.EncodeToString(sig),
		PubKey:        base64.StdEncoding.EncodeToString(pub.Bytes()),
	}
}

func configDocuments(t *testing.T, rounds map[string]votingconfig.RoundEntry) ([]byte, []byte) {
	t.Helper()
	if rounds == nil {
		rounds = map[string]votingconfig.RoundEntry{}
	}
	cfg := votingconfig.SignedConfig{
		ConfigVersion: votingconfig.ConfigVersionV1,
		VoteServers:   []votingconfig.Endpoint{{URL: "https://vote.example", Label: "vote"}},
		PIREndpoints:  []votingconfig.Endpoint{{URL: "https://pir.example", Label: "pir"}},
		SupportedVersions: votingconfig.SupportedVersions{
			PIR:          []string{"v0"},
			VoteProtocol: "v0",
			Tally:        "v0",
			VoteServer:   "v1",
		},
		Rounds: rounds,
	}
	dynamic, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatal(err)
	}

	pub := testEd25519PrivateKey().Public().(ed25519.PublicKey)
	staticCfg := votingconfig.StaticConfig{
		StaticConfigVersion: votingconfig.StaticConfigVersionV1,
		DynamicConfigURL:    "https://example.com/dynamic-voting-config.json",
		TrustedKeys: []votingconfig.TrustedKey{{
			KeyID:  "valar-test",
			Alg:    votingconfig.AlgEd25519,
			Pubkey: base64.StdEncoding.EncodeToString(pub),
		}},
	}
	static, err := json.MarshalIndent(staticCfg, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(dynamic, '\n'), append(static, '\n')
}

func testEd25519PrivateKey() ed25519.PrivateKey {
	return ed25519.NewKeyFromSeed(bytes.Repeat([]byte{7}, ed25519.SeedSize))
}

func serveCreateConfigPR(t *testing.T, r *mux.Router, body createConfigPRRequest) *httptest.ResponseRecorder {
	t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/config-prs", bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func testConfigPRAutomation(apiURL string) configPRAutomation {
	return configPRAutomation{
		GitHubToken:      "test-token",
		Owner:            "valargroup",
		Repo:             "token-holder-voting-config",
		BaseBranch:       "main",
		APIURL:           apiURL,
		ConfigPath:       "prod",
		EnvironmentLabel: "production",
	}
}

func writeContent(t *testing.T, w http.ResponseWriter, content []byte, sha string) {
	t.Helper()
	writeJSON(t, w, map[string]string{
		"encoding": "base64",
		"content":  base64.StdEncoding.EncodeToString(content),
		"sha":      sha,
	})
}

func writeJSON(t *testing.T, w http.ResponseWriter, body any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(body); err != nil {
		t.Fatal(err)
	}
}

func hexString(data []byte) string {
	const alphabet = "0123456789abcdef"
	out := make([]byte, len(data)*2)
	for i, b := range data {
		out[i*2] = alphabet[b>>4]
		out[i*2+1] = alphabet[b&0x0f]
	}
	return string(out)
}
