// Package admin implements the server directory, validator registration,
// and health monitoring services. It runs as an optional in-process
// component inside the svoted binary, backed by a local SQLite database.
//
// This package replaces the Vercel Edge functions that previously managed
// the server fleet via Vercel Edge Config.
package admin

// Config holds the admin server configuration, read from app.toml [admin].
type Config struct {
	// Disable turns off the admin server entirely.
	Disable bool `mapstructure:"disable"`

	// DBPath is the path to the SQLite database file.
	DBPath string `mapstructure:"db_path"`

	// AdminAddress is the bech32 address of the bootstrap admin authorized
	// to approve/reject pending validator registrations. When empty, the
	// approve-registration endpoint returns 403 for all callers.
	AdminAddress string `mapstructure:"admin_address"`

	// ProbeInterval is how often to probe vote servers for health (seconds).
	ProbeInterval int `mapstructure:"probe_interval"`

	// EvictInterval is how often to check for stale server pulses (seconds).
	EvictInterval int `mapstructure:"evict_interval"`

	// StaleThreshold is how long a server can go without a pulse before
	// being excluded from the voting-config response (seconds).
	StaleThreshold int `mapstructure:"stale_threshold"`

	// PIRServers is the JSON-encoded list of PIR server entries included
	// in the voting-config response. Read from app.toml; wallets need this
	// alongside the dynamic vote_servers list.
	PIRServers string `mapstructure:"pir_servers"`
}

// DefaultConfig returns the default admin configuration.
func DefaultConfig() Config {
	return Config{
		Disable:        true,
		ProbeInterval:  1800,  // 30 minutes
		EvictInterval:  120,   // 2 minutes
		StaleThreshold: 21600, // 6 hours
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
	PIRServers  []ServiceEntry `json:"pir_servers"`
}

// PendingRegistration is the wire format for a pending validator registration.
type PendingRegistration struct {
	OperatorAddress string `json:"operator_address"`
	URL             string `json:"url"`
	Moniker         string `json:"moniker"`
	Timestamp       int64  `json:"timestamp"`
	Signature       string `json:"signature"`
	PubKey          string `json:"pub_key"`
	ExpiresAt       int64  `json:"expires_at"`
}

// BondingChecker returns true if the given valoper address is a bonded validator.
type BondingChecker func(valoperAddress string) bool

// VoteManagerGetter returns the current vote-manager bech32 address, or empty.
type VoteManagerGetter func() string
