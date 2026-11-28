package ante_test

import (
	errorsmod "cosmossdk.io/errors"

	"github.com/valargroup/vote-sdk/ffi/zkp"
	svtest "github.com/valargroup/vote-sdk/testutil"
	"github.com/valargroup/vote-sdk/testutil/proxytest"
	"github.com/valargroup/vote-sdk/x/vote/ante"
	"github.com/valargroup/vote-sdk/x/vote/types"
)

// These tests run proposal-0 reveals, pool shares, through the ante
// (docs/proxy-delegation/contracts-v1.md §5.4): with the gate open the round
// must be a proxy round (69) and 1 <= d < next_index at reveal time (53),
// before the share nullifier and ZKP3, which are today's. The keeper suite
// covers the delegate status and DC pause, which the pool check ignores.

// poolRevealAnchor is the anchor height of the pool shares in these tests.
const poolRevealAnchor = 40

// poolRevealRoot is the tree root stored at poolRevealAnchor.
var poolRevealRoot = svtest.FpLE(0xB0C)

// setupPoolRevealRound stores setupProxyRegistry's registry, whose
// next_index is 3, and proxyAnteRound, changed by mutate, with
// poolRevealRoot at poolRevealAnchor.
func (s *ValidateTestSuite) setupPoolRevealRound(mutate func(*types.VoteRound)) {
	s.setupProxyRegistry(1, 10)
	s.setupProxyRound(mutate)
	s.Require().NoError(s.keeper.SetCommitmentRootAtHeight(s.keeper.OpenKVStore(s.ctx), proxyAnteRound, poolRevealAnchor, poolRevealRoot))
}

// anteRevealPoolShare returns a pool share for delegate d in proxyAnteRound,
// with its share nullifier derived from seed.
func anteRevealPoolShare(d uint32, seed byte) *types.MsgRevealShare {
	msg := svtest.ValidRevealShare(proxyAnteRound, poolRevealAnchor, seed)
	msg.ProposalId = types.PoolProposalID
	msg.VoteDecision = d
	return msg
}

// poolShareWrites returns every nullifier, tally and share count, to show
// the ante writes none.
func (s *ValidateTestSuite) poolShareWrites() map[string][]byte {
	return proxytest.Values(s.T(), s.keeper.OpenKVStore(s.ctx), types.NullifierPrefix, types.TallyPrefix, types.ShareCountPrefix)
}

// TestPoolShareAnteAccepts checks that a pool share passes CheckTx,
// RecheckTx and FinalizeBlock without writing, and that outside RecheckTx
// ZKP3 gets proposal 0, d and the root at the anchor.
func (s *ValidateTestSuite) TestPoolShareAnteAccepts() {
	proxytest.SkipUnlessEnabled(s.T())
	s.setupPoolRevealRound(func(*types.VoteRound) {})
	before := s.poolShareWrites()
	for _, m := range s.proxyModes() {
		_, zk, _ := newProxyVerifiers(-1)
		msg := anteRevealPoolShare(1, 0x41)
		s.Require().NoError(ante.ValidateVoteTx(m.ctx, msg, s.keeper, proxyOpts(m, nil, zk)), m.name)
		if m.recheck {
			s.Require().Empty(zk.shares)
			continue
		}
		s.Require().Equal([]zkp.VoteShareInputs{{
			ShareNullifier:   msg.ShareNullifier,
			EncShare:         msg.EncShare,
			ProposalId:       types.PoolProposalID,
			VoteDecision:     1,
			VoteRoundId:      proxyAnteRound,
			AnchorHeight:     poolRevealAnchor,
			VoteCommTreeRoot: poolRevealRoot,
		}}, zk.shares, m.name)
	}
	s.Require().Equal(before, s.poolShareWrites())
}

// TestPoolShareAnteCodes checks the pool share's ante checks in order, each
// case failing one check while every later check would fail too: the
// stateless d >= 1 (9), the proxy flag (69), the delegate index at reveal
// time (53), the share nullifier (2), then ZKP3 (5), which RecheckTx skips.
// No check writes.
func (s *ValidateTestSuite) TestPoolShareAnteCodes() {
	proxytest.SkipUnlessEnabled(s.T())
	zkp3 := []string{"zkp3"}
	spent := func(seed byte) func() {
		return func() {
			s.recordNullifier(types.NullifierTypeShare, proxyAnteRound, svtest.MakeNullifier(seed))
		}
	}
	for _, c := range []struct {
		name    string
		round   func(*types.VoteRound)
		setup   func()
		msg     *types.MsgRevealShare
		zkpFail []string
		want    *errorsmod.Error
		// recheckPasses is set when the failing check is one RecheckTx skips.
		recheckPasses bool
	}{
		{name: "delegate 0", msg: anteRevealPoolShare(0, 0x51), setup: spent(0x51), want: types.ErrInvalidField},
		{name: "round without the proxy flag", round: func(r *types.VoteRound) { r.ProxyDelegation = nil }, msg: anteRevealPoolShare(9, 0x55), setup: spent(0x55), zkpFail: zkp3, want: types.ErrProxyDelegationDisabled},
		{name: "delegate at next_index", msg: anteRevealPoolShare(3, 0x57), setup: spent(0x57), zkpFail: zkp3, want: types.ErrDelegateNotFound},
		{name: "spent share nullifier", msg: anteRevealPoolShare(2, 0x5A), setup: spent(0x5A), zkpFail: zkp3, want: types.ErrDuplicateNullifier},
		{name: "invalid ZKP3", msg: anteRevealPoolShare(2, 0x5B), zkpFail: zkp3, want: types.ErrInvalidProof, recheckPasses: true},
	} {
		s.Run(c.name, func() {
			s.SetupTest()
			s.setupPoolRevealRound(func(r *types.VoteRound) {
				if c.round != nil {
					c.round(r)
				}
			})
			if c.setup != nil {
				c.setup()
			}
			before := s.poolShareWrites()
			for _, m := range s.proxyModes() {
				_, zk, _ := newProxyVerifiers(-1, c.zkpFail...)
				err := ante.ValidateVoteTx(m.ctx, c.msg, s.keeper, proxyOpts(m, nil, zk))
				if m.recheck && c.recheckPasses {
					s.Require().NoError(err, m.name)
					continue
				}
				s.requireVoteCode(err, c.want)
			}
			s.Require().Equal(before, s.poolShareWrites())
		})
	}
}
