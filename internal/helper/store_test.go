package helper

import (
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestStore(t *testing.T) *ShareStore {
	t.Helper()
	// Provide a permissive round fetcher so tests don't fail on unknown rounds.
	// minDelay=0 gives zero delay, preserving the existing test expectation
	// that shares are immediately ready.
	fetcher := func(roundID string) (uint64, error) {
		return 0, nil
	}
	s, err := NewShareStore(":memory:", 0, fetcher)
	require.NoError(t, err)
	t.Cleanup(func() { s.Close() })
	return s
}

func testPayload(roundID string, shareIndex uint32) SharePayload {
	const zeroB64 = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
	comms := make([]string, 16)
	for i := range comms {
		comms[i] = zeroB64
	}
	return SharePayload{
		SharesHash:   zeroB64,
		ProposalID:   1,
		VoteDecision: 0,
		EncShare: EncryptedShareWire{
			C1:         zeroB64,
			C2:         zeroB64,
			ShareIndex: shareIndex,
		},
		ShareIndex:   shareIndex,
		TreePosition: 0,
		VoteRoundID:  roundID,
		ShareComms:   comms,
		PrimaryBlind: zeroB64,
	}
}

func enqueueAndRequireInserted(t *testing.T, s *ShareStore, payload SharePayload) {
	t.Helper()
	result, err := s.Enqueue(payload)
	require.NoError(t, err)
	require.Equal(t, EnqueueInserted, result)
}

func TestEnqueueAndTakeReady(t *testing.T) {
	s := newTestStore(t)

	enqueueAndRequireInserted(t, s, testPayload("aabbccdd", 0))

	// With zero delay, share should be immediately ready.
	ready := s.TakeReady()
	assert.Len(t, ready, 1)
	assert.Equal(t, "aabbccdd", ready[0].Payload.VoteRoundID)
	assert.Equal(t, uint32(0), ready[0].Payload.EncShare.ShareIndex)

	// Second call: nothing ready (already taken).
	ready = s.TakeReady()
	assert.Empty(t, ready)
}

func TestMarkSubmitted(t *testing.T) {
	s := newTestStore(t)

	enqueueAndRequireInserted(t, s, testPayload("round1", 0))

	ready := s.TakeReady()
	require.Len(t, ready, 1)

	s.MarkSubmitted("round1", 0, 1, 0)

	status := s.Status()
	assert.Equal(t, 1, status["round1"].Submitted)
	assert.Equal(t, 0, status["round1"].Pending)
}

func TestMarkFailed_RetryAndPermanent(t *testing.T) {
	s := newTestStore(t)

	enqueueAndRequireInserted(t, s, testPayload("round1", 0))

	// Take and fail it repeatedly, fast-forwarding the backoff schedule.
	for i := range 4 {
		ready := s.TakeReady()
		require.Len(t, ready, 1, "attempt %d", i)
		s.MarkFailed("round1", 0, 1, 0)
		// Fast-forward schedule so it's immediately ready again.
		s.mu.Lock()
		s.schedule[schedKey("round1", 0, 1, 0)] = time.Now().Add(-time.Second)
		s.mu.Unlock()
	}

	// After 4 failures (attempts = 4), take once more.
	ready := s.TakeReady()
	require.Len(t, ready, 1)
	s.MarkFailed("round1", 0, 1, 0) // 5th attempt = permanent failure

	// Now it should be permanently failed.
	status := s.Status()
	assert.Equal(t, 1, status["round1"].Failed)
	assert.Equal(t, 0, status["round1"].Pending)
}

func TestStatus(t *testing.T) {
	s := newTestStore(t)

	// Enqueue 2 shares for the same round.
	enqueueAndRequireInserted(t, s, testPayload("round1", 0))
	enqueueAndRequireInserted(t, s, testPayload("round1", 1))

	status := s.Status()
	assert.Equal(t, 2, status["round1"].Total)
	assert.Equal(t, 2, status["round1"].Pending)
}

func TestDuplicateEnqueue(t *testing.T) {
	s := newTestStore(t)

	result, err := s.Enqueue(testPayload("round1", 0))
	require.NoError(t, err)
	require.Equal(t, EnqueueInserted, result)

	// Duplicate: same payload, idempotent result.
	result, err = s.Enqueue(testPayload("round1", 0))
	require.NoError(t, err)
	require.Equal(t, EnqueueDuplicate, result)

	status := s.Status()
	assert.Equal(t, 1, status["round1"].Total)
}

func TestConflictingDuplicateEnqueue(t *testing.T) {
	s := newTestStore(t)

	result, err := s.Enqueue(testPayload("round1", 0))
	require.NoError(t, err)
	require.Equal(t, EnqueueInserted, result)

	conflicting := testPayload("round1", 0)
	conflicting.SharesHash = "AQAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="

	result, err = s.Enqueue(conflicting)
	require.NoError(t, err)
	require.Equal(t, EnqueueConflict, result)

	status := s.Status()
	assert.Equal(t, 1, status["round1"].Total)
}

func TestSameShareIndexDifferentProposals(t *testing.T) {
	s := newTestStore(t)

	// share_index 0 repeats across proposals in the same round — both must be accepted.
	p1 := testPayload("round1", 0)
	p1.ProposalID = 1
	enqueueAndRequireInserted(t, s, p1)

	p2 := testPayload("round1", 0)
	p2.ProposalID = 2
	enqueueAndRequireInserted(t, s, p2)

	status := s.Status()
	assert.Equal(t, 2, status["round1"].Total)

	// Both should be independently takeable and submittable.
	ready := s.TakeReady()
	assert.Len(t, ready, 2)

	s.MarkSubmitted("round1", 0, 1, 0)
	s.MarkSubmitted("round1", 0, 2, 0)

	status = s.Status()
	assert.Equal(t, 2, status["round1"].Submitted)
}

func TestSameShareIndexDifferentTreePositions(t *testing.T) {
	s := newTestStore(t)

	// Two shares with the same (round_id, share_index, proposal_id) but different
	// tree_position — the multi-bundle scenario. Both must be accepted.
	p1 := testPayload("round1", 0)
	p1.TreePosition = 10
	enqueueAndRequireInserted(t, s, p1)

	p2 := testPayload("round1", 0)
	p2.TreePosition = 20
	enqueueAndRequireInserted(t, s, p2)

	status := s.Status()
	assert.Equal(t, 2, status["round1"].Total)

	// Both should be independently takeable and submittable.
	ready := s.TakeReady()
	assert.Len(t, ready, 2)

	s.MarkSubmitted("round1", 0, 1, 10)
	s.MarkSubmitted("round1", 0, 1, 20)

	status = s.Status()
	assert.Equal(t, 2, status["round1"].Submitted)
}

func TestRecovery(t *testing.T) {
	// Use a file-based DB so we can reopen it.
	dbPath := t.TempDir() + "/helper_test.db"
	fetcher := func(roundID string) (uint64, error) { return 0, nil }

	s1, err := NewShareStore(dbPath, 0, fetcher)
	require.NoError(t, err)

	enqueueAndRequireInserted(t, s1, testPayload("round1", 0))

	// Take the share (moves to Witnessed state).
	ready := s1.TakeReady()
	require.Len(t, ready, 1)

	// Close without marking submitted (simulates crash).
	s1.Close()

	// Reopen: recovery should reset Witnessed → Received with fresh delay.
	s2, err := NewShareStore(dbPath, 0, fetcher)
	require.NoError(t, err)
	defer s2.Close()

	ready = s2.TakeReady()
	assert.Len(t, ready, 1, "recovered share should be ready again")
}

func TestUniformDelay_ImminentDeadline(t *testing.T) {
	s, err := NewShareStore(":memory:", 90*time.Second, nil)
	require.NoError(t, err)
	defer s.Close()

	// 30s remaining minus 60s buffer → negative remaining → delay must be 0.
	voteEndTime := uint64(time.Now().Add(30 * time.Second).Unix())
	delay := s.uniformDelay(voteEndTime)
	assert.Equal(t, time.Duration(0), delay, "delay should be 0 when remaining time < 60s buffer")

	// With enough remaining time, delay must be in [0, remaining - 60s].
	voteEndTime = uint64(time.Now().Add(5 * time.Minute).Unix())
	delay = s.uniformDelay(voteEndTime)
	maxAllowed := 5*time.Minute - 60*time.Second
	assert.LessOrEqual(t, delay, maxAllowed, "delay should be within remaining - 60s")
}

func TestUniformDelayDistribution(t *testing.T) {
	// Verify delays are uniformly spread in [0, remaining - 60s] with no
	// clustering at zero (the old exponential problem).
	voteEndTime := uint64(time.Now().Add(24 * time.Hour).Unix())
	remaining := 24*time.Hour - 60*time.Second

	s, err := NewShareStore(":memory:", 90*time.Second, nil)
	require.NoError(t, err)
	defer s.Close()

	const n = 1000
	var total time.Duration
	for range n {
		d := s.uniformDelay(voteEndTime)
		assert.GreaterOrEqual(t, d, time.Duration(0), "delay must be non-negative")
		assert.LessOrEqual(t, d, remaining, "delay must be within window")
		total += d
	}
	// Mean of Uniform(0, remaining) ≈ remaining/2. Allow 30% tolerance.
	mean := total / n
	expected := remaining / 2
	assert.Greater(t, mean, expected*7/10, "mean should be > 0.35 * remaining")
	assert.Less(t, mean, expected*13/10, "mean should be < 0.65 * remaining")
}

func TestUniformDelay_FloorRespected(t *testing.T) {
	minDelay := 2 * time.Minute
	s, err := NewShareStore(":memory:", minDelay, nil)
	require.NoError(t, err)
	defer s.Close()

	voteEndTime := uint64(time.Now().Add(time.Hour).Unix())
	for range 50 {
		d := s.uniformDelay(voteEndTime)
		assert.GreaterOrEqual(t, d, minDelay, "delay must respect minDelay floor")
	}
}

func TestPurgeExpiredRounds(t *testing.T) {
	fetcher := func(roundID string) (uint64, error) {
		if roundID == "expired_round" {
			return uint64(time.Now().Add(-time.Hour).Unix()), nil
		}
		return uint64(time.Now().Add(time.Hour).Unix()), nil
	}

	s, err := NewShareStore(":memory:", 0, fetcher)
	require.NoError(t, err)
	defer s.Close()

	// Enqueue a share for an expired round and an active round.
	enqueueAndRequireInserted(t, s, testPayload("expired_round", 0))
	enqueueAndRequireInserted(t, s, testPayload("active_round", 0))

	status := s.Status()
	assert.Equal(t, 1, status["expired_round"].Total)
	assert.Equal(t, 1, status["active_round"].Total)

	deleted := s.PurgeExpiredRounds()
	assert.Equal(t, int64(1), deleted)

	status = s.Status()
	assert.Equal(t, 0, status["expired_round"].Total)
	assert.Equal(t, 1, status["active_round"].Total)
}

func TestGetVoteEndTime_Cache(t *testing.T) {
	fetchCalls := 0
	fetcher := func(roundID string) (uint64, error) {
		fetchCalls++
		return 1000000, nil
	}

	s, err := NewShareStore(":memory:", 0, fetcher)
	require.NoError(t, err)
	defer s.Close()

	// First call should fetch from keeper.
	vet, err := s.getVoteEndTime("round1")
	require.NoError(t, err)
	assert.Equal(t, uint64(1000000), vet)
	assert.Equal(t, 1, fetchCalls)

	// Second call should hit cache, no additional fetch.
	vet, err = s.getVoteEndTime("round1")
	require.NoError(t, err)
	assert.Equal(t, uint64(1000000), vet)
	assert.Equal(t, 1, fetchCalls)
}

func TestGetVoteEndTime_NilFetcher(t *testing.T) {
	s, err := NewShareStore(":memory:", 0, nil)
	require.NoError(t, err)
	defer s.Close()

	// With nil fetcher and no cache, should return ErrUnknownRound.
	_, err = s.getVoteEndTime("round1")
	assert.ErrorIs(t, err, ErrUnknownRound)
}

func TestMigrateOldSchema(t *testing.T) {
	dbPath := t.TempDir() + "/old_helper.db"

	// Simulate a database with old 3-column PK and without vote_end_time.
	oldDB, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)

	_, err = oldDB.Exec(`
		CREATE TABLE shares (
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
	require.NoError(t, err)
	require.NoError(t, oldDB.Close())

	// Opening with current code should migrate PK and add vote_end_time.
	fetcher := func(roundID string) (uint64, error) { return 0, nil }
	s, err := NewShareStore(dbPath, 0, fetcher)
	require.NoError(t, err)
	defer s.Close()

	// vote_end_time column should now exist.
	hasVoteEndTime, err := tableHasColumn(s.db, "shares", "vote_end_time")
	require.NoError(t, err)
	assert.True(t, hasVoteEndTime)

	// tree_position should now be part of the primary key.
	notInPK, err := columnNotInPK(s.db, "shares", "tree_position")
	require.NoError(t, err)
	assert.False(t, notInPK, "tree_position should be in the PK after migration")

	// rounds table should exist.
	var roundsTableCount int
	err = s.db.QueryRow(
		"SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'rounds'",
	).Scan(&roundsTableCount)
	require.NoError(t, err)
	assert.Equal(t, 1, roundsTableCount)

	// Enqueue path should work on migrated DB.
	result, err := s.Enqueue(testPayload("round1", 0))
	require.NoError(t, err)
	assert.Equal(t, EnqueueInserted, result)

	// Multi-bundle scenario should work on migrated DB: same share_index
	// and proposal_id but different tree_position.
	p2 := testPayload("round1", 0)
	p2.TreePosition = 42
	result, err = s.Enqueue(p2)
	require.NoError(t, err)
	assert.Equal(t, EnqueueInserted, result)
}
