package keeper

import (
	"cosmossdk.io/core/store"

	"github.com/valargroup/vote-sdk/x/vote/types"
)

const defaultMinCeremonyValidators uint32 = 1

// GetMinCeremonyValidators reads the minimum ceremony validator count from KV.
// Returns defaultMinCeremonyValidators (1) if the key has not been set.
func (k *Keeper) GetMinCeremonyValidators(kvStore store.KVStore) (uint32, error) {
	bz, err := kvStore.Get(types.MinCeremonyValidatorsKey)
	if err != nil {
		return 0, err
	}
	if bz == nil || len(bz) != 4 {
		return defaultMinCeremonyValidators, nil
	}
	return uint32(bz[0])<<24 | uint32(bz[1])<<16 | uint32(bz[2])<<8 | uint32(bz[3]), nil
}

// SetMinCeremonyValidators writes the minimum ceremony validator count to KV
// as a 4-byte big-endian uint32.
func (k *Keeper) SetMinCeremonyValidators(kvStore store.KVStore, val uint32) error {
	bz := []byte{byte(val >> 24), byte(val >> 16), byte(val >> 8), byte(val)}
	return kvStore.Set(types.MinCeremonyValidatorsKey, bz)
}
