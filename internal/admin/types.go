// Package admin serves the voting-config endpoint by proxying the
// GitHub Pages CDN (valargroup/token-holder-voting-config) and exposes
// HTTP endpoints for validator join registration (pending queue in SQLite).
//
// Published vote_servers URLs are updated via manual PRs on the config repo;
// this package does not write to GitHub.
package admin

import "time"

// PendingRegistrationTTL is how long an operator remains in the pending join
// queue before the row expires (SQLite; not configurable).
const PendingRegistrationTTL = 7 * 24 * time.Hour

// PendingEvictionSweepInterval is how often expired pending_registrations rows
// are deleted (not configurable).
const PendingEvictionSweepInterval = time.Hour

// Config holds the admin server configuration, read from app.toml [admin].
type Config struct {
	// Disable turns off the admin server entirely.
	Disable bool `mapstructure:"disable"`

	// ConfigURL is the voting-config JSON the admin polls every
	// WatchdogInterval to feed the fleet-health watchdog (and re-serves the
	// cached copy at GET /api/voting-config). It points at the same canonical
	// CDN URL that wallets and join.sh fetch directly
	// (valargroup.github.io/token-holder-voting-config/voting-config.json) —
	// override only for staging mirrors or fork testing.
	ConfigURL string `mapstructure:"config_url"`

	// WatchdogInterval is how often the fleet health watchdog probes all
	// vote servers and PIR endpoints listed in voting-config.json. Set to
	// 0 to disable the watchdog. Default: 5 minutes.
	WatchdogInterval time.Duration `mapstructure:"watchdog_interval"`

	// DBPath is the SQLite path for pending validator registrations.
	// Default: $HOME/.svoted/admin.db (resolved in New).
	DBPath string `mapstructure:"db_path"`
}

// DefaultConfig returns the default admin configuration.
func DefaultConfig() Config {
	return Config{
		Disable:          true,
		ConfigURL:        "https://valargroup.github.io/token-holder-voting-config/voting-config.json",
		WatchdogInterval: 5 * time.Minute,
	}
}

// ServiceEntry is the wire format for a server in the voting-config response.
type ServiceEntry struct {
	URL             string `json:"url"`
	Label           string `json:"label"`
	OperatorAddress string `json:"operator_address,omitempty"`
}

// VotingConfig is the wire format returned by GET /api/voting-config.
type VotingConfig struct {
	Version     int            `json:"version"`
	VoteServers []ServiceEntry `json:"vote_servers"`
	PIRServers  []ServiceEntry `json:"pir_endpoints"`
	// SnapshotHeight is the canonical Orchard nullifier-tree snapshot height
	// for the current voting round. PIR servers must serve this exact height,
	// and the admin UI auto-populates round drafts from it.
	SnapshotHeight *uint64 `json:"snapshot_height,omitempty"`
}

// PendingRegistration is a row in pending_registrations and the API shape
// for GET /api/pending-validators.
type PendingRegistration struct {
	OperatorAddress string `json:"operator_address"`
	URL             string `json:"url"`
	Moniker         string `json:"moniker"`
	Timestamp       int64  `json:"timestamp"`
	Signature       string `json:"signature,omitempty"`
	PubKey          string `json:"pub_key,omitempty"`
	FirstSeenAt     int64  `json:"first_seen_at"`
	LastSeenAt      int64  `json:"last_seen_at"`
	ExpiresAt       int64  `json:"expires_at"`
}
