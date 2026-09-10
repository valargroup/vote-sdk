package helper

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"time"
)

const (
	retrySafetyBuffer = 5 * time.Minute
	retryMinBuffer    = 30 * time.Second
	retryFinalJitter  = 10 * time.Minute
	retryMinSpacing   = 10 * time.Second
)

// retryState records the inputs and progress needed to derive retry times.
// NextSlot skips missed slots and is separate from the failed-attempt count.
type retryState struct {
	FirstAttempt time.Time `json:"first_attempt"`
	FinalAttempt time.Time `json:"final_attempt"`
	NextSlot     int       `json:"next_slot"`
	LastAttempt  time.Time `json:"last_attempt"`
	LastHeight   uint64    `json:"last_height"`
}

// newRetryState records the first attempt and one random final retry time.
// Intermediate retry times and the cutoff are derived rather than persisted.
func newRetryState(now time.Time, voteEndTime uint64) retryState {
	cutoff := retryCutoff(now, voteEndTime)
	if cutoff.Equal(now) {
		return retryState{FirstAttempt: now, FinalAttempt: now}
	}
	remaining := time.Unix(int64(voteEndTime), 0).Sub(now)
	jitter := min(retryFinalJitter, remaining/8, cutoff.Sub(now))
	final := cutoff.Add(-time.Duration(rand.Int64N(int64(jitter) + 1)))
	return retryState{FirstAttempt: now, FinalAttempt: final}
}

// retryCutoff reserves time for proving and block inclusion. Late shares or
// shares without a known deadline get only their immediate first attempt.
func retryCutoff(firstAttempt time.Time, voteEndTime uint64) time.Time {
	deadline := time.Unix(int64(voteEndTime), 0)
	remaining := deadline.Sub(firstAttempt)
	if voteEndTime == 0 || remaining <= retryMinBuffer {
		return firstAttempt
	}
	buffer := min(retrySafetyBuffer, max(retryMinBuffer, remaining/8))
	return deadline.Add(-buffer)
}

// retryTimes preserves preferred offsets when there is room, otherwise each
// retry takes at most half the time left until the randomized final attempt.
// Slots less than ten seconds apart are omitted, preserving the final slot.
func retryTimes(start, final time.Time) []time.Time {
	slots := []time.Time{start}
	previous := start
	for _, offset := range []time.Duration{time.Minute, 10 * time.Minute, 48 * time.Hour} {
		next := start.Add(offset)
		halfway := previous.Add(final.Sub(previous) / 2)
		if next.After(halfway) {
			next = halfway
		}
		if next.Sub(previous) >= retryMinSpacing && final.Sub(next) >= retryMinSpacing {
			slots = append(slots, next)
			previous = next
		}
	}
	if final.Sub(previous) >= retryMinSpacing {
		slots = append(slots, final)
	}
	return slots
}

// decodeRetryState rejects malformed persisted progress instead of granting
// a fresh budget. Empty state means no proof attempt has been reserved yet.
func decodeRetryState(raw string, voteEndTime uint64) (*retryState, error) {
	if raw == "" {
		return nil, nil
	}
	var state retryState
	if err := json.Unmarshal([]byte(raw), &state); err != nil {
		return nil, fmt.Errorf("decode retry state: %w", err)
	}
	if state.FirstAttempt.IsZero() || state.FinalAttempt.Before(state.FirstAttempt) ||
		state.FinalAttempt.After(retryCutoff(state.FirstAttempt, voteEndTime)) {
		return nil, fmt.Errorf("invalid retry times")
	}
	if state.NextSlot < 0 || state.NextSlot > len(retryTimes(state.FirstAttempt, state.FinalAttempt)) {
		return nil, fmt.Errorf("invalid retry progress")
	}
	return &state, nil
}

// due derives retry times and selects only the latest missed slot. A zero
// next time means proof attempts are finished, but commitment checks continue.
func (s *retryState) due(now time.Time, voteEndTime uint64) (slot int, next time.Time) {
	times := retryTimes(s.FirstAttempt, s.FinalAttempt)
	cutoff := retryCutoff(s.FirstAttempt, voteEndTime)
	if s.NextSlot >= len(times) || now.After(cutoff) {
		return -1, time.Time{}
	}
	slot = s.NextSlot
	for slot+1 < len(times) && !times[slot+1].After(now) {
		slot++
	}
	next = times[slot]
	if earliest := s.LastAttempt.Add(retryMinSpacing); !s.LastAttempt.IsZero() && next.Before(earliest) {
		next = earliest
	}
	if next.After(cutoff) {
		return -1, time.Time{}
	}
	if next.After(now) {
		return -1, next
	}
	return slot, next
}

// reserveProofAttempt atomically consumes a slot on the witnessed row before any
// proof work. The compare-and-swap prevents stale workers from resetting it.
func (s *ShareStore) reserveProofAttempt(share QueuedShare, state retryState) error {
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.db.Exec(`UPDATE shares SET retry_state = ?
		WHERE round_id = ? AND share_index = ? AND proposal_id = ? AND tree_position = ?
		AND state = 1 AND retry_state = ?`, string(raw), share.Payload.VoteRoundID,
		share.Payload.EncShare.ShareIndex, share.Payload.ProposalID, share.Payload.TreePosition, share.retryState)
	if err != nil {
		return fmt.Errorf("reserve proof attempt: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("proof attempt reservation lost witnessed row")
	}
	return nil
}
