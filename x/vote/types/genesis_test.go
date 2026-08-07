package types_test

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/valargroup/vote-sdk/x/vote/types"
)

func canonicalPallasFieldElement(value byte) []byte {
	encoded := make([]byte, 32)
	encoded[0] = value
	return encoded
}
func validGenesis() *types.GenesisState {
	roundID := bytes.Repeat([]byte{0xAA}, 32)
	return &types.GenesisState{
		Rounds: []*types.VoteRound{
			{
				VoteRoundId: roundID,
				VoteEndTime: 2_000_000,
				Status:      types.SessionStatus_SESSION_STATUS_ACTIVE,
			},
		},
		Nullifiers: []*types.NullifierEntry{
			{NullifierType: 0, RoundId: roundID, Nullifier: bytes.Repeat([]byte{0xB1}, 32)},
			{NullifierType: 1, RoundId: roundID, Nullifier: bytes.Repeat([]byte{0xB2}, 32)},
			{NullifierType: 2, RoundId: roundID, Nullifier: bytes.Repeat([]byte{0xB3}, 32)},
		},
		VoteManagerAddresses: []string{"sv1mqts0klc9768rns9h2ykeaka5tve6ts39c2zu3"},
		TallyResults: []*types.TallyResult{
			{VoteRoundId: roundID, ProposalId: 1, VoteDecision: 0, TotalValue: 100},
		},
		PallasKeys: []*types.ValidatorPallasKey{
			{ValidatorAddress: "svvaloper1xyz", PallasPk: bytes.Repeat([]byte{0xCC}, 32)},
		},
		TallyAccumulators: []*types.GenesisTallyAccumulator{
			{RoundId: roundID, ProposalId: 1, VoteDecision: 0, Ciphertext: bytes.Repeat([]byte{0xDD}, 64)},
		},
		ShareCounts: []*types.GenesisShareCount{
			{RoundId: roundID, ProposalId: 1, VoteDecision: 0, Count: 5},
		},
	}
}

func TestValidateGenesisState_Valid(t *testing.T) {
	require.NoError(t, types.ValidateGenesisState(validGenesis()))
}

func TestValidateGenesisState_Nil(t *testing.T) {
	require.NoError(t, types.ValidateGenesisState(nil))
}

func TestValidateGenesisState_NilRecords(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*types.GenesisState)
		wantErr string
	}{
		{"round", func(gs *types.GenesisState) { gs.Rounds = []*types.VoteRound{nil} }, "rounds[0] cannot be nil"},
		{"round tree", func(gs *types.GenesisState) { gs.RoundTrees = []*types.GenesisRoundTree{nil} }, "round_trees[0] cannot be nil"},
		{"round tree leaf", func(gs *types.GenesisState) {
			gs.RoundTrees = []*types.GenesisRoundTree{{CommitmentLeaves: []*types.CommitmentLeaf{nil}}}
		}, "round_trees[0].commitment_leaves[0] cannot be nil"},
		{"round tree root", func(gs *types.GenesisState) {
			gs.RoundTrees = []*types.GenesisRoundTree{{CommitmentRoots: []*types.GenesisCommitmentRoot{nil}}}
		}, "round_trees[0].commitment_roots[0] cannot be nil"},
		{"round tree block range", func(gs *types.GenesisState) {
			gs.RoundTrees = []*types.GenesisRoundTree{{BlockLeafIndices: []*types.GenesisBlockLeafIndex{nil}}}
		}, "round_trees[0].block_leaf_indices[0] cannot be nil"},
		{"nullifier", func(gs *types.GenesisState) { gs.Nullifiers = []*types.NullifierEntry{nil} }, "nullifiers[0] cannot be nil"},
		{"tally result", func(gs *types.GenesisState) { gs.TallyResults = []*types.TallyResult{nil} }, "tally_results[0] cannot be nil"},
		{"Pallas key", func(gs *types.GenesisState) { gs.PallasKeys = []*types.ValidatorPallasKey{nil} }, "pallas_keys[0] cannot be nil"},
		{"tally accumulator", func(gs *types.GenesisState) { gs.TallyAccumulators = []*types.GenesisTallyAccumulator{nil} }, "tally_accumulators[0] cannot be nil"},
		{"share count", func(gs *types.GenesisState) { gs.ShareCounts = []*types.GenesisShareCount{nil} }, "share_counts[0] cannot be nil"},
		{"partial decryption", func(gs *types.GenesisState) { gs.PartialDecryptions = []*types.GenesisPartialDecryption{nil} }, "partial_decryptions[0] cannot be nil"},
		{"endorser", func(gs *types.GenesisState) { gs.Endorsers = []*types.Endorser{nil} }, "endorsers[0] cannot be nil"},
		{"endorsed round", func(gs *types.GenesisState) { gs.EndorsedRounds = []*types.EndorsedRound{nil} }, "endorsed_rounds[0] cannot be nil"},
		{"coordinator action", func(gs *types.GenesisState) { gs.CoordinatorActions = []*types.CoordinatorAction{nil} }, "coordinator_actions[0] cannot be nil"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gs := validGenesis()
			tc.mutate(gs)
			err := types.ValidateGenesisState(gs)
			require.EqualError(t, err, tc.wantErr)
		})
	}
}

