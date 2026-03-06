package keeper

import (
	"context"
	"fmt"

	"cosmossdk.io/core/store"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/z-cale/zally/crypto/elgamal"
	"github.com/z-cale/zally/x/vote/types"
)

// ---------------------------------------------------------------------------
// Tally accumulation (ElGamal ciphertext homomorphic add)
// ---------------------------------------------------------------------------

// GetTally returns the accumulated ciphertext tally for a (round, proposal, decision) tuple.
// Returns nil if no tally exists for this tuple.
func (k *Keeper) GetTally(kvStore store.KVStore, roundID []byte, proposalID, decision uint32) ([]byte, error) {
	bz, err := kvStore.Get(types.TallyKey(roundID, proposalID, decision))
	if err != nil {
		return nil, err
	}
	return bz, nil // nil means no tally yet
}

// AddToTally accumulates an ElGamal ciphertext (encShareBytes, 64 bytes) into
// the tally for a (round, proposal, decision) tuple using HomomorphicAdd.
func (k *Keeper) AddToTally(kvStore store.KVStore, roundID []byte, proposalID, decision uint32, encShareBytes []byte) error {
	key := types.TallyKey(roundID, proposalID, decision)
	existing, err := kvStore.Get(key)
	if err != nil {
		return err
	}

	if existing == nil {
		// First share: validate it is a well-formed ElGamal ciphertext before
		// storing. Subsequent additions go through UnmarshalCiphertext anyway,
		// but skipping this check on the first share would leave a malformed
		// baseline in the KV store that breaks all later accumulations.
		if _, err := elgamal.UnmarshalCiphertext(encShareBytes); err != nil {
			return fmt.Errorf("failed to unmarshal first enc_share: %w", err)
		}
		return kvStore.Set(key, encShareBytes)
	}

	// Deserialize both, HomomorphicAdd, serialize result.
	acc, err := elgamal.UnmarshalCiphertext(existing)
	if err != nil {
		return fmt.Errorf("failed to unmarshal accumulator: %w", err)
	}
	share, err := elgamal.UnmarshalCiphertext(encShareBytes)
	if err != nil {
		return fmt.Errorf("failed to unmarshal enc_share: %w", err)
	}
	result := elgamal.HomomorphicAdd(acc, share)
	resultBytes, err := elgamal.MarshalCiphertext(result)
	if err != nil {
		return fmt.Errorf("failed to marshal accumulated ciphertext: %w", err)
	}
	return kvStore.Set(key, resultBytes)
}

// GetProposalTally returns all tallied ciphertexts for a (round, proposal) pair,
// keyed by decision ID. Iterates over the tally prefix
// 0x05 || round_id || proposal_id to collect all decision → ciphertext entries.
func (k *Keeper) GetProposalTally(kvStore store.KVStore, roundID []byte, proposalID uint32) (map[uint32][]byte, error) {
	prefix := types.TallyPrefixForProposal(roundID, proposalID)
	end := types.PrefixEndBytes(prefix)

	iter, err := kvStore.Iterator(prefix, end)
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	tally := make(map[uint32][]byte)
	for ; iter.Valid(); iter.Next() {
		key := iter.Key()
		val := iter.Value()

		// The decision is the last 4 bytes of the key.
		if len(key) < 4 {
			continue
		}
		decision := getUint32BE(key[len(key)-4:])
		// Store the raw ciphertext bytes.
		ct := make([]byte, len(val))
		copy(ct, val)
		tally[decision] = ct
	}

	return tally, nil
}

// ---------------------------------------------------------------------------
// Share count tracking (incremented by RevealShare, read by VoteSummary)
// ---------------------------------------------------------------------------

// IncrementShareCount atomically increments the share reveal count for a
// (round, proposal, decision) tuple. If no count exists yet, writes 1.
func (k *Keeper) IncrementShareCount(kvStore store.KVStore, roundID []byte, proposalID, decision uint32) error {
	key := types.ShareCountKey(roundID, proposalID, decision)
	bz, err := kvStore.Get(key)
	if err != nil {
		return err
	}
	var count uint64
	if len(bz) == 8 {
		count = getUint64BE(bz)
	}
	count++
	val := make([]byte, 8)
	putUint64BE(val, count)
	return kvStore.Set(key, val)
}

// GetShareCount returns the number of shares revealed for a (round, proposal, decision) tuple.
// Returns 0 if no shares have been revealed.
func (k *Keeper) GetShareCount(kvStore store.KVStore, roundID []byte, proposalID, decision uint32) (uint64, error) {
	bz, err := kvStore.Get(types.ShareCountKey(roundID, proposalID, decision))
	if err != nil {
		return 0, err
	}
	if len(bz) < 8 {
		return 0, nil
	}
	return getUint64BE(bz), nil
}

