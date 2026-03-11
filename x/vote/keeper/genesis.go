package keeper

import (
	"fmt"

	"cosmossdk.io/core/store"

	"github.com/valargroup/shielded-vote/x/vote/types"
)

// InitGenesis initializes the vote module state from a genesis state.
func (k *Keeper) InitGenesis(kvStore store.KVStore, genesis *types.GenesisState) error {
	if genesis == nil {
		return nil
	}

	// Restore vote rounds.
	for _, round := range genesis.Rounds {
		if err := k.SetVoteRound(kvStore, round); err != nil {
			return err
		}
	}

	// Restore commitment tree state.
	if genesis.TreeState != nil {
		if err := k.SetCommitmentTreeState(kvStore, genesis.TreeState); err != nil {
			return err
		}
	}

	// Restore commitment leaves.
	for _, leaf := range genesis.CommitmentLeaves {
		if err := kvStore.Set(types.CommitmentLeafKey(leaf.Index), leaf.Value); err != nil {
			return err
		}
	}

	// Restore vote manager.
	if genesis.VoteManager != "" {
		if err := k.SetVoteManager(kvStore, &types.VoteManagerState{Address: genesis.VoteManager}); err != nil {
			return err
		}
	}

	// Restore nullifiers (scoped by type + round).
	for _, entry := range genesis.Nullifiers {
		nfType := types.NullifierType(entry.NullifierType)
		if err := k.SetNullifier(kvStore, nfType, entry.RoundId, entry.Nullifier); err != nil {
			return err
		}
	}

	// Restore tally results.
	for _, result := range genesis.TallyResults {
		if err := k.SetTallyResult(kvStore, result); err != nil {
			return err
		}
	}

	// Restore Pallas key registry.
	for _, vpk := range genesis.PallasKeys {
		if err := k.SetPallasKey(kvStore, vpk); err != nil {
			return err
		}
	}

	// Restore tally accumulators (raw ElGamal ciphertexts).
	for _, acc := range genesis.TallyAccumulators {
		key, err := types.TallyKey(acc.RoundId, acc.ProposalId, acc.VoteDecision)
		if err != nil {
			return fmt.Errorf("tally accumulator: %w", err)
		}
		if err := kvStore.Set(key, acc.Ciphertext); err != nil {
			return err
		}
	}

	// Restore share counts.
	for _, sc := range genesis.ShareCounts {
		key, err := types.ShareCountKey(sc.RoundId, sc.ProposalId, sc.VoteDecision)
		if err != nil {
			return fmt.Errorf("share count: %w", err)
		}
		val := make([]byte, 8)
		putUint64BE(val, sc.Count)
		if err := kvStore.Set(key, val); err != nil {
			return err
		}
	}

	// Restore commitment roots by height.
	for _, cr := range genesis.CommitmentRoots {
		if err := k.SetCommitmentRootAtHeight(kvStore, cr.Height, cr.Root); err != nil {
			return err
		}
	}

	return nil
}

