package helper

import (
	"crypto/rand"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// ErrUnknownRound is returned when a share references a round that does not
// exist on-chain. Callers can check for this with errors.Is to distinguish
// it from transient failures.
var ErrUnknownRound = errors.New("unknown voting round")

// ShareStore is a SQLite-backed share queue with ephemeral in-memory scheduling.
// Payload data and processing state are persisted; scheduling delays (which
// provide temporal unlinkability) are kept only in memory — on recovery,
// shares get fresh random delays per spec.
type ShareStore struct {
	db             *sql.DB
	mu             sync.Mutex
	schedule       map[string]time.Time // key: "round_id:share_index:proposal_id:tree_position"
	meanDelay      time.Duration
	minDelay       time.Duration
	roundCache     map[string]uint64                // roundID → vote_end_time (unix seconds)
	fetchRoundInfo RoundInfoFetcher                 // queries the chain; may be nil in tests
	logger         func(msg string, keyvals ...any) // optional error logger
	logInfo        func(msg string, keyvals ...any) // optional info logger
}

// EnqueueResult reports how an enqueue attempt was handled.
type EnqueueResult int

const (
	EnqueueInserted EnqueueResult = iota
	EnqueueDuplicate
	EnqueueConflict
)

// NewShareStore opens (or creates) a SQLite database and runs migrations.
func NewShareStore(dbPath string, meanDelay, minDelay time.Duration, fetcher RoundInfoFetcher) (*ShareStore, error) {
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
		db:             db,
		schedule:       make(map[string]time.Time),
		meanDelay:      meanDelay,
		minDelay:       minDelay,
		roundCache:     make(map[string]uint64),
		fetchRoundInfo: fetcher,
	}

	// Recover non-terminal shares from SQLite.
	if err := s.recover(); err != nil {
		db.Close()
		return nil, fmt.Errorf("recovery: %w", err)
	}

	return s, nil
}

func migrate(db *sql.DB) error {
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS shares (
			round_id        TEXT NOT NULL,
			share_index     INTEGER NOT NULL,
			shares_hash     TEXT NOT NULL,
			proposal_id     INTEGER NOT NULL,
			vote_decision   INTEGER NOT NULL,
			enc_share_c1    TEXT NOT NULL,
			enc_share_c2    TEXT NOT NULL,
			tree_position   INTEGER NOT NULL,
			share_comms     TEXT NOT NULL DEFAULT '[]',
			primary_blind   TEXT NOT NULL DEFAULT '',
			state           INTEGER NOT NULL DEFAULT 0,
			attempts        INTEGER NOT NULL DEFAULT 0,
			vote_end_time   INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (round_id, share_index, proposal_id, tree_position)
		)
	`); err != nil {
		return fmt.Errorf("create shares table: %w", err)
	}

	// Migrate: add tree_position to PK if the table was created with the old 3-column PK.
	if needsMigration, err := columnNotInPK(db, "shares", "tree_position"); err != nil {
		return fmt.Errorf("check shares PK: %w", err)
	} else if needsMigration {
		if err := migrateSharesPK(db); err != nil {
			return fmt.Errorf("migrate shares PK: %w", err)
		}
	}

	hasShareComms, err := tableHasColumn(db, "shares", "share_comms")
	if err != nil {
		return fmt.Errorf("check shares schema: %w", err)
	}
	if !hasShareComms {
		if _, err := db.Exec("ALTER TABLE shares ADD COLUMN share_comms TEXT NOT NULL DEFAULT '[]'"); err != nil {
			return fmt.Errorf("add shares.share_comms: %w", err)
		}
	}

	hasPrimaryBlind, err := tableHasColumn(db, "shares", "primary_blind")
	if err != nil {
		return fmt.Errorf("check shares schema: %w", err)
	}
	if !hasPrimaryBlind {
		if _, err := db.Exec("ALTER TABLE shares ADD COLUMN primary_blind TEXT NOT NULL DEFAULT ''"); err != nil {
			return fmt.Errorf("add shares.primary_blind: %w", err)
		}
	}

	hasVoteEndTime, err := tableHasColumn(db, "shares", "vote_end_time")
	if err != nil {
		return fmt.Errorf("check shares schema: %w", err)
	}
	if !hasVoteEndTime {
		if _, err := db.Exec("ALTER TABLE shares ADD COLUMN vote_end_time INTEGER NOT NULL DEFAULT 0"); err != nil {
			return fmt.Errorf("add shares.vote_end_time: %w", err)
		}
	}

	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS rounds (
			round_id       TEXT PRIMARY KEY,
			vote_end_time  INTEGER NOT NULL
		)
	`); err != nil {
		return fmt.Errorf("create rounds table: %w", err)
	}

	return nil
}

