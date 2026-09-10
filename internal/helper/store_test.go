package helper

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/valargroup/vote-sdk/crypto/elgamal"
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
	generator := elgamal.PallasGenerator()
	c1B64 := base64.StdEncoding.EncodeToString(generator.ToAffineCompressed())
	c2B64 := base64.StdEncoding.EncodeToString(generator.Double().ToAffineCompressed())
	comms := make([]string, 16)
	for i := range comms {
		comms[i] = zeroB64
	}
	return SharePayload{
		SharesHash:   zeroB64,
		ProposalID:   1,
		VoteDecision: 0,
		EncShare: EncryptedShareWire{
			C1:         c1B64,
			C2:         c2B64,
			ShareIndex: shareIndex,
		},
		TreePosition: 0,
		VoteRoundID:  roundID,
		ShareComms:   comms,
		PrimaryBlind: zeroB64,
		SubmitAt:     0, // immediate
	}
}

// testFieldB64 returns a valid 32-byte base64 field with a recognizable byte pattern.
func testFieldB64(fill byte) string {
	return base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{fill}, 32))
}

// distinctSensitivePayload returns a payload whose sensitive fields have
// different sentinel values so tests can catch field swaps.
func distinctSensitivePayload(roundID string, shareIndex uint32) SharePayload {
	p := testPayload(roundID, shareIndex)
	p.SharesHash = testFieldB64(1)
	p.EncShare.C1 = testFieldB64(2)
	p.EncShare.C2 = testFieldB64(3)
	p.PrimaryBlind = testFieldB64(4)
	p.ShareComms = make([]string, 16)
	for i := range p.ShareComms {
		p.ShareComms[i] = testFieldB64(byte(10 + i))
	}
	return p
}

// sensitiveFieldStrings returns the payload fields expected to be retained only
// until terminal cleanup for failed shares.
func sensitiveFieldStrings(payload SharePayload) []string {
	fields := []string{
		payload.EncShare.C1,
		payload.EncShare.C2,
		payload.PrimaryBlind,
	}
	fields = append(fields, payload.ShareComms...)
	return fields
}

// containsSensitiveField reports whether any sensitive payload sentinel appears
// in raw database bytes.
func containsSensitiveField(data []byte, payload SharePayload) bool {
	for _, field := range sensitiveFieldStrings(payload) {
		if bytes.Contains(data, []byte(field)) {
			return true
		}
	}
	return false
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

func TestTakeReadyBatch_OldestFirstWithinRound(t *testing.T) {
	s := newTestStore(t)
	base := time.Now().Add(-time.Minute)
	for i := range uint32(3) {
		payload := testPayload("round-a", i)
		payload.TreePosition = uint64(i)
		enqueueAndRequireInserted(t, s, payload)
	}

	s.mu.Lock()
	s.schedule[schedKey("round-a", 0, 1, 0)] = base.Add(-2 * time.Second)
	s.schedule[schedKey("round-a", 1, 1, 1)] = base
	s.schedule[schedKey("round-a", 2, 1, 2)] = base.Add(-time.Second)
	s.mu.Unlock()

	ready := s.TakeReadyBatch(3)
	require.Len(t, ready, 3)
	assert.Equal(t, []uint32{0, 2, 1}, []uint32{
		ready[0].Payload.EncShare.ShareIndex,
		ready[1].Payload.EncShare.ShareIndex,
		ready[2].Payload.EncShare.ShareIndex,
	})
}

func TestTakeReadyBatch_CorruptOldestFailsOnlyThatShare(t *testing.T) {
	s := newTestStore(t)
	for i := range uint32(3) {
		payload := testPayload("round-a", i)
		payload.TreePosition = uint64(i)
		enqueueAndRequireInserted(t, s, payload)
	}
	_, err := s.db.Exec(
		"UPDATE shares SET share_comms = ? WHERE round_id = ? AND share_index = ?",
		"{invalid", "round-a", 0,
	)
	require.NoError(t, err)

	s.mu.Lock()
	base := time.Now().Add(-time.Minute)
	s.schedule[schedKey("round-a", 0, 1, 0)] = base
	s.schedule[schedKey("round-a", 1, 1, 1)] = base.Add(time.Second)
	s.schedule[schedKey("round-a", 2, 1, 2)] = base.Add(2 * time.Second)
	s.mu.Unlock()

	ready := s.TakeReadyBatch(3)
	require.Len(t, ready, 2)
	assert.Equal(t, []uint32{1, 2}, []uint32{
		ready[0].Payload.EncShare.ShareIndex,
		ready[1].Payload.EncShare.ShareIndex,
	})

	var state, attempts int
	err = s.db.QueryRow(
		"SELECT state, attempts FROM shares WHERE round_id = ? AND share_index = ?",
		"round-a", 0,
	).Scan(&state, &attempts)
	require.NoError(t, err)
	assert.Equal(t, int(ShareStateFailed), state)
	assert.Equal(t, 1, attempts)

	for range 10 {
		assert.Empty(t, s.TakeReadyBatch(3))
	}
	_, scheduled := s.NextScheduledTime()
	assert.False(t, scheduled, "terminal poison row must not keep the processor timer due")
	for _, share := range ready {
		s.MarkRetry(share.Payload.VoteRoundID, share.Payload.EncShare.ShareIndex, share.Payload.ProposalID, share.Payload.TreePosition)
	}

	exported, err := s.ExportQueue("round-a", time.Now())
	require.NoError(t, err)
	require.Len(t, exported.Rows, 3)
	var corrupt QueueExportRow
	for _, row := range exported.Rows {
		if row.ShareIndex == 0 {
			corrupt = row
		}
	}
	assert.True(t, corrupt.Corrupt)
	assert.Equal(t, "invalid_share_comms_json", corrupt.CorruptionReason)
	require.NotNil(t, corrupt.RawShareCommsBase64)
	assert.Equal(t, base64.StdEncoding.EncodeToString([]byte("{invalid")), *corrupt.RawShareCommsBase64)
	assert.False(t, corrupt.Processable)
	assert.Empty(t, corrupt.ShareComms)

	dest := newTestStore(t)
	imported, err := dest.ImportQueue(exported, QueueImportOptions{})
	require.NoError(t, err)
	assert.Equal(t, 2, imported.Inserted)
	assert.Equal(t, 1, imported.SkippedTerminal)
}

func TestExportQueue_IncludesUnclassifiedCorruptDiagnostic(t *testing.T) {
	s := newTestStore(t)
	corruptPayload := testPayload("round-a", 0)
	corruptPayload.SubmitAt = uint64(time.Now().Add(time.Hour).Unix())
	enqueueAndRequireInserted(t, s, corruptPayload)
	healthyPayload := testPayload("round-a", 1)
	healthyPayload.TreePosition = 1
	enqueueAndRequireInserted(t, s, healthyPayload)
	_, err := s.db.Exec("UPDATE shares SET share_comms = ? WHERE round_id = ? AND share_index = ?", "{invalid", "round-a", 0)
	require.NoError(t, err)

	exported, err := s.ExportQueue("round-a", time.Now())
	require.NoError(t, err)
	require.Len(t, exported.Rows, 2)
	assert.True(t, exported.Rows[1].Corrupt)
	assert.False(t, exported.Rows[1].Processable)
	require.NotNil(t, exported.Rows[1].RawShareCommsBase64)
	assert.Equal(t, base64.StdEncoding.EncodeToString([]byte("{invalid")), *exported.Rows[1].RawShareCommsBase64)

	dest := newTestStore(t)
	result, err := dest.ImportQueue(exported, QueueImportOptions{})
	require.NoError(t, err)
	assert.Equal(t, 1, result.Inserted)
	assert.Equal(t, 1, result.SkippedTerminal)
}

func TestExportQueue_CorruptEmptyShareCommsRetainsRawField(t *testing.T) {
	s := newTestStore(t)
	enqueueAndRequireInserted(t, s, testPayload("round-a", 0))
	_, err := s.db.Exec("UPDATE shares SET share_comms = '' WHERE round_id = ?", "round-a")
	require.NoError(t, err)

	exported, err := s.ExportQueue("round-a", time.Now())
	require.NoError(t, err)
	require.Len(t, exported.Rows, 1)
	require.NotNil(t, exported.Rows[0].RawShareCommsBase64)
	assert.Empty(t, *exported.Rows[0].RawShareCommsBase64)
	wire, err := json.Marshal(exported.Rows[0])
	require.NoError(t, err)
	assert.Contains(t, string(wire), `"raw_share_comms_base64":""`)
}

func TestTakeReadyBatch_ValueTypeCorruptionIsTerminal(t *testing.T) {
	s := newTestStore(t)
	for i := range uint32(2) {
		payload := testPayload("round-a", i)
		payload.TreePosition = uint64(i)
		enqueueAndRequireInserted(t, s, payload)
	}
	_, err := s.db.Exec(
		"UPDATE shares SET vote_decision = ? WHERE round_id = ? AND share_index = ?",
		"not-an-integer", "round-a", 0,
	)
	require.NoError(t, err)
	s.mu.Lock()
	s.schedule[schedKey("round-a", 0, 1, 0)] = time.Now().Add(-time.Minute)
	s.schedule[schedKey("round-a", 1, 1, 1)] = time.Now().Add(-time.Minute + time.Second)
	s.mu.Unlock()

	ready := s.TakeReadyBatch(2)
	require.Len(t, ready, 1)
	assert.Equal(t, uint32(1), ready[0].Payload.EncShare.ShareIndex)
	var state, attempts int
	err = s.db.QueryRow(
		"SELECT state, attempts FROM shares WHERE round_id = ? AND share_index = ?",
		"round-a", 0,
	).Scan(&state, &attempts)
	require.NoError(t, err)
	assert.Equal(t, int(ShareStateFailed), state)
	assert.Equal(t, 1, attempts)
}

func TestTakeReadyBatch_StateValueCorruptionIsTerminal(t *testing.T) {
	s := newTestStore(t)
	payload := testPayload("round-a", 0)
	enqueueAndRequireInserted(t, s, payload)
	_, err := s.db.Exec(
		"UPDATE shares SET state = ? WHERE round_id = ? AND share_index = ?",
		"not-an-integer", "round-a", 0,
	)
	require.NoError(t, err)

	assert.Empty(t, s.TakeReadyBatch(1))
	var state, attempts int
	err = s.db.QueryRow(
		"SELECT state, attempts FROM shares WHERE round_id = ? AND share_index = ?",
		"round-a", 0,
	).Scan(&state, &attempts)
	require.NoError(t, err)
	assert.Equal(t, int(ShareStateFailed), state)
	assert.Equal(t, 1, attempts)
	assert.NotContains(t, s.schedule, schedKey("round-a", 0, 1, 0))
}

func TestTakeReadyBatch_TerminalizationFailureRetainsSchedule(t *testing.T) {
	s := newTestStore(t)
	now := time.Unix(2_000_000_000, 0)
	s.now = func() time.Time { return now }
	payload := testPayload("round-a", 0)
	enqueueAndRequireInserted(t, s, payload)
	_, err := s.db.Exec("UPDATE shares SET share_comms = ? WHERE round_id = ?", "{invalid", "round-a")
	require.NoError(t, err)
	_, err = s.db.Exec(`CREATE TRIGGER fail_terminalization BEFORE UPDATE OF state ON shares
		WHEN NEW.state = 3 BEGIN SELECT RAISE(FAIL, 'injected terminalization failure'); END`)
	require.NoError(t, err)

	assert.Empty(t, s.TakeReadyBatch(1))
	key := schedKey("round-a", 0, 1, 0)
	assert.Contains(t, s.schedule, key)
	next, ok := s.NextScheduledTime()
	require.True(t, ok)
	assert.Equal(t, now.Add(shareSystemRetryBackoff), next)

	_, err = s.db.Exec("DROP TRIGGER fail_terminalization")
	require.NoError(t, err)
	now = now.Add(shareSystemRetryBackoff + time.Second)
	assert.Empty(t, s.TakeReadyBatch(1))
	assert.NotContains(t, s.schedule, key)
}

func TestTakeReadyBatch_StoreFailureAppliesGlobalBackoff(t *testing.T) {
	s := newTestStore(t)
	now := time.Unix(2_000_000_000, 500)
	s.now = func() time.Time { return now }
	enqueueAndRequireInserted(t, s, testPayload("round-a", 0))
	enqueueAndRequireInserted(t, s, testPayload("round-b", 0))
	require.NoError(t, s.db.Close())

	assert.Empty(t, s.TakeReadyBatch(2))
	assert.Len(t, s.schedule, 2)
	next, ok := s.NextScheduledTime()
	require.True(t, ok)
	assert.Equal(t, now.Add(shareSystemRetryBackoff), next)
	assert.Empty(t, s.TakeReadyBatch(2), "common backoff must avoid hammering every candidate")
}

func TestTakeReadyBatch_MissingSchemaRetainsReceivedShare(t *testing.T) {
	s := newTestStore(t)
	now := time.Unix(2_000_000_000, 0)
	s.now = func() time.Time { return now }
	enqueueAndRequireInserted(t, s, testPayload("round-a", 0))
	key := schedKey("round-a", 0, 1, 0)

	_, err := s.db.Exec("ALTER TABLE shares RENAME TO shares_unavailable")
	require.NoError(t, err)
	assert.Empty(t, s.TakeReadyBatch(1))
	assert.Contains(t, s.schedule, key)
	assert.Empty(t, s.inFlight)
	next, ok := s.NextScheduledTime()
	require.True(t, ok)
	assert.Equal(t, now.Add(shareSystemRetryBackoff), next)

	_, err = s.db.Exec("ALTER TABLE shares_unavailable RENAME TO shares")
	require.NoError(t, err)
	now = now.Add(shareSystemRetryBackoff + time.Second)
	ready := s.TakeReadyBatch(1)
	require.Len(t, ready, 1)
	assert.Zero(t, ready[0].Attempts)
	var state, attempts int
	err = s.db.QueryRow("SELECT state, attempts FROM shares WHERE round_id = ?", "round-a").Scan(&state, &attempts)
	require.NoError(t, err)
	assert.Equal(t, int(ShareStateReceived), state)
	assert.Zero(t, attempts)
}

func TestTakeReadyBatch_LoadFailureNeverDispatchesZeroShare(t *testing.T) {
	s := newTestStore(t)
	base := time.Unix(2_000_000_000, 0)
	s.now = func() time.Time { return base }
	enqueueAndRequireInserted(t, s, testPayload("round-a", 0))

	_, err := s.db.Exec("ALTER TABLE shares RENAME TO shares_unavailable")
	require.NoError(t, err)
	calls := 0
	s.now = func() time.Time {
		calls++
		if calls == 1 {
			return base
		}
		return base.Add(-20 * time.Second)
	}

	assert.Empty(t, s.TakeReadyBatch(1), "load failure must exit before dispatch regardless of clock movement")
	assert.Empty(t, s.inFlight)
	assert.Contains(t, s.schedule, schedKey("round-a", 0, 1, 0))
}

func TestMarkSubmitted_WriteLockRestoresScheduleAndBacksOff(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "helper.db")
	now := uint64(time.Now().Unix())
	s, err := NewShareStore(dbPath, func(string) (RoundInfo, error) {
		return RoundInfo{CreatedAtTime: now, VoteEndTime: now + testVoteEndOffset}, nil
	})
	require.NoError(t, err)
	t.Cleanup(func() { s.Close() })
	clock := time.Now().Truncate(time.Second)
	s.now = func() time.Time { return clock }
	enqueueAndRequireInserted(t, s, testPayload("round-a", 0))

	blocker, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { blocker.Close() })
	blocker.SetMaxOpenConns(1)
	_, err = blocker.Exec("PRAGMA busy_timeout=0")
	require.NoError(t, err)
	_, err = blocker.Exec("BEGIN IMMEDIATE")
	require.NoError(t, err)
	defer blocker.Exec("ROLLBACK")

	require.Len(t, s.TakeReadyBatch(1), 1)
	s.MarkSubmitted("round-a", 0, 1, 0)
	key := schedKey("round-a", 0, 1, 0)
	assert.Contains(t, s.schedule, key)
	assert.NotContains(t, s.inFlight, key)
	next, ok := s.NextScheduledTime()
	require.True(t, ok)
	assert.Equal(t, clock.Add(shareSystemRetryBackoff), next)
}

