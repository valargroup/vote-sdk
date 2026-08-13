package admin

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/valargroup/vote-sdk/internal/votingconfig"
)

const (
	configPRAction       = "create_config_pr"
	configPRRepoOwner    = "valargroup"
	configPRRepoName     = "token-holder-voting-config"
	configPRBaseBranch   = "main"
	configPRGitHubAPIURL = "https://api.github.com"
	dynamicConfigName    = "dynamic-voting-config.json"
	staticConfigName     = "static-voting-config.json"
)

type configPRAutomation struct {
	GitHubToken      string
	Owner            string
	Repo             string
	BaseBranch       string
	APIURL           string
	ConfigPath       string
	EnvironmentLabel string
}

func newConfigPRAutomation(cfg Config) configPRAutomation {
	out := configPRAutomation{
		GitHubToken: strings.TrimSpace(cfg.ConfigPRGitHubToken),
		Owner:       configPRRepoOwner,
		Repo:        configPRRepoName,
		BaseBranch:  configPRBaseBranch,
		APIURL:      configPRGitHubAPIURL,
	}
	out.ConfigPath, out.EnvironmentLabel = configPRPathForConfigURL(cfg.ConfigURL)
	return out
}

func configPRPathForConfigURL(configURL string) (string, string) {
	configPath := strings.TrimSpace(configURL)
	if parsed, err := url.Parse(configURL); err == nil {
		configPath = parsed.Path
	}

	// The PR endpoint has no separate static-config setting. It derives the
	// matching files from the same repo directory selected by admin.config_url.
	configDir := path.Base(strings.TrimRight(configPath, "/"))
	if configDir == dynamicConfigName || configDir == staticConfigName {
		configDir = path.Base(path.Dir(configPath))
	}
	switch configDir {
	case "prod":
		return "prod", "production"
	case "stage":
		return "stage", "staging"
	default:
		return "prod", "production"
	}
}

func configURLForFile(configURL, name string) string {
	configURL = strings.TrimSpace(configURL)
	if configURL == "" {
		return name
	}

	if parsed, err := url.Parse(configURL); err == nil && parsed.Scheme != "" {
		parsed.Path = configPathForFile(parsed.Path, name)
		parsed.RawQuery = ""
		parsed.Fragment = ""
		return parsed.String()
	}
	return configPathForFile(configURL, name)
}

func configPathForFile(configPath, name string) string {
	configPath = strings.TrimSpace(configPath)
	trimmed := strings.TrimRight(configPath, "/")
	configName := path.Base(trimmed)
	if configName == dynamicConfigName || configName == staticConfigName {
		configDir := path.Dir(trimmed)
		if path.Base(configDir) == "stage" || path.Base(configDir) == "prod" {
			return path.Join(configDir, name)
		}
		return path.Join(configDir, "prod", name)
	}
	if configName == "stage" || configName == "prod" {
		return path.Join(trimmed, name)
	}
	return path.Join(configPath, "prod", name)
}

func (c configPRAutomation) dynamicConfigPath() string {
	return c.configFilePath(dynamicConfigName)
}

func (c configPRAutomation) staticConfigPath() string {
	return c.configFilePath(staticConfigName)
}

func (c configPRAutomation) configFilePath(name string) string {
	if c.ConfigPath == "" {
		return name
	}
	return path.Join(c.ConfigPath, name)
}

func (c configPRAutomation) environmentLabel() string {
	if c.EnvironmentLabel != "" {
		return c.EnvironmentLabel
	}
	return "production"
}

func (c configPRAutomation) branchName(roundID string) string {
	env := strings.ReplaceAll(c.environmentLabel(), " ", "-")
	if env == "legacy-root" {
		return fmt.Sprintf("config-round-%s", roundID[:12])
	}
	return fmt.Sprintf("config-%s-round-%s", env, roundID[:12])
}

func (c configPRAutomation) enabled() bool {
	return c.GitHubToken != ""
}

type configPRAuth struct {
	SignerAddress string `json:"signer_address"`
	Payload       string `json:"payload"`
	Signature     string `json:"signature"`
	PubKey        string `json:"pub_key"`
}

