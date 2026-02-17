package keeper

import (
	"cosmossdk.io/core/store"

	"github.com/z-cale/zally/x/vote/types"
)

// InitGenesis initializes the vote module state from a genesis state.
func (k Keeper) InitGenesis(kvStore store.KVStore, genesis *types.GenesisState) error {
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

	return nil
}

// ExportGenesis returns the current vote module genesis state.
// NOTE: Full export (iterating all KV entries) will be implemented when needed.
// For now, this returns a minimal genesis state.
func (k Keeper) ExportGenesis(kvStore store.KVStore) (*types.GenesisState, error) {
	state, err := k.GetCommitmentTreeState(kvStore)
	if err != nil {
		return nil, err
	}

	gs := &types.GenesisState{
		TreeState: state,
		// TODO: Export rounds, leaves, and nullifiers by iterating store prefixes.
		// Nullifier entries must include NullifierType and RoundId fields.
	}

	// Export vote manager.
	vm, err := k.GetVoteManager(kvStore)
	if err != nil {
		return nil, err
	}
	if vm != nil {
		gs.VoteManager = vm.Address
	}

	return gs, nil
}
