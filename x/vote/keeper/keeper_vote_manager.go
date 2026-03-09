package keeper

import (
	"context"
	"fmt"

	"cosmossdk.io/core/store"
	sdk "github.com/cosmos/cosmos-sdk/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"

	"github.com/valargroup/shielded-vote/x/vote/types"
)

// GetVoteManager retrieves the singleton vote manager state from the KV store.
// Returns nil, nil if no vote manager has been set yet.
func (k *Keeper) GetVoteManager(kvStore store.KVStore) (*types.VoteManagerState, error) {
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
func (k *Keeper) SetVoteManager(kvStore store.KVStore, state *types.VoteManagerState) error {
	bz, err := marshal(state)
	if err != nil {
		return err
	}
	return kvStore.Set(types.VoteManagerKey, bz)
}

// IsValidator checks whether the given address is a bonded validator.
func (k *Keeper) IsValidator(ctx context.Context, address string) bool {
	valAddr, err := sdk.ValAddressFromBech32(address)
	if err != nil {
		return false
	}
	val, err := k.stakingKeeper.GetValidator(ctx, valAddr)
	if err != nil {
		return false
	}
	return val.GetStatus() == stakingtypes.Bonded
}

// ValidateVoteManagerOnly checks that the creator is the current vote manager.
// Used for MsgCreateVotingSession and MsgSetVoteManager authorization.
func (k *Keeper) ValidateVoteManagerOnly(ctx context.Context, creator string) error {
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
