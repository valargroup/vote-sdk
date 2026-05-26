package helper

import (
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	_ "modernc.org/sqlite"
)

// ErrUnknownRound is returned when a share references a round that does not
// exist on-chain. Callers can check for this with errors.Is to distinguish
// it from transient failures.
var ErrUnknownRound = errors.New("unknown voting round")

// ErrInvalidSubmitAt is returned when submit_at is after vote end time.
var ErrInvalidSubmitAt = errors.New("invalid submit_at")

// ErrRoundClosed is returned when a share arrives after the vote window closed.
var ErrRoundClosed = errors.New("voting round closed")

// ErrInvalidRoundInfo is returned when cached round metadata cannot produce a
// valid queue summary.
var ErrInvalidRoundInfo = errors.New("invalid voting round metadata")

// ErrCompletedRoundDataExpired is returned when public completed-round helper
// data is outside the configured serving window.
var ErrCompletedRoundDataExpired = errors.New("completed round data expired")

// ShareStore is a SQLite-backed share queue with ephemeral in-memory scheduling.
// Payload data and processing state are persisted; client-provided submit_at
// timestamps control when each share is eligible for proof generation.
type ShareStore struct {
	db              *sql.DB
	lockFile        *os.File
	mu              sync.Mutex
	schedule        map[string]time.Time // key: "round_id:share_index:proposal_id:tree_position"
	scheduleChanged chan struct{}
	roundCache      map[string]RoundInfo             // roundID -> chain round metadata
	fetchRoundInfo  RoundInfoFetcher                 // queries the chain; may be nil in tests
	logger          func(msg string, keyvals ...any) // optional error logger
	logInfo         func(msg string, keyvals ...any) // optional info logger
	captureErr      func(err error, tags map[string]string)
}

// EnqueueResult reports how an enqueue attempt was handled.
type EnqueueResult int

const (
	EnqueueInserted EnqueueResult = iota
	EnqueueDuplicate
	EnqueueConflict
)

// NewShareStore opens (or creates) a SQLite database and runs migrations.
func NewShareStore(dbPath string, fetcher RoundInfoFetcher) (*ShareStore, error) {
	lockFile, err := acquireShareStoreLock(dbPath)
	if err != nil {
		return nil, err
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		releaseShareStoreLock(lockFile)
		return nil, fmt.Errorf("open database: %w", err)
	}

	// Enable WAL mode for concurrent reads.
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		releaseShareStoreLock(lockFile)
		return nil, fmt.Errorf("set WAL mode: %w", err)
	}

	// Overwrite deleted content with zeros so forensic recovery cannot
	// retrieve witness data (blinds, commitments, encrypted shares).
	if _, err := db.Exec("PRAGMA secure_delete=ON"); err != nil {
		db.Close()
		releaseShareStoreLock(lockFile)
		return nil, fmt.Errorf("set secure_delete: %w", err)
	}

	// Run migrations.
	if err := migrate(db); err != nil {
		db.Close()
		releaseShareStoreLock(lockFile)
		return nil, fmt.Errorf("migration: %w", err)
	}

	s := &ShareStore{
		db:              db,
		lockFile:        lockFile,
		schedule:        make(map[string]time.Time),
		scheduleChanged: make(chan struct{}, 1),
		roundCache:      make(map[string]RoundInfo),
		fetchRoundInfo:  fetcher,
	}

	// Recover non-terminal shares from SQLite.
	if err := s.recover(); err != nil {
		db.Close()
		releaseShareStoreLock(lockFile)
		return nil, fmt.Errorf("recovery: %w", err)
	}

	return s, nil
}

// acquireShareStoreLock takes a process-wide advisory lock for a helper DB file.
func acquireShareStoreLock(dbPath string) (*os.File, error) {
	if dbPath == "" || dbPath == ":memory:" || strings.Contains(dbPath, "mode=memory") {
		return nil, nil
	}
	absPath, err := filepath.Abs(dbPath)
	if err != nil {
		return nil, fmt.Errorf("resolve helper db path: %w", err)
	}
	lockPath := absPath + ".lock"
	lockFile, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open helper db lock %s: %w", lockPath, err)
	}
	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lockFile.Close()
		return nil, fmt.Errorf("helper db is already in use; stop svoted before running helper queue rescue commands: %w", err)
	}
	return lockFile, nil
}

