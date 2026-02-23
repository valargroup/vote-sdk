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
	s, err := NewShareStore(":memory:", 0, nil)
	require.NoError(t, err)
	t.Cleanup(func() { s.Close() })
	return s
}

func testPayload(roundID string, shareIndex uint32) SharePayload {
	return SharePayload{
		SharesHash:   "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=",
		ProposalID:   1,
		VoteDecision: 0,
		EncShare: EncryptedShareWire{
			C1:         "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=",
			C2:         "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=",
			ShareIndex: shareIndex,
		},
		ShareIndex:   shareIndex,
		TreePosition: 0,
		VoteRoundID:  roundID,
		AllEncShares: []EncryptedShareWire{
			{C1: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=", C2: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=", ShareIndex: 0},
			{C1: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=", C2: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=", ShareIndex: 1},
			{C1: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=", C2: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=", ShareIndex: 2},
			{C1: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=", C2: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=", ShareIndex: 3},
			{C1: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=", C2: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=", ShareIndex: 4},
		},
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

	s1, err := NewShareStore(dbPath, 0, nil)
	require.NoError(t, err)

	enqueueAndRequireInserted(t, s1, testPayload("round1", 0))

	// Take the share (moves to Witnessed state).
	ready := s1.TakeReady()
	require.Len(t, ready, 1)

	// Close without marking submitted (simulates crash).
	s1.Close()

	// Reopen: recovery should reset Witnessed → Received with fresh delay.
	s2, err := NewShareStore(dbPath, 0, nil)
	require.NoError(t, err)
	defer s2.Close()

	ready = s2.TakeReady()
	assert.Len(t, ready, 1, "recovered share should be ready again")
}

func TestExponentialDelayCapped(t *testing.T) {
	// meanDelay=1h, voteEndTime=30s from now → delay must be capped.
	s, err := NewShareStore(":memory:", time.Hour, nil)
	require.NoError(t, err)
	defer s.Close()

	voteEndTime := uint64(time.Now().Add(30 * time.Second).Unix())
	delay := s.cappedExponentialDelay(voteEndTime)

	// Delay should be at most 30s - 60s buffer = 0 (since remaining < 0 after buffer).
	// Actually 30s - 60s = -30s, so delay should be 0.
	assert.Equal(t, time.Duration(0), delay, "delay should be 0 when remaining time < 60s buffer")

	// Now test with enough remaining time.
	voteEndTime = uint64(time.Now().Add(5 * time.Minute).Unix())
	delay = s.cappedExponentialDelay(voteEndTime)
	maxAllowed := 5*time.Minute - 60*time.Second
	assert.LessOrEqual(t, delay, maxAllowed, "delay should be capped at remaining - 60s")
}

func TestExponentialDelayZeroMean(t *testing.T) {
	s, err := NewShareStore(":memory:", 0, nil)
	require.NoError(t, err)
	defer s.Close()

	// With meanDelay=0, all delays should be 0 regardless of voteEndTime.
	delay := s.cappedExponentialDelay(0)
	assert.Equal(t, time.Duration(0), delay)

	delay = s.cappedExponentialDelay(uint64(time.Now().Add(time.Hour).Unix()))
	assert.Equal(t, time.Duration(0), delay)
}

func TestExponentialDelayDistribution(t *testing.T) {
	// Verify that exponential samples are non-negative and roughly follow the mean.
	s, err := NewShareStore(":memory:", 10*time.Second, nil)
	require.NoError(t, err)
	defer s.Close()

	const n = 1000
	var total time.Duration
	for range n {
		d := s.exponentialSample()
		assert.GreaterOrEqual(t, d, time.Duration(0), "delay must be non-negative")
		total += d
	}
	// Mean should be roughly 10s. Allow wide range: 5s to 20s.
	mean := total / n
	assert.Greater(t, mean, 5*time.Second, "mean delay should be > 5s")
	assert.Less(t, mean, 20*time.Second, "mean delay should be < 20s")
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

	// First call should fetch from chain.
	vet := s.getVoteEndTime("round1")
	assert.Equal(t, uint64(1000000), vet)
	assert.Equal(t, 1, fetchCalls)

	// Second call should hit cache, no additional fetch.
	vet = s.getVoteEndTime("round1")
	assert.Equal(t, uint64(1000000), vet)
	assert.Equal(t, 1, fetchCalls)
}

func TestGetVoteEndTime_NilFetcher(t *testing.T) {
	s, err := NewShareStore(":memory:", 0, nil)
	require.NoError(t, err)
	defer s.Close()

	// With nil fetcher and no cache, should return 0.
	vet := s.getVoteEndTime("round1")
	assert.Equal(t, uint64(0), vet)
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
	s, err := NewShareStore(dbPath, 0, nil)
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
