package keeper

import (
	"context"
	"fmt"

	"cosmossdk.io/core/store"
	sdk "github.com/cosmos/cosmos-sdk/types"

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

// JailValidator resolves a validator operator address to its consensus address
// and jails the validator via the staking module.
func (k Keeper) JailValidator(ctx context.Context, operatorAddr string) error {
	valAddr, err := sdk.ValAddressFromBech32(operatorAddr)
	if err != nil {
		return fmt.Errorf("invalid operator address %s: %w", operatorAddr, err)
	}

	validator, err := k.stakingKeeper.GetValidator(ctx, valAddr)
	if err != nil {
		return fmt.Errorf("validator %s not found: %w", operatorAddr, err)
	}

	consAddr, err := validator.GetConsAddr()
	if err != nil {
		return fmt.Errorf("failed to get consensus address for %s: %w", operatorAddr, err)
	}

	return k.stakingKeeper.Jail(ctx, consAddr)
}