type createConfigPRRequest struct {
	RoundID string                  `json:"round_id"`
	Entry   votingconfig.RoundEntry `json:"entry"`
	// PIRLayout the signer bound into the v2 signature (including poly_len).
	// Must match the pir_layout in the target dynamic config or the merged
	// file would carry entries that can never verify.
	PIRLayout         votingconfig.PIRLayout `json:"pir_layout"`
	SignedPayloadHash string                 `json:"signed_payload_hash"`
	Title             string                 `json:"title,omitempty"`
	Auth              configPRAuth           `json:"auth"`
}

type createConfigPRResponse struct {
	HTMLURL                 string `json:"html_url"`
	Branch                  string `json:"branch"`
	CommitSHA               string `json:"commit_sha,omitempty"`
	MergedExistingSignature bool   `json:"merged_existing_signature"`
}

type configPRIntentPayload struct {
	Action            string `json:"action"`
	RoundID           string `json:"round_id"`
	SignedPayloadHash string `json:"signed_payload_hash"`
	EntrySHA256       string `json:"entry_sha256"`
	Timestamp         int64  `json:"timestamp"`
}

func marshalConfigPRIntentPayload(payload configPRIntentPayload) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(payload); err != nil {
		return nil, err
	}
	return bytes.TrimSpace(buf.Bytes()), nil
}

func (h *apiHandler) handleCreateConfigPR(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		corsHeaders(w)
		w.WriteHeader(http.StatusNoContent)
		return
	}

	a := h.getAdmin()
	if a == nil {
		jsonError(w, "admin server not initialized", http.StatusServiceUnavailable)
		return
	}
	if !a.configPR.enabled() {
		jsonError(w, "config PR automation is not configured", http.StatusServiceUnavailable)
		return
	}

	var body createConfigPRRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if err := validateCreateConfigPRRequest(body); err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := authorizeConfigPRRequest(a, body); err != nil {
		status := http.StatusUnauthorized
		if err == errConfigPRForbidden {
			status = http.StatusForbidden
		}
		jsonError(w, err.Error(), status)
		return
	}

	resp, err := a.createConfigPR(r.Context(), body)
	if err != nil {
		h.logger.Error("create config PR", "error", err)
		jsonError(w, err.Error(), http.StatusBadGateway)
		return
	}
	jsonResponse(w, resp, http.StatusOK)
}

var errConfigPRForbidden = fmt.Errorf("signer is not a current vote manager")

func authorizeConfigPRRequest(a *Admin, body createConfigPRRequest) error {
	entryHash, err := hashRoundEntry(body.Entry)
	if err != nil {
		return err
	}

	var intent configPRIntentPayload
	if err := json.Unmarshal([]byte(body.Auth.Payload), &intent); err != nil {
		return fmt.Errorf("invalid auth payload")
	}
	if intent.Action != configPRAction ||
		intent.RoundID != body.RoundID ||
		intent.SignedPayloadHash != body.SignedPayloadHash ||
		intent.EntrySHA256 != entryHash {
		return fmt.Errorf("auth payload does not match request")
	}
	now := time.Now().Unix()
	if abs64(now-intent.Timestamp) > timestampWindowSecs {
		return fmt.Errorf("timestamp too far from server time (>5min)")
	}
	wantPayload, err := marshalConfigPRIntentPayload(intent)
	if err != nil {
		return fmt.Errorf("marshal auth payload: %w", err)
	}
	if string(wantPayload) != body.Auth.Payload {
		return fmt.Errorf("auth payload is not canonical")
	}
	if body.Auth.SignerAddress == "" || body.Auth.Signature == "" || body.Auth.PubKey == "" {
		return fmt.Errorf("missing auth fields")
	}
	if err := VerifyArbitrarySignature(body.Auth.SignerAddress, body.Auth.Payload, body.Auth.Signature, body.Auth.PubKey); err != nil {
		return err
	}
	if !a.IsVoteManager(body.Auth.SignerAddress) {
		return errConfigPRForbidden
	}
	return nil
}

