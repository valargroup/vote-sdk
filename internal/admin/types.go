// Package admin serves the voting-config endpoint by proxying the
// GitHub Pages CDN (valargroup/token-holder-voting-config).
//
// Server registration, approval, and removal happen via GitHub PRs
// on the config repo — no write endpoints here.
package admin

import "time"

// Config holds the admin server configuration, read from app.toml [admin].
type Config struct {
	// Disable turns off the admin server entirely.
	Disable bool `mapstructure:"disable"`

	// ConfigURL is the GitHub Pages CDN URL for the voting-config JSON.
	ConfigURL string `mapstructure:"config_url"`

	// WatchdogInterval is how often the fleet health watchdog probes all
	// vote servers and PIR endpoints listed in voting-config.json. Set to
	// 0 to disable the watchdog. Default: 5 minutes.
	WatchdogInterval time.Duration `mapstructure:"watchdog_interval"`
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
