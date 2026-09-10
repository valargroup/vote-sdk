package helper

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRetrySlots(t *testing.T) {
	start := time.Unix(1_800_000_000, 0)
	for _, tc := range []struct {
		name  string
		final time.Duration
		want  []time.Duration
	}{
		{"seven days", 7*24*time.Hour - 10*time.Minute, []time.Duration{0, time.Minute, 10 * time.Minute, 48 * time.Hour, 7*24*time.Hour - 10*time.Minute}},
		{"six hours", 6*time.Hour - 10*time.Minute, []time.Duration{0, time.Minute, 10 * time.Minute, 3 * time.Hour, 6*time.Hour - 10*time.Minute}},
		{"short window", 200 * time.Second, []time.Duration{0, time.Minute, 130 * time.Second, 165 * time.Second, 200 * time.Second}},
		{"too close", 15 * time.Second, []time.Duration{0, 15 * time.Second}},
		{"no retry room", 5 * time.Second, []time.Duration{0}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := retryTimes(start, start.Add(tc.final))
			require.Len(t, got, len(tc.want))
			for i, offset := range tc.want {
				assert.Equal(t, start.Add(offset), got[i])
			}
		})
	}
}

func TestRetryFinalJitterAndBuffer(t *testing.T) {
	start := time.Unix(1_800_000_000, 0)
	for _, remaining := range []time.Duration{7 * 24 * time.Hour, 6 * time.Hour, 4 * time.Minute} {
		t.Run(remaining.String(), func(t *testing.T) {
			deadline := start.Add(remaining)
			buffer := min(retrySafetyBuffer, max(retryMinBuffer, remaining/8))
			cutoff := deadline.Add(-buffer)
			jitter := min(retryFinalJitter, remaining/8)
			seen := make(map[time.Time]bool)
			for range 32 {
				state := newRetryState(start, uint64(deadline.Unix()))
				require.Len(t, retryTimes(state.FirstAttempt, state.FinalAttempt), 5)
				last := retryTimes(state.FirstAttempt, state.FinalAttempt)[4]
				assert.Equal(t, cutoff, retryCutoff(state.FirstAttempt, uint64(deadline.Unix())))
				assert.False(t, last.Before(cutoff.Add(-jitter)))
				assert.False(t, last.After(cutoff))
				seen[last] = true
			}
			assert.Greater(t, len(seen), 1, "independent shares should not share one final time")
		})
	}
	for _, end := range []uint64{0, uint64(start.Add(20 * time.Second).Unix()), uint64(start.Add(-time.Hour).Unix())} {
		state := newRetryState(start, end)
		assert.Equal(t, []time.Time{start}, retryTimes(state.FirstAttempt, state.FinalAttempt))
	}
}

func TestRetryDueSkipsMissedSlotsAndHonorsCutoff(t *testing.T) {
	start := time.Unix(1_800_000_000, 0)
	end := uint64(start.Add(6*time.Hour + 6*time.Minute).Unix())
	state := retryState{FirstAttempt: start, FinalAttempt: start.Add(6 * time.Hour), NextSlot: 1, LastAttempt: start}
	slot, next := state.due(start.Add(30*time.Second), end)
	assert.Equal(t, -1, slot)
	assert.Equal(t, start.Add(time.Minute), next)

	// After downtime, the missed one-minute slot is skipped in favor of ten minutes.
	now := start.Add(20 * time.Minute)
	slot, _ = state.due(now, end)
	require.Equal(t, 2, slot)
	state.NextSlot, state.LastAttempt = slot+1, now
	slot, next = state.due(now, end)
	assert.Equal(t, -1, slot)
	assert.Equal(t, retryTimes(state.FirstAttempt, state.FinalAttempt)[3], next)

	slot, next = state.due(retryCutoff(state.FirstAttempt, end).Add(time.Second), end)
	assert.Equal(t, -1, slot)
	assert.True(t, next.IsZero())
	state.NextSlot = len(retryTimes(state.FirstAttempt, state.FinalAttempt))
	slot, next = state.due(state.FinalAttempt, end)
	assert.Equal(t, -1, slot)
	assert.True(t, next.IsZero())
}