func TestTakeReadyBatch_UsesProcessLocalOwnership(t *testing.T) {
	s := newTestStore(t)
	payload := testPayload("round-a", 0)
	enqueueAndRequireInserted(t, s, payload)
	ready := s.TakeReadyBatch(1)
	require.Len(t, ready, 1)
	key := schedKey("round-a", 0, 1, 0)
	assert.NotContains(t, s.schedule, key)
	assert.Contains(t, s.inFlight, key)
	assert.Empty(t, s.TakeReadyBatch(1), "an active share must not be dispatched twice")

	var state, attempts int
	err := s.db.QueryRow("SELECT state, attempts FROM shares WHERE round_id = ?", "round-a").Scan(&state, &attempts)
	require.NoError(t, err)
	assert.Equal(t, int(ShareStateReceived), state)
	assert.Zero(t, attempts)
	status := s.Status()["round-a"]
	assert.Equal(t, 1, status.Pending)
	assert.Equal(t, 1, status.Processing)
	summary, err := s.QueueSummary("round-a", time.Now())
	require.NoError(t, err)
	assert.Equal(t, 1, summary.Processing)
	assert.Zero(t, summary.Ready)
}

func TestTakeReadyBatch_DropsScheduleEntryForInFlightShare(t *testing.T) {
	s := newTestStore(t)
	payload := testPayload("round-a", 0)
	enqueueAndRequireInserted(t, s, payload)
	require.Len(t, s.TakeReadyBatch(1), 1)

	key := schedKey("round-a", 0, 1, 0)
	s.mu.Lock()
	s.schedule[key] = s.now().Add(-time.Second)
	s.mu.Unlock()

	assert.Empty(t, s.TakeReadyBatch(1))
	assert.NotContains(t, s.schedule, key)
	assert.Contains(t, s.inFlight, key)
	_, scheduled := s.NextScheduledTime()
	assert.False(t, scheduled, "an active duplicate must not keep the scheduler awake")
}

func TestRequeueInFlightIfOwned(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().Truncate(time.Second)
	s.now = func() time.Time { return now }
	enqueueAndRequireInserted(t, s, testPayload("round-a", 0))
	ready := s.TakeReadyBatch(1)
	require.Len(t, ready, 1)

	key := schedKey("round-a", 0, 1, 0)
	require.True(t, s.requeueInFlightIfOwned("round-a", 0, 1, 0, ready[0].attemptID))
	assert.NotContains(t, s.inFlight, key)
	assert.Equal(t, scheduleSecond(now.Add(shareSystemRetryBackoff)), s.schedule[key])
	assert.False(t, s.requeueInFlightIfOwned("round-a", 0, 1, 0, ready[0].attemptID), "fallback must be idempotent")

	var state, attempts int
	err := s.db.QueryRow("SELECT state, attempts FROM shares WHERE round_id = ?", "round-a").Scan(&state, &attempts)
	require.NoError(t, err)
	assert.Equal(t, int(ShareStateReceived), state)
	assert.Zero(t, attempts)
}

