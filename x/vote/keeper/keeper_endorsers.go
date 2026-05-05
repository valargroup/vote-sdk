package keeper

import (
	"fmt"

	"cosmossdk.io/core/store"

	"github.com/valargroup/vote-sdk/x/vote/types"
)

// GetEndorser retrieves the current bech32 address mapped to endorserID.
func (k *Keeper) GetEndorser(kvStore store.KVStore, endorserID string) (string, bool, error) {
	key, err := types.EndorserAddressKey(endorserID)
	if err != nil {
		return "", false, err
	}
	bz, err := kvStore.Get(key)
	if err != nil {
		return "", false, err
	}
	if bz == nil {
		return "", false, nil
	}
	return string(bz), true, nil
}

// SetEndorser stores a canonical endorser_id -> bech32 address mapping.
func (k *Keeper) SetEndorser(kvStore store.KVStore, endorserID, address string) error {
	key, err := types.EndorserAddressKey(endorserID)
	if err != nil {
		return err
	}
	if address == "" {
		return fmt.Errorf("%w: address cannot be empty", types.ErrInvalidField)
	}
	return kvStore.Set(key, []byte(address))
}

// DeleteEndorser clears the current mapping. Stored endorsements remain intact.
func (k *Keeper) DeleteEndorser(kvStore store.KVStore, endorserID string) error {
	key, err := types.EndorserAddressKey(endorserID)
	if err != nil {
		return err
	}
	return kvStore.Delete(key)
}

// IterateEndorsers iterates all configured endorser mappings.
func (k *Keeper) IterateEndorsers(kvStore store.KVStore, fn func(endorser *types.Endorser) bool) error {
	prefix := types.EndorserAddressPrefix
	end := types.PrefixEndBytes(prefix)
	iter, err := kvStore.Iterator(prefix, end)
	if err != nil {
		return err
	}
	defer iter.Close()

	prefixLen := len(prefix)
	for ; iter.Valid(); iter.Next() {
		key := iter.Key()
		if len(key) < prefixLen {
			continue
		}
		endorser := &types.Endorser{
			EndorserId: string(key[prefixLen:]),
			Address:    string(iter.Value()),
		}
		if fn(endorser) {
			break
		}
	}
	return nil
}

// AddEndorsedRound records an endorsement. Re-adding the same pair is idempotent.
func (k *Keeper) AddEndorsedRound(kvStore store.KVStore, endorserID string, roundID []byte) error {
	key, err := types.EndorsedRoundKey(endorserID, roundID)
	if err != nil {
		return err
	}
	return kvStore.Set(key, []byte{1})
}

// DeleteEndorsedRound clears an endorsement. Deleting a missing pair is idempotent.
func (k *Keeper) DeleteEndorsedRound(kvStore store.KVStore, endorserID string, roundID []byte) error {
	key, err := types.EndorsedRoundKey(endorserID, roundID)
	if err != nil {
		return err
	}
	return kvStore.Delete(key)
}

// IsRoundEndorsed reports whether endorserID endorsed roundID.
func (k *Keeper) IsRoundEndorsed(kvStore store.KVStore, endorserID string, roundID []byte) (bool, error) {
	key, err := types.EndorsedRoundKey(endorserID, roundID)
	if err != nil {
		return false, err
	}
	bz, err := kvStore.Get(key)
	if err != nil {
		return false, err
	}
	return bz != nil, nil
}

// IterateEndorsedRounds iterates all round IDs endorsed by endorserID.
func (k *Keeper) IterateEndorsedRounds(kvStore store.KVStore, endorserID string, fn func(roundID []byte) bool) error {
	prefix, err := types.EndorsedRoundPrefixForID(endorserID)
	if err != nil {
		return err
	}
	end := types.PrefixEndBytes(prefix)
	iter, err := kvStore.Iterator(prefix, end)
	if err != nil {
		return err
	}
	defer iter.Close()

	prefixLen := len(prefix)
	for ; iter.Valid(); iter.Next() {
		key := iter.Key()
		if len(key) != prefixLen+types.RoundIDLen {
			continue
		}
		roundID := make([]byte, types.RoundIDLen)
		copy(roundID, key[prefixLen:])
		if fn(roundID) {
			break
		}
	}
	return nil
}