// ExportGenesis returns the current vote module genesis state by iterating
// all KV prefixes. The exported state can be imported by InitGenesis to
// fully restore the module.
func (k *Keeper) ExportGenesis(kvStore store.KVStore) (*types.GenesisState, error) {
	gs := &types.GenesisState{}

	// Tree state (singleton).
	state, err := k.GetCommitmentTreeState(kvStore)
	if err != nil {
		return nil, err
	}
	gs.TreeState = state

	// Vote manager (singleton).
	vm, err := k.GetVoteManager(kvStore)
	if err != nil {
		return nil, err
	}
	if vm != nil {
		gs.VoteManager = vm.Address
	}

	// Vote rounds (0x04 prefix).
	if err := k.IterateAllRounds(kvStore, func(round *types.VoteRound) bool {
		gs.Rounds = append(gs.Rounds, round)
		return false
	}); err != nil {
		return nil, fmt.Errorf("export rounds: %w", err)
	}

	// Commitment leaves (0x02 prefix).
	leaves, err := exportCommitmentLeaves(kvStore)
	if err != nil {
		return nil, fmt.Errorf("export commitment leaves: %w", err)
	}
	gs.CommitmentLeaves = leaves

	// Nullifiers (0x01 prefix).
	nullifiers, err := exportNullifiers(kvStore)
	if err != nil {
		return nil, fmt.Errorf("export nullifiers: %w", err)
	}
	gs.Nullifiers = nullifiers

	// Tally results (0x07 prefix).
	tallyResults, err := exportTallyResults(kvStore)
	if err != nil {
		return nil, fmt.Errorf("export tally results: %w", err)
	}
	gs.TallyResults = tallyResults

	// Pallas key registry (0x0C prefix).
	if err := k.IterateAllPallasKeys(kvStore, func(vpk *types.ValidatorPallasKey) bool {
		gs.PallasKeys = append(gs.PallasKeys, vpk)
		return false
	}); err != nil {
		return nil, fmt.Errorf("export pallas keys: %w", err)
	}

	// Tally accumulators (0x05 prefix).
	accumulators, err := exportTallyAccumulators(kvStore)
	if err != nil {
		return nil, fmt.Errorf("export tally accumulators: %w", err)
	}
	gs.TallyAccumulators = accumulators

	// Share counts (0x0B prefix).
	shareCounts, err := exportShareCounts(kvStore)
	if err != nil {
		return nil, fmt.Errorf("export share counts: %w", err)
	}
	gs.ShareCounts = shareCounts

	// Commitment roots by height (0x03 prefix).
	roots, err := exportCommitmentRoots(kvStore)
	if err != nil {
		return nil, fmt.Errorf("export commitment roots: %w", err)
	}
	gs.CommitmentRoots = roots

	return gs, nil
}

// exportCommitmentLeaves iterates the 0x02 prefix and returns all leaves.
// Key format: 0x02 || uint64 BE index -> commitment_bytes
func exportCommitmentLeaves(kvStore store.KVStore) ([]*types.CommitmentLeaf, error) {
	prefix := types.CommitmentLeafPrefix
	end := types.PrefixEndBytes(prefix)

	iter, err := kvStore.Iterator(prefix, end)
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	var leaves []*types.CommitmentLeaf
	for ; iter.Valid(); iter.Next() {
		key := iter.Key()
		index := getUint64BE(key[len(prefix):])
		val := iter.Value()
		leaf := make([]byte, len(val))
		copy(leaf, val)
		leaves = append(leaves, &types.CommitmentLeaf{
			Index: index,
			Value: leaf,
		})
	}
	return leaves, nil
}

// exportNullifiers iterates the 0x01 prefix and returns all nullifier entries.
// Key format: 0x01 || type_byte || round_id (32 bytes) || nullifier_bytes -> []byte{1}
func exportNullifiers(kvStore store.KVStore) ([]*types.NullifierEntry, error) {
	prefix := types.NullifierPrefix
	end := types.PrefixEndBytes(prefix)

	iter, err := kvStore.Iterator(prefix, end)
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	var entries []*types.NullifierEntry
	prefixLen := len(prefix)
	for ; iter.Valid(); iter.Next() {
		key := iter.Key()
		// key layout: prefix(1) || type(1) || round_id(32) || nullifier(rest)
		if len(key) < prefixLen+1+types.RoundIDLen {
			continue
		}
		nfType := key[prefixLen]
		roundID := make([]byte, types.RoundIDLen)
		copy(roundID, key[prefixLen+1:prefixLen+1+types.RoundIDLen])
		nf := make([]byte, len(key)-(prefixLen+1+types.RoundIDLen))
		copy(nf, key[prefixLen+1+types.RoundIDLen:])

		entries = append(entries, &types.NullifierEntry{
			NullifierType: uint32(nfType),
			RoundId:       roundID,
			Nullifier:     nf,
		})
	}
	return entries, nil
}

// exportTallyResults iterates the 0x07 prefix and returns all finalized tally results.
func exportTallyResults(kvStore store.KVStore) ([]*types.TallyResult, error) {
	prefix := types.TallyResultPrefix
	end := types.PrefixEndBytes(prefix)

	iter, err := kvStore.Iterator(prefix, end)
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	var results []*types.TallyResult
	for ; iter.Valid(); iter.Next() {
		var result types.TallyResult
		if err := unmarshal(iter.Value(), &result); err != nil {
			return nil, err
		}
		results = append(results, &result)
	}
	return results, nil
}

