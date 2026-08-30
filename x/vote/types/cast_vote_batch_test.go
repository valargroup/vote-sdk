package types_test

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	svtest "github.com/valargroup/vote-sdk/testutil"
	"github.com/valargroup/vote-sdk/x/vote/types"
)

func validCastVoteBatch() *types.MsgCastVoteBatch {
	roundID := bytes.Repeat([]byte{0x42}, types.RoundIDLen)
	votes := svtest.ValidCastVoteN(roundID, 77, 2, 10)
	votes[0].ProposalId = 1
	votes[1].ProposalId = 2
	return &types.MsgCastVoteBatch{Votes: votes}
}

func TestMsgCastVoteBatchValidateBasic(t *testing.T) {
	require.Equal(t, 15, types.MaxCastVoteBatchSize)
	require.Less(t, types.MaxCastVoteBatchSize, types.MaxProposals)
	require.NoError(t, validCastVoteBatch().ValidateBasic())

	tests := []struct {
		name string
		edit func(*types.MsgCastVoteBatch)
		want string
	}{
		{"empty", func(batch *types.MsgCastVoteBatch) { batch.Votes = nil }, "votes count"},
		{"nil action", func(batch *types.MsgCastVoteBatch) { batch.Votes[1] = nil }, "votes[1] cannot be nil"},
		{"duplicate proposal", func(batch *types.MsgCastVoteBatch) { batch.Votes[1].ProposalId = 1 }, "duplicate proposal_id"},
		{"duplicate nullifier", func(batch *types.MsgCastVoteBatch) {
			batch.Votes[1].VanNullifier = append([]byte(nil), batch.Votes[0].VanNullifier...)
		}, "duplicate van_nullifier"},
		{"mixed round", func(batch *types.MsgCastVoteBatch) { batch.Votes[1].VoteRoundId[0] ^= 1 }, "different vote_round_id"},
		{"mixed anchor", func(batch *types.MsgCastVoteBatch) { batch.Votes[1].VoteCommTreeAnchorHeight++ }, "different vote_comm_tree_anchor_height"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			batch := proto.Clone(validCastVoteBatch()).(*types.MsgCastVoteBatch)
			test.edit(batch)
			require.ErrorContains(t, batch.ValidateBasic(), test.want)
		})
	}

	tooMany := validCastVoteBatch()
	template := tooMany.Votes[0]
	tooMany.Votes = make([]*types.MsgCastVote, types.MaxCastVoteBatchSize+1)
	for i := range tooMany.Votes {
		tooMany.Votes[i] = proto.Clone(template).(*types.MsgCastVote)
	}
	require.ErrorContains(t, tooMany.ValidateBasic(), "votes count")
}

func TestMsgCastVoteBatchVoteMessageFields(t *testing.T) {
	batch := validCastVoteBatch()
	require.Equal(t, batch.Votes[0].VoteRoundId, batch.GetVoteRoundId())
	require.Equal(t, [][]byte{batch.Votes[0].VanNullifier, batch.Votes[1].VanNullifier}, batch.GetNullifiers())
	require.Equal(t, types.NullifierTypeVoteAuthorityNote, batch.GetNullifierType())
	require.False(t, batch.AcceptsTallyingRound())
}