// releaseShareStoreLock releases and closes a lock file returned by acquireShareStoreLock.
func releaseShareStoreLock(lockFile *os.File) error {
	if lockFile == nil {
		return nil
	}
	unlockErr := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN)
	closeErr := lockFile.Close()
	return errors.Join(unlockErr, closeErr)
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
			submit_at       INTEGER NOT NULL DEFAULT 0,
			original_submit_at INTEGER NOT NULL DEFAULT 0,
			received_at     INTEGER NOT NULL DEFAULT 0,
			share_nullifier TEXT NOT NULL DEFAULT '',
			closed_at       INTEGER NOT NULL DEFAULT 0,
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
			round_id         TEXT PRIMARY KEY,
			vote_end_time    INTEGER NOT NULL,
			created_at_time  INTEGER NOT NULL DEFAULT 0
		)
	`); err != nil {
		return fmt.Errorf("create rounds table: %w", err)
	}

	hasRoundCreatedAtTime, err := tableHasColumn(db, "rounds", "created_at_time")
	if err != nil {
		return fmt.Errorf("check rounds schema: %w", err)
	}
	if !hasRoundCreatedAtTime {
		if _, err := db.Exec("ALTER TABLE rounds ADD COLUMN created_at_time INTEGER NOT NULL DEFAULT 0"); err != nil {
			return fmt.Errorf("add rounds.created_at_time: %w", err)
		}
	}

	hasSubmitAt, err := tableHasColumn(db, "shares", "submit_at")
	if err != nil {
		return fmt.Errorf("check shares schema: %w", err)
	}
	if !hasSubmitAt {
		if _, err := db.Exec("ALTER TABLE shares ADD COLUMN submit_at INTEGER NOT NULL DEFAULT 0"); err != nil {
			return fmt.Errorf("add shares.submit_at: %w", err)
		}
	}

	hasOriginalSubmitAt, err := tableHasColumn(db, "shares", "original_submit_at")
	if err != nil {
		return fmt.Errorf("check shares schema: %w", err)
	}
	if !hasOriginalSubmitAt {
		if _, err := db.Exec("ALTER TABLE shares ADD COLUMN original_submit_at INTEGER NOT NULL DEFAULT 0"); err != nil {
			return fmt.Errorf("add shares.original_submit_at: %w", err)
		}
		if _, err := db.Exec("UPDATE shares SET original_submit_at = submit_at WHERE original_submit_at = 0 AND submit_at != 0"); err != nil {
			return fmt.Errorf("backfill shares.original_submit_at: %w", err)
		}
	}

	hasReceivedAt, err := tableHasColumn(db, "shares", "received_at")
	if err != nil {
		return fmt.Errorf("check shares schema: %w", err)
	}
	if !hasReceivedAt {
		if _, err := db.Exec("ALTER TABLE shares ADD COLUMN received_at INTEGER NOT NULL DEFAULT 0"); err != nil {
			return fmt.Errorf("add shares.received_at: %w", err)
		}
	}

	hasShareNullifier, err := tableHasColumn(db, "shares", "share_nullifier")
	if err != nil {
		return fmt.Errorf("check shares schema: %w", err)
	}
	if !hasShareNullifier {
		if _, err := db.Exec("ALTER TABLE shares ADD COLUMN share_nullifier TEXT NOT NULL DEFAULT ''"); err != nil {
			return fmt.Errorf("add shares.share_nullifier: %w", err)
		}
	}

	hasClosedAt, err := tableHasColumn(db, "shares", "closed_at")
	if err != nil {
		return fmt.Errorf("check shares schema: %w", err)
	}
	if !hasClosedAt {
		if _, err := db.Exec("ALTER TABLE shares ADD COLUMN closed_at INTEGER NOT NULL DEFAULT 0"); err != nil {
			return fmt.Errorf("add shares.closed_at: %w", err)
		}
	}

	hasRoundsClosedAt, err := tableHasColumn(db, "rounds", "closed_at")
	if err != nil {
		return fmt.Errorf("check rounds schema: %w", err)
	}
	if !hasRoundsClosedAt {
		if _, err := db.Exec("ALTER TABLE rounds ADD COLUMN closed_at INTEGER NOT NULL DEFAULT 0"); err != nil {
			return fmt.Errorf("add rounds.closed_at: %w", err)
		}
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
// (round_id, share_index, proposal_id, tree_position). It first adds any
// missing queue metadata columns so existing scheduling and audit data survives
// the table rebuild.
func migrateSharesPK(db *sql.DB) error {
	columns := []struct {
		name       string
		definition string
		backfill   string
	}{
		{name: "share_comms", definition: "TEXT NOT NULL DEFAULT '[]'"},
		{name: "primary_blind", definition: "TEXT NOT NULL DEFAULT ''"},
		{name: "vote_end_time", definition: "INTEGER NOT NULL DEFAULT 0"},
		{name: "submit_at", definition: "INTEGER NOT NULL DEFAULT 0"},
		{
			name:       "original_submit_at",
			definition: "INTEGER NOT NULL DEFAULT 0",
			backfill:   "UPDATE shares SET original_submit_at = submit_at WHERE original_submit_at = 0 AND submit_at != 0",
		},
		{name: "received_at", definition: "INTEGER NOT NULL DEFAULT 0"},
		{name: "share_nullifier", definition: "TEXT NOT NULL DEFAULT ''"},
		{name: "closed_at", definition: "INTEGER NOT NULL DEFAULT 0"},
	}
	for _, column := range columns {
		hasColumn, err := tableHasColumn(db, "shares", column.name)
		if err != nil {
			return fmt.Errorf("check %s column: %w", column.name, err)
		}
		if !hasColumn {
			if _, err := db.Exec(fmt.Sprintf("ALTER TABLE shares ADD COLUMN %s %s", column.name, column.definition)); err != nil {
				return fmt.Errorf("add %s before PK migration: %w", column.name, err)
			}
			if column.backfill != "" {
				if _, err := db.Exec(column.backfill); err != nil {
					return fmt.Errorf("backfill %s before PK migration: %w", column.name, err)
				}
			}
		}
	}

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

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
			submit_at       INTEGER NOT NULL DEFAULT 0,
			original_submit_at INTEGER NOT NULL DEFAULT 0,
			received_at     INTEGER NOT NULL DEFAULT 0,
			share_nullifier TEXT NOT NULL DEFAULT '',
			closed_at       INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (round_id, share_index, proposal_id, tree_position)
		)`); err != nil {
		return err
	}

	if _, err := tx.Exec(`INSERT INTO shares_new SELECT
			round_id, share_index, shares_hash, proposal_id, vote_decision,
			enc_share_c1, enc_share_c2, tree_position, share_comms,
			primary_blind, state, attempts, vote_end_time, submit_at,
			original_submit_at, received_at, share_nullifier, closed_at
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

// Enqueue adds a share payload using the wallet-provided submit_at time.
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

	// Fetch round metadata before acquiring the lock (direct keeper KV read).
	roundInfo, err := s.getRoundInfo(payload.VoteRoundID)
	if err != nil {
		return EnqueueConflict, err
	}

	// Validate submit_at: must not exceed vote end time.
	if payload.SubmitAt > roundInfo.VoteEndTime {
		return EnqueueConflict, fmt.Errorf("%w: submit_at (%d) > vote_end_time (%d)", ErrInvalidSubmitAt, payload.SubmitAt, roundInfo.VoteEndTime)
	}
	receivedAt := uint64(time.Now().Unix())
	if roundInfo.VoteEndTime != 0 && receivedAt >= roundInfo.VoteEndTime {
		return EnqueueConflict, fmt.Errorf("%w: vote_end_time %d <= now %d", ErrRoundClosed, roundInfo.VoteEndTime, receivedAt)
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	res, err := s.db.Exec(
		`INSERT INTO shares
		 (round_id, share_index, shares_hash, proposal_id, vote_decision,
		  enc_share_c1, enc_share_c2, tree_position, share_comms, primary_blind, state, attempts, vote_end_time, submit_at, original_submit_at, received_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, 0, ?, ?, ?, ?)
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
		roundInfo.VoteEndTime,
		payload.SubmitAt,
		payload.SubmitAt,
		receivedAt,
	)
	if err != nil {
		return EnqueueConflict, fmt.Errorf("insert share: %w", err)
	}

	// Only schedule if the row was actually inserted (not a duplicate).
	affected, _ := res.RowsAffected()
	if affected > 0 {
		var schedTime time.Time
		if payload.SubmitAt == 0 {
			schedTime = time.Now()
		} else {
			schedTime = time.Unix(int64(payload.SubmitAt), 0)
		}
		key := schedKey(payload.VoteRoundID, payload.EncShare.ShareIndex, payload.ProposalID, payload.TreePosition)
		s.schedule[key] = schedTime
		s.notifyScheduleChangedLocked()
		if s.logInfo != nil {
			s.logInfo("share scheduled",
				"round_id", payload.VoteRoundID,
				"share_index", payload.EncShare.ShareIndex,
				"proposal_id", payload.ProposalID,
				"submit_at", payload.SubmitAt,
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

// ScheduleChanged returns a buffered notification channel that receives a signal
// when enqueue or retry scheduling changes. Multiple changes may coalesce.
func (s *ShareStore) ScheduleChanged() <-chan struct{} {
	return s.scheduleChanged
}

func (s *ShareStore) notifyScheduleChangedLocked() {
	select {
	case s.scheduleChanged <- struct{}{}:
	default:
	}
}

// NextScheduledTime returns the earliest scheduled share time.
func (s *ShareStore) NextScheduledTime() (time.Time, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var next time.Time
	for _, scheduledAt := range s.schedule {
		if next.IsZero() || scheduledAt.Before(next) {
			next = scheduledAt
		}
	}
	if next.IsZero() {
		return time.Time{}, false
	}
	return next, true
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
		`UPDATE shares SET state = 2,
		        enc_share_c1 = '', enc_share_c2 = '',
		        share_comms = '[]', primary_blind = ''
		 WHERE round_id = ? AND share_index = ? AND proposal_id = ? AND tree_position = ? AND state = 1`,
		roundID, shareIndex, proposalID, treePosition,
	); err != nil {
		s.logError("MarkSubmitted: db update failed", "round_id", roundID, "share_index", shareIndex, "proposal_id", proposalID, "tree_position", treePosition, "error", err)
	}
	key := schedKey(roundID, shareIndex, proposalID, treePosition)
	if _, ok := s.schedule[key]; ok {
		delete(s.schedule, key)
		s.notifyScheduleChangedLocked()
	}
}

// MarkFailed marks a share processing attempt as failed, with retry or
// permanent failure after max attempts. If a share nullifier is supplied, it is
// retained on permanent failure so closeout can later detect whether another
// helper accepted the share on-chain.
func (s *ShareStore) MarkFailed(roundID string, shareIndex, proposalID uint32, treePosition uint64, shareNullifier ...[]byte) {
	const maxAttempts = 5
	shareNullifierHex := ""
	if len(shareNullifier) > 0 && len(shareNullifier[0]) > 0 {
		shareNullifierHex = hex.EncodeToString(shareNullifier[0])
	}

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
		// Permanently failed — clear witness data.
		if _, err := s.db.Exec(
			`UPDATE shares SET state = 3, attempts = ?,
				        share_nullifier = CASE
				          WHEN share_nullifier = '' THEN ?
				          ELSE share_nullifier
				        END,
				        enc_share_c1 = '', enc_share_c2 = '',
				        share_comms = '[]', primary_blind = ''
				 WHERE round_id = ? AND share_index = ? AND proposal_id = ? AND tree_position = ?`,
			newAttempts, shareNullifierHex, roundID, shareIndex, proposalID, treePosition,
		); err != nil {
			s.logError("MarkFailed: db update (permanent) failed", "error", err)
		}
		if _, ok := s.schedule[key]; ok {
			delete(s.schedule, key)
			s.notifyScheduleChangedLocked()
		}
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
		s.notifyScheduleChangedLocked()
	}
}

func (s *ShareStore) logError(msg string, keyvals ...any) {
	if s.logger != nil {
		s.logger(msg, keyvals...)
	}
	if s.captureErr == nil {
		return
	}
	var err error
	tags := map[string]string{
		"component": "helper_store",
		"stage":     msg,
	}
	for i := 0; i+1 < len(keyvals); i += 2 {
		key, ok := keyvals[i].(string)
		if !ok {
			continue
		}
		value := keyvals[i+1]
		if key == "error" {
			if e, ok := value.(error); ok {
				err = e
			}
			continue
		}
		switch v := value.(type) {
		case string:
			tags[key] = v
		case fmt.Stringer:
			tags[key] = v.String()
		case int:
			tags[key] = strconv.Itoa(v)
		case uint32:
			tags[key] = strconv.FormatUint(uint64(v), 10)
		case uint64:
			tags[key] = strconv.FormatUint(v, 10)
		}
	}
	if err != nil {
		s.captureErr(fmt.Errorf("%s: %w", msg, err), tags)
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
		case 4:
			entry.MissedDeadline += count
		case 5:
			entry.ObservedOnChain += count
		}
		result[roundID] = entry
	}

	return result
}

