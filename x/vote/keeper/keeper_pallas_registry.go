package keeper

import (
	"bytes"
	"context"
	"fmt"

	"cosmossdk.io/core/store"
	sdk "github.com/cosmos/cosmos-sdk/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"

	"github.com/valargroup/vote-sdk/x/vote/types"
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

// SetPallasKey stores a validator's Pallas PK in the global registry and
// writes the reverse-lookup index (PK -> validator address) used to enforce
// cross-validator key uniqueness.
func (k Keeper) SetPallasKey(kvStore store.KVStore, vpk *types.ValidatorPallasKey) error {
	bz, err := marshal(vpk)
	if err != nil {
		return err
	}
	if err := kvStore.Set(types.PallasKeyKey(vpk.ValidatorAddress), bz); err != nil {
		return err
	}
	return kvStore.Set(
		types.PallasKeyReverseLookupKey(vpk.PallasPk),
		[]byte(vpk.ValidatorAddress),
	)
}

// GetPallasKeyOwner returns the validator address that registered the given
// Pallas public key, or "" if the key is not registered.
func (k Keeper) GetPallasKeyOwner(kvStore store.KVStore, pallasPk []byte) (string, error) {
	bz, err := kvStore.Get(types.PallasKeyReverseLookupKey(pallasPk))
	if err != nil {
		return "", err
	}
	if bz == nil {
		return "", nil
	}
	return string(bz), nil
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
//
// Uniqueness is enforced in two dimensions:
//  1. A validator address may only register once (forward index).
//  2. A Pallas public key may only be registered by one validator (reverse index).
//
// Rejecting duplicate PKs prevents colluding validators from sharing a secret
// key and breaking threshold security during DKG.
func (k Keeper) RegisterPallasKeyCore(kvStore store.KVStore, valAddr string, pallasPk []byte) error {
	has, err := k.HasPallasKey(kvStore, valAddr)
	if err != nil {
		return err
	}
	if has {
		return fmt.Errorf("%w: %s", types.ErrDuplicateRegistration, valAddr)
	}

	owner, err := k.GetPallasKeyOwner(kvStore, pallasPk)
	if err != nil {
		return err
	}
	if owner != "" {
		return fmt.Errorf("%w: already registered by %s", types.ErrDuplicatePallasKey, owner)
	}

	return k.SetPallasKey(kvStore, &types.ValidatorPallasKey{
		ValidatorAddress: valAddr,
		PallasPk:         pallasPk,
	})
}

// DeletePallasKeyReverse removes the reverse-lookup index entry for the given
// Pallas public key. Used during key rotation to clean up the old PK's entry.
func (k Keeper) DeletePallasKeyReverse(kvStore store.KVStore, pallasPk []byte) error {
	return kvStore.Delete(types.PallasKeyReverseLookupKey(pallasPk))
}

// RotatePallasKeyCore replaces a validator's registered Pallas PK with a new one.
// The validator must already have a registered key (use RegisterPallasKeyCore for
// first-time registration). The new PK must differ from the current one and pass
// global uniqueness checks. Returns the old PK for audit logging.
func (k Keeper) RotatePallasKeyCore(kvStore store.KVStore, valAddr string, newPallasPk []byte) (oldPallasPk []byte, err error) {
	existing, err := k.GetPallasKey(kvStore, valAddr)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		return nil, fmt.Errorf("%w: %s", types.ErrNoPallasKey, valAddr)
	}

	if bytes.Equal(existing.PallasPk, newPallasPk) {
		return nil, fmt.Errorf("%w: %s", types.ErrSameKey, valAddr)
	}

	owner, err := k.GetPallasKeyOwner(kvStore, newPallasPk)
	if err != nil {
		return nil, err
	}
	if owner != "" {
		return nil, fmt.Errorf("%w: already registered by %s", types.ErrDuplicatePallasKey, owner)
	}

	if err := k.DeletePallasKeyReverse(kvStore, existing.PallasPk); err != nil {
		return nil, err
	}

	if err := k.SetPallasKey(kvStore, &types.ValidatorPallasKey{
		ValidatorAddress: valAddr,
		PallasPk:         newPallasPk,
	}); err != nil {
		return nil, err
	}
	return existing.PallasPk, nil
}

// GetEligibleValidators returns all bonded, unjailed validators that have a registered Pallas PK.
// Used when creating a round to snapshot the ceremony participants.
func (k Keeper) GetEligibleValidators(ctx context.Context, kvStore store.KVStore) ([]*types.ValidatorPallasKey, error) {
	var eligible []*types.ValidatorPallasKey
	var eligibilityErr error

	if err := k.IterateAllPallasKeys(kvStore, func(vpk *types.ValidatorPallasKey) bool {
		if reason, err := k.validatorCeremonyIneligibilityReason(ctx, vpk.ValidatorAddress); err != nil {
			eligibilityErr = err
			return true
		} else if reason == "" {
			eligible = append(eligible, vpk)
		}
		return false
	}); err != nil {
		return nil, err
	}
	if eligibilityErr != nil {
		return nil, eligibilityErr
	}

	return eligible, nil
}

// RegisteringTimeoutDrop records why a validator from the original ceremony
// snapshot was not retained when a REGISTERING timeout fired.
type RegisteringTimeoutDrop struct {
	ValidatorAddress string
	Reason           string
}

// RetainRegisteringTimeoutValidators returns the original ceremony validators
// that contributed during the expired REGISTERING window and are still eligible
// to participate. The retained set preserves original order but receives fresh
// contiguous Shamir indexes because all prior DKG material is discarded.
func (k Keeper) RetainRegisteringTimeoutValidators(
	ctx context.Context,
	kvStore store.KVStore,
	round *types.VoteRound,
) ([]*types.ValidatorPallasKey, []RegisteringTimeoutDrop, error) {
	contributed := make(map[string]bool, len(round.DkgContributions))
	for _, c := range round.DkgContributions {
		contributed[c.ValidatorAddress] = true
	}

	retained := make([]*types.ValidatorPallasKey, 0, len(round.CeremonyValidators))
	dropped := make([]RegisteringTimeoutDrop, 0, len(round.CeremonyValidators))
	for _, v := range round.CeremonyValidators {
		valAddr := v.ValidatorAddress
		if !contributed[valAddr] {
			dropped = append(dropped, RegisteringTimeoutDrop{
				ValidatorAddress: valAddr,
				Reason:           "no contribution",
			})
			continue
		}

		current, reason, err := k.currentEligiblePallasKey(ctx, kvStore, valAddr)
		if err != nil {
			return nil, nil, err
		}
		if reason != "" {
			dropped = append(dropped, RegisteringTimeoutDrop{
				ValidatorAddress: valAddr,
				Reason:           reason,
			})
			continue
		}

		retained = append(retained, &types.ValidatorPallasKey{
			ValidatorAddress: current.ValidatorAddress,
			PallasPk:         append([]byte(nil), current.PallasPk...),
			ShamirIndex:      uint32(len(retained) + 1),
		})
	}

	return retained, dropped, nil
}

func (k Keeper) currentEligiblePallasKey(
	ctx context.Context,
	kvStore store.KVStore,
	valoperAddr string,
) (*types.ValidatorPallasKey, string, error) {
	if reason, err := k.validatorCeremonyIneligibilityReason(ctx, valoperAddr); err != nil {
		return nil, "", err
	} else if reason != "" {
		return nil, reason, nil
	}

	vpk, err := k.GetPallasKey(kvStore, valoperAddr)
	if err != nil {
		return nil, "", err
	}
	if vpk == nil {
		return nil, "missing pallas key", nil
	}
	return vpk, "", nil
}

func (k Keeper) validatorCeremonyIneligibilityReason(ctx context.Context, valoperAddr string) (string, error) {
	if k.stakingKeeper == nil {
		return "", fmt.Errorf("staking keeper not configured")
	}
	valAddr, err := sdk.ValAddressFromBech32(valoperAddr)
	if err != nil {
		return "invalid validator address", nil
	}
	val, err := k.stakingKeeper.GetValidator(ctx, valAddr)
	if err != nil {
		return "validator not found", nil
	}
	if val.GetStatus() != stakingtypes.Bonded {
		return "not bonded", nil
	}
	if val.IsJailed() {
		return "jailed", nil
	}
	return "", nil
}
