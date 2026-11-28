package keeper

import (
	"math"

	"cosmossdk.io/core/store"
	errorsmod "cosmossdk.io/errors"

	"github.com/valargroup/vote-sdk/x/vote/types"
)

// Delegate ballots under prefix 0x1B (docs/proxy-delegation/contracts-v1.md
// §3.2): at most one DelegateBallotRecord per (round, d), stored as
// deterministic protobuf and never overwritten.

// HasDelegateBallot reports whether delegate delegateIndex has a ballot in
// round roundID.
func (k *Keeper) HasDelegateBallot(kvStore store.KVStore, roundID []byte, delegateIndex uint32) (bool, error) {
	key, err := types.DelegateBallotKey(roundID, delegateIndex)
	if err != nil {
		return false, err
	}
	return kvStore.Has(key)
}

// SetDelegateBallot stores ballot under its round and delegate index.
func (k *Keeper) SetDelegateBallot(kvStore store.KVStore, ballot *types.DelegateBallotRecord) error {
	key, err := types.DelegateBallotKey(ballot.VoteRoundId, ballot.DelegateIndex)
	if err != nil {
		return err
	}
	bz, err := marshalDeterministic(ballot)
	if err != nil {
		return err
	}
	return kvStore.Set(key, bz)
}

// IterateDelegateBallots calls cb with each ballot of round roundID whose
// delegate index is above startAfter, in ascending index order, until cb
// returns true.
func (k *Keeper) IterateDelegateBallots(kvStore store.KVStore, roundID []byte, startAfter uint32, cb func(*types.DelegateBallotRecord) bool) error {
	prefix, err := types.DelegateBallotPrefixForRound(roundID)
	if err != nil {
		return err
	}
	if startAfter == math.MaxUint32 {
		return nil
	}
	start, err := types.DelegateBallotKey(roundID, startAfter+1)
	if err != nil {
		return err
	}
	return iterateDelegateBallots(kvStore, start, types.PrefixEndBytes(prefix), cb)
}

// IterateAllDelegateBallots calls cb with every stored ballot, ordered by
// round id and then delegate index, until cb returns true.
func (k *Keeper) IterateAllDelegateBallots(kvStore store.KVStore, cb func(*types.DelegateBallotRecord) bool) error {
	return iterateDelegateBallots(kvStore, types.DelegateBallotPrefix, types.PrefixEndBytes(types.DelegateBallotPrefix), cb)
}

// iterateDelegateBallots calls cb with each ballot stored in [start, end), in
// key order, until cb returns true.
func iterateDelegateBallots(kvStore store.KVStore, start, end []byte, cb func(*types.DelegateBallotRecord) bool) error {
	iter, err := kvStore.Iterator(start, end)
	if err != nil {
		return err
	}
	defer iter.Close()
	for ; iter.Valid(); iter.Next() {
		var ballot types.DelegateBallotRecord
		if err := unmarshal(iter.Value(), &ballot); err != nil {
			return errorsmod.Wrapf(err, "decode delegate ballot at key %x", iter.Key())
		}
		if cb(&ballot) {
			return nil
		}
	}
	return nil
}