// isProcessableShareState reports whether a queue row can still be submitted.
func isProcessableShareState(state ShareState) bool {
	return state == ShareStateReceived || state == ShareStateWitnessed
}

// ExportQueue returns every persisted row for a round. Terminal rows are
// included for local debugging, but their witness material should already be
// cleared by MarkSubmitted or permanent MarkFailed.
func (s *ShareStore) ExportQueue(roundID string, now time.Time) (QueueExport, error) {
	export := QueueExport{
		Version:    QueueExportVersion,
		RoundID:    roundID,
		ExportedAt: uint64(now.Unix()),
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.db.QueryRow(
		"SELECT created_at_time, vote_end_time FROM rounds WHERE round_id = ?",
		roundID,
	).Scan(&export.Round.CreatedAtTime, &export.Round.VoteEndTime); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return QueueExport{}, fmt.Errorf("read round metadata: %w", err)
	}

	rows, err := s.db.Query(
		`SELECT share_index, shares_hash, proposal_id, vote_decision,
		        enc_share_c1, enc_share_c2, tree_position, share_comms,
		        primary_blind, state, attempts, vote_end_time, submit_at,
		        original_submit_at, received_at, share_nullifier, closed_at
		   FROM shares
		  WHERE round_id = ?
		  ORDER BY submit_at, received_at, proposal_id, vote_decision, share_index, tree_position`,
		roundID,
	)
	if err != nil {
		return QueueExport{}, fmt.Errorf("query queue rows: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var row QueueExportRow
		var state int
		var commsJSON string
		if err := rows.Scan(
			&row.ShareIndex,
			&row.SharesHash,
			&row.ProposalID,
			&row.VoteDecision,
			&row.EncShare.C1,
			&row.EncShare.C2,
			&row.TreePosition,
			&commsJSON,
			&row.PrimaryBlind,
			&state,
			&row.Attempts,
			&row.VoteEndTime,
			&row.SubmitAt,
			&row.OriginalSubmitAt,
			&row.ReceivedAt,
			&row.ShareNullifier,
			&row.ClosedAt,
		); err != nil {
			return QueueExport{}, fmt.Errorf("scan queue row: %w", err)
		}
		row.EncShare.ShareIndex = row.ShareIndex
		row.State = ShareState(state)
		row.Processable = isProcessableShareState(row.State)
		if row.OriginalSubmitAt == 0 {
			row.OriginalSubmitAt = row.SubmitAt
		}
		if commsJSON != "" {
			if err := json.Unmarshal([]byte(commsJSON), &row.ShareComms); err != nil {
				return QueueExport{}, fmt.Errorf("decode share_comms for share_index %d: %w", row.ShareIndex, err)
			}
		}
		if export.Round.VoteEndTime == 0 && row.VoteEndTime != 0 {
			export.Round.VoteEndTime = row.VoteEndTime
		}
		export.Rows = append(export.Rows, row)
	}
	if err := rows.Err(); err != nil {
		return QueueExport{}, fmt.Errorf("iterate queue rows: %w", err)
	}

	return export, nil
}

// ImportQueue inserts processable rows from a local rescue export. Submitted
// and permanently failed rows are counted and skipped so importing a full
// export cannot submit terminal shares again.
func (s *ShareStore) ImportQueue(export QueueExport, opts QueueImportOptions) (QueueImportResult, error) {
	if export.Version != QueueExportVersion {
		return QueueImportResult{}, fmt.Errorf("unsupported queue export version %d", export.Version)
	}
	if strings.TrimSpace(export.RoundID) == "" {
		return QueueImportResult{}, errors.New("queue export missing round_id")
	}

	now := uint64(time.Now().Unix())
	result := QueueImportResult{}
	schedule := make(map[string]time.Time)

	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return QueueImportResult{}, fmt.Errorf("begin import: %w", err)
	}
	defer tx.Rollback()

	var exportedRoundInfo *RoundInfo
	if export.Round.CreatedAtTime != 0 || export.Round.VoteEndTime != 0 {
		exportedRoundInfo = &RoundInfo{
			CreatedAtTime: export.Round.CreatedAtTime,
			VoteEndTime:   export.Round.VoteEndTime,
		}
	}

	for _, row := range export.Rows {
		if !isProcessableShareState(row.State) {
			result.SkippedTerminal++
			continue
		}

		submitAt := row.SubmitAt
		originalSubmitAt := row.OriginalSubmitAt
		if originalSubmitAt == 0 {
			originalSubmitAt = submitAt
		}
		if opts.ForceReady {
			submitAt = 0
		}
		receivedAt := row.ReceivedAt
		if receivedAt == 0 {
			receivedAt = now
		}
		voteEndTime := row.VoteEndTime
		if voteEndTime == 0 {
			voteEndTime = export.Round.VoteEndTime
		}
		if !opts.ForceReady && submitAt > voteEndTime {
			return QueueImportResult{}, fmt.Errorf("%w: imported submit_at (%d) > vote_end_time (%d) for share_index %d proposal_id %d tree_position %d", ErrInvalidSubmitAt, submitAt, voteEndTime, row.ShareIndex, row.ProposalID, row.TreePosition)
		}
		commsJSON, err := json.Marshal(row.ShareComms)
		if err != nil {
			return QueueImportResult{}, fmt.Errorf("marshal share_comms for share_index %d: %w", row.ShareIndex, err)
		}

		res, err := tx.Exec(
			`INSERT INTO shares
			 (round_id, share_index, shares_hash, proposal_id, vote_decision,
			  enc_share_c1, enc_share_c2, tree_position, share_comms, primary_blind,
			  state, attempts, vote_end_time, submit_at, original_submit_at, received_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?, ?, ?, ?)
			 ON CONFLICT(round_id, share_index, proposal_id, tree_position) DO NOTHING`,
			export.RoundID,
			row.ShareIndex,
			row.SharesHash,
			row.ProposalID,
			row.VoteDecision,
			row.EncShare.C1,
			row.EncShare.C2,
			row.TreePosition,
			string(commsJSON),
			row.PrimaryBlind,
			row.Attempts,
			voteEndTime,
			submitAt,
			originalSubmitAt,
			receivedAt,
		)
		if err != nil {
			return QueueImportResult{}, fmt.Errorf("insert share_index %d proposal_id %d tree_position %d: %w", row.ShareIndex, row.ProposalID, row.TreePosition, err)
		}
		affected, _ := res.RowsAffected()
		if affected > 0 {
			result.Inserted++
			schedule[schedKey(export.RoundID, row.ShareIndex, row.ProposalID, row.TreePosition)] = scheduledTime(submitAt)
			continue
		}

		duplicate, existingState, err := importRowMatchesExisting(tx, export.RoundID, row)
		if err != nil {
			return QueueImportResult{}, err
		}
		if duplicate {
			result.Duplicates++
			if opts.ForceReady && isProcessableShareState(existingState) {
				if err := forceReadyExistingImportRow(tx, export.RoundID, row, originalSubmitAt); err != nil {
					return QueueImportResult{}, err
				}
				if existingState == ShareStateReceived {
					schedule[schedKey(export.RoundID, row.ShareIndex, row.ProposalID, row.TreePosition)] = scheduledTime(0)
				}
			}
		} else {
			result.Conflicts++
		}
	}

	var importedRoundInfo *RoundInfo
	if exportedRoundInfo != nil && result.Inserted+result.Duplicates > 0 {
		if _, err := tx.Exec(
			`INSERT INTO rounds (round_id, vote_end_time, created_at_time)
			 VALUES (?, ?, ?)
			 ON CONFLICT(round_id) DO UPDATE SET
			   vote_end_time = excluded.vote_end_time,
			   created_at_time = excluded.created_at_time`,
			export.RoundID,
			exportedRoundInfo.VoteEndTime,
			exportedRoundInfo.CreatedAtTime,
		); err != nil {
			return QueueImportResult{}, fmt.Errorf("cache round metadata: %w", err)
		}
		importedRoundInfo = exportedRoundInfo
	}

	if err := tx.Commit(); err != nil {
		return QueueImportResult{}, fmt.Errorf("commit import: %w", err)
	}

	for key, at := range schedule {
		s.schedule[key] = at
	}
	if importedRoundInfo != nil {
		s.roundCache[export.RoundID] = *importedRoundInfo
	}
	if len(schedule) > 0 {
		s.notifyScheduleChangedLocked()
	}

	return result, nil
}

