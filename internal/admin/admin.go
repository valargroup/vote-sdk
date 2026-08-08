package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"cosmossdk.io/log"
)

// ValidatorChecker returns whether a validator with the given valoper bech32
// exists in staking state.
type ValidatorChecker func(valoper string) bool

// VoteManagerChecker returns whether the given account address is a current
// vote manager on the chain.
type VoteManagerChecker func(address string) bool

// Admin fetches and caches voting-config from the CDN and stores pending
// validator registrations in SQLite.
type Admin struct {
	configURL            string
	configPR             configPRAutomation
	logger               log.Logger
	store                *Store
	checkValidatorExists ValidatorChecker
	checkVoteManager     VoteManagerChecker

	mu     sync.RWMutex
	cached *VotingConfig
}

// New creates a new Admin from the given configuration.
// homeDir is used to resolve default DBPath when cfg.DBPath is empty.
// checkValidatorExists and checkVoteManager may be nil; in that case those
// authorization checks always return false.
func New(cfg Config, homeDir string, checkValidatorExists ValidatorChecker, checkVoteManager VoteManagerChecker, logger log.Logger) (*Admin, error) {
	logger = logger.With("module", "admin")

	if cfg.Disable {
		logger.Info("admin server disabled")
		return nil, nil
	}

	configURL := cfg.ConfigURL
	if configURL == "" {
		configURL = DefaultConfig().ConfigURL
	}
	configURL = configURLForFile(configURL, staticConfigName)
	cfg = applyConfigPREnvDefaults(cfg)

	dbPath := cfg.DBPath
	if dbPath == "" {
		dbPath = filepath.Join(homeDir, "admin.db")
	}

	store, err := NewStore(dbPath)
	if err != nil {
		return nil, fmt.Errorf("create admin store: %w", err)
	}

	a := &Admin{
		configURL:            configURL,
		configPR:             newConfigPRAutomation(cfg),
		logger:               logger,
		store:                store,
		checkValidatorExists: checkValidatorExists,
		checkVoteManager:     checkVoteManager,
	}

	if err := a.refresh(); err != nil {
		logger.Error("initial config fetch failed, will retry", "error", err)
	}

	return a, nil
}

func applyConfigPREnvDefaults(cfg Config) Config {
	if cfg.ConfigPRGitHubToken == "" {
		cfg.ConfigPRGitHubToken = os.Getenv("SVOTE_CONFIG_PR_GITHUB_TOKEN")
	}
	return cfg
}

// Store returns the SQLite store (never nil when Admin is non-nil).
func (a *Admin) Store() *Store {
	return a.store
}

// Close releases the SQLite store.
func (a *Admin) Close() error {
	if a == nil || a.store == nil {
		return nil
	}
	return a.store.Close()
}

// ValidatorExists reports whether the operator account (bech32) has a staking
// validator record. It converts the account address to its valoper form before
// checking staking state.
func (a *Admin) ValidatorExists(operatorAddress string) bool {
	if a.checkValidatorExists == nil {
		return false
	}
	valoper, err := AddressToValoper(operatorAddress)
	if err != nil {
		return false
	}
	return a.checkValidatorExists(valoper)
}

// IsVoteManager reports whether address is in the current chain vote-manager set.
func (a *Admin) IsVoteManager(address string) bool {
	if a.checkVoteManager == nil {
		return false
	}
	return a.checkVoteManager(address)
}

// GetVotingConfig returns the cached voting config, refreshing if stale.
func (a *Admin) GetVotingConfig() (*VotingConfig, error) {
	a.mu.RLock()
	cfg := a.cached
	a.mu.RUnlock()

	if cfg != nil {
		return cfg, nil
	}

	if err := a.refresh(); err != nil {
		return nil, err
	}

	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.cached, nil
}

