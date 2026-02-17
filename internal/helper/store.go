package helper

import (
	"crypto/rand"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// ShareStore is a SQLite-backed share queue with ephemeral in-memory scheduling.
// Payload data and processing state are persisted; scheduling delays (which
// provide temporal unlinkability) are kept only in memory — on recovery,
// shares get fresh random delays per spec.
type ShareStore struct {
	db       *sql.DB
	mu       sync.Mutex
	schedule map[string]time.Time // key: "round_id:share_index:proposal_id"
	minDelay time.Duration
	maxDelay time.Duration
	logger   func(msg string, keyvals ...any) // optional error logger
}

// EnqueueResult reports how an enqueue attempt was handled.
type EnqueueResult int

const (
	EnqueueInserted EnqueueResult = iota
	EnqueueDuplicate
	EnqueueConflict
)

// NewShareStore opens (or creates) a SQLite database and runs migrations.
func NewShareStore(dbPath string, minDelay, maxDelay time.Duration) (*ShareStore, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	// Enable WAL mode for concurrent reads.
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("set WAL mode: %w", err)
	}

	// Run migrations.
	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migration: %w", err)
	}

	s := &ShareStore{
		db:       db,
		schedule: make(map[string]time.Time),
		minDelay: minDelay,
		maxDelay: maxDelay,
	}

	// Recover non-terminal shares from SQLite.
	if err := s.recover(); err != nil {
		db.Close()
		return nil, fmt.Errorf("recovery: %w", err)
	}

	return s, nil
}