func TestRetryReservationSurvivesRestartAndRejectsStaleWorker(t *testing.T) {
	path := filepath.Join(t.TempDir(), "helper.db")
	start := time.Now()
	fetcher := func(string) (RoundInfo, error) {
		return RoundInfo{CreatedAtTime: uint64(start.Unix()), VoteEndTime: uint64(start.Add(7 * 24 * time.Hour).Unix())}, nil
	}
	store, err := NewShareStore(path, fetcher)
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })
	payload := testPayload("round1", 0)
	enqueueAndRequireInserted(t, store, payload)
	ready := store.TakeReady()
	require.Len(t, ready, 1)
	state := newRetryState(start, ready[0].VoteEndTime)
	state.NextSlot, state.LastAttempt, state.LastHeight = 1, start, 100
	require.NoError(t, store.reserveProofAttempt(ready[0], state))
	require.Error(t, store.reserveProofAttempt(ready[0], state), "a stale worker cannot consume or reset another slot")
	require.NoError(t, store.Close())

	// A crash after reservation leaves the slot consumed, even without a result.
	store, err = NewShareStore(path, fetcher)
	require.NoError(t, err)
	result, err := store.Enqueue(payload)
	require.NoError(t, err)
	require.Equal(t, EnqueueDuplicate, result)
	ready = store.TakeReady()
	require.Len(t, ready, 1)
	recovered, err := decodeRetryState(ready[0].retryState, ready[0].VoteEndTime)
	require.NoError(t, err)
	raw, err := json.Marshal(state)
	require.NoError(t, err)
	assert.JSONEq(t, string(raw), ready[0].retryState)
	assert.NotContains(t, ready[0].retryState, `"slots"`)
	assert.NotContains(t, ready[0].retryState, `"cutoff"`)
	slot, next := recovered.due(start.Add(30*time.Second), ready[0].VoteEndTime)
	assert.Equal(t, -1, slot)
	assert.True(t, retryTimes(state.FirstAttempt, state.FinalAttempt)[1].Equal(next))
	assert.Equal(t, uint64(100), recovered.LastHeight)
}

func TestRetryPollingWakesAtFinalSlot(t *testing.T) {
	store := newTestStore(t)
	enqueueAndRequireInserted(t, store, testPayload("round1", 0))
	ready := store.TakeReady()
	require.Len(t, ready, 1)
	now := time.Now()
	final := now.Add(3 * time.Second)
	state := retryState{FirstAttempt: now.Add(-time.Minute), FinalAttempt: final}
	state.NextSlot = len(retryTimes(state.FirstAttempt, state.FinalAttempt)) - 1
	require.NoError(t, store.reserveProofAttempt(ready[0], state))
	store.MarkRetry("round1", 0, 1, 0)
	next, ok := store.NextScheduledTime()
	require.True(t, ok)
	assert.True(t, final.Equal(next), "the ten-second poll must not oversleep the final proof slot")
}

func TestDecodeRetryStateRejectsInvalidProgress(t *testing.T) {
	for _, raw := range []string{
		`garbage`, `{}`, `{"next_slot":0}`,
		`{"first_attempt":"2027-01-01T00:00:00Z","final_attempt":"2027-01-01T00:00:00Z","next_slot":-1}`,
		`{"first_attempt":"2027-01-01T00:00:00Z","final_attempt":"2027-01-01T00:00:00Z","next_slot":2}`,
		`{"first_attempt":"2027-01-01T00:00:00Z","final_attempt":"2027-01-01T00:00:01Z","next_slot":0}`,
	} {
		_, err := decodeRetryState(raw, 0)
		require.Error(t, err)
	}
}

func TestRetryChecksDoNotSpinAfterLocalDeadline(t *testing.T) {
	store := newTestStore(t)
	enqueueAndRequireInserted(t, store, testPayload("round1", 0))
	now := time.Now()
	_, err := store.db.Exec("UPDATE shares SET vote_end_time = ?", now.Add(-time.Hour).Unix())
	require.NoError(t, err)
	ready := store.TakeReady()
	require.Len(t, ready, 1)
	state := retryState{FirstAttempt: now.Add(-2 * time.Hour), FinalAttempt: now.Add(-2 * time.Hour), NextSlot: 1}
	require.NoError(t, store.reserveProofAttempt(ready[0], state))
	store.MarkRetry("round1", 0, 1, 0)
	next, ok := store.NextScheduledTime()
	require.True(t, ok)
	assert.WithinDuration(t, time.Now().Add(shareSystemRetryBackoff), next, time.Second)
	share, ok := store.loadShare("round1", 0, 1, 0)
	require.True(t, ok)
	assert.Equal(t, ShareStateReceived, share.State)
	assert.NotEmpty(t, share.Payload.PrimaryBlind)
}

func TestRetryStateMigrationPreservesExistingQueue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "helper.db")
	store, err := NewShareStore(path, func(string) (RoundInfo, error) {
		return RoundInfo{CreatedAtTime: 1, VoteEndTime: uint64(time.Now().Add(time.Hour).Unix())}, nil
	})
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })
	payload := testPayload("round1", 0)
	enqueueAndRequireInserted(t, store, payload)
	_, err = store.db.Exec("ALTER TABLE shares DROP COLUMN retry_state")
	require.NoError(t, err)
	require.NoError(t, store.Close())
	store, err = NewShareStore(path, nil)
	require.NoError(t, err)
	ready := store.TakeReady()
	require.Len(t, ready, 1)
	assert.Equal(t, payload, ready[0].Payload)
	assert.Empty(t, ready[0].retryState)
	require.NoError(t, migrate(store.db), "migration is idempotent")
}
