package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/valargroup/vote-sdk/internal/pirupdate"
)

type pirProposal struct {
	CurrentConfig string            `json:"current_config"`
	Scope         string            `json:"scope"`
	Network       string            `json:"network"`
	BaseSHA       string            `json:"base_sha"`
	Config        string            `json:"config"`
	Payload       pirupdate.Payload `json:"payload"`
	Keys          []pirupdate.Key   `json:"keys"`
}
type pirPRRequest struct {
	BaseSHA      string                 `json:"base_sha"`
	Config       string                 `json:"config"`
	Attestations pirupdate.Attestations `json:"attestations"`
}

// fetchPIRArtifact only receives URLs assembled from fixed origins and validated selectors.
func fetchPIRArtifact(ctx context.Context, target string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if req.URL.Scheme != "https" || len(via) > 5 {
			return fmt.Errorf("unsafe artifact redirect")
		}
		return nil
	}}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("artifact download failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("artifact returned HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1048577))
	if err != nil {
		return nil, err
	}
	if len(data) > 1048576 {
		return nil, fmt.Errorf("artifact metadata too large")
	}
	return data, nil
}
func resolvePIRPayload(ctx context.Context, raw []byte, cfg pirupdate.Config, network string) (pirupdate.Payload, error) {
	p := pirupdate.Payload{ConfigSHA256: pirupdate.Hash(raw)}
	sums, err := fetchPIRArtifact(ctx, "https://github.com/valargroup/vote-nullifier-pir/releases/download/"+cfg.BinaryTag+"/SHA256SUMS")
	if err != nil {
		return p, err
	}
	hashes := map[string]string{}
	for _, line := range strings.Split(string(sums), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 {
			if _, ok := hashes[fields[1]]; ok {
				return p, fmt.Errorf("duplicate checksum entry")
			}
			hashes[fields[1]] = fields[0]
		}
	}
	p.LinuxAMD64SHA256 = hashes["nf-server-linux-amd64"]
	p.LinuxARM64SHA256 = hashes["nf-server-linux-arm64"]
	p.ServiceSHA256 = hashes["nullifier-query-server.service"]
	manifest, err := fetchPIRArtifact(ctx, fmt.Sprintf("https://shielded-vote.nyc3.digitaloceanspaces.com/snapshots/%s/%d/manifest.json", network, cfg.SnapshotHeight))
	if err != nil {
		return p, err
	}
	var m struct {
		SchemaVersion int    `json:"schema_version"`
		Height        uint64 `json:"height"`
	}
	if err = json.Unmarshal(manifest, &m); err != nil || m.SchemaVersion != 2 || m.Height != cfg.SnapshotHeight {
		return p, fmt.Errorf("snapshot manifest identity mismatch")
	}
	p.SnapshotManifestSHA256 = pirupdate.Hash(manifest)
	_, err = pirupdate.SigningBytes("prod", p)
	return p, err
}
func (h *apiHandler) handlePIRUpdateProposal(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		corsHeaders(w)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	a := h.getAdmin()
	if a == nil || !a.configPR.enabled() {
		jsonError(w, "config PR automation unavailable", 503)
		return
	}
	var cfg pirupdate.Config
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 65536)).Decode(&cfg); err != nil {
		jsonError(w, "invalid request", 400)
		return
	}
	if err := pirupdate.ValidateConfig(cfg); err != nil {
		jsonError(w, err.Error(), 400)
		return
	}
	network := strings.ToLower(strings.TrimSpace(os.Getenv(zcashNetworkEnv)))
	if network != "main" && network != "test" {
		jsonError(w, "admin Zcash network is not configured", 503)
		return
	}
	scope := a.configPR.ConfigPath
	if scope != "prod" && scope != "stage" {
		jsonError(w, "admin PIR scope is ambiguous", 503)
		return
	}
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		jsonError(w, "encode config", 500)
		return
	}
	raw = append(raw, '\n')
	p, err := resolvePIRPayload(r.Context(), raw, cfg, network)
	if err != nil {
		jsonError(w, err.Error(), 502)
		return
	}
	client := newGitHubConfigClient(a.configPR)
	current, sha, err := client.getContent(r.Context(), a.configPR.configFilePath("pir.json"), a.configPR.BaseBranch)
	if err != nil {
		jsonError(w, err.Error(), 502)
		return
	}
	jsonResponse(w, pirProposal{CurrentConfig: string(current), Scope: scope, Network: network, BaseSHA: sha, Config: string(raw), Payload: p, Keys: pirupdate.TrustedKeys(scope)}, 200)
}
func (h *apiHandler) handlePIRUpdatePR(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		corsHeaders(w)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	a := h.getAdmin()
	if a == nil || !a.configPR.enabled() {
		jsonError(w, "config PR automation unavailable", 503)
		return
	}
	var body pirPRRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 131072))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		jsonError(w, "invalid request", 400)
		return
	}
	scope := a.configPR.ConfigPath
	cfg, err := pirupdate.Verify([]byte(body.Config), body.Attestations, scope)
	if err != nil {
		jsonError(w, err.Error(), 403)
		return
	}
	network := strings.ToLower(strings.TrimSpace(os.Getenv(zcashNetworkEnv)))
	if network != "main" && network != "test" {
		jsonError(w, "admin Zcash network is not configured", 503)
		return
	}
	resolved, err := resolvePIRPayload(r.Context(), []byte(body.Config), cfg, network)
	if err != nil || resolved != body.Attestations.Payload {
		jsonError(w, "published artifact hashes changed; review and sign again", 409)
		return
	}
	result, err := a.createPIRUpdatePR(r.Context(), body, cfg)
	if err != nil {
		jsonError(w, err.Error(), 409)
		return
	}
	jsonResponse(w, result, 200)
}

