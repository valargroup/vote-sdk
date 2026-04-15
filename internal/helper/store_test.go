package helper

import (
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	// testVoteEndOffset is how far in the future the test vote end time is (12 hours).
	testVoteEndOffset = 12 * 3600
	// oneHourSecs is one hour in seconds.
	oneHourSecs = 3600
)

func newTestStore(t *testing.T) *ShareStore {
	t.Helper()
	// Provide a permissive round fetcher so tests don't fail on unknown rounds.
	// Return voteEndTime 12h from now.
	now := uint64(time.Now().Unix())
	fetcher := func(roundID string) (uint64, error) {
		return now + testVoteEndOffset, nil
	}
	s, err := NewShareStore(":memory:", fetcher)
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
		TreePosition: 0,
		VoteRoundID:  roundID,
		ShareComms:   comms,
		PrimaryBlind: zeroB64,
		SubmitAt:     0, // immediate
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

	// With submit_at=0, share should be immediately ready.
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

	// Witness data must be scrubbed from the row after submission.
	var c1, c2, comms, blind string
	err := s.db.QueryRow(
		"SELECT enc_share_c1, enc_share_c2, share_comms, primary_blind FROM shares WHERE round_id = ? AND share_index = ? AND proposal_id = ? AND tree_position = ?",
		"round1", 0, 1, 0,
	).Scan(&c1, &c2, &comms, &blind)
	require.NoError(t, err)
	assert.Empty(t, c1, "enc_share_c1 should be cleared")
	assert.Empty(t, c2, "enc_share_c2 should be cleared")
	assert.Equal(t, "[]", comms, "share_comms should be reset to empty array")
	assert.Empty(t, blind, "primary_blind should be cleared")
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

	// Witness data must be scrubbed after permanent failure.
	var c1, c2, comms, blind string
	err := s.db.QueryRow(
		"SELECT enc_share_c1, enc_share_c2, share_comms, primary_blind FROM shares WHERE round_id = ? AND share_index = ? AND proposal_id = ? AND tree_position = ?",
		"round1", 0, 1, 0,
	).Scan(&c1, &c2, &comms, &blind)
	require.NoError(t, err)
	assert.Empty(t, c1, "enc_share_c1 should be cleared after permanent failure")
	assert.Empty(t, c2, "enc_share_c2 should be cleared after permanent failure")
	assert.Equal(t, "[]", comms, "share_comms should be reset after permanent failure")
	assert.Empty(t, blind, "primary_blind should be cleared after permanent failure")
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
	now := uint64(time.Now().Unix())
	fetcher := func(roundID string) (uint64, error) { return now + 12*3600, nil }

	s1, err := NewShareStore(dbPath, fetcher)
	require.NoError(t, err)

	enqueueAndRequireInserted(t, s1, testPayload("round1", 0))

	// Take the share (moves to Witnessed state).
	ready := s1.TakeReady()
	require.Len(t, ready, 1)

	// Close without marking submitted (simulates crash).
	s1.Close()

	// Reopen: recovery should reset Witnessed → Received with same submit_at.
	s2, err := NewShareStore(dbPath, fetcher)
	require.NoError(t, err)
	defer s2.Close()

	ready = s2.TakeReady()
	assert.Len(t, ready, 1, "recovered share should be ready again")
}

func TestRecovery_FutureSubmitAt(t *testing.T) {
	// Shares with future submit_at should not be immediately ready after recovery.
	dbPath := t.TempDir() + "/helper_test.db"
	futureTime := uint64(time.Now().Add(time.Hour).Unix())
	fetcher := func(roundID string) (uint64, error) { return futureTime + oneHourSecs, nil }

	s1, err := NewShareStore(dbPath, fetcher)
	require.NoError(t, err)

	p := testPayload("round1", 0)
	p.SubmitAt = futureTime
	enqueueAndRequireInserted(t, s1, p)

	s1.Close()

	// Reopen: share should not be immediately ready (submit_at is in the future).
	s2, err := NewShareStore(dbPath, fetcher)
	require.NoError(t, err)
	defer s2.Close()

	ready := s2.TakeReady()
	assert.Empty(t, ready, "share with future submit_at should not be ready")
}

func TestEnqueue_SubmitAtValidation(t *testing.T) {
	now := uint64(time.Now().Unix())
	voteEndTime := now + oneHourSecs
	fetcher := func(roundID string) (uint64, error) { return voteEndTime, nil }

	s, err := NewShareStore(":memory:", fetcher)
	require.NoError(t, err)
	defer s.Close()

	t.Run("submit_at after vote_end_time rejected", func(t *testing.T) {
		p := testPayload("round1", 0)
		p.SubmitAt = voteEndTime + 100
		_, err := s.Enqueue(p)
		assert.ErrorIs(t, err, ErrInvalidSubmitAt)
	})

	t.Run("submit_at=0 accepted (immediate)", func(t *testing.T) {
		p := testPayload("round3", 0)
		p.SubmitAt = 0
		result, err := s.Enqueue(p)
		require.NoError(t, err)
		assert.Equal(t, EnqueueInserted, result)
	})

	t.Run("valid future submit_at accepted", func(t *testing.T) {
		p := testPayload("round4", 0)
		p.SubmitAt = now + 1800 // 30min from now
		result, err := s.Enqueue(p)
		require.NoError(t, err)
		assert.Equal(t, EnqueueInserted, result)
	})
}

func TestPurgeExpiredRounds(t *testing.T) {
	fetcher := func(roundID string) (uint64, error) {
		if roundID == "expired_round" {
			return uint64(time.Now().Add(-time.Hour).Unix()), nil
		}
		return uint64(time.Now().Add(time.Hour).Unix()), nil
	}

	s, err := NewShareStore(":memory:", fetcher)
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

func TestGetRoundEndTime_Cache(t *testing.T) {
	fetchCalls := 0
	fetcher := func(roundID string) (uint64, error) {
		fetchCalls++
		return 1000000, nil
	}

	s, err := NewShareStore(":memory:", fetcher)
	require.NoError(t, err)
	defer s.Close()

	// First call should fetch from keeper.
	vet, err := s.getRoundEndTime("round1")
	require.NoError(t, err)
	assert.Equal(t, uint64(1000000), vet)
	assert.Equal(t, 1, fetchCalls)

	// Second call should hit cache, no additional fetch.
	vet, err = s.getRoundEndTime("round1")
	require.NoError(t, err)
	assert.Equal(t, uint64(1000000), vet)
	assert.Equal(t, 1, fetchCalls)
}

func TestGetRoundEndTime_NilFetcher(t *testing.T) {
	s, err := NewShareStore(":memory:", nil)
	require.NoError(t, err)
	defer s.Close()

	// With nil fetcher and no cache, should return ErrUnknownRound.
	_, err = s.getRoundEndTime("round1")
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

	// Opening with current code should migrate PK and add vote_end_time + submit_at.
	now := uint64(time.Now().Unix())
	fetcher := func(roundID string) (uint64, error) { return now + 12*3600, nil }
	s, err := NewShareStore(dbPath, fetcher)
	require.NoError(t, err)
	defer s.Close()

	// vote_end_time column should now exist.
	hasVoteEndTime, err := tableHasColumn(s.db, "shares", "vote_end_time")
	require.NoError(t, err)
	assert.True(t, hasVoteEndTime)

	// submit_at column should now exist.
	hasSubmitAt, err := tableHasColumn(s.db, "shares", "submit_at")
	require.NoError(t, err)
	assert.True(t, hasSubmitAt)

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