func TestValidateGenesisState_RoundIDBadLength(t *testing.T) {
	gs := validGenesis()
	gs.Rounds[0].VoteRoundId = []byte{0x01, 0x02}
	err := types.ValidateGenesisState(gs)
	require.Error(t, err)
	require.Contains(t, err.Error(), "rounds[0].vote_round_id is 2 bytes")
}

func TestValidateGenesisState_DuplicateRoundID(t *testing.T) {
	gs := validGenesis()
	gs.Rounds = append(gs.Rounds, &types.VoteRound{
		VoteRoundId: gs.Rounds[0].VoteRoundId,
		VoteEndTime: 2_000_000,
	})
	err := types.ValidateGenesisState(gs)
	require.Error(t, err)
	require.Contains(t, err.Error(), "duplicate vote_round_id")
}

func TestValidateGenesisState_RoundTrees(t *testing.T) {
	roundID := bytes.Repeat([]byte{0xAA}, types.RoundIDLen)
	root := canonicalPallasFieldElement(3)
	nonCanonical := bytes.Repeat([]byte{0xff}, 32)
	validTree := func() *types.GenesisRoundTree {
		return &types.GenesisRoundTree{
			VoteRoundId: roundID,
			TreeState: &types.CommitmentTreeState{
				NextIndex:       2,
				NextIndexAtRoot: 2,
				Height:          10,
				Root:            root,
			},
			CommitmentLeaves: []*types.CommitmentLeaf{
				{Index: 0, Value: bytes.Repeat([]byte{0x01}, 32)},
				{Index: 1, Value: bytes.Repeat([]byte{0x02}, 32)},
			},
			CommitmentRoots: []*types.GenesisCommitmentRoot{{Height: 10, Root: root}},
			BlockLeafIndices: []*types.GenesisBlockLeafIndex{{
				Height: 10, StartIndex: 0, Count: 2,
			}},
		}
	}

	tests := []struct {
		name    string
		mutate  func(*types.GenesisState)
		wantErr string
	}{
		{"invalid round ID", func(gs *types.GenesisState) { gs.RoundTrees[0].VoteRoundId = []byte{1} }, "vote_round_id"},
		{"unknown round", func(gs *types.GenesisState) { gs.RoundTrees[0].VoteRoundId = bytes.Repeat([]byte{0xBB}, 32) }, "references unknown round"},
		{"duplicate tree", func(gs *types.GenesisState) { gs.RoundTrees = append(gs.RoundTrees, validTree()) }, "duplicate vote_round_id"},
		{"missing state", func(gs *types.GenesisState) { gs.RoundTrees[0].TreeState = nil }, "tree_state cannot be nil"},
		{"checkpoint beyond leaves", func(gs *types.GenesisState) { gs.RoundTrees[0].TreeState.NextIndexAtRoot = 3 }, "next_index_at_root 3 exceeds next_index 2"},
		{"invalid state root", func(gs *types.GenesisState) { gs.RoundTrees[0].TreeState.Root = []byte{1} }, "tree_state.root is 1 bytes"},
		{"non-canonical state root", func(gs *types.GenesisState) { gs.RoundTrees[0].TreeState.Root = nonCanonical }, "tree_state.root is not a canonical Pallas field element"},
		{"rooted index without height", func(gs *types.GenesisState) { gs.RoundTrees[0].TreeState.Height = 0 }, "next_index_at_root must be zero when height is zero"},
		{"root without height", func(gs *types.GenesisState) {
			gs.RoundTrees[0].TreeState.Height = 0
			gs.RoundTrees[0].TreeState.NextIndexAtRoot = 0
			gs.RoundTrees[0].CommitmentRoots = nil
			gs.RoundTrees[0].BlockLeafIndices = nil
		}, "root must be empty when height is zero"},
		{"missing leaf", func(gs *types.GenesisState) {
			gs.RoundTrees[0].CommitmentLeaves = gs.RoundTrees[0].CommitmentLeaves[:1]
		}, "has 1 leaves, expected 2"},
		{"duplicate leaf", func(gs *types.GenesisState) { gs.RoundTrees[0].CommitmentLeaves[1].Index = 0 }, "duplicate index 0"},
		{"leaf outside state", func(gs *types.GenesisState) { gs.RoundTrees[0].CommitmentLeaves[1].Index = 2 }, "outside next_index 2"},
		{"invalid leaf", func(gs *types.GenesisState) { gs.RoundTrees[0].CommitmentLeaves[0].Value = []byte{1} }, "value is 1 bytes"},
		{"non-canonical leaf", func(gs *types.GenesisState) { gs.RoundTrees[0].CommitmentLeaves[0].Value = nonCanonical }, "value is not a canonical Pallas field element"},
		{"duplicate root", func(gs *types.GenesisState) {
			gs.RoundTrees[0].CommitmentRoots = append(gs.RoundTrees[0].CommitmentRoots, &types.GenesisCommitmentRoot{Height: 10, Root: root})
		}, "duplicate height 10"},
		{"invalid root", func(gs *types.GenesisState) { gs.RoundTrees[0].CommitmentRoots[0].Root = []byte{1} }, "root is 1 bytes"},
		{"non-canonical root", func(gs *types.GenesisState) { gs.RoundTrees[0].CommitmentRoots[0].Root = nonCanonical }, "root is not a canonical Pallas field element"},
		{"duplicate block height", func(gs *types.GenesisState) {
			gs.RoundTrees[0].BlockLeafIndices = append(gs.RoundTrees[0].BlockLeafIndices, &types.GenesisBlockLeafIndex{Height: 10, StartIndex: 1, Count: 1})
		}, "duplicate height 10"},
		{"empty block range", func(gs *types.GenesisState) { gs.RoundTrees[0].BlockLeafIndices[0].Count = 0 }, "count cannot be zero"},
		{"block range beyond leaves", func(gs *types.GenesisState) { gs.RoundTrees[0].BlockLeafIndices[0].Count = 3 }, "exceeds next_index 2"},
		{"block range without root", func(gs *types.GenesisState) { gs.RoundTrees[0].BlockLeafIndices[0].Height = 11 }, "has no root at height 11"},
		{"root without block range", func(gs *types.GenesisState) {
			gs.RoundTrees[0].CommitmentRoots = append(gs.RoundTrees[0].CommitmentRoots, &types.GenesisCommitmentRoot{Height: 11, Root: root})
		}, "has 2 roots but 1 block leaf ranges"},
		{"block range gap", func(gs *types.GenesisState) {
			gs.RoundTrees[0].BlockLeafIndices[0].StartIndex = 1
			gs.RoundTrees[0].BlockLeafIndices[0].Count = 1
		}, "do not cover rooted leaf index 0"},
		{"block range after checkpoint", func(gs *types.GenesisState) {
			gs.RoundTrees[0].TreeState.NextIndexAtRoot = 1
			gs.RoundTrees[0].CommitmentRoots = append(gs.RoundTrees[0].CommitmentRoots, &types.GenesisCommitmentRoot{Height: 11, Root: root})
			gs.RoundTrees[0].BlockLeafIndices[0].Count = 1
			gs.RoundTrees[0].BlockLeafIndices = append(gs.RoundTrees[0].BlockLeafIndices, &types.GenesisBlockLeafIndex{Height: 11, StartIndex: 1, Count: 1})
		}, "inconsistent with next_index_at_root 1"},
		{"state root mismatch", func(gs *types.GenesisState) { gs.RoundTrees[0].TreeState.Root = canonicalPallasFieldElement(4) }, "does not match stored root"},
	}

	gs := validGenesis()
	gs.RoundTrees = []*types.GenesisRoundTree{validTree()}
	require.NoError(t, types.ValidateGenesisState(gs))

	unrootedTree := validTree()
	unrootedTree.TreeState.Root = nil
	unrootedTree.TreeState.Height = 0
	unrootedTree.TreeState.NextIndexAtRoot = 0
	unrootedTree.CommitmentRoots = nil
	unrootedTree.BlockLeafIndices = nil
	gs.RoundTrees = []*types.GenesisRoundTree{unrootedTree}
	require.NoError(t, types.ValidateGenesisState(gs))

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gs := validGenesis()
			gs.RoundTrees = []*types.GenesisRoundTree{validTree()}
			tc.mutate(gs)
			err := types.ValidateGenesisState(gs)
			require.ErrorContains(t, err, tc.wantErr)
		})
	}
}

