package types

import (
	"bytes"
	"fmt"
	"math"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/valargroup/vote-sdk/crypto/elgamal"
	"github.com/valargroup/vote-sdk/crypto/pallas"
)

// ValidateAndNormalizeVoteManagerSet parses each address through bech32, rejects
// duplicates (on the canonical form, so mixed-case variants don't slip
// through), and returns the canonical list. The input list is not mutated.
// Returns ErrEmptyVoteManagerSet when the list is empty.
//
// Shared by ValidateGenesisState and the MsgUpdateVoteManagers handler so both
// paths apply the same admissibility rules.
func ValidateAndNormalizeVoteManagerSet(addrs []string) ([]string, error) {
	normalized, _, err := ValidateAndNormalizeVoteManagerPolicy(addrs, 1)
	return normalized, err
}

// NormalizeVoteManagerThreshold applies the genesis/config default. A zero
// threshold means "use default 1"; any non-zero value is preserved for
// validation against the manager set.
func NormalizeVoteManagerThreshold(threshold uint32) uint32 {
	if threshold == 0 {
		return 1
	}
	return threshold
}

// NormalizeMinCeremonyValidators applies the default minimum ceremony validator
// count. Zero means "use default 1".
func NormalizeMinCeremonyValidators(minValidators uint32) uint32 {
	if minValidators == 0 {
		return 1
	}
	return minValidators
}

// ValidateAndNormalizeVoteManagerPolicy parses each manager address through
// bech32, rejects duplicates, applies the default threshold, and enforces
// 1 <= threshold <= len(addrs). The input list is not mutated.
func ValidateAndNormalizeVoteManagerPolicy(addrs []string, threshold uint32) ([]string, uint32, error) {
	if len(addrs) == 0 {
		return nil, 0, fmt.Errorf("%w", ErrEmptyVoteManagerSet)
	}
	seen := make(map[string]struct{}, len(addrs))
	normalized := make([]string, 0, len(addrs))
	for i, addr := range addrs {
		acc, err := sdk.AccAddressFromBech32(addr)
		if err != nil {
			return nil, 0, fmt.Errorf("[%d] %q is not a valid bech32 address: %w", i, addr, err)
		}
		canonical := acc.String()
		if _, dup := seen[canonical]; dup {
			return nil, 0, fmt.Errorf("%w: %s", ErrDuplicateVoteManager, canonical)
		}
		seen[canonical] = struct{}{}
		normalized = append(normalized, canonical)
	}
	effectiveThreshold := NormalizeVoteManagerThreshold(threshold)
	if effectiveThreshold < 1 {
		return nil, 0, fmt.Errorf("%w: vote-manager threshold must be at least 1", ErrInvalidThreshold)
	}
	if effectiveThreshold > uint32(len(normalized)) {
		return nil, 0, fmt.Errorf("%w: vote-manager threshold %d exceeds manager count %d", ErrInvalidThreshold, effectiveThreshold, len(normalized))
	}
	return normalized, effectiveThreshold, nil
}

