// Package admin serves the voting-config endpoint by proxying the
// GitHub Pages CDN (valargroup/token-holder-voting-config).
//
// Server registration, approval, and removal happen via GitHub PRs
// on the config repo — no write endpoints here.
package admin

// Config holds the admin server configuration, read from app.toml [admin].
type Config struct {
	// Disable turns off the admin server entirely.
	Disable bool `mapstructure:"disable"`

	// ConfigURL is the GitHub Pages CDN URL for the voting-config JSON.
	// Defaults to the staging environment.
	ConfigURL string `mapstructure:"config_url"`
}

// DefaultConfig returns the default admin configuration.
func DefaultConfig() Config {
	return Config{
		Disable:   true,
		ConfigURL: "https://valargroup.github.io/token-holder-voting-config/staging/voting-config.json",
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
}
