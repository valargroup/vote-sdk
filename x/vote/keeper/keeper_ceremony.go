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