// exportTallyAccumulators iterates the 0x05 prefix and returns in-progress tally ciphertexts.
// Key format: 0x05 || round_id (32 B) || uint32 BE proposal_id || uint32 BE decision -> ciphertext bytes
func exportTallyAccumulators(kvStore store.KVStore) ([]*types.GenesisTallyAccumulator, error) {
	prefix := types.TallyPrefix
	end := types.PrefixEndBytes(prefix)

	iter, err := kvStore.Iterator(prefix, end)
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	var accumulators []*types.GenesisTallyAccumulator
	prefixLen := len(prefix)
	for ; iter.Valid(); iter.Next() {
		key := iter.Key()
		// key layout: prefix(1) || round_id(32) || proposal_id(4) || decision(4)
		if len(key) < prefixLen+types.RoundIDLen+8 {
			continue
		}
		roundID := make([]byte, types.RoundIDLen)
		copy(roundID, key[prefixLen:prefixLen+types.RoundIDLen])
		proposalID := getUint32BE(key[prefixLen+types.RoundIDLen:])
		decision := getUint32BE(key[prefixLen+types.RoundIDLen+4:])

		val := iter.Value()
		ct := make([]byte, len(val))
		copy(ct, val)

		accumulators = append(accumulators, &types.GenesisTallyAccumulator{
			RoundId:      roundID,
			ProposalId:   proposalID,
			VoteDecision: decision,
			Ciphertext:   ct,
		})
	}
	return accumulators, nil
}

// exportShareCounts iterates the 0x0B prefix and returns all share counts.
// Key format: 0x0B || round_id (32 B) || uint32 BE proposal_id || uint32 BE decision -> uint64 BE count
func exportShareCounts(kvStore store.KVStore) ([]*types.GenesisShareCount, error) {
	prefix := types.ShareCountPrefix
	end := types.PrefixEndBytes(prefix)

	iter, err := kvStore.Iterator(prefix, end)
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	var counts []*types.GenesisShareCount
	prefixLen := len(prefix)
	for ; iter.Valid(); iter.Next() {
		key := iter.Key()
		// key layout: prefix(1) || round_id(32) || proposal_id(4) || decision(4)
		if len(key) < prefixLen+types.RoundIDLen+8 {
			continue
		}
		roundID := make([]byte, types.RoundIDLen)
		copy(roundID, key[prefixLen:prefixLen+types.RoundIDLen])
		proposalID := getUint32BE(key[prefixLen+types.RoundIDLen:])
		decision := getUint32BE(key[prefixLen+types.RoundIDLen+4:])

		val := iter.Value()
		if len(val) < 8 {
			continue
		}
		count := getUint64BE(val)

		counts = append(counts, &types.GenesisShareCount{
			RoundId:      roundID,
			ProposalId:   proposalID,
			VoteDecision: decision,
			Count:        count,
		})
	}
	return counts, nil
}

// exportCommitmentRoots iterates the 0x03 prefix and returns all commitment tree roots.
// Key format: 0x03 || uint64 BE height -> root_bytes
func exportCommitmentRoots(kvStore store.KVStore) ([]*types.GenesisCommitmentRoot, error) {
	prefix := types.CommitmentRootByHeightPrefix
	end := types.PrefixEndBytes(prefix)

	iter, err := kvStore.Iterator(prefix, end)
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	var roots []*types.GenesisCommitmentRoot
	prefixLen := len(prefix)
	for ; iter.Valid(); iter.Next() {
		key := iter.Key()
		if len(key) < prefixLen+8 {
			continue
		}
		height := getUint64BE(key[prefixLen:])
		val := iter.Value()
		root := make([]byte, len(val))
		copy(root, val)

		roots = append(roots, &types.GenesisCommitmentRoot{
			Height: height,
			Root:   root,
		})
	}
	return roots, nil
}
