package types_test

import (
	"math"
	"testing"

	errorsmod "cosmossdk.io/errors"
	"github.com/stretchr/testify/require"

	svtest "github.com/valargroup/vote-sdk/testutil"
	"github.com/valargroup/vote-sdk/testutil/proxytest"
	"github.com/valargroup/vote-sdk/x/vote/types"
)

// These tests cover MsgRevealShare.ValidateBasic for pool shares
// (docs/proxy-delegation/contracts-v1.md §5.4) with the gate open, and the
// gate's effect on vote reveals.

// poolShare returns a well-formed reveal of a pool share for delegate d.
func poolShare(d uint32) *types.MsgRevealShare {
	msg := svtest.ValidRevealShare(proxyTestRoundID, 10, 0x31)
	msg.ProposalId = types.PoolProposalID
	msg.VoteDecision = d
	return msg
}

// TestRevealShareValidateBasicPoolShares checks that with the gate open a
// pool share needs vote_decision >= 1 and has no option bound, and keeps the
// field checks before and after the proposal branch, each failure reporting
// code 9.
func TestRevealShareValidateBasicPoolShares(t *testing.T) {
	proxytest.SkipUnlessEnabled(t)
	for _, d := range []uint32{1, math.MaxUint32} {
		require.NoError(t, poolShare(d).ValidateBasic(), "d = %d", d)
	}
	requireInvalidField(t, poolShare(0).ValidateBasic(), "delegate index, at least 1")

	for name, c := range map[string]struct {
		mutate func(*types.MsgRevealShare)
		part   string
	}{
		"short share_nullifier": {func(m *types.MsgRevealShare) { m.ShareNullifier = m.ShareNullifier[:31] }, "share_nullifier"},
		"no proof":              {func(m *types.MsgRevealShare) { m.Proof = nil }, "proof"},
	} {
		t.Run(name, func(t *testing.T) {
			msg := poolShare(9)
			c.mutate(msg)
			requireInvalidField(t, msg.ValidateBasic(), c.part)
		})
	}
}

// TestRevealShareValidateBasicVotes checks that the gate leaves a vote
// reveal's checks and messages unchanged: while dormant each failure reports
// code 1, and with the gate open its registered code.
func TestRevealShareValidateBasicVotes(t *testing.T) {
	valid := svtest.ValidRevealShare(proxyTestRoundID, 10, 0x32)
	require.NoError(t, valid.ValidateBasic())

	for name, c := range map[string]struct {
		mutate func(*types.MsgRevealShare)
		err    string
	}{
		"vote_decision 8": {func(m *types.MsgRevealShare) { m.VoteDecision = types.MaxVoteOptions }, "invalid message field: vote_decision must be 0..7, got 8"},
		"proposal 51":     {func(m *types.MsgRevealShare) { m.ProposalId = types.MaxProposals + 1 }, "invalid message field: proposal_id must be 1..50, got 51"},
	} {
		t.Run(name, func(t *testing.T) {
			msg := svtest.ValidRevealShare(proxyTestRoundID, 10, 0x33)
			c.mutate(msg)
			err := msg.ValidateBasic()
			require.EqualError(t, err, c.err)
			if types.ProxyDelegationEnabled {
				requireInvalidField(t, err, "")
				return
			}
			require.ErrorIs(t, err, types.ErrInvalidField)
			_, code, _ := errorsmod.ABCIInfo(err, false)
			require.Equal(t, uint32(1), code)
		})
	}
}