// ValidateGenesisState performs structural validation of the vote module genesis state.
func ValidateGenesisState(gs *GenesisState) error {
	if gs == nil {
		return nil
	}
	if err := validateGenesisRecordPresence(gs); err != nil {
		return err
	}

	// Validate rounds: IDs must be 32 bytes, no duplicates.
	seenRounds := make(map[string]struct{}, len(gs.Rounds))
	for i, round := range gs.Rounds {
		if len(round.VoteRoundId) != RoundIDLen {
			return fmt.Errorf("rounds[%d].vote_round_id is %d bytes, expected %d", i, len(round.VoteRoundId), RoundIDLen)
		}
		if round.VoteEndTime == 0 {
			return fmt.Errorf("rounds[%d].vote_end_time cannot be zero", i)
		}
		if round.CeremonyPhaseTimeout > 0 && round.CeremonyPhaseStart > math.MaxUint64-round.CeremonyPhaseTimeout {
			return fmt.Errorf("rounds[%d] ceremony phase deadline overflows uint64", i)
		}
		if round.TallyPhaseTimeout > 0 && round.TallyPhaseStart > math.MaxUint64-round.TallyPhaseTimeout {
			return fmt.Errorf("rounds[%d] tally phase deadline overflows uint64", i)
		}
		key := string(round.VoteRoundId)
		if _, dup := seenRounds[key]; dup {
			return fmt.Errorf("rounds[%d]: duplicate vote_round_id %x", i, round.VoteRoundId)
		}
		seenRounds[key] = struct{}{}
	}
	if err := validateGenesisRoundTrees(gs.RoundTrees, seenRounds); err != nil {
		return err
	}

	// Validate nullifiers: type in {0,1,2}, round_id is 32 bytes, nullifier is non-empty.
	for i, entry := range gs.Nullifiers {
		if entry.NullifierType > 2 {
			return fmt.Errorf("nullifiers[%d].nullifier_type is %d, expected 0-2", i, entry.NullifierType)
		}
		if len(entry.RoundId) != RoundIDLen {
			return fmt.Errorf("nullifiers[%d].round_id is %d bytes, expected %d", i, len(entry.RoundId), RoundIDLen)
		}
		if len(entry.Nullifier) == 0 {
			return fmt.Errorf("nullifiers[%d].nullifier is empty", i)
		}
	}

	// Vote-manager set is required in genesis — there is no bootstrap path.
	if _, _, err := ValidateAndNormalizeVoteManagerPolicy(gs.VoteManagerAddresses, gs.VoteManagerThreshold); err != nil {
		return fmt.Errorf("vote_manager_addresses: %w", err)
	}

	// min_ceremony_validators must be at least 1 when explicitly set.
	// A zero value means "use default (1)", so we only reject values that
	// are explicitly invalid once we enforce a minimum.
	// (No explicit validation needed: 0 is treated as default 1 in InitGenesis.)

	type tallyRecordKey struct {
		roundID      string
		proposalID   uint32
		voteDecision uint32
	}

	// Validate tally results.
	seenTallyResults := make(map[tallyRecordKey]struct{}, len(gs.TallyResults))
	for i, result := range gs.TallyResults {
		if len(result.VoteRoundId) != RoundIDLen {
			return fmt.Errorf("tally_results[%d].vote_round_id is %d bytes, expected %d", i, len(result.VoteRoundId), RoundIDLen)
		}
		key := tallyRecordKey{string(result.VoteRoundId), result.ProposalId, result.VoteDecision}
		if _, dup := seenTallyResults[key]; dup {
			return fmt.Errorf("tally_results[%d]: duplicate round/proposal/decision tuple", i)
		}
		seenTallyResults[key] = struct{}{}
	}

	// Validate Pallas keys.
	seenPallasValidators := make(map[string]struct{}, len(gs.PallasKeys))
	seenPallasKeys := make(map[string]struct{}, len(gs.PallasKeys))
	for i, vpk := range gs.PallasKeys {
		if vpk.ValidatorAddress == "" {
			return fmt.Errorf("pallas_keys[%d].validator_address is empty", i)
		}
		valAddr, err := sdk.ValAddressFromBech32(vpk.ValidatorAddress)
		if err != nil {
			return fmt.Errorf("pallas_keys[%d].validator_address %q is not a valid bech32 validator address: %w", i, vpk.ValidatorAddress, err)
		}
		canonicalAddress := valAddr.String()
		if _, dup := seenPallasValidators[canonicalAddress]; dup {
			return fmt.Errorf("pallas_keys[%d]: duplicate validator_address %s", i, canonicalAddress)
		}
		seenPallasValidators[canonicalAddress] = struct{}{}
		if _, err := elgamal.UnmarshalPublicKey(vpk.PallasPk); err != nil {
			return fmt.Errorf("pallas_keys[%d].pallas_pk is invalid: %w", i, err)
		}
		key := string(vpk.PallasPk)
		if _, dup := seenPallasKeys[key]; dup {
			return fmt.Errorf("pallas_keys[%d]: duplicate pallas_pk", i)
		}
		seenPallasKeys[key] = struct{}{}
	}

	// Validate tally accumulators.
	seenTallyAccumulators := make(map[tallyRecordKey]struct{}, len(gs.TallyAccumulators))
	for i, acc := range gs.TallyAccumulators {
		if len(acc.RoundId) != RoundIDLen {
			return fmt.Errorf("tally_accumulators[%d].round_id is %d bytes, expected %d", i, len(acc.RoundId), RoundIDLen)
		}
		if len(acc.Ciphertext) != 64 {
			return fmt.Errorf("tally_accumulators[%d].ciphertext is %d bytes, expected 64", i, len(acc.Ciphertext))
		}
		key := tallyRecordKey{string(acc.RoundId), acc.ProposalId, acc.VoteDecision}
		if _, dup := seenTallyAccumulators[key]; dup {
			return fmt.Errorf("tally_accumulators[%d]: duplicate round/proposal/decision tuple", i)
		}
		seenTallyAccumulators[key] = struct{}{}
	}

	// Validate share counts.
	seenShareCounts := make(map[tallyRecordKey]struct{}, len(gs.ShareCounts))
	for i, sc := range gs.ShareCounts {
		if len(sc.RoundId) != RoundIDLen {
			return fmt.Errorf("share_counts[%d].round_id is %d bytes, expected %d", i, len(sc.RoundId), RoundIDLen)
		}
		key := tallyRecordKey{string(sc.RoundId), sc.ProposalId, sc.VoteDecision}
		if _, dup := seenShareCounts[key]; dup {
			return fmt.Errorf("share_counts[%d]: duplicate round/proposal/decision tuple", i)
		}
		seenShareCounts[key] = struct{}{}
	}

	// Validate partial decryptions.
	type partialDecryptionKey struct {
		tallyRecordKey
		validatorIndex uint32
	}
	seenPartialDecryptions := make(map[partialDecryptionKey]struct{}, len(gs.PartialDecryptions))
	for i, pd := range gs.PartialDecryptions {
		if len(pd.RoundId) != RoundIDLen {
			return fmt.Errorf("partial_decryptions[%d].round_id is %d bytes, expected %d", i, len(pd.RoundId), RoundIDLen)
		}
		if pd.ValidatorIndex == 0 {
			return fmt.Errorf("partial_decryptions[%d].validator_index must be >= 1", i)
		}
		if len(pd.PartialDecrypt) != 32 {
			return fmt.Errorf("partial_decryptions[%d].partial_decrypt is %d bytes, expected 32", i, len(pd.PartialDecrypt))
		}
		key := partialDecryptionKey{
			tallyRecordKey: tallyRecordKey{string(pd.RoundId), pd.ProposalId, pd.VoteDecision},
			validatorIndex: pd.ValidatorIndex,
		}
		if _, dup := seenPartialDecryptions[key]; dup {
			return fmt.Errorf("partial_decryptions[%d]: duplicate round/validator/proposal/decision tuple", i)
		}
		seenPartialDecryptions[key] = struct{}{}
	}

	// Validate endorser mappings.
	seenEndorsers := make(map[string]struct{}, len(gs.Endorsers))
	for i, endorser := range gs.Endorsers {
		if err := ValidateEndorserID(endorser.EndorserId); err != nil {
			return fmt.Errorf("endorsers[%d].endorser_id: %w", i, err)
		}
		if _, dup := seenEndorsers[endorser.EndorserId]; dup {
			return fmt.Errorf("endorsers[%d]: duplicate endorser_id %q", i, endorser.EndorserId)
		}
		seenEndorsers[endorser.EndorserId] = struct{}{}
		if _, err := sdk.AccAddressFromBech32(endorser.Address); err != nil {
			return fmt.Errorf("endorsers[%d].address %q is not a valid bech32 address: %w", i, endorser.Address, err)
		}
	}

	// Validate append-only endorsements.
	seenEndorsedRounds := make(map[string]struct{}, len(gs.EndorsedRounds))
	for i, endorsed := range gs.EndorsedRounds {
		if err := ValidateEndorserID(endorsed.EndorserId); err != nil {
			return fmt.Errorf("endorsed_rounds[%d].endorser_id: %w", i, err)
		}
		if len(endorsed.VoteRoundId) != RoundIDLen {
			return fmt.Errorf("endorsed_rounds[%d].vote_round_id is %d bytes, expected %d", i, len(endorsed.VoteRoundId), RoundIDLen)
		}
		key := endorsed.EndorserId + "\x00" + string(endorsed.VoteRoundId)
		if _, dup := seenEndorsedRounds[key]; dup {
			return fmt.Errorf("endorsed_rounds[%d]: duplicate endorsement for %q/%x", i, endorsed.EndorserId, endorsed.VoteRoundId)
		}
		seenEndorsedRounds[key] = struct{}{}
	}

	seenCoordinatorActions := make(map[uint64]struct{}, len(gs.CoordinatorActions))
	for i, action := range gs.CoordinatorActions {
		if action == nil {
			return fmt.Errorf("coordinator_actions[%d] cannot be nil", i)
		}
		if action.ActionId == 0 {
			return fmt.Errorf("coordinator_actions[%d].action_id cannot be zero", i)
		}
		if action.ActionId == math.MaxUint64 {
			return fmt.Errorf("coordinator_actions[%d].action_id cannot be %d", i, uint64(math.MaxUint64))
		}
		if _, dup := seenCoordinatorActions[action.ActionId]; dup {
			return fmt.Errorf("coordinator_actions[%d]: duplicate action_id %d", i, action.ActionId)
		}
		seenCoordinatorActions[action.ActionId] = struct{}{}
		if action.Payload == nil || action.Payload.TypeUrl == "" || len(action.Payload.Value) == 0 {
			return fmt.Errorf("coordinator_actions[%d].payload cannot be empty", i)
		}
		if _, err := sdk.AccAddressFromBech32(action.Proposer); err != nil {
			return fmt.Errorf("coordinator_actions[%d].proposer %q is not a valid bech32 address: %w", i, action.Proposer, err)
		}
		switch action.Status {
		case CoordinatorActionStatus_COORDINATOR_ACTION_STATUS_PENDING,
			CoordinatorActionStatus_COORDINATOR_ACTION_STATUS_EXECUTED,
			CoordinatorActionStatus_COORDINATOR_ACTION_STATUS_EXPIRED:
		default:
			return fmt.Errorf("coordinator_actions[%d].status is invalid: %s", i, action.Status.String())
		}
		seenApprovals := make(map[string]struct{}, len(action.Approvals))
		for j, approval := range action.Approvals {
			acc, err := sdk.AccAddressFromBech32(approval)
			if err != nil {
				return fmt.Errorf("coordinator_actions[%d].approvals[%d] %q is not a valid bech32 address: %w", i, j, approval, err)
			}
			canonical := acc.String()
			if _, dup := seenApprovals[canonical]; dup {
				return fmt.Errorf("coordinator_actions[%d].approvals[%d]: duplicate approval %s", i, j, canonical)
			}
			seenApprovals[canonical] = struct{}{}
		}
	}
	if gs.NextCoordinatorActionId != 0 {
		for actionID := range seenCoordinatorActions {
			if actionID >= gs.NextCoordinatorActionId {
				return fmt.Errorf("next_coordinator_action_id %d must be greater than imported action_id %d", gs.NextCoordinatorActionId, actionID)
			}
		}
	}

	return nil
}

