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
// (docs/proxy-delegation/contracts-v1.md §5.4) with the gate closed and open.

// poolShare returns a well-formed reveal of a pool share for delegate d.
func poolShare(d uint32) *types.MsgRevealShare {
	msg := svtest.ValidRevealShare(proxyTestRoundID, 10, 0x31)
	msg.ProposalId = types.PoolProposalID
	msg.VoteDecision = d
	return msg
}

// TestRevealShareValidateBasicDormant checks that while proxy delegation is
// dormant ValidateBasic rejects every proposal-0 reveal exactly as today:
// ErrInvalidField reported as code 1 in codespace undefined.
func TestRevealShareValidateBasicDormant(t *testing.T) {
	proxytest.SkipUnlessDormant(t)
	for _, d := range []uint32{0, 1, 7, 8, math.MaxUint32} {
		msg := poolShare(d)
		err := msg.ValidateBasic()
		require.ErrorIs(t, err, types.ErrInvalidField, "d = %d", d)
		require.EqualError(t, err, "invalid message field: proposal_id must be 1..50, got 0")
		codespace, code, _ := errorsmod.ABCIInfo(err, false)
		require.Equal(t, errorsmod.UndefinedCodespace, codespace)
		require.Equal(t, uint32(1), code)
	}
}

// TestRevealShareValidateBasicPoolShares checks that with the gate open a
// pool share needs vote_decision >= 1 and has no option bound, and keeps
// every other field check, each failure reporting code 9.
func TestRevealShareValidateBasicPoolShares(t *testing.T) {
	proxytest.SkipUnlessEnabled(t)
	for _, d := range []uint32{1, 7, 8, 1 << 30, math.MaxUint32} {
		require.NoError(t, poolShare(d).ValidateBasic(), "d = %d", d)
	}
	requireInvalidField(t, poolShare(0).ValidateBasic(), "delegate index, at least 1")

	for name, c := range map[string]struct {
		mutate func(*types.MsgRevealShare)
		part   string
	}{
		"short share_nullifier": {func(m *types.MsgRevealShare) { m.ShareNullifier = m.ShareNullifier[:31] }, "share_nullifier"},
		"short enc_share":       {func(m *types.MsgRevealShare) { m.EncShare = m.EncShare[:63] }, "enc_share"},
		"no proof":              {func(m *types.MsgRevealShare) { m.Proof = nil }, "proof"},
		"oversized proof":       {func(m *types.MsgRevealShare) { m.Proof = make([]byte, types.MaxProofSize+1) }, "proof"},
		"short vote_round_id":   {func(m *types.MsgRevealShare) { m.VoteRoundId = m.VoteRoundId[:31] }, "vote_round_id"},
		"anchor height 0":       {func(m *types.MsgRevealShare) { m.VoteCommTreeAnchorHeight = 0 }, "vote_comm_tree_anchor_height"},
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
		"short nullifier": {func(m *types.MsgRevealShare) { m.ShareNullifier = nil }, "invalid message field: share_nullifier must be 32 bytes, got 0"},
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
