package keeper_test

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"

	"cosmossdk.io/core/store"
	errorsmod "cosmossdk.io/errors"
	"cosmossdk.io/log"
	storetypes "cosmossdk.io/store/types"

	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/cosmos/cosmos-sdk/testutil"
	sdk "github.com/cosmos/cosmos-sdk/types"

	svtest "github.com/valargroup/vote-sdk/testutil"
	"github.com/valargroup/vote-sdk/testutil/proxytest"
	"github.com/valargroup/vote-sdk/x/vote/keeper"
	"github.com/valargroup/vote-sdk/x/vote/types"
)

// These tests run the 0x09 handler (docs/proxy-delegation/contracts-v1.md
// §5.2) with the dormancy gate open, so they run only under -tags
// proxyenabled.

const (
	// proxyBatchHeight is the suite's block height.
	proxyBatchHeight = 100
	// proxyBatchAnchor is the anchor height of anchored batches.
	proxyBatchAnchor = 40
)

// proxyBatchRound is the proxy round the batches spend in.
var proxyBatchRound = bytes.Repeat([]byte{0x2B}, 32)

// ProxyBatchTestSuite runs 0x09 batches against a fresh store holding an
// ACTIVE proxy round with a root at proxyBatchAnchor.
type ProxyBatchTestSuite struct {
	suite.Suite
	ctx       sdk.Context
	keeper    *keeper.Keeper
	msgServer types.MsgServer
}

// TestProxyBatchTestSuite runs the 0x09 handler suite, which needs the gate
// open.
func TestProxyBatchTestSuite(t *testing.T) {
	proxytest.SkipUnlessEnabled(t)
	suite.Run(t, new(ProxyBatchTestSuite))
}

// SetupTest stores the proxy round on chain zvote-1 at proxyBatchHeight.
func (s *ProxyBatchTestSuite) SetupTest() {
	key := storetypes.NewKVStoreKey(types.StoreKey)
	tkey := storetypes.NewTransientStoreKey("transient_test")
	testCtx := testutil.DefaultContextWithDB(s.T(), key, tkey)
	s.ctx = testCtx.Ctx.WithChainID(proxyTestChainID).WithBlockHeight(proxyBatchHeight).WithBlockTime(time.Unix(1_000_000, 0).UTC())
	s.keeper = keeper.NewKeeper(runtime.NewKVStoreService(key), svtest.TestAuthority, log.NewNopLogger(), nil, nil)
	s.msgServer = keeper.NewMsgServerImpl(s.keeper)

	round := svtest.ActiveRoundFixture(proxyBatchRound)
	round.ProxyDelegation = &types.ProxyDelegationRoundState{Enabled: true, NextDelegateIndexAtCreation: 1}
	s.Require().NoError(s.keeper.SetVoteRound(s.kv(), round))
	s.Require().NoError(s.keeper.SetCommitmentRootAtHeight(s.kv(), proxyBatchRound, proxyBatchAnchor, svtest.FpLE(0xA0C)))
}

// kv opens the vote store at the current context.
func (s *ProxyBatchTestSuite) kv() store.KVStore {
	return s.keeper.OpenKVStore(s.ctx)
}

// proxyBatchDC returns a DC whose fields derive from seed, as canonical Fp
// encodings.
func proxyBatchDC(seed uint64) *types.ProxyDelegationAction {
	return &types.ProxyDelegationAction{
		VanNullifier:              svtest.FpLE(seed<<8 | 1),
		RVpk:                      append([]byte(nil), svtest.DummyPallasPoint...),
		VoteAuthorityNoteNew:      svtest.FpLE(seed<<8 | 2),
		ProxyDelegationCommitment: svtest.FpLE(seed<<8 | 3),
		DcSlotNullifier:           svtest.FpLE(seed<<8 | 4),
		Proof:                     []byte("mock-zkp4-proof"),
		VoteAuthSig:               bytes.Repeat([]byte{byte(seed)}, 64),
	}
}

