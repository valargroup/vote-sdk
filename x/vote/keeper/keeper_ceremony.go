package keeper

import (
	"fmt"

	"cosmossdk.io/core/store"

	"github.com/z-cale/zally/x/vote/types"
)

// GetCeremonyState retrieves the singleton ceremony state from the KV store.
// Returns nil, nil if no ceremony has been initialized yet.
func (k *Keeper) GetCeremonyState(kvStore store.KVStore) (*types.CeremonyState, error) {
	bz, err := kvStore.Get(types.CeremonyStateKey)
	if err != nil {
		return nil, err
	}
	if bz == nil {
		return nil, nil
	}
	var state types.CeremonyState
	if err := unmarshal(bz, &state); err != nil {
		return nil, err
	}
	return &state, nil
}

// AppendCeremonyLog appends a timestamped entry to the round's ceremony log.
// The entry is prefixed with the block height for chronological context.
func AppendCeremonyLog(round *types.VoteRound, blockHeight uint64, msg string) {
	entry := fmt.Sprintf("[height=%d] %s", blockHeight, msg)
	round.CeremonyLog = append(round.CeremonyLog, entry)
}

// SetCeremonyState stores the singleton ceremony state in the KV store.
func (k *Keeper) SetCeremonyState(kvStore store.KVStore, state *types.CeremonyState) error {
	bz, err := marshal(state)
	if err != nil {
		return err
	}
	return kvStore.Set(types.CeremonyStateKey, bz)
}

// ---------------------------------------------------------------------------
// Per-round ceremony helpers (operate on VoteRound ceremony fields)
// ---------------------------------------------------------------------------

// OneThirdAcked returns true if at least 1/3 of round ceremony validators have
// acknowledged. Uses integer arithmetic: acks * 3 >= validators.
func OneThirdAcked(round *types.VoteRound) bool {
	n := len(round.CeremonyValidators)
	if n == 0 {
		return false
	}
	return len(round.CeremonyAcks)*3 >= n
}

// FindValidatorInRoundCeremony returns the index and true if valAddr is found
// in the round's ceremony_validators list, or (-1, false) otherwise.
func FindValidatorInRoundCeremony(round *types.VoteRound, valAddr string) (int, bool) {
	for i, v := range round.CeremonyValidators {
		if v.ValidatorAddress == valAddr {
			return i, true
		}
	}
	return -1, false
}

// FindAckInRoundCeremony returns the index and true if valAddr has an ack entry
// in the round's ceremony, or (-1, false) otherwise.
func FindAckInRoundCeremony(round *types.VoteRound, valAddr string) (int, bool) {
	for i, a := range round.CeremonyAcks {
		if a.ValidatorAddress == valAddr {
			return i, true
		}
	}
	return -1, false
}

// StripNonAckersFromRound removes non-acking validators from the round's
// CeremonyValidators and CeremonyPayloads. After this call, only validators
// with a matching ack remain.
func StripNonAckersFromRound(round *types.VoteRound) {
	acked := make(map[string]bool, len(round.CeremonyAcks))
	for _, a := range round.CeremonyAcks {
		acked[a.ValidatorAddress] = true
	}

	kept := round.CeremonyValidators[:0]
	for _, v := range round.CeremonyValidators {
		if acked[v.ValidatorAddress] {
			kept = append(kept, v)
		}
	}
	round.CeremonyValidators = kept

	keptPayloads := round.CeremonyPayloads[:0]
	for _, p := range round.CeremonyPayloads {
		if acked[p.ValidatorAddress] {
			keptPayloads = append(keptPayloads, p)
		}
	}
	round.CeremonyPayloads = keptPayloads
}
