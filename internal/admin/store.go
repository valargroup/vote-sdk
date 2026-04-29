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
	legacy, err := pendingRegistrationsNeedsRecreate(db)
	if err != nil {
		return err
	}
	if legacy {
		if _, err := db.Exec(`DROP TABLE pending_registrations`); err != nil {
			return err
		}
	}

	_, err = db.Exec(`
			CREATE TABLE IF NOT EXISTS pending_registrations (
				operator_address TEXT PRIMARY KEY,
				url              TEXT NOT NULL,
				moniker          TEXT NOT NULL,
				requested_at     INTEGER NOT NULL,
				expires_at       INTEGER NOT NULL
			);

			CREATE INDEX IF NOT EXISTS idx_pending_expires ON pending_registrations (expires_at);
	`)
	return err
}

func pendingRegistrationsNeedsRecreate(db *sql.DB) (bool, error) {
	rows, err := db.Query(`PRAGMA table_info(pending_registrations)`)
	if err != nil {
		return false, err
	}
	defer rows.Close()

	cols := make(map[string]bool)
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull int
		var defaultValue interface{}
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			return false, err
		}
		cols[name] = true
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	if len(cols) == 0 {
		return false, nil
	}

	required := []string{"operator_address", "url", "moniker", "requested_at", "expires_at"}
	for _, name := range required {
		if !cols[name] {
			return true, nil
		}
	}

	legacy := []string{"timestamp", "signature", "pub_key", "first_seen_at", "last_seen_at"}
	for _, name := range legacy {
		if cols[name] {
			return true, nil
		}
	}
	return false, nil
}

// Close closes the underlying database.
func (s *Store) Close() error {
	return s.db.Close()
}

// ListPendingRegistrations returns non-expired pending registrations.
func (s *Store) ListPendingRegistrations() ([]PendingRegistration, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().Unix()
	rows, err := s.db.Query(
		`SELECT operator_address, url, moniker, requested_at, expires_at
		 FROM pending_registrations WHERE expires_at > ? ORDER BY requested_at`,
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
			&r.OperatorAddress, &r.URL, &r.Moniker, &r.RequestedAt, &r.ExpiresAt,
		); err != nil {
			return nil, err
		}
		regs = append(regs, r)
	}
	return regs, rows.Err()
}

// UpsertPendingRegistration inserts or updates a pending registration.
// requested_at is preserved on conflict.
func (s *Store) UpsertPendingRegistration(r PendingRegistration) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec(`
		INSERT INTO pending_registrations (
			operator_address, url, moniker, requested_at, expires_at
		) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(operator_address) DO UPDATE SET
			url = excluded.url,
			moniker = excluded.moniker,
			expires_at = excluded.expires_at,
			requested_at = pending_registrations.requested_at`,
		r.OperatorAddress, r.URL, r.Moniker, r.RequestedAt, r.ExpiresAt,
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