func validateCreateConfigPRRequest(body createConfigPRRequest) error {
	if err := votingconfig.ValidateRoundID(body.RoundID); err != nil {
		return fmt.Errorf("round_id must be 64 lowercase hex characters")
	}
	// New entries must be auth_version 2: v1 signatures do not bind the round
	// id and are replayable across rounds.
	if body.Entry.AuthVersion != votingconfig.AuthVersionV2 {
		return fmt.Errorf("unsupported auth_version; new entries must use auth_version 2")
	}
	if body.Entry.EaPK == "" {
		return fmt.Errorf("entry.ea_pk is required")
	}
	eaPKBytes, err := votingconfig.DecodeBase64Fixed(body.Entry.EaPK, 32, "entry.ea_pk")
	if err != nil {
		return fmt.Errorf("entry.ea_pk must be base64-encoded 32 bytes")
	}
	if len(body.Entry.Signatures) == 0 {
		return fmt.Errorf("entry.signatures must contain at least one signature")
	}
	for _, sig := range body.Entry.Signatures {
		if sig.KeyID == "" {
			return fmt.Errorf("entry.signatures.key_id is required")
		}
		if sig.Alg != votingconfig.AlgEd25519 {
			return fmt.Errorf("entry.signatures.alg must be ed25519")
		}
		if _, err := votingconfig.DecodeBase64Fixed(sig.Sig, 64, "entry.signatures.sig"); err != nil {
			return fmt.Errorf("entry.signatures.sig must be base64-encoded 64 bytes")
		}
	}
	if err := votingconfig.ValidatePIRLayout(body.PIRLayout); err != nil {
		return err
	}
	var eaPK [32]byte
	copy(eaPK[:], eaPKBytes)
	payload, err := votingconfig.CanonicalPayloadV2(body.RoundID, eaPK, body.PIRLayout)
	if err != nil {
		return fmt.Errorf("failed to build canonical payload")
	}
	hash := votingconfig.SignedPayloadHash(payload)
	if body.SignedPayloadHash != hex.EncodeToString(hash[:]) {
		return fmt.Errorf("signed_payload_hash does not match round_id, entry.ea_pk, and pir_layout")
	}
	return nil
}

