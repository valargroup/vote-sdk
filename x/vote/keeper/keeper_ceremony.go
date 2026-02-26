package keeper

import (
	"context"
	"fmt"

	"cosmossdk.io/core/store"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/z-cale/zally/x/vote/types"
)

// DefaultCeremonyMissJailThreshold is the number of consecutive ceremony
// misses after which a validator is jailed.
const DefaultCeremonyMissJailThreshold = 3

// AppendCeremonyLog appends a timestamped entry to the round's ceremony log.
// The entry is prefixed with the block height for chronological context.
func AppendCeremonyLog(round *types.VoteRound, blockHeight uint64, msg string) {
	entry := fmt.Sprintf("[height=%d] %s", blockHeight, msg)
	round.CeremonyLog = append(round.CeremonyLog, entry)
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

// ---------------------------------------------------------------------------
// Ceremony miss counter (consecutive misses per validator)
// ---------------------------------------------------------------------------

// JailValidator jails a validator by its operator address.
// Resolves the valoper → consensus address via the staking keeper.
func (k Keeper) JailValidator(ctx context.Context, valoperAddr string) (err error) {
	// Recover from panics in GetConsAddr (happens when consensus pubkey is nil).
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic resolving consensus address for %s: %v", valoperAddr, r)
		}
	}()

	valAddr, err := sdk.ValAddressFromBech32(valoperAddr)
	if err != nil {
		return fmt.Errorf("invalid valoper address %q: %w", valoperAddr, err)
	}
	val, err := k.stakingKeeper.GetValidator(ctx, valAddr)
	if err != nil {
		return fmt.Errorf("failed to get validator %s: %w", valoperAddr, err)
	}
	consAddr, err := val.GetConsAddr()
	if err != nil {
		return fmt.Errorf("failed to get consensus address for %s: %w", valoperAddr, err)
	}
	return k.stakingKeeper.Jail(ctx, consAddr)
}

// UnjailValidator unjails a validator by its operator address.
// Resolves the valoper → consensus address via the staking keeper, calls
// Unjail, and resets the ceremony miss counter.
func (k Keeper) UnjailValidator(ctx context.Context, valoperAddr string) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic resolving consensus address for %s: %v", valoperAddr, r)
		}
	}()

	valAddr, err := sdk.ValAddressFromBech32(valoperAddr)
	if err != nil {
		return fmt.Errorf("invalid valoper address %q: %w", valoperAddr, err)
	}
	val, err := k.stakingKeeper.GetValidator(ctx, valAddr)
	if err != nil {
		return fmt.Errorf("failed to get validator %s: %w", valoperAddr, err)
	}
	consAddr, err := val.GetConsAddr()
	if err != nil {
		return fmt.Errorf("failed to get consensus address for %s: %w", valoperAddr, err)
	}
	if err := k.stakingKeeper.Unjail(ctx, consAddr); err != nil {
		return err
	}

	kvStore := k.OpenKVStore(ctx)
	return k.ResetCeremonyMiss(kvStore, valoperAddr)
}

// GetCeremonyMissCount returns the consecutive ceremony miss count for a validator.
func (k Keeper) GetCeremonyMissCount(kvStore store.KVStore, valoperAddr string) (uint64, error) {
	bz, err := kvStore.Get(types.CeremonyMissKey(valoperAddr))
	if err != nil {
		return 0, err
	}
	if len(bz) < 8 {
		return 0, nil
	}
	return getUint64BE(bz), nil
}

// IncrementCeremonyMiss increments the consecutive miss counter for a validator
// and returns the new count.
func (k Keeper) IncrementCeremonyMiss(kvStore store.KVStore, valoperAddr string) (uint64, error) {
	count, err := k.GetCeremonyMissCount(kvStore, valoperAddr)
	if err != nil {
		return 0, err
	}
	count++
	val := make([]byte, 8)
	putUint64BE(val, count)
	return count, kvStore.Set(types.CeremonyMissKey(valoperAddr), val)
}

// ResetCeremonyMiss resets the consecutive miss counter for a validator to zero.
func (k Keeper) ResetCeremonyMiss(kvStore store.KVStore, valoperAddr string) error {
	return kvStore.Delete(types.CeremonyMissKey(valoperAddr))
}
