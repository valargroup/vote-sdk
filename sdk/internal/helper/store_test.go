package helper

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestStore(t *testing.T) *ShareStore {
	t.Helper()
	s, err := NewShareStore(":memory:", 0, 0)
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

	s.MarkSubmitted("round1", 0, 1)

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
		s.MarkFailed("round1", 0, 1)
		// Fast-forward schedule so it's immediately ready again.
		s.mu.Lock()
		s.schedule[schedKey("round1", 0, 1)] = time.Now().Add(-time.Second)
		s.mu.Unlock()
	}

	// After 4 failures (attempts = 4), take once more.
	ready := s.TakeReady()
	require.Len(t, ready, 1)
	s.MarkFailed("round1", 0, 1) // 5th attempt = permanent failure

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

	s.MarkSubmitted("round1", 0, 1)
	s.MarkSubmitted("round1", 0, 2)

	status = s.Status()
	assert.Equal(t, 2, status["round1"].Submitted)
}

func TestRecovery(t *testing.T) {
	// Use a file-based DB so we can reopen it.
	dbPath := t.TempDir() + "/helper_test.db"

	s1, err := NewShareStore(dbPath, 0, 0)
	require.NoError(t, err)

	enqueueAndRequireInserted(t, s1, testPayload("round1", 0))

	// Take the share (moves to Witnessed state).
	ready := s1.TakeReady()
	require.Len(t, ready, 1)

	// Close without marking submitted (simulates crash).
	s1.Close()

	// Reopen: recovery should reset Witnessed → Received with fresh delay.
	s2, err := NewShareStore(dbPath, 0, 0)
	require.NoError(t, err)
	defer s2.Close()

	ready = s2.TakeReady()
	assert.Len(t, ready, 1, "recovered share should be ready again")
}
