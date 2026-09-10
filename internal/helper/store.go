package helper

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
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

// ShareStore is a SQLite-backed share queue with ephemeral scheduling and
// worker ownership. Payload data and terminal outcomes are persisted; effective
// submit_at timestamps control when each share is eligible for proof generation.
type ShareStore struct {
	metrics           *storeMetrics // immutable after construction; nil outside opted-in staging
	db                *sql.DB
	lockFile          *os.File
	mu                sync.Mutex
	schedule          map[string]time.Time // key: "round_id:share_index:proposal_id:tree_position"
	inFlight          map[string]inFlightShare
	nextAttemptID     uint64
	lastReadyRound    string    // process-local round-robin cursor; guarded by mu
	dequeueRetryUntil time.Time // common store-error backoff; guarded by mu
	schedulingSecret  [32]byte
	now               func() time.Time
	scheduleChanged   chan struct{}
	roundCache        map[string]RoundInfo             // roundID -> chain round metadata
	fetchRoundInfo    RoundInfoFetcher                 // queries the chain; may be nil in tests
	logger            func(msg string, keyvals ...any) // optional error logger
	logInfo           func(msg string, keyvals ...any) // optional info logger
	captureErr        func(err error, tags map[string]string)
}

// inFlightShare is the process-local ownership record for a dequeued share.
// SQLite remains in Received state until the worker records its outcome.
type inFlightShare struct {
	roundID     string
	voteEndTime uint64
	attemptID   uint64
	receivedAt  uint64
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
	var schedulingSecret [32]byte
	if _, err := rand.Read(schedulingSecret[:]); err != nil {
		return nil, fmt.Errorf("generate scheduling secret: %w", err)
	}

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
		db:               db,
		lockFile:         lockFile,
		schedule:         make(map[string]time.Time),
		inFlight:         make(map[string]inFlightShare),
		scheduleChanged:  make(chan struct{}, 1),
		roundCache:       make(map[string]RoundInfo),
		fetchRoundInfo:   fetcher,
		schedulingSecret: schedulingSecret,
		now:              time.Now,
	}

	if helperDiagnosticsEnabled() {
		s.metrics = stagingStoreMetrics()
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

	hasRetryState, err := tableHasColumn(db, "shares", "retry_state")
	if err != nil {
		return fmt.Errorf("check retry state schema: %w", err)
	}
	if !hasRetryState {
		if _, err := db.Exec("ALTER TABLE shares ADD COLUMN retry_state TEXT NOT NULL DEFAULT ''"); err != nil {
			return fmt.Errorf("add shares.retry_state: %w", err)
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

// Enqueue adds an admission-validated share payload, clamping a past nonzero
// submit_at to arrival. The production caller authenticates the request and
// validates its witness relationships and on-chain commitment before calling
// Enqueue; that validation authorizes repair of an undecodable stored row.
//
// Returns:
//   - EnqueueInserted when a new row was inserted or a corrupt row was repaired
//     and scheduled.
//   - EnqueueDuplicate when an identical payload already exists.
//   - EnqueueConflict when an entry exists for the same canonical schedule key
//     but with different payload content.
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
	arrival := s.now()
	arrivalSecond := time.Unix(arrival.Unix(), 0)
	receivedAt := uint64(arrival.Unix())
	originalSubmitAt := payload.SubmitAt
	effectiveSubmitAt := originalSubmitAt
	// Past timestamps already mean "ready now"; clamp them to arrival so they
	// cannot jump ahead of queued shares under oldest-first scheduling.
	if effectiveSubmitAt != 0 && effectiveSubmitAt < receivedAt && receivedAt < roundInfo.VoteEndTime {
		effectiveSubmitAt = receivedAt
	}
	unlock := s.lockMeasured("Enqueue")
	defer unlock()

	res, err := s.execWrite(
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
		effectiveSubmitAt,
		originalSubmitAt,
		receivedAt,
	)
	if err != nil {
		return EnqueueConflict, fmt.Errorf("insert share: %w", err)
	}

	// Only schedule if the row was actually inserted (not a duplicate).
	affected, _ := res.RowsAffected()
	if affected > 0 {
		schedTime := arrivalSecond
		if effectiveSubmitAt != 0 {
			schedTime = time.Unix(int64(effectiveSubmitAt), 0)
		}
		key := schedKey(payload.VoteRoundID, payload.EncShare.ShareIndex, payload.ProposalID, payload.TreePosition)
		s.schedule[key] = schedTime
		s.notifyScheduleChangedLocked()
		if s.logInfo != nil {
			s.logInfo("share scheduled",
				"round_id", payload.VoteRoundID,
				"share_index", payload.EncShare.ShareIndex,
				"proposal_id", payload.ProposalID,
				"submit_at", effectiveSubmitAt,
				"original_submit_at", originalSubmitAt,
			)
		}
		return EnqueueInserted, nil
	}

	// Conflict path: row already exists, classify as idempotent duplicate vs
	// conflict. A validated wallet retry may replace an undecodable row that is
	// neither submitted nor owned by a worker.
	existing, loadErr := loadShareFrom(s.db, payload.VoteRoundID, payload.EncShare.ShareIndex, payload.ProposalID, payload.TreePosition)
	if loadErr == nil && payloadEqual(existing.Payload, payload) {
		return EnqueueDuplicate, nil
	}
	if loadErr == nil {
		return EnqueueConflict, nil
	}
	if !errors.Is(loadErr, errCorruptShareRow) {
		return EnqueueConflict, fmt.Errorf(
			"load existing share after conflict: round_id=%s share_index=%d proposal_id=%d: %w",
			payload.VoteRoundID,
			payload.EncShare.ShareIndex,
			payload.ProposalID,
			loadErr,
		)
	}

	key := schedKey(payload.VoteRoundID, payload.EncShare.ShareIndex, payload.ProposalID, payload.TreePosition)
	if _, active := s.inFlight[key]; active {
		return EnqueueDuplicate, nil
	}

	var stateValue any
	if err := s.db.QueryRow(
		"SELECT state FROM shares WHERE round_id = ? AND share_index = ? AND proposal_id = ? AND tree_position = ?",
		payload.VoteRoundID, payload.EncShare.ShareIndex, payload.ProposalID, payload.TreePosition,
	).Scan(&stateValue); err != nil {
		return EnqueueConflict, fmt.Errorf("read corrupt share state before replacement: %w", err)
	}
	state, stateErr := decodeRowInt("state", stateValue)
	if stateErr != nil {
		s.logError("refusing corrupt share replacement with unreadable state",
			"round_id", payload.VoteRoundID,
			"share_index", payload.EncShare.ShareIndex,
			"proposal_id", payload.ProposalID,
			"tree_position", payload.TreePosition,
			"error", stateErr,
		)
		return EnqueueConflict, nil
	}
	if ShareState(state) == ShareStateSubmitted {
		return EnqueueDuplicate, nil
	}

	replaced, err := s.execWrite(
		`UPDATE shares SET
		   shares_hash = ?, vote_decision = ?, enc_share_c1 = ?, enc_share_c2 = ?,
		   share_comms = ?, primary_blind = ?, state = 0, attempts = 0,
		   vote_end_time = ?, submit_at = ?, original_submit_at = ?, received_at = ?
		 WHERE round_id = ? AND share_index = ? AND proposal_id = ? AND tree_position = ?`,
		payload.SharesHash,
		payload.VoteDecision,
		payload.EncShare.C1,
		payload.EncShare.C2,
		string(commsJSON),
		payload.PrimaryBlind,
		roundInfo.VoteEndTime,
		effectiveSubmitAt,
		originalSubmitAt,
		receivedAt,
		payload.VoteRoundID,
		payload.EncShare.ShareIndex,
		payload.ProposalID,
		payload.TreePosition,
	)
	if err != nil {
		return EnqueueConflict, fmt.Errorf("replace corrupt share: %w", err)
	}
	replacedCount, err := replaced.RowsAffected()
	if err != nil {
		return EnqueueConflict, fmt.Errorf("read corrupt share replacement result: %w", err)
	}
	if replacedCount != 1 {
		return EnqueueConflict, fmt.Errorf("replace corrupt share affected %d rows", replacedCount)
	}

	schedTime := arrivalSecond
	if effectiveSubmitAt != 0 {
		schedTime = time.Unix(int64(effectiveSubmitAt), 0)
	}
	s.schedule[key] = schedTime
	s.notifyScheduleChangedLocked()
	s.logError("replaced corrupt share from validated wallet retry",
		"round_id", payload.VoteRoundID,
		"share_index", payload.EncShare.ShareIndex,
		"proposal_id", payload.ProposalID,
		"tree_position", payload.TreePosition,
		"error", loadErr,
	)
	if s.logInfo != nil {
		s.logInfo("share scheduled",
			"round_id", payload.VoteRoundID,
			"share_index", payload.EncShare.ShareIndex,
			"proposal_id", payload.ProposalID,
			"submit_at", effectiveSubmitAt,
			"original_submit_at", originalSubmitAt,
		)
	}
	return EnqueueInserted, nil
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

// NextScheduledTime returns the earliest scheduled share time, delayed by a
// common backoff after a store-wide dequeue failure.
func (s *ShareStore) NextScheduledTime() (time.Time, bool) {
	unlock := s.lockMeasured("NextScheduledTime")
	defer unlock()
	defer s.measureScan("NextScheduledTime")()

	var next time.Time
	for _, scheduledAt := range s.schedule {
		if next.IsZero() || scheduledAt.Before(next) {
			next = scheduledAt
		}
	}
	if next.IsZero() {
		return time.Time{}, false
	}
	if next.Before(s.dequeueRetryUntil) {
		return s.dequeueRetryUntil, true
	}
	return next, true
}

// TakeReady returns all shares past their scheduled submission time that are
// not already owned by a worker in this process.
func (s *ShareStore) TakeReady() []QueuedShare {
	return s.takeReady(0)
}

// TakeReadyBatch returns at most limit ready shares, leaving the rest queued.
func (s *ShareStore) TakeReadyBatch(limit int) []QueuedShare {
	return s.takeReady(limit)
}

func (s *ShareStore) takeReady(limit int) []QueuedShare {
	now := s.now()

	unlock := s.lockMeasured("takeReady")
	defer unlock()
	defer s.measureScan("takeReady")()
	if now.Before(s.dequeueRetryUntil) {
		return nil
	}
	s.dequeueRetryUntil = time.Time{}

	type readyCandidate struct {
		key          string
		roundID      string
		shareIndex   uint32
		proposalID   uint32
		treePosition uint64
		scheduledAt  time.Time
		rank         [sha256.Size]byte
	}

	// Group ready shares by round. Malformed keys are internal corruption and
	// cannot be processed, so remove them from the ephemeral schedule.
	readyByRound := make(map[string][]readyCandidate)
	for key, scheduledAt := range s.schedule {
		// Worker ownership is authoritative. Drop any schedule entry that was
		// reintroduced for an active share so it cannot be dispatched twice or
		// keep the scheduler waiting on a duplicate.
		if _, active := s.inFlight[key]; active {
			delete(s.schedule, key)
			continue
		}
		if scheduledAt.After(now) {
			continue
		}
		parts := strings.SplitN(key, ":", 4)
		if len(parts) != 4 {
			delete(s.schedule, key)
			continue
		}
		idx64, err := strconv.ParseUint(parts[1], 10, 32)
		if err != nil {
			delete(s.schedule, key)
			continue
		}
		pid64, err := strconv.ParseUint(parts[2], 10, 32)
		if err != nil {
			delete(s.schedule, key)
			continue
		}
		treePos, err := strconv.ParseUint(parts[3], 10, 64)
		if err != nil {
			delete(s.schedule, key)
			continue
		}
		candidate := readyCandidate{
			key:          key,
			roundID:      parts[0],
			shareIndex:   uint32(idx64),
			proposalID:   uint32(pid64),
			treePosition: treePos,
			scheduledAt:  scheduledAt,
			rank:         s.scheduleRank(key),
		}
		readyByRound[candidate.roundID] = append(readyByRound[candidate.roundID], candidate)
	}

	if len(readyByRound) == 0 {
		return nil
	}

	roundIDs := make([]string, 0, len(readyByRound))
	remaining := 0
	for roundID, candidates := range readyByRound {
		roundIDs = append(roundIDs, roundID)
		remaining += len(candidates)
		sort.Slice(candidates, func(i, j int) bool {
			if !candidates[i].scheduledAt.Equal(candidates[j].scheduledAt) {
				return candidates[i].scheduledAt.Before(candidates[j].scheduledAt)
			}
			return bytes.Compare(candidates[i].rank[:], candidates[j].rank[:]) < 0
		})
		readyByRound[roundID] = candidates
	}
	sort.Strings(roundIDs)

	// Start strictly after the previously served round. If that round is no
	// longer ready, Search still finds its successor in the sorted ring.
	roundIndex := sort.Search(len(roundIDs), func(i int) bool {
		return roundIDs[i] > s.lastReadyRound
	})
	if roundIndex == len(roundIDs) {
		roundIndex = 0
	}
	nextByRound := make(map[string]int, len(roundIDs))
	var result []QueuedShare
candidateLoop:
	for remaining > 0 {
		if limit > 0 && len(result) >= limit {
			break
		}
		roundID := roundIDs[roundIndex]
		roundIndex = (roundIndex + 1) % len(roundIDs)
		candidateIndex := nextByRound[roundID]
		candidates := readyByRound[roundID]
		if candidateIndex >= len(candidates) {
			continue
		}
		candidate := candidates[candidateIndex]
		nextByRound[roundID] = candidateIndex + 1
		remaining--

		share, err := loadShareFrom(s.db, candidate.roundID, candidate.shareIndex, candidate.proposalID, candidate.treePosition)
		if err != nil {
			switch {
			case errors.Is(err, sql.ErrNoRows):
				delete(s.schedule, candidate.key)
				continue
			case errors.Is(err, errCorruptShareRow):
				if !s.handleCorruptCandidateLocked(candidate.roundID, candidate.shareIndex, candidate.proposalID, candidate.treePosition, candidate.key, err) {
					break candidateLoop
				}
				continue
			default:
				s.backoffStoreLocked("TakeReadyBatch: load share", err)
				break candidateLoop
			}
		}
		switch share.State {
		case ShareStateReceived:
		case ShareStateSubmitted, ShareStateFailed:
			delete(s.schedule, candidate.key)
			continue
		default:
			if !s.handleCorruptCandidateLocked(candidate.roundID, candidate.shareIndex, candidate.proposalID, candidate.treePosition, candidate.key, fmt.Errorf("%w: invalid state %d", errCorruptShareRow, share.State)) {
				break candidateLoop
			}
			continue
		}
		s.nextAttemptID++
		if s.nextAttemptID == 0 {
			s.nextAttemptID++
		}
		share.attemptID = s.nextAttemptID
		result = append(result, share)
		s.lastReadyRound = candidate.roundID
		s.inFlight[candidate.key] = inFlightShare{
			roundID:     candidate.roundID,
			voteEndTime: share.VoteEndTime,
			attemptID:   share.attemptID,
			receivedAt:  share.receivedAt,
		}
		delete(s.schedule, candidate.key)
	}

	return result
}

func (s *ShareStore) scheduleRank(key string) [sha256.Size]byte {
	mac := hmac.New(sha256.New, s.schedulingSecret[:])
	_, _ = mac.Write([]byte("helper-schedule-v1:" + key))
	var rank [sha256.Size]byte
	copy(rank[:], mac.Sum(nil))
	return rank
}

func (s *ShareStore) backoffStoreLocked(stage string, err error) {
	s.dequeueRetryUntil = s.now().Add(shareSystemRetryBackoff)
	s.logError(stage+" failed", "error", err)
}

var errCorruptShareRow = errors.New("corrupt share row")

func (s *ShareStore) handleCorruptCandidateLocked(roundID string, shareIndex, proposalID uint32, treePosition uint64, key string, cause error) bool {
	s.lastReadyRound = roundID
	if err := s.failCorruptCandidateLocked(roundID, shareIndex, proposalID, treePosition, key); err != nil {
		s.backoffStoreLocked("TakeReadyBatch: terminalize corrupt share", err)
		return false
	}
	s.logError("TakeReadyBatch: corrupt share failed permanently", "round_id", roundID, "share_index", shareIndex, "proposal_id", proposalID, "tree_position", treePosition, "error", cause)
	return true
}

func (s *ShareStore) failCorruptCandidateLocked(roundID string, shareIndex, proposalID uint32, treePosition uint64, key string) error {
	res, err := s.execWrite(
		`UPDATE shares SET state = 3, attempts = attempts + 1,
		        enc_share_c1 = CASE WHEN vote_end_time = 0 THEN '' ELSE enc_share_c1 END,
		        enc_share_c2 = CASE WHEN vote_end_time = 0 THEN '' ELSE enc_share_c2 END,
		        share_comms = CASE WHEN vote_end_time = 0 THEN '[]' ELSE share_comms END,
		        primary_blind = CASE WHEN vote_end_time = 0 THEN '' ELSE primary_blind END
		  WHERE round_id = ? AND share_index = ? AND proposal_id = ? AND tree_position = ?`,
		roundID, shareIndex, proposalID, treePosition,
	)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		delete(s.schedule, key)
		s.notifyScheduleChangedLocked()
		return nil
	}
	if affected != 1 {
		return fmt.Errorf("terminal update affected %d rows", affected)
	}
	delete(s.schedule, key)
	s.notifyScheduleChangedLocked()
	return nil
}

// MarkSubmitted marks a share as successfully submitted to the chain.
func (s *ShareStore) MarkSubmitted(roundID string, shareIndex, proposalID uint32, treePosition uint64) {
	unlock := s.lockMeasured("MarkSubmitted")
	defer unlock()

	key := schedKey(roundID, shareIndex, proposalID, treePosition)
	if _, ok := s.inFlight[key]; !ok {
		s.logError("MarkSubmitted: completion has no active owner", "round_id", roundID, "share_index", shareIndex, "proposal_id", proposalID, "tree_position", treePosition)
		return
	}
	res, err := s.execWrite(
		`UPDATE shares SET state = 2,
		        enc_share_c1 = '', enc_share_c2 = '',
		        share_comms = '[]', primary_blind = ''
		 WHERE round_id = ? AND share_index = ? AND proposal_id = ? AND tree_position = ? AND state = 0`,
		roundID, shareIndex, proposalID, treePosition,
	)
	if err != nil {
		s.requeueInFlightAfterStoreFailureLocked("MarkSubmitted: db update", key, err)
		s.logError("MarkSubmitted: db update failed", "round_id", roundID, "share_index", shareIndex, "proposal_id", proposalID, "tree_position", treePosition, "error", err)
		return
	}
	affected, err := res.RowsAffected()
	if err != nil {
		s.requeueInFlightAfterStoreFailureLocked("MarkSubmitted: read update result", key, err)
		s.logError("MarkSubmitted: read update result failed", "round_id", roundID, "share_index", shareIndex, "proposal_id", proposalID, "tree_position", treePosition, "error", err)
		return
	}
	if affected == 0 {
		s.requeueInFlightAfterStoreFailureLocked("MarkSubmitted: unresolved update", key, errors.New("submitted update affected no rows"))
		return
	}
	if received := s.inFlight[key].receivedAt; s.metrics != nil && received > 0 && received <= uint64(s.now().Unix()) {
		s.metrics.confirmation.Observe(float64(uint64(s.now().Unix()) - received))
	}
	delete(s.inFlight, key)
}

const (
	shareSystemRetryBackoff        = 10 * time.Second
	shareSystemRetryUrgentBackoff  = 2 * time.Second
	shareSystemRetryDeadlineBuffer = 30 * time.Second
	shareStalledRetryMaxBackoff    = 2 * time.Minute
	shareStalledRetryMaxCount      = 5
	shareFailedMaxAttempts         = 5
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
	unlock := s.lockMeasured("markRetry")
	defer unlock()

	key := schedKey(roundID, shareIndex, proposalID, treePosition)
	active, ok := s.inFlight[key]
	if !ok {
		s.logError("MarkRetry: completion has no active owner", "round_id", roundID, "share_index", shareIndex, "proposal_id", proposalID, "tree_position", treePosition)
		return
	}

	var retryRaw string
	if err := s.db.QueryRow(
		"SELECT retry_state FROM shares WHERE round_id = ? AND share_index = ? AND proposal_id = ? AND tree_position = ? AND state = 0",
		roundID, shareIndex, proposalID, treePosition,
	).Scan(&retryRaw); err != nil {
		s.requeueInFlightAfterStoreFailureLocked("MarkRetry: db query", key, err)
		return
	}
	delete(s.inFlight, key)

	now := s.now()
	if stalledRetryCount == 0 {
		s.schedule[key] = scheduleRetryTime(now, nextShareSystemRetryTime(now, active.voteEndTime))
	} else {
		s.schedule[key] = scheduleRetryTime(now, nextShareStalledRetryTime(now, active.voteEndTime, stalledRetryCount))
	}
	// Poll cheaply, but do not sleep past a future proof slot near the cutoff.
	if retry, err := decodeRetryState(retryRaw, active.voteEndTime); err == nil && retry != nil {
		if _, next := retry.due(now, active.voteEndTime); next.After(now) && next.Before(s.schedule[key]) {
			s.schedule[key] = next
		}
	}

	s.notifyScheduleChangedLocked()
}

// nextShareSystemRetryTime uses the standard polling backoff.
func nextShareSystemRetryTime(now time.Time, voteEndTime uint64) time.Time {
	return nextSharePollTime(now, voteEndTime, shareSystemRetryBackoff)
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
	return nextSharePollTime(now, voteEndTime, backoff)
}

// nextSharePollTime wakes by the urgent window and tightens polling inside it.
// At or after the deadline it uses the standard backoff, even before any proof
// was reserved. Polling must not spin while committed closure is unavailable.
func nextSharePollTime(now time.Time, voteEndTime uint64, backoff time.Duration) time.Time {
	scheduled := now.Add(backoff)
	if voteEndTime == 0 {
		return scheduled
	}

	deadline := time.Unix(int64(voteEndTime), 0)
	remaining := deadline.Sub(now)
	if remaining <= 0 {
		return now.Add(shareSystemRetryBackoff)
	}
	urgentStart := deadline.Add(-shareSystemRetryDeadlineBuffer)
	if now.Before(urgentStart) {
		if scheduled.After(urgentStart) {
			return urgentStart
		}
		return scheduled
	}

	urgentBackoff := max(time.Nanosecond, min(shareSystemRetryUrgentBackoff, remaining/2))
	return now.Add(urgentBackoff)
}

func scheduleSecond(at time.Time) time.Time {
	return time.Unix(at.Unix(), 0)
}

func scheduleRetryTime(now, at time.Time) time.Time {
	scheduled := scheduleSecond(at)
	// Retain subsecond precision when truncation would erase an intentional
	// positive backoff and make the retry immediately due.
	if at.After(now) && !scheduled.After(now) {
		return at
	}
	return scheduled
}

func (s *ShareStore) scheduleCandidateRetryLocked(key string) {
	s.schedule[key] = scheduleSecond(s.now().Add(shareSystemRetryBackoff))
	s.notifyScheduleChangedLocked()
}

func (s *ShareStore) requeueInFlightAfterStoreFailureLocked(stage, key string, err error) {
	delete(s.inFlight, key)
	s.scheduleCandidateRetryLocked(key)
	s.backoffStoreLocked(stage, err)
}

// requeueInFlightIfOwned releases the calling attempt's ownership record when
// it reached the end of processing without recording an outcome. A stale
// attempt cannot release ownership acquired by a later retry.
func (s *ShareStore) requeueInFlightIfOwned(roundID string, shareIndex, proposalID uint32, treePosition, attemptID uint64) bool {
	unlock := s.lockMeasured("requeueInFlightIfOwned")
	defer unlock()

	key := schedKey(roundID, shareIndex, proposalID, treePosition)
	active, ok := s.inFlight[key]
	if !ok || active.attemptID != attemptID {
		return false
	}
	delete(s.inFlight, key)
	// The safety net does not spend an attempt, so always apply a positive
	// backoff. Deadline-aware retries may become immediately due after vote end
	// and are reserved for normal paths with a bounded failure transition.
	s.scheduleCandidateRetryLocked(key)
	s.logError("worker returned without queue transition",
		"round_id", roundID,
		"share_index", shareIndex,
		"proposal_id", proposalID,
		"tree_position", treePosition,
		"error", errors.New("worker returned without queue transition"),
	)
	return true
}

// recordFailedAttemptLocked atomically spends one processing attempt. The
// caller restores process-local ownership after an unresolved database error.
func (s *ShareStore) recordFailedAttemptLocked(roundID string, shareIndex, proposalID uint32, treePosition uint64, key string) error {
	var state, attempts int
	var voteEndTime uint64
	err := s.db.QueryRow(
		`UPDATE shares
		    SET attempts = attempts + 1,
		        state = CASE WHEN attempts + 1 >= ? THEN 3 ELSE 0 END,
		        enc_share_c1 = CASE WHEN attempts + 1 >= ? AND vote_end_time = 0 THEN '' ELSE enc_share_c1 END,
		        enc_share_c2 = CASE WHEN attempts + 1 >= ? AND vote_end_time = 0 THEN '' ELSE enc_share_c2 END,
		        share_comms = CASE WHEN attempts + 1 >= ? AND vote_end_time = 0 THEN '[]' ELSE share_comms END,
		        primary_blind = CASE WHEN attempts + 1 >= ? AND vote_end_time = 0 THEN '' ELSE primary_blind END
		  WHERE round_id = ? AND share_index = ? AND proposal_id = ? AND tree_position = ?
		    AND state = 0
		  RETURNING state, attempts, vote_end_time`,
		shareFailedMaxAttempts,
		shareFailedMaxAttempts,
		shareFailedMaxAttempts,
		shareFailedMaxAttempts,
		shareFailedMaxAttempts,
		roundID, shareIndex, proposalID, treePosition,
	).Scan(&state, &attempts, &voteEndTime)
	if errors.Is(err, sql.ErrNoRows) {
		return errors.New("failed-attempt update affected no received row")
	}
	if err != nil {
		return err
	}

	switch ShareState(state) {
	case ShareStateReceived:
		delete(s.inFlight, key)
		backoff := time.Duration(1<<uint(min(attempts, 6))) * time.Second
		s.schedule[key] = scheduleSecond(s.now().Add(backoff))
		s.notifyScheduleChangedLocked()
		return nil
	case ShareStateFailed:
		delete(s.inFlight, key)
		delete(s.schedule, key)
		s.notifyScheduleChangedLocked()
		if voteEndTime == 0 {
			s.truncateWALAfterWitnessCleanup("MarkFailed: permanent scrub")
		}
		return nil
	default:
		return fmt.Errorf("unexpected failed-attempt state %d", state)
	}
}

// MarkFailed marks a share processing attempt as failed, with retry or
// permanent failure after max attempts.
func (s *ShareStore) MarkFailed(roundID string, shareIndex, proposalID uint32, treePosition uint64) {
	unlock := s.lockMeasured("MarkFailed")
	defer unlock()

	key := schedKey(roundID, shareIndex, proposalID, treePosition)
	if _, ok := s.inFlight[key]; !ok {
		s.logError("MarkFailed: completion has no active owner", "round_id", roundID, "share_index", shareIndex, "proposal_id", proposalID, "tree_position", treePosition)
		return
	}
	if err := s.recordFailedAttemptLocked(roundID, shareIndex, proposalID, treePosition, key); err != nil {
		s.requeueInFlightAfterStoreFailureLocked("MarkFailed: record attempt", key, err)
		s.logError("MarkFailed: db update failed", "round_id", roundID, "share_index", shareIndex, "proposal_id", proposalID, "tree_position", treePosition, "error", err)
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
	now := s.now()

	unlock := s.lockMeasured("Status")
	defer unlock()

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
		case 0:
			entry.Pending += count
		case 2:
			entry.Submitted += count
		case 3:
			entry.Failed += count
		}
		result[roundID] = entry
	}
	for _, active := range s.inFlight {
		entry := result[active.roundID]
		entry.Processing++
		result[active.roundID] = entry
	}

	// Readiness follows the effective in-memory schedule rather than persisted
	// submit_at. This keeps retry backoff out of the ready count.
	for key, scheduledAt := range s.schedule {
		roundID, _, ok := strings.Cut(key, ":")
		if !ok {
			continue
		}
		entry, exists := result[roundID]
		if !exists {
			continue
		}
		if scheduledAt.After(now) {
			entry.NotYetDue++
		} else {
			entry.Ready++
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
// included for local debugging. Submitted rows should already have witness
// material cleared, while failed rows retain it until round purge.
func (s *ShareStore) ExportQueue(roundID string, now time.Time) (QueueExport, error) {
	export := QueueExport{
		Version:    QueueExportVersion,
		RoundID:    roundID,
		ExportedAt: uint64(now.Unix()),
	}

	unlock := s.lockMeasured("ExportQueue")
	defer unlock()
	for _, active := range s.inFlight {
		if active.roundID == roundID {
			return QueueExport{}, fmt.Errorf("cannot export round %s while a worker is active", roundID)
		}
	}

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
		        original_submit_at, received_at
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
		var commsJSON []byte
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
		); err != nil {
			return QueueExport{}, fmt.Errorf("scan queue row: %w", err)
		}
		row.EncShare.ShareIndex = row.ShareIndex
		row.State = ShareState(state)
		row.Processable = isProcessableShareState(row.State)
		if row.OriginalSubmitAt == 0 {
			row.OriginalSubmitAt = row.SubmitAt
		}
		if err := json.Unmarshal(commsJSON, &row.ShareComms); err != nil {
			rawComms := base64.StdEncoding.EncodeToString(commsJSON)
			row.Corrupt = true
			row.CorruptionReason = "invalid_share_comms_json"
			row.RawShareCommsBase64 = &rawComms
			row.ShareComms = nil
			row.Processable = false
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

	result := QueueImportResult{}
	schedule := make(map[string]time.Time)
	immediateBucket := time.Unix(s.now().Unix(), 0)

	unlock := s.lockMeasured("ImportQueue")
	defer unlock()

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
		if row.Corrupt || !isProcessableShareState(row.State) {
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
			schedule[schedKey(export.RoundID, row.ShareIndex, row.ProposalID, row.TreePosition)] = scheduledTime(submitAt, immediateBucket)
			continue
		}

		duplicate, existingState, err := importRowMatchesExisting(tx, export.RoundID, row)
		if err != nil {
			return QueueImportResult{}, err
		}
		if duplicate {
			result.Duplicates++
			if opts.ForceReady && isProcessableShareState(existingState) {
				key := schedKey(export.RoundID, row.ShareIndex, row.ProposalID, row.TreePosition)
				if _, active := s.inFlight[key]; active {
					// Preserve the current worker's ownership. Force-ready may
					// reschedule an idle duplicate, but it cannot create a second
					// owner for an attempt already in progress.
					continue
				}
				if err := forceReadyExistingImportRow(tx, export.RoundID, row, originalSubmitAt); err != nil {
					return QueueImportResult{}, err
				}
				if existingState == ShareStateReceived {
					schedule[key] = scheduledTime(0, immediateBucket)
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
func scheduledTime(submitAt uint64, immediateBucket time.Time) time.Time {
	if submitAt == 0 {
		return immediateBucket
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

// lastMinuteWindowStart is shared by retry scheduling and the public queue
// summary. The final window is 40% of the round duration, capped at 6 hours.
func lastMinuteWindowStart(createdAtTime, voteEndTime uint64) uint64 {
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
		LastMinuteStart: lastMinuteWindowStart(info.CreatedAtTime, info.VoteEndTime),
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

	unlock := s.lockMeasured("QueueSummary")
	defer unlock()

	rows, err := s.db.Query(
		`SELECT state, submit_at, received_at, share_index, proposal_id, tree_position
		   FROM shares
		  WHERE round_id = ?`,
		roundID,
	)
	if err != nil {
		return QueueSummary{}, err
	}
	defer rows.Close()

	for rows.Next() {
		var state int
		var submitAt, receivedAt uint64
		var shareIndex, proposalID uint32
		var treePosition uint64
		if err := rows.Scan(&state, &submitAt, &receivedAt, &shareIndex, &proposalID, &treePosition); err != nil {
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
		key := schedKey(roundID, shareIndex, proposalID, treePosition)
		_, active := s.inFlight[key]
		switch {
		case active:
			bucket.Processing++
			summary.Processing++
		case ShareState(state) == ShareStateReceived:
			if effectiveTime <= generatedAt {
				bucket.OverduePending++
			} else {
				bucket.PendingFuture++
			}
			if scheduledAt, ok := s.schedule[key]; ok {
				if scheduledAt.After(now) {
					summary.NotYetDue++
				} else {
					summary.Ready++
				}
			}
		case ShareState(state) == ShareStateWitnessed:
			bucket.Processing++
			summary.Processing++
		case ShareState(state) == ShareStateSubmitted:
			bucket.Submitted++
		case ShareState(state) == ShareStateFailed:
			bucket.Failed++
		}
		bucket.Total++
	}
	if err := rows.Err(); err != nil {
		return QueueSummary{}, err
	}

	return summary, nil
}

// ExpiredRoundSummaries returns per-round queue counts for rounds whose voting
// deadline has passed by wall time; this does not establish chain closure.
// It excludes shares first received at or after the round's
// close time, since they were not pending before close and should not trigger
// this alert. Call this before PurgeRounds so unsubmitted share alerts
// can be emitted without retaining witness data.
func (s *ShareStore) ExpiredRoundSummaries(now time.Time) ([]ExpiredRoundSummary, error) {
	unlock := s.lockMeasured("ExpiredRoundSummaries")
	defer unlock()

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

// unsubmittedSharesBeforeClose loads only the inputs needed to check commitment
// for pending and failed rows counted by ExpiredRoundSummaries. Submitted rows
// have no witness left, and shares received after closure are not reported.
func (s *ShareStore) unsubmittedSharesBeforeClose(roundID string, now time.Time) ([]QueuedShare, error) {
	unlock := s.lockMeasured("unsubmittedSharesBeforeClose")
	defer unlock()
	rows, err := s.db.Query(`SELECT share_index, shares_hash, proposal_id,
		vote_decision, primary_blind, tree_position, state
		FROM shares WHERE round_id = ? AND state IN (0, 1, 3)
		AND vote_end_time > 0 AND vote_end_time < ?
		AND (received_at = 0 OR received_at < vote_end_time)`, roundID, now.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var shares []QueuedShare
	for rows.Next() {
		var share QueuedShare
		share.Payload.VoteRoundID = roundID
		if err := rows.Scan(&share.Payload.EncShare.ShareIndex, &share.Payload.SharesHash,
			&share.Payload.ProposalID, &share.Payload.VoteDecision, &share.Payload.PrimaryBlind,
			&share.Payload.TreePosition, &share.State); err != nil {
			return nil, err
		}
		shares = append(shares, share)
	}
	return shares, rows.Err()
}

// Close closes the database connection.
func (s *ShareStore) Close() error {
	if s.metrics != nil {
		s.metrics.store.CompareAndSwap(s, nil)
	}
	return errors.Join(s.db.Close(), releaseShareStoreLock(s.lockFile))
}

// ExpiredRoundIDs lists wall-clock expiry candidates, including rounds whose
// shares were received after the deadline. Callers must confirm chain closure.
func (s *ShareStore) ExpiredRoundIDs(now time.Time) ([]string, error) {
	unlock := s.lockMeasured("ExpiredRoundIDs")
	defer unlock()
	rows, err := s.db.Query(`SELECT round_id FROM shares WHERE vote_end_time > 0 AND vote_end_time < ?
 UNION SELECT round_id FROM rounds WHERE vote_end_time > 0 AND vote_end_time < ?`, now.Unix(), now.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// PurgeRounds deletes share data and metadata only for the supplied rounds.
// The caller must positively confirm committed closure before including a round.
// An empty list still retries WAL cleanup from an earlier blocked checkpoint.
func (s *ShareStore) PurgeRounds(roundIDs []string) int64 {
	unlock := s.lockMeasured("PurgeRounds")
	defer unlock()
	var deleted int64
	activeRounds := make(map[string]bool)
	for _, active := range s.inFlight {
		activeRounds[active.roundID] = true
	}
	purgeable := make([]string, 0, len(roundIDs))
	for _, roundID := range roundIDs {
		if activeRounds[roundID] {
			s.logError("PurgeRounds: retaining active round", "round_id", roundID)
			continue
		}
		purgeable = append(purgeable, roundID)
	}
	if len(purgeable) > 0 {
		tx, err := s.db.Begin()
		if err != nil {
			s.logError("PurgeRounds: begin failed", "error", err)
			return 0
		}
		defer tx.Rollback()
		for _, roundID := range purgeable {
			res, err := tx.Exec("DELETE FROM shares WHERE round_id = ?", roundID)
			if err != nil {
				s.logError("PurgeRounds: delete shares failed", "error", err)
				return 0
			}
			count, err := res.RowsAffected()
			if err != nil {
				s.logError("PurgeRounds: row count failed", "error", err)
				return 0
			}
			deleted += count
			if _, err := tx.Exec("DELETE FROM rounds WHERE round_id = ?", roundID); err != nil {
				s.logError("PurgeRounds: delete metadata failed", "error", err)
				return 0
			}
		}
		if err := tx.Commit(); err != nil {
			s.logError("PurgeRounds: commit failed", "error", err)
			return 0
		}
	}
	s.truncateWALAfterWitnessCleanup("PurgeRounds")
	closed := make(map[string]bool, len(purgeable))
	for _, roundID := range purgeable {
		closed[roundID] = true
		delete(s.roundCache, roundID)
	}
	schedulePruned := false
	for key := range s.schedule {
		roundID := strings.SplitN(key, ":", 2)[0]
		if closed[roundID] {
			delete(s.schedule, key)
			schedulePruned = true
		}
	}
	if schedulePruned {
		s.notifyScheduleChangedLocked()
	}
	if deleted > 0 && s.logInfo != nil {
		s.logInfo("purged closed round data", "rows_deleted", deleted)
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

// recover normalizes legacy state and restores every pending submit_at schedule.
func (s *ShareStore) recover() error {
	immediateBucket := time.Unix(s.now().Unix(), 0)
	// Current workers remain Received; reset Witnessed rows left by older binaries.
	if _, err := s.execWrite("UPDATE shares SET state = 0 WHERE state = 1"); err != nil {
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
			return fmt.Errorf("scan rounds cache: %w", err)
		}
		s.roundCache[roundID] = info
	}
	if err := roundRows.Err(); err != nil {
		return fmt.Errorf("iterate rounds cache: %w", err)
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
			return fmt.Errorf("scan recoverable share: %w", err)
		}
		schedTime := scheduledTime(submitAt, immediateBucket)
		s.schedule[schedKey(roundID, shareIndex, proposalID, treePosition)] = schedTime
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate recoverable shares: %w", err)
	}
	return nil
}

type shareRowQuerier interface {
	QueryRow(query string, args ...any) *sql.Row
}

func (s *ShareStore) loadShare(roundID string, shareIndex, proposalID uint32, treePosition uint64) (QueuedShare, bool) {
	share, err := loadShareFrom(s.db, roundID, shareIndex, proposalID, treePosition)
	return share, err == nil
}

func loadShareFrom(queryer shareRowQuerier, roundID string, shareIndex, proposalID uint32, treePosition uint64) (QueuedShare, error) {
	var share QueuedShare
	var sharesHash, voteDecision, encC1, encC2 any
	var commsJSON, primaryBlind, stateValue, attemptsValue any
	var voteEndTime, submitAt, retryState, receivedAt any

	err := queryer.QueryRow(
		`SELECT shares_hash, vote_decision, enc_share_c1, enc_share_c2,
		        share_comms, primary_blind, state, attempts, vote_end_time, submit_at, retry_state, received_at
		 FROM shares WHERE round_id = ? AND share_index = ? AND proposal_id = ? AND tree_position = ?`,
		roundID, shareIndex, proposalID, treePosition,
	).Scan(
		&sharesHash,
		&voteDecision,
		&encC1,
		&encC2,
		&commsJSON,
		&primaryBlind,
		&stateValue,
		&attemptsValue,
		&voteEndTime,
		&submitAt,
		&retryState,
		&receivedAt,
	)
	if err != nil {
		return share, fmt.Errorf("scan share row: %w", err)
	}

	share.Payload.SharesHash, err = decodeRowText("shares_hash", sharesHash)
	if err != nil {
		return share, err
	}
	share.Payload.VoteDecision, err = decodeRowUint32("vote_decision", voteDecision)
	if err != nil {
		return share, err
	}
	share.Payload.EncShare.C1, err = decodeRowText("enc_share_c1", encC1)
	if err != nil {
		return share, err
	}
	share.Payload.EncShare.C2, err = decodeRowText("enc_share_c2", encC2)
	if err != nil {
		return share, err
	}
	commsText, err := decodeRowText("share_comms", commsJSON)
	if err != nil {
		return share, err
	}
	share.Payload.PrimaryBlind, err = decodeRowText("primary_blind", primaryBlind)
	if err != nil {
		return share, err
	}
	state, err := decodeRowInt("state", stateValue)
	if err != nil {
		return share, err
	}
	switch ShareState(state) {
	case ShareStateReceived, ShareStateWitnessed, ShareStateSubmitted, ShareStateFailed:
	default:
		return share, fmt.Errorf("%w: invalid state %d", errCorruptShareRow, state)
	}
	attempts, err := decodeRowInt("attempts", attemptsValue)
	if err != nil {
		return share, err
	}
	share.VoteEndTime, err = decodeRowUint64("vote_end_time", voteEndTime)
	if err != nil {
		return share, err
	}
	share.Payload.SubmitAt, err = decodeRowUint64("submit_at", submitAt)
	if err != nil {
		return share, err
	}
	share.retryState, err = decodeRowText("retry_state", retryState)
	if err != nil {
		return share, err
	}

	// Diagnostics cannot make a legacy or malformed timestamp reject a share.
	share.receivedAt, _ = decodeRowUint64("received_at", receivedAt)
	share.Payload.VoteRoundID = roundID
	share.Payload.ProposalID = proposalID
	share.Payload.TreePosition = treePosition
	share.Payload.EncShare.ShareIndex = shareIndex
	share.State = ShareState(state)
	share.Attempts = attempts

	if err := json.Unmarshal([]byte(commsText), &share.Payload.ShareComms); err != nil {
		return share, fmt.Errorf("%w: invalid share_comms JSON: %v", errCorruptShareRow, err)
	}

	return share, nil
}

func decodeRowText(column string, value any) (string, error) {
	switch value := value.(type) {
	case string:
		return value, nil
	case []byte:
		return string(value), nil
	default:
		return "", fmt.Errorf("%w: %s has type %T", errCorruptShareRow, column, value)
	}
}

func decodeRowInt(column string, value any) (int, error) {
	integer, err := decodeRowUint64(column, value)
	if err != nil {
		return 0, err
	}
	maxInt := uint64(^uint(0) >> 1)
	if integer > maxInt {
		return 0, fmt.Errorf("%w: %s is out of range", errCorruptShareRow, column)
	}
	return int(integer), nil
}

func decodeRowUint32(column string, value any) (uint32, error) {
	integer, err := decodeRowUint64(column, value)
	if err != nil {
		return 0, err
	}
	if integer > uint64(^uint32(0)) {
		return 0, fmt.Errorf("%w: %s is out of range", errCorruptShareRow, column)
	}
	return uint32(integer), nil
}

func decodeRowUint64(column string, value any) (uint64, error) {
	switch value := value.(type) {
	case int64:
		if value >= 0 {
			return uint64(value), nil
		}
	case int:
		if value >= 0 {
			return uint64(value), nil
		}
	case uint64:
		return value, nil
	case uint32:
		return uint64(value), nil
	}
	return 0, fmt.Errorf("%w: %s must be a non-negative integer, got %T", errCorruptShareRow, column, value)
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
	unlockLookup := s.lockMeasured("round_cache_lookup")
	if info, ok := s.roundCache[roundID]; ok {
		if info.CreatedAtTime != 0 || s.fetchRoundInfo == nil {
			unlockLookup()
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
			unlockLookup()
			return info, nil
		}
		// Older helper DBs may have only vote_end_time cached. Refresh from the
		// keeper so the public summary can cover the full round window.
	}
	unlockLookup()

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
	unlock := s.lockMeasured("getRoundInfo")
	defer unlock()
	if cached, ok := s.roundCache[roundID]; ok && (cached.CreatedAtTime != 0 || s.fetchRoundInfo == nil) {
		return cached, nil
	}

	s.roundCache[roundID] = info
	if _, err := s.execWrite(
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
