package keeper

import (
	"context"
	"fmt"

	"cosmossdk.io/core/store"
	sdk "github.com/cosmos/cosmos-sdk/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"

	"github.com/valargroup/vote-sdk/x/vote/types"
)

// GetAdmins retrieves the admin set (singleton) from the KV store.
// Returns nil, nil if no admin set has been installed yet.
func (k *Keeper) GetAdmins(kvStore store.KVStore) (*types.AdminSet, error) {
	bz, err := kvStore.Get(types.AdminSetKey)
	if err != nil {
		return nil, err
	}
	if bz == nil {
		return nil, nil
	}

	var set types.AdminSet
	if err := unmarshal(bz, &set); err != nil {
		return nil, err
	}
	return &set, nil
}

// SetAdmins stores the admin set (singleton) in the KV store. Addresses are
// normalized and deduplicated before persist so every read returns canonical
// bech32, even if callers passed mixed-case or uncanonical forms.
func (k *Keeper) SetAdmins(kvStore store.KVStore, set *types.AdminSet) error {
	normalized, err := types.ValidateAndNormalizeAdminSet(set.Addresses)
	if err != nil {
		return err
	}
	bz, err := marshal(&types.AdminSet{Addresses: normalized})
	if err != nil {
		return err
	}
	return kvStore.Set(types.AdminSetKey, bz)
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

// IsAdmin reports whether addr is a current admin. Comparison is on the
// canonical bech32 form; invalid bech32 returns (false, nil).
func (k *Keeper) IsAdmin(ctx context.Context, addr string) (bool, error) {
	kvStore := k.OpenKVStore(ctx)
	set, err := k.GetAdmins(kvStore)
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

// ValidateAdminOnly returns nil iff creator is in the current admin set.
// Distinguishes ErrNoAdmins (empty set) from ErrNotAuthorized (non-member).
func (k *Keeper) ValidateAdminOnly(ctx context.Context, creator string) error {
	kvStore := k.OpenKVStore(ctx)
	set, err := k.GetAdmins(kvStore)
	if err != nil {
		return err
	}

	if set == nil || len(set.Addresses) == 0 {
		return fmt.Errorf("%w", types.ErrNoAdmins)
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

	return fmt.Errorf("%w: sender %s is not in the admin set", types.ErrNotAuthorized, normalized)
}

// normalizeBech32Addr parses the address and returns its canonical bech32 form.
func normalizeBech32Addr(addr string) (string, error) {
	acc, err := sdk.AccAddressFromBech32(addr)
	if err != nil {
		return "", err
	}
	return acc.String(), nil
}