// validateGenesisRoundTrees checks the replay data that InitGenesis writes
// directly into the commitment-tree namespace.
func validateGenesisRoundTrees(trees []*GenesisRoundTree, rounds map[string]struct{}) error {
	seenTrees := make(map[string]struct{}, len(trees))
	for i, tree := range trees {
		if err := ValidateRoundID(tree.VoteRoundId); err != nil {
			return fmt.Errorf("round_trees[%d].vote_round_id: %w", i, err)
		}
		roundKey := string(tree.VoteRoundId)
		if _, ok := rounds[roundKey]; !ok {
			return fmt.Errorf("round_trees[%d] references unknown round %x", i, tree.VoteRoundId)
		}
		if _, dup := seenTrees[roundKey]; dup {
			return fmt.Errorf("round_trees[%d]: duplicate vote_round_id %x", i, tree.VoteRoundId)
		}
		seenTrees[roundKey] = struct{}{}

		state := tree.TreeState
		if state == nil {
			return fmt.Errorf("round_trees[%d].tree_state cannot be nil", i)
		}
		maxNextIndex := uint64(MaxTreePosition) + 1
		if state.NextIndex > maxNextIndex {
			return fmt.Errorf("round_trees[%d].tree_state.next_index %d exceeds tree capacity %d", i, state.NextIndex, maxNextIndex)
		}
		if state.NextIndexAtRoot > state.NextIndex {
			return fmt.Errorf("round_trees[%d].tree_state.next_index_at_root %d exceeds next_index %d", i, state.NextIndexAtRoot, state.NextIndex)
		}
		if len(state.Root) != 0 && len(state.Root) != 32 {
			return fmt.Errorf("round_trees[%d].tree_state.root is %d bytes, expected 32", i, len(state.Root))
		}
		if len(state.Root) != 0 && !pallas.IsCanonicalBaseFieldElement(state.Root) {
			return fmt.Errorf("round_trees[%d].tree_state.root is not a canonical Pallas field element", i)
		}
		if state.Height == 0 {
			if state.NextIndexAtRoot != 0 {
				return fmt.Errorf("round_trees[%d].tree_state.next_index_at_root must be zero when height is zero", i)
			}
			if len(state.Root) != 0 {
				return fmt.Errorf("round_trees[%d].tree_state.root must be empty when height is zero", i)
			}
		}
		if uint64(len(tree.CommitmentLeaves)) != state.NextIndex {
			return fmt.Errorf("round_trees[%d] has %d leaves, expected %d", i, len(tree.CommitmentLeaves), state.NextIndex)
		}

		seenLeaves := make(map[uint64]struct{}, len(tree.CommitmentLeaves))
		for j, leaf := range tree.CommitmentLeaves {
			if leaf.Index >= state.NextIndex {
				return fmt.Errorf("round_trees[%d].commitment_leaves[%d].index %d is outside next_index %d", i, j, leaf.Index, state.NextIndex)
			}
			if _, dup := seenLeaves[leaf.Index]; dup {
				return fmt.Errorf("round_trees[%d].commitment_leaves[%d]: duplicate index %d", i, j, leaf.Index)
			}
			seenLeaves[leaf.Index] = struct{}{}
			if len(leaf.Value) != 32 {
				return fmt.Errorf("round_trees[%d].commitment_leaves[%d].value is %d bytes, expected 32", i, j, len(leaf.Value))
			}
			if !pallas.IsCanonicalBaseFieldElement(leaf.Value) {
				return fmt.Errorf("round_trees[%d].commitment_leaves[%d].value is not a canonical Pallas field element", i, j)
			}
		}

		roots := make(map[uint64][]byte, len(tree.CommitmentRoots))
		for j, root := range tree.CommitmentRoots {
			if _, dup := roots[root.Height]; dup {
				return fmt.Errorf("round_trees[%d].commitment_roots[%d]: duplicate height %d", i, j, root.Height)
			}
			if len(root.Root) != 32 {
				return fmt.Errorf("round_trees[%d].commitment_roots[%d].root is %d bytes, expected 32", i, j, len(root.Root))
			}
			if !pallas.IsCanonicalBaseFieldElement(root.Root) {
				return fmt.Errorf("round_trees[%d].commitment_roots[%d].root is not a canonical Pallas field element", i, j)
			}
			roots[root.Height] = root.Root
		}

		rangesByStart := make(map[uint64]uint64, len(tree.BlockLeafIndices))
		seenRangeHeights := make(map[uint64]struct{}, len(tree.BlockLeafIndices))
		for j, blockRange := range tree.BlockLeafIndices {
			if _, dup := seenRangeHeights[blockRange.Height]; dup {
				return fmt.Errorf("round_trees[%d].block_leaf_indices[%d]: duplicate height %d", i, j, blockRange.Height)
			}
			seenRangeHeights[blockRange.Height] = struct{}{}
			if blockRange.Count == 0 {
				return fmt.Errorf("round_trees[%d].block_leaf_indices[%d].count cannot be zero", i, j)
			}
			end := blockRange.StartIndex + blockRange.Count
			if end < blockRange.StartIndex || end > state.NextIndex {
				return fmt.Errorf("round_trees[%d].block_leaf_indices[%d] range [%d,%d) exceeds next_index %d", i, j, blockRange.StartIndex, end, state.NextIndex)
			}
			if _, dup := rangesByStart[blockRange.StartIndex]; dup {
				return fmt.Errorf("round_trees[%d].block_leaf_indices[%d]: duplicate start_index %d", i, j, blockRange.StartIndex)
			}
			rangesByStart[blockRange.StartIndex] = blockRange.Count
			if _, ok := roots[blockRange.Height]; !ok {
				return fmt.Errorf("round_trees[%d].block_leaf_indices[%d] has no root at height %d", i, j, blockRange.Height)
			}
		}
		if len(roots) != len(seenRangeHeights) {
			return fmt.Errorf("round_trees[%d] has %d roots but %d block leaf ranges", i, len(roots), len(seenRangeHeights))
		}

		covered := uint64(0)
		coveredRanges := 0
		for covered < state.NextIndexAtRoot {
			count, ok := rangesByStart[covered]
			if !ok {
				return fmt.Errorf("round_trees[%d].block_leaf_indices do not cover rooted leaf index %d", i, covered)
			}
			covered += count
			coveredRanges++
		}
		if covered != state.NextIndexAtRoot || coveredRanges != len(rangesByStart) {
			return fmt.Errorf("round_trees[%d].block_leaf_indices are inconsistent with next_index_at_root %d", i, state.NextIndexAtRoot)
		}
		if state.Height > 0 {
			root, ok := roots[state.Height]
			if !ok {
				return fmt.Errorf("round_trees[%d].tree_state.height %d has no stored root", i, state.Height)
			}
			if !bytes.Equal(root, state.Root) {
				return fmt.Errorf("round_trees[%d].tree_state.root does not match stored root at height %d", i, state.Height)
			}
		}
	}
	return nil
}

