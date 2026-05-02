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
)

type configPRAutomation struct {
	GitHubToken string
	Owner       string
	Repo        string
	BaseBranch  string
	APIURL      string
}

func newConfigPRAutomation(cfg Config) configPRAutomation {
	out := configPRAutomation{
		GitHubToken: strings.TrimSpace(cfg.ConfigPRGitHubToken),
		Owner:       configPRRepoOwner,
		Repo:        configPRRepoName,
		BaseBranch:  configPRBaseBranch,
		APIURL:      configPRGitHubAPIURL,
	}
	return out
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
	RoundID           string                  `json:"round_id"`
	Entry             votingconfig.RoundEntry `json:"entry"`
	SignedPayloadHash string                  `json:"signed_payload_hash"`
	Title             string                  `json:"title,omitempty"`
	Auth              configPRAuth            `json:"auth"`
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
	if body.Entry.AuthVersion != votingconfig.AuthVersionV1 {
		return fmt.Errorf("unsupported auth_version")
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
	var eaPK [32]byte
	copy(eaPK[:], eaPKBytes)
	hash := votingconfig.SignedPayloadHash(votingconfig.CanonicalPayloadV1(eaPK))
	if body.SignedPayloadHash != hex.EncodeToString(hash[:]) {
		return fmt.Errorf("signed_payload_hash does not match entry.ea_pk")
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
	branch := fmt.Sprintf("config-round-%s", body.RoundID[:12])

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

	dynamicContent, _, err := client.getContent(ctx, "dynamic-voting-config.json", a.configPR.BaseBranch)
	if err != nil {
		return nil, err
	}
	staticContent, _, err := client.getContent(ctx, "static-voting-config.json", a.configPR.BaseBranch)
	if err != nil {
		return nil, err
	}

	mergedContent, mergedExisting, err := mergeConfigPREntry(dynamicContent, staticContent, body.RoundID, body.Entry)
	if err != nil {
		return nil, err
	}

	_, branchFileSHA, err := client.getContent(ctx, "dynamic-voting-config.json", branch)
	if err != nil {
		if branchExists {
			return nil, err
		}
		_, branchFileSHA, err = client.getContent(ctx, "dynamic-voting-config.json", a.configPR.BaseBranch)
		if err != nil {
			return nil, err
		}
	}

	message := fmt.Sprintf("Add signed config entry for round %s", body.RoundID[:12])
	commitSHA, err := client.updateContent(ctx, "dynamic-voting-config.json", branch, branchFileSHA, message, mergedContent)
	if err != nil {
		if ghErr, ok := err.(*githubAPIError); !ok || ghErr.Status != http.StatusUnprocessableEntity {
			return nil, err
		}
	}

	prBody := configPRBody(body, mergedExisting)
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

func mergeConfigPREntry(dynamicContent, staticContent []byte, roundID string, entry votingconfig.RoundEntry) ([]byte, bool, error) {
	var cfg votingconfig.SignedConfig
	if err := json.Unmarshal(dynamicContent, &cfg); err != nil {
		return nil, false, fmt.Errorf("parse dynamic-voting-config.json: %w", err)
	}
	if err := votingconfig.ValidateWrapper(&cfg); err != nil {
		return nil, false, err
	}

	var staticCfg votingconfig.StaticConfig
	if err := json.Unmarshal(staticContent, &staticCfg); err != nil {
		return nil, false, fmt.Errorf("parse static-voting-config.json: %w", err)
	}
	if err := votingconfig.ValidateStaticConfig(&staticCfg); err != nil {
		return nil, false, err
	}

	entry, err := resolveConfigPREntrySignatureKeyIDs(entry, staticCfg.TrustedKeys)
	if err != nil {
		return nil, false, err
	}

	mergedExisting := false
	if cfg.Rounds == nil {
		cfg.Rounds = map[string]votingconfig.RoundEntry{}
	}
	if existing, ok := cfg.Rounds[roundID]; ok {
		if existing.AuthVersion != votingconfig.AuthVersionV1 {
			return nil, false, fmt.Errorf("round %s: cannot merge into auth_version %d", roundID, existing.AuthVersion)
		}
		if existing.EaPK != entry.EaPK {
			return nil, false, fmt.Errorf("round %s: ea_pk mismatch in merge target", roundID)
		}
		entry.Signatures = mergeConfigPRSignatures(existing.Signatures, entry.Signatures)
		mergedExisting = true
	}
	cfg.Rounds[roundID] = entry

	for roundID, entry := range cfg.Rounds {
		if !votingconfig.VerifyEntrySignatures(entry, staticCfg.TrustedKeys) {
			return nil, false, fmt.Errorf("round %s: no valid signature", roundID)
		}
	}

	data, err := json.MarshalIndent(&cfg, "", "  ")
	if err != nil {
		return nil, false, fmt.Errorf("marshal dynamic-voting-config.json: %w", err)
	}
	return append(data, '\n'), mergedExisting, nil
}

func resolveConfigPREntrySignatureKeyIDs(entry votingconfig.RoundEntry, trusted []votingconfig.TrustedKey) (votingconfig.RoundEntry, error) {
	entryEaPK, err := votingconfig.DecodeBase64Fixed(entry.EaPK, 32, "entry.ea_pk")
	if err != nil {
		return entry, fmt.Errorf("entry.ea_pk must be base64-encoded 32 bytes")
	}
	var eaPK [32]byte
	copy(eaPK[:], entryEaPK)

	resolved := entry
	resolved.Signatures = append([]votingconfig.Signature(nil), entry.Signatures...)
	for i, sig := range resolved.Signatures {
		if sig.Alg != votingconfig.AlgEd25519 {
			continue
		}
		if keyID, ok := resolveConfigPRSignatureKeyID(sig, trusted, eaPK); ok {
			resolved.Signatures[i].KeyID = keyID
		}
	}
	return resolved, nil
}

func resolveConfigPRSignatureKeyID(sig votingconfig.Signature, trusted []votingconfig.TrustedKey, eaPK [32]byte) (string, bool) {
	for _, key := range trusted {
		if key.KeyID == sig.KeyID && configPRSignatureMatchesTrustedKey(sig, key, eaPK) {
			return key.KeyID, true
		}
	}
	for _, key := range trusted {
		if configPRSignatureMatchesTrustedKey(sig, key, eaPK) {
			return key.KeyID, true
		}
	}
	return "", false
}

func configPRSignatureMatchesTrustedKey(sig votingconfig.Signature, key votingconfig.TrustedKey, eaPK [32]byte) bool {
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
	return votingconfig.VerifyV1(ed25519.PublicKey(pub), eaPK, sigBytes)
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

func configPRBody(body createConfigPRRequest, mergedExisting bool) string {
	keyIDs := make([]string, 0, len(body.Entry.Signatures))
	for _, sig := range body.Entry.Signatures {
		keyIDs = append(keyIDs, sig.KeyID)
	}
	mergeNote := "new round entry"
	if mergedExisting {
		mergeNote = "merged with existing round entry"
	}
	return fmt.Sprintf(`## Summary
- Add signed dynamic config entry for round %s.
- Config signer key ID(s): %s.
- Authenticated vote manager: %s.
- Merge mode: %s.

## Reviewer check
- signed_payload_hash: %s
- CI will verify dynamic-voting-config.json against static-voting-config.json.
`, body.RoundID, strings.Join(keyIDs, ", "), body.Auth.SignerAddress, mergeNote, body.SignedPayloadHash)
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