func TestRequeueInFlightIfOwned_ExpiredRoundUsesBackoff(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().Truncate(time.Second)
	s.now = func() time.Time { return now }
	enqueueAndRequireInserted(t, s, testPayload("round-a", 0))
	_, err := s.db.Exec(
		"UPDATE shares SET vote_end_time = ? WHERE round_id = ?",
		uint64(now.Add(-time.Minute).Unix()), "round-a",
	)
	require.NoError(t, err)

	key := schedKey("round-a", 0, 1, 0)
	for cycle := range 3 {
		ready := s.TakeReadyBatch(1)
		require.Len(t, ready, 1, "cycle %d", cycle)
		require.True(t, s.requeueInFlightIfOwned("round-a", 0, 1, 0, ready[0].attemptID), "cycle %d", cycle)

		next, ok := s.NextScheduledTime()
		require.True(t, ok, "cycle %d", cycle)
		expected := scheduleSecond(now.Add(shareSystemRetryBackoff))
		assert.Equal(t, expected, next, "cycle %d", cycle)
		assert.True(t, next.After(now), "cycle %d must not remain immediately due", cycle)
		assert.NotContains(t, s.inFlight, key)

		now = next.Add(time.Second)
	}

	var state, attempts int
	err = s.db.QueryRow("SELECT state, attempts FROM shares WHERE round_id = ?", "round-a").Scan(&state, &attempts)
	require.NoError(t, err)
	assert.Equal(t, int(ShareStateReceived), state)
	assert.Zero(t, attempts)
}

func TestRequeueInFlightIfOwned_DoesNotReleaseLaterAttempt(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().Truncate(time.Second)
	s.now = func() time.Time { return now }
	enqueueAndRequireInserted(t, s, testPayload("round-a", 0))

	first := s.TakeReadyBatch(1)
	require.Len(t, first, 1)
	s.MarkRetry("round-a", 0, 1, 0)

	now = now.Add(shareSystemRetryBackoff + time.Second)
	second := s.TakeReadyBatch(1)
	require.Len(t, second, 1)
	require.NotEqual(t, first[0].attemptID, second[0].attemptID)

	key := schedKey("round-a", 0, 1, 0)
	assert.False(t, s.requeueInFlightIfOwned("round-a", 0, 1, 0, first[0].attemptID))
	assert.Equal(t, second[0].attemptID, s.inFlight[key].attemptID)
	assert.NotContains(t, s.schedule, key)

	assert.True(t, s.requeueInFlightIfOwned("round-a", 0, 1, 0, second[0].attemptID))
	assert.NotContains(t, s.inFlight, key)
	assert.Contains(t, s.schedule, key)
}

func TestWorkerCompletionWithoutOwnerIsIgnored(t *testing.T) {
	s := newTestStore(t)
	enqueueAndRequireInserted(t, s, testPayload("round-a", 0))
	require.Len(t, s.TakeReadyBatch(1), 1)
	s.MarkRetry("round-a", 0, 1, 0)

	// Duplicate and stale completions from the released ownership must not
	// mutate or terminalize the pending row.
	s.MarkSubmitted("round-a", 0, 1, 0)
	s.MarkFailed("round-a", 0, 1, 0)
	s.MarkRetry("round-a", 0, 1, 0)

	var state, attempts int
	err := s.db.QueryRow("SELECT state, attempts FROM shares WHERE round_id = ?", "round-a").Scan(&state, &attempts)
	require.NoError(t, err)
	assert.Equal(t, int(ShareStateReceived), state)
	assert.Zero(t, attempts)
	assert.Contains(t, s.schedule, schedKey("round-a", 0, 1, 0))
}

func TestTakeReadyBatch_RoundRobinAcrossCalls(t *testing.T) {
	s := newTestStore(t)
	for _, roundID := range []string{"round-a", "round-b", "round-c"} {
		for i := range uint32(3) {
			payload := testPayload(roundID, i)
			payload.TreePosition = uint64(i)
			enqueueAndRequireInserted(t, s, payload)
		}
	}

	var got []string
	seen := make(map[string]struct{})
	for range 9 {
		ready := s.TakeReadyBatch(1)
		require.Len(t, ready, 1)
		got = append(got, ready[0].Payload.VoteRoundID)
		key := schedKey(
			ready[0].Payload.VoteRoundID,
			ready[0].Payload.EncShare.ShareIndex,
			ready[0].Payload.ProposalID,
			ready[0].Payload.TreePosition,
		)
		_, duplicate := seen[key]
		assert.False(t, duplicate, "share dequeued more than once")
		seen[key] = struct{}{}
	}

	assert.Equal(t, []string{
		"round-a", "round-b", "round-c",
		"round-a", "round-b", "round-c",
		"round-a", "round-b", "round-c",
	}, got)
	assert.Len(t, seen, 9)
	assert.Empty(t, s.TakeReady())
}

func TestTakeReadyBatch_RoundRobinWithinLargeBatch(t *testing.T) {
	s := newTestStore(t)
	for _, roundID := range []string{"round-a", "round-b", "round-c"} {
		for i := range uint32(2) {
			payload := testPayload(roundID, i)
			payload.TreePosition = uint64(i)
			enqueueAndRequireInserted(t, s, payload)
		}
	}

	ready := s.TakeReadyBatch(5)
	require.Len(t, ready, 5)
	assert.Equal(t, []string{"round-a", "round-b", "round-c", "round-a", "round-b"}, []string{
		ready[0].Payload.VoteRoundID,
		ready[1].Payload.VoteRoundID,
		ready[2].Payload.VoteRoundID,
		ready[3].Payload.VoteRoundID,
		ready[4].Payload.VoteRoundID,
	})
}

func TestTakeReadyBatch_FreshRoundWithinOneRotation(t *testing.T) {
	s := newTestStore(t)
	for _, roundID := range []string{"round-a", "round-b"} {
		for i := range uint32(3) {
			payload := testPayload(roundID, i)
			payload.TreePosition = uint64(i)
			enqueueAndRequireInserted(t, s, payload)
		}
	}

	first := s.TakeReadyBatch(1)
	require.Len(t, first, 1)
	require.Equal(t, "round-a", first[0].Payload.VoteRoundID)

	fresh := testPayload("round-c", 0)
	enqueueAndRequireInserted(t, s, fresh)
	next := s.TakeReadyBatch(2)
	require.Len(t, next, 2)
	assert.Equal(t, "round-b", next[0].Payload.VoteRoundID)
	assert.Equal(t, "round-c", next[1].Payload.VoteRoundID)
}

func TestTakeReadyBatch_ExcludesFutureRoundUntilDue(t *testing.T) {
	s := newTestStore(t)
	enqueueAndRequireInserted(t, s, testPayload("round-a", 0))
	future := testPayload("round-b", 0)
	future.SubmitAt = uint64(time.Now().Add(time.Hour).Unix())
	enqueueAndRequireInserted(t, s, future)

	ready := s.TakeReadyBatch(2)
	require.Len(t, ready, 1)
	assert.Equal(t, "round-a", ready[0].Payload.VoteRoundID)

	s.mu.Lock()
	s.schedule[schedKey("round-b", 0, 1, 0)] = time.Now().Add(-time.Second)
	s.mu.Unlock()
	ready = s.TakeReadyBatch(1)
	require.Len(t, ready, 1)
	assert.Equal(t, "round-b", ready[0].Payload.VoteRoundID)
}

func TestConcurrentEnqueueColdRound(t *testing.T) {
	const enqueueCount = 100

	fetchStarted := sync.WaitGroup{}
	fetchStarted.Add(enqueueCount)
	releaseFetch := make(chan struct{})
	now := uint64(time.Now().Unix())
	fetcher := func(string) (RoundInfo, error) {
		fetchStarted.Done()
		<-releaseFetch
		return RoundInfo{CreatedAtTime: now, VoteEndTime: now + testVoteEndOffset}, nil
	}

	s, err := NewShareStore(filepath.Join(t.TempDir(), "helper.db"), fetcher)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })

	results := make(chan error, enqueueCount)
	workers := sync.WaitGroup{}
	workers.Add(enqueueCount)
	for i := range enqueueCount {
		go func() {
			defer workers.Done()
			result, err := s.Enqueue(testPayload("cold-round", uint32(i)))
			if err == nil && result != EnqueueInserted {
				err = fmt.Errorf("unexpected enqueue result: %v", result)
			}
			results <- err
		}()
	}

	allFetchesStarted := make(chan struct{})
	go func() {
		fetchStarted.Wait()
		close(allFetchesStarted)
	}()
	select {
	case <-allFetchesStarted:
	case <-time.After(5 * time.Second):
		close(releaseFetch)
		workers.Wait()
		t.Fatal("timed out waiting for concurrent cold-round fetches")
	}
	close(releaseFetch)
	workers.Wait()
	close(results)
	for err := range results {
		require.NoError(t, err)
	}

	assert.Equal(t, enqueueCount, s.Status()["cold-round"].Total)
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

func TestMarkSubmittedUpdateFailureRestoresReceivedSchedule(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().Truncate(time.Second)
	s.now = func() time.Time { return now }
	enqueueAndRequireInserted(t, s, testPayload("round1", 0))
	require.Len(t, s.TakeReadyBatch(1), 1)
	_, err := s.db.Exec(`CREATE TRIGGER fail_completion BEFORE UPDATE OF state ON shares
		WHEN OLD.state = 0 AND NEW.state = 2
		BEGIN SELECT RAISE(ABORT, 'injected completion failure'); END`)
	require.NoError(t, err)

	s.MarkSubmitted("round1", 0, 1, 0)
	key := schedKey("round1", 0, 1, 0)
	assert.Equal(t, scheduleSecond(now.Add(shareSystemRetryBackoff)), s.schedule[key])
	assert.NotContains(t, s.inFlight, key)
	var state, attempts int
	err = s.db.QueryRow("SELECT state, attempts FROM shares WHERE round_id = ?", "round1").Scan(&state, &attempts)
	require.NoError(t, err)
	assert.Equal(t, int(ShareStateReceived), state)
	assert.Zero(t, attempts)

	_, err = s.db.Exec("DROP TRIGGER fail_completion")
	require.NoError(t, err)
	now = now.Add(shareSystemRetryBackoff + time.Second)
	require.Len(t, s.TakeReadyBatch(1), 1)
}

func TestMarkFailed_RetryAndPermanent(t *testing.T) {
	s := newTestStore(t)

	payload := distinctSensitivePayload("round1", 0)
	enqueueAndRequireInserted(t, s, payload)

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

	share, ok := s.loadShare("round1", 0, 1, 0)
	require.True(t, ok)
	assert.Equal(t, ShareStateFailed, share.State)
	assert.Equal(t, payload.EncShare.C1, share.Payload.EncShare.C1)
	assert.Equal(t, payload.EncShare.C2, share.Payload.EncShare.C2)
	assert.Equal(t, payload.ShareComms, share.Payload.ShareComms)
	assert.Equal(t, payload.PrimaryBlind, share.Payload.PrimaryBlind)
}