// proxyBatch returns a batch of n DCs expiring at proxyBatchHeight + 10,
// anchored at proxyBatchAnchor or, with registration, carrying one at
// anchor 0.
func proxyBatch(n int, registration bool) *types.MsgProxyDelegationBatch {
	msg := &types.MsgProxyDelegationBatch{
		VoteRoundId:              append([]byte(nil), proxyBatchRound...),
		VoteCommTreeAnchorHeight: proxyBatchAnchor,
		ExpiryHeight:             proxyBatchHeight + 10,
	}
	for i := range n {
		msg.Dcs = append(msg.Dcs, proxyBatchDC(uint64(0x10+i)))
	}
	if registration {
		msg.VoteCommTreeAnchorHeight = 0
		msg.Registration = svtest.ValidDelegation(proxyBatchRound, 0x51)
	}
	return msg
}

// proxyBatchState is everything a 0x09 batch can write: every nullifier key,
// and the round's tree size and stored leaves in index order.
type proxyBatchState struct {
	nullifiers []string
	treeSize   uint64
	leaves     []string
}

// state reads the suite's proxyBatchState.
func (s *ProxyBatchTestSuite) state() proxyBatchState {
	var st proxyBatchState
	iter, err := s.kv().Iterator(types.NullifierPrefix, types.PrefixEndBytes(types.NullifierPrefix))
	s.Require().NoError(err)
	for ; iter.Valid(); iter.Next() {
		st.nullifiers = append(st.nullifiers, fmt.Sprintf("%x", iter.Key()))
	}
	s.Require().NoError(iter.Close())

	tree, err := s.keeper.GetCommitmentTreeState(s.kv(), proxyBatchRound)
	s.Require().NoError(err)
	st.treeSize = tree.NextIndex
	leafPrefix := append(types.RoundTreeKey(proxyBatchRound), types.CommitmentLeafPrefix...)
	iter, err = s.kv().Iterator(leafPrefix, types.PrefixEndBytes(leafPrefix))
	s.Require().NoError(err)
	for ; iter.Valid(); iter.Next() {
		st.leaves = append(st.leaves, fmt.Sprintf("%x", iter.Value()))
	}
	s.Require().NoError(iter.Close())
	return st
}

// requireSpent fails unless nf's spent state in set nfType is spent.
func (s *ProxyBatchTestSuite) requireSpent(nfType types.NullifierType, nf []byte, spent bool) {
	has, err := s.keeper.HasNullifier(s.kv(), nfType, proxyBatchRound, nf)
	s.Require().NoError(err)
	s.Require().Equal(spent, has, "nullifier %x of type %d", nf, nfType)
}

// TestProxyBatchAppendsFinalVANThenDCs checks a batch without a registration:
// it spends each DC's VAN and slot nullifiers, appends the final successor
// VAN and then each DC in message order after the existing leaves, never an
// intermediate VAN, answers batch_digest and emits proxy_delegation_batch
// with contracts-v1 §7's attributes in order.
func (s *ProxyBatchTestSuite) TestProxyBatchAppendsFinalVANThenDCs() {
	for _, leaf := range []uint64{0xE1, 0xE2} {
		_, err := s.keeper.AppendCommitment(s.kv(), proxyBatchRound, svtest.FpLE(leaf))
		s.Require().NoError(err)
	}
	msg := proxyBatch(3, false)

	resp, err := s.msgServer.ProxyDelegationBatch(s.ctx, msg)
	s.Require().NoError(err)
	digest, err := types.ComputeProxyDelegationBatchDigest(proxyTestChainID, msg.SighashFields())
	s.Require().NoError(err)
	s.Require().Equal(digest, resp.BatchDigest)

	s.Require().Equal([]string{
		fmt.Sprintf("%x", svtest.FpLE(0xE1)),
		fmt.Sprintf("%x", svtest.FpLE(0xE2)),
		fmt.Sprintf("%x", msg.Dcs[2].VoteAuthorityNoteNew),
		fmt.Sprintf("%x", msg.Dcs[0].ProxyDelegationCommitment),
		fmt.Sprintf("%x", msg.Dcs[1].ProxyDelegationCommitment),
		fmt.Sprintf("%x", msg.Dcs[2].ProxyDelegationCommitment),
	}, s.state().leaves)
	for _, dc := range msg.Dcs {
		s.requireSpent(types.NullifierTypeVoteAuthorityNote, dc.VanNullifier, true)
		s.requireSpent(types.NullifierTypeProxySlot, dc.DcSlotNullifier, true)
		// Each set is its own: a VAN nullifier is not a slot nullifier.
		s.requireSpent(types.NullifierTypeProxySlot, dc.VanNullifier, false)
	}
	s.Require().Len(s.state().nullifiers, 6, "no gov nullifier without a registration")

	var event sdk.Event
	for _, e := range s.ctx.EventManager().Events() {
		if e.Type == types.EventTypeProxyDelegationBatch {
			s.Require().Empty(event.Type, "one event per batch")
			event = e
		}
	}
	var keys, values []string
	for _, attribute := range event.Attributes {
		keys = append(keys, attribute.Key)
		values = append(values, attribute.Value)
	}
	s.Require().Equal([]string{
		"vote_round_id", "batch_digest", "has_registration", "nullifier_count",
		"final_van_leaf_index", "dc_leaf_indices", "van_nullifiers", "dc_count",
	}, keys)
	s.Require().Equal([]string{
		fmt.Sprintf("%x", proxyBatchRound),
		fmt.Sprintf("%x", digest),
		"false",
		"0",
		"2",
		"3,4,5",
		strings.Join([]string{
			fmt.Sprintf("%x", msg.Dcs[0].VanNullifier),
			fmt.Sprintf("%x", msg.Dcs[1].VanNullifier),
			fmt.Sprintf("%x", msg.Dcs[2].VanNullifier),
		}, ","),
		"3",
	}, values)
}