// scheduledTime converts a submit_at unix timestamp into an in-memory schedule time.
func scheduledTime(submitAt uint64) time.Time {
	if submitAt == 0 {
		return time.Now()
	}
	return time.Unix(int64(submitAt), 0)
}

// forceReadyExistingImportRow moves an identical existing processable import row
// into the immediate schedule while preserving the row's original submit time.
func forceReadyExistingImportRow(tx *sql.Tx, roundID string, row QueueExportRow, originalSubmitAt uint64) error {
	if _, err := tx.Exec(
		`UPDATE shares
		    SET submit_at = 0,
		        original_submit_at = CASE
		          WHEN original_submit_at = 0 THEN ?
		          ELSE original_submit_at
		        END
		  WHERE round_id = ? AND share_index = ? AND proposal_id = ? AND tree_position = ?
		    AND state IN (?, ?)`,
		originalSubmitAt,
		roundID,
		row.ShareIndex,
		row.ProposalID,
		row.TreePosition,
		ShareStateReceived,
		ShareStateWitnessed,
	); err != nil {
		return fmt.Errorf("force ready existing share_index %d proposal_id %d tree_position %d: %w", row.ShareIndex, row.ProposalID, row.TreePosition, err)
	}
	return nil
}

// importRowMatchesExisting compares an import row against the existing queue row.
func importRowMatchesExisting(tx *sql.Tx, roundID string, row QueueExportRow) (bool, ShareState, error) {
	var existing SharePayload
	var commsJSON string
	var state int
	err := tx.QueryRow(
		`SELECT shares_hash, vote_decision, enc_share_c1, enc_share_c2,
		        share_comms, primary_blind, state
		   FROM shares
		  WHERE round_id = ? AND share_index = ? AND proposal_id = ? AND tree_position = ?`,
		roundID,
		row.ShareIndex,
		row.ProposalID,
		row.TreePosition,
	).Scan(
		&existing.SharesHash,
		&existing.VoteDecision,
		&existing.EncShare.C1,
		&existing.EncShare.C2,
		&commsJSON,
		&existing.PrimaryBlind,
		&state,
	)
	if err != nil {
		return false, 0, fmt.Errorf("read existing share_index %d proposal_id %d tree_position %d: %w", row.ShareIndex, row.ProposalID, row.TreePosition, err)
	}
	existing.VoteRoundID = roundID
	existing.ProposalID = row.ProposalID
	existing.EncShare.ShareIndex = row.ShareIndex
	existing.TreePosition = row.TreePosition
	if commsJSON != "" {
		if err := json.Unmarshal([]byte(commsJSON), &existing.ShareComms); err != nil {
			return false, 0, fmt.Errorf("decode existing share_comms for share_index %d: %w", row.ShareIndex, err)
		}
	}

	incoming := SharePayload{
		SharesHash:   row.SharesHash,
		ProposalID:   row.ProposalID,
		VoteDecision: row.VoteDecision,
		EncShare: EncryptedShareWire{
			C1:         row.EncShare.C1,
			C2:         row.EncShare.C2,
			ShareIndex: row.ShareIndex,
		},
		TreePosition: row.TreePosition,
		VoteRoundID:  roundID,
		ShareComms:   row.ShareComms,
		PrimaryBlind: row.PrimaryBlind,
	}
	return payloadEqual(existing, incoming), ShareState(state), nil
}