func migrate(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS shares (
			round_id        TEXT NOT NULL,
			share_index     INTEGER NOT NULL,
			shares_hash     TEXT NOT NULL,
			proposal_id     INTEGER NOT NULL,
			vote_decision   INTEGER NOT NULL,
			enc_share_c1    TEXT NOT NULL,
			enc_share_c2    TEXT NOT NULL,
			tree_position   INTEGER NOT NULL,
			all_enc_shares  TEXT NOT NULL,
			state           INTEGER NOT NULL DEFAULT 0,
			attempts        INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (round_id, share_index, proposal_id)
		)
	`)
	return err
}

func schedKey(roundID string, shareIndex, proposalID uint32) string {
	return fmt.Sprintf("%s:%d:%d", roundID, shareIndex, proposalID)
}

// Enqueue adds a share payload with a random submission delay.
//
// Returns:
//   - EnqueueInserted when a new row was inserted and scheduled.
//   - EnqueueDuplicate when an identical payload already exists.
//   - EnqueueConflict when an entry exists for (round_id, share_index) but
//     with different payload content.
func (s *ShareStore) Enqueue(payload SharePayload) (EnqueueResult, error) {
	allEncJSON, err := json.Marshal(payload.AllEncShares)
	if err != nil {
		return EnqueueConflict, fmt.Errorf("marshal all_enc_shares: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	res, err := s.db.Exec(
		`INSERT INTO shares
		 (round_id, share_index, shares_hash, proposal_id, vote_decision,
		  enc_share_c1, enc_share_c2, tree_position, all_enc_shares, state, attempts)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 0, 0)
		 ON CONFLICT(round_id, share_index, proposal_id) DO NOTHING`,
		payload.VoteRoundID,
		payload.EncShare.ShareIndex,
		payload.SharesHash,
		payload.ProposalID,
		payload.VoteDecision,
		payload.EncShare.C1,
		payload.EncShare.C2,
		payload.TreePosition,
		string(allEncJSON),
	)
	if err != nil {
		return EnqueueConflict, fmt.Errorf("insert share: %w", err)
	}

	// Only schedule if the row was actually inserted (not a duplicate).
	affected, _ := res.RowsAffected()
	if affected > 0 {
		delay := s.randomDelay()
		key := schedKey(payload.VoteRoundID, payload.EncShare.ShareIndex, payload.ProposalID)
		s.schedule[key] = time.Now().Add(delay)
		return EnqueueInserted, nil
	}

	// Conflict path: row already exists, classify as idempotent duplicate vs conflict.
	existing, ok := s.loadShare(payload.VoteRoundID, payload.EncShare.ShareIndex, payload.ProposalID)
	if !ok {
		return EnqueueConflict, fmt.Errorf(
			"load existing share after conflict: round_id=%s share_index=%d proposal_id=%d",
			payload.VoteRoundID,
			payload.EncShare.ShareIndex,
			payload.ProposalID,
		)
	}
	if payloadEqual(existing.Payload, payload) {
		return EnqueueDuplicate, nil
	}

	return EnqueueConflict, nil
}

// TakeReady returns all shares past their scheduled submission time that are
// in Received state, transitioning them to Witnessed.
func (s *ShareStore) TakeReady() []QueuedShare {
	now := time.Now()

	s.mu.Lock()
	defer s.mu.Unlock()

	// Find ready keys.
	var readyKeys []string
	for key, scheduledAt := range s.schedule {
		if scheduledAt.Before(now) || scheduledAt.Equal(now) {
			readyKeys = append(readyKeys, key)
		}
	}

	if len(readyKeys) == 0 {
		return nil
	}

	var result []QueuedShare
	for _, key := range readyKeys {
		// Parse round_id, share_index, and proposal_id from key.
		parts := strings.SplitN(key, ":", 3)
		if len(parts) != 3 {
			delete(s.schedule, key)
			continue
		}
		roundID := parts[0]
		idx64, _ := strconv.ParseUint(parts[1], 10, 32)
		shareIndex := uint32(idx64)
		pid64, _ := strconv.ParseUint(parts[2], 10, 32)
		proposalID := uint32(pid64)

		// Only take shares in Received state (0).
		res, err := s.db.Exec(
			"UPDATE shares SET state = 1 WHERE round_id = ? AND share_index = ? AND proposal_id = ? AND state = 0",
			roundID, shareIndex, proposalID,
		)
		if err != nil {
			continue
		}
		affected, _ := res.RowsAffected()
		if affected == 0 {
			// Not in Received state, remove from schedule.
			delete(s.schedule, key)
			continue
		}

		// Load the payload.
		if share, ok := s.loadShare(roundID, shareIndex, proposalID); ok {
			result = append(result, share)
		}
		delete(s.schedule, key)
	}

	return result
}

// MarkSubmitted marks a share as successfully submitted to the chain.
func (s *ShareStore) MarkSubmitted(roundID string, shareIndex, proposalID uint32) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, err := s.db.Exec(
		"UPDATE shares SET state = 2 WHERE round_id = ? AND share_index = ? AND proposal_id = ? AND state = 1",
		roundID, shareIndex, proposalID,
	); err != nil {
		s.logError("MarkSubmitted: db update failed", "round_id", roundID, "share_index", shareIndex, "proposal_id", proposalID, "error", err)
	}
	delete(s.schedule, schedKey(roundID, shareIndex, proposalID))
}

// MarkFailed marks a share processing attempt as failed, with retry or
// permanent failure after max attempts.
func (s *ShareStore) MarkFailed(roundID string, shareIndex, proposalID uint32) {
	const maxAttempts = 5

	s.mu.Lock()
	defer s.mu.Unlock()

	var attempts int
	if err := s.db.QueryRow(
		"SELECT attempts FROM shares WHERE round_id = ? AND share_index = ? AND proposal_id = ?",
		roundID, shareIndex, proposalID,
	).Scan(&attempts); err != nil {
		s.logError("MarkFailed: db query failed", "round_id", roundID, "share_index", shareIndex, "proposal_id", proposalID, "error", err)
		return
	}

	newAttempts := attempts + 1
	key := schedKey(roundID, shareIndex, proposalID)

	if newAttempts >= maxAttempts {
		// Permanently failed.
		if _, err := s.db.Exec(
			"UPDATE shares SET state = 3, attempts = ? WHERE round_id = ? AND share_index = ? AND proposal_id = ?",
			newAttempts, roundID, shareIndex, proposalID,
		); err != nil {
			s.logError("MarkFailed: db update (permanent) failed", "error", err)
		}
		delete(s.schedule, key)
	} else {
		// Re-schedule with exponential backoff.
		if _, err := s.db.Exec(
			"UPDATE shares SET state = 0, attempts = ? WHERE round_id = ? AND share_index = ? AND proposal_id = ?",
			newAttempts, roundID, shareIndex, proposalID,
		); err != nil {
			s.logError("MarkFailed: db update (retry) failed", "error", err)
		}
		backoff := time.Duration(1<<uint(min(newAttempts, 6))) * time.Second
		s.schedule[key] = time.Now().Add(backoff)
	}
}

func (s *ShareStore) logError(msg string, keyvals ...any) {
	if s.logger != nil {
		s.logger(msg, keyvals...)
	}
}

// Status returns per-round queue statistics.
func (s *ShareStore) Status() map[string]QueueStatus {
	s.mu.Lock()
	defer s.mu.Unlock()

	rows, err := s.db.Query(
		"SELECT round_id, state, COUNT(*) FROM shares GROUP BY round_id, state",
	)
	if err != nil {
		return nil
	}
	defer rows.Close()

	result := make(map[string]QueueStatus)
	for rows.Next() {
		var roundID string
		var state, count int
		if err := rows.Scan(&roundID, &state, &count); err != nil {
			continue
		}
		entry := result[roundID]
		entry.Total += count
		switch state {
		case 0, 1:
			entry.Pending += count
		case 2:
			entry.Submitted += count
		case 3:
			entry.Failed += count
		}
		result[roundID] = entry
	}

	return result
}

// Close closes the database connection.
func (s *ShareStore) Close() error {
	return s.db.Close()
}

// recover resets in-flight shares and schedules fresh delays.
func (s *ShareStore) recover() error {
	// Reset Witnessed (1) → Received (0).
	if _, err := s.db.Exec("UPDATE shares SET state = 0 WHERE state = 1"); err != nil {
		return fmt.Errorf("reset witnessed shares: %w", err)
	}

	// Load all non-terminal shares.
	rows, err := s.db.Query("SELECT round_id, share_index, proposal_id FROM shares WHERE state = 0")
	if err != nil {
		return fmt.Errorf("query recoverable shares: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var roundID string
		var shareIndex, proposalID uint32
		if err := rows.Scan(&roundID, &shareIndex, &proposalID); err != nil {
			continue
		}
		delay := s.randomDelay()
		s.schedule[schedKey(roundID, shareIndex, proposalID)] = time.Now().Add(delay)
	}
	return nil
}

func (s *ShareStore) loadShare(roundID string, shareIndex, proposalID uint32) (QueuedShare, bool) {
	var q QueuedShare
	var allEncJSON string
	var state, attempts int

	err := s.db.QueryRow(
		`SELECT shares_hash, proposal_id, vote_decision, enc_share_c1, enc_share_c2,
		        tree_position, all_enc_shares, state, attempts
		 FROM shares WHERE round_id = ? AND share_index = ? AND proposal_id = ?`,
		roundID, shareIndex, proposalID,
	).Scan(
		&q.Payload.SharesHash,
		&q.Payload.ProposalID,
		&q.Payload.VoteDecision,
		&q.Payload.EncShare.C1,
		&q.Payload.EncShare.C2,
		&q.Payload.TreePosition,
		&allEncJSON,
		&state,
		&attempts,
	)
	if err != nil {
		return q, false
	}

	q.Payload.VoteRoundID = roundID
	q.Payload.EncShare.ShareIndex = shareIndex
	q.Payload.ShareIndex = shareIndex
	q.State = ShareState(state)
	q.Attempts = attempts

	if err := json.Unmarshal([]byte(allEncJSON), &q.Payload.AllEncShares); err != nil {
		return q, false
	}

	return q, true
}

func payloadEqual(existing, incoming SharePayload) bool {
	if existing.VoteRoundID != incoming.VoteRoundID ||
		existing.SharesHash != incoming.SharesHash ||
		existing.ProposalID != incoming.ProposalID ||
		existing.VoteDecision != incoming.VoteDecision ||
		existing.EncShare != incoming.EncShare ||
		existing.ShareIndex != incoming.ShareIndex ||
		existing.TreePosition != incoming.TreePosition {
		return false
	}
	if len(existing.AllEncShares) != len(incoming.AllEncShares) {
		return false
	}
	for i := range existing.AllEncShares {
		if existing.AllEncShares[i] != incoming.AllEncShares[i] {
			return false
		}
	}
	return true
}

func (s *ShareStore) randomDelay() time.Duration {
	minSecs := int64(s.minDelay.Seconds())
	maxSecs := int64(s.maxDelay.Seconds())
	if maxSecs <= minSecs {
		return s.minDelay
	}
	// Use crypto/rand for unpredictable delays (temporal unlinkability).
	var buf [8]byte
	_, _ = rand.Read(buf[:])
	n := int64(binary.LittleEndian.Uint64(buf[:]))
	if n < 0 {
		n = -n
	}
	secs := minSecs + n%(maxSecs-minSecs+1)
	return time.Duration(secs) * time.Second
}
