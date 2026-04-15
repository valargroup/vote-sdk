package admin

import (
	"database/sql"
	"fmt"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// Store is a SQLite-backed store for the admin server's state:
// approved servers, pending registrations, and heartbeat pulses.
type Store struct {
	db *sql.DB
	mu sync.Mutex
}

// NewStore opens (or creates) a SQLite database and runs migrations.
func NewStore(dbPath string) (*Store, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("set WAL mode: %w", err)
	}

	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migration: %w", err)
	}

	return &Store{db: db}, nil
}

func migrate(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS approved_servers (
			operator_address TEXT PRIMARY KEY,
			url              TEXT NOT NULL UNIQUE,
			label            TEXT NOT NULL
		);

		CREATE TABLE IF NOT EXISTS pending_registrations (
			operator_address TEXT PRIMARY KEY,
			url              TEXT NOT NULL,
			moniker          TEXT NOT NULL,
			timestamp        INTEGER NOT NULL,
			signature        TEXT NOT NULL,
			pub_key          TEXT NOT NULL,
			expires_at       INTEGER NOT NULL
		);

		CREATE TABLE IF NOT EXISTS server_pulses (
			url       TEXT PRIMARY KEY,
			pulse_at  INTEGER NOT NULL
		);

		CREATE TABLE IF NOT EXISTS config (
			key   TEXT PRIMARY KEY,
			value TEXT NOT NULL
		);
	`)
	return err
}

// Close closes the underlying database.
func (s *Store) Close() error {
	return s.db.Close()
}

// --- Approved servers ---

// ListApprovedServers returns all approved server entries.
func (s *Store) ListApprovedServers() ([]ServiceEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	rows, err := s.db.Query("SELECT operator_address, url, label FROM approved_servers ORDER BY url")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var servers []ServiceEntry
	for rows.Next() {
		var e ServiceEntry
		if err := rows.Scan(&e.OperatorAddress, &e.URL, &e.Label); err != nil {
			return nil, err
		}
		servers = append(servers, e)
	}
	return servers, rows.Err()
}

// UpsertApprovedServer inserts or replaces an approved server entry.
func (s *Store) UpsertApprovedServer(e ServiceEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec(`
		DELETE FROM approved_servers WHERE operator_address = ? OR url = ?;
		INSERT INTO approved_servers (operator_address, url, label) VALUES (?, ?, ?)`,
		e.OperatorAddress, e.URL,
		e.OperatorAddress, e.URL, e.Label,
	)
	return err
}

// RemoveApprovedServer removes an approved server by operator address.
// Returns the removed entry's URL (empty if not found).
func (s *Store) RemoveApprovedServer(operatorAddress string) (removedURL string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	err = s.db.QueryRow("SELECT url FROM approved_servers WHERE operator_address = ?", operatorAddress).Scan(&removedURL)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}

	_, err = s.db.Exec("DELETE FROM approved_servers WHERE operator_address = ?", operatorAddress)
	return removedURL, err
}

// IsApproved returns true if the given operator address is in the approved list.
func (s *Store) IsApproved(operatorAddress string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var count int
	err := s.db.QueryRow("SELECT COUNT(*) FROM approved_servers WHERE operator_address = ?", operatorAddress).Scan(&count)
	return count > 0, err
}

// --- Pending registrations ---

// ListPendingRegistrations returns non-expired pending registrations.
func (s *Store) ListPendingRegistrations() ([]PendingRegistration, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().Unix()
	rows, err := s.db.Query(
		"SELECT operator_address, url, moniker, timestamp, signature, pub_key, expires_at FROM pending_registrations WHERE expires_at > ? ORDER BY timestamp",
		now,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var regs []PendingRegistration
	for rows.Next() {
		var r PendingRegistration
		if err := rows.Scan(&r.OperatorAddress, &r.URL, &r.Moniker, &r.Timestamp, &r.Signature, &r.PubKey, &r.ExpiresAt); err != nil {
			return nil, err
		}
		regs = append(regs, r)
	}
	return regs, rows.Err()
}

// UpsertPendingRegistration inserts or replaces a pending registration.
func (s *Store) UpsertPendingRegistration(r PendingRegistration) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec(`
		INSERT INTO pending_registrations (operator_address, url, moniker, timestamp, signature, pub_key, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(operator_address) DO UPDATE SET
			url = excluded.url,
			moniker = excluded.moniker,
			timestamp = excluded.timestamp,
			signature = excluded.signature,
			pub_key = excluded.pub_key,
			expires_at = excluded.expires_at`,
		r.OperatorAddress, r.URL, r.Moniker, r.Timestamp, r.Signature, r.PubKey, r.ExpiresAt,
	)
	return err
}

// RemovePendingRegistration removes a pending registration by operator address.
// Returns the entry if found, or nil if not found.
func (s *Store) RemovePendingRegistration(operatorAddress string) (*PendingRegistration, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var r PendingRegistration
	err := s.db.QueryRow(
		"SELECT operator_address, url, moniker, timestamp, signature, pub_key, expires_at FROM pending_registrations WHERE operator_address = ?",
		operatorAddress,
	).Scan(&r.OperatorAddress, &r.URL, &r.Moniker, &r.Timestamp, &r.Signature, &r.PubKey, &r.ExpiresAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	_, err = s.db.Exec("DELETE FROM pending_registrations WHERE operator_address = ?", operatorAddress)
	return &r, err
}

// CleanPendingByOperator removes pending registrations matching the given operator address or URL.
func (s *Store) CleanPendingByOperator(operatorAddress, url string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec("DELETE FROM pending_registrations WHERE operator_address = ? OR url = ?", operatorAddress, url)
	return err
}

// --- Server pulses ---

// UpsertPulse records a heartbeat timestamp for a server URL.
func (s *Store) UpsertPulse(url string, pulseAt int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec(`
		INSERT INTO server_pulses (url, pulse_at) VALUES (?, ?)
		ON CONFLICT(url) DO UPDATE SET pulse_at = excluded.pulse_at`,
		url, pulseAt,
	)
	return err
}

// GetPulses returns all pulse entries as a map of url -> timestamp.
func (s *Store) GetPulses() (map[string]int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	rows, err := s.db.Query("SELECT url, pulse_at FROM server_pulses")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	pulses := make(map[string]int64)
	for rows.Next() {
		var url string
		var pulseAt int64
		if err := rows.Scan(&url, &pulseAt); err != nil {
			return nil, err
		}
		pulses[url] = pulseAt
	}
	return pulses, rows.Err()
}

// RemovePulse removes a pulse entry by URL.
func (s *Store) RemovePulse(url string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec("DELETE FROM server_pulses WHERE url = ?", url)
	return err
}

// EvictStalePulses removes pulse entries older than the threshold and returns
// the URLs that were evicted.
func (s *Store) EvictStalePulses(staleThresholdSecs int) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	cutoff := time.Now().Unix() - int64(staleThresholdSecs)
	rows, err := s.db.Query("SELECT url FROM server_pulses WHERE pulse_at < ?", cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var stale []string
	for rows.Next() {
		var url string
		if err := rows.Scan(&url); err != nil {
			return nil, err
		}
		stale = append(stale, url)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if len(stale) > 0 {
		_, err = s.db.Exec("DELETE FROM server_pulses WHERE pulse_at < ?", cutoff)
	}
	return stale, err
}

// --- Voting config assembly ---

// BuildVotingConfig assembles the voting-config response from approved servers
// that have a pulse (or were recently approved). Servers without a pulse are
// still included — the health prober will remove truly dead ones.
func (s *Store) BuildVotingConfig(pirServers []ServiceEntry) (*VotingConfig, error) {
	servers, err := s.ListApprovedServers()
	if err != nil {
		return nil, err
	}

	return &VotingConfig{
		Version:     1,
		VoteServers: servers,
		PIRServers:  pirServers,
	}, nil
}
