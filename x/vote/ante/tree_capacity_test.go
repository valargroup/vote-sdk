package ante_test

import (
	"github.com/valargroup/vote-sdk/ffi/redpallas"
	"github.com/valargroup/vote-sdk/ffi/zkp"
	svtest "github.com/valargroup/vote-sdk/testutil"
	"github.com/valargroup/vote-sdk/testutil/proxytest"
	"github.com/valargroup/vote-sdk/x/vote/ante"
	"github.com/valargroup/vote-sdk/x/vote/types"
)

// These tests check the ante's tree capacity step: once proxy delegation is
// enabled, 0x02, 0x03, 0x06 and 0x07 need room for every leaf they append in
// CheckTx, RecheckTx and FinalizeBlock, as 0x09 does, so an overfull tx
// never reaches EndBlock (docs/proxy-delegation/contracts-v1.md §5.1).
// While dormant they keep today's checks.

// capacityTx is a tx of an existing tag that appends to the tree.
type capacityTx struct {
	tag     string
	msg     types.VoteMessage
	appends uint64 // the leaves its handler appends
	// nfType and nullifier are the first nullifier the ante checks.
	nfType    types.NullifierType
	nullifier []byte
}

// capacityTxs returns a tx in proxyAnteRound of each existing tag that
// appends: 0x02, 0x03, and 0x06 and 0x07 with two casts.
func capacityTxs() []capacityTx {
	castBatch := func(anchor, seed uint64) *types.MsgCastVoteBatch {
		votes := svtest.ValidCastVoteN(proxyAnteRound, anchor, 2, seed)
		votes[1].ProposalId = 2
		return &types.MsgCastVoteBatch{Votes: votes}
	}
	delegation := svtest.ValidDelegation(proxyAnteRound, 0x61)
	cast := svtest.ValidCastVote(proxyAnteRound, proxyBatchAnteAnchor, 0x63)
	batch := castBatch(proxyBatchAnteAnchor, 0x600)
	composite := &types.MsgDelegateAndCastVoteBatch{
		Delegation: svtest.ValidDelegation(proxyAnteRound, 0x65),
		Batch:      castBatch(0, 0x700),
	}
	return []capacityTx{
		{tag: "0x02", msg: delegation, appends: 1, nfType: types.NullifierTypeGov, nullifier: delegation.GovNullifiers[0]},
		{tag: "0x03", msg: cast, appends: 2, nfType: types.NullifierTypeVoteAuthorityNote, nullifier: cast.VanNullifier},
		{tag: "0x06", msg: batch, appends: 3, nfType: types.NullifierTypeVoteAuthorityNote, nullifier: batch.Votes[0].VanNullifier},
		{tag: "0x07", msg: composite, appends: 3, nfType: types.NullifierTypeGov, nullifier: composite.Delegation.GovNullifiers[0]},
	}
}

// mockProxyOpts returns m's options with mock verifiers.
func mockProxyOpts(m proxyMode) ante.ValidateOpts {
	return proxyOpts(m, redpallas.NewMockVerifier(), zkp.NewMockVerifier())
}

// TestTreeCapacityAnteSharedBound fills a round's last two slots as a 1-DC
// 0x09 does (passing the ante, with the gate open), then checks that with the
// gate open 0x02, 0x03, 0x06 and 0x07 are each code 11 in every mode,
// writing nothing, while dormant they pass.
func (s *ValidateTestSuite) TestTreeCapacityAnteSharedBound() {
	s.setupProxyBatchRound(func(*types.VoteRound) {})
	storeSetter{s}.fillTree(2)
	batch := proxyAnteBatch(1, false)
	if types.ProxyDelegationEnabled {
		for _, m := range s.proxyModes() {
			s.Require().NoError(ante.ValidateVoteTx(m.ctx, batch, s.keeper, mockProxyOpts(m)), m.name)
		}
	}
	// The batch lands its final VAN and its DC, as its handler does.
	kv := s.keeper.OpenKVStore(s.ctx)
	for _, leaf := range [][]byte{batch.Dcs[0].VoteAuthorityNoteNew, batch.Dcs[0].ProxyDelegationCommitment} {
		_, err := s.keeper.AppendCommitment(kv, proxyAnteRound, leaf)
		s.Require().NoError(err)
	}
	tree, err := s.keeper.GetCommitmentTreeState(kv, proxyAnteRound)
	s.Require().NoError(err)
	s.Require().Equal(types.ProxyMaxTreePosition+1, tree.NextIndex)

	for _, tx := range capacityTxs() {
		s.Run(tx.tag, func() {
			before := s.proxyBatchWrites()
			for _, m := range s.proxyModes() {
				err := ante.ValidateVoteTx(m.ctx, tx.msg, s.keeper, mockProxyOpts(m))
				if types.ProxyDelegationEnabled {
					s.requireVoteCode(err, types.ErrCommitmentTreeFull)
				} else {
					s.Require().NoError(err, "dormant in %s", m.name)
				}
			}
			s.Require().Equal(before, s.proxyBatchWrites())
		})
	}
}

// TestTreeCapacityAnteExactFit checks each tag at the tree's last slots
// with the gate open: a tx whose last leaf lands at ProxyMaxTreePosition
// passes in every mode, and with one slot fewer it is code 11.
func (s *ValidateTestSuite) TestTreeCapacityAnteExactFit() {
	proxytest.SkipUnlessEnabled(s.T())
	for _, tx := range capacityTxs() {
		s.Run(tx.tag, func() {
			s.SetupTest()
			s.setupProxyBatchRound(func(*types.VoteRound) {})
			for _, m := range s.proxyModes() {
				storeSetter{s}.fillTree(tx.appends)
				s.Require().NoError(ante.ValidateVoteTx(m.ctx, tx.msg, s.keeper, mockProxyOpts(m)), m.name)
				storeSetter{s}.fillTree(tx.appends - 1)
				s.requireVoteCode(ante.ValidateVoteTx(m.ctx, tx.msg, s.keeper, mockProxyOpts(m)), types.ErrCommitmentTreeFull)
			}
		})
	}
}

// TestTreeCapacityAnteAfterNullifiers checks that the capacity step follows
// the nullifier check, as in 0x09, so a landed tx's retry on a full tree is
// still code 2.
func (s *ValidateTestSuite) TestTreeCapacityAnteAfterNullifiers() {
	proxytest.SkipUnlessEnabled(s.T())
	for _, tx := range capacityTxs() {
		s.Run(tx.tag, func() {
			s.SetupTest()
			s.setupProxyBatchRound(func(*types.VoteRound) {})
			storeSetter{s}.fillTree(0)
			s.recordNullifier(tx.nfType, proxyAnteRound, tx.nullifier)
			for _, m := range s.proxyModes() {
				s.requireVoteCode(ante.ValidateVoteTx(m.ctx, tx.msg, s.keeper, mockProxyOpts(m)), types.ErrDuplicateNullifier)
			}
		})
	}
}
