package keeper

import (
	"context"
	"fmt"

	"cosmossdk.io/core/store"
	sdk "github.com/cosmos/cosmos-sdk/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"

	"github.com/valargroup/shielded-vote/x/vote/types"
)

// GetPallasKey retrieves a validator's Pallas PK from the global registry.
// Returns nil, nil if the key has not been registered.
func (k Keeper) GetPallasKey(kvStore store.KVStore, valoperAddr string) (*types.ValidatorPallasKey, error) {
	bz, err := kvStore.Get(types.PallasKeyKey(valoperAddr))
	if err != nil {
		return nil, err
	}
	if bz == nil {
		return nil, nil
	}

	var vpk types.ValidatorPallasKey
	if err := unmarshal(bz, &vpk); err != nil {
		return nil, err
	}
	return &vpk, nil
}

// SetPallasKey stores a validator's Pallas PK in the global registry.
func (k Keeper) SetPallasKey(kvStore store.KVStore, vpk *types.ValidatorPallasKey) error {
	bz, err := marshal(vpk)
	if err != nil {
		return err
	}
	return kvStore.Set(types.PallasKeyKey(vpk.ValidatorAddress), bz)
}

// HasPallasKey returns true if the validator has a registered Pallas PK.
func (k Keeper) HasPallasKey(kvStore store.KVStore, valoperAddr string) (bool, error) {
	return kvStore.Has(types.PallasKeyKey(valoperAddr))
}

// IterateAllPallasKeys iterates over all entries in the global Pallas PK registry.
// The callback receives each ValidatorPallasKey; returning true stops iteration.
func (k Keeper) IterateAllPallasKeys(kvStore store.KVStore, cb func(vpk *types.ValidatorPallasKey) bool) error {
	prefix := types.PallasKeyPrefix
	end := types.PrefixEndBytes(prefix)

	iter, err := kvStore.Iterator(prefix, end)
	if err != nil {
		return err
	}
	defer iter.Close()

	for ; iter.Valid(); iter.Next() {
		var vpk types.ValidatorPallasKey
		if err := unmarshal(iter.Value(), &vpk); err != nil {
			return err
		}
		if cb(&vpk) {
			break
		}
	}
	return nil
}

// RegisterPallasKeyCore validates, deduplicates, and stores a Pallas PK for
// the given validator address. Shared by RegisterPallasKey and
// CreateValidatorWithPallasKey.
func (k Keeper) RegisterPallasKeyCore(kvStore store.KVStore, valAddr string, pallasPk []byte) error {
	has, err := k.HasPallasKey(kvStore, valAddr)
	if err != nil {
		return err
	}
	if has {
		return fmt.Errorf("%w: %s", types.ErrDuplicateRegistration, valAddr)
	}
	return k.SetPallasKey(kvStore, &types.ValidatorPallasKey{
		ValidatorAddress: valAddr,
		PallasPk:         pallasPk,
	})
}

// GetEligibleValidators returns all bonded validators that have a registered Pallas PK.
// Used when creating a round to snapshot the ceremony participants.
func (k Keeper) GetEligibleValidators(ctx context.Context, kvStore store.KVStore) ([]*types.ValidatorPallasKey, error) {
	var eligible []*types.ValidatorPallasKey

	if err := k.IterateAllPallasKeys(kvStore, func(vpk *types.ValidatorPallasKey) bool {
		// Check that the validator is bonded.
		valAddr, err := sdk.ValAddressFromBech32(vpk.ValidatorAddress)
		if err != nil {
			return false // skip invalid addresses
		}
		val, err := k.stakingKeeper.GetValidator(ctx, valAddr)
		if err != nil {
			return false // skip if not found
		}
		if val.GetStatus() != stakingtypes.Bonded {
			return false // skip non-bonded
		}
		eligible = append(eligible, vpk)
		return false
	}); err != nil {
		return nil, err
	}

	return eligible, nil
}