// validateGenesisRecordPresence rejects null protobuf records before field
// validation or restoration can dereference them.
func validateGenesisRecordPresence(gs *GenesisState) error {
	for i, round := range gs.Rounds {
		if round == nil {
			return fmt.Errorf("rounds[%d] cannot be nil", i)
		}
	}
	for i, tree := range gs.RoundTrees {
		if tree == nil {
			return fmt.Errorf("round_trees[%d] cannot be nil", i)
		}
		for j, leaf := range tree.CommitmentLeaves {
			if leaf == nil {
				return fmt.Errorf("round_trees[%d].commitment_leaves[%d] cannot be nil", i, j)
			}
		}
		for j, root := range tree.CommitmentRoots {
			if root == nil {
				return fmt.Errorf("round_trees[%d].commitment_roots[%d] cannot be nil", i, j)
			}
		}
		for j, blockRange := range tree.BlockLeafIndices {
			if blockRange == nil {
				return fmt.Errorf("round_trees[%d].block_leaf_indices[%d] cannot be nil", i, j)
			}
		}
	}
	for i, entry := range gs.Nullifiers {
		if entry == nil {
			return fmt.Errorf("nullifiers[%d] cannot be nil", i)
		}
	}
	for i, result := range gs.TallyResults {
		if result == nil {
			return fmt.Errorf("tally_results[%d] cannot be nil", i)
		}
	}
	for i, key := range gs.PallasKeys {
		if key == nil {
			return fmt.Errorf("pallas_keys[%d] cannot be nil", i)
		}
	}
	for i, accumulator := range gs.TallyAccumulators {
		if accumulator == nil {
			return fmt.Errorf("tally_accumulators[%d] cannot be nil", i)
		}
	}
	for i, count := range gs.ShareCounts {
		if count == nil {
			return fmt.Errorf("share_counts[%d] cannot be nil", i)
		}
	}
	for i, partial := range gs.PartialDecryptions {
		if partial == nil {
			return fmt.Errorf("partial_decryptions[%d] cannot be nil", i)
		}
	}
	for i, endorser := range gs.Endorsers {
		if endorser == nil {
			return fmt.Errorf("endorsers[%d] cannot be nil", i)
		}
	}
	for i, endorsed := range gs.EndorsedRounds {
		if endorsed == nil {
			return fmt.Errorf("endorsed_rounds[%d] cannot be nil", i)
		}
	}
	for i, action := range gs.CoordinatorActions {
		if action == nil {
			return fmt.Errorf("coordinator_actions[%d] cannot be nil", i)
		}
	}
	return nil
}