// GetVoteSummary builds a denormalized QueryVoteSummaryResponse for a vote round,
// including proposals with option labels, ballot counts, and (if finalized) totals.
func (k *Keeper) GetVoteSummary(kvStore store.KVStore, roundID []byte) (*types.QueryVoteSummaryResponse, error) {
	round, err := k.GetVoteRound(kvStore, roundID)
	if err != nil {
		return nil, err
	}

	proposals := make([]*types.ProposalSummary, len(round.Proposals))

	// If finalized, pre-load all tally results for this round.
	var tallyResults map[uint64]*types.TallyResult // key: (proposalID<<32)|decision
	if round.Status == types.SessionStatus_SESSION_STATUS_FINALIZED {
		results, err := k.GetAllTallyResults(kvStore, roundID)
		if err != nil {
			return nil, err
		}
		tallyResults = make(map[uint64]*types.TallyResult, len(results))
		for _, r := range results {
			key := uint64(r.ProposalId)<<32 | uint64(r.VoteDecision)
			tallyResults[key] = r
		}
	}

	for i, prop := range round.Proposals {
		options := make([]*types.OptionSummary, len(prop.Options))
		for j, opt := range prop.Options {
			count, err := k.GetShareCount(kvStore, roundID, prop.Id, opt.Index)
			if err != nil {
				return nil, err
			}
			os := &types.OptionSummary{
				Index:       opt.Index,
				Label:       opt.Label,
				BallotCount: count,
			}
			if tallyResults != nil {
				key := uint64(prop.Id)<<32 | uint64(opt.Index)
				if tr, ok := tallyResults[key]; ok {
					os.TotalValue = tr.TotalValue
				}
			}
			options[j] = os
		}
		proposals[i] = &types.ProposalSummary{
			Id:          prop.Id,
			Title:       prop.Title,
			Description: prop.Description,
			Options:     options,
		}
	}

	return &types.QueryVoteSummaryResponse{
		VoteRoundId: round.VoteRoundId,
		Status:      round.Status,
		Description: round.Description,
		VoteEndTime: round.VoteEndTime,
		Proposals:   proposals,
	}, nil
}

// ---------------------------------------------------------------------------
// Tally result storage (written by MsgSubmitTally, read by TallyResults query)
// ---------------------------------------------------------------------------

// SetTallyResult stores a finalized tally result for one (round, proposal, decision) tuple.
func (k *Keeper) SetTallyResult(kvStore store.KVStore, result *types.TallyResult) error {
	bz, err := marshal(result)
	if err != nil {
		return err
	}
	return kvStore.Set(types.TallyResultKey(result.VoteRoundId, result.ProposalId, result.VoteDecision), bz)
}

// GetTallyResult retrieves a finalized tally result for one (round, proposal, decision) tuple.
func (k *Keeper) GetTallyResult(kvStore store.KVStore, roundID []byte, proposalID, decision uint32) (*types.TallyResult, error) {
	bz, err := kvStore.Get(types.TallyResultKey(roundID, proposalID, decision))
	if err != nil {
		return nil, err
	}
	if bz == nil {
		return nil, nil
	}
	var result types.TallyResult
	if err := unmarshal(bz, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// GetAllTallyResults retrieves all finalized tally results for a vote round.
// Results are returned in key order (proposal_id, then decision).
func (k *Keeper) GetAllTallyResults(kvStore store.KVStore, roundID []byte) ([]*types.TallyResult, error) {
	prefix := types.TallyResultPrefixForRound(roundID)
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

// ---------------------------------------------------------------------------
// Tally validation helpers
// ---------------------------------------------------------------------------

// ValidateRoundForShares checks that a vote round exists and is in a state
// that accepts MsgRevealShare. Shares are accepted when the round is ACTIVE
// (with time check) or TALLYING (unconditionally).
func (k *Keeper) ValidateRoundForShares(ctx context.Context, roundID []byte) error {
	kvStore := k.OpenKVStore(ctx)
	round, err := k.GetVoteRound(kvStore, roundID)
	if err != nil {
		return err
	}

	switch round.Status {
	case types.SessionStatus_SESSION_STATUS_ACTIVE:
		// Belt-and-suspenders: also check time in case EndBlocker hasn't run yet.
		sdkCtx := sdk.UnwrapSDKContext(ctx)
		blockTime := uint64(sdkCtx.BlockTime().Unix())
		if blockTime >= round.VoteEndTime {
			// Time has passed but EndBlocker hasn't transitioned yet — still
			// accept shares (the round will become TALLYING this block).
			return nil
		}
		return nil

	case types.SessionStatus_SESSION_STATUS_TALLYING:
		// Tallying phase: shares are accepted unconditionally.
		return nil

	default:
		return fmt.Errorf("%w: status is %s", types.ErrRoundNotActive, round.Status)
	}
}

// ValidateRoundForTally checks that a vote round exists and is in TALLYING state.
func (k *Keeper) ValidateRoundForTally(ctx context.Context, roundID []byte) error {
	kvStore := k.OpenKVStore(ctx)
	round, err := k.GetVoteRound(kvStore, roundID)
	if err != nil {
		return err // wraps ErrRoundNotFound if missing
	}

	if round.Status != types.SessionStatus_SESSION_STATUS_TALLYING {
		return fmt.Errorf("%w: status is %s", types.ErrRoundNotTallying, round.Status)
	}

	return nil
}