func TestMarkRetry_DoesNotSpendFailedAttempts(t *testing.T) {
	s := newTestStore(t)

	enqueueAndRequireInserted(t, s, testPayload("round1", 0))

	for i := range 6 {
		ready := s.TakeReady()
		require.Len(t, ready, 1, "retry %d", i)
		s.MarkRetry("round1", 0, 1, 0)

		s.mu.Lock()
		s.schedule[schedKey("round1", 0, 1, 0)] = time.Now().Add(-time.Second)
		s.mu.Unlock()
	}

	share, ok := s.loadShare("round1", 0, 1, 0)
	require.True(t, ok)
	assert.Equal(t, ShareStateReceived, share.State)
	assert.Equal(t, 0, share.Attempts)

	status := s.Status()
	assert.Equal(t, 1, status["round1"].Pending)
	assert.Equal(t, 0, status["round1"].Failed)
}

func TestMarkStalledRetry_BacksOffWithoutSpendingFailedAttempts(t *testing.T) {
	s := newTestStore(t)
	enqueueAndRequireInserted(t, s, testPayload("round1", 0))
	key := schedKey("round1", 0, 1, 0)
	now := time.Now()
	s.now = func() time.Time { return now }

	for retryCount, expected := range []time.Duration{
		shareSystemRetryBackoff,
		20 * time.Second,
		40 * time.Second,
		80 * time.Second,
		shareStalledRetryMaxBackoff,
		shareStalledRetryMaxBackoff,
	} {
		ready := s.TakeReady()
		require.Len(t, ready, 1, "retry %d", retryCount+1)
		s.MarkStalledRetry("round1", 0, 1, 0, uint8(retryCount+1))

		s.mu.Lock()
		next, ok := s.schedule[key]
		s.mu.Unlock()
		require.True(t, ok)
		assert.Equal(t, scheduleSecond(now.Add(expected)), next)

		s.mu.Lock()
		s.schedule[key] = time.Now().Add(-time.Second)
		s.mu.Unlock()
	}

	share, ok := s.loadShare("round1", 0, 1, 0)
	require.True(t, ok)
	assert.Equal(t, ShareStateReceived, share.State)
	assert.Equal(t, 0, share.Attempts)
}

func TestMarkRetry_PreservesFailedAttempts(t *testing.T) {
	s := newTestStore(t)

	enqueueAndRequireInserted(t, s, testPayload("round1", 0))
	key := schedKey("round1", 0, 1, 0)

	for i := range 4 {
		ready := s.TakeReady()
		require.Len(t, ready, 1, "failed attempt %d", i)
		s.MarkFailed("round1", 0, 1, 0)

		s.mu.Lock()
		s.schedule[key] = time.Now().Add(-time.Second)
		s.mu.Unlock()
	}

	share, ok := s.loadShare("round1", 0, 1, 0)
	require.True(t, ok)
	require.Equal(t, ShareStateReceived, share.State)
	require.Equal(t, 4, share.Attempts)

	for i := range 2 {
		ready := s.TakeReady()
		require.Len(t, ready, 1, "system retry %d", i)
		s.MarkRetry("round1", 0, 1, 0)

		share, ok := s.loadShare("round1", 0, 1, 0)
		require.True(t, ok)
		assert.Equal(t, ShareStateReceived, share.State)
		assert.Equal(t, 4, share.Attempts)

		s.mu.Lock()
		next, ok := s.schedule[key]
		s.mu.Unlock()
		require.True(t, ok)
		assert.True(t, time.Until(next) > 0)

		s.mu.Lock()
		s.schedule[key] = time.Now().Add(-time.Second)
		s.mu.Unlock()
	}

	ready := s.TakeReady()
	require.Len(t, ready, 1)
	s.MarkFailed("round1", 0, 1, 0)

	share, ok = s.loadShare("round1", 0, 1, 0)
	require.True(t, ok)
	assert.Equal(t, ShareStateFailed, share.State)
	assert.Equal(t, 5, share.Attempts)
}

func TestRetrySchedulesUseCommonSecondBucket(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().Truncate(time.Second).Add(900 * time.Millisecond)
	for i := range uint32(4) {
		payload := testPayload("round1", i)
		payload.TreePosition = uint64(i)
		enqueueAndRequireInserted(t, s, payload)
	}
	ready := s.TakeReadyBatch(4)
	require.Len(t, ready, 4)
	s.now = func() time.Time { return now }

	for _, share := range ready[:2] {
		s.MarkRetry("round1", share.Payload.EncShare.ShareIndex, 1, share.Payload.TreePosition)
	}
	for _, share := range ready[2:] {
		s.MarkFailed("round1", share.Payload.EncShare.ShareIndex, 1, share.Payload.TreePosition)
	}

	assert.Equal(t,
		s.schedule[schedKey("round1", ready[0].Payload.EncShare.ShareIndex, 1, ready[0].Payload.TreePosition)],
		s.schedule[schedKey("round1", ready[1].Payload.EncShare.ShareIndex, 1, ready[1].Payload.TreePosition)],
	)
	assert.Equal(t,
		s.schedule[schedKey("round1", ready[2].Payload.EncShare.ShareIndex, 1, ready[2].Payload.TreePosition)],
		s.schedule[schedKey("round1", ready[3].Payload.EncShare.ShareIndex, 1, ready[3].Payload.TreePosition)],
	)
}

func TestMarkFailed_UpdateFailureRestoresReceivedSchedule(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().Truncate(time.Second)
	s.now = func() time.Time { return now }
	payload := testPayload("round1", 0)
	enqueueAndRequireInserted(t, s, payload)
	require.Len(t, s.TakeReadyBatch(1), 1)
	_, err := s.db.Exec(`CREATE TRIGGER fail_mark_failed BEFORE UPDATE OF state ON shares
		WHEN OLD.state = 0 AND NEW.state = 0
		BEGIN SELECT RAISE(ABORT, 'injected failure accounting error'); END`)
	require.NoError(t, err)

	s.MarkFailed("round1", 0, 1, 0)
	key := schedKey("round1", 0, 1, 0)
	assert.Equal(t, scheduleSecond(now.Add(shareSystemRetryBackoff)), s.schedule[key])
	var state, attempts int
	err = s.db.QueryRow("SELECT state, attempts FROM shares WHERE round_id = ?", "round1").Scan(&state, &attempts)
	require.NoError(t, err)
	assert.Equal(t, int(ShareStateReceived), state)
	assert.Zero(t, attempts)
	assert.NotContains(t, s.inFlight, key)

	_, err = s.db.Exec("DROP TRIGGER fail_mark_failed")
	require.NoError(t, err)
	now = now.Add(shareSystemRetryBackoff + time.Second)
	require.Len(t, s.TakeReadyBatch(1), 1, "retained received row must be taken again")
}

func TestMarkRetry_SchedulesUrgentlyNearVoteEnd(t *testing.T) {
	now := time.Now()
	voteEndTime := uint64(now.Add(5 * time.Second).Unix())
	fetcher := func(roundID string) (RoundInfo, error) {
		return RoundInfo{CreatedAtTime: uint64(now.Add(-time.Hour).Unix()), VoteEndTime: voteEndTime}, nil
	}
	s, err := NewShareStore(":memory:", fetcher)
	require.NoError(t, err)
	defer s.Close()
	s.now = func() time.Time { return now }

	enqueueAndRequireInserted(t, s, testPayload("round1", 0))
	ready := s.TakeReady()
	require.Len(t, ready, 1)
	s.MarkRetry("round1", 0, 1, 0)

	s.mu.Lock()
	next, ok := s.schedule[schedKey("round1", 0, 1, 0)]
	s.mu.Unlock()
	require.True(t, ok)
	assert.Equal(t, scheduleSecond(nextShareSystemRetryTime(now, voteEndTime)), next)
}

func TestMarkRetry_SchedulesUrgentlyWhenBackoffWouldLeaveLittleTime(t *testing.T) {
	now := time.Now()
	voteEndTime := uint64(now.Add(shareSystemRetryBackoff + shareSystemRetryDeadlineBuffer/2).Unix())
	fetcher := func(roundID string) (RoundInfo, error) {
		return RoundInfo{CreatedAtTime: uint64(now.Add(-time.Hour).Unix()), VoteEndTime: voteEndTime}, nil
	}
	s, err := NewShareStore(":memory:", fetcher)
	require.NoError(t, err)
	defer s.Close()
	s.now = func() time.Time { return now }

	enqueueAndRequireInserted(t, s, testPayload("round1", 0))
	ready := s.TakeReady()
	require.Len(t, ready, 1)
	s.MarkRetry("round1", 0, 1, 0)

	s.mu.Lock()
	next, ok := s.schedule[schedKey("round1", 0, 1, 0)]
	s.mu.Unlock()
	require.True(t, ok)
	assert.Equal(t, scheduleSecond(nextShareSystemRetryTime(now, voteEndTime)), next)
}

func TestMarkRetry_UsesBackoffBeforeVoteEnd(t *testing.T) {
	s := newTestStore(t)
	now := time.Now()
	s.now = func() time.Time { return now }

	enqueueAndRequireInserted(t, s, testPayload("round1", 0))
	ready := s.TakeReady()
	require.Len(t, ready, 1)

	s.MarkRetry("round1", 0, 1, 0)

	s.mu.Lock()
	next, ok := s.schedule[schedKey("round1", 0, 1, 0)]
	s.mu.Unlock()
	require.True(t, ok)
	assert.Equal(t, scheduleSecond(now.Add(shareSystemRetryBackoff)), next)
}