// TestProxyBatchWithRegistration checks that a registration's gov
// nullifiers are spent, its van_cmx never becomes a leaf, and the event
// counts them as 0x07 does.
func (s *ProxyBatchTestSuite) TestProxyBatchWithRegistration() {
	msg := proxyBatch(2, true)
	resp, err := s.msgServer.ProxyDelegationBatch(s.ctx, msg)
	s.Require().NoError(err)

	for _, nf := range msg.Registration.GovNullifiers {
		s.requireSpent(types.NullifierTypeGov, nf, true)
	}
	s.Require().Equal([]string{
		fmt.Sprintf("%x", msg.Dcs[1].VoteAuthorityNoteNew),
		fmt.Sprintf("%x", msg.Dcs[0].ProxyDelegationCommitment),
		fmt.Sprintf("%x", msg.Dcs[1].ProxyDelegationCommitment),
	}, s.state().leaves)

	attributes := eventAttributes(s.T(), s.ctx.EventManager().Events(), types.EventTypeProxyDelegationBatch)
	s.Require().Equal("true", attributes[types.AttributeKeyHasRegistration])
	s.Require().Equal("2", attributes[types.AttributeKeyNullifiers])
	s.Require().Equal("0", attributes[types.AttributeKeyFinalVANLeafIndex])
	s.Require().Equal("1,2", attributes[types.AttributeKeyDCLeafIndices])
	s.Require().Equal(fmt.Sprintf("%x", resp.BatchDigest), attributes[types.AttributeKeyBatchDigest])
}

