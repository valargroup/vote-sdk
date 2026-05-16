package helper

import (
	"database/sql"
	"strings"
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
	fetcher := func(roundID string) (RoundInfo, error) {
		return RoundInfo{CreatedAtTime: now, VoteEndTime: now + testVoteEndOffset}, nil
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

func requireScheduleChanged(t *testing.T, s *ShareStore) {
	t.Helper()
	select {
	case <-s.ScheduleChanged():
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timed out waiting for schedule change notification")
	}
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

func TestQueueSummaryBucketPolicy(t *testing.T) {
	assert.Equal(t, uint64(6*3600), queueSummaryPolicyBucketSeconds(28*24*3600))
	assert.Equal(t, uint64(3*3600), queueSummaryPolicyBucketSeconds(14*24*3600))
	assert.Equal(t, uint64(3600), queueSummaryPolicyBucketSeconds(2*24*3600))
	assert.Equal(t, uint64(15*60), queueSummaryPolicyBucketSeconds(2*3600))
	assert.Equal(t, uint64(60), queueSummaryPolicyBucketSeconds(10*60))
}

func TestQueueSummaryLastMinuteStartPolicy(t *testing.T) {
	start := uint64(1700000000)
	assert.Equal(t, start+6*60, queueSummaryLastMinuteStart(start, start+10*60))
	assert.Equal(t, start+36*60, queueSummaryLastMinuteStart(start, start+60*60))
	assert.Equal(t, start+2*3600-72, queueSummaryLastMinuteStart(start, start+2*3600))
	assert.Equal(t, start, queueSummaryLastMinuteStart(start, start))
}

func TestQueueSummaryAggregatesStatesByBucket(t *testing.T) {
	const roundID = "1111111111111111111111111111111111111111111111111111111111111111"
	start := uint64(1700000000)
	end := start + 2*24*3600
	now := time.Unix(int64(start+2*3600+30*60), 0)

	fetcher := func(roundID string) (RoundInfo, error) {
		return RoundInfo{CreatedAtTime: start, VoteEndTime: end}, nil
	}
	s, err := NewShareStore(":memory:", fetcher)
	require.NoError(t, err)
	defer s.Close()

	insert := func(shareIndex uint32, treePosition, submitAt uint64) {
		p := testPayload(roundID, shareIndex)
		p.TreePosition = treePosition
		p.SubmitAt = submitAt
		enqueueAndRequireInserted(t, s, p)
	}
	insert(0, 0, start+30*60)
	insert(1, 1, start+90*60)
	insert(2, 2, start+4*3600)
	insert(3, 3, start+5*3600)
	insert(4, 4, 0)
	insert(5, 5, start+3600)

	_, err = s.db.Exec(
		`UPDATE shares
		    SET state = 2, enc_share_c1 = '', enc_share_c2 = '', share_comms = '[]', primary_blind = ''
		  WHERE share_index = 0`,
	)
	require.NoError(t, err)
	_, err = s.db.Exec("UPDATE shares SET state = 1 WHERE share_index = 1")
	require.NoError(t, err)
	_, err = s.db.Exec(
		`UPDATE shares
		    SET state = 3, enc_share_c1 = '', enc_share_c2 = '', share_comms = '[]', primary_blind = ''
		  WHERE share_index = 3`,
	)
	require.NoError(t, err)
	_, err = s.db.Exec("UPDATE shares SET state = 2, received_at = ? WHERE share_index = 4", start+2*3600)
	require.NoError(t, err)

	summary, err := s.QueueSummary(roundID, now)
	require.NoError(t, err)
	require.Len(t, summary.Buckets, 48)
	assert.Equal(t, uint64(3600), summary.BucketSeconds)
	assert.Equal(t, start, summary.CreatedAtTime)
	assert.Equal(t, end, summary.VoteEndTime)
	assert.Equal(t, uint64(now.Unix()), summary.GeneratedAt)

	assert.Equal(t, 1, summary.Buckets[0].Submitted)
	assert.Equal(t, 1, summary.Buckets[1].Processing)
	assert.Equal(t, 1, summary.Buckets[1].OverduePending)
	assert.Equal(t, 1, summary.Buckets[2].Processing)
	assert.Equal(t, 1, summary.Buckets[4].PendingFuture)
	assert.Equal(t, 1, summary.Buckets[5].Failed)

	total := 0
	for _, bucket := range summary.Buckets {
		total += bucket.Total
	}
	assert.Equal(t, 6, total)
}

func TestQueueSummaryMasksOpenBuckets(t *testing.T) {
	const roundID = "2222222222222222222222222222222222222222222222222222222222222222"
	start := uint64(1700000000)
	end := start + 10*60

	fetcher := func(roundID string) (RoundInfo, error) {
		return RoundInfo{CreatedAtTime: start, VoteEndTime: end}, nil
	}
	s, err := NewShareStore(":memory:", fetcher)
	require.NoError(t, err)
	defer s.Close()

	insert := func(shareIndex uint32, submitAt uint64) {
		p := testPayload(roundID, shareIndex)
		p.TreePosition = uint64(shareIndex)
		p.SubmitAt = submitAt
		enqueueAndRequireInserted(t, s, p)
	}
	setState := func(shareIndex uint32, state ShareState) {
		_, err := s.db.Exec("UPDATE shares SET state = ? WHERE share_index = ?", int(state), shareIndex)
		require.NoError(t, err)
	}

	insert(0, start+130)
	insert(1, start+140)
	insert(2, start+150)
	insert(3, start+170)
	insert(4, start+160)
	insert(5, start+250)

	setState(0, ShareStateSubmitted)
	setState(1, ShareStateWitnessed)
	setState(4, ShareStateFailed)
	setState(5, ShareStateSubmitted)

	current, err := s.QueueSummary(roundID, time.Unix(int64(start+150), 0))
	require.NoError(t, err)
	require.Len(t, current.Buckets, 10)
	assert.Equal(t, uint64(60), current.BucketSeconds)

	currentBucket := current.Buckets[2]
	assert.Equal(t, 0, currentBucket.Submitted)
	assert.Equal(t, 0, currentBucket.PendingFuture)
	assert.Equal(t, 0, currentBucket.OverduePending)
	assert.Equal(t, 4, currentBucket.Processing)
	assert.Equal(t, 1, currentBucket.Failed)
	assert.Equal(t, 5, currentBucket.Total)

	futureBucket := current.Buckets[4]
	assert.Equal(t, 0, futureBucket.Submitted)
	assert.Equal(t, 1, futureBucket.PendingFuture)
	assert.Equal(t, 1, futureBucket.Total)

	afterCurrent, err := s.QueueSummary(roundID, time.Unix(int64(start+181), 0))
	require.NoError(t, err)
	elapsedBucket := afterCurrent.Buckets[2]
	assert.Equal(t, 1, elapsedBucket.Submitted)
	assert.Equal(t, 0, elapsedBucket.PendingFuture)
	assert.Equal(t, 2, elapsedBucket.OverduePending)
	assert.Equal(t, 1, elapsedBucket.Processing)
	assert.Equal(t, 1, elapsedBucket.Failed)
	assert.Equal(t, 5, elapsedBucket.Total)

	afterFuture, err := s.QueueSummary(roundID, time.Unix(int64(start+301), 0))
	require.NoError(t, err)
	assert.Equal(t, 1, afterFuture.Buckets[4].Submitted)
	assert.Equal(t, 0, afterFuture.Buckets[4].PendingFuture)
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
	fetcher := func(roundID string) (RoundInfo, error) {
		return RoundInfo{CreatedAtTime: now, VoteEndTime: now + 12*3600}, nil
	}

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
	fetcher := func(roundID string) (RoundInfo, error) {
		return RoundInfo{CreatedAtTime: futureTime - oneHourSecs, VoteEndTime: futureTime + oneHourSecs}, nil
	}

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
	fetcher := func(roundID string) (RoundInfo, error) {
		return RoundInfo{CreatedAtTime: now, VoteEndTime: voteEndTime}, nil
	}

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
	fetcher := func(roundID string) (RoundInfo, error) {
		if roundID == "expired_round" {
			end := uint64(time.Now().Add(-time.Hour).Unix())
			return RoundInfo{CreatedAtTime: end - oneHourSecs, VoteEndTime: end}, nil
		}
		now := uint64(time.Now().Unix())
		return RoundInfo{CreatedAtTime: now - oneHourSecs, VoteEndTime: now + oneHourSecs}, nil
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

func TestExpiredRoundSummaries(t *testing.T) {
	fetcher := func(roundID string) (RoundInfo, error) {
		end := uint64(time.Now().Add(-time.Hour).Unix())
		return RoundInfo{CreatedAtTime: end - oneHourSecs, VoteEndTime: end}, nil
	}

	s, err := NewShareStore(":memory:", fetcher)
	require.NoError(t, err)
	defer s.Close()

	enqueueAndRequireInserted(t, s, testPayload("expired_round", 0))
	enqueueAndRequireInserted(t, s, testPayload("expired_round", 1))

	ready := s.TakeReady()
	require.Len(t, ready, 2)
	s.MarkSubmitted("expired_round", 0, 1, 0)
	s.MarkFailed("expired_round", 1, 1, 0)

	summaries, err := s.ExpiredRoundSummaries(time.Now())
	require.NoError(t, err)
	require.Len(t, summaries, 1)

	assert.Equal(t, "expired_round", summaries[0].RoundID)
	assert.Equal(t, 2, summaries[0].Total)
	assert.Equal(t, 1, summaries[0].Pending)
	assert.Equal(t, 1, summaries[0].Submitted)
	assert.Equal(t, 0, summaries[0].Failed)
	assert.Equal(t, 1, summaries[0].Unsubmitted())
}

func TestGetRoundEndTime_Cache(t *testing.T) {
	fetchCalls := 0
	fetcher := func(roundID string) (RoundInfo, error) {
		fetchCalls++
		return RoundInfo{CreatedAtTime: 990000, VoteEndTime: 1000000}, nil
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

func TestQueueSummaryRefreshesLegacyRoundCache(t *testing.T) {
	dbPath := t.TempDir() + "/legacy_round_cache.db"
	roundID := strings.Repeat("3", 64)
	start := uint64(1_700_000_000)
	end := start + oneHourSecs

	oldDB, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	_, err = oldDB.Exec(`
		CREATE TABLE rounds (
			round_id       TEXT PRIMARY KEY,
			vote_end_time  INTEGER NOT NULL
		)
	`)
	require.NoError(t, err)
	_, err = oldDB.Exec("INSERT INTO rounds (round_id, vote_end_time) VALUES (?, ?)", roundID, end)
	require.NoError(t, err)
	require.NoError(t, oldDB.Close())

	fetchCalls := 0
	fetcher := func(gotRoundID string) (RoundInfo, error) {
		fetchCalls++
		require.Equal(t, roundID, gotRoundID)
		return RoundInfo{CreatedAtTime: start, VoteEndTime: end}, nil
	}

	s, err := NewShareStore(dbPath, fetcher)
	require.NoError(t, err)
	defer s.Close()

	summary, err := s.QueueSummary(roundID, time.Unix(int64(start+60), 0))
	require.NoError(t, err)
	assert.Equal(t, start, summary.CreatedAtTime)
	assert.Equal(t, end, summary.VoteEndTime)
	assert.Equal(t, 1, fetchCalls)

	var cachedCreatedAt uint64
	err = s.db.QueryRow("SELECT created_at_time FROM rounds WHERE round_id = ?", roundID).Scan(&cachedCreatedAt)
	require.NoError(t, err)
	assert.Equal(t, start, cachedCreatedAt)

	_, err = s.QueueSummary(roundID, time.Unix(int64(start+120), 0))
	require.NoError(t, err)
	assert.Equal(t, 1, fetchCalls, "refreshed metadata should stay cached")
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

	// Opening with current code should migrate PK and add queue metadata columns.
	now := uint64(time.Now().Unix())
	fetcher := func(roundID string) (RoundInfo, error) {
		return RoundInfo{CreatedAtTime: now, VoteEndTime: now + 12*3600}, nil
	}
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

	hasReceivedAt, err := tableHasColumn(s.db, "shares", "received_at")
	require.NoError(t, err)
	assert.True(t, hasReceivedAt)

	hasRoundCreatedAtTime, err := tableHasColumn(s.db, "rounds", "created_at_time")
	require.NoError(t, err)
	assert.True(t, hasRoundCreatedAtTime)

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

func TestNextScheduledTimeEmptyAndReadyRemoval(t *testing.T) {
	s := newTestStore(t)

	_, ok := s.NextScheduledTime()
	assert.False(t, ok)

	enqueueAndRequireInserted(t, s, testPayload("round1", 0))
	requireScheduleChanged(t, s)

	next, ok := s.NextScheduledTime()
	require.True(t, ok)
	assert.False(t, next.After(time.Now()))

	ready := s.TakeReady()
	require.Len(t, ready, 1)

	_, ok = s.NextScheduledTime()
	assert.False(t, ok)
}

func TestNextScheduledTimeReturnsEarliest(t *testing.T) {
	s := newTestStore(t)

	now := uint64(time.Now().Unix())
	later := testPayload("later", 0)
	later.SubmitAt = now + 180
	enqueueAndRequireInserted(t, s, later)
	requireScheduleChanged(t, s)

	earlier := testPayload("earlier", 0)
	earlier.SubmitAt = now + 60
	enqueueAndRequireInserted(t, s, earlier)
	requireScheduleChanged(t, s)

	next, ok := s.NextScheduledTime()
	require.True(t, ok)
	assert.Equal(t, int64(earlier.SubmitAt), next.Unix())
}

func TestScheduleChangedOnRetryScheduling(t *testing.T) {
	s := newTestStore(t)

	enqueueAndRequireInserted(t, s, testPayload("round1", 0))
	requireScheduleChanged(t, s)

	ready := s.TakeReady()
	require.Len(t, ready, 1)

	s.MarkFailed("round1", 0, 1, 0)
	requireScheduleChanged(t, s)

	next, ok := s.NextScheduledTime()
	require.True(t, ok)
	assert.True(t, next.After(time.Now()))
}