func TestMarkRetry_PreservesSubsecondBackoffNearVoteEnd(t *testing.T) {
	now := time.Unix(1000, 900*int64(time.Millisecond))
	voteEndTime := uint64(now.Add(100 * time.Millisecond).Unix())
	fetcher := func(roundID string) (RoundInfo, error) {
		return RoundInfo{CreatedAtTime: uint64(now.Add(-time.Hour).Unix()), VoteEndTime: voteEndTime}, nil
	}

	tests := []struct {
		name              string
		stalledRetryCount uint8
	}{
		{name: "system retry"},
		{name: "stalled retry", stalledRetryCount: 1},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			s, err := NewShareStore(":memory:", fetcher)
			require.NoError(t, err)
			defer s.Close()
			s.now = func() time.Time { return now }

			enqueueAndRequireInserted(t, s, testPayload("round1", 0))
			require.Len(t, s.TakeReady(), 1)
			if test.stalledRetryCount == 0 {
				s.MarkRetry("round1", 0, 1, 0)
			} else {
				s.MarkStalledRetry("round1", 0, 1, 0, test.stalledRetryCount)
			}

			next, ok := s.NextScheduledTime()
			require.True(t, ok)
			assert.True(t, next.After(now), "retry must retain a positive backoff")
			assert.Equal(t, now.Add(50*time.Millisecond), next)
			assert.Empty(t, s.TakeReady(), "retry must not be immediately redispatched")
		})
	}
}

func TestNextShareSystemRetryTime_StaysBeforeDeadline(t *testing.T) {
	now := time.Unix(1000, 0)
	next := nextShareSystemRetryTime(now, uint64(now.Add(time.Second).Unix()))

	assert.Equal(t, now.Add(500*time.Millisecond), next)
}

func TestNextShareSystemRetryTime_HalvesRemainingNearDeadline(t *testing.T) {
	now := time.Unix(1000, 0)
	next := nextShareSystemRetryTime(now, uint64(now.Add(3*time.Second).Unix()))

	assert.Equal(t, now.Add(1500*time.Millisecond), next)
}

func TestPollingBackoffAcrossDeadline(t *testing.T) {
	deadline := time.Unix(1000, 0)
	for _, tc := range []struct {
		name      string
		remaining time.Duration
		want      time.Duration
	}{
		{"before urgent window", 35 * time.Second, 5 * time.Second},
		{"urgent window start", 30 * time.Second, 2 * time.Second},
		{"before deadline", time.Second, 500 * time.Millisecond},
		{"last clock tick", time.Nanosecond, time.Nanosecond},
		{"at deadline", 0, shareSystemRetryBackoff},
		{"after deadline", -time.Hour, shareSystemRetryBackoff},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := deadline.Add(-tc.remaining)
			assert.Equal(t, now.Add(tc.want), nextShareSystemRetryTime(now, uint64(deadline.Unix())))
			for _, retryCount := range []uint8{1, 2, shareStalledRetryMaxCount} {
				assert.Equal(t, now.Add(tc.want), nextShareStalledRetryTime(now, uint64(deadline.Unix()), retryCount))
			}
		})
	}
}

func TestNextShareStalledRetryTime_BackoffAndCap(t *testing.T) {
	now := time.Unix(1000, 0)
	tests := []struct {
		retryCount uint8
		delay      time.Duration
	}{
		{retryCount: 1, delay: 10 * time.Second},
		{retryCount: 2, delay: 20 * time.Second},
		{retryCount: 3, delay: 40 * time.Second},
		{retryCount: 4, delay: 80 * time.Second},
		{retryCount: 5, delay: 2 * time.Minute},
		{retryCount: 255, delay: 2 * time.Minute},
	}

	for _, test := range tests {
		assert.Equal(t, now.Add(test.delay), nextShareStalledRetryTime(now, 0, test.retryCount))
	}
}

func TestNextShareStalledRetryTime_LandsAtUrgentWindow(t *testing.T) {
	now := time.Unix(1000, 0)
	deadline := now.Add(100 * time.Second)

	next := nextShareStalledRetryTime(now, uint64(deadline.Unix()), shareStalledRetryMaxCount)

	assert.Equal(t, deadline.Add(-shareSystemRetryDeadlineBuffer), next)
}

func TestNextShareStalledRetryTime_UsesUrgentBackoffInsideWindow(t *testing.T) {
	now := time.Unix(1000, 0)
	deadline := now.Add(20 * time.Second)

	next := nextShareStalledRetryTime(now, uint64(deadline.Unix()), shareStalledRetryMaxCount)

	assert.Equal(t, now.Add(shareSystemRetryUrgentBackoff), next)
}

func TestMarkFailed_PermanentUnknownVoteEndTimeScrubsWitnessMaterial(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "helper.db")
	now := uint64(time.Now().Unix())
	fetcher := func(roundID string) (RoundInfo, error) {
		return RoundInfo{CreatedAtTime: now, VoteEndTime: now + testVoteEndOffset}, nil
	}

	s, err := NewShareStore(dbPath, fetcher)
	require.NoError(t, err)
	defer s.Close()

	payload := distinctSensitivePayload("round1", 0)
	enqueueAndRequireInserted(t, s, payload)
	_, err = s.db.Exec(
		"UPDATE shares SET vote_end_time = 0 WHERE round_id = ? AND share_index = ? AND proposal_id = ? AND tree_position = ?",
		payload.VoteRoundID,
		payload.EncShare.ShareIndex,
		payload.ProposalID,
		payload.TreePosition,
	)
	require.NoError(t, err)

	walPath := dbPath + "-wal"
	walBefore, err := os.ReadFile(walPath)
	require.NoError(t, err)
	require.True(t, containsSensitiveField(walBefore, payload), "expected WAL to contain unknown-time witness material before permanent scrub")

	failSharePermanently(t, s, payload)

	share, ok := s.loadShare(payload.VoteRoundID, payload.EncShare.ShareIndex, payload.ProposalID, payload.TreePosition)
	require.True(t, ok)
	assert.Equal(t, ShareStateFailed, share.State)
	assert.Empty(t, share.Payload.EncShare.C1)
	assert.Empty(t, share.Payload.EncShare.C2)
	assert.Empty(t, share.Payload.ShareComms)
	assert.Empty(t, share.Payload.PrimaryBlind)

	walAfter, err := os.ReadFile(walPath)
	if os.IsNotExist(err) {
		return
	}
	require.NoError(t, err)
	assert.False(t, containsSensitiveField(walAfter, payload), "unknown-time witness material should not remain in WAL after permanent scrub")
}

// failSharePermanently advances a queued test share through all retry attempts.
func failSharePermanently(t *testing.T, s *ShareStore, payload SharePayload) {
	t.Helper()

	key := schedKey(payload.VoteRoundID, payload.EncShare.ShareIndex, payload.ProposalID, payload.TreePosition)
	for attempt := 0; attempt < 5; attempt++ {
		ready := s.TakeReady()
		require.Len(t, ready, 1, "attempt %d", attempt)
		assert.Equal(t, payload.VoteRoundID, ready[0].Payload.VoteRoundID)
		assert.Equal(t, payload.EncShare.ShareIndex, ready[0].Payload.EncShare.ShareIndex)
		assert.Equal(t, payload.ProposalID, ready[0].Payload.ProposalID)
		assert.Equal(t, payload.TreePosition, ready[0].Payload.TreePosition)

		s.MarkFailed(payload.VoteRoundID, payload.EncShare.ShareIndex, payload.ProposalID, payload.TreePosition)
		if attempt < 4 {
			s.mu.Lock()
			s.schedule[key] = time.Now().Add(-time.Second)
			s.mu.Unlock()
		}
	}
}

func TestStatus(t *testing.T) {
	s := newTestStore(t)

	ready := testPayload("round1", 0)
	future := testPayload("round1", 1)
	future.TreePosition = 1
	future.SubmitAt = uint64(time.Now().Add(time.Hour).Unix())
	enqueueAndRequireInserted(t, s, ready)
	enqueueAndRequireInserted(t, s, future)

	status := s.Status()
	assert.Equal(t, 2, status["round1"].Total)
	assert.Equal(t, 2, status["round1"].Pending)
	assert.Equal(t, 1, status["round1"].Ready)
	assert.Equal(t, 1, status["round1"].NotYetDue)
	assert.Equal(t, 0, status["round1"].Processing)

	taken := s.TakeReady()
	require.Len(t, taken, 1)
	status = s.Status()
	assert.Equal(t, 0, status["round1"].Ready)
	assert.Equal(t, 1, status["round1"].NotYetDue)
	assert.Equal(t, 1, status["round1"].Processing)

	s.MarkRetry("round1", 0, 1, 0)
	status = s.Status()
	assert.Equal(t, 0, status["round1"].Ready)
	assert.Equal(t, 2, status["round1"].NotYetDue)
	assert.Equal(t, 0, status["round1"].Processing)
	assert.Equal(t, status["round1"].Pending, status["round1"].Ready+status["round1"].NotYetDue+status["round1"].Processing)
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

	_, err = s.QueueSummary(roundID, time.Unix(int64(start), 0))
	require.ErrorIs(t, err, ErrInvalidRoundInfo)
}

func TestQueueSummaryLastMinuteStartPolicy(t *testing.T) {
	start := uint64(1700000000)
	assert.Equal(t, start+6*60, lastMinuteWindowStart(start, start+10*60))
	assert.Equal(t, start+36*60, lastMinuteWindowStart(start, start+60*60))
	assert.Equal(t, start+2*3600-48*60, lastMinuteWindowStart(start, start+2*3600))
	assert.Equal(t, start+7*24*3600-6*3600, lastMinuteWindowStart(start, start+7*24*3600))
	assert.Equal(t, start, lastMinuteWindowStart(start, start))
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
	assert.Equal(t, 1, summary.Buckets[2].Submitted)
	assert.Equal(t, 1, summary.Buckets[4].PendingFuture)
	assert.Equal(t, 1, summary.Buckets[5].Failed)

	total := 0
	for _, bucket := range summary.Buckets {
		total += bucket.Total
	}
	assert.Equal(t, 6, total)
}

