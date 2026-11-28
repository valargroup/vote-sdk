package keeper

import (
	"encoding/binary"
	"fmt"
	"math"

	"cosmossdk.io/core/store"
	errorsmod "cosmossdk.io/errors"
	"google.golang.org/protobuf/proto"

	"github.com/valargroup/vote-sdk/x/vote/types"
)

// The proxy delegate registry under prefix 0x1A
// (docs/proxy-delegation/contracts-v1.md §3.2): entries, every key ever
// registered, the live entry of each handle, next_index and the params. Message values are deterministic
// protobuf and integers are big-endian.

// GetProxyDelegateEntry returns entry delegateIndex, or ErrDelegateNotFound
// when the registry has none.
func (k *Keeper) GetProxyDelegateEntry(kvStore store.KVStore, delegateIndex uint32) (*types.ProxyDelegateEntry, error) {
	bz, err := kvStore.Get(types.ProxyDelegateEntryKey(delegateIndex))
	if err != nil {
		return nil, err
	}
	if bz == nil {
		return nil, errorsmod.Wrapf(types.ErrDelegateNotFound, "delegate %d", delegateIndex)
	}
	var entry types.ProxyDelegateEntry
	if err := unmarshal(bz, &entry); err != nil {
		return nil, errorsmod.Wrapf(err, "decode proxy delegate entry %d", delegateIndex)
	}
	return &entry, nil
}

// SetProxyDelegateEntry stores entry under its own index.
func (k *Keeper) SetProxyDelegateEntry(kvStore store.KVStore, entry *types.ProxyDelegateEntry) error {
	bz, err := marshalDeterministic(entry)
	if err != nil {
		return err
	}
	return kvStore.Set(types.ProxyDelegateEntryKey(entry.Index), bz)
}

// GetProxyUsedDelegateKey returns the index of the entry dk was registered
// to, and false if dk was never registered.
func (k *Keeper) GetProxyUsedDelegateKey(kvStore store.KVStore, dk []byte) (uint32, bool, error) {
	key, err := types.ProxyUsedDelegateKeyKey(dk)
	if err != nil {
		return 0, false, err
	}
	bz, err := kvStore.Get(key)
	if err != nil {
		return 0, false, err
	}
	if bz == nil {
		return 0, false, nil
	}
	delegateIndex, err := decodeUint32BE(bz)
	if err != nil {
		return 0, false, errorsmod.Wrapf(err, "used delegate key %x", dk)
	}
	return delegateIndex, true, nil
}

// SetProxyUsedDelegateKey records dk as registered to entry delegateIndex.
func (k *Keeper) SetProxyUsedDelegateKey(kvStore store.KVStore, dk []byte, delegateIndex uint32) error {
	key, err := types.ProxyUsedDelegateKeyKey(dk)
	if err != nil {
		return err
	}
	return kvStore.Set(key, binary.BigEndian.AppendUint32(nil, delegateIndex))
}

// GetProxyDelegateByHandle returns the index of the live entry of a
// canonical handle at provider, and false if the handle has none.
func (k *Keeper) GetProxyDelegateByHandle(kvStore store.KVStore, provider uint32, handle string) (uint32, bool, error) {
	bz, err := kvStore.Get(types.ProxyDelegateHandleKey(provider, handle))
	if err != nil {
		return 0, false, err
	}
	if bz == nil {
		return 0, false, nil
	}
	delegateIndex, err := decodeUint32BE(bz)
	if err != nil {
		return 0, false, errorsmod.Wrapf(err, "provider %d handle %q", provider, handle)
	}
	return delegateIndex, true, nil
}

// SetProxyDelegateByHandle records entry delegateIndex as the live entry of a
// canonical handle at provider.
func (k *Keeper) SetProxyDelegateByHandle(kvStore store.KVStore, provider uint32, handle string, delegateIndex uint32) error {
	return kvStore.Set(types.ProxyDelegateHandleKey(provider, handle), binary.BigEndian.AppendUint32(nil, delegateIndex))
}

// GetProxyNextDelegateIndex returns next_index, the index the next new entry
// gets. Absent reads as 1, so a store from before v1.7.0 needs no migration
// (contracts-v1 §9).
func (k *Keeper) GetProxyNextDelegateIndex(kvStore store.KVStore) (uint32, error) {
	bz, err := kvStore.Get(types.ProxyNextDelegateIndexKey)
	if err != nil {
		return 0, err
	}
	if bz == nil {
		return 1, nil
	}
	next, err := decodeUint32BE(bz)
	if err != nil {
		return 0, errorsmod.Wrap(err, "next delegate index")
	}
	return next, nil
}

// SetProxyNextDelegateIndex stores next_index.
func (k *Keeper) SetProxyNextDelegateIndex(kvStore store.KVStore, next uint32) error {
	return kvStore.Set(types.ProxyNextDelegateIndexKey, binary.BigEndian.AppendUint32(nil, next))
}

// GetProxyDelegationParams returns the registry params. Absent reads as the
// zero value: unpaused, no verifiers and max_delegates = 0, so registration
// stays closed until coordinators set params (contracts-v1 §9).
func (k *Keeper) GetProxyDelegationParams(kvStore store.KVStore) (*types.ProxyDelegationParams, error) {
	bz, err := kvStore.Get(types.ProxyDelegationParamsKey)
	if err != nil {
		return nil, err
	}
	params := &types.ProxyDelegationParams{}
	if bz == nil {
		return params, nil
	}
	if err := unmarshal(bz, params); err != nil {
		return nil, errorsmod.Wrap(err, "decode proxy delegation params")
	}
	return params, nil
}

// SetProxyDelegationParams stores the registry params.
func (k *Keeper) SetProxyDelegationParams(kvStore store.KVStore, params *types.ProxyDelegationParams) error {
	bz, err := marshalDeterministic(params)
	if err != nil {
		return err
	}
	return kvStore.Set(types.ProxyDelegationParamsKey, bz)
}

// IterateProxyDelegateEntries calls cb with each entry whose index is above
// startAfter, in ascending index order, until cb returns true.
func (k *Keeper) IterateProxyDelegateEntries(kvStore store.KVStore, startAfter uint32, cb func(*types.ProxyDelegateEntry) bool) error {
	if startAfter == math.MaxUint32 {
		return nil
	}
	iter, err := kvStore.Iterator(types.ProxyDelegateEntryKey(startAfter+1), types.PrefixEndBytes(types.ProxyDelegateEntryPrefix))
	if err != nil {
		return err
	}
	defer iter.Close()
	for ; iter.Valid(); iter.Next() {
		var entry types.ProxyDelegateEntry
		if err := unmarshal(iter.Value(), &entry); err != nil {
			return errorsmod.Wrapf(err, "decode proxy delegate entry at key %x", iter.Key())
		}
		if cb(&entry) {
			return nil
		}
	}
	return nil
}

// marshalDeterministic encodes m as deterministic protobuf, the encoding of
// every registry value (contracts-v1 §3.2).
func marshalDeterministic(m proto.Message) ([]byte, error) {
	return proto.MarshalOptions{Deterministic: true}.Marshal(m)
}

// decodeUint32BE decodes a stored big-endian uint32, which must be 4 bytes.
func decodeUint32BE(bz []byte) (uint32, error) {
	if len(bz) != 4 {
		return 0, fmt.Errorf("stored uint32 is %d bytes, expected 4", len(bz))
	}
	return binary.BigEndian.Uint32(bz), nil
}
