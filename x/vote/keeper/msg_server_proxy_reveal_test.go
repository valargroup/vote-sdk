package keeper_test

import (
	"bytes"
	"encoding/hex"
	"fmt"

	errorsmod "cosmossdk.io/errors"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/valargroup/vote-sdk/crypto/elgamal"
	svtest "github.com/valargroup/vote-sdk/testutil"
	"github.com/valargroup/vote-sdk/testutil/proxytest"
	"github.com/valargroup/vote-sdk/x/vote/types"
)

// These tests cover the 0x04 handler for pool shares, proposal-0 reveals
// (docs/proxy-delegation/contracts-v1.md §5.4, §7), with the dormancy gate
// open. The ante and app tests pin today's handling of proposal 0 while
// dormant.

// poolRound is the proxy round of the pool share tests.
var poolRound = bytes.Repeat([]byte{0xD1}, 32)

// poolShareMsg returns a pool share for delegate d in poolRound with its
// share nullifier and ciphertext derived from seed.
func poolShareMsg(d uint32, seed byte) *types.MsgRevealShare {
	msg := svtest.ValidRevealShare(poolRound, 10, seed)
	msg.ProposalId = types.PoolProposalID
	msg.VoteDecision = d
	return msg
}

// setupPoolRound stores entries 1 to 3 (next_index 4) and poolRound, a proxy
// round created at proxyTestTime.
func (s *ProxyRegistryTestSuite) setupPoolRound() {
	s.seedEntries(1, 3)
	s.setRound(proxyRound(poolRound, proxyTestTime))
}

// deliverReveal runs msg through the enabled 0x04 handler as FinalizeBlock
// does: in a cache whose writes and events reach s.ctx only on success.
func (s *ProxyRegistryTestSuite) deliverReveal(msg *types.MsgRevealShare) error {
	cacheCtx, write := s.ctx.CacheContext()
	_, err := s.msgServer.RevealShare(cacheCtx, msg)
	if err == nil {
		write()
	}
	return err
}

// tallyState returns every tally, share count and share nullifier key and
// value under roundID.
func (s *ProxyRegistryTestSuite) tallyState(roundID []byte) map[string]string {
	state := map[string]string{}
	nullifiers, err := types.NullifierPrefixForRound(types.NullifierTypeShare, roundID)
	s.Require().NoError(err)
	for _, prefix := range [][]byte{
		append(append([]byte(nil), types.TallyPrefix...), roundID...),
		append(append([]byte(nil), types.ShareCountPrefix...), roundID...),
		nullifiers,
	} {
		iter, err := s.kv().Iterator(prefix, types.PrefixEndBytes(prefix))
		s.Require().NoError(err)
		for ; iter.Valid(); iter.Next() {
			state[hex.EncodeToString(iter.Key())] = hex.EncodeToString(iter.Value())
		}
		s.Require().NoError(iter.Close())
	}
	return state
}

// shareCount returns the share count at (roundID, proposalID, decision).
func (s *ProxyRegistryTestSuite) shareCount(roundID []byte, proposalID, decision uint32) uint64 {
	count, err := s.keeper.GetShareCount(s.kv(), roundID, proposalID, decision)
	s.Require().NoError(err)
	return count
}