func TestQueueSummaryDepthUsesEffectiveSchedule(t *testing.T) {
	s := newTestStore(t)
	now := time.Now()

	ready := testPayload("round1", 0)
	future := testPayload("round1", 1)
	future.TreePosition = 1
	future.SubmitAt = uint64(now.Add(time.Hour).Unix())
	enqueueAndRequireInserted(t, s, ready)
	enqueueAndRequireInserted(t, s, future)

	taken := s.TakeReady()
	require.Len(t, taken, 1)
	s.MarkRetry("round1", 0, 1, 0)

	summary, err := s.QueueSummary("round1", now)
	require.NoError(t, err)
	assert.Equal(t, 0, summary.Ready)
	assert.Equal(t, 2, summary.NotYetDue)
	assert.Equal(t, 0, summary.Processing)

	s.mu.Lock()
	s.schedule[schedKey("round1", 0, 1, 0)] = now.Add(-time.Second)
	s.mu.Unlock()

	summary, err = s.QueueSummary("round1", now)
	require.NoError(t, err)
	assert.Equal(t, 1, summary.Ready)
	assert.Equal(t, 1, summary.NotYetDue)
	assert.Equal(t, 0, summary.Processing)

	taken = s.TakeReady()
	require.Len(t, taken, 1)
	summary, err = s.QueueSummary("round1", time.Now())
	require.NoError(t, err)
	assert.Equal(t, 0, summary.Ready)
	assert.Equal(t, 1, summary.NotYetDue)
	assert.Equal(t, 1, summary.Processing)
}

func TestQueueSummaryReportsCurrentBucketStates(t *testing.T) {
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
	assert.Equal(t, 1, currentBucket.Submitted)
	assert.Equal(t, 1, currentBucket.PendingFuture)
	assert.Equal(t, 1, currentBucket.OverduePending)
	assert.Equal(t, 1, currentBucket.Processing)
	assert.Equal(t, 1, currentBucket.Failed)
	assert.Equal(t, 5, currentBucket.Total)

	futureBucket := current.Buckets[4]
	assert.Equal(t, 1, futureBucket.Submitted)
	assert.Equal(t, 0, futureBucket.PendingFuture)
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

func TestEnqueue_ReplacesUnownedCorruptRow(t *testing.T) {
	s := newTestStore(t)
	payload := testPayload("round1", 0)
	enqueueAndRequireInserted(t, s, payload)
	_, err := s.db.Exec(
		"UPDATE shares SET share_comms = 'not-json', attempts = 4 WHERE round_id = ?",
		payload.VoteRoundID,
	)
	require.NoError(t, err)

	result, err := s.Enqueue(payload)
	require.NoError(t, err)
	assert.Equal(t, EnqueueInserted, result)
	assert.Len(t, s.Status(), 1)
	assert.Equal(t, 1, s.Status()[payload.VoteRoundID].Total)

	repaired, ok := s.loadShare(payload.VoteRoundID, payload.EncShare.ShareIndex, payload.ProposalID, payload.TreePosition)
	require.True(t, ok)
	assert.True(t, payloadEqual(repaired.Payload, payload))
	assert.Equal(t, ShareStateReceived, repaired.State)
	assert.Zero(t, repaired.Attempts)
	assert.Contains(t, s.schedule, schedKey("round1", 0, 1, 0))
}

func TestEnqueue_ReplacesTerminalizedCorruptRow(t *testing.T) {
	s := newTestStore(t)
	payload := testPayload("round1", 0)
	enqueueAndRequireInserted(t, s, payload)
	_, err := s.db.Exec("UPDATE shares SET share_comms = 'not-json' WHERE round_id = ?", payload.VoteRoundID)
	require.NoError(t, err)
	assert.Empty(t, s.TakeReadyBatch(1))

	var state int
	err = s.db.QueryRow("SELECT state FROM shares WHERE round_id = ?", payload.VoteRoundID).Scan(&state)
	require.NoError(t, err)
	require.Equal(t, int(ShareStateFailed), state)

	result, err := s.Enqueue(payload)
	require.NoError(t, err)
	assert.Equal(t, EnqueueInserted, result)
	ready := s.TakeReadyBatch(1)
	require.Len(t, ready, 1)
	assert.True(t, payloadEqual(ready[0].Payload, payload))
	assert.Zero(t, ready[0].Attempts)
}

func TestEnqueue_DoesNotReplaceActiveCorruptRow(t *testing.T) {
	s := newTestStore(t)
	payload := testPayload("round1", 0)
	enqueueAndRequireInserted(t, s, payload)
	require.Len(t, s.TakeReadyBatch(1), 1)
	key := schedKey("round1", 0, 1, 0)
	_, err := s.db.Exec("UPDATE shares SET share_comms = 'not-json' WHERE round_id = ?", payload.VoteRoundID)
	require.NoError(t, err)

	result, err := s.Enqueue(payload)
	require.NoError(t, err)
	assert.Equal(t, EnqueueDuplicate, result)
	assert.Contains(t, s.inFlight, key)
	assert.NotContains(t, s.schedule, key)

	s.MarkSubmitted("round1", 0, 1, 0)
	var state int
	err = s.db.QueryRow("SELECT state FROM shares WHERE round_id = ?", payload.VoteRoundID).Scan(&state)
	require.NoError(t, err)
	assert.Equal(t, int(ShareStateSubmitted), state)
}

func TestEnqueue_DoesNotResurrectSubmittedCorruptRow(t *testing.T) {
	s := newTestStore(t)
	payload := testPayload("round1", 0)
	enqueueAndRequireInserted(t, s, payload)
	require.Len(t, s.TakeReadyBatch(1), 1)
	s.MarkSubmitted("round1", 0, 1, 0)
	_, err := s.db.Exec("UPDATE shares SET share_comms = 'not-json' WHERE round_id = ?", payload.VoteRoundID)
	require.NoError(t, err)

	result, err := s.Enqueue(payload)
	require.NoError(t, err)
	assert.Equal(t, EnqueueDuplicate, result)
	assert.NotContains(t, s.schedule, schedKey("round1", 0, 1, 0))
	assert.Equal(t, 1, s.Status()[payload.VoteRoundID].Submitted)
}

func TestEnqueue_DoesNotReplaceCorruptRowWithUnreadableState(t *testing.T) {
	s := newTestStore(t)
	payload := testPayload("round1", 0)
	enqueueAndRequireInserted(t, s, payload)
	key := schedKey("round1", 0, 1, 0)
	scheduledBefore := s.schedule[key]
	_, err := s.db.Exec(
		"UPDATE shares SET share_comms = 'not-json', state = '2x' WHERE round_id = ?",
		payload.VoteRoundID,
	)
	require.NoError(t, err)

	result, err := s.Enqueue(payload)
	require.NoError(t, err)
	assert.Equal(t, EnqueueConflict, result)
	assert.Equal(t, scheduledBefore, s.schedule[key])
	assert.Empty(t, s.inFlight)

	var state, comms string
	err = s.db.QueryRow("SELECT state, share_comms FROM shares WHERE round_id = ?", payload.VoteRoundID).Scan(&state, &comms)
	require.NoError(t, err)
	assert.Equal(t, "2x", state)
	assert.Equal(t, "not-json", comms)
}

func TestEnqueue_CorruptRowReplacementFailurePreservesRow(t *testing.T) {
	s := newTestStore(t)
	payload := testPayload("round1", 0)
	enqueueAndRequireInserted(t, s, payload)
	_, err := s.db.Exec("UPDATE shares SET share_comms = 'not-json' WHERE round_id = ?", payload.VoteRoundID)
	require.NoError(t, err)
	assert.Empty(t, s.TakeReadyBatch(1))
	_, err = s.db.Exec(`CREATE TRIGGER fail_corrupt_replacement BEFORE UPDATE ON shares
		WHEN OLD.state = 3 AND NEW.state = 0
		BEGIN SELECT RAISE(ABORT, 'injected replacement failure'); END`)
	require.NoError(t, err)

	result, err := s.Enqueue(payload)
	require.Error(t, err)
	assert.Equal(t, EnqueueConflict, result)
	assert.Contains(t, err.Error(), "replace corrupt share")
	assert.NotContains(t, s.schedule, schedKey("round1", 0, 1, 0))

	var state int
	var comms string
	err = s.db.QueryRow("SELECT state, share_comms FROM shares WHERE round_id = ?", payload.VoteRoundID).Scan(&state, &comms)
	require.NoError(t, err)
	assert.Equal(t, int(ShareStateFailed), state)
	assert.Equal(t, "not-json", comms)
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

	// Take the share. Ownership is process-local and durable state stays Received.
	ready := s1.TakeReady()
	require.Len(t, ready, 1)
	share, ok := s1.loadShare("round1", 0, 1, 0)
	require.True(t, ok)
	assert.Equal(t, ShareStateReceived, share.State)

	// Close without marking submitted (simulates crash).
	s1.Close()

	// Reopen: recovery schedules the still-Received row with the same submit_at.
	s2, err := NewShareStore(dbPath, fetcher)
	require.NoError(t, err)
	defer s2.Close()

	ready = s2.TakeReady()
	assert.Len(t, ready, 1, "recovered share should be ready again")
}

func TestRecovery_ScanFailureDoesNotSilentlyStrandShare(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "helper_test.db")
	now := uint64(time.Now().Unix())
	fetcher := func(string) (RoundInfo, error) {
		return RoundInfo{CreatedAtTime: now, VoteEndTime: now + testVoteEndOffset}, nil
	}
	s, err := NewShareStore(dbPath, fetcher)
	require.NoError(t, err)
	enqueueAndRequireInserted(t, s, testPayload("round1", 0))
	require.NoError(t, s.Close())

	db, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	_, err = db.Exec("UPDATE shares SET submit_at = 'invalid'")
	require.NoError(t, err)
	require.NoError(t, db.Close())

	_, err = NewShareStore(dbPath, fetcher)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "scan recoverable share")
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

