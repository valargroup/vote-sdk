package keeper

import (
	"context"
	"fmt"

	"cosmossdk.io/core/store"
	sdk "github.com/cosmos/cosmos-sdk/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"

	"github.com/valargroup/vote-sdk/x/vote/types"
)

// GetVoteManagers retrieves the vote-manager set from the KV store.
// Returns nil, nil if no vote-manager set has been installed yet.
func (k *Keeper) GetVoteManagers(kvStore store.KVStore) (*types.VoteManagerSet, error) {
	bz, err := kvStore.Get(types.VoteManagerSetKey)
	if err != nil {
		return nil, err
	}
	if bz == nil {
		return nil, nil
	}

	var set types.VoteManagerSet
	if err := unmarshal(bz, &set); err != nil {
		return nil, err
	}
	return &set, nil
}

// SetVoteManagers stores the vote-manager set in the KV store. Addresses are
// normalized and deduplicated before persist so every read returns canonical
// bech32, even if callers passed mixed-case or uncanonical forms.
func (k *Keeper) SetVoteManagers(kvStore store.KVStore, set *types.VoteManagerSet) error {
	normalized, err := types.ValidateAndNormalizeVoteManagerSet(set.Addresses)
	if err != nil {
		return err
	}
	bz, err := marshal(&types.VoteManagerSet{Addresses: normalized})
	if err != nil {
		return err
	}
	return kvStore.Set(types.VoteManagerSetKey, bz)
}

// IsValidator checks whether the given address is a bonded validator.
func (k *Keeper) IsValidator(ctx context.Context, address string) bool {
	valAddr, err := sdk.ValAddressFromBech32(address)
	if err != nil {
		return false
	}
	val, err := k.stakingKeeper.GetValidator(ctx, valAddr)
	if err != nil {
		return false
	}
	return val.GetStatus() == stakingtypes.Bonded
}

// IsVoteManager reports whether addr is a current vote manager. Comparison is on the
// canonical bech32 form; invalid bech32 returns (false, nil).
func (k *Keeper) IsVoteManager(ctx context.Context, addr string) (bool, error) {
	kvStore := k.OpenKVStore(ctx)
	set, err := k.GetVoteManagers(kvStore)
	if err != nil {
		return false, err
	}
	if set == nil {
		return false, nil
	}
	normalized, err := normalizeBech32Addr(addr)
	if err != nil {
		return false, nil
	}
	for _, a := range set.Addresses {
		if a == normalized {
			return true, nil
		}
	}
	return false, nil
}

// ValidateVoteManagerOnly returns nil iff creator is in the current vote manager set.
// Distinguishes ErrNoVoteManagers (empty set) from ErrNotAuthorized (non-member).
func (k *Keeper) ValidateVoteManagerOnly(ctx context.Context, creator string) error {
	kvStore := k.OpenKVStore(ctx)
	set, err := k.GetVoteManagers(kvStore)
	if err != nil {
		return err
	}

	if set == nil || len(set.Addresses) == 0 {
		return fmt.Errorf("%w", types.ErrNoVoteManagers)
	}

	normalized, err := normalizeBech32Addr(creator)
	if err != nil {
		return fmt.Errorf("%w: creator %q is not a valid bech32 address: %v", types.ErrNotAuthorized, creator, err)
	}

	for _, a := range set.Addresses {
		if a == normalized {
			return nil
		}
	}

	return fmt.Errorf("%w: sender %s is not in the vote-manager set", types.ErrNotAuthorized, normalized)
}

// normalizeBech32Addr parses the address and returns its canonical bech32 form.
func normalizeBech32Addr(addr string) (string, error) {
	acc, err := sdk.AccAddressFromBech32(addr)
	if err != nil {
		return "", err
	}
	return acc.String(), nil
}
