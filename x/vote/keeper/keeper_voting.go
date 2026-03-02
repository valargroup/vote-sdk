package keeper

import (
	"context"
	"fmt"

	"cosmossdk.io/core/store"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/z-cale/zally/x/vote/types"
)

// ---------------------------------------------------------------------------
// Nullifiers
// ---------------------------------------------------------------------------

// HasNullifier checks if a nullifier has already been recorded in the given
// type-scoped, round-scoped nullifier set.
func (k *Keeper) HasNullifier(ctx store.KVStore, nfType types.NullifierType, roundID, nullifier []byte) (bool, error) {
	return ctx.Has(types.NullifierKey(nfType, roundID, nullifier))
}

// SetNullifier records a nullifier as spent in the given type-scoped,
// round-scoped nullifier set.
func (k *Keeper) SetNullifier(ctx store.KVStore, nfType types.NullifierType, roundID, nullifier []byte) error {
	return ctx.Set(types.NullifierKey(nfType, roundID, nullifier), []byte{1})
}

// CheckNullifiersUnique verifies that none of the provided nullifiers have
// already been recorded in the type-scoped, round-scoped nullifier set.
// This runs on every check including RecheckTx, because nullifiers may have
// been consumed by the newly committed block.
func (k *Keeper) CheckNullifiersUnique(ctx context.Context, nfType types.NullifierType, roundID []byte, nullifiers [][]byte) error {
	kvStore := k.OpenKVStore(ctx)
	for _, nf := range nullifiers {
		has, err := k.HasNullifier(kvStore, nfType, roundID, nf)
		if err != nil {
			return err
		}
		if has {
			return fmt.Errorf("%w: %x", types.ErrDuplicateNullifier, nf)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Commitment tree
// ---------------------------------------------------------------------------

// GetCommitmentTreeState returns the current state of the commitment tree.
func (k *Keeper) GetCommitmentTreeState(kvStore store.KVStore) (*types.CommitmentTreeState, error) {
	bz, err := kvStore.Get(types.TreeStateKey)
	if err != nil {
		return nil, err
	}
	if bz == nil {
		// Return default state if not initialized.
		return &types.CommitmentTreeState{NextIndex: 0}, nil
	}

	var state types.CommitmentTreeState
	if err := unmarshal(bz, &state); err != nil {
		return nil, err
	}
	return &state, nil
}

// SetCommitmentTreeState stores the commitment tree state.
func (k *Keeper) SetCommitmentTreeState(kvStore store.KVStore, state *types.CommitmentTreeState) error {
	bz, err := marshal(state)
	if err != nil {
		return err
	}
	return kvStore.Set(types.TreeStateKey, bz)
}

// AppendCommitment appends a commitment to the tree and returns its index.
func (k *Keeper) AppendCommitment(kvStore store.KVStore, commitment []byte) (uint64, error) {
	state, err := k.GetCommitmentTreeState(kvStore)
	if err != nil {
		return 0, err
	}

	index := state.NextIndex

	// Write the leaf.
	if err := kvStore.Set(types.CommitmentLeafKey(index), commitment); err != nil {
		return 0, err
	}

	// Increment next_index.
	state.NextIndex = index + 1
	if err := k.SetCommitmentTreeState(kvStore, state); err != nil {
		return 0, err
	}

	return index, nil
}

// ---------------------------------------------------------------------------
// Block leaf index
// ---------------------------------------------------------------------------

// SetBlockLeafIndex records the range of commitment leaves that were appended
// during a specific block height. Value format: start_index (uint64 BE) || count (uint64 BE).
func (k *Keeper) SetBlockLeafIndex(kvStore store.KVStore, height, startIndex, count uint64) error {
	val := make([]byte, 16)
	putUint64BE(val[0:8], startIndex)
	putUint64BE(val[8:16], count)
	return kvStore.Set(types.BlockLeafIndexKey(height), val)
}

// GetBlockLeafIndex returns the (start_index, count) for leaves appended at
// the given block height. Returns (0, 0, false) if no mapping exists.
func (k *Keeper) GetBlockLeafIndex(kvStore store.KVStore, height uint64) (startIndex, count uint64, found bool, err error) {
	val, err := kvStore.Get(types.BlockLeafIndexKey(height))
	if err != nil {
		return 0, 0, false, err
	}
	if len(val) < 16 {
		return 0, 0, false, nil
	}
	startIndex = getUint64BE(val[0:8])
	count = getUint64BE(val[8:16])
	return startIndex, count, true, nil
}

// GetCommitmentLeaves returns the commitment leaves that were appended during
// blocks from fromHeight to toHeight (inclusive). Each entry contains the block
// height, the starting leaf index, and the leaves themselves.
func (k *Keeper) GetCommitmentLeaves(kvStore store.KVStore, fromHeight, toHeight uint64) ([]*types.BlockCommitments, error) {
	// Iterate over the BlockLeafIndex prefix for the requested height range.
	startKey := types.BlockLeafIndexKey(fromHeight)
	// End key is exclusive: the key just after toHeight.
	endKey := types.BlockLeafIndexKey(toHeight + 1)

	iter, err := kvStore.Iterator(startKey, endKey)
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	var blocks []*types.BlockCommitments
	for ; iter.Valid(); iter.Next() {
		val := iter.Value()
		if len(val) < 16 {
			return nil, fmt.Errorf("corrupt BlockLeafIndex entry: expected 16 bytes, got %d", len(val))
		}
		startIndex := getUint64BE(val[0:8])
		count := getUint64BE(val[8:16])

		// Read the actual leaves from the commitment leaf store.
		leaves := make([][]byte, count)
		for i := uint64(0); i < count; i++ {
			leaf, err := kvStore.Get(types.CommitmentLeafKey(startIndex + i))
			if err != nil {
				return nil, err
			}
			leaves[i] = leaf
		}

		// Extract height from the key: prefix (1 byte) + height (8 bytes BE).
		key := iter.Key()
		height := getUint64BE(key[len(types.BlockLeafIndexPrefix):])

		blocks = append(blocks, &types.BlockCommitments{
			Height:     height,
			StartIndex: startIndex,
			Leaves:     leaves,
		})
	}

	return blocks, nil
}

// ---------------------------------------------------------------------------
// Commitment roots
// ---------------------------------------------------------------------------

// GetCommitmentRootAtHeight returns the commitment tree root stored at a specific height.
func (k *Keeper) GetCommitmentRootAtHeight(kvStore store.KVStore, height uint64) ([]byte, error) {
	return kvStore.Get(types.CommitmentRootKey(height))
}

// SetCommitmentRootAtHeight stores the commitment tree root for a specific height.
func (k *Keeper) SetCommitmentRootAtHeight(kvStore store.KVStore, height uint64, root []byte) error {
	return kvStore.Set(types.CommitmentRootKey(height), root)
}

// ---------------------------------------------------------------------------
// Proposal validation
// ---------------------------------------------------------------------------

// ValidateProposalId checks that proposalId is valid for the round (1-indexed).
// This 1-indexed value is passed directly to the ZKP circuit as the bit-position
// in the proposal_authority bitmask. The circuit's non-zero gate rejects 0,
// aligning on-chain validation with circuit semantics.
func (k *Keeper) ValidateProposalId(kvStore store.KVStore, roundID []byte, proposalId uint32) error {
	round, err := k.GetVoteRound(kvStore, roundID)
	if err != nil {
		return err
	}
	if proposalId < 1 || int(proposalId) > len(round.Proposals) {
		return fmt.Errorf("%w: proposal_id %d out of range [1, %d]", types.ErrInvalidProposalID, proposalId, len(round.Proposals))
	}
	return nil
}

// ValidateVoteDecision checks that voteDecision is a valid option index for the
// given proposal within the round. Proposals are 1-indexed; vote decisions are
// 0-indexed into the proposal's options list.
func (k *Keeper) ValidateVoteDecision(kvStore store.KVStore, roundID []byte, proposalId, voteDecision uint32) error {
	round, err := k.GetVoteRound(kvStore, roundID)
	if err != nil {
		return err
	}
	if proposalId < 1 || int(proposalId) > len(round.Proposals) {
		return fmt.Errorf("%w: proposal_id %d out of range [1, %d]", types.ErrInvalidProposalID, proposalId, len(round.Proposals))
	}
	proposal := round.Proposals[proposalId-1]
	if int(voteDecision) >= len(proposal.Options) {
		return fmt.Errorf("%w: vote_decision %d out of range [0, %d) for proposal %d",
			types.ErrInvalidField, voteDecision, len(proposal.Options), proposalId)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Voting round validation
// ---------------------------------------------------------------------------

// ValidateRoundForVoting checks that a vote round exists, has ACTIVE status,
// and has not expired (belt-and-suspenders: EndBlocker may not have run yet
// this block).
func (k *Keeper) ValidateRoundForVoting(ctx context.Context, roundID []byte) error {
	kvStore := k.OpenKVStore(ctx)
	round, err := k.GetVoteRound(kvStore, roundID)
	if err != nil {
		return err // wraps ErrRoundNotFound if missing
	}

	if round.Status != types.SessionStatus_SESSION_STATUS_ACTIVE {
		return fmt.Errorf("%w: status is %s", types.ErrRoundNotActive, round.Status)
	}

	sdkCtx := sdk.UnwrapSDKContext(ctx)
	blockTime := uint64(sdkCtx.BlockTime().Unix())

	if blockTime >= round.VoteEndTime {
		return fmt.Errorf("%w: vote_end_time %d <= block_time %d", types.ErrRoundNotActive, round.VoteEndTime, blockTime)
	}

	return nil
}

// ValidateRoundActive checks that a vote round exists and has not expired.
// Deprecated: Use ValidateRoundForVoting or ValidateRoundForShares instead.
// Kept as a thin wrapper to minimize churn in existing callers.
func (k *Keeper) ValidateRoundActive(ctx context.Context, roundID []byte) error {
	return k.ValidateRoundForVoting(ctx, roundID)
}