func TestValidateGenesisState_NullifierTypeTooHigh(t *testing.T) {
	gs := validGenesis()
	gs.Nullifiers[0].NullifierType = 3
	err := types.ValidateGenesisState(gs)
	require.Error(t, err)
	require.Contains(t, err.Error(), "nullifiers[0].nullifier_type is 3")
}

func TestValidateGenesisState_NullifierRoundIDBadLength(t *testing.T) {
	gs := validGenesis()
	gs.Nullifiers[0].RoundId = []byte{0x01}
	err := types.ValidateGenesisState(gs)
	require.Error(t, err)
	require.Contains(t, err.Error(), "nullifiers[0].round_id is 1 bytes")
}

func TestValidateGenesisState_NullifierEmpty(t *testing.T) {
	gs := validGenesis()
	gs.Nullifiers[0].Nullifier = nil
	err := types.ValidateGenesisState(gs)
	require.Error(t, err)
	require.Contains(t, err.Error(), "nullifiers[0].nullifier is empty")
}

func TestValidateGenesisState_VoteManagerBadAddress(t *testing.T) {
	gs := validGenesis()
	gs.VoteManagerAddresses = []string{"not-a-valid-address"}
	err := types.ValidateGenesisState(gs)
	require.Error(t, err)
	require.Contains(t, err.Error(), "not a valid bech32 address")
}

