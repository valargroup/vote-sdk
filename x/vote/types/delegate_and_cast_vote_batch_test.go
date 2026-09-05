package types_test

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	svtest "github.com/valargroup/vote-sdk/testutil"
	"github.com/valargroup/vote-sdk/x/vote/types"
)

func validDelegateAndCast() *types.MsgDelegateAndCastVoteBatch {
	roundID := bytes.Repeat([]byte{0x51}, types.RoundIDLen)
	votes := svtest.ValidCastVoteN(roundID, 0, 2, 90)
	votes[0].ProposalId = 1
	votes[1].ProposalId = 2
	return &types.MsgDelegateAndCastVoteBatch{
		Delegation: svtest.ValidDelegation(roundID, 0x31),
		Batch:      &types.MsgCastVoteBatch{Votes: votes},
	}
}

func TestMsgDelegateAndCastVoteBatchValidateBasic(t *testing.T) {
	msg := validDelegateAndCast()
	require.NoError(t, msg.ValidateBasic())
	// Synthetic anchors remain illegal outside the composite envelope.
	require.ErrorContains(t, msg.Batch.ValidateBasic(), "anchor_height cannot be zero")

	tests := []struct {
		name string
		edit func(*types.MsgDelegateAndCastVoteBatch)
		want string
	}{
		{"missing delegation", func(m *types.MsgDelegateAndCastVoteBatch) { m.Delegation = nil }, "delegation cannot be nil"},
		{"missing batch", func(m *types.MsgDelegateAndCastVoteBatch) { m.Batch = nil }, "batch cannot be nil"},
		{"mixed round", func(m *types.MsgDelegateAndCastVoteBatch) { m.Batch.Votes[0].VoteRoundId[0] ^= 1 }, "different vote_round_id"},
		{"real anchor forbidden", func(m *types.MsgDelegateAndCastVoteBatch) {
			for _, v := range m.Batch.Votes {
				v.VoteCommTreeAnchorHeight = 7
			}
		}, "synthetic anchor height zero"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := proto.Clone(msg).(*types.MsgDelegateAndCastVoteBatch)
			test.edit(candidate)
			require.ErrorContains(t, candidate.ValidateBasic(), test.want)
		})
	}
}