// createPIRUpdatePR places both files in one tree/commit; signed bytes are never reserialized.
func (a *Admin) createPIRUpdatePR(ctx context.Context, body pirPRRequest, cfg pirupdate.Config) (*createConfigPRResponse, error) {
	c := newGitHubConfigClient(a.configPR)
	head, err := c.getRefSHA(ctx, a.configPR.BaseBranch)
	if err != nil {
		return nil, err
	}
	_, sha, err := c.getContent(ctx, a.configPR.configFilePath("pir.json"), head)
	if err != nil {
		return nil, err
	}
	if sha != body.BaseSHA {
		return nil, fmt.Errorf("PIR config changed; review and sign a new proposal")
	}
	att, err := json.MarshalIndent(body.Attestations, "", "  ")
	if err != nil {
		return nil, err
	}
	att = append(att, '\n')
	id := pirupdate.Hash(append([]byte(body.Config), att...))[:20]
	branch := "pir-update-" + a.configPR.ConfigPath + "-" + id
	branchExists := false
	if _, err := c.getRefSHA(ctx, branch); err == nil {
		branchExists = true
		for name, expected := range map[string][]byte{"pir.json": []byte(body.Config), "pir_attestations.json": att} {
			actual, _, err := c.getContent(ctx, a.configPR.configFilePath(name), branch)
			if err != nil || !bytes.Equal(actual, expected) {
				return nil, fmt.Errorf("existing PIR proposal branch changed; refusing to overwrite")
			}
		}
		if pr, err := c.findOpenPullRequest(ctx, branch, a.configPR.BaseBranch); err == nil && pr != nil {
			return &createConfigPRResponse{HTMLURL: pr.HTMLURL, Branch: branch}, nil
		}
	} else if ghErr, ok := err.(*githubAPIError); !ok || ghErr.Status != http.StatusNotFound {
		return nil, err
	}
	var commit struct {
		Tree struct {
			SHA string `json:"sha"`
		} `json:"tree"`
	}
	if err := c.doJSON(ctx, http.MethodGet, c.repoPath("git/commits/"+head), nil, nil, &commit); err != nil {
		return nil, err
	}
	var tree struct {
		SHA string `json:"sha"`
	}
	entries := []map[string]string{
		{"path": a.configPR.configFilePath("pir.json"), "mode": "100644", "type": "blob", "content": body.Config},
		{"path": a.configPR.configFilePath("pir_attestations.json"), "mode": "100644", "type": "blob", "content": string(att)},
	}
	if err := c.doJSON(ctx, http.MethodPost, c.repoPath("git/trees"), nil, map[string]any{"base_tree": commit.Tree.SHA, "tree": entries}, &tree); err != nil {
		return nil, err
	}
	var created struct {
		SHA string `json:"sha"`
	}
	title := fmt.Sprintf("Authorize PIR %s at snapshot %d (%s)", cfg.BinaryTag, cfg.SnapshotHeight, a.configPR.ConfigPath)
	if err := c.doJSON(ctx, http.MethodPost, c.repoPath("git/commits"), nil, map[string]any{"message": title, "tree": tree.SHA, "parents": []string{head}}, &created); err != nil {
		return nil, err
	}
	if err := func() error {
		if branchExists {
			return nil
		}
		return c.createRef(ctx, branch, created.SHA)
	}(); err != nil {
		return nil, err
	}
	pr, err := c.createPullRequest(ctx, branch, a.configPR.BaseBranch, title, "Coordinator-signed PIR update. Both files are committed together; hosts verify the config and artifact hashes before activation.\n\nNo expiration or replay counter is used. Previously signed targets remain valid.")
	if err != nil {
		return nil, err
	}
	return &createConfigPRResponse{HTMLURL: pr.HTMLURL, Branch: branch, CommitSHA: created.SHA}, nil
}