// TestPoolShareReveal checks a pool share's effects: the nullifier is spent,
// enc_share is added into Pool[d] at TallyKey(round, 0, d), the pool's share
// count at ShareCountKey(round, 0, d) grows, and reveal_pool_share is emitted
// with §7's attributes, never reveal_share. No proposal or other pool
// changes.
func (s *ProxyRegistryTestSuite) TestPoolShareReveal() {
	s.setupPoolRound()
	first, second, other := poolShareMsg(2, 0x11), poolShareMsg(2, 0x12), poolShareMsg(3, 0x13)

	s.ctx = s.ctx.WithEventManager(sdk.NewEventManager())
	s.Require().NoError(s.deliverReveal(first))
	s.Require().Equal(map[string]string{
		types.AttributeKeyRoundID:        hex.EncodeToString(poolRound),
		types.AttributeKeyDelegateIndex:  "2",
		types.AttributeKeyShareNullifier: hex.EncodeToString(first.ShareNullifier),
	}, eventAttributes(s.T(), s.ctx.EventManager().Events(), types.EventTypeRevealPoolShare))
	for _, event := range s.ctx.EventManager().Events() {
		s.Require().NotEqual(types.EventTypeRevealShare, event.Type)
	}
	pool, err := s.keeper.GetTally(s.kv(), poolRound, types.PoolProposalID, 2)
	s.Require().NoError(err)
	s.Require().Equal(first.EncShare, pool)

	s.Require().NoError(s.deliverReveal(second))
	s.Require().NoError(s.deliverReveal(other))
	a, err := elgamal.UnmarshalCiphertext(first.EncShare)
	s.Require().NoError(err)
	b, err := elgamal.UnmarshalCiphertext(second.EncShare)
	s.Require().NoError(err)
	want, err := elgamal.MarshalCiphertext(elgamal.HomomorphicAdd(a, b))
	s.Require().NoError(err)
	pool, err = s.keeper.GetTally(s.kv(), poolRound, types.PoolProposalID, 2)
	s.Require().NoError(err)
	s.Require().Equal(want, pool)
	s.Require().Equal(uint64(2), s.shareCount(poolRound, types.PoolProposalID, 2))
	s.Require().Equal(uint64(1), s.shareCount(poolRound, types.PoolProposalID, 3))
	for _, msg := range []*types.MsgRevealShare{first, second, other} {
		spent, err := s.keeper.HasNullifier(s.kv(), types.NullifierTypeShare, poolRound, msg.ShareNullifier)
		s.Require().NoError(err)
		s.Require().True(spent)
	}

	// Only the two pools hold anything: no proposal tally and no other key.
	state := s.tallyState(poolRound)
	s.Require().Len(state, 7, "two pools, two counts, three nullifiers")
	for _, p := range []uint32{1, 2, 3} {
		tally, err := s.keeper.GetProposalTally(s.kv(), poolRound, p)
		s.Require().NoError(err)
		s.Require().Empty(tally, "proposal %d", p)
	}
}

// TestPoolShareRevealIgnoresDelegateStatus checks that pool shares are
// accepted for a suspended delegate, a retired one, paused DC batches, and
// an index registered after the round was created (§5.4).
func (s *ProxyRegistryTestSuite) TestPoolShareRevealIgnoresDelegateStatus() {
	s.openRegistry()
	alice, bob := proxytest.Key("alice/1"), proxytest.Key("bob/1")
	a, b := s.registerNew(alice, "alice"), s.registerNew(bob, "bob")
	s.setRound(proxyRound(poolRound, proxyTestTime))

	s.setStatus(a, types.DelegateStatusSuspended, types.DelegateStatusReasonImpersonation)
	s.mustDeliver(s.retireOp(b, bob))
	params, err := s.keeper.GetProxyDelegationParams(s.kv())
	s.Require().NoError(err)
	params.ProxyDcPaused = true
	s.Require().NoError(s.keeper.SetProxyDelegationParams(s.kv(), params))
	s.setBlockTime(proxyTestTime + 3600)
	c := s.registerNew(proxytest.Key("carol/1"), "carol")
	s.Require().Equal(uint32(3), c)

	for i, d := range []uint32{a, b, c} {
		s.Require().NoError(s.deliverReveal(poolShareMsg(d, byte(0x21+i))), "delegate %d", d)
		s.Require().Equal(uint64(1), s.shareCount(poolRound, types.PoolProposalID, d))
	}
}

