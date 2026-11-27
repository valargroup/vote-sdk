package ante_test

import (
	"bytes"
	"crypto/ed25519"

	errorsmod "cosmossdk.io/errors"

	"github.com/valargroup/vote-sdk/testutil/proxytest"
	"github.com/valargroup/vote-sdk/x/vote/ante"
	"github.com/valargroup/vote-sdk/x/vote/types"
)

// These tests run 0x0B ballot ops through the ante with the dormancy gate
// open (docs/proxy-delegation/contracts-v1.md §5.3): the op checks its own
// round in §5.3's order, so ValidateVoteTx's generic round check never
// answers first; every failure keeps its registered code in CheckTx,
// RecheckTx and FinalizeBlock; RecheckTx skips only the signature; and the
// ante never writes.

// proxyAnteRound is the round of the ballot ante tests.
var proxyAnteRound = bytes.Repeat([]byte{0xC1}, 32)

// setupProxyRound stores proxyAnteRound, changed by mutate: an ACTIVE proxy
// round created an hour before proxyAnteTime and ending a day after it, with
// two proposals of two options.
func (s *ValidateTestSuite) setupProxyRound(mutate func(*types.VoteRound)) {
	round := proxytest.Round(proxyAnteRound, 2, 2)
	round.CreatedAtTime = proxyAnteTime - 3600
	round.VoteEndTime = proxyAnteTime + 86_400
	round.ProxyDelegation.NextDelegateIndexAtCreation = 3
	mutate(round)
	s.Require().NoError(s.keeper.SetVoteRound(s.keeper.OpenKVStore(s.ctx), round))
}

// proxyBallotOp builds delegate d's ballot in proxyAnteRound, signed by dk.
func proxyBallotOp(d uint32, dk ed25519.PrivateKey, options ...uint32) *types.MsgDelegateOp {
	return proxytest.BallotOp(proxyAnteChainID, proxyAnteRound, d, dk, options...)
}

// proxyStateValues returns every registry and ballot key and value, to show
// the ante leaves them alone.
func (s *ValidateTestSuite) proxyStateValues() map[string][]byte {
	return proxytest.Values(s.T(), s.keeper.OpenKVStore(s.ctx), []byte{0x1A}, []byte{0x1B})
}

// TestDelegateBallotAnteAcceptsValidBallot checks that a valid ballot passes
// CheckTx, RecheckTx and the FinalizeBlock ante without writing.
func (s *ValidateTestSuite) TestDelegateBallotAnteAcceptsValidBallot() {
	proxytest.SkipUnlessEnabled(s.T())
	s.setupProxyRegistry(1, 3)
	s.setupProxyRound(func(*types.VoteRound) {})
	before := s.proxyStateValues()
	op := proxyBallotOp(1, proxytest.Key("alice/1"), 1, 0)
	s.Require().NoError(ante.ValidateVoteTx(s.proxyCtx().WithIsCheckTx(true), op, s.keeper, mockOpts()))
	s.Require().NoError(ante.ValidateVoteTx(s.proxyCtx().WithIsReCheckTx(true), op, s.keeper, recheckOpts()))
	s.Require().NoError(ante.ValidateVoteTx(s.proxyCtx(), op, s.keeper, mockOpts()))
	s.Require().Equal(before, s.proxyStateValues())
}

// TestDelegateBallotAnteCodes checks that a ballot's failure keeps its
// registered code in codespace vote through the ante (§0, §6): in CheckTx
// and FinalizeBlock always, and in RecheckTx unless it is the signature,
// which RecheckTx does not check. The keeper suite covers each check and
// its order.
func (s *ValidateTestSuite) TestDelegateBallotAnteCodes() {
	proxytest.SkipUnlessEnabled(s.T())
	alice := proxytest.Key("alice/1")
	for _, c := range []struct {
		name      string
		round     func(*types.VoteRound)
		op        func() *types.MsgDelegateOp
		want      *errorsmod.Error
		code      uint32
		signature bool // an Ed25519 failure, which RecheckTx skips
	}{
		{name: "not a proxy round", round: func(r *types.VoteRound) {
			r.ProxyDelegation = nil
		}, op: func() *types.MsgDelegateOp { return proxyBallotOp(1, alice, 0, 0) }, want: types.ErrProxyDelegationDisabled, code: 69},
		{name: "signed by another key", op: func() *types.MsgDelegateOp {
			return proxyBallotOp(1, proxytest.Key("alice/2"), 0, 0)
		}, want: types.ErrInvalidSignature, code: 6, signature: true},
	} {
		s.Run(c.name, func() {
			s.SetupTest()
			s.setupProxyRegistry(1, 3)
			round := c.round
			if round == nil {
				round = func(*types.VoteRound) {}
			}
			s.setupProxyRound(round)
			before := s.proxyStateValues()
			op := c.op()

			requireCode := func(err error) { proxytest.RequireCode(s.T(), err, c.want, c.code) }
			requireCode(ante.ValidateVoteTx(s.proxyCtx().WithIsCheckTx(true), op, s.keeper, mockOpts()))
			requireCode(ante.ValidateVoteTx(s.proxyCtx(), op, s.keeper, mockOpts()))
			recheckErr := ante.ValidateVoteTx(s.proxyCtx().WithIsReCheckTx(true), op, s.keeper, recheckOpts())
			if c.signature {
				s.Require().NoError(recheckErr)
			} else {
				requireCode(recheckErr)
			}
			s.Require().Equal(before, s.proxyStateValues())
		})
	}
}
