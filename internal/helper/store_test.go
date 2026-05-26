package helper

import (
	"database/sql"
	"path/filepath"
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

func expireRoundRowsForTest(t *testing.T, s *ShareStore, roundID string, end uint64) {
	t.Helper()
	_, err := s.db.Exec(
		`UPDATE shares
		    SET vote_end_time = ?
		  WHERE round_id = ?`,
		end,
		roundID,
	)
	require.NoError(t, err)
	_, err = s.db.Exec(
		`INSERT INTO rounds (round_id, vote_end_time, created_at_time, closed_at)
		 VALUES (?, ?, ?, 0)
		 ON CONFLICT(round_id) DO UPDATE SET
		   vote_end_time = excluded.vote_end_time,
		   created_at_time = excluded.created_at_time,
		   closed_at = 0`,
		roundID,
		end,
		end-oneHourSecs,
	)
	require.NoError(t, err)
	s.roundCache[roundID] = RoundInfo{CreatedAtTime: end - oneHourSecs, VoteEndTime: end}
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

func TestDefaultConfigCompletedRoundDataServeSeconds(t *testing.T) {
	assert.Equal(t, DefaultCompletedRoundDataServeSeconds, DefaultConfig().CompletedRoundDataServeSeconds)
	assert.Equal(t, DefaultCompletedRoundDataServeSeconds, NormalizeCompletedRoundDataServeSeconds(-2))
	assert.Equal(t, int64(-1), NormalizeCompletedRoundDataServeSeconds(-1))
	assert.Equal(t, int64(0), NormalizeCompletedRoundDataServeSeconds(0))
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
	knownNullifier := make([]byte, 32)
	knownNullifier[0] = 0xAB
	knownNullifier[1] = 0xCD
	s.MarkFailed("round1", 0, 1, 0, knownNullifier) // 5th attempt = permanent failure

	// Now it should be permanently failed.
	status := s.Status()
	assert.Equal(t, 1, status["round1"].Failed)
	assert.Equal(t, 0, status["round1"].Pending)

	// Witness data must be scrubbed after permanent failure.
	var c1, c2, comms, blind, shareNullifier string
	err := s.db.QueryRow(
		"SELECT enc_share_c1, enc_share_c2, share_comms, primary_blind, share_nullifier FROM shares WHERE round_id = ? AND share_index = ? AND proposal_id = ? AND tree_position = ?",
		"round1", 0, 1, 0,
	).Scan(&c1, &c2, &comms, &blind, &shareNullifier)
	require.NoError(t, err)
	assert.Empty(t, c1, "enc_share_c1 should be cleared after permanent failure")
	assert.Empty(t, c2, "enc_share_c2 should be cleared after permanent failure")
	assert.Equal(t, "[]", comms, "share_comms should be reset after permanent failure")
	assert.Empty(t, blind, "primary_blind should be cleared after permanent failure")
	assert.Equal(t, "abcd"+strings.Repeat("0", 60), shareNullifier)
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

func TestQueueSummaryRejectsTooManyBuckets(t *testing.T) {
	const roundID = "4444444444444444444444444444444444444444444444444444444444444444"
	start := uint64(1700000000)
	end := start + uint64(maxQueueSummaryBuckets+1)*6*queueSummaryHour

	fetcher := func(roundID string) (RoundInfo, error) {
		return RoundInfo{CreatedAtTime: start, VoteEndTime: end}, nil
	}
	s, err := NewShareStore(":memory:", fetcher)
	require.NoError(t, err)
	defer s.Close()

	_, err = s.QueueSummary(roundID, time.Unix(int64(start), 0), DefaultCompletedRoundDataServeSeconds)
	require.ErrorIs(t, err, ErrInvalidRoundInfo)
}

func TestQueueSummaryLastMinuteStartPolicy(t *testing.T) {
	start := uint64(1700000000)
	assert.Equal(t, start+6*60, queueSummaryLastMinuteStart(start, start+10*60))
	assert.Equal(t, start+36*60, queueSummaryLastMinuteStart(start, start+60*60))
	assert.Equal(t, start+2*3600-48*60, queueSummaryLastMinuteStart(start, start+2*3600))
	assert.Equal(t, start+7*24*3600-6*3600, queueSummaryLastMinuteStart(start, start+7*24*3600))
	assert.Equal(t, start, queueSummaryLastMinuteStart(start, start))
}

func TestQueueSummaryAggregatesStatesByBucket(t *testing.T) {
	const roundID = "1111111111111111111111111111111111111111111111111111111111111111"
	start := uint64(time.Now().Add(time.Hour).Unix())
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

	summary, err := s.QueueSummary(roundID, now, DefaultCompletedRoundDataServeSeconds)
	require.NoError(t, err)
	require.Len(t, summary.Buckets, 48)
	assert.Equal(t, uint64(3600), summary.BucketSeconds)
	assert.Equal(t, start, summary.CreatedAtTime)
	assert.Equal(t, end, summary.VoteEndTime)
	assert.Equal(t, uint64(now.Unix()), summary.GeneratedAt)

	assert.Equal(t, 1, summary.Buckets[0].Submitted)
	assert.Equal(t, 1, summary.Buckets[1].Processing)
	assert.Equal(t, 1, summary.Buckets[1].OverduePending)
	assert.Equal(t, 1, summary.Buckets[2].Submitted)
	assert.Equal(t, 1, summary.Buckets[4].PendingFuture)
	assert.Equal(t, 1, summary.Buckets[5].Failed)

	total := 0
	for _, bucket := range summary.Buckets {
		total += bucket.Total
	}
	assert.Equal(t, 6, total)
}

func TestQueueSummaryReportsCurrentBucketStates(t *testing.T) {
	const roundID = "2222222222222222222222222222222222222222222222222222222222222222"
	start := uint64(time.Now().Add(time.Hour).Unix())
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
	insert(6, start+155)
	insert(7, start+158)

	setState(0, ShareStateSubmitted)
	setState(1, ShareStateWitnessed)
	setState(4, ShareStateFailed)
	setState(5, ShareStateSubmitted)
	setState(6, ShareStateObservedOnChain)
	setState(7, ShareStateMissedDeadline)

	current, err := s.QueueSummary(roundID, time.Unix(int64(start+150), 0), DefaultCompletedRoundDataServeSeconds)
	require.NoError(t, err)
	require.Len(t, current.Buckets, 10)
	assert.Equal(t, uint64(60), current.BucketSeconds)

	currentBucket := current.Buckets[2]
	assert.Equal(t, 1, currentBucket.Submitted)
	assert.Equal(t, 1, currentBucket.ObservedOnChain)
	assert.Equal(t, 1, currentBucket.PendingFuture)
	assert.Equal(t, 1, currentBucket.OverduePending)
	assert.Equal(t, 1, currentBucket.Processing)
	assert.Equal(t, 1, currentBucket.Failed)
	assert.Equal(t, 1, currentBucket.MissedDeadline)
	assert.Equal(t, 7, currentBucket.Total)

	futureBucket := current.Buckets[4]
	assert.Equal(t, 1, futureBucket.Submitted)
	assert.Equal(t, 0, futureBucket.PendingFuture)
	assert.Equal(t, 1, futureBucket.Total)

	afterCurrent, err := s.QueueSummary(roundID, time.Unix(int64(start+181), 0), DefaultCompletedRoundDataServeSeconds)
	require.NoError(t, err)
	elapsedBucket := afterCurrent.Buckets[2]
	assert.Equal(t, 1, elapsedBucket.Submitted)
	assert.Equal(t, 1, elapsedBucket.ObservedOnChain)
	assert.Equal(t, 0, elapsedBucket.PendingFuture)
	assert.Equal(t, 2, elapsedBucket.OverduePending)
	assert.Equal(t, 1, elapsedBucket.Processing)
	assert.Equal(t, 1, elapsedBucket.Failed)
	assert.Equal(t, 1, elapsedBucket.MissedDeadline)
	assert.Equal(t, 7, elapsedBucket.Total)

	afterFuture, err := s.QueueSummary(roundID, time.Unix(int64(start+301), 0), DefaultCompletedRoundDataServeSeconds)
	require.NoError(t, err)
	assert.Equal(t, 1, afterFuture.Buckets[4].Submitted)
	assert.Equal(t, 0, afterFuture.Buckets[4].PendingFuture)
}

func TestQueueSummaryCompletedRoundServeWindow(t *testing.T) {
	const roundID = "3333333333333333333333333333333333333333333333333333333333333333"
	start := uint64(1700000000)
	end := start + oneHourSecs

	fetcher := func(roundID string) (RoundInfo, error) {
		return RoundInfo{CreatedAtTime: start, VoteEndTime: end}, nil
	}
	s, err := NewShareStore(":memory:", fetcher)
	require.NoError(t, err)
	defer s.Close()

	_, err = s.QueueSummary(roundID, time.Unix(int64(end+1), 0), DefaultCompletedRoundDataServeSeconds)
	require.NoError(t, err)

	_, err = s.QueueSummary(roundID, time.Unix(int64(end+uint64(DefaultCompletedRoundDataServeSeconds)+1), 0), DefaultCompletedRoundDataServeSeconds)
	require.ErrorIs(t, err, ErrCompletedRoundDataExpired)

	_, err = s.QueueSummary(roundID, time.Unix(int64(end+uint64(DefaultCompletedRoundDataServeSeconds)+1), 0), -1)
	require.NoError(t, err)

	_, err = s.QueueSummary(roundID, time.Unix(int64(end+uint64(DefaultCompletedRoundDataServeSeconds)+1), 0), -2)
	require.ErrorIs(t, err, ErrCompletedRoundDataExpired)

	_, err = s.QueueSummary(roundID, time.Unix(int64(end+1), 0), 0)
	require.ErrorIs(t, err, ErrCompletedRoundDataExpired)
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

func TestEnqueue_RoundClosedRejected(t *testing.T) {
	now := uint64(time.Now().Unix())
	fetcher := func(roundID string) (RoundInfo, error) {
		return RoundInfo{CreatedAtTime: now - 2*oneHourSecs, VoteEndTime: now - oneHourSecs}, nil
	}

	s, err := NewShareStore(":memory:", fetcher)
	require.NoError(t, err)
	defer s.Close()

	_, err = s.Enqueue(testPayload("closed_round", 0))
	require.ErrorIs(t, err, ErrRoundClosed)
}

func TestCloseoutShareRetainsRowsAndScrubsWitness(t *testing.T) {
	s := newTestStore(t)

	roundID := "expired_round"
	enqueueAndRequireInserted(t, s, testPayload(roundID, 0))
	end := uint64(time.Now().Add(-time.Hour).Unix())
	expireRoundRowsForTest(t, s, roundID, end)

	roundIDs, err := s.ExpiredRoundIDsForCloseout(time.Now())
	require.NoError(t, err)
	require.Equal(t, []string{roundID}, roundIDs)

	processable, err := s.ProcessableSharesForRound(roundID)
	require.NoError(t, err)
	require.Len(t, processable, 1)

	nullifier := []byte{0xAA, 0xBB}
	closedAt := uint64(time.Now().Unix())
	require.NoError(t, s.CloseoutShare(roundID, 0, 1, 0, ShareStateMissedDeadline, nullifier, closedAt))
	require.NoError(t, s.MarkRoundClosed(roundID, closedAt))

	status := s.Status()
	assert.Equal(t, 1, status[roundID].Total)
	assert.Equal(t, 0, status[roundID].Pending)
	assert.Equal(t, 1, status[roundID].MissedDeadline)

	var state int
	var c1, c2, comms, blind, shareNullifier string
	var gotClosedAt uint64
	err = s.db.QueryRow(
		`SELECT state, enc_share_c1, enc_share_c2, share_comms, primary_blind, share_nullifier, closed_at
		   FROM shares
		  WHERE round_id = ? AND share_index = ? AND proposal_id = ? AND tree_position = ?`,
		roundID, 0, 1, 0,
	).Scan(&state, &c1, &c2, &comms, &blind, &shareNullifier, &gotClosedAt)
	require.NoError(t, err)
	assert.Equal(t, int(ShareStateMissedDeadline), state)
	assert.Empty(t, c1)
	assert.Empty(t, c2)
	assert.Equal(t, "[]", comms)
	assert.Empty(t, blind)
	assert.Equal(t, "aabb", shareNullifier)
	assert.Equal(t, closedAt, gotClosedAt)

	roundIDs, err = s.ExpiredRoundIDsForCloseout(time.Now())
	require.NoError(t, err)
	assert.Empty(t, roundIDs)
}

func TestExpiredRoundSummaries(t *testing.T) {
	fetcher := func(roundID string) (RoundInfo, error) {
		now := uint64(time.Now().Unix())
		return RoundInfo{CreatedAtTime: now - oneHourSecs, VoteEndTime: now + oneHourSecs}, nil
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
	end := uint64(time.Now().Add(-time.Hour).Unix())
	expireRoundRowsForTest(t, s, "expired_round", end)

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

	summary, err := s.QueueSummary(roundID, time.Unix(int64(start+60), 0), DefaultCompletedRoundDataServeSeconds)
	require.NoError(t, err)
	assert.Equal(t, start, summary.CreatedAtTime)
	assert.Equal(t, end, summary.VoteEndTime)
	assert.Equal(t, 1, fetchCalls)

	var cachedCreatedAt uint64
	err = s.db.QueryRow("SELECT created_at_time FROM rounds WHERE round_id = ?", roundID).Scan(&cachedCreatedAt)
	require.NoError(t, err)
	assert.Equal(t, start, cachedCreatedAt)

	_, err = s.QueueSummary(roundID, time.Unix(int64(start+120), 0), DefaultCompletedRoundDataServeSeconds)
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

	hasShareNullifier, err := tableHasColumn(s.db, "shares", "share_nullifier")
	require.NoError(t, err)
	assert.True(t, hasShareNullifier)

	hasClosedAt, err := tableHasColumn(s.db, "shares", "closed_at")
	require.NoError(t, err)
	assert.True(t, hasClosedAt)

	hasOriginalSubmitAt, err := tableHasColumn(s.db, "shares", "original_submit_at")
	require.NoError(t, err)
	assert.True(t, hasOriginalSubmitAt)

	hasRoundCreatedAtTime, err := tableHasColumn(s.db, "rounds", "created_at_time")
	require.NoError(t, err)
	assert.True(t, hasRoundCreatedAtTime)

	hasRoundClosedAt, err := tableHasColumn(s.db, "rounds", "closed_at")
	require.NoError(t, err)
	assert.True(t, hasRoundClosedAt)

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

	require.NoError(t, s.Close())
	reopened, err := NewShareStore(dbPath, fetcher)
	require.NoError(t, err)
	defer reopened.Close()
	status := reopened.Status()
	assert.Equal(t, 2, status["round1"].Pending)
}

func TestMigrateOldPKPreservesQueueMetadata(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "old_helper_metadata.db")

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
			share_comms     TEXT NOT NULL DEFAULT '[]',
			primary_blind   TEXT NOT NULL DEFAULT '',
			state           INTEGER NOT NULL DEFAULT 0,
			attempts        INTEGER NOT NULL DEFAULT 0,
			vote_end_time   INTEGER NOT NULL DEFAULT 0,
			submit_at       INTEGER NOT NULL DEFAULT 0,
			original_submit_at INTEGER NOT NULL DEFAULT 0,
			received_at     INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (round_id, share_index, proposal_id)
		)
	`)
	require.NoError(t, err)
	_, err = oldDB.Exec(
		`INSERT INTO shares (
			round_id, share_index, shares_hash, proposal_id, vote_decision,
			enc_share_c1, enc_share_c2, tree_position, share_comms, primary_blind,
			state, attempts, vote_end_time, submit_at, original_submit_at, received_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"round1", 7, "shares-hash", 11, 1,
		"c1", "c2", 99, `["comm"]`, "blind",
		int(ShareStateReceived), 3, uint64(2000), uint64(1500), uint64(1400), uint64(1300),
	)
	require.NoError(t, err)
	require.NoError(t, oldDB.Close())

	s, err := NewShareStore(dbPath, nil)
	require.NoError(t, err)
	defer s.Close()

	var row struct {
		sharesHash       string
		proposalID       uint32
		voteDecision     uint32
		c1               string
		c2               string
		shareComms       string
		primaryBlind     string
		state            int
		attempts         int
		voteEndTime      uint64
		submitAt         uint64
		originalSubmitAt uint64
		receivedAt       uint64
		shareNullifier   string
		closedAt         uint64
	}
	err = s.db.QueryRow(
		`SELECT shares_hash, proposal_id, vote_decision, enc_share_c1, enc_share_c2,
		        share_comms, primary_blind, state, attempts, vote_end_time,
		        submit_at, original_submit_at, received_at, share_nullifier, closed_at
		   FROM shares
		  WHERE round_id = ? AND share_index = ? AND proposal_id = ? AND tree_position = ?`,
		"round1", 7, 11, 99,
	).Scan(
		&row.sharesHash,
		&row.proposalID,
		&row.voteDecision,
		&row.c1,
		&row.c2,
		&row.shareComms,
		&row.primaryBlind,
		&row.state,
		&row.attempts,
		&row.voteEndTime,
		&row.submitAt,
		&row.originalSubmitAt,
		&row.receivedAt,
		&row.shareNullifier,
		&row.closedAt,
	)
	require.NoError(t, err)
	assert.Equal(t, "shares-hash", row.sharesHash)
	assert.Equal(t, uint32(11), row.proposalID)
	assert.Equal(t, uint32(1), row.voteDecision)
	assert.Equal(t, "c1", row.c1)
	assert.Equal(t, "c2", row.c2)
	assert.Equal(t, `["comm"]`, row.shareComms)
	assert.Equal(t, "blind", row.primaryBlind)
	assert.Equal(t, int(ShareStateReceived), row.state)
	assert.Equal(t, 3, row.attempts)
	assert.Equal(t, uint64(2000), row.voteEndTime)
	assert.Equal(t, uint64(1500), row.submitAt)
	assert.Equal(t, uint64(1400), row.originalSubmitAt)
	assert.Equal(t, uint64(1300), row.receivedAt)
	assert.Empty(t, row.shareNullifier)
	assert.Zero(t, row.closedAt)
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

func TestShareStoreExclusiveLock(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "helper.db")
	now := uint64(time.Now().Unix())
	fetcher := func(roundID string) (RoundInfo, error) {
		return RoundInfo{CreatedAtTime: now, VoteEndTime: now + testVoteEndOffset}, nil
	}

	s1, err := NewShareStore(dbPath, fetcher)
	require.NoError(t, err)
	defer s1.Close()

	_, err = NewShareStore(dbPath, fetcher)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already in use")
}

func TestExportQueueIncludesTerminalRows(t *testing.T) {
	s := newTestStore(t)

	pending := testPayload("round1", 0)
	pending.SubmitAt = uint64(time.Now().Add(time.Hour).Unix())
	enqueueAndRequireInserted(t, s, pending)

	submitted := testPayload("round1", 1)
	submitted.TreePosition = 11
	enqueueAndRequireInserted(t, s, submitted)
	ready := s.TakeReady()
	require.Len(t, ready, 1)
	s.MarkSubmitted("round1", 1, 1, 11)

	missed := testPayload("round1", 2)
	missed.TreePosition = 22
	enqueueAndRequireInserted(t, s, missed)
	require.NoError(t, s.CloseoutShare("round1", 2, 1, 22, ShareStateMissedDeadline, nil, 1200))

	observed := testPayload("round1", 3)
	observed.TreePosition = 33
	enqueueAndRequireInserted(t, s, observed)
	require.NoError(t, s.CloseoutShare("round1", 3, 1, 33, ShareStateObservedOnChain, []byte{0xAA, 0xBB}, 1300))

	export, err := s.ExportQueue("round1", time.Unix(1234, 0))
	require.NoError(t, err)
	require.Len(t, export.Rows, 4)
	assert.Equal(t, QueueExportVersion, export.Version)
	assert.Equal(t, uint64(1234), export.ExportedAt)

	var sawPending, sawSubmitted, sawMissed, sawObserved bool
	for _, row := range export.Rows {
		switch row.ShareIndex {
		case 0:
			sawPending = true
			assert.True(t, row.Processable)
			assert.Equal(t, ShareStateReceived, row.State)
			assert.NotEmpty(t, row.EncShare.C1)
			assert.Equal(t, pending.SubmitAt, row.OriginalSubmitAt)
		case 1:
			sawSubmitted = true
			assert.False(t, row.Processable)
			assert.Equal(t, ShareStateSubmitted, row.State)
			assert.Empty(t, row.EncShare.C1)
			assert.Empty(t, row.PrimaryBlind)
			assert.Empty(t, row.ShareComms)
		case 2:
			sawMissed = true
			assert.False(t, row.Processable)
			assert.Equal(t, ShareStateMissedDeadline, row.State)
			assert.Empty(t, row.EncShare.C1)
			assert.Empty(t, row.PrimaryBlind)
			assert.Empty(t, row.ShareNullifier)
			assert.Equal(t, uint64(1200), row.ClosedAt)
		case 3:
			sawObserved = true
			assert.False(t, row.Processable)
			assert.Equal(t, ShareStateObservedOnChain, row.State)
			assert.Empty(t, row.EncShare.C1)
			assert.Empty(t, row.PrimaryBlind)
			assert.Equal(t, "aabb", row.ShareNullifier)
			assert.Equal(t, uint64(1300), row.ClosedAt)
		}
	}
	assert.True(t, sawPending)
	assert.True(t, sawSubmitted)
	assert.True(t, sawMissed)
	assert.True(t, sawObserved)
}

func TestImportQueueSkipsTerminalAndRoundTripsProcessableRows(t *testing.T) {
	source := newTestStore(t)
	received := testPayload("round1", 0)
	enqueueAndRequireInserted(t, source, received)
	submitted := testPayload("round1", 1)
	submitted.TreePosition = 11
	enqueueAndRequireInserted(t, source, submitted)
	ready := source.TakeReady()
	require.Len(t, ready, 2)
	source.MarkSubmitted("round1", 1, 1, 11)
	source.MarkFailed("round1", 0, 1, 0)
	missed := testPayload("round1", 2)
	missed.TreePosition = 22
	enqueueAndRequireInserted(t, source, missed)
	require.NoError(t, source.CloseoutShare("round1", 2, 1, 22, ShareStateMissedDeadline, nil, 1200))
	observed := testPayload("round1", 3)
	observed.TreePosition = 33
	enqueueAndRequireInserted(t, source, observed)
	require.NoError(t, source.CloseoutShare("round1", 3, 1, 33, ShareStateObservedOnChain, []byte{0xAA, 0xBB}, 1300))

	export, err := source.ExportQueue("round1", time.Now())
	require.NoError(t, err)

	dest := newTestStore(t)
	result, err := dest.ImportQueue(export, QueueImportOptions{})
	require.NoError(t, err)
	assert.Equal(t, 1, result.Inserted)
	assert.Equal(t, 3, result.SkippedTerminal)

	status := dest.Status()
	assert.Equal(t, 1, status["round1"].Total)
	assert.Equal(t, 1, status["round1"].Pending)
	assert.Equal(t, 0, status["round1"].Submitted)

	ready = dest.TakeReady()
	require.Len(t, ready, 1)
	assert.Equal(t, uint32(0), ready[0].Payload.EncShare.ShareIndex)
	assert.Equal(t, received.PrimaryBlind, ready[0].Payload.PrimaryBlind)

	result, err = dest.ImportQueue(export, QueueImportOptions{})
	require.NoError(t, err)
	assert.Equal(t, 0, result.Inserted)
	assert.Equal(t, 1, result.Duplicates)
	assert.Equal(t, 3, result.SkippedTerminal)
}

func TestImportQueueRejectsUnsupportedVersion(t *testing.T) {
	dest := newTestStore(t)

	_, err := dest.ImportQueue(QueueExport{
		Version: QueueExportVersion + 1,
		RoundID: "round1",
	}, QueueImportOptions{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported queue export version")
}

func TestImportQueueRejectsMissingRoundID(t *testing.T) {
	dest := newTestStore(t)

	_, err := dest.ImportQueue(QueueExport{
		Version: QueueExportVersion,
	}, QueueImportOptions{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "queue export missing round_id")
}

func TestImportQueueRejectsSubmitAtAfterVoteEndTime(t *testing.T) {
	submitAt := uint64(time.Now().Add(2 * time.Hour).Unix())
	voteEndTime := submitAt - 60
	payload := testPayload("round1", 0)
	payload.SubmitAt = submitAt
	export := QueueExport{
		Version: QueueExportVersion,
		RoundID: "round1",
		Round: QueueExportRound{
			CreatedAtTime: voteEndTime - oneHourSecs,
			VoteEndTime:   voteEndTime,
		},
		Rows: []QueueExportRow{
			queueExportRowFromPayload(payload, ShareStateReceived, voteEndTime),
		},
	}

	dest := newTestStore(t)
	_, err := dest.ImportQueue(export, QueueImportOptions{})
	require.ErrorIs(t, err, ErrInvalidSubmitAt)
	assert.Contains(t, err.Error(), "imported submit_at")
}

func TestImportQueueReportsConflicts(t *testing.T) {
	dest := newTestStore(t)
	existing := testPayload("round1", 0)
	enqueueAndRequireInserted(t, dest, existing)

	incoming := existing
	incoming.PrimaryBlind = "AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE="
	export := QueueExport{
		Version: QueueExportVersion,
		RoundID: "round1",
		Rows: []QueueExportRow{
			queueExportRowFromPayload(incoming, ShareStateReceived, uint64(time.Now().Add(time.Hour).Unix())),
		},
	}

	result, err := dest.ImportQueue(export, QueueImportOptions{})
	require.NoError(t, err)
	assert.Equal(t, 0, result.Inserted)
	assert.Equal(t, 0, result.Duplicates)
	assert.Equal(t, 1, result.Conflicts)
	assert.Equal(t, 0, result.SkippedTerminal)
}

func TestImportQueueSkipsRoundMetadataForTerminalOnlyExport(t *testing.T) {
	dest := newTestStore(t)
	_, err := dest.db.Exec(
		"INSERT INTO rounds (round_id, vote_end_time, created_at_time) VALUES (?, ?, ?)",
		"round1", uint64(2000), uint64(1000),
	)
	require.NoError(t, err)
	dest.roundCache["round1"] = RoundInfo{CreatedAtTime: 1000, VoteEndTime: 2000}

	payload := testPayload("round1", 0)
	export := QueueExport{
		Version: QueueExportVersion,
		RoundID: "round1",
		Round: QueueExportRound{
			CreatedAtTime: 9000,
			VoteEndTime:   10000,
		},
		Rows: []QueueExportRow{
			queueExportRowFromPayload(payload, ShareStateSubmitted, 10000),
		},
	}

	result, err := dest.ImportQueue(export, QueueImportOptions{})
	require.NoError(t, err)
	assert.Equal(t, 0, result.Inserted)
	assert.Equal(t, 0, result.Duplicates)
	assert.Equal(t, 1, result.SkippedTerminal)

	var cachedCreatedAt, cachedVoteEndTime uint64
	err = dest.db.QueryRow(
		"SELECT created_at_time, vote_end_time FROM rounds WHERE round_id = ?",
		"round1",
	).Scan(&cachedCreatedAt, &cachedVoteEndTime)
	require.NoError(t, err)
	assert.Equal(t, uint64(1000), cachedCreatedAt)
	assert.Equal(t, uint64(2000), cachedVoteEndTime)
	assert.Equal(t, RoundInfo{CreatedAtTime: 1000, VoteEndTime: 2000}, dest.roundCache["round1"])
}

func TestImportQueueForceReadyPreservesOriginalSubmitAt(t *testing.T) {
	futureSubmitAt := uint64(time.Now().Add(time.Hour).Unix())
	export := QueueExport{
		Version: QueueExportVersion,
		RoundID: "round1",
		Round: QueueExportRound{
			CreatedAtTime: futureSubmitAt - oneHourSecs,
			VoteEndTime:   futureSubmitAt + oneHourSecs,
		},
		Rows: []QueueExportRow{
			{
				ShareIndex:       0,
				SharesHash:       testPayload("round1", 0).SharesHash,
				ProposalID:       1,
				VoteDecision:     0,
				EncShare:         testPayload("round1", 0).EncShare,
				TreePosition:     0,
				ShareComms:       testPayload("round1", 0).ShareComms,
				PrimaryBlind:     testPayload("round1", 0).PrimaryBlind,
				State:            ShareStateReceived,
				VoteEndTime:      futureSubmitAt + oneHourSecs,
				SubmitAt:         futureSubmitAt,
				OriginalSubmitAt: futureSubmitAt,
				Processable:      true,
			},
		},
	}

	dest := newTestStore(t)
	result, err := dest.ImportQueue(export, QueueImportOptions{ForceReady: true})
	require.NoError(t, err)
	assert.Equal(t, 1, result.Inserted)

	var submitAt, originalSubmitAt uint64
	err = dest.db.QueryRow(
		"SELECT submit_at, original_submit_at FROM shares WHERE round_id = ? AND share_index = ? AND proposal_id = ? AND tree_position = ?",
		"round1", 0, 1, 0,
	).Scan(&submitAt, &originalSubmitAt)
	require.NoError(t, err)
	assert.Equal(t, uint64(0), submitAt)
	assert.Equal(t, futureSubmitAt, originalSubmitAt)

	var cachedCreatedAt, cachedVoteEndTime uint64
	err = dest.db.QueryRow(
		"SELECT created_at_time, vote_end_time FROM rounds WHERE round_id = ?",
		"round1",
	).Scan(&cachedCreatedAt, &cachedVoteEndTime)
	require.NoError(t, err)
	assert.Equal(t, futureSubmitAt-oneHourSecs, cachedCreatedAt)
	assert.Equal(t, futureSubmitAt+oneHourSecs, cachedVoteEndTime)

	ready := dest.TakeReady()
	require.Len(t, ready, 1)
	assert.Equal(t, uint64(0), ready[0].Payload.SubmitAt)
}

func TestImportQueueForceReadyReschedulesDuplicate(t *testing.T) {
	futureSubmitAt := uint64(time.Now().Add(time.Hour).Unix())
	payload := testPayload("round1", 0)
	export := QueueExport{
		Version: QueueExportVersion,
		RoundID: "round1",
		Round: QueueExportRound{
			CreatedAtTime: futureSubmitAt - oneHourSecs,
			VoteEndTime:   futureSubmitAt + oneHourSecs,
		},
		Rows: []QueueExportRow{
			{
				ShareIndex:       payload.EncShare.ShareIndex,
				SharesHash:       payload.SharesHash,
				ProposalID:       payload.ProposalID,
				VoteDecision:     payload.VoteDecision,
				EncShare:         payload.EncShare,
				TreePosition:     payload.TreePosition,
				ShareComms:       payload.ShareComms,
				PrimaryBlind:     payload.PrimaryBlind,
				State:            ShareStateReceived,
				VoteEndTime:      futureSubmitAt + oneHourSecs,
				SubmitAt:         futureSubmitAt,
				OriginalSubmitAt: futureSubmitAt,
				Processable:      true,
			},
		},
	}

	dest := newTestStore(t)
	result, err := dest.ImportQueue(export, QueueImportOptions{})
	require.NoError(t, err)
	assert.Equal(t, 1, result.Inserted)
	assert.Empty(t, dest.TakeReady(), "normal import should respect the future submit_at")

	result, err = dest.ImportQueue(export, QueueImportOptions{ForceReady: true})
	require.NoError(t, err)
	assert.Equal(t, 0, result.Inserted)
	assert.Equal(t, 1, result.Duplicates)
	assert.Equal(t, 0, result.Conflicts)

	var submitAt, originalSubmitAt uint64
	err = dest.db.QueryRow(
		"SELECT submit_at, original_submit_at FROM shares WHERE round_id = ? AND share_index = ? AND proposal_id = ? AND tree_position = ?",
		"round1", 0, 1, 0,
	).Scan(&submitAt, &originalSubmitAt)
	require.NoError(t, err)
	assert.Equal(t, uint64(0), submitAt)
	assert.Equal(t, futureSubmitAt, originalSubmitAt)

	ready := dest.TakeReady()
	require.Len(t, ready, 1)
	assert.Equal(t, uint64(0), ready[0].Payload.SubmitAt)

	result, err = dest.ImportQueue(export, QueueImportOptions{ForceReady: true})
	require.NoError(t, err)
	assert.Equal(t, 0, result.Inserted)
	assert.Equal(t, 1, result.Duplicates)
	assert.Equal(t, 0, result.Conflicts)
}

func queueExportRowFromPayload(payload SharePayload, state ShareState, voteEndTime uint64) QueueExportRow {
	return QueueExportRow{
		ShareIndex:       payload.EncShare.ShareIndex,
		SharesHash:       payload.SharesHash,
		ProposalID:       payload.ProposalID,
		VoteDecision:     payload.VoteDecision,
		EncShare:         payload.EncShare,
		TreePosition:     payload.TreePosition,
		ShareComms:       payload.ShareComms,
		PrimaryBlind:     payload.PrimaryBlind,
		State:            state,
		VoteEndTime:      voteEndTime,
		SubmitAt:         payload.SubmitAt,
		OriginalSubmitAt: payload.SubmitAt,
		Processable:      isProcessableShareState(state),
	}
}
