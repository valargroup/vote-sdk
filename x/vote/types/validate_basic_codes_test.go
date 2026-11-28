package types_test

import (
	"bytes"
	"testing"

	errorsmod "cosmossdk.io/errors"
	"github.com/stretchr/testify/require"

	svtest "github.com/valargroup/vote-sdk/testutil"
	"github.com/valargroup/vote-sdk/x/vote/types"
)

// These tests cover the codes the existing vote messages' stateless failures
// report (docs/proxy-delegation/contracts-v1.md §0): BaseApp runs
// ValidateBasic before the vote ante, so while dormant they keep today's code
// 1, and once enabled they report their registered code with the same
// message.

// gatedValidateBasicCases returns, for each existing vote tag that BaseApp
// validates statelessly (0x02, 0x03, 0x05, 0x06 and 0x07), a valid message
// and a mutation that breaks one field, nested as each message nests it.
func gatedValidateBasicCases() map[string]struct {
	valid  func() types.VoteMessage
	mutate func(types.VoteMessage)
	err    string
} {
	roundID := bytes.Repeat([]byte{0x01}, 32)
	castBatch := func(anchor uint64) *types.MsgCastVoteBatch {
		first, second := svtest.ValidCastVote(roundID, anchor, 0x21), svtest.ValidCastVote(roundID, anchor, 0x22)
		second.ProposalId = 2
		return &types.MsgCastVoteBatch{Votes: []*types.MsgCastVote{first, second}}
	}
	return map[string]struct {
		valid  func() types.VoteMessage
		mutate func(types.VoteMessage)
		err    string
	}{
		"0x02 delegate": {
			func() types.VoteMessage { return svtest.ValidDelegation(roundID, 0x11) },
			func(m types.VoteMessage) { m.(*types.MsgDelegateVote).Rk = make([]byte, 31) },
			"invalid message field: rk must be 32 bytes, got 31",
		},
		"0x03 cast": {
			func() types.VoteMessage { return svtest.ValidCastVote(roundID, 10, 0x12) },
			func(m types.VoteMessage) { m.(*types.MsgCastVote).VoteAuthSig = make([]byte, 63) },
			"invalid message field: vote_auth_sig must be 64 bytes, got 63",
		},
		"0x05 tally": {
			func() types.VoteMessage { return svtest.ValidSubmitTally(roundID, svtest.TestAccAddr(1)) },
			func(m types.VoteMessage) { m.(*types.MsgSubmitTally).Creator = "" },
			"invalid message field: creator cannot be empty",
		},
		"0x06 cast batch": {
			func() types.VoteMessage { return castBatch(10) },
			func(m types.VoteMessage) { m.(*types.MsgCastVoteBatch).Votes[1].RVpk = make([]byte, 32) },
			"votes[1]: invalid message field: r_vpk must not be the identity point (all zeros)",
		},
		"0x07 delegate and cast": {
			func() types.VoteMessage {
				return &types.MsgDelegateAndCastVoteBatch{Delegation: svtest.ValidDelegation(roundID, 0x13), Batch: castBatch(0)}
			},
			func(m types.VoteMessage) { m.(*types.MsgDelegateAndCastVoteBatch).Delegation.VanCmx = make([]byte, 31) },
			"delegation: invalid message field: van_cmx must be 32 bytes, got 31",
		},
	}
}

// TestExistingValidateBasicCodes checks that a valid message passes and each
// failure keeps its message. While dormant it is ErrInvalidField reported as
// code 1 in codespace undefined, as today; with the gate open it reports
// ErrInvalidField's code 9 in codespace vote.
func TestExistingValidateBasicCodes(t *testing.T) {
	for name, c := range gatedValidateBasicCases() {
		t.Run(name, func(t *testing.T) {
			require.NoError(t, c.valid().ValidateBasic())

			msg := c.valid()
			c.mutate(msg)
			err := msg.ValidateBasic()
			require.EqualError(t, err, c.err)
			if types.ProxyDelegationEnabled {
				requireInvalidField(t, err, "")
				return
			}
			require.ErrorIs(t, err, types.ErrInvalidField)
			codespace, code, _ := errorsmod.ABCIInfo(err, false)
			require.Equal(t, errorsmod.UndefinedCodespace, codespace)
			require.Equal(t, uint32(1), code)
		})
	}
}
