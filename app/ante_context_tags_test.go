package app

import (
	"bytes"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/valargroup/vote-sdk/x/vote/types"
)

func TestDetermineAnteFailureStage(t *testing.T) {
	roundID := bytes.Repeat([]byte{0xAA}, 32)

	tests := []struct {
		name  string
		err   error
		msg   types.VoteMessage
		stage string
	}{
		{
			name:  "invalid signature",
			err:   types.ErrInvalidSignature,
			msg:   &types.MsgCastVote{VoteRoundId: roundID},
			stage: "signature_verify",
		},
		{
			name:  "delegation proof",
			err:   types.ErrInvalidProof,
			msg:   &types.MsgDelegateVote{VoteRoundId: roundID},
			stage: "proof_delegation",
		},
		{
			name:  "cast proof",
			err:   types.ErrInvalidProof,
			msg:   &types.MsgCastVote{VoteRoundId: roundID},
			stage: "proof_vote_commitment",
		},
		{
			name:  "reveal proof",
			err:   types.ErrInvalidProof,
			msg:   &types.MsgRevealShare{VoteRoundId: roundID},
			stage: "proof_vote_share",
		},
		{
			name:  "unknown wraps fallback",
			err:   errors.New("other"),
			msg:   &types.MsgCreateVotingSession{},
			stage: "ante_validation",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.stage, determineAnteFailureStage(tc.err, tc.msg))
		})
	}
}

func TestBuildAnteFailureTags(t *testing.T) {
	roundID := bytes.Repeat([]byte{0xAB}, 32)
	msg := &types.MsgRevealShare{
		VoteRoundId: roundID,
		ProposalId:  7,
	}

	tags := buildAnteFailureTags(msg, true, "proof_vote_share")
	require.Equal(t, "ante", tags["handler"])
	require.Equal(t, "proof_vote_share", tags["stage"])
	require.Equal(t, "true", tags["is_recheck"])
	require.Equal(t, "*types.MsgRevealShare", tags["msg_type"])
	require.Equal(t, hex.EncodeToString(roundID), tags["round_id"])
	require.Equal(t, "7", tags["proposal_id"])
}

func TestBuildAnteFailureTagsWithoutVoteMsg(t *testing.T) {
	tags := buildAnteFailureTags(nil, false, "ante_validation")
	require.Equal(t, "ante", tags["handler"])
	require.Equal(t, "ante_validation", tags["stage"])
	require.Equal(t, "false", tags["is_recheck"])
	require.NotContains(t, tags, "msg_type")
	require.NotContains(t, tags, "round_id")
	require.NotContains(t, tags, "proposal_id")
}
