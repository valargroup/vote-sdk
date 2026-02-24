package keeper

import (
	"cosmossdk.io/core/store"

	"github.com/z-cale/zally/x/vote/types"
)

// GetCeremonyState retrieves the singleton ceremony state from the KV store.
// Returns nil, nil if no ceremony has been initialized yet.
func (k Keeper) GetCeremonyState(kvStore store.KVStore) (*types.CeremonyState, error) {
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

// SetCeremonyState stores the singleton ceremony state in the KV store.
func (k Keeper) SetCeremonyState(kvStore store.KVStore, state *types.CeremonyState) error {
	bz, err := marshal(state)
	if err != nil {
		return err
	}
	return kvStore.Set(types.CeremonyStateKey, bz)
}

// FindValidatorInCeremony returns the index and true if valAddr is found
// in the ceremony's validator list, or (-1, false) otherwise.
func FindValidatorInCeremony(state *types.CeremonyState, valAddr string) (int, bool) {
	for i, v := range state.Validators {
		if v.ValidatorAddress == valAddr {
			return i, true
		}
	}
	return -1, false
}

// FindAckForValidator returns the index and true if valAddr has an ack entry
// in the ceremony, or (-1, false) otherwise.
func FindAckForValidator(state *types.CeremonyState, valAddr string) (int, bool) {
	for i, a := range state.Acks {
		if a.ValidatorAddress == valAddr {
			return i, true
		}
	}
	return -1, false
}

// AllValidatorsAcked returns true if every registered validator has a
// corresponding ack entry in the ceremony state.
func AllValidatorsAcked(state *types.CeremonyState) bool {
	if len(state.Validators) == 0 {
		return false
	}
	for _, v := range state.Validators {
		if _, found := FindAckForValidator(state, v.ValidatorAddress); !found {
			return false
		}
	}
	return true
}

// TwoThirdsAcked returns true if at least 2/3 of registered validators have
// acknowledged. Uses integer arithmetic to avoid floating point:
// acks * 3 >= validators * 2.
func TwoThirdsAcked(state *types.CeremonyState) bool {
	n := len(state.Validators)
	if n == 0 {
		return false
	}
	return len(state.Acks)*3 >= n*2
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

// NonAckingValidators returns the operator addresses of validators that have
// not yet submitted an ack entry.
func NonAckingValidators(state *types.CeremonyState) []string {
	var addrs []string
	for _, v := range state.Validators {
		if _, found := FindAckForValidator(state, v.ValidatorAddress); !found {
			addrs = append(addrs, v.ValidatorAddress)
		}
	}
	return addrs
}

// StripNonAckers removes non-acking validators from state.Validators and
// their corresponding entries from state.Payloads. After this call, only
// validators with a matching ack remain.
func StripNonAckers(state *types.CeremonyState) {
	acked := make(map[string]bool, len(state.Acks))
	for _, a := range state.Acks {
		acked[a.ValidatorAddress] = true
	}

	// Filter validators.
	kept := state.Validators[:0]
	for _, v := range state.Validators {
		if acked[v.ValidatorAddress] {
			kept = append(kept, v)
		}
	}
	state.Validators = kept

	// Filter payloads.
	keptPayloads := state.Payloads[:0]
	for _, p := range state.Payloads {
		if acked[p.ValidatorAddress] {
			keptPayloads = append(keptPayloads, p)
		}
	}
	state.Payloads = keptPayloads
}