func hashRoundEntry(entry votingconfig.RoundEntry) (string, error) {
	data, err := json.Marshal(entry)
	if err != nil {
		return "", fmt.Errorf("marshal entry: %w", err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func (a *Admin) createConfigPR(ctx context.Context, body createConfigPRRequest) (*createConfigPRResponse, error) {
	client := newGitHubConfigClient(a.configPR)
	branch := a.configPR.branchName(body.RoundID)
	dynamicPath := a.configPR.dynamicConfigPath()
	staticPath := a.configPR.staticConfigPath()

	mainSHA, err := client.getRefSHA(ctx, a.configPR.BaseBranch)
	if err != nil {
		return nil, err
	}
	branchExists := false
	if err := client.createRef(ctx, branch, mainSHA); err != nil {
		if ghErr, ok := err.(*githubAPIError); ok && ghErr.Status == http.StatusUnprocessableEntity {
			branchExists = true
		} else {
			return nil, err
		}
	}

	dynamicContent, _, err := client.getContent(ctx, dynamicPath, a.configPR.BaseBranch)
	if err != nil {
		return nil, err
	}
	staticContent, _, err := client.getContent(ctx, staticPath, a.configPR.BaseBranch)
	if err != nil {
		return nil, err
	}

	mergedContent, mergedExisting, resolvedKeyIDs, err := mergeConfigPREntry(dynamicContent, staticContent, body.RoundID, body.Entry, body.PIRLayout)
	if err != nil {
		return nil, err
	}

	_, branchFileSHA, err := client.getContent(ctx, dynamicPath, branch)
	if err != nil {
		if branchExists {
			return nil, err
		}
		_, branchFileSHA, err = client.getContent(ctx, dynamicPath, a.configPR.BaseBranch)
		if err != nil {
			return nil, err
		}
	}

	message := fmt.Sprintf("Add signed config entry for round %s", body.RoundID[:12])
	commitSHA, err := client.updateContent(ctx, dynamicPath, branch, branchFileSHA, message, mergedContent)
	if err != nil {
		if ghErr, ok := err.(*githubAPIError); !ok || ghErr.Status != http.StatusUnprocessableEntity {
			return nil, err
		}
	}

	prBody := configPRBody(body, mergedExisting, resolvedKeyIDs, a.configPR)
	pr, err := client.createPullRequest(ctx, branch, a.configPR.BaseBranch, configPRTitle(body), prBody)
	if err != nil {
		if ghErr, ok := err.(*githubAPIError); ok && ghErr.Status == http.StatusUnprocessableEntity {
			pr, err = client.findOpenPullRequest(ctx, branch, a.configPR.BaseBranch)
		}
		if err != nil {
			return nil, err
		}
	}
	return &createConfigPRResponse{
		HTMLURL:                 pr.HTMLURL,
		Branch:                  branch,
		CommitSHA:               commitSHA,
		MergedExistingSignature: mergedExisting,
	}, nil
}

func mergeConfigPREntry(dynamicContent, staticContent []byte, roundID string, entry votingconfig.RoundEntry, signedLayout votingconfig.PIRLayout) ([]byte, bool, []string, error) {
	var cfg votingconfig.SignedConfig
	if err := json.Unmarshal(dynamicContent, &cfg); err != nil {
		return nil, false, nil, fmt.Errorf("parse dynamic-voting-config.json: %w", err)
	}
	if err := votingconfig.ValidateWrapper(&cfg); err != nil {
		return nil, false, nil, err
	}
	// The layout the signer attested must be the layout this file advertises;
	// otherwise the merged entry could never verify against the file.
	if cfg.PIRLayout != signedLayout {
		return nil, false, nil, fmt.Errorf(
			"pir_layout mismatch: signed %d/%d/%d poly_len=%d but dynamic config advertises %d/%d/%d poly_len=%d",
			signedLayout.PIRDepth, signedLayout.Tier0Layers, signedLayout.Tier1Layers, signedLayout.PolyLen,
			cfg.PIRLayout.PIRDepth, cfg.PIRLayout.Tier0Layers, cfg.PIRLayout.Tier1Layers, cfg.PIRLayout.PolyLen,
		)
	}

	var staticCfg votingconfig.StaticConfig
	if err := json.Unmarshal(staticContent, &staticCfg); err != nil {
		return nil, false, nil, fmt.Errorf("parse static-voting-config.json: %w", err)
	}
	if err := votingconfig.ValidateStaticConfig(&staticCfg); err != nil {
		return nil, false, nil, err
	}

	entry, err := resolveConfigPREntrySignatureKeyIDs(roundID, entry, staticCfg.TrustedKeys, cfg.PIRLayout)
	if err != nil {
		return nil, false, nil, err
	}
	resolvedKeyIDs := make([]string, 0, len(entry.Signatures))
	for _, sig := range entry.Signatures {
		resolvedKeyIDs = append(resolvedKeyIDs, sig.KeyID)
	}

	mergedExisting := false
	if cfg.Rounds == nil {
		cfg.Rounds = map[string]votingconfig.RoundEntry{}
	}
	if existing, ok := cfg.Rounds[roundID]; ok {
		if existing.EaPK != entry.EaPK {
			return nil, false, nil, fmt.Errorf("round %s: ea_pk mismatch in merge target", roundID)
		}
		switch existing.AuthVersion {
		case votingconfig.AuthVersionV1:
			// Legacy v1 signatures cover a different preimage; replace the
			// entry outright instead of merging incompatible signatures.
			mergedExisting = true
		case entry.AuthVersion:
			entry.Signatures = mergeConfigPRSignatures(existing.Signatures, entry.Signatures)
			mergedExisting = true
		default:
			return nil, false, nil, fmt.Errorf("round %s: cannot merge into auth_version %d", roundID, existing.AuthVersion)
		}
	}
	cfg.Rounds[roundID] = entry

	// Mixed v1/v2 files remain valid during migration: VerifyEntrySignatures
	// dispatches on each entry's auth_version.
	for roundID, entry := range cfg.Rounds {
		if !votingconfig.VerifyEntrySignatures(roundID, entry, staticCfg.TrustedKeys, cfg.PIRLayout) {
			return nil, false, nil, fmt.Errorf("round %s: no valid signature", roundID)
		}
	}

	data, err := json.MarshalIndent(&cfg, "", "  ")
	if err != nil {
		return nil, false, nil, fmt.Errorf("marshal dynamic-voting-config.json: %w", err)
	}
	return append(data, '\n'), mergedExisting, resolvedKeyIDs, nil
}

func resolveConfigPREntrySignatureKeyIDs(roundID string, entry votingconfig.RoundEntry, trusted []votingconfig.TrustedKey, layout votingconfig.PIRLayout) (votingconfig.RoundEntry, error) {
	entryEaPK, err := votingconfig.DecodeBase64Fixed(entry.EaPK, 32, "entry.ea_pk")
	if err != nil {
		return entry, fmt.Errorf("entry.ea_pk must be base64-encoded 32 bytes")
	}
	var eaPK [32]byte
	copy(eaPK[:], entryEaPK)
	payload, err := votingconfig.CanonicalPayload(entry.AuthVersion, roundID, eaPK, layout)
	if err != nil {
		return entry, fmt.Errorf("failed to build canonical payload")
	}

	resolved := entry
	resolved.Signatures = append([]votingconfig.Signature(nil), entry.Signatures...)
	for i, sig := range resolved.Signatures {
		if sig.Alg != votingconfig.AlgEd25519 {
			continue
		}
		if keyID, ok := resolveConfigPRSignatureKeyID(sig, trusted, payload); ok {
			resolved.Signatures[i].KeyID = keyID
		}
	}
	return resolved, nil
}

func resolveConfigPRSignatureKeyID(sig votingconfig.Signature, trusted []votingconfig.TrustedKey, payload []byte) (string, bool) {
	for _, key := range trusted {
		if key.KeyID == sig.KeyID && configPRSignatureMatchesTrustedKey(sig, key, payload) {
			return key.KeyID, true
		}
	}
	for _, key := range trusted {
		if configPRSignatureMatchesTrustedKey(sig, key, payload) {
			return key.KeyID, true
		}
	}
	return "", false
}

func configPRSignatureMatchesTrustedKey(sig votingconfig.Signature, key votingconfig.TrustedKey, payload []byte) bool {
	if sig.Alg != votingconfig.AlgEd25519 || key.Alg != votingconfig.AlgEd25519 {
		return false
	}
	pub, err := votingconfig.DecodeBase64Fixed(key.Pubkey, ed25519.PublicKeySize, "trusted_keys.pubkey")
	if err != nil {
		return false
	}
	sigBytes, err := votingconfig.DecodeBase64Fixed(sig.Sig, ed25519.SignatureSize, "signatures.sig")
	if err != nil {
		return false
	}
	return ed25519.Verify(ed25519.PublicKey(pub), payload, sigBytes)
}

func mergeConfigPRSignatures(existing, incoming []votingconfig.Signature) []votingconfig.Signature {
	merged := append([]votingconfig.Signature(nil), existing...)
	for _, sig := range incoming {
		replaced := false
		for i := range merged {
			if merged[i].KeyID == sig.KeyID {
				merged[i] = sig
				replaced = true
				break
			}
		}
		if !replaced {
			merged = append(merged, sig)
		}
	}
	return merged
}

func configPRTitle(body createConfigPRRequest) string {
	if strings.TrimSpace(body.Title) != "" {
		return fmt.Sprintf("Add signed config entry for %s", strings.TrimSpace(body.Title))
	}
	return fmt.Sprintf("Add signed config entry for round %s", body.RoundID[:12])
}

func configPRBody(body createConfigPRRequest, mergedExisting bool, trustedKeyIDs []string, automation configPRAutomation) string {
	keyIDsLine := strings.Join(trustedKeyIDs, ", ")
	if keyIDsLine == "" {
		keyIDsLine = "(unresolved)"
	}
	mergeNote := "new round entry"
	if mergedExisting {
		mergeNote = "merged with existing round entry"
	}
	return fmt.Sprintf(`## Summary
- Add signed %s dynamic config entry for round %s.
- Target file: %s.
- Trusted key IDs (from %s) attesting this entry: %s.
- Authenticated vote manager: %s.
- Merge mode: %s.

## Reviewer check
- signed_payload_hash: %s
- CI will verify %s against %s.
`, automation.environmentLabel(), body.RoundID, automation.dynamicConfigPath(), automation.staticConfigPath(), keyIDsLine, body.Auth.SignerAddress, mergeNote, body.SignedPayloadHash, automation.dynamicConfigPath(), automation.staticConfigPath())
}

type githubConfigClient struct {
	cfg        configPRAutomation
	httpClient *http.Client
}

func newGitHubConfigClient(cfg configPRAutomation) *githubConfigClient {
	return &githubConfigClient{
		cfg:        cfg,
		httpClient: &http.Client{Timeout: 20 * time.Second},
	}
}

type githubAPIError struct {
	Status  int
	Message string
}

func (e *githubAPIError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("GitHub API HTTP %d: %s", e.Status, e.Message)
	}
	return fmt.Sprintf("GitHub API HTTP %d", e.Status)
}

func (c *githubConfigClient) getContent(ctx context.Context, path, ref string) ([]byte, string, error) {
	var resp struct {
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
		SHA      string `json:"sha"`
	}
	if err := c.doJSON(ctx, http.MethodGet, c.repoPath("contents/"+path), url.Values{"ref": {ref}}, nil, &resp); err != nil {
		return nil, "", err
	}
	if resp.Encoding != "base64" {
		return nil, "", fmt.Errorf("GitHub content %s uses unsupported encoding %q", path, resp.Encoding)
	}
	content, err := base64.StdEncoding.DecodeString(removeBase64Whitespace(resp.Content))
	if err != nil {
		return nil, "", fmt.Errorf("decode GitHub content %s: %w", path, err)
	}
	return content, resp.SHA, nil
}

func (c *githubConfigClient) getRefSHA(ctx context.Context, branch string) (string, error) {
	var resp struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	if err := c.doJSON(ctx, http.MethodGet, c.repoPath("git/ref/heads/"+branch), nil, nil, &resp); err != nil {
		return "", err
	}
	if resp.Object.SHA == "" {
		return "", fmt.Errorf("GitHub ref %s has empty sha", branch)
	}
	return resp.Object.SHA, nil
}

func (c *githubConfigClient) createRef(ctx context.Context, branch, sha string) error {
	body := map[string]string{
		"ref": "refs/heads/" + branch,
		"sha": sha,
	}
	return c.doJSON(ctx, http.MethodPost, c.repoPath("git/refs"), nil, body, nil)
}

func (c *githubConfigClient) updateContent(ctx context.Context, path, branch, sha, message string, content []byte) (string, error) {
	body := map[string]string{
		"message": message,
		"content": base64.StdEncoding.EncodeToString(content),
		"branch":  branch,
		"sha":     sha,
	}
	var resp struct {
		Commit struct {
			SHA string `json:"sha"`
		} `json:"commit"`
	}
	if err := c.doJSON(ctx, http.MethodPut, c.repoPath("contents/"+path), nil, body, &resp); err != nil {
		return "", err
	}
	return resp.Commit.SHA, nil
}

type githubPullRequest struct {
	HTMLURL string `json:"html_url"`
}

func (c *githubConfigClient) createPullRequest(ctx context.Context, head, base, title, body string) (*githubPullRequest, error) {
	req := map[string]string{
		"title": title,
		"head":  head,
		"base":  base,
		"body":  body,
	}
	var resp githubPullRequest
	if err := c.doJSON(ctx, http.MethodPost, c.repoPath("pulls"), nil, req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *githubConfigClient) findOpenPullRequest(ctx context.Context, head, base string) (*githubPullRequest, error) {
	query := url.Values{
		"state": {"open"},
		"head":  {c.cfg.Owner + ":" + head},
		"base":  {base},
	}
	var pulls []githubPullRequest
	if err := c.doJSON(ctx, http.MethodGet, c.repoPath("pulls"), query, nil, &pulls); err != nil {
		return nil, err
	}
	if len(pulls) == 0 || pulls[0].HTMLURL == "" {
		return nil, fmt.Errorf("existing pull request for branch %s was not found", head)
	}
	return &pulls[0], nil
}

func (c *githubConfigClient) repoPath(path string) string {
	return "/repos/" + url.PathEscape(c.cfg.Owner) + "/" + url.PathEscape(c.cfg.Repo) + "/" + path
}

func (c *githubConfigClient) doJSON(ctx context.Context, method, path string, query url.Values, body interface{}, out interface{}) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}
	endpoint := c.cfg.APIURL + path
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+c.cfg.GitHubToken)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var parsed struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(respBody, &parsed)
		return &githubAPIError{Status: resp.StatusCode, Message: parsed.Message}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(respBody, out); err != nil {
		return fmt.Errorf("decode GitHub API response: %w", err)
	}
	return nil
}

func removeBase64Whitespace(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, ch := range s {
		switch ch {
		case '\n', '\r', '\t', ' ':
			continue
		default:
			b.WriteRune(ch)
		}
	}
	return b.String()
}