const (
	queueSummaryMinute uint64 = 60
	queueSummaryHour   uint64 = 60 * queueSummaryMinute
	queueSummaryDay    uint64 = 24 * queueSummaryHour

	// maxQueueSummaryBuckets bounds public response size and allocation. The
	// longest rounds use 6 hour buckets, so 4096 buckets covers 24,576 hours,
	// 1,024 days, or about 2.8 years of voting duration.
	maxQueueSummaryBuckets = 4096
)

// queueSummaryPolicyBucketSeconds chooses the fixed bucket size for a round
// based on the round's total voting duration.
func queueSummaryPolicyBucketSeconds(durationSeconds uint64) uint64 {
	switch {
	case durationSeconds >= 21*queueSummaryDay:
		return 6 * queueSummaryHour
	case durationSeconds >= 7*queueSummaryDay:
		return 3 * queueSummaryHour
	case durationSeconds >= queueSummaryDay:
		return queueSummaryHour
	case durationSeconds >= queueSummaryHour:
		return 15 * queueSummaryMinute
	default:
		return queueSummaryMinute
	}
}

// queueSummaryLastMinuteStart returns the start of the final public summary
// window. The window is 40% of the round duration, capped at 6 hours.
func queueSummaryLastMinuteStart(createdAtTime, voteEndTime uint64) uint64 {
	if voteEndTime <= createdAtTime {
		return createdAtTime
	}
	duration := voteEndTime - createdAtTime
	window := duration * 40 / 100
	if window < 1 {
		window = 1
	}
	if window > 6*queueSummaryHour {
		window = 6 * queueSummaryHour
	}
	return voteEndTime - window
}

// queueSummaryBucketIndex maps a timestamp into the bounded bucket range for
// the round, clamping times outside the voting window to the nearest bucket.
func queueSummaryBucketIndex(ts, createdAtTime, voteEndTime, bucketSeconds uint64, bucketCount int) int {
	if bucketCount <= 1 || ts <= createdAtTime {
		return 0
	}
	if ts >= voteEndTime {
		return bucketCount - 1
	}
	idx := int((ts - createdAtTime) / bucketSeconds)
	if idx >= bucketCount {
		return bucketCount - 1
	}
	return idx
}