func TestRecovery_PreservesClampedAndOriginalSubmitAt(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "helper_test.db")
	now := uint64(time.Now().Unix())
	requested := now - 600
	fetcher := func(string) (RoundInfo, error) {
		return RoundInfo{CreatedAtTime: now - oneHourSecs, VoteEndTime: now + oneHourSecs}, nil
	}

	s1, err := NewShareStore(dbPath, fetcher)
	require.NoError(t, err)
	payload := testPayload("round1", 0)
	payload.SubmitAt = requested
	enqueueAndRequireInserted(t, s1, payload)

	var effective uint64
	err = s1.db.QueryRow("SELECT submit_at FROM shares WHERE round_id = ?", payload.VoteRoundID).Scan(&effective)
	require.NoError(t, err)
	require.Greater(t, effective, requested)
	require.NoError(t, s1.Close())

	s2, err := NewShareStore(dbPath, fetcher)
	require.NoError(t, err)
	defer s2.Close()

	ready := s2.TakeReadyBatch(1)
	require.Len(t, ready, 1)
	assert.Equal(t, effective, ready[0].Payload.SubmitAt)
	s2.MarkRetry(payload.VoteRoundID, payload.EncShare.ShareIndex, payload.ProposalID, payload.TreePosition)
	exported, err := s2.ExportQueue(payload.VoteRoundID, time.Now())
	require.NoError(t, err)
	require.Len(t, exported.Rows, 1)
	assert.Equal(t, effective, exported.Rows[0].SubmitAt)
	assert.Equal(t, requested, exported.Rows[0].OriginalSubmitAt)
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

	t.Run("submit_at at vote_end_time rejected", func(t *testing.T) {
		p := testPayload("round2", 0)
		p.SubmitAt = voteEndTime
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

		var submitAt, originalSubmitAt uint64
		err = s.db.QueryRow(
			"SELECT submit_at, original_submit_at FROM shares WHERE round_id = ?",
			p.VoteRoundID,
		).Scan(&submitAt, &originalSubmitAt)
		require.NoError(t, err)
		assert.Equal(t, p.SubmitAt, submitAt)
		assert.Equal(t, p.SubmitAt, originalSubmitAt)
	})
}

func TestEnqueue_ClampsPastSubmitAtToArrival(t *testing.T) {
	now := uint64(time.Now().Unix())
	requested := now - 600
	store, err := NewShareStore(":memory:", func(string) (RoundInfo, error) {
		return RoundInfo{CreatedAtTime: now - oneHourSecs, VoteEndTime: now + oneHourSecs}, nil
	})
	require.NoError(t, err)
	defer store.Close()

	payload := testPayload("round-past", 0)
	payload.SubmitAt = requested
	result, err := store.Enqueue(payload)
	require.NoError(t, err)
	require.Equal(t, EnqueueInserted, result)

	var submitAt, originalSubmitAt, receivedAt uint64
	err = store.db.QueryRow(
		"SELECT submit_at, original_submit_at, received_at FROM shares WHERE round_id = ?",
		payload.VoteRoundID,
	).Scan(&submitAt, &originalSubmitAt, &receivedAt)
	require.NoError(t, err)
	assert.Equal(t, requested, originalSubmitAt)
	assert.Equal(t, receivedAt, submitAt)
	assert.GreaterOrEqual(t, receivedAt, now)

	ready := store.TakeReadyBatch(1)
	require.Len(t, ready, 1)
	assert.Equal(t, submitAt, ready[0].Payload.SubmitAt)

	// The stored effective timestamp differs from the request, but an exact
	// payload retry remains idempotent.
	result, err = store.Enqueue(payload)
	require.NoError(t, err)
	assert.Equal(t, EnqueueDuplicate, result)
}

func TestEnqueue_ImmediateAndClampedShareScheduleBucket(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().Add(time.Second).Truncate(time.Second).Add(987_654_321)
	s.now = func() time.Time { return now }
	immediate := testPayload("round-a", 0)
	clamped := testPayload("round-a", 1)
	clamped.TreePosition = 1
	clamped.SubmitAt = uint64(now.Add(-time.Hour).Unix())
	enqueueAndRequireInserted(t, s, immediate)
	enqueueAndRequireInserted(t, s, clamped)

	s.mu.Lock()
	immediateAt := s.schedule[schedKey("round-a", 0, 1, 0)]
	clampedAt := s.schedule[schedKey("round-a", 1, 1, 1)]
	s.mu.Unlock()
	assert.Equal(t, time.Unix(now.Unix(), 0), immediateAt)
	assert.Equal(t, immediateAt, clampedAt)
}

func TestTakeReadyBatch_EqualSecondUsesOpaqueRank(t *testing.T) {
	s := newTestStore(t)
	s.schedulingSecret = [32]byte{1, 2, 3, 4}
	now := time.Unix(2_000_000_000, 500)
	s.now = func() time.Time { return now }

	const count = 12
	keys := make([]string, 0, count)
	for i := range uint32(count) {
		payload := testPayload("round-a", i)
		payload.TreePosition = uint64(i)
		enqueueAndRequireInserted(t, s, payload)
		keys = append(keys, schedKey("round-a", i, 1, uint64(i)))
	}
	insertionOrder := append([]string(nil), keys...)
	sort.Slice(keys, func(i, j int) bool {
		macI := hmac.New(sha256.New, s.schedulingSecret[:])
		_, err := macI.Write([]byte("helper-schedule-v1:" + keys[i]))
		require.NoError(t, err)
		macJ := hmac.New(sha256.New, s.schedulingSecret[:])
		_, err = macJ.Write([]byte("helper-schedule-v1:" + keys[j]))
		require.NoError(t, err)
		return bytes.Compare(macI.Sum(nil), macJ.Sum(nil)) < 0
	})
	assert.NotEqual(t, insertionOrder, keys, "fixed secret must not restore insertion order")

	ready := s.TakeReadyBatch(count)
	require.Len(t, ready, count)
	for i, share := range ready {
		assert.Equal(t, keys[i], schedKey(
			share.Payload.VoteRoundID,
			share.Payload.EncShare.ShareIndex,
			share.Payload.ProposalID,
			share.Payload.TreePosition,
		))
	}
}

func TestEnqueue_LaterSecondCannotJumpEarlierImmediate(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().Truncate(time.Second)
	s.now = func() time.Time { return now }
	first := testPayload("round-a", 0)
	enqueueAndRequireInserted(t, s, first)

	now = now.Add(time.Second)
	second := testPayload("round-a", 1)
	second.TreePosition = 1
	second.SubmitAt = uint64(now.Add(-time.Hour).Unix())
	enqueueAndRequireInserted(t, s, second)

	ready := s.TakeReadyBatch(2)
	require.Len(t, ready, 2)
	assert.Equal(t, uint32(0), ready[0].Payload.EncShare.ShareIndex)
	assert.Equal(t, uint32(1), ready[1].Payload.EncShare.ShareIndex)
}

func TestRecovery_ImmediateRowsShareOneOpaqueBucket(t *testing.T) {
	s := newTestStore(t)
	for i := range uint32(3) {
		payload := testPayload("round-a", i)
		payload.TreePosition = uint64(i)
		enqueueAndRequireInserted(t, s, payload)
	}
	s.schedule = make(map[string]time.Time)
	base := time.Unix(2_000_000_000, 0)
	calls := 0
	s.now = func() time.Time {
		calls++
		return base.Add(time.Duration(calls) * time.Second)
	}
	require.NoError(t, s.recover())
	require.Equal(t, 1, calls)

	var bucket time.Time
	for _, at := range s.schedule {
		if bucket.IsZero() {
			bucket = at
		}
		assert.Equal(t, bucket, at)
	}
}

func TestPurgeRounds(t *testing.T) {
	end := uint64(time.Now().Add(-time.Hour).Unix())
	fetcher := func(string) (RoundInfo, error) {
		return RoundInfo{CreatedAtTime: end - oneHourSecs, VoteEndTime: end}, nil
	}

	s, err := NewShareStore(":memory:", fetcher)
	require.NoError(t, err)
	defer s.Close()

	// Both rounds are past the local deadline; only one is confirmed closed.
	enqueueAndRequireInserted(t, s, testPayload("expired_round", 0))
	enqueueAndRequireInserted(t, s, testPayload("active_round", 0))

	status := s.Status()
	assert.Equal(t, 1, status["expired_round"].Total)
	assert.Equal(t, 1, status["active_round"].Total)

	candidates, err := s.ExpiredRoundIDs(time.Now())
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"expired_round", "active_round"}, candidates)
	require.Zero(t, s.PurgeRounds(nil))
	require.Len(t, s.schedule, 2)
	deleted := s.PurgeRounds([]string{"expired_round"})
	assert.Equal(t, int64(1), deleted)

	status = s.Status()
	assert.Equal(t, 0, status["expired_round"].Total)
	assert.Equal(t, 1, status["active_round"].Total)
}

func TestPurgeRoundsTruncatesWALWithFailedWitnessMaterial(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "helper.db")
	end := uint64(time.Now().Add(-time.Hour).Unix())
	fetcher := func(roundID string) (RoundInfo, error) {
		return RoundInfo{CreatedAtTime: end - oneHourSecs, VoteEndTime: end}, nil
	}

	s, err := NewShareStore(dbPath, fetcher)
	require.NoError(t, err)
	defer s.Close()

	failed := distinctSensitivePayload("expired_round", 0)
	enqueueAndRequireInserted(t, s, failed)
	failSharePermanently(t, s, failed)

	walPath := dbPath + "-wal"
	walBefore, err := os.ReadFile(walPath)
	require.NoError(t, err)
	require.True(t, containsSensitiveField(walBefore, failed), "expected WAL to contain failed-row witness material before purge")

	blockerDB, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	defer blockerDB.Close()
	blockerTx, err := blockerDB.Begin()
	require.NoError(t, err)
	var rowCount int
	require.NoError(t, blockerTx.QueryRow("SELECT COUNT(*) FROM shares").Scan(&rowCount))
	require.Equal(t, 1, rowCount)

	deleted := s.PurgeRounds([]string{"expired_round"})
	assert.Equal(t, int64(1), deleted)
	_, ok := s.loadShare(failed.VoteRoundID, failed.EncShare.ShareIndex, failed.ProposalID, failed.TreePosition)
	assert.False(t, ok)

	walAfterBlockedCheckpoint, err := os.ReadFile(walPath)
	require.NoError(t, err)
	assert.True(t, containsSensitiveField(walAfterBlockedCheckpoint, failed), "blocked checkpoint should leave cleanup for a later purge pass")
	require.NoError(t, blockerTx.Rollback())

	deleted = s.PurgeRounds(nil)
	assert.Equal(t, int64(0), deleted)

	walAfter, err := os.ReadFile(walPath)
	if os.IsNotExist(err) {
		return
	}
	require.NoError(t, err)
	assert.False(t, containsSensitiveField(walAfter, failed), "failed-row witness material should not remain in WAL after purge")
}