func tableHasColumn(db *sql.DB, tableName, columnName string) (bool, error) {
	rows, err := db.Query(fmt.Sprintf("PRAGMA table_info(%s)", tableName))
	if err != nil {
		return false, err
	}
	defer rows.Close()

	for rows.Next() {
		var cid int
		var name string
		var colType string
		var notNull int
		var defaultValue sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &colType, &notNull, &defaultValue, &pk); err != nil {
			return false, err
		}
		if name == columnName {
			return true, nil
		}
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	return false, nil
}

// columnNotInPK returns true if the named column exists in the table but is
// NOT part of its primary key. Returns false (no migration needed) if the
// column is already in the PK or doesn't exist at all (fresh table).
func columnNotInPK(db *sql.DB, tableName, columnName string) (bool, error) {
	rows, err := db.Query(fmt.Sprintf("PRAGMA table_info(%s)", tableName))
	if err != nil {
		return false, err
	}
	defer rows.Close()

	for rows.Next() {
		var cid int
		var name, colType string
		var notNull int
		var defaultValue sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &colType, &notNull, &defaultValue, &pk); err != nil {
			return false, err
		}
		if name == columnName {
			return pk == 0, nil // pk=0 means not in primary key
		}
	}
	return false, rows.Err()
}

// migrateSharesPK recreates the shares table with the new 4-column primary key
// (round_id, share_index, proposal_id, tree_position). Handles old schemas
// that may lack the vote_end_time column.
func migrateSharesPK(db *sql.DB) error {
	// Ensure vote_end_time exists before copying (old schemas may lack it).
	hasVET, err := tableHasColumn(db, "shares", "vote_end_time")
	if err != nil {
		return fmt.Errorf("check vote_end_time column: %w", err)
	}
	if !hasVET {
		if _, err := db.Exec("ALTER TABLE shares ADD COLUMN vote_end_time INTEGER NOT NULL DEFAULT 0"); err != nil {
			return fmt.Errorf("add vote_end_time: %w", err)
		}
	}

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Ensure share_comms and primary_blind columns exist before migration.
	hasComms, err := tableHasColumn(db, "shares", "share_comms")
	if err != nil {
		return fmt.Errorf("check share_comms column: %w", err)
	}
	if !hasComms {
		if _, errA := db.Exec("ALTER TABLE shares ADD COLUMN share_comms TEXT NOT NULL DEFAULT '[]'"); errA != nil {
			return fmt.Errorf("add share_comms before PK migration: %w", errA)
		}
	}
	hasBlind, err := tableHasColumn(db, "shares", "primary_blind")
	if err != nil {
		return fmt.Errorf("check primary_blind column: %w", err)
	}
	if !hasBlind {
		if _, errA := db.Exec("ALTER TABLE shares ADD COLUMN primary_blind TEXT NOT NULL DEFAULT ''"); errA != nil {
			return fmt.Errorf("add primary_blind before PK migration: %w", errA)
		}
	}

	if _, err := tx.Exec(`CREATE TABLE shares_new (
		round_id        TEXT NOT NULL,
		share_index     INTEGER NOT NULL,
		shares_hash     TEXT NOT NULL,
		proposal_id     INTEGER NOT NULL,
		vote_decision   INTEGER NOT NULL,
		enc_share_c1    TEXT NOT NULL,
		enc_share_c2    TEXT NOT NULL,
		tree_position   INTEGER NOT NULL,
		share_comms     TEXT NOT NULL DEFAULT '[]',
		primary_blind   TEXT NOT NULL DEFAULT '',
		state           INTEGER NOT NULL DEFAULT 0,
		attempts        INTEGER NOT NULL DEFAULT 0,
		vote_end_time   INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY (round_id, share_index, proposal_id, tree_position)
	)`); err != nil {
		return err
	}

	if _, err := tx.Exec(`INSERT INTO shares_new SELECT
		round_id, share_index, shares_hash, proposal_id, vote_decision,
		enc_share_c1, enc_share_c2, tree_position, share_comms,
		primary_blind, state, attempts, vote_end_time
	FROM shares`); err != nil {
		return err
	}

	if _, err := tx.Exec("DROP TABLE shares"); err != nil {
		return err
	}
	if _, err := tx.Exec("ALTER TABLE shares_new RENAME TO shares"); err != nil {
		return err
	}

	return tx.Commit()
}

