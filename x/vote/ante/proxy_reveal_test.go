package ante_test

import (
	"bytes"
	"errors"

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
// whatever the delegate's status or the DC pause, before the share nullifier
// and ZKP3, which are today's; while dormant a pool share fails
// ValidateBasic as today.

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

// setAnteEntryStatus stores entry index with status.
func (s *ValidateTestSuite) setAnteEntryStatus(index, status uint32) {
	kv := s.keeper.OpenKVStore(s.ctx)
	entry, err := s.keeper.GetProxyDelegateEntry(kv, index)
	s.Require().NoError(err)
	entry.Status = status
	s.Require().NoError(s.keeper.SetProxyDelegateEntry(kv, entry))
}

// poolShareSpy records the ZKP3 checks of the ante and fails them all when
// fail is set; every other proof kind is unexpected.
type poolShareSpy struct {
	fail   bool
	shares []zkp.VoteShareInputs
}

// VerifyDelegation is never reached by a reveal.
func (*poolShareSpy) VerifyDelegation([]byte, zkp.DelegationInputs) error {
	return errors.New("unexpected ZKP #1")
}

// VerifyVoteCommitment is never reached by a reveal.
func (*poolShareSpy) VerifyVoteCommitment([]byte, zkp.VoteCommitmentInputs) error {
	return errors.New("unexpected ZKP #2")
}

// VerifyVoteShare records a ZKP3 check.
func (v *poolShareSpy) VerifyVoteShare(_ []byte, inputs zkp.VoteShareInputs) error {
	v.shares = append(v.shares, inputs)
	if v.fail {
		return errors.New("mock ZKP #3 failure")
	}
	return nil
}

// VerifyProxyDelegation is never reached by a reveal.
func (*poolShareSpy) VerifyProxyDelegation([]byte, zkp.ProxyDelegationInputs) error {
	return errors.New("unexpected ZKP #4")
}

// poolOpts returns m's options with a ZKP3 spy.
func poolOpts(m proxyMode, spy *poolShareSpy) ante.ValidateOpts {
	opts := proxyOpts(m, nil, spy)
	if m.recheck {
		opts.ZKPVerifier = errZKPVerifier{}
	}
	return opts
}

// TestPoolShareAnteAccepts checks that a pool share passes CheckTx, RecheckTx
// and FinalizeBlock without writing whatever its delegate's status or the DC
// pause: a suspended delegate, a retired one, paused DC batches, and an index
// registered after the round was created. Outside RecheckTx, ZKP3 gets proposal 0, d and the root
// at the anchor.
func (s *ValidateTestSuite) TestPoolShareAnteAccepts() {
	proxytest.SkipUnlessEnabled(s.T())
	for _, c := range []struct {
		name  string
		d     uint32
		setup func()
	}{
		{name: "registered delegate", d: 1},
		{name: "suspended delegate", d: 1, setup: func() { s.setAnteEntryStatus(1, types.DelegateStatusSuspended) }},
		{name: "retired delegate", d: 2, setup: func() { s.setAnteEntryStatus(2, types.DelegateStatusRetired) }},
		{name: "paused DC batches", d: 2, setup: func() {
			kv := s.keeper.OpenKVStore(s.ctx)
			params, err := s.keeper.GetProxyDelegationParams(kv)
			s.Require().NoError(err)
			params.ProxyDcPaused = true
			s.Require().NoError(s.keeper.SetProxyDelegationParams(kv, params))
		}},
		{name: "registered after round creation", d: 3, setup: func() {
			kv := s.keeper.OpenKVStore(s.ctx)
			s.Require().NoError(s.keeper.SetProxyDelegateEntry(kv, &types.ProxyDelegateEntry{
				Index: 3, Dk: proxytest.Pub(proxytest.Key("carol/1")), Provider: types.DelegateProviderX, Handle: "carol",
			}))
			s.Require().NoError(s.keeper.SetProxyNextDelegateIndex(kv, 4))
		}},
	} {
		s.Run(c.name, func() {
			s.SetupTest()
			s.setupPoolRevealRound(func(*types.VoteRound) {})
			if c.setup != nil {
				c.setup()
			}
			before := s.poolShareWrites()
			for _, m := range s.proxyModes() {
				spy := &poolShareSpy{}
				msg := anteRevealPoolShare(c.d, 0x41)
				s.Require().NoError(ante.ValidateVoteTx(m.ctx, msg, s.keeper, poolOpts(m, spy)), m.name)
				if m.recheck {
					s.Require().Empty(spy.shares)
					continue
				}
				s.Require().Equal([]zkp.VoteShareInputs{{
					ShareNullifier:   msg.ShareNullifier,
					EncShare:         msg.EncShare,
					ProposalId:       types.PoolProposalID,
					VoteDecision:     c.d,
					VoteRoundId:      proxyAnteRound,
					AnchorHeight:     poolRevealAnchor,
					VoteCommTreeRoot: poolRevealRoot,
				}}, spy.shares, m.name)
			}
			s.Require().Equal(before, s.poolShareWrites())
		})
	}
}

// TestPoolShareAnteCodes checks the pool share's ante checks in order, each
// case failing one check while every later check would fail too: the
// stateless d >= 1 (9), the round (3, 4), the proxy flag (69), the delegate
// index at reveal time (53, also while the round's creation-time index was
// higher), the share nullifier (2), then ZKP3 and its root (5), which
// RecheckTx skips. No check writes.
func (s *ValidateTestSuite) TestPoolShareAnteCodes() {
	proxytest.SkipUnlessEnabled(s.T())
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
		failZKP bool
		want    *errorsmod.Error
		// recheckPasses is set when the failing check is one RecheckTx skips.
		recheckPasses bool
	}{
		{name: "delegate 0", msg: anteRevealPoolShare(0, 0x51), setup: spent(0x51), want: types.ErrInvalidField},
		{name: "unknown round", round: func(r *types.VoteRound) { r.VoteRoundId = bytes.Repeat([]byte{0xEE}, 32) }, msg: anteRevealPoolShare(9, 0x52), want: types.ErrRoundNotFound},
		{name: "ended round", round: func(r *types.VoteRound) {
			r.VoteEndTime = proxyAnteTime
			r.ProxyDelegation = nil
		}, msg: anteRevealPoolShare(9, 0x53), want: types.ErrRoundNotActive},
		{name: "round without the proxy flag", round: func(r *types.VoteRound) { r.ProxyDelegation = nil }, msg: anteRevealPoolShare(9, 0x55), setup: spent(0x55), failZKP: true, want: types.ErrProxyDelegationDisabled},
		{name: "delegate at next_index", msg: anteRevealPoolShare(3, 0x57), setup: spent(0x57), failZKP: true, want: types.ErrDelegateNotFound},
		{name: "index above next_index but below the creation-time one", round: func(r *types.VoteRound) {
			r.ProxyDelegation.NextDelegateIndexAtCreation = 9
		}, msg: anteRevealPoolShare(5, 0x59), want: types.ErrDelegateNotFound},
		{name: "spent share nullifier", msg: anteRevealPoolShare(2, 0x5A), setup: spent(0x5A), failZKP: true, want: types.ErrDuplicateNullifier},
		{name: "invalid ZKP3", msg: anteRevealPoolShare(2, 0x5B), failZKP: true, want: types.ErrInvalidProof, recheckPasses: true},
		{name: "no root at the anchor", msg: func() *types.MsgRevealShare {
			msg := anteRevealPoolShare(2, 0x5C)
			msg.VoteCommTreeAnchorHeight = poolRevealAnchor + 1
			return msg
		}(), want: types.ErrInvalidProof, recheckPasses: true},
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
				spy := &poolShareSpy{fail: c.failZKP}
				err := ante.ValidateVoteTx(m.ctx, c.msg, s.keeper, poolOpts(m, spy))
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

// TestPoolShareAnteLandedRetry checks that once a pool share has landed its
// retry is answered code 2 in every mode, even after its delegate is retired
// and DC batches are paused, because nothing the pool checks can change once
// it holds.
func (s *ValidateTestSuite) TestPoolShareAnteLandedRetry() {
	proxytest.SkipUnlessEnabled(s.T())
	s.setupPoolRevealRound(func(*types.VoteRound) {})
	msg := anteRevealPoolShare(2, 0x61)
	s.recordNullifier(types.NullifierTypeShare, proxyAnteRound, msg.ShareNullifier)
	s.setAnteEntryStatus(2, types.DelegateStatusRetired)
	kv := s.keeper.OpenKVStore(s.ctx)
	s.Require().NoError(s.keeper.SetProxyDelegationParams(kv, &types.ProxyDelegationParams{ProxyDcPaused: true}))
	for _, m := range s.proxyModes() {
		s.requireVoteCode(ante.ValidateVoteTx(m.ctx, msg, s.keeper, poolOpts(m, &poolShareSpy{})), types.ErrDuplicateNullifier)
	}
}

// TestPoolShareAnteDormant checks that while proxy delegation is dormant a
// pool share fails basic validation exactly as today, with code 1, whatever
// the round and registry say.
func (s *ValidateTestSuite) TestPoolShareAnteDormant() {
	proxytest.SkipUnlessDormant(s.T())
	s.setupPoolRevealRound(func(*types.VoteRound) {})
	for _, m := range s.proxyModes() {
		err := ante.ValidateVoteTx(m.ctx, anteRevealPoolShare(1, 0x71), s.keeper, poolOpts(m, &poolShareSpy{}))
		s.Require().EqualError(err, "basic validation failed: invalid message field: proposal_id must be 1..50, got 0", m.name)
		s.Require().ErrorIs(err, types.ErrInvalidField)
		codespace, code, _ := errorsmod.ABCIInfo(err, false)
		s.Require().Equal(errorsmod.UndefinedCodespace, codespace)
		s.Require().Equal(uint32(1), code)
	}
}

// TestVoteRevealAnteWithGateOpen checks that opening the gate leaves a vote
// reveal's ante path as it was, apart from its registered codes: a valid one
// passes in a proxy round too, and a spent nullifier is code 2.
func (s *ValidateTestSuite) TestVoteRevealAnteWithGateOpen() {
	proxytest.SkipUnlessEnabled(s.T())
	s.setupPoolRevealRound(func(*types.VoteRound) {})
	msg := svtest.ValidRevealShare(proxyAnteRound, poolRevealAnchor, 0x81)
	for _, m := range s.proxyModes() {
		spy := &poolShareSpy{}
		s.Require().NoError(ante.ValidateVoteTx(m.ctx, msg, s.keeper, poolOpts(m, spy)), m.name)
		if !m.recheck {
			s.Require().Len(spy.shares, 1)
			s.Require().Equal(msg.ProposalId, spy.shares[0].ProposalId)
		}
	}
	s.recordNullifier(types.NullifierTypeShare, proxyAnteRound, msg.ShareNullifier)
	s.requireVoteCode(ante.ValidateVoteTx(s.proxyCtx(), msg, s.keeper, mockOpts()), types.ErrDuplicateNullifier)
}