// TestProxyBatchChecksBeforeWriting checks that the handler re-runs
// ValidateBasic and every state check, all three nullifier sets included,
// in contracts-v1 §5.1 order before it writes anything: each case fails one
// check while every later check would fail too, and reports the earlier
// check's registered code.
func (s *ProxyBatchTestSuite) TestProxyBatchChecksBeforeWriting() {
	spend := func(nfType types.NullifierType, nf []byte) {
		s.Require().NoError(s.keeper.SetNullifier(s.kv(), nfType, proxyBatchRound, nf))
	}
	pause := func() {
		s.Require().NoError(s.keeper.SetProxyDelegationParams(s.kv(), &types.ProxyDelegationParams{ProxyDcPaused: true}))
	}
	fillTree := func() {
		s.Require().NoError(s.keeper.SetCommitmentTreeState(s.kv(), proxyBatchRound, &types.CommitmentTreeState{NextIndex: types.ProxyMaxTreePosition - 2}))
	}
	for _, c := range []struct {
		name         string
		registration bool
		setup        func(msg *types.MsgProxyDelegationBatch)
		want         *errorsmod.Error
	}{
		{name: "repeated slot nullifier", setup: func(msg *types.MsgProxyDelegationBatch) {
			msg.Dcs[2].DcSlotNullifier = msg.Dcs[0].DcSlotNullifier
			msg.VoteRoundId = bytes.Repeat([]byte{0x2C}, 32)
		}, want: types.ErrInvalidField},
		{name: "unknown round", setup: func(msg *types.MsgProxyDelegationBatch) {
			msg.VoteRoundId = bytes.Repeat([]byte{0x2C}, 32)
			pause()
		}, want: types.ErrRoundNotFound},
		{name: "not a proxy round", setup: func(msg *types.MsgProxyDelegationBatch) {
			s.mutateRound(func(r *types.VoteRound) {
				r.Status = types.SessionStatus_SESSION_STATUS_TALLYING
				r.ProxyDelegation = nil
			})
			spend(types.NullifierTypeVoteAuthorityNote, msg.Dcs[0].VanNullifier)
			msg.ExpiryHeight = 1
		}, want: types.ErrProxyDelegationDisabled},
		{name: "spent gov nullifier", registration: true, setup: func(msg *types.MsgProxyDelegationBatch) {
			s.closeRound()
			spend(types.NullifierTypeGov, msg.Registration.GovNullifiers[1])
			pause()
			fillTree()
			msg.ExpiryHeight = proxyBatchHeight
		}, want: types.ErrDuplicateNullifier},
		{name: "spent VAN nullifier after close", setup: func(msg *types.MsgProxyDelegationBatch) {
			s.closeRound()
			spend(types.NullifierTypeVoteAuthorityNote, msg.Dcs[2].VanNullifier)
			pause()
			msg.ExpiryHeight = proxyBatchHeight
		}, want: types.ErrDuplicateNullifier},
		{name: "spent slot nullifier", registration: true, setup: func(msg *types.MsgProxyDelegationBatch) {
			s.mutateRound(func(r *types.VoteRound) { r.VoteEndTime = uint64(s.ctx.BlockTime().Unix()) })
			spend(types.NullifierTypeProxySlot, msg.Dcs[2].DcSlotNullifier)
			pause()
			msg.ExpiryHeight = proxyBatchHeight
		}, want: types.ErrDuplicateNullifier},
		{name: "expired after close", setup: func(msg *types.MsgProxyDelegationBatch) {
			s.closeRound()
			msg.ExpiryHeight = proxyBatchHeight
			pause()
			fillTree()
		}, want: types.ErrProxyBatchExpired},
		{name: "round not active", setup: func(*types.MsgProxyDelegationBatch) {
			s.closeRound()
			pause()
			fillTree()
		}, want: types.ErrRoundNotActive},
		{name: "paused", setup: func(*types.MsgProxyDelegationBatch) {
			pause()
			fillTree()
		}, want: types.ErrProxyDelegationPaused},
		{name: "tree full", setup: func(msg *types.MsgProxyDelegationBatch) {
			fillTree()
			msg.VoteCommTreeAnchorHeight = proxyBatchAnchor + 1
		}, want: types.ErrCommitmentTreeFull},
		{name: "no root at the anchor", setup: func(msg *types.MsgProxyDelegationBatch) {
			msg.VoteCommTreeAnchorHeight = proxyBatchAnchor + 1
		}, want: types.ErrInvalidAnchorHeight},
	} {
		s.Run(c.name, func() {
			s.SetupTest()
			msg := proxyBatch(3, c.registration)
			c.setup(msg)
			before := s.state()

			resp, err := s.msgServer.ProxyDelegationBatch(s.ctx, msg)
			s.Require().Nil(resp)
			s.Require().ErrorIs(err, c.want)
			codespace, code, _ := errorsmod.ABCIInfo(err, false)
			s.Require().Equal(types.ModuleName, codespace, "error: %v", err)
			s.Require().Equal(c.want.ABCICode(), code, "error: %v", err)
			s.Require().Equal(before, s.state())
			s.Require().Empty(s.ctx.EventManager().Events())
		})
	}
}

// mutateRound rewrites the suite's proxy round with mutate applied.
func (s *ProxyBatchTestSuite) mutateRound(mutate func(*types.VoteRound)) {
	round, err := s.keeper.GetVoteRound(s.kv(), proxyBatchRound)
	s.Require().NoError(err)
	mutate(round)
	s.Require().NoError(s.keeper.SetVoteRound(s.kv(), round))
}

// closeRound moves the suite's proxy round to TALLYING.
func (s *ProxyBatchTestSuite) closeRound() {
	s.mutateRound(func(r *types.VoteRound) { r.Status = types.SessionStatus_SESSION_STATUS_TALLYING })
}
