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

// BondingChecker returns whether the validator with the given valoper bech32
// is currently bonded.
type BondingChecker func(valoper string) bool

// Admin fetches and caches voting-config from the CDN and stores pending
// validator registrations in SQLite.
type Admin struct {
	configURL    string
	logger       log.Logger
	store        *Store
	checkBonded  BondingChecker
	healthClient *http.Client

	mu               sync.RWMutex
	cached           *VotingConfig
	healthMu         sync.RWMutex
	voteServerHealth map[string]VoteServerHealth
}

// New creates a new Admin from the given configuration.
// homeDir is used to resolve default DBPath when cfg.DBPath is empty.
// checkBonded may be nil; in that case bonding checks always return false.
func New(cfg Config, homeDir string, checkBonded BondingChecker, logger log.Logger) (*Admin, error) {
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
		configURL:        configURL,
		logger:           logger,
		store:            store,
		checkBonded:      checkBonded,
		healthClient:     &http.Client{Timeout: VoteServerHealthProbeTimeout},
		voteServerHealth: make(map[string]VoteServerHealth),
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

// IsBonded reports whether the operator account (bech32) is bonded as a validator.
func (a *Admin) IsBonded(operatorAddress string) bool {
	if a.checkBonded == nil {
		return false
	}
	valoper, err := AddressToValoper(operatorAddress)
	if err != nil {
		return false
	}
	return a.checkBonded(valoper)
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
// This also feeds the in-process vote-server health poller so newly added or
// removed vote_servers become visible without a process restart.
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