func TestValidateGenesisState_VoteManagersEmpty(t *testing.T) {
	gs := validGenesis()
	gs.VoteManagerAddresses = nil
	err := types.ValidateGenesisState(gs)
	require.ErrorIs(t, err, types.ErrEmptyVoteManagerSet)
}

func TestValidateGenesisState_VoteManagersDuplicate(t *testing.T) {
	gs := validGenesis()
	addr := "sv1mqts0klc9768rns9h2ykeaka5tve6ts39c2zu3"
	gs.VoteManagerAddresses = []string{addr, addr}
	err := types.ValidateGenesisState(gs)
	require.ErrorIs(t, err, types.ErrDuplicateVoteManager)
}

func TestValidateAndNormalizeVoteManagerPolicy_DefaultThreshold(t *testing.T) {
	addr := "sv1mqts0klc9768rns9h2ykeaka5tve6ts39c2zu3"
	normalized, threshold, err := types.ValidateAndNormalizeVoteManagerPolicy([]string{addr}, 0)
	require.NoError(t, err)
	require.Equal(t, []string{addr}, normalized)
	require.Equal(t, uint32(1), threshold)
}

func TestValidateGenesisState_VoteManagerThresholdTooHigh(t *testing.T) {
	gs := validGenesis()
	gs.VoteManagerThreshold = 2
	err := types.ValidateGenesisState(gs)
	require.ErrorIs(t, err, types.ErrInvalidThreshold)
	require.Contains(t, err.Error(), "exceeds manager count")
}

func TestValidateGenesisState_TallyResultBadRoundID(t *testing.T) {
	gs := validGenesis()
	gs.TallyResults[0].VoteRoundId = []byte{0x01}
	err := types.ValidateGenesisState(gs)
	require.Error(t, err)
	require.Contains(t, err.Error(), "tally_results[0].vote_round_id")
}

func TestValidateGenesisState_PallasKeyEmptyAddress(t *testing.T) {
	gs := validGenesis()
	gs.PallasKeys[0].ValidatorAddress = ""
	err := types.ValidateGenesisState(gs)
	require.Error(t, err)
	require.Contains(t, err.Error(), "pallas_keys[0].validator_address is empty")
}

func TestValidateGenesisState_PallasKeyBadPK(t *testing.T) {
	gs := validGenesis()
	gs.PallasKeys[0].PallasPk = []byte{0x01}
	err := types.ValidateGenesisState(gs)
	require.Error(t, err)
	require.Contains(t, err.Error(), "pallas_keys[0].pallas_pk is 1 bytes")
}

func TestValidateGenesisState_TallyAccumulatorBadRoundID(t *testing.T) {
	gs := validGenesis()
	gs.TallyAccumulators[0].RoundId = []byte{0x01}
	err := types.ValidateGenesisState(gs)
	require.Error(t, err)
	require.Contains(t, err.Error(), "tally_accumulators[0].round_id")
}

func TestValidateGenesisState_TallyAccumulatorBadCiphertext(t *testing.T) {
	gs := validGenesis()
	gs.TallyAccumulators[0].Ciphertext = []byte{0x01}
	err := types.ValidateGenesisState(gs)
	require.Error(t, err)
	require.Contains(t, err.Error(), "tally_accumulators[0].ciphertext is 1 bytes")
}

func TestValidateGenesisState_ShareCountBadRoundID(t *testing.T) {
	gs := validGenesis()
	gs.ShareCounts[0].RoundId = []byte{0x01}
	err := types.ValidateGenesisState(gs)
	require.Error(t, err)
	require.Contains(t, err.Error(), "share_counts[0].round_id")
}
