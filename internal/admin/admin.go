package admin

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"cosmossdk.io/log"
)

// Admin fetches and caches the voting-config from the GitHub Pages CDN.
type Admin struct {
	configURL string
	logger    log.Logger

	mu     sync.RWMutex
	cached *VotingConfig
}

// New creates a new Admin from the given configuration.
func New(cfg Config, logger log.Logger) (*Admin, error) {
	logger = logger.With("module", "admin")

	if cfg.Disable {
		logger.Info("admin server disabled")
		return nil, nil
	}

	configURL := cfg.ConfigURL
	if configURL == "" {
		configURL = DefaultConfig().ConfigURL
	}

	a := &Admin{
		configURL: configURL,
		logger:    logger,
	}

	if err := a.refresh(); err != nil {
		logger.Error("initial config fetch failed, will retry", "error", err)
	}

	return a, nil
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

// RefreshConfig forces a reload of voting-config from the CDN and returns
// the new value. Used by the fleet health watchdog on every tick so it
// picks up newly added/removed servers without a process restart.
func (a *Admin) RefreshConfig() (*VotingConfig, error) {
	if err := a.refresh(); err != nil {
		return nil, err
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.cached, nil
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