// TestPoolShareRevealCodes checks that the handler refuses a pool share with
// its registered code and changes nothing: d = 0 fails ValidateBasic (9), an
// unknown or ended round (3, 4), a round without the proxy flag (69), d at or
// above next_index at reveal time (53) and a spent share nullifier (2).
func (s *ProxyRegistryTestSuite) TestPoolShareRevealCodes() {
	s.setupPoolRound()
	plain := proxyRound(bytes.Repeat([]byte{0xD2}, 32), proxyTestTime)
	plain.ProxyDelegation = nil
	s.setRound(plain)
	landed := poolShareMsg(1, 0x31)
	s.Require().NoError(s.deliverReveal(landed))

	for _, c := range []struct {
		name string
		msg  func() *types.MsgRevealShare
		want *errorsmod.Error
		code uint32
	}{
		{"delegate 0", func() *types.MsgRevealShare { return poolShareMsg(0, 0x32) }, types.ErrInvalidField, 9},
		{"unknown round", func() *types.MsgRevealShare {
			msg := poolShareMsg(1, 0x33)
			msg.VoteRoundId = bytes.Repeat([]byte{0xD3}, 32)
			return msg
		}, types.ErrRoundNotFound, 3},
		{"round without the proxy flag", func() *types.MsgRevealShare {
			msg := poolShareMsg(1, 0x34)
			msg.VoteRoundId = plain.VoteRoundId
			return msg
		}, types.ErrProxyDelegationDisabled, 69},
		{"delegate at next_index", func() *types.MsgRevealShare { return poolShareMsg(4, 0x35) }, types.ErrDelegateNotFound, 53},
		{"delegate far above next_index", func() *types.MsgRevealShare { return poolShareMsg(1<<31, 0x36) }, types.ErrDelegateNotFound, 53},
		{"spent share nullifier", func() *types.MsgRevealShare { return poolShareMsg(2, 0x31) }, types.ErrDuplicateNullifier, 2},
	} {
		s.Run(c.name, func() {
			before := s.tallyState(c.msg().VoteRoundId)
			s.ctx = s.ctx.WithEventManager(sdk.NewEventManager())
			proxytest.RequireCode(s.T(), s.deliverReveal(c.msg()), c.want, c.code)
			s.Require().Equal(before, s.tallyState(c.msg().VoteRoundId))
			s.Require().Empty(s.ctx.EventManager().Events())
		})
	}

	s.Run("ended round", func() {
		before := s.tallyState(poolRound)
		s.setBlockTime(proxyTestTime + 10*proxyTestDay)
		proxytest.RequireCode(s.T(), s.deliverReveal(poolShareMsg(1, 0x37)), types.ErrRoundNotActive, 4)
		s.Require().Equal(before, s.tallyState(poolRound))
	})
}

// TestCheckPoolRevealIndexBounds checks CheckPoolReveal's index rule on its
// own, d = 0 included, which ValidateBasic stops before it in the ante and
// the handler: 1 <= d < next_index, read at reveal time.
func (s *ProxyRegistryTestSuite) TestCheckPoolRevealIndexBounds() {
	s.setupPoolRound()
	for d, want := range map[uint32]error{0: types.ErrDelegateNotFound, 1: nil, 3: nil, 4: types.ErrDelegateNotFound} {
		err := s.keeper.CheckPoolReveal(s.ctx, poolShareMsg(d, 0x41))
		if want == nil {
			s.Require().NoError(err, "d = %d", d)
			continue
		}
		proxytest.RequireCode(s.T(), err, types.ErrDelegateNotFound, 53)
	}
	s.seedEntries(4, 1)
	s.Require().NoError(s.keeper.CheckPoolReveal(s.ctx, poolShareMsg(4, 0x41)), "registered after the round was created")
}

// TestVoteRevealWithGateOpen checks that opening the gate leaves a vote
// reveal on today's path: its tally and count are at (p, decision) and it
// emits reveal_share, never reveal_pool_share.
func (s *ProxyRegistryTestSuite) TestVoteRevealWithGateOpen() {
	s.setupPoolRound()
	msg := svtest.ValidRevealShare(poolRound, 10, 0x51)
	msg.ProposalId, msg.VoteDecision = 3, 2
	s.ctx = s.ctx.WithEventManager(sdk.NewEventManager())
	s.Require().NoError(s.deliverReveal(msg))
	tally, err := s.keeper.GetTally(s.kv(), poolRound, 3, 2)
	s.Require().NoError(err)
	s.Require().Equal(msg.EncShare, tally)
	s.Require().Equal(uint64(1), s.shareCount(poolRound, 3, 2))
	s.Require().Zero(s.shareCount(poolRound, types.PoolProposalID, 2))
	s.Require().Equal(fmt.Sprint(msg.ProposalId), eventAttributes(s.T(), s.ctx.EventManager().Events(), types.EventTypeRevealShare)[types.AttributeKeyProposalID])
	for _, event := range s.ctx.EventManager().Events() {
		s.Require().NotEqual(types.EventTypeRevealPoolShare, event.Type)
	}
}
