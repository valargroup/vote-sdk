package keeper

import (
	"fmt"

	"cosmossdk.io/core/store"

	"github.com/z-cale/zally/x/vote/types"
)

// PartialDecryptionWithIndex pairs a validator's 1-based Shamir index with the
// partial decryption data for one (proposal_id, vote_decision) accumulator.
// Returned by GetPartialDecryptionsForRound to give the tally combiner direct
// access to (index, D_i) without requiring it to re-parse KV keys.
type PartialDecryptionWithIndex struct {
	ValidatorIndex uint32 // 1-based Shamir evaluation point (matches Share.Index)
	PartialDecrypt []byte // 32-byte compressed Pallas point: D_i = share_i * C1
	DleqProof      []byte // reserved for Step 2; empty in Step 1
}

// AccumulatorKey packs a (proposalID, decision) pair into a single uint64 for
// use as a map key. proposalID occupies the high 32 bits.
func AccumulatorKey(proposalID, decision uint32) uint64 {
	return uint64(proposalID)<<32 | uint64(decision)
}

// SetPartialDecryptions stores all entries from a MsgSubmitPartialDecryption.
// Each entry is keyed by (roundID, validatorIndex, proposalID, decision).
// Returns an error if any individual write fails; the caller is responsible
// for transactional behaviour (the Cosmos SDK store is transactional per block).
func (k *Keeper) SetPartialDecryptions(
	kvStore store.KVStore,
	roundID []byte,
	validatorIndex uint32,
	entries []*types.PartialDecryptionEntry,
) error {
	for _, entry := range entries {
		bz, err := marshal(entry)
		if err != nil {
			return fmt.Errorf("keeper: SetPartialDecryptions: marshal entry (proposal=%d decision=%d): %w",
				entry.ProposalId, entry.VoteDecision, err)
		}
		key, err := types.PartialDecryptionKey(roundID, validatorIndex, entry.ProposalId, entry.VoteDecision)
		if err != nil {
			return fmt.Errorf("keeper: SetPartialDecryptions: key (proposal=%d decision=%d): %w",
				entry.ProposalId, entry.VoteDecision, err)
		}
		if err := kvStore.Set(key, bz); err != nil {
			return fmt.Errorf("keeper: SetPartialDecryptions: set (proposal=%d decision=%d): %w",
				entry.ProposalId, entry.VoteDecision, err)
		}
	}
	return nil
}

// GetPartialDecryption retrieves a single stored partial decryption entry.
// Returns nil, nil if no entry exists for the given key.
func (k *Keeper) GetPartialDecryption(
	kvStore store.KVStore,
	roundID []byte,
	validatorIndex, proposalID, decision uint32,
) (*types.PartialDecryptionEntry, error) {
	key, err := types.PartialDecryptionKey(roundID, validatorIndex, proposalID, decision)
	if err != nil {
		return nil, err
	}
	bz, err := kvStore.Get(key)
	if err != nil {
		return nil, err
	}
	if bz == nil {
		return nil, nil
	}
	var entry types.PartialDecryptionEntry
	if err := unmarshal(bz, &entry); err != nil {
		return nil, fmt.Errorf("keeper: GetPartialDecryption: unmarshal: %w", err)
	}
	return &entry, nil
}

// HasPartialDecryptionsFromValidator returns true if the given validator has
// already submitted any partial decryption entries for this round.
// Uses a prefix scan over 0x12 || round_id || validator_index.
func (k *Keeper) HasPartialDecryptionsFromValidator(
	kvStore store.KVStore,
	roundID []byte,
	validatorIndex uint32,
) (bool, error) {
	prefix, err := types.PartialDecryptionPrefixForValidator(roundID, validatorIndex)
	if err != nil {
		return false, err
	}
	end := types.PrefixEndBytes(prefix)

	iter, err := kvStore.Iterator(prefix, end)
	if err != nil {
		return false, err
	}
	defer iter.Close()

	return iter.Valid(), nil
}

// CountPartialDecryptionValidators returns the number of distinct validators
// that have submitted partial decryptions for the given round. Used by the
// tally combiner to check whether the threshold t has been reached.
func (k *Keeper) CountPartialDecryptionValidators(
	kvStore store.KVStore,
	roundID []byte,
) (int, error) {
	prefix, err := types.PartialDecryptionPrefixForRound(roundID)
	if err != nil {
		return 0, err
	}
	end := types.PrefixEndBytes(prefix)

	iter, err := kvStore.Iterator(prefix, end)
	if err != nil {
		return 0, err
	}
	defer iter.Close()

	// The key layout after the round_id prefix is: uint32 BE validator_index || ...
	// Reading the first 4 bytes after the prefix gives the validator_index for
	// each key; we count transitions between distinct validator_index values.
	prefixLen := len(prefix)
	var lastIdx uint32
	firstSeen := false
	count := 0

	for ; iter.Valid(); iter.Next() {
		key := iter.Key()
		if len(key) < prefixLen+4 {
			continue
		}
		idx := getUint32BE(key[prefixLen : prefixLen+4])
		if !firstSeen || idx != lastIdx {
			count++
			lastIdx = idx
			firstSeen = true
		}
	}
	return count, nil
}

// GetPartialDecryptionsForRound returns all stored partial decryptions for a
// round, grouped by accumulator. The map key is AccumulatorKey(proposalID, decision).
// Each slice element carries the validator_index and the D_i point bytes so the
// tally combiner can build shamir.PartialDecryption values directly.
func (k *Keeper) GetPartialDecryptionsForRound(
	kvStore store.KVStore,
	roundID []byte,
) (map[uint64][]PartialDecryptionWithIndex, error) {
	prefix, err := types.PartialDecryptionPrefixForRound(roundID)
	if err != nil {
		return nil, err
	}
	end := types.PrefixEndBytes(prefix)

	iter, err := kvStore.Iterator(prefix, end)
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	// Key layout after the round_id prefix:
	//   uint32 BE validator_index (4 bytes)
	//   uint32 BE proposal_id    (4 bytes)
	//   uint32 BE vote_decision  (4 bytes)
	prefixLen := len(prefix)
	result := make(map[uint64][]PartialDecryptionWithIndex)

	for ; iter.Valid(); iter.Next() {
		key := iter.Key()
		if len(key) < prefixLen+12 {
			return nil, fmt.Errorf("keeper: GetPartialDecryptionsForRound: corrupt key (len=%d)", len(key))
		}

		validatorIndex := getUint32BE(key[prefixLen : prefixLen+4])
		proposalID := getUint32BE(key[prefixLen+4 : prefixLen+8])
		decision := getUint32BE(key[prefixLen+8 : prefixLen+12])

		var entry types.PartialDecryptionEntry
		if err := unmarshal(iter.Value(), &entry); err != nil {
			return nil, fmt.Errorf("keeper: GetPartialDecryptionsForRound: unmarshal: %w", err)
		}

		accKey := AccumulatorKey(proposalID, decision)
		result[accKey] = append(result[accKey], PartialDecryptionWithIndex{
			ValidatorIndex: validatorIndex,
			PartialDecrypt: entry.PartialDecrypt,
			DleqProof:      entry.DleqProof,
		})
	}

	return result, nil
}