// QueueSummary returns a public, coarse histogram for one round across all
// proposals handled by this helper.
func (s *ShareStore) QueueSummary(roundID string, now time.Time, completedRoundDataServeSeconds int64) (QueueSummary, error) {
	completedRoundDataServeSeconds = NormalizeCompletedRoundDataServeSeconds(completedRoundDataServeSeconds)

	info, err := s.getRoundInfo(roundID)
	if err != nil {
		return QueueSummary{}, err
	}
	if info.CreatedAtTime == 0 {
		return QueueSummary{}, fmt.Errorf("%w: missing created_at_time for round %s", ErrInvalidRoundInfo, roundID)
	}
	if info.VoteEndTime <= info.CreatedAtTime {
		return QueueSummary{}, fmt.Errorf("%w: vote_end_time must be after created_at_time for round %s", ErrInvalidRoundInfo, roundID)
	}

	generatedAt := uint64(now.Unix())
	if completedRoundDataServeSeconds >= 0 && generatedAt > info.VoteEndTime {
		serveUntil := info.VoteEndTime + uint64(completedRoundDataServeSeconds)
		if generatedAt > serveUntil {
			return QueueSummary{}, fmt.Errorf("%w: round %s expired at %d", ErrCompletedRoundDataExpired, roundID, serveUntil)
		}
	}
	duration := info.VoteEndTime - info.CreatedAtTime
	bucketSeconds := queueSummaryPolicyBucketSeconds(duration)
	bucketCount64 := duration / bucketSeconds
	if duration%bucketSeconds != 0 {
		bucketCount64++
	}
	if bucketCount64 == 0 || bucketCount64 > maxQueueSummaryBuckets {
		return QueueSummary{}, fmt.Errorf("%w: queue summary bucket count out of range for round %s", ErrInvalidRoundInfo, roundID)
	}
	bucketCount := int(bucketCount64)

	summary := QueueSummary{
		RoundID:         roundID,
		BucketSeconds:   bucketSeconds,
		CreatedAtTime:   info.CreatedAtTime,
		VoteEndTime:     info.VoteEndTime,
		GeneratedAt:     generatedAt,
		LastMinuteStart: queueSummaryLastMinuteStart(info.CreatedAtTime, info.VoteEndTime),
		Buckets:         make([]QueueSummaryBucket, bucketCount),
	}
	for i := range summary.Buckets {
		start := info.CreatedAtTime + uint64(i)*bucketSeconds
		end := start + bucketSeconds
		if end > info.VoteEndTime {
			end = info.VoteEndTime
		}
		summary.Buckets[i] = QueueSummaryBucket{
			Start: start,
			End:   end,
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	rows, err := s.db.Query(
		`SELECT state, submit_at, received_at, COUNT(*)
		   FROM shares
		  WHERE round_id = ?
		  GROUP BY state, submit_at, received_at`,
		roundID,
	)
	if err != nil {
		return QueueSummary{}, err
	}
	defer rows.Close()

	for rows.Next() {
		var state int
		var submitAt, receivedAt uint64
		var count int
		if err := rows.Scan(&state, &submitAt, &receivedAt, &count); err != nil {
			return QueueSummary{}, err
		}

		effectiveTime := submitAt
		if effectiveTime == 0 {
			effectiveTime = receivedAt
		}
		if effectiveTime == 0 {
			effectiveTime = info.CreatedAtTime
		}

		idx := queueSummaryBucketIndex(effectiveTime, info.CreatedAtTime, info.VoteEndTime, bucketSeconds, bucketCount)
		bucket := &summary.Buckets[idx]
		switch ShareState(state) {
		case ShareStateReceived:
			if effectiveTime <= generatedAt {
				bucket.OverduePending += count
			} else {
				bucket.PendingFuture += count
			}
		case ShareStateWitnessed:
			bucket.Processing += count
		case ShareStateSubmitted:
			bucket.Submitted += count
		case ShareStateFailed:
			bucket.Failed += count
		case ShareStateMissedDeadline:
			bucket.MissedDeadline += count
		case ShareStateObservedOnChain:
			bucket.ObservedOnChain += count
		}
		bucket.Total += count
	}
	if err := rows.Err(); err != nil {
		return QueueSummary{}, err
	}

	return summary, nil
}

// ExpiredRoundSummaries returns per-round queue counts for rounds whose voting
// window has ended and have not been closed out yet.
func (s *ShareStore) ExpiredRoundSummaries(now time.Time) ([]ExpiredRoundSummary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	rows, err := s.db.Query(
		`SELECT shares.round_id, shares.state, COUNT(*)
		   FROM shares
		   LEFT JOIN rounds ON rounds.round_id = shares.round_id
		  WHERE shares.vote_end_time > 0
		    AND shares.vote_end_time < ?
		    AND COALESCE(rounds.closed_at, 0) = 0
		  GROUP BY shares.round_id, shares.state`,
		now.Unix(),
	)
	if err != nil {
		s.logError("ExpiredRoundSummaries: query failed", "error", err)
		return nil, err
	}
	defer rows.Close()

	byRound := make(map[string]*ExpiredRoundSummary)
	var order []string
	for rows.Next() {
		var roundID string
		var state, count int
		if err := rows.Scan(&roundID, &state, &count); err != nil {
			s.logError("ExpiredRoundSummaries: scan failed", "error", err)
			return nil, err
		}
		summary := byRound[roundID]
		if summary == nil {
			summary = &ExpiredRoundSummary{RoundID: roundID}
			byRound[roundID] = summary
			order = append(order, roundID)
		}
		summary.Total += count
		switch ShareState(state) {
		case ShareStateReceived, ShareStateWitnessed:
			summary.Pending += count
		case ShareStateSubmitted:
			summary.Submitted += count
		case ShareStateFailed:
			summary.Failed += count
		case ShareStateMissedDeadline:
			summary.MissedDeadline += count
		case ShareStateObservedOnChain:
			summary.ObservedOnChain += count
		}
	}
	if err := rows.Err(); err != nil {
		s.logError("ExpiredRoundSummaries: rows failed", "error", err)
		return nil, err
	}

	summaries := make([]ExpiredRoundSummary, 0, len(order))
	for _, roundID := range order {
		summaries = append(summaries, *byRound[roundID])
	}
	return summaries, nil
}

// RoundCloseoutSummary returns queue counts for one round after closeout
// classification has updated any processable rows.
func (s *ShareStore) RoundCloseoutSummary(roundID string) (ExpiredRoundSummary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	rows, err := s.db.Query(
		`SELECT state, COUNT(*)
		   FROM shares
		  WHERE round_id = ?
		  GROUP BY state`,
		roundID,
	)
	if err != nil {
		return ExpiredRoundSummary{}, fmt.Errorf("query round closeout summary: %w", err)
	}
	defer rows.Close()

	summary := ExpiredRoundSummary{RoundID: roundID}
	for rows.Next() {
		var state, count int
		if err := rows.Scan(&state, &count); err != nil {
			return ExpiredRoundSummary{}, fmt.Errorf("scan round closeout summary: %w", err)
		}
		summary.Total += count
		switch ShareState(state) {
		case ShareStateReceived, ShareStateWitnessed:
			summary.Pending += count
		case ShareStateSubmitted:
			summary.Submitted += count
		case ShareStateFailed:
			summary.Failed += count
		case ShareStateMissedDeadline:
			summary.MissedDeadline += count
		case ShareStateObservedOnChain:
			summary.ObservedOnChain += count
		}
	}
	if err := rows.Err(); err != nil {
		return ExpiredRoundSummary{}, fmt.Errorf("iterate round closeout summary: %w", err)
	}
	return summary, nil
}

// ExpiredRoundIDsForCloseout returns round IDs whose voting window has ended
// and whose helper rows have not been terminally closed out yet.
func (s *ShareStore) ExpiredRoundIDsForCloseout(now time.Time) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	rows, err := s.db.Query(
		`SELECT round_id
		   FROM rounds
		  WHERE vote_end_time > 0
		    AND vote_end_time < ?
		    AND closed_at = 0
		 UNION
		 SELECT shares.round_id
		   FROM shares
		   LEFT JOIN rounds ON rounds.round_id = shares.round_id
		  WHERE shares.vote_end_time > 0
		    AND shares.vote_end_time < ?
		    AND COALESCE(rounds.closed_at, 0) = 0
		  ORDER BY round_id`,
		now.Unix(),
		now.Unix(),
	)
	if err != nil {
		s.logError("ExpiredRoundIDsForCloseout: query failed", "error", err)
		return nil, err
	}
	defer rows.Close()

	var roundIDs []string
	for rows.Next() {
		var roundID string
		if err := rows.Scan(&roundID); err != nil {
			s.logError("ExpiredRoundIDsForCloseout: scan failed", "error", err)
			return nil, err
		}
		roundIDs = append(roundIDs, roundID)
	}
	if err := rows.Err(); err != nil {
		s.logError("ExpiredRoundIDsForCloseout: rows failed", "error", err)
		return nil, err
	}
	return roundIDs, nil
}

// Close closes the database connection.
func (s *ShareStore) Close() error {
	return errors.Join(s.db.Close(), releaseShareStoreLock(s.lockFile))
}

// ProcessableSharesForRound returns rows that closeout can classify for a round.
func (s *ShareStore) ProcessableSharesForRound(roundID string) ([]QueuedShare, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	rows, err := s.db.Query(
		`SELECT share_index, shares_hash, proposal_id, vote_decision,
		        tree_position, primary_blind, state, attempts, vote_end_time,
		        submit_at, share_nullifier
			   FROM shares
			  WHERE round_id = ?
			    AND closed_at = 0
			    AND (
			      state IN (?, ?)
			      OR (state = ? AND share_nullifier != '')
		    )
		  ORDER BY submit_at, received_at, proposal_id, vote_decision, share_index, tree_position`,
		roundID,
		ShareStateReceived,
		ShareStateWitnessed,
		ShareStateFailed,
	)
	if err != nil {
		return nil, fmt.Errorf("query processable shares: %w", err)
	}
	defer rows.Close()

	var shares []QueuedShare
	for rows.Next() {
		var q QueuedShare
		var state, attempts int
		var shareNullifierHex string
		if err := rows.Scan(
			&q.Payload.EncShare.ShareIndex,
			&q.Payload.SharesHash,
			&q.Payload.ProposalID,
			&q.Payload.VoteDecision,
			&q.Payload.TreePosition,
			&q.Payload.PrimaryBlind,
			&state,
			&attempts,
			&q.VoteEndTime,
			&q.Payload.SubmitAt,
			&shareNullifierHex,
		); err != nil {
			return nil, fmt.Errorf("scan processable share: %w", err)
		}
		if shareNullifierHex != "" {
			shareNullifier, err := hex.DecodeString(shareNullifierHex)
			if err != nil {
				return nil, fmt.Errorf("decode stored share nullifier for share_index %d proposal_id %d tree_position %d: %w", q.Payload.EncShare.ShareIndex, q.Payload.ProposalID, q.Payload.TreePosition, err)
			}
			q.ShareNullifier = shareNullifier
		}
		q.Payload.VoteRoundID = roundID
		q.State = ShareState(state)
		q.Attempts = attempts
		shares = append(shares, q)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate processable shares: %w", err)
	}
	return shares, nil
}

// MarkFailedCloseoutChecked records that a failed row with a known share
// nullifier was checked during round closeout and was still absent on-chain.
func (s *ShareStore) MarkFailedCloseoutChecked(roundID string, shareIndex, proposalID uint32, treePosition uint64, closedAt uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, err := s.db.Exec(
		`UPDATE shares
		    SET closed_at = ?
		  WHERE round_id = ?
		    AND share_index = ?
		    AND proposal_id = ?
		    AND tree_position = ?
		    AND state = ?
		    AND closed_at = 0`,
		closedAt,
		roundID,
		shareIndex,
		proposalID,
		treePosition,
		ShareStateFailed,
	); err != nil {
		return fmt.Errorf("mark failed closeout checked: %w", err)
	}
	return nil
}

// MarkObservedOnChain records that a processable row was already accepted on-chain.
func (s *ShareStore) MarkObservedOnChain(roundID string, shareIndex, proposalID uint32, treePosition uint64, shareNullifier []byte) error {
	return s.markTerminalObserved(roundID, shareIndex, proposalID, treePosition, ShareStateObservedOnChain, shareNullifier, 0)
}

// CloseoutShare makes a processable row terminal at round close and clears
// witness material while preserving timing and coarse audit state.
func (s *ShareStore) CloseoutShare(roundID string, shareIndex, proposalID uint32, treePosition uint64, state ShareState, shareNullifier []byte, closedAt uint64) error {
	if state != ShareStateMissedDeadline && state != ShareStateObservedOnChain {
		return fmt.Errorf("invalid closeout state %d", state)
	}
	return s.markTerminalObserved(roundID, shareIndex, proposalID, treePosition, state, shareNullifier, closedAt)
}

func (s *ShareStore) markTerminalObserved(roundID string, shareIndex, proposalID uint32, treePosition uint64, state ShareState, shareNullifier []byte, closedAt uint64) error {
	shareNullifierHex := ""
	if len(shareNullifier) > 0 {
		shareNullifierHex = hex.EncodeToString(shareNullifier)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	stateFilter := "AND state IN (?, ?)"
	args := []any{
		state,
		shareNullifierHex,
		closedAt,
		roundID,
		shareIndex,
		proposalID,
		treePosition,
		ShareStateReceived,
		ShareStateWitnessed,
	}
	if state == ShareStateObservedOnChain {
		stateFilter = "AND state IN (?, ?, ?)"
		args = append(args, ShareStateFailed)
	}

	_, err := s.db.Exec(
		fmt.Sprintf(`UPDATE shares
			    SET state = ?,
			        share_nullifier = ?,
			        closed_at = ?,
		        enc_share_c1 = '',
		        enc_share_c2 = '',
		        share_comms = '[]',
		        primary_blind = ''
			  WHERE round_id = ?
			    AND share_index = ?
			    AND proposal_id = ?
			    AND tree_position = ?
			    %s`, stateFilter),
		args...,
	)
	if err != nil {
		return fmt.Errorf("close out share: %w", err)
	}

	key := schedKey(roundID, shareIndex, proposalID, treePosition)
	if _, ok := s.schedule[key]; ok {
		delete(s.schedule, key)
		s.notifyScheduleChangedLocked()
	}
	return nil
}

// MarkRoundClosed records that closeout has completed for a round.
func (s *ShareStore) MarkRoundClosed(roundID string, closedAt uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, err := s.db.Exec(
		`INSERT INTO rounds (round_id, vote_end_time, created_at_time, closed_at)
		 VALUES (?, 0, 0, ?)
		 ON CONFLICT(round_id) DO UPDATE SET
		   closed_at = CASE
		     WHEN rounds.closed_at = 0 THEN excluded.closed_at
		     ELSE rounds.closed_at
		   END`,
		roundID,
		closedAt,
	); err != nil {
		return fmt.Errorf("mark round closed: %w", err)
	}

	schedulePruned := false
	for key := range s.schedule {
		parts := strings.SplitN(key, ":", 4)
		if len(parts) > 0 && parts[0] == roundID {
			delete(s.schedule, key)
			schedulePruned = true
		}
	}
	if schedulePruned {
		s.notifyScheduleChangedLocked()
	}
	return nil
}

// recover resets in-flight shares and restores their submit_at schedule.
func (s *ShareStore) recover() error {
	// Reset Witnessed (1) → Received (0).
	if _, err := s.db.Exec("UPDATE shares SET state = 0 WHERE state = 1"); err != nil {
		return fmt.Errorf("reset witnessed shares: %w", err)
	}

	// Repopulate round cache from rounds table.
	roundRows, err := s.db.Query("SELECT round_id, vote_end_time, created_at_time FROM rounds")
	if err != nil {
		return fmt.Errorf("query rounds cache: %w", err)
	}
	defer roundRows.Close()
	for roundRows.Next() {
		var roundID string
		var info RoundInfo
		if err := roundRows.Scan(&roundID, &info.VoteEndTime, &info.CreatedAtTime); err != nil {
			continue
		}
		s.roundCache[roundID] = info
	}

	// Load all non-terminal shares with their submit_at times.
	rows, err := s.db.Query("SELECT round_id, share_index, proposal_id, tree_position, submit_at FROM shares WHERE state = 0")
	if err != nil {
		return fmt.Errorf("query recoverable shares: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var roundID string
		var shareIndex, proposalID uint32
		var treePosition, submitAt uint64
		if err := rows.Scan(&roundID, &shareIndex, &proposalID, &treePosition, &submitAt); err != nil {
			continue
		}
		var schedTime time.Time
		if submitAt == 0 {
			schedTime = time.Now()
		} else {
			schedTime = time.Unix(int64(submitAt), 0)
		}
		s.schedule[schedKey(roundID, shareIndex, proposalID, treePosition)] = schedTime
	}
	return nil
}

func (s *ShareStore) loadShare(roundID string, shareIndex, proposalID uint32, treePosition uint64) (QueuedShare, bool) {
	var q QueuedShare
	var commsJSON string
	var state, attempts int

	err := s.db.QueryRow(
		`SELECT shares_hash, proposal_id, vote_decision, enc_share_c1, enc_share_c2,
		        tree_position, share_comms, primary_blind, state, attempts, vote_end_time, submit_at
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
		&q.Payload.SubmitAt,
	)
	if err != nil {
		return q, false
	}

	q.Payload.VoteRoundID = roundID
	q.Payload.EncShare.ShareIndex = shareIndex
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

// getRoundInfo returns cached round metadata, fetching from SQLite or the
// keeper if not in memory. Returns an error if the round is unknown.
func (s *ShareStore) getRoundInfo(roundID string) (RoundInfo, error) {
	s.mu.Lock()
	if info, ok := s.roundCache[roundID]; ok {
		if info.CreatedAtTime != 0 || s.fetchRoundInfo == nil {
			s.mu.Unlock()
			return info, nil
		}
		// Older helper DBs may have recovered a cache entry that only had
		// vote_end_time. Refresh from the keeper so queue summaries can use the
		// full round window.
	}

	// Check SQLite rounds table.
	var info RoundInfo
	err := s.db.QueryRow(
		"SELECT vote_end_time, created_at_time FROM rounds WHERE round_id = ?",
		roundID,
	).Scan(&info.VoteEndTime, &info.CreatedAtTime)
	if err == nil {
		if info.CreatedAtTime != 0 || s.fetchRoundInfo == nil {
			s.roundCache[roundID] = info
			s.mu.Unlock()
			return info, nil
		}
		// Older helper DBs may have only vote_end_time cached. Refresh from the
		// keeper so the public summary can cover the full round window.
	}
	s.mu.Unlock()

	// Fetch from keeper (outside lock — direct KV read).
	if s.fetchRoundInfo == nil {
		return RoundInfo{}, fmt.Errorf("%w: no round fetcher configured", ErrUnknownRound)
	}
	info, err = s.fetchRoundInfo(roundID)
	if err != nil {
		return RoundInfo{}, err
	}

	// Cache in both memory and SQLite.
	s.mu.Lock()
	s.roundCache[roundID] = info
	s.mu.Unlock()

	_, _ = s.db.Exec(
		`INSERT INTO rounds (round_id, vote_end_time, created_at_time)
		 VALUES (?, ?, ?)
		 ON CONFLICT(round_id) DO UPDATE SET
		   vote_end_time = excluded.vote_end_time,
		   created_at_time = excluded.created_at_time`,
		roundID,
		info.VoteEndTime,
		info.CreatedAtTime,
	)

	return info, nil
}

// getRoundEndTime is kept for call sites that only need scheduling validation.
func (s *ShareStore) getRoundEndTime(roundID string) (uint64, error) {
	info, err := s.getRoundInfo(roundID)
	if err != nil {
		return 0, err
	}
	return info.VoteEndTime, nil
}
