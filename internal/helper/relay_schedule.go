package helper

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"time"
)

const (
	relaySafetyBuffer = 5 * time.Minute
	relayMinBuffer    = 30 * time.Second
	relayFinalJitter  = 10 * time.Minute
	relayMinSpacing   = 10 * time.Second
)

// relayPlan is local, durable proof-attempt state. Reserving a slot before
// proving bounds expensive work even if the process crashes during submission.
// Next also skips missed slots; it is not the failed-share attempt count.
type relayPlan struct {
	Slots      []time.Time `json:"slots"`
	Cutoff     time.Time   `json:"cutoff"`
	Next       int         `json:"next"`
	LastStart  time.Time   `json:"last_start"`
	LastHeight uint64      `json:"last_height"`
}

// newRelayPlan starts at the first proof attempt and draws one final time per
// share. Short windows shrink the jitter and buffer, retaining a 30-second floor.
func newRelayPlan(now time.Time, voteEndTime uint64) relayPlan {
	deadline := time.Unix(int64(voteEndTime), 0)
	remaining := deadline.Sub(now)
	// Late or legacy shares get one immediate attempt, not a compressed burst.
	if voteEndTime == 0 || remaining <= relayMinBuffer {
		return relayPlan{Slots: []time.Time{now}, Cutoff: now}
	}
	buffer := min(relaySafetyBuffer, max(relayMinBuffer, remaining/8))
	cutoff := deadline.Add(-buffer)
	jitter := min(relayFinalJitter, remaining/8)
	jitter = min(jitter, cutoff.Sub(now))
	final := cutoff.Add(-time.Duration(rand.Int64N(int64(jitter) + 1)))
	return relayPlan{Slots: relaySlots(now, final), Cutoff: cutoff}
}

// relaySlots preserves preferred offsets when there is room, otherwise each
// retry takes at most half the time left until the randomized final attempt.
// Slots less than ten seconds apart are omitted, preserving the final slot.
func relaySlots(start, final time.Time) []time.Time {
	slots := []time.Time{start}
	previous := start
	for _, offset := range []time.Duration{time.Minute, 10 * time.Minute, 48 * time.Hour} {
		next := start.Add(offset)
		halfway := previous.Add(final.Sub(previous) / 2)
		if next.After(halfway) {
			next = halfway
		}
		if next.Sub(previous) >= relayMinSpacing && final.Sub(next) >= relayMinSpacing {
			slots = append(slots, next)
			previous = next
		}
	}
	if final.Sub(previous) >= relayMinSpacing {
		slots = append(slots, final)
	}
	return slots
}

// decodeRelayPlan rejects malformed persisted progress instead of granting a
// fresh budget. An empty value is a share that has never reserved a proof slot.
func decodeRelayPlan(raw string) (*relayPlan, error) {
	if raw == "" {
		return nil, nil
	}
	var plan relayPlan
	if err := json.Unmarshal([]byte(raw), &plan); err != nil {
		return nil, fmt.Errorf("decode relay schedule: %w", err)
	}
	if len(plan.Slots) == 0 || len(plan.Slots) > 5 || plan.Next < 0 || plan.Next > len(plan.Slots) {
		return nil, fmt.Errorf("invalid relay schedule progress")
	}
	for i, at := range plan.Slots {
		if at.IsZero() || at.After(plan.Cutoff) || (i > 0 && !at.After(plan.Slots[i-1])) {
			return nil, fmt.Errorf("invalid relay schedule times")
		}
	}
	return &plan, nil
}

// due selects only the latest missed slot, never a catch-up burst. A zero next
// time means proof attempts are finished, but commitment checks must continue.
func (p *relayPlan) due(now time.Time) (slot int, next time.Time) {
	if p.Next >= len(p.Slots) || now.After(p.Cutoff) {
		return -1, time.Time{}
	}
	slot = p.Next
	for slot+1 < len(p.Slots) && !p.Slots[slot+1].After(now) {
		slot++
	}
	next = p.Slots[slot]
	if earliest := p.LastStart.Add(relayMinSpacing); !p.LastStart.IsZero() && next.Before(earliest) {
		next = earliest
	}
	if next.After(p.Cutoff) {
		return -1, time.Time{}
	}
	if next.After(now) {
		return -1, next
	}
	return slot, next
}

// reserveRelaySlot atomically consumes a slot on the witnessed row before any
// proof work. The compare-and-swap prevents stale workers from resetting it.
func (s *ShareStore) reserveRelaySlot(share QueuedShare, plan relayPlan) error {
	raw, err := json.Marshal(plan)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.db.Exec(`UPDATE shares SET relay_plan = ?
		WHERE round_id = ? AND share_index = ? AND proposal_id = ? AND tree_position = ?
		AND state = 1 AND relay_plan = ?`, string(raw), share.Payload.VoteRoundID,
		share.Payload.EncShare.ShareIndex, share.Payload.ProposalID, share.Payload.TreePosition, share.relayPlan)
	if err != nil {
		return fmt.Errorf("reserve relay slot: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("relay slot reservation lost witnessed row")
	}
	return nil
}
