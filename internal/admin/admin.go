package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"sync"
	"time"

	"cosmossdk.io/log"
)

// ValidatorChecker returns whether a validator with the given valoper bech32
// exists in staking state.
type ValidatorChecker func(valoper string) bool

// Admin fetches and caches voting-config from the CDN and stores pending
// validator registrations in SQLite.
type Admin struct {
	configURL            string
	logger               log.Logger
	store                *Store
	checkValidatorExists ValidatorChecker

	mu     sync.RWMutex
	cached *VotingConfig
}

// New creates a new Admin from the given configuration.
// homeDir is used to resolve default DBPath when cfg.DBPath is empty.
// checkValidatorExists may be nil; in that case validator checks always return false.
func New(cfg Config, homeDir string, checkValidatorExists ValidatorChecker, logger log.Logger) (*Admin, error) {
	logger = logger.With("module", "admin")

	if cfg.Disable {
		logger.Info("admin server disabled")
		return nil, nil
	}

	configURL := cfg.ConfigURL
	if configURL == "" {
		configURL = DefaultConfig().ConfigURL
	}

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
		logger:               logger,
		store:                store,
		checkValidatorExists: checkValidatorExists,
	}

	if err := a.refresh(); err != nil {
		logger.Error("initial config fetch failed, will retry", "error", err)
	}

	return a, nil
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
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(a.configURL)
	if err != nil {
		return fmt.Errorf("fetch config: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("fetch config: HTTP %d – %s", resp.StatusCode, string(body))
	}

	var cfg VotingConfig
	if err := json.NewDecoder(resp.Body).Decode(&cfg); err != nil {
		return fmt.Errorf("decode config: %w", err)
	}

	a.mu.Lock()
	a.cached = &cfg
	a.mu.Unlock()

	a.logger.Info("voting config loaded", "vote_servers", len(cfg.VoteServers), "pir_endpoints", len(cfg.PIRServers))
	return nil
}
