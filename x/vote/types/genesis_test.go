package types_test

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/valargroup/shielded-vote/x/vote/types"
)

func validGenesis() *types.GenesisState {
	roundID := bytes.Repeat([]byte{0xAA}, 32)
	return &types.GenesisState{
		TreeState: &types.CommitmentTreeState{
			NextIndex: 2,
		},
		CommitmentLeaves: []*types.CommitmentLeaf{
			{Index: 0, Value: bytes.Repeat([]byte{0x01}, 32)},
			{Index: 1, Value: bytes.Repeat([]byte{0x02}, 32)},
		},
		Rounds: []*types.VoteRound{
			{
				VoteRoundId: roundID,
				Status:      types.SessionStatus_SESSION_STATUS_ACTIVE,
			},
		},
		Nullifiers: []*types.NullifierEntry{
			{NullifierType: 0, RoundId: roundID, Nullifier: bytes.Repeat([]byte{0xB1}, 32)},
			{NullifierType: 1, RoundId: roundID, Nullifier: bytes.Repeat([]byte{0xB2}, 32)},
			{NullifierType: 2, RoundId: roundID, Nullifier: bytes.Repeat([]byte{0xB3}, 32)},
		},
		VoteManager: "sv15fjfr6rrs60vu4st6arrd94w5j6z7f6k0mfzpl",
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
		CommitmentRoots: []*types.GenesisCommitmentRoot{
			{Height: 10, Root: bytes.Repeat([]byte{0xEE}, 32)},
		},
	}
}

func TestValidateGenesisState_Valid(t *testing.T) {
	require.NoError(t, types.ValidateGenesisState(validGenesis()))
}

func TestValidateGenesisState_Nil(t *testing.T) {
	require.NoError(t, types.ValidateGenesisState(nil))
}

func TestValidateGenesisState_TreeStateLeafCountMismatch(t *testing.T) {
	gs := validGenesis()
	gs.TreeState.NextIndex = 99
	err := types.ValidateGenesisState(gs)
	require.Error(t, err)
	require.Contains(t, err.Error(), "tree_state.next_index is 99")
}

func TestValidateGenesisState_LeafIndexNotSequential(t *testing.T) {
	gs := validGenesis()
	gs.CommitmentLeaves[1].Index = 5
	err := types.ValidateGenesisState(gs)
	require.Error(t, err)
	require.Contains(t, err.Error(), "commitment_leaves[1].index is 5")
}

func TestValidateGenesisState_LeafValueEmpty(t *testing.T) {
	gs := validGenesis()
	gs.CommitmentLeaves[0].Value = nil
	err := types.ValidateGenesisState(gs)
	require.Error(t, err)
	require.Contains(t, err.Error(), "commitment_leaves[0].value is empty")
}

func TestValidateGenesisState_LeafValueWrongSize(t *testing.T) {
	gs := validGenesis()
	gs.CommitmentLeaves[0].Value = []byte{0x01, 0x02}
	err := types.ValidateGenesisState(gs)
	require.Error(t, err)
	require.Contains(t, err.Error(), "commitment_leaves[0].value is 2 bytes")
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
	})
	err := types.ValidateGenesisState(gs)
	require.Error(t, err)
	require.Contains(t, err.Error(), "duplicate vote_round_id")
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
	gs.VoteManager = "not-a-valid-address"
	err := types.ValidateGenesisState(gs)
	require.Error(t, err)
	require.Contains(t, err.Error(), "vote_manager")
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

func TestValidateGenesisState_CommitmentRootZeroHeight(t *testing.T) {
	gs := validGenesis()
	gs.CommitmentRoots[0].Height = 0
	err := types.ValidateGenesisState(gs)
	require.Error(t, err)
	require.Contains(t, err.Error(), "commitment_roots[0].height is zero")
}

func TestValidateGenesisState_CommitmentRootEmptyRoot(t *testing.T) {
	gs := validGenesis()
	gs.CommitmentRoots[0].Root = nil
	err := types.ValidateGenesisState(gs)
	require.Error(t, err)
	require.Contains(t, err.Error(), "commitment_roots[0].root is empty")
}

func TestValidateGenesisState_EmptyVoteManagerRejected(t *testing.T) {
	err := types.ValidateGenesisState(&types.GenesisState{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "vote_manager is required")
}

func TestValidateGenesisState_NoTreeStateWithLeavesIsValid(t *testing.T) {
	gs := &types.GenesisState{
		VoteManager: "sv15fjfr6rrs60vu4st6arrd94w5j6z7f6k0mfzpl",
		CommitmentLeaves: []*types.CommitmentLeaf{
			{Index: 0, Value: bytes.Repeat([]byte{0x01}, 32)},
		},
	}
	require.NoError(t, types.ValidateGenesisState(gs))
}
