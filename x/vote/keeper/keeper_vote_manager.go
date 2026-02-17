package keeper

import (
	"context"
	"fmt"

	"cosmossdk.io/core/store"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/z-cale/zally/x/vote/types"
)

// GetVoteManager retrieves the singleton vote manager state from the KV store.
// Returns nil, nil if no vote manager has been set yet.
func (k Keeper) GetVoteManager(kvStore store.KVStore) (*types.VoteManagerState, error) {
	bz, err := kvStore.Get(types.VoteManagerKey)
	if err != nil {
		return nil, err
	}
	if bz == nil {
		return nil, nil
	}

	var state types.VoteManagerState
	if err := unmarshal(bz, &state); err != nil {
		return nil, err
	}
	return &state, nil
}

// SetVoteManager stores the singleton vote manager state in the KV store.
func (k Keeper) SetVoteManager(kvStore store.KVStore, state *types.VoteManagerState) error {
	bz, err := marshal(state)
	if err != nil {
		return err
	}
	return kvStore.Set(types.VoteManagerKey, bz)
}

// IsValidator checks whether the given address is a bonded validator.
func (k Keeper) IsValidator(ctx context.Context, address string) bool {
	valAddr, err := sdk.ValAddressFromBech32(address)
	if err != nil {
		return false
	}
	_, err = k.stakingKeeper.GetValidator(ctx, valAddr)
	return err == nil
}

// ValidateVoteManagerOrValidator checks that the creator is either the current
// vote manager or any bonded validator. Used for MsgSetVoteManager authorization.
// On bootstrap (no vote manager set), accepts any bonded validator.
func (k Keeper) ValidateVoteManagerOrValidator(ctx context.Context, creator string) error {
	kvStore := k.OpenKVStore(ctx)
	mgr, err := k.GetVoteManager(kvStore)
	if err != nil {
		return err
	}

	// Bootstrap: no vote manager set — accept any bonded validator.
	if mgr == nil {
		if !k.IsValidator(ctx, creator) {
			return fmt.Errorf("%w: no vote manager set and sender %s is not a validator", types.ErrNotAuthorized, creator)
		}
		return nil
	}

	// Accept if sender is the current vote manager.
	if mgr.Address == creator {
		return nil
	}

	// Accept if sender is any bonded validator.
	if k.IsValidator(ctx, creator) {
		return nil
	}

	return fmt.Errorf("%w: sender %s is not the vote manager or a validator", types.ErrNotAuthorized, creator)
}

// ValidateVoteManagerOnly checks that the creator is the current vote manager.
// Used for MsgCreateVotingSession authorization.
func (k Keeper) ValidateVoteManagerOnly(ctx context.Context, creator string) error {
	kvStore := k.OpenKVStore(ctx)
	mgr, err := k.GetVoteManager(kvStore)
	if err != nil {
		return err
	}

	if mgr == nil {
		return fmt.Errorf("%w", types.ErrNoVoteManager)
	}

	if mgr.Address != creator {
		return fmt.Errorf("%w: sender %s is not the vote manager %s", types.ErrNotAuthorized, creator, mgr.Address)
	}

	return nil
}