// schedKey builds a colon-delimited schedule key.
// roundID must be hex-encoded (no colons), so the delimiter is unambiguous.
func schedKey(roundID string, shareIndex, proposalID uint32, treePosition uint64) string {
	return fmt.Sprintf("%s:%d:%d:%d", roundID, shareIndex, proposalID, treePosition)
}

// Enqueue adds a share payload with an exponential random submission delay,
// capped at the vote end time for the round.
//
// Returns:
//   - EnqueueInserted when a new row was inserted and scheduled.
//   - EnqueueDuplicate when an identical payload already exists.
//   - EnqueueConflict when an entry exists for (round_id, share_index) but
//     with different payload content.
func (s *ShareStore) Enqueue(payload SharePayload) (EnqueueResult, error) {
	commsJSON, err := json.Marshal(payload.ShareComms)
	if err != nil {
		return EnqueueConflict, fmt.Errorf("marshal share_comms: %w", err)
	}

	// Fetch vote_end_time before acquiring the lock (direct keeper KV read).
	voteEndTime, err := s.getVoteEndTime(payload.VoteRoundID)
	if err != nil {
		return EnqueueConflict, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	res, err := s.db.Exec(
		`INSERT INTO shares
		 (round_id, share_index, shares_hash, proposal_id, vote_decision,
		  enc_share_c1, enc_share_c2, tree_position, share_comms, primary_blind, state, attempts, vote_end_time)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, 0, ?)
		 ON CONFLICT(round_id, share_index, proposal_id, tree_position) DO NOTHING`,
		payload.VoteRoundID,
		payload.EncShare.ShareIndex,
		payload.SharesHash,
		payload.ProposalID,
		payload.VoteDecision,
		payload.EncShare.C1,
		payload.EncShare.C2,
		payload.TreePosition,
		string(commsJSON),
		payload.PrimaryBlind,
		voteEndTime,
	)
	if err != nil {
		return EnqueueConflict, fmt.Errorf("insert share: %w", err)
	}

	// Only schedule if the row was actually inserted (not a duplicate).
	affected, _ := res.RowsAffected()
	if affected > 0 {
		delay := s.uniformDelay(voteEndTime)
		key := schedKey(payload.VoteRoundID, payload.EncShare.ShareIndex, payload.ProposalID, payload.TreePosition)
		s.schedule[key] = time.Now().Add(delay)
		if s.logInfo != nil {
			s.logInfo("share scheduled",
				"round_id", payload.VoteRoundID,
				"share_index", payload.EncShare.ShareIndex,
				"proposal_id", payload.ProposalID,
			)
		}
		return EnqueueInserted, nil
	}

	// Conflict path: row already exists, classify as idempotent duplicate vs conflict.
	existing, ok := s.loadShare(payload.VoteRoundID, payload.EncShare.ShareIndex, payload.ProposalID, payload.TreePosition)
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
		// Parse round_id, share_index, proposal_id, and tree_position from key.
		parts := strings.SplitN(key, ":", 4)
		if len(parts) != 4 {
			delete(s.schedule, key)
			continue
		}
		roundID := parts[0]
		idx64, err := strconv.ParseUint(parts[1], 10, 32)
		if err != nil {
			delete(s.schedule, key)
			continue
		}
		shareIndex := uint32(idx64)
		pid64, err := strconv.ParseUint(parts[2], 10, 32)
		if err != nil {
			delete(s.schedule, key)
			continue
		}
		proposalID := uint32(pid64)
		treePos, err := strconv.ParseUint(parts[3], 10, 64)
		if err != nil {
			delete(s.schedule, key)
			continue
		}

		// Only take shares in Received state (0).
		res, err := s.db.Exec(
			"UPDATE shares SET state = 1 WHERE round_id = ? AND share_index = ? AND proposal_id = ? AND tree_position = ? AND state = 0",
			roundID, shareIndex, proposalID, treePos,
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
		if share, ok := s.loadShare(roundID, shareIndex, proposalID, treePos); ok {
			result = append(result, share)
		}
		delete(s.schedule, key)
	}

	return result
}

// MarkSubmitted marks a share as successfully submitted to the chain.
func (s *ShareStore) MarkSubmitted(roundID string, shareIndex, proposalID uint32, treePosition uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, err := s.db.Exec(
		"UPDATE shares SET state = 2 WHERE round_id = ? AND share_index = ? AND proposal_id = ? AND tree_position = ? AND state = 1",
		roundID, shareIndex, proposalID, treePosition,
	); err != nil {
		s.logError("MarkSubmitted: db update failed", "round_id", roundID, "share_index", shareIndex, "proposal_id", proposalID, "tree_position", treePosition, "error", err)
	}
	delete(s.schedule, schedKey(roundID, shareIndex, proposalID, treePosition))
}

// MarkFailed marks a share processing attempt as failed, with retry or
// permanent failure after max attempts.
func (s *ShareStore) MarkFailed(roundID string, shareIndex, proposalID uint32, treePosition uint64) {
	const maxAttempts = 5

	s.mu.Lock()
	defer s.mu.Unlock()

	var attempts int
	if err := s.db.QueryRow(
		"SELECT attempts FROM shares WHERE round_id = ? AND share_index = ? AND proposal_id = ? AND tree_position = ?",
		roundID, shareIndex, proposalID, treePosition,
	).Scan(&attempts); err != nil {
		s.logError("MarkFailed: db query failed", "round_id", roundID, "share_index", shareIndex, "proposal_id", proposalID, "tree_position", treePosition, "error", err)
		return
	}

	newAttempts := attempts + 1
	key := schedKey(roundID, shareIndex, proposalID, treePosition)

	if newAttempts >= maxAttempts {
		// Permanently failed.
		if _, err := s.db.Exec(
			"UPDATE shares SET state = 3, attempts = ? WHERE round_id = ? AND share_index = ? AND proposal_id = ? AND tree_position = ?",
			newAttempts, roundID, shareIndex, proposalID, treePosition,
		); err != nil {
			s.logError("MarkFailed: db update (permanent) failed", "error", err)
		}
		delete(s.schedule, key)
	} else {
		// Re-schedule with exponential backoff.
		if _, err := s.db.Exec(
			"UPDATE shares SET state = 0, attempts = ? WHERE round_id = ? AND share_index = ? AND proposal_id = ? AND tree_position = ?",
			newAttempts, roundID, shareIndex, proposalID, treePosition,
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

// PurgeExpiredRounds deletes all share data for rounds whose vote_end_time
// has passed, and removes the corresponding entries from the in-memory
// schedule and round cache. Returns the number of rows deleted.
func (s *ShareStore) PurgeExpiredRounds() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().Unix()

	res, err := s.db.Exec(
		"DELETE FROM shares WHERE vote_end_time > 0 AND vote_end_time < ?", now,
	)
	if err != nil {
		s.logError("PurgeExpiredRounds: delete shares failed", "error", err)
		return 0
	}
	deleted, _ := res.RowsAffected()

	// Also clean the rounds metadata table.
	if _, err := s.db.Exec(
		"DELETE FROM rounds WHERE vote_end_time > 0 AND vote_end_time < ?", now,
	); err != nil {
		s.logError("PurgeExpiredRounds: delete rounds failed", "error", err)
	}

	// Prune in-memory caches for expired rounds.
	for roundID, vet := range s.roundCache {
		if vet > 0 && vet < uint64(now) {
			delete(s.roundCache, roundID)
		}
	}
	for key := range s.schedule {
		parts := strings.SplitN(key, ":", 4)
		if len(parts) < 1 {
			continue
		}
		roundID := parts[0]
		if _, ok := s.roundCache[roundID]; !ok {
			delete(s.schedule, key)
		}
	}

	if deleted > 0 {
		if s.logInfo != nil {
			s.logInfo("purged expired round data", "rows_deleted", deleted)
		}
	}
	return deleted
}

// recover resets in-flight shares and schedules fresh delays.
func (s *ShareStore) recover() error {
	// Reset Witnessed (1) → Received (0).
	if _, err := s.db.Exec("UPDATE shares SET state = 0 WHERE state = 1"); err != nil {
		return fmt.Errorf("reset witnessed shares: %w", err)
	}

	// Repopulate round cache from rounds table.
	roundRows, err := s.db.Query("SELECT round_id, vote_end_time FROM rounds")
	if err != nil {
		return fmt.Errorf("query rounds cache: %w", err)
	}
	defer roundRows.Close()
	for roundRows.Next() {
		var roundID string
		var vet uint64
		if err := roundRows.Scan(&roundID, &vet); err != nil {
			continue
		}
		s.roundCache[roundID] = vet
	}

	// Load all non-terminal shares with their denormalized vote_end_time.
	rows, err := s.db.Query("SELECT round_id, share_index, proposal_id, tree_position, vote_end_time FROM shares WHERE state = 0")
	if err != nil {
		return fmt.Errorf("query recoverable shares: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var roundID string
		var shareIndex, proposalID uint32
		var treePosition, voteEndTime uint64
		if err := rows.Scan(&roundID, &shareIndex, &proposalID, &treePosition, &voteEndTime); err != nil {
			continue
		}
		// Heal rows with vote_end_time=0 (transient fetch failure at enqueue
		// time) from the round cache.
		if voteEndTime == 0 {
			if cached, ok := s.roundCache[roundID]; ok && cached != 0 {
				voteEndTime = cached
				if _, err := s.db.Exec(
					"UPDATE shares SET vote_end_time = ? WHERE round_id = ? AND share_index = ? AND proposal_id = ? AND tree_position = ?",
					voteEndTime, roundID, shareIndex, proposalID, treePosition,
				); err != nil {
					s.logError("loadSchedule: healing vote_end_time failed", "round_id", roundID, "error", err)
				}
			}
		}
		delay := s.uniformDelay(voteEndTime)
		s.schedule[schedKey(roundID, shareIndex, proposalID, treePosition)] = time.Now().Add(delay)
	}
	return nil
}

func (s *ShareStore) loadShare(roundID string, shareIndex, proposalID uint32, treePosition uint64) (QueuedShare, bool) {
	var q QueuedShare
	var commsJSON string
	var state, attempts int

	err := s.db.QueryRow(
		`SELECT shares_hash, proposal_id, vote_decision, enc_share_c1, enc_share_c2,
		        tree_position, share_comms, primary_blind, state, attempts, vote_end_time
		 FROM shares WHERE round_id = ? AND share_index = ? AND proposal_id = ? AND tree_position = ?`,
		roundID, shareIndex, proposalID, treePosition,
	).Scan(
		&q.Payload.SharesHash,
		&q.Payload.ProposalID,
		&q.Payload.VoteDecision,
		&q.Payload.EncShare.C1,
		&q.Payload.EncShare.C2,
		&q.Payload.TreePosition,
		&commsJSON,
		&q.Payload.PrimaryBlind,
		&state,
		&attempts,
		&q.VoteEndTime,
	)
	if err != nil {
		return q, false
	}

	q.Payload.VoteRoundID = roundID
	q.Payload.EncShare.ShareIndex = shareIndex
	q.Payload.ShareIndex = shareIndex
	q.State = ShareState(state)
	q.Attempts = attempts

	if err := json.Unmarshal([]byte(commsJSON), &q.Payload.ShareComms); err != nil {
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
	if len(existing.ShareComms) != len(incoming.ShareComms) {
		return false
	}
	for i := range existing.ShareComms {
		if existing.ShareComms[i] != incoming.ShareComms[i] {
			return false
		}
	}

	if existing.PrimaryBlind != incoming.PrimaryBlind {
		return false
	}

	return true
}

// getVoteEndTime returns the cached vote_end_time for a round, fetching from
// SQLite or the keeper if not in memory. Returns an error if the round is
// unknown (the share should be rejected).
func (s *ShareStore) getVoteEndTime(roundID string) (uint64, error) {
	s.mu.Lock()
	if vet, ok := s.roundCache[roundID]; ok {
		s.mu.Unlock()
		return vet, nil
	}

	// Check SQLite rounds table.
	var vet uint64
	err := s.db.QueryRow("SELECT vote_end_time FROM rounds WHERE round_id = ?", roundID).Scan(&vet)
	if err == nil {
		s.roundCache[roundID] = vet
		s.mu.Unlock()
		return vet, nil
	}
	s.mu.Unlock()

	// Fetch from keeper (outside lock — direct KV read).
	if s.fetchRoundInfo == nil {
		return 0, fmt.Errorf("%w: no round fetcher configured", ErrUnknownRound)
	}
	vet, err = s.fetchRoundInfo(roundID)
	if err != nil {
		return 0, err
	}

	// Cache in both memory and SQLite.
	s.mu.Lock()
	s.roundCache[roundID] = vet
	s.mu.Unlock()

	_, _ = s.db.Exec(
		"INSERT OR IGNORE INTO rounds (round_id, vote_end_time) VALUES (?, ?)",
		roundID, vet,
	)

	return vet, nil
}

// uniformDelay samples a delay uniformly from [0, remaining_window) where
// remaining_window = vote_end_time − now − 60s. A minimum floor (minDelay)
// prevents near-zero samples from making shares trivially linkable to their
// submission session. When vote_end_time is unknown (0) the delay falls back
// to a uniform sample over [minDelay, meanDelay*2] as a best-effort spread.
func (s *ShareStore) uniformDelay(voteEndTime uint64) time.Duration {
	// Benchmark / testing mode: skip all delays.
	if s.meanDelay == 0 && s.minDelay == 0 {
		return 0
	}

	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		s.logError("uniformDelay: crypto/rand failed, falling back to min delay", "error", err)
		return s.minDelay
	}
	u := float64(binary.LittleEndian.Uint64(buf[:])) / (float64(1<<64) + 1.0)

	if voteEndTime == 0 {
		// Fallback: uniform over [minDelay, 2*meanDelay].
		spread := 2*s.meanDelay - s.minDelay
		if spread <= 0 {
			return s.minDelay
		}
		return s.minDelay + time.Duration(u*float64(spread))
	}

	remaining := time.Until(time.Unix(int64(voteEndTime), 0)) - 60*time.Second
	if remaining <= 0 {
		return 0
	}

	delay := time.Duration(u * float64(remaining))
	if delay < s.minDelay && remaining > s.minDelay {
		delay = s.minDelay
	}
	return delay
}
