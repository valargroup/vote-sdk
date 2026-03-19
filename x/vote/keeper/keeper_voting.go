package keeper

import (
	"context"
	"fmt"

	"cosmossdk.io/core/store"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/valargroup/vote-sdk/x/vote/types"
)

// ---------------------------------------------------------------------------
// Nullifiers
// ---------------------------------------------------------------------------

// HasNullifier checks if a nullifier has already been recorded in the given
// type-scoped, round-scoped nullifier set.
func (k *Keeper) HasNullifier(ctx store.KVStore, nfType types.NullifierType, roundID, nullifier []byte) (bool, error) {
	key, err := types.NullifierKey(nfType, roundID, nullifier)
	if err != nil {
		return false, err
	}
	return ctx.Has(key)
}

// SetNullifier records a nullifier as spent in the given type-scoped,
// round-scoped nullifier set.
func (k *Keeper) SetNullifier(ctx store.KVStore, nfType types.NullifierType, roundID, nullifier []byte) error {
	key, err := types.NullifierKey(nfType, roundID, nullifier)
	if err != nil {
		return err
	}
	return ctx.Set(key, []byte{1})
}

// CheckAndSetNullifier atomically checks that a nullifier has not been recorded
// and then records it. Returns ErrDuplicateNullifier if already spent.
func (k *Keeper) CheckAndSetNullifier(kvStore store.KVStore, nfType types.NullifierType, roundID, nullifier []byte) error {
	has, err := k.HasNullifier(kvStore, nfType, roundID, nullifier)
	if err != nil {
		return err
	}
	if has {
		return fmt.Errorf("%w: nullifier already exists", types.ErrDuplicateNullifier)
	}
	return k.SetNullifier(kvStore, nfType, roundID, nullifier)
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
// Commitment tree (per-round)
// ---------------------------------------------------------------------------

// GetCommitmentTreeState returns the current state of the commitment tree for a round.
func (k *Keeper) GetCommitmentTreeState(kvStore store.KVStore, roundID []byte) (*types.CommitmentTreeState, error) {
	bz, err := kvStore.Get(types.RoundTreeStateKey(roundID))
	if err != nil {
		return nil, err
	}
	if bz == nil {
		return &types.CommitmentTreeState{NextIndex: 0}, nil
	}

	var state types.CommitmentTreeState
	if err := unmarshal(bz, &state); err != nil {
		return nil, err
	}
	return &state, nil
}

// SetCommitmentTreeState stores the commitment tree state for a round.
func (k *Keeper) SetCommitmentTreeState(kvStore store.KVStore, roundID []byte, state *types.CommitmentTreeState) error {
	bz, err := marshal(state)
	if err != nil {
		return err
	}
	return kvStore.Set(types.RoundTreeStateKey(roundID), bz)
}

// AppendCommitment appends a commitment to a round's tree and returns its index.
func (k *Keeper) AppendCommitment(kvStore store.KVStore, roundID, commitment []byte) (uint64, error) {
	state, err := k.GetCommitmentTreeState(kvStore, roundID)
	if err != nil {
		return 0, err
	}

	index := state.NextIndex

	if err := kvStore.Set(types.CommitmentLeafKey(roundID, index), commitment); err != nil {
		return 0, err
	}

	state.NextIndex = index + 1
	if err := k.SetCommitmentTreeState(kvStore, roundID, state); err != nil {
		return 0, err
	}

	return index, nil
}

// ---------------------------------------------------------------------------
// Block leaf index (per-round)
// ---------------------------------------------------------------------------

// SetBlockLeafIndex records the range of commitment leaves that were appended
// during a specific block height for a round.
func (k *Keeper) SetBlockLeafIndex(kvStore store.KVStore, roundID []byte, height, startIndex, count uint64) error {
	val := make([]byte, 16)
	putUint64BE(val[0:8], startIndex)
	putUint64BE(val[8:16], count)
	return kvStore.Set(types.BlockLeafIndexKey(roundID, height), val)
}

// GetBlockLeafIndex returns the (start_index, count) for leaves appended at
// the given block height for a round. Returns (0, 0, false) if no mapping exists.
func (k *Keeper) GetBlockLeafIndex(kvStore store.KVStore, roundID []byte, height uint64) (startIndex, count uint64, found bool, err error) {
	val, err := kvStore.Get(types.BlockLeafIndexKey(roundID, height))
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
// blocks from fromHeight to toHeight (inclusive) for a round.
func (k *Keeper) GetCommitmentLeaves(kvStore store.KVStore, roundID []byte, fromHeight, toHeight uint64) ([]*types.BlockCommitments, error) {
	startKey := types.BlockLeafIndexKey(roundID, fromHeight)
	endKey := types.BlockLeafIndexKey(roundID, toHeight+1)

	iter, err := kvStore.Iterator(startKey, endKey)
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	// The round prefix length preceding the inner block-leaf-index key.
	innerOffset := len(types.RoundTreeKey(roundID)) + len(types.BlockLeafIndexPrefix)

	var blocks []*types.BlockCommitments
	for ; iter.Valid(); iter.Next() {
		val := iter.Value()
		if len(val) < 16 {
			return nil, fmt.Errorf("corrupt BlockLeafIndex entry: expected 16 bytes, got %d", len(val))
		}
		startIndex := getUint64BE(val[0:8])
		count := getUint64BE(val[8:16])

		leaves := make([][]byte, count)
		for i := uint64(0); i < count; i++ {
			leaf, err := kvStore.Get(types.CommitmentLeafKey(roundID, startIndex+i))
			if err != nil {
				return nil, err
			}
			leaves[i] = leaf
		}

		key := iter.Key()
		height := getUint64BE(key[innerOffset:])

		blocks = append(blocks, &types.BlockCommitments{
			Height:     height,
			StartIndex: startIndex,
			Leaves:     leaves,
		})
	}

	return blocks, nil
}

// ---------------------------------------------------------------------------
// Commitment roots (per-round)
// ---------------------------------------------------------------------------

// GetCommitmentRootAtHeight returns the commitment tree root stored at a specific height for a round.
func (k *Keeper) GetCommitmentRootAtHeight(kvStore store.KVStore, roundID []byte, height uint64) ([]byte, error) {
	return kvStore.Get(types.CommitmentRootKey(roundID, height))
}

// SetCommitmentRootAtHeight stores the commitment tree root for a specific height and round.
func (k *Keeper) SetCommitmentRootAtHeight(kvStore store.KVStore, roundID []byte, height uint64, root []byte) error {
	return kvStore.Set(types.CommitmentRootKey(roundID, height), root)
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

// ValidateEntryBounds checks that proposalId and voteDecision are within
// the valid ranges for the given round. Unlike ValidateProposalId and
// ValidateVoteDecision, this takes the already-loaded round to avoid
// redundant KV lookups in hot loops (SubmitTally, SubmitPartialDecryption).
func ValidateEntryBounds(round *types.VoteRound, proposalId, voteDecision uint32) error {
	if proposalId < 1 || int(proposalId) > len(round.Proposals) {
		return fmt.Errorf("%w: proposal_id %d out of range [1, %d]",
			types.ErrInvalidProposalID, proposalId, len(round.Proposals))
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
