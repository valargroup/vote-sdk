package admin

import (
	"database/sql"
	"fmt"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// Store is a SQLite-backed store for pending validator registrations.
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
		CREATE TABLE IF NOT EXISTS pending_registrations (
			operator_address TEXT PRIMARY KEY,
			url              TEXT NOT NULL,
			moniker          TEXT NOT NULL,
			timestamp        INTEGER NOT NULL,
			signature        TEXT NOT NULL,
			pub_key          TEXT NOT NULL,
			first_seen_at    INTEGER NOT NULL,
			last_seen_at     INTEGER NOT NULL,
			expires_at       INTEGER NOT NULL
		);

		CREATE INDEX IF NOT EXISTS idx_pending_expires ON pending_registrations (expires_at);
	`)
	return err
}

// Close closes the underlying database.
func (s *Store) Close() error {
	return s.db.Close()
}

// ListPendingRegistrations returns non-expired pending registrations. It also
// opportunistically deletes expired rows so GET /api/pending-validators both
// hides and evicts stale join requests even if the background sweeper has not
// run yet.
func (s *Store) ListPendingRegistrations() ([]PendingRegistration, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().Unix()
	if _, err := s.db.Exec("DELETE FROM pending_registrations WHERE expires_at <= ?", now); err != nil {
		return nil, err
	}

	rows, err := s.db.Query(
		`SELECT operator_address, url, moniker, timestamp, signature, pub_key,
			first_seen_at, last_seen_at, expires_at
		 FROM pending_registrations WHERE expires_at > ? ORDER BY first_seen_at`,
		now,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var regs []PendingRegistration
	for rows.Next() {
		var r PendingRegistration
		if err := rows.Scan(
			&r.OperatorAddress, &r.URL, &r.Moniker, &r.Timestamp,
			&r.Signature, &r.PubKey,
			&r.FirstSeenAt, &r.LastSeenAt, &r.ExpiresAt,
		); err != nil {
			return nil, err
		}
		regs = append(regs, r)
	}
	return regs, rows.Err()
}

// UpsertPendingRegistration inserts or updates a pending registration.
// first_seen_at is preserved on conflict; last_seen_at is always updated.
func (s *Store) UpsertPendingRegistration(r PendingRegistration) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec(`
		INSERT INTO pending_registrations (
			operator_address, url, moniker, timestamp, signature, pub_key,
			first_seen_at, last_seen_at, expires_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(operator_address) DO UPDATE SET
			url = excluded.url,
			moniker = excluded.moniker,
			timestamp = excluded.timestamp,
			signature = excluded.signature,
			pub_key = excluded.pub_key,
			last_seen_at = excluded.last_seen_at,
			expires_at = excluded.expires_at,
			first_seen_at = pending_registrations.first_seen_at`,
		r.OperatorAddress, r.URL, r.Moniker, r.Timestamp, r.Signature, r.PubKey,
		r.FirstSeenAt, r.LastSeenAt, r.ExpiresAt,
	)
	return err
}

// RemovePendingRegistration deletes a pending row by operator address.
// Returns true if a row was deleted.
func (s *Store) RemovePendingRegistration(operatorAddress string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	res, err := s.db.Exec("DELETE FROM pending_registrations WHERE operator_address = ?", operatorAddress)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// EvictExpiredPending deletes rows whose expires_at is in the past.
func (s *Store) EvictExpiredPending() (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().Unix()
	res, err := s.db.Exec("DELETE FROM pending_registrations WHERE expires_at <= ?", now)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