// RunConfigRefresher periodically re-fetches voting-config so the cached
// GET /api/voting-config response stays warm and picks up newly added or
// removed servers without a process restart. Blocks until ctx is cancelled.
//
// This used to be a side effect of the in-process fleet health watchdog,
// which now lives in vote-infrastructure/watchdog/ as a standalone Rust
// service. The refresher remains in-process because the cached endpoint
// only matters when the admin HTTP server is up.
func RunConfigRefresher(ctx context.Context, a *Admin, interval time.Duration, logger log.Logger) {
	if a == nil || interval <= 0 {
		return
	}
	logger.Info("voting-config refresher started", "interval", interval.String())

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			logger.Info("voting-config refresher stopped")
			return
		case <-ticker.C:
			if err := a.refresh(); err != nil {
				logger.Error("voting-config refresh failed", "error", err)
			}
		}
	}
}

func (a *Admin) refresh() error {
	staticURL, err := parseConfigURL(a.configURL)
	if err != nil {
		return fmt.Errorf("invalid static config URL: %w", err)
	}
	client := &http.Client{
		Timeout: 10 * time.Second,
		CheckRedirect: func(req *http.Request, _ []*http.Request) error {
			if !sameOrigin(staticURL, req.URL) {
				return fmt.Errorf("config redirect crosses origin: %s", req.URL.Redacted())
			}
			return nil
		},
	}
	body, err := fetchConfigBody(client, a.configURL)
	if err != nil {
		return err
	}

	var static struct {
		DynamicConfigURL string `json:"dynamic_config_url"`
	}
	if err := json.Unmarshal(body, &static); err != nil {
		return fmt.Errorf("decode config: %w", err)
	}
	if dynamicURL := strings.TrimSpace(static.DynamicConfigURL); dynamicURL != "" {
		parsedDynamicURL, err := parseConfigURL(dynamicURL)
		if err != nil {
			return fmt.Errorf("invalid dynamic config URL: %w", err)
		}
		if !sameOrigin(staticURL, parsedDynamicURL) {
			return fmt.Errorf("dynamic config URL crosses origin: %s", parsedDynamicURL.Redacted())
		}
		body, err = fetchConfigBody(client, dynamicURL)
		if err != nil {
			return fmt.Errorf("fetch dynamic config: %w", err)
		}
	}

	var cfg VotingConfig
	if err := json.Unmarshal(body, &cfg); err != nil {
		return fmt.Errorf("decode dynamic config: %w", err)
	}

	a.mu.Lock()
	a.cached = &cfg
	a.mu.Unlock()

	a.logger.Info("voting config loaded", "vote_servers", len(cfg.VoteServers), "pir_endpoints", len(cfg.PIRServers))
	return nil
}

func parseConfigURL(rawURL string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return nil, err
	}
	if (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" {
		return nil, fmt.Errorf("must be an absolute HTTP(S) URL")
	}
	return parsed, nil
}

func sameOrigin(a, b *url.URL) bool {
	return strings.EqualFold(a.Scheme, b.Scheme) &&
		strings.EqualFold(a.Hostname(), b.Hostname()) &&
		effectivePort(a) == effectivePort(b)
}

func effectivePort(u *url.URL) string {
	if port := u.Port(); port != "" {
		return port
	}
	if u.Scheme == "https" {
		return "443"
	}
	if u.Scheme == "http" {
		return "80"
	}
	return ""
}

func fetchConfigBody(client *http.Client, url string) ([]byte, error) {
	resp, err := client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("fetch config: %w", err)
	}
	defer resp.Body.Close()

	body, readErr := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		if readErr != nil {
			return nil, fmt.Errorf("fetch config: HTTP %d (body unreadable: %v)", resp.StatusCode, readErr)
		}
		return nil, fmt.Errorf("fetch config: HTTP %d – %s", resp.StatusCode, string(body))
	}
	if readErr != nil {
		return nil, fmt.Errorf("read config: %w", readErr)
	}
	return body, nil
}
