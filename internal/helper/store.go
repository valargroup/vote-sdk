package helper

import (
	"database/sql"
	"encoding/base64"
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

// ErrInvalidSubmitAt is returned when submit_at is at or after vote end time.
var ErrInvalidSubmitAt = errors.New("invalid submit_at")

// ErrInvalidRoundInfo is returned when cached round metadata cannot produce a
// valid queue summary.
var ErrInvalidRoundInfo = errors.New("invalid voting round metadata")

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
			pending_reveal_json TEXT NOT NULL DEFAULT '',
			pending_tx_hash TEXT NOT NULL DEFAULT '',
			pending_since_height INTEGER NOT NULL DEFAULT 0,
			pending_rebroadcast_count INTEGER NOT NULL DEFAULT 0,
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

	pendingColumns := []struct {
		name       string
		definition string
	}{
		{name: "pending_reveal_json", definition: "TEXT NOT NULL DEFAULT ''"},
		{name: "pending_tx_hash", definition: "TEXT NOT NULL DEFAULT ''"},
		{name: "pending_since_height", definition: "INTEGER NOT NULL DEFAULT 0"},
		{name: "pending_rebroadcast_count", definition: "INTEGER NOT NULL DEFAULT 0"},
	}
	for _, column := range pendingColumns {
		hasColumn, err := tableHasColumn(db, "shares", column.name)
		if err != nil {
			return fmt.Errorf("check shares.%s: %w", column.name, err)
		}
		if hasColumn {
			continue
		}
		if _, err := db.Exec(fmt.Sprintf("ALTER TABLE shares ADD COLUMN %s %s", column.name, column.definition)); err != nil {
			return fmt.Errorf("add shares.%s: %w", column.name, err)
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

	// A scheduled reveal must run before the round stops accepting votes.
	if payload.SubmitAt != 0 && payload.SubmitAt >= roundInfo.VoteEndTime {
		return EnqueueConflict, fmt.Errorf("%w: submit_at (%d) >= vote_end_time (%d)", ErrInvalidSubmitAt, payload.SubmitAt, roundInfo.VoteEndTime)
	}
	receivedAt := uint64(time.Now().Unix())
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
		        share_comms = '[]', primary_blind = '',
		        pending_reveal_json = '', pending_tx_hash = '',
		        pending_since_height = 0, pending_rebroadcast_count = 0
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

// stagePendingReveal persists the exact generated reveal before its first
// outbound attempt. The row stays in-flight so the caller can submit it, while
// crash recovery and retry paths retain the same proof.
func (s *ShareStore) stagePendingReveal(
	roundID string,
	shareIndex, proposalID uint32,
	treePosition uint64,
	reveal MsgRevealShareJSON,
) error {
	revealJSON, err := json.Marshal(&reveal)
	if err != nil {
		return fmt.Errorf("marshal staged reveal: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	res, err := s.db.Exec(
		`UPDATE shares SET pending_reveal_json = ?,
		        pending_tx_hash = '', pending_since_height = 0,
		        pending_rebroadcast_count = 0
		 WHERE round_id = ? AND share_index = ? AND proposal_id = ? AND tree_position = ? AND state = 1`,
		string(revealJSON), roundID, shareIndex, proposalID, treePosition,
	)
	if err != nil {
		return fmt.Errorf("persist staged reveal: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("inspect staged reveal update: %w", err)
	}
	if affected != 1 {
		return fmt.Errorf("persist staged reveal: updated %d rows", affected)
	}
	return nil
}

// markAwaitingCommit persists a CheckTx-accepted reveal and returns the
// in-flight row to the pending queue without spending a failed attempt.
func (s *ShareStore) markAwaitingCommit(
	roundID string,
	shareIndex, proposalID uint32,
	treePosition uint64,
	pending pendingRevealBroadcast,
) error {
	revealJSON, err := json.Marshal(&pending.Reveal)
	if err != nil {
		return fmt.Errorf("marshal pending reveal: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	var voteEndTime uint64
	if err := s.db.QueryRow(
		"SELECT vote_end_time FROM shares WHERE round_id = ? AND share_index = ? AND proposal_id = ? AND tree_position = ? AND state = 1",
		roundID, shareIndex, proposalID, treePosition,
	).Scan(&voteEndTime); err != nil {
		return fmt.Errorf("load pending reveal row: %w", err)
	}

	res, err := s.db.Exec(
		`UPDATE shares SET state = 0,
		        pending_reveal_json = ?, pending_tx_hash = ?,
		        pending_since_height = ?, pending_rebroadcast_count = ?
		 WHERE round_id = ? AND share_index = ? AND proposal_id = ? AND tree_position = ? AND state = 1`,
		string(revealJSON), pending.TxHash, pending.SinceHeight, pending.RebroadcastCount,
		roundID, shareIndex, proposalID, treePosition,
	)
	if err != nil {
		return fmt.Errorf("persist pending reveal: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("inspect pending reveal update: %w", err)
	}
	if affected != 1 {
		return fmt.Errorf("persist pending reveal: updated %d rows", affected)
	}

	key := schedKey(roundID, shareIndex, proposalID, treePosition)
	s.schedule[key] = nextShareSystemRetryTime(time.Now(), voteEndTime)
	s.notifyScheduleChangedLocked()
	return nil
}

// markPendingRebroadcast advances the committed-height backoff before sending
// the exact reveal again. Persisting first prevents a crash or ambiguous HTTP
// result from immediately repeating the rescue broadcast after restart. A
// response that proves the local handler did not broadcast may restore it.
func (s *ShareStore) markPendingRebroadcast(
	roundID string,
	shareIndex, proposalID uint32,
	treePosition uint64,
	pending pendingRevealBroadcast,
) error {
	if pending.SinceHeight == 0 || pending.RebroadcastCount == 0 {
		return fmt.Errorf("pending rebroadcast requires a height and positive count")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	res, err := s.db.Exec(
		`UPDATE shares SET pending_since_height = ?, pending_rebroadcast_count = ?
		 WHERE round_id = ? AND share_index = ? AND proposal_id = ? AND tree_position = ?
		   AND state = 1 AND pending_reveal_json != ''`,
		pending.SinceHeight, pending.RebroadcastCount,
		roundID, shareIndex, proposalID, treePosition,
	)
	if err != nil {
		return fmt.Errorf("persist pending rebroadcast: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("inspect pending rebroadcast update: %w", err)
	}
	if affected != 1 {
		return fmt.Errorf("persist pending rebroadcast: updated %d rows", affected)
	}
	return nil
}

// restorePendingRebroadcast restores the prior backoff window after a response
// proves the local handler did not attempt a chain broadcast. The attempted
// values make the update compare-and-swap so an unexpected state is not lost.
func (s *ShareStore) restorePendingRebroadcast(
	roundID string,
	shareIndex, proposalID uint32,
	treePosition uint64,
	previous, attempted pendingRevealBroadcast,
) error {
	if previous.SinceHeight == 0 || attempted.SinceHeight == 0 || attempted.RebroadcastCount == 0 {
		return fmt.Errorf("pending rebroadcast restore requires accepted windows")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	res, err := s.db.Exec(
		`UPDATE shares SET pending_since_height = ?, pending_rebroadcast_count = ?
		 WHERE round_id = ? AND share_index = ? AND proposal_id = ? AND tree_position = ?
		   AND state = 1 AND pending_reveal_json != ''
		   AND pending_since_height = ? AND pending_rebroadcast_count = ?`,
		previous.SinceHeight, previous.RebroadcastCount,
		roundID, shareIndex, proposalID, treePosition,
		attempted.SinceHeight, attempted.RebroadcastCount,
	)
	if err != nil {
		return fmt.Errorf("restore pending rebroadcast: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("inspect pending rebroadcast restore: %w", err)
	}
	if affected != 1 {
		return fmt.Errorf("restore pending rebroadcast: updated %d rows", affected)
	}
	return nil
}

const (
	shareSystemRetryBackoff        = 10 * time.Second
	shareSystemRetryUrgentBackoff  = 2 * time.Second
	shareSystemRetryDeadlineBuffer = 30 * time.Second
	shareStalledRetryMaxBackoff    = 2 * time.Minute
	shareStalledRetryMaxCount      = 5
)

// MarkRetry returns an in-flight share to the pending queue without spending a
// failed-share attempt.
func (s *ShareStore) MarkRetry(roundID string, shareIndex, proposalID uint32, treePosition uint64) {
	s.markRetry(roundID, shareIndex, proposalID, treePosition, 0)
}

// MarkStalledRetry returns an in-flight share to the pending queue with a
// bounded backoff for repeated retries at the same committed height.
func (s *ShareStore) MarkStalledRetry(roundID string, shareIndex, proposalID uint32, treePosition uint64, retryCount uint8) {
	s.markRetry(roundID, shareIndex, proposalID, treePosition, retryCount)
}

func (s *ShareStore) markRetry(roundID string, shareIndex, proposalID uint32, treePosition uint64, stalledRetryCount uint8) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var voteEndTime uint64
	if err := s.db.QueryRow(
		"SELECT vote_end_time FROM shares WHERE round_id = ? AND share_index = ? AND proposal_id = ? AND tree_position = ? AND state = 1",
		roundID, shareIndex, proposalID, treePosition,
	).Scan(&voteEndTime); err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			s.logError("MarkRetry: db query failed", "round_id", roundID, "share_index", shareIndex, "proposal_id", proposalID, "tree_position", treePosition, "error", err)
		}
		return
	}

	res, err := s.db.Exec(
		"UPDATE shares SET state = 0 WHERE round_id = ? AND share_index = ? AND proposal_id = ? AND tree_position = ? AND state = 1",
		roundID, shareIndex, proposalID, treePosition,
	)
	if err != nil {
		s.logError("MarkRetry: db update failed", "round_id", roundID, "share_index", shareIndex, "proposal_id", proposalID, "tree_position", treePosition, "error", err)
		return
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return
	}

	key := schedKey(roundID, shareIndex, proposalID, treePosition)
	now := time.Now()
	if stalledRetryCount == 0 {
		s.schedule[key] = nextShareSystemRetryTime(now, voteEndTime)
	} else {
		s.schedule[key] = nextShareStalledRetryTime(now, voteEndTime, stalledRetryCount)
	}
	s.notifyScheduleChangedLocked()
}

// nextShareSystemRetryTime returns the next retry time without intentionally
// leaving too little processing time before the round's vote end time.
func nextShareSystemRetryTime(now time.Time, voteEndTime uint64) time.Time {
	scheduled := now.Add(shareSystemRetryBackoff)
	if voteEndTime == 0 {
		return scheduled
	}
	deadline := time.Unix(int64(voteEndTime), 0)
	if scheduled.After(deadline.Add(-shareSystemRetryDeadlineBuffer)) {
		remaining := deadline.Sub(now)
		if remaining <= 0 {
			return now
		}
		urgentBackoff := shareSystemRetryUrgentBackoff
		if halfRemaining := remaining / 2; halfRemaining < urgentBackoff {
			urgentBackoff = halfRemaining
		}
		return now.Add(urgentBackoff)
	}
	return scheduled
}

// nextShareStalledRetryTime backs off repeated checks at one committed height
// while ensuring the share wakes at the start of the urgent deadline window.
func nextShareStalledRetryTime(now time.Time, voteEndTime uint64, retryCount uint8) time.Time {
	backoff := shareSystemRetryBackoff
	for retry := uint8(1); retry < retryCount && backoff < shareStalledRetryMaxBackoff; retry++ {
		backoff *= 2
	}
	if backoff > shareStalledRetryMaxBackoff {
		backoff = shareStalledRetryMaxBackoff
	}

	scheduled := now.Add(backoff)
	if voteEndTime == 0 {
		return scheduled
	}

	deadline := time.Unix(int64(voteEndTime), 0)
	remaining := deadline.Sub(now)
	if remaining <= 0 {
		return now
	}
	urgentStart := deadline.Add(-shareSystemRetryDeadlineBuffer)
	if now.Before(urgentStart) {
		if scheduled.After(urgentStart) {
			return urgentStart
		}
		return scheduled
	}

	urgentBackoff := shareSystemRetryUrgentBackoff
	if halfRemaining := remaining / 2; halfRemaining < urgentBackoff {
		urgentBackoff = halfRemaining
	}
	return now.Add(urgentBackoff)
}

// MarkFailed marks a share processing attempt as failed, with retry or
// permanent failure after max attempts.
func (s *ShareStore) MarkFailed(roundID string, shareIndex, proposalID uint32, treePosition uint64) {
	const maxAttempts = 5

	s.mu.Lock()
	defer s.mu.Unlock()

	var attempts int
	var voteEndTime uint64
	if err := s.db.QueryRow(
		"SELECT attempts, vote_end_time FROM shares WHERE round_id = ? AND share_index = ? AND proposal_id = ? AND tree_position = ?",
		roundID, shareIndex, proposalID, treePosition,
	).Scan(&attempts, &voteEndTime); err != nil {
		s.logError("MarkFailed: db query failed", "round_id", roundID, "share_index", shareIndex, "proposal_id", proposalID, "tree_position", treePosition, "error", err)
		return
	}

	newAttempts := attempts + 1
	key := schedKey(roundID, shareIndex, proposalID, treePosition)

	if newAttempts >= maxAttempts {
		if voteEndTime == 0 {
			// Legacy or imported rows without a purge time cannot safely retain
			// witness data because no end-of-round cleanup can be scheduled.
			if _, err := s.db.Exec(
				`UPDATE shares SET state = 3, attempts = ?,
				        enc_share_c1 = '', enc_share_c2 = '',
				        share_comms = '[]', primary_blind = '',
				        pending_reveal_json = '', pending_tx_hash = '',
				        pending_since_height = 0, pending_rebroadcast_count = 0
				 WHERE round_id = ? AND share_index = ? AND proposal_id = ? AND tree_position = ?`,
				newAttempts, roundID, shareIndex, proposalID, treePosition,
			); err != nil {
				s.logError("MarkFailed: db update (permanent scrub) failed", "error", err)
			} else {
				s.truncateWALAfterWitnessCleanup("MarkFailed: permanent scrub")
			}
		} else {
			// Permanently failed. Keep witness data until round purge so operators
			// can inspect or export failed rows before the voting window closes.
			if _, err := s.db.Exec(
				`UPDATE shares SET state = 3, attempts = ?,
				        pending_reveal_json = '', pending_tx_hash = '',
				        pending_since_height = 0, pending_rebroadcast_count = 0
				 WHERE round_id = ? AND share_index = ? AND proposal_id = ? AND tree_position = ?`,
				newAttempts, roundID, shareIndex, proposalID, treePosition,
			); err != nil {
				s.logError("MarkFailed: db update (permanent) failed", "error", err)
			}
		}
		if _, ok := s.schedule[key]; ok {
			delete(s.schedule, key)
			s.notifyScheduleChangedLocked()
		}
	} else {
		// Re-schedule with exponential backoff.
		if _, err := s.db.Exec(
			`UPDATE shares SET state = 0, attempts = ?,
			        pending_reveal_json = '', pending_tx_hash = '',
			        pending_since_height = 0, pending_rebroadcast_count = 0
			 WHERE round_id = ? AND share_index = ? AND proposal_id = ? AND tree_position = ?`,
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
		}
		result[roundID] = entry
	}

	return result
}

// isProcessableShareState reports whether a queue row can still be submitted.
func isProcessableShareState(state ShareState) bool {
	return state == ShareStateReceived || state == ShareStateWitnessed
}

func decodePendingRevealBroadcast(
	revealJSON, txHash string,
	sinceHeight uint64,
	rebroadcastCount uint32,
) (*pendingRevealBroadcast, error) {
	if revealJSON == "" {
		return nil, nil
	}
	var reveal MsgRevealShareJSON
	if err := json.Unmarshal([]byte(revealJSON), &reveal); err != nil {
		return nil, err
	}
	return &pendingRevealBroadcast{
		Reveal:           reveal,
		TxHash:           txHash,
		SinceHeight:      sinceHeight,
		RebroadcastCount: rebroadcastCount,
	}, nil
}

func encodePendingRevealBroadcast(pending *pendingRevealBroadcast) (string, string, uint64, uint32, error) {
	if pending == nil {
		return "", "", 0, 0, nil
	}
	revealJSON, err := json.Marshal(&pending.Reveal)
	if err != nil {
		return "", "", 0, 0, err
	}
	return string(revealJSON), pending.TxHash, pending.SinceHeight, pending.RebroadcastCount, nil
}

func queueExportPendingBroadcast(pending *pendingRevealBroadcast) *QueueExportPendingBroadcast {
	if pending == nil {
		return nil
	}
	return &QueueExportPendingBroadcast{
		Reveal:           pending.Reveal,
		TxHash:           pending.TxHash,
		SinceHeight:      pending.SinceHeight,
		RebroadcastCount: pending.RebroadcastCount,
	}
}

func pendingRevealBroadcastFromExport(pending *QueueExportPendingBroadcast) *pendingRevealBroadcast {
	if pending == nil {
		return nil
	}
	return &pendingRevealBroadcast{
		Reveal:           pending.Reveal,
		TxHash:           pending.TxHash,
		SinceHeight:      pending.SinceHeight,
		RebroadcastCount: pending.RebroadcastCount,
	}
}

// validateImportedPendingReveal ensures a preserved reveal belongs to its
// enclosing queue row before the import can bypass proof generation.
func validateImportedPendingReveal(roundID string, row QueueExportRow, opts QueueImportOptions) error {
	if row.PendingBroadcast == nil {
		return nil
	}

	roundBytes, err := hex.DecodeString(roundID)
	if err != nil {
		return fmt.Errorf("decode round_id: %w", err)
	}
	if len(roundBytes) != 32 {
		return fmt.Errorf("round_id must be 32 bytes, got %d", len(roundBytes))
	}

	reveal := row.PendingBroadcast.Reveal
	if reveal.VoteRoundID != base64.StdEncoding.EncodeToString(roundBytes) {
		return errors.New("pending reveal vote_round_id does not match imported round_id")
	}
	if reveal.ProposalID != row.ProposalID {
		return errors.New("pending reveal proposal_id does not match imported row")
	}
	if reveal.VoteDecision != row.VoteDecision {
		return errors.New("pending reveal vote_decision does not match imported row")
	}

	c1, err := decodeBase64Array32(row.EncShare.C1, "imported enc_share.c1")
	if err != nil {
		return err
	}
	c2, err := decodeBase64Array32(row.EncShare.C2, "imported enc_share.c2")
	if err != nil {
		return err
	}
	encShare := make([]byte, 0, len(c1)+len(c2))
	encShare = append(encShare, c1[:]...)
	encShare = append(encShare, c2[:]...)
	if reveal.EncShare != base64.StdEncoding.EncodeToString(encShare) {
		return errors.New("pending reveal enc_share does not match imported row")
	}
	if opts.VCHash == nil || opts.ShareNullifierHash == nil {
		return errors.New("pending reveal nullifier validation unavailable")
	}

	var roundIDField [32]byte
	copy(roundIDField[:], roundBytes)
	sharesHash, err := decodeBase64Array32(row.SharesHash, "imported shares_hash")
	if err != nil {
		return err
	}
	primaryBlind, err := decodeBase64Array32(row.PrimaryBlind, "imported primary_blind")
	if err != nil {
		return err
	}
	voteCommitment, err := opts.VCHash(roundIDField, sharesHash, row.ProposalID, row.VoteDecision)
	if err != nil {
		return fmt.Errorf("compute imported vote commitment: %w", err)
	}
	expectedNullifier, err := opts.ShareNullifierHash(voteCommitment, row.ShareIndex, primaryBlind)
	if err != nil {
		return fmt.Errorf("compute imported share nullifier: %w", err)
	}
	importedNullifier, err := decodeBase64Array32(reveal.ShareNullifier, "pending reveal share_nullifier")
	if err != nil {
		return err
	}
	if importedNullifier != expectedNullifier {
		return errors.New("pending reveal share_nullifier does not match imported row")
	}

	return nil
}

// pendingRevealImportMatchesExisting reports whether an imported pending
// broadcast is compatible with the existing row. The local lifecycle may have
// advanced since an older rescue artifact was created, but an imported reveal
// must not be silently discarded or replaced with a different reveal.
func pendingRevealImportMatchesExisting(existing, incoming *pendingRevealBroadcast) bool {
	if incoming == nil {
		return true
	}
	if existing == nil {
		return false
	}
	return existing.Reveal == incoming.Reveal
}

// ExportQueue returns every persisted row for a round. Terminal rows are
// included for local debugging. Submitted rows should already have witness
// material cleared, while failed rows retain it until round purge.
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
		        original_submit_at, received_at, pending_reveal_json,
		        pending_tx_hash, pending_since_height, pending_rebroadcast_count
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
		var commsJSON, pendingRevealJSON, pendingTxHash string
		var pendingSinceHeight uint64
		var pendingRebroadcastCount uint32
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
			&pendingRevealJSON,
			&pendingTxHash,
			&pendingSinceHeight,
			&pendingRebroadcastCount,
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
		pending, err := decodePendingRevealBroadcast(
			pendingRevealJSON,
			pendingTxHash,
			pendingSinceHeight,
			pendingRebroadcastCount,
		)
		if err != nil {
			return QueueExport{}, fmt.Errorf("decode pending reveal for share_index %d: %w", row.ShareIndex, err)
		}
		row.PendingBroadcast = queueExportPendingBroadcast(pending)
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
	if export.Version != queueExportLegacyVersion && export.Version != QueueExportVersion {
		return QueueImportResult{}, fmt.Errorf("unsupported queue export version %d", export.Version)
	}
	if strings.TrimSpace(export.RoundID) == "" {
		return QueueImportResult{}, errors.New("queue export missing round_id")
	}

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
		if err := validateImportedPendingReveal(export.RoundID, row, opts); err != nil {
			return QueueImportResult{}, fmt.Errorf(
				"validate pending reveal for share_index %d proposal_id %d tree_position %d: %w",
				row.ShareIndex,
				row.ProposalID,
				row.TreePosition,
				err,
			)
		}

		submitAt := row.SubmitAt
		originalSubmitAt := row.OriginalSubmitAt
		if originalSubmitAt == 0 {
			originalSubmitAt = submitAt
		}
		if opts.ForceReady {
			submitAt = 0
		}
		// Preserve zero as the unknown receipt-time sentinel for migrated rows.
		receivedAt := row.ReceivedAt
		voteEndTime := row.VoteEndTime
		if voteEndTime == 0 {
			voteEndTime = export.Round.VoteEndTime
		}
		if !opts.ForceReady && submitAt != 0 && submitAt >= voteEndTime {
			return QueueImportResult{}, fmt.Errorf("%w: imported submit_at (%d) >= vote_end_time (%d) for share_index %d proposal_id %d tree_position %d", ErrInvalidSubmitAt, submitAt, voteEndTime, row.ShareIndex, row.ProposalID, row.TreePosition)
		}
		commsJSON, err := json.Marshal(row.ShareComms)
		if err != nil {
			return QueueImportResult{}, fmt.Errorf("marshal share_comms for share_index %d: %w", row.ShareIndex, err)
		}
		pendingRevealJSON, pendingTxHash, pendingSinceHeight, pendingRebroadcastCount, err := encodePendingRevealBroadcast(
			pendingRevealBroadcastFromExport(row.PendingBroadcast),
		)
		if err != nil {
			return QueueImportResult{}, fmt.Errorf("marshal pending reveal for share_index %d: %w", row.ShareIndex, err)
		}

		res, err := tx.Exec(
			`INSERT INTO shares
			 (round_id, share_index, shares_hash, proposal_id, vote_decision,
			  enc_share_c1, enc_share_c2, tree_position, share_comms, primary_blind,
			  state, attempts, vote_end_time, submit_at, original_submit_at, received_at,
			  pending_reveal_json, pending_tx_hash, pending_since_height, pending_rebroadcast_count)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?, ?, ?, ?, ?, ?, ?, ?)
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
			pendingRevealJSON,
			pendingTxHash,
			pendingSinceHeight,
			pendingRebroadcastCount,
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
	var commsJSON, pendingRevealJSON, pendingTxHash string
	var pendingSinceHeight uint64
	var pendingRebroadcastCount uint32
	var state int
	err := tx.QueryRow(
		`SELECT shares_hash, vote_decision, enc_share_c1, enc_share_c2,
		        share_comms, primary_blind, state, pending_reveal_json,
		        pending_tx_hash, pending_since_height, pending_rebroadcast_count
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
		&pendingRevealJSON,
		&pendingTxHash,
		&pendingSinceHeight,
		&pendingRebroadcastCount,
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
	existingPending, err := decodePendingRevealBroadcast(
		pendingRevealJSON,
		pendingTxHash,
		pendingSinceHeight,
		pendingRebroadcastCount,
	)
	if err != nil {
		return false, 0, fmt.Errorf("decode existing pending reveal for share_index %d: %w", row.ShareIndex, err)
	}
	incomingPending := pendingRevealBroadcastFromExport(row.PendingBroadcast)
	return payloadEqual(existing, incoming) && pendingRevealImportMatchesExisting(existingPending, incomingPending), ShareState(state), nil
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
func (s *ShareStore) QueueSummary(roundID string, now time.Time) (QueueSummary, error) {
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
		}
		bucket.Total += count
	}
	if err := rows.Err(); err != nil {
		return QueueSummary{}, err
	}

	return summary, nil
}

// ExpiredRoundSummaries returns per-round queue counts for rounds whose voting
// window has ended. It excludes shares first received at or after the round's
// close time, since they were not pending before close and should not trigger
// this alert. Call this before PurgeExpiredRounds so unsubmitted share alerts
// can be emitted without retaining witness data.
func (s *ShareStore) ExpiredRoundSummaries(now time.Time) ([]ExpiredRoundSummary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	rows, err := s.db.Query(
		`SELECT round_id, state, COUNT(*)
		   FROM shares
		  WHERE vote_end_time > 0
		    AND vote_end_time < ?
		    AND (received_at = 0 OR received_at < vote_end_time)
		  GROUP BY round_id, state`,
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

// Close closes the database connection.
func (s *ShareStore) Close() error {
	return errors.Join(s.db.Close(), releaseShareStoreLock(s.lockFile))
}

// PurgeExpiredRounds deletes all share data for rounds whose vote_end_time
// has passed, checkpoints the WAL after deleting expired share rows, and
// removes the corresponding entries from the in-memory schedule and round
// cache. Returns the number of rows deleted.
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
	s.truncateWALAfterWitnessCleanup("PurgeExpiredRounds")

	// Prune in-memory caches for expired rounds.
	for roundID, info := range s.roundCache {
		if info.VoteEndTime > 0 && info.VoteEndTime < uint64(now) {
			delete(s.roundCache, roundID)
		}
	}
	schedulePruned := false
	for key := range s.schedule {
		parts := strings.SplitN(key, ":", 4)
		if len(parts) < 1 {
			continue
		}
		roundID := parts[0]
		if _, ok := s.roundCache[roundID]; !ok {
			delete(s.schedule, key)
			schedulePruned = true
		}
	}
	if schedulePruned {
		s.notifyScheduleChangedLocked()
	}

	if deleted > 0 {
		if s.logInfo != nil {
			s.logInfo("purged expired round data", "rows_deleted", deleted)
		}
	}
	return deleted
}

// truncateWALAfterWitnessCleanup checkpoints and truncates the SQLite WAL after
// witness cleanup attempts so removed material from this or an earlier cleanup
// does not remain in WAL frames until a later checkpoint.
func (s *ShareStore) truncateWALAfterWitnessCleanup(stage string) {
	var busy, logFrames, checkpointedFrames int
	if err := s.db.QueryRow("PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &logFrames, &checkpointedFrames); err != nil {
		s.logError(stage+": WAL checkpoint failed", "error", err)
		return
	}
	if busy != 0 {
		s.logError(
			stage+": WAL checkpoint busy",
			"busy", busy,
			"log_frames", logFrames,
			"checkpointed_frames", checkpointedFrames,
		)
	}
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
	var pendingRevealJSON, pendingTxHash string
	var pendingSinceHeight uint64
	var pendingRebroadcastCount uint32
	var state, attempts int

	err := s.db.QueryRow(
		`SELECT shares_hash, proposal_id, vote_decision, enc_share_c1, enc_share_c2,
		        tree_position, share_comms, primary_blind, state, attempts, vote_end_time, submit_at,
		        pending_reveal_json, pending_tx_hash, pending_since_height, pending_rebroadcast_count
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
		&pendingRevealJSON,
		&pendingTxHash,
		&pendingSinceHeight,
		&pendingRebroadcastCount,
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
	pending, err := decodePendingRevealBroadcast(
		pendingRevealJSON,
		pendingTxHash,
		pendingSinceHeight,
		pendingRebroadcastCount,
	)
	if err != nil {
		return q, false
	}
	q.pendingBroadcast = pending

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

	// Another request may have populated the round while this request fetched it.
	// Recheck under the store lock so only one request writes a cold round and so
	// the rounds upsert cannot race the serialized share writes.
	s.mu.Lock()
	defer s.mu.Unlock()
	if cached, ok := s.roundCache[roundID]; ok && (cached.CreatedAtTime != 0 || s.fetchRoundInfo == nil) {
		return cached, nil
	}

	s.roundCache[roundID] = info
	if _, err := s.db.Exec(
		`INSERT INTO rounds (round_id, vote_end_time, created_at_time)
		 VALUES (?, ?, ?)
		 ON CONFLICT(round_id) DO UPDATE SET
		   vote_end_time = excluded.vote_end_time,
		   created_at_time = excluded.created_at_time`,
		roundID,
		info.VoteEndTime,
		info.CreatedAtTime,
	); err != nil {
		s.logError("getRoundInfo: cache round failed", "round_id", roundID, "error", err)
	}

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
