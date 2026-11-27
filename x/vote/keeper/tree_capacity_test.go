package keeper_test

import (
	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"

	svtest "github.com/valargroup/vote-sdk/testutil"
	"github.com/valargroup/vote-sdk/x/vote/keeper"
	"github.com/valargroup/vote-sdk/x/vote/types"
)

// These tests hold every append to a round's depth-24 tree to leaf index
// types.ProxyMaxTreePosition once proxy delegation is enabled
// (docs/proxy-delegation/contracts-v1.md §5.1), and pin that dormant
// handlers append as before (proxy_dormancy_test.go).

// appendingTx is a tx of a tag that appends to the tree.
type appendingTx struct {
	tag     string
	msg     types.VoteMessage
	appends uint64 // the leaves its handler appends
}

// existingAppendingTxs returns a tx in proxyBatchRound of each existing tag
// that appends, each with its own nullifiers: 0x02, 0x03, and 0x06 and 0x07
// with two casts.
func existingAppendingTxs() []appendingTx {
	castBatch := func(anchor, seed uint64) *types.MsgCastVoteBatch {
		votes := svtest.ValidCastVoteN(proxyBatchRound, anchor, 2, seed)
		votes[1].ProposalId = 2
		return &types.MsgCastVoteBatch{Votes: votes}
	}
	return []appendingTx{
		{tag: "0x02", msg: svtest.ValidDelegation(proxyBatchRound, 0x61), appends: 1},
		{tag: "0x03", msg: svtest.ValidCastVote(proxyBatchRound, proxyBatchAnchor, 0x63), appends: 2},
		{tag: "0x06", msg: castBatch(proxyBatchAnchor, 0x600), appends: 3},
		{tag: "0x07", msg: &types.MsgDelegateAndCastVoteBatch{
			Delegation: svtest.ValidDelegation(proxyBatchRound, 0x65),
			Batch:      castBatch(0, 0x700),
		}, appends: 3},
	}
}

// deliver runs msg through the handler for its tag.
func (s *ProxyBatchTestSuite) deliver(msg types.VoteMessage) error {
	h := s.msgServer
	var err error
	switch m := msg.(type) {
	case *types.MsgDelegateVote:
		_, err = h.DelegateVote(s.ctx, m)
	case *types.MsgCastVote:
		_, err = h.CastVote(s.ctx, m)
	case *types.MsgCastVoteBatch:
		_, err = h.CastVoteBatch(s.ctx, m)
	case *types.MsgDelegateAndCastVoteBatch:
		_, err = h.DelegateAndCastVoteBatch(s.ctx, m)
	case *types.MsgProxyDelegationBatch:
		_, err = h.ProxyDelegationBatch(s.ctx, m)
	default:
		s.FailNow("no handler", "%T", msg)
	}
	return err
}

// seedTree sets proxyBatchRound's tree size to next without storing leaves.
func (s *ProxyBatchTestSuite) seedTree(next uint64) {
	s.Require().NoError(s.keeper.SetCommitmentTreeState(s.kv(), proxyBatchRound, &types.CommitmentTreeState{NextIndex: next}))
}

// requireTreeFull fails unless err is ErrCommitmentTreeFull with its
// registered code 11 in codespace vote.
func (s *ProxyBatchTestSuite) requireTreeFull(err error, tag string) {
	s.Require().ErrorIs(err, types.ErrCommitmentTreeFull, tag)
	codespace, code, _ := errorsmod.ABCIInfo(err, false)
	s.Require().Equal(types.ModuleName, codespace, tag)
	s.Require().Equal(uint32(11), code, tag)
}

// TestTreeCapacitySharedAcrossTags fills a round's last two slots with a
// 1-DC 0x09, then checks that 0x02, 0x03, 0x06 and 0x07 are each refused
// with code 11 and write nothing.
func (s *ProxyBatchTestSuite) TestTreeCapacitySharedAcrossTags() {
	s.seedTree(types.ProxyMaxTreePosition - 1)
	s.Require().NoError(s.deliver(proxyBatch(1, false)))
	s.Require().Equal(types.ProxyMaxTreePosition+1, s.state().treeSize, "the final VAN and the DC fill the last two slots")

	for _, tx := range existingAppendingTxs() {
		s.ctx = s.ctx.WithEventManager(sdk.NewEventManager())
		before := s.state()
		s.requireTreeFull(s.deliver(tx.msg), tx.tag)
		s.Require().Equal(before, s.state(), tx.tag)
		s.Require().Empty(s.ctx.EventManager().Events(), tx.tag)
	}
}

// TestTreeCapacityExactFit checks each appending tag at the tree's last
// slots: with one slot too few it is refused with code 11 before any write,
// and when its last leaf lands at ProxyMaxTreePosition it succeeds, having
// appended exactly CommitmentAppendCount leaves.
func (s *ProxyBatchTestSuite) TestTreeCapacityExactFit() {
	txs := append(existingAppendingTxs(), appendingTx{tag: "0x09", msg: proxyBatch(3, false), appends: 4})
	for _, tx := range txs {
		s.Run(tx.tag, func() {
			s.SetupTest()
			s.Require().Equal(tx.appends, keeper.CommitmentAppendCount(tx.msg))

			s.seedTree(types.ProxyMaxTreePosition + 2 - tx.appends)
			before := s.state()
			s.requireTreeFull(s.deliver(tx.msg), tx.tag)
			s.Require().Equal(before, s.state())

			s.seedTree(types.ProxyMaxTreePosition + 1 - tx.appends)
			s.Require().NoError(s.deliver(tx.msg))
			after := s.state()
			s.Require().Equal(types.ProxyMaxTreePosition+1, after.treeSize)
			s.Require().Len(after.leaves, int(tx.appends))
		})
	}
}

// TestAppendCommitmentBound checks that with the gate open AppendCommitment
// fills ProxyMaxTreePosition and then returns code 11 without writing.
func (s *ProxyBatchTestSuite) TestAppendCommitmentBound() {
	s.seedTree(types.ProxyMaxTreePosition)
	index, err := s.keeper.AppendCommitment(s.kv(), proxyBatchRound, svtest.FpLE(0xE1))
	s.Require().NoError(err)
	s.Require().Equal(types.ProxyMaxTreePosition, index)

	before := s.state()
	_, err = s.keeper.AppendCommitment(s.kv(), proxyBatchRound, svtest.FpLE(0xE2))
	s.requireTreeFull(err, "append past the bound")
	s.Require().Equal(before, s.state())
}