func TestExpiredRoundSummaries(t *testing.T) {
	end := uint64(time.Now().Add(-time.Hour).Unix())
	fetcher := func(roundID string) (RoundInfo, error) {
		return RoundInfo{CreatedAtTime: end - oneHourSecs, VoteEndTime: end}, nil
	}

	s, err := NewShareStore(":memory:", fetcher)
	require.NoError(t, err)
	defer s.Close()

	enqueueAndRequireInserted(t, s, testPayload("expired_round", 0))
	enqueueAndRequireInserted(t, s, testPayload("expired_round", 1))
	_, err = s.db.Exec("UPDATE shares SET received_at = ?", end-1)
	require.NoError(t, err)

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

func TestExpiredRoundSummariesIgnoresSharesReceivedAtClose(t *testing.T) {
	end := uint64(time.Now().Add(-time.Hour).Unix())
	s, err := NewShareStore(":memory:", func(roundID string) (RoundInfo, error) {
		return RoundInfo{CreatedAtTime: end - oneHourSecs, VoteEndTime: end}, nil
	})
	require.NoError(t, err)
	defer s.Close()

	enqueueAndRequireInserted(t, s, testPayload("expired_round", 0))
	_, err = s.db.Exec("UPDATE shares SET received_at = ?", end)
	require.NoError(t, err)

	summaries, err := s.ExpiredRoundSummaries(time.Now())
	require.NoError(t, err)
	assert.Empty(t, summaries)
}

func TestExpiredRoundSummariesIgnoresSharesReceivedAfterClose(t *testing.T) {
	end := uint64(time.Now().Add(-time.Hour).Unix())
	s, err := NewShareStore(":memory:", func(roundID string) (RoundInfo, error) {
		return RoundInfo{CreatedAtTime: end - oneHourSecs, VoteEndTime: end}, nil
	})
	require.NoError(t, err)
	defer s.Close()

	enqueueAndRequireInserted(t, s, testPayload("expired_round", 0))

	summaries, err := s.ExpiredRoundSummaries(time.Now())
	require.NoError(t, err)
	assert.Empty(t, summaries)
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

	hasOriginalSubmitAt, err := tableHasColumn(s.db, "shares", "original_submit_at")
	require.NoError(t, err)
	assert.True(t, hasOriginalSubmitAt)

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

	export, err := s.ExportQueue("round1", time.Unix(1234, 0))
	require.NoError(t, err)
	require.Len(t, export.Rows, 2)
	assert.Equal(t, QueueExportVersion, export.Version)
	assert.Equal(t, uint64(1234), export.ExportedAt)

	var sawPending, sawSubmitted bool
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
		}
	}
	assert.True(t, sawPending)
	assert.True(t, sawSubmitted)
}

func TestExportQueueRejectsActiveRound(t *testing.T) {
	s := newTestStore(t)
	enqueueAndRequireInserted(t, s, testPayload("round1", 0))
	require.Len(t, s.TakeReadyBatch(1), 1)

	_, err := s.ExportQueue("round1", time.Now())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "worker is active")
	assert.Equal(t, 1, s.Status()["round1"].Processing)
}

func TestExportQueueIncludesFailedRowsWithWitnessMaterial(t *testing.T) {
	s := newTestStore(t)

	failed := distinctSensitivePayload("round1", 0)
	enqueueAndRequireInserted(t, s, failed)
	failSharePermanently(t, s, failed)

	export, err := s.ExportQueue("round1", time.Now())
	require.NoError(t, err)
	require.Len(t, export.Rows, 1)

	row := export.Rows[0]
	assert.False(t, row.Processable)
	assert.Equal(t, ShareStateFailed, row.State)
	assert.Equal(t, failed.EncShare.C1, row.EncShare.C1)
	assert.Equal(t, failed.EncShare.C2, row.EncShare.C2)
	assert.Equal(t, failed.ShareComms, row.ShareComms)
	assert.Equal(t, failed.PrimaryBlind, row.PrimaryBlind)

	dest := newTestStore(t)
	result, err := dest.ImportQueue(export, QueueImportOptions{})
	require.NoError(t, err)
	assert.Equal(t, 0, result.Inserted)
	assert.Equal(t, 1, result.SkippedTerminal)
	assert.Empty(t, dest.Status())
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

	export, err := source.ExportQueue("round1", time.Now())
	require.NoError(t, err)

	dest := newTestStore(t)
	result, err := dest.ImportQueue(export, QueueImportOptions{})
	require.NoError(t, err)
	assert.Equal(t, 1, result.Inserted)
	assert.Equal(t, 1, result.SkippedTerminal)

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
	assert.Equal(t, 1, result.SkippedTerminal)
}

func TestImportQueuePreservesUnknownLegacyReceivedAt(t *testing.T) {
	end := uint64(time.Now().Add(-time.Hour).Unix())
	payload := testPayload("legacy_round", 0)
	export := QueueExport{
		Version: QueueExportVersion,
		RoundID: payload.VoteRoundID,
		Round: QueueExportRound{
			CreatedAtTime: end - oneHourSecs,
			VoteEndTime:   end,
		},
		Rows: []QueueExportRow{
			queueExportRowFromPayload(payload, ShareStateReceived, end),
		},
	}

	dest := newTestStore(t)
	result, err := dest.ImportQueue(export, QueueImportOptions{})
	require.NoError(t, err)
	require.Equal(t, 1, result.Inserted)

	var receivedAt uint64
	err = dest.db.QueryRow(
		"SELECT received_at FROM shares WHERE round_id = ? AND share_index = ? AND proposal_id = ? AND tree_position = ?",
		payload.VoteRoundID,
		payload.EncShare.ShareIndex,
		payload.ProposalID,
		payload.TreePosition,
	).Scan(&receivedAt)
	require.NoError(t, err)
	assert.Zero(t, receivedAt)

	summaries, err := dest.ExpiredRoundSummaries(time.Now())
	require.NoError(t, err)
	require.Len(t, summaries, 1)
	assert.Equal(t, 1, summaries[0].Unsubmitted())
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

func TestImportQueueRejectsSubmitAtAtVoteEndTime(t *testing.T) {
	submitAt := uint64(time.Now().Add(2 * time.Hour).Unix())
	voteEndTime := submitAt
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

func TestImportQueueForceReadyPreservesInFlightOwnership(t *testing.T) {
	payload := testPayload("round1", 0)
	export := QueueExport{
		Version: QueueExportVersion,
		RoundID: "round1",
		Rows: []QueueExportRow{
			queueExportRowFromPayload(payload, ShareStateReceived, 0),
		},
	}
	dest := newTestStore(t)

	result, err := dest.ImportQueue(export, QueueImportOptions{})
	require.NoError(t, err)
	require.Equal(t, 1, result.Inserted)
	require.Len(t, dest.TakeReadyBatch(1), 1)
	key := schedKey("round1", 0, 1, 0)
	owner := dest.inFlight[key]

	result, err = dest.ImportQueue(export, QueueImportOptions{ForceReady: true})
	require.NoError(t, err)
	assert.Equal(t, 1, result.Duplicates)
	assert.Equal(t, owner, dest.inFlight[key])
	assert.NotContains(t, dest.schedule, key)
	assert.Empty(t, dest.TakeReadyBatch(1), "force-ready must not dispatch an active share twice")

	dest.MarkSubmitted("round1", 0, 1, 0)
	share, ok := dest.loadShare("round1", 0, 1, 0)
	require.True(t, ok)
	assert.Equal(t, ShareStateSubmitted, share.State)
}

func TestImportQueue_ImmediateRowsShareOneOpaqueBucket(t *testing.T) {
	payload0 := testPayload("round1", 0)
	payload1 := testPayload("round1", 1)
	payload1.TreePosition = 1
	export := QueueExport{
		Version: QueueExportVersion,
		RoundID: "round1",
		Rows: []QueueExportRow{
			queueExportRowFromPayload(payload0, ShareStateReceived, 0),
			queueExportRowFromPayload(payload1, ShareStateReceived, 0),
		},
	}
	dest := newTestStore(t)
	now := time.Unix(2_000_000_000, 987_654_321)
	dest.now = func() time.Time { return now }

	result, err := dest.ImportQueue(export, QueueImportOptions{})
	require.NoError(t, err)
	require.Equal(t, 2, result.Inserted)
	assert.Equal(t,
		dest.schedule[schedKey("round1", 0, 1, 0)],
		dest.schedule[schedKey("round1", 1, 1, 1)],
	)
	assert.Equal(t, time.Unix(now.Unix(), 0), dest.schedule[schedKey("round1", 0, 1, 0)])
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

func TestPurgeRoundsRollsBackOnMetadataFailure(t *testing.T) {
	store := newTestStore(t)
	enqueueAndRequireInserted(t, store, testPayload("aabbccdd", 0))
	_, err := store.db.Exec(`CREATE TRIGGER fail_round_delete BEFORE DELETE ON rounds BEGIN SELECT RAISE(FAIL, 'injected delete failure'); END`)
	require.NoError(t, err)
	require.Zero(t, store.PurgeRounds([]string{"aabbccdd"}))
	require.Equal(t, 1, store.Status()["aabbccdd"].Pending)
	require.Len(t, store.schedule, 1)
	require.Contains(t, store.roundCache, "aabbccdd")
}
