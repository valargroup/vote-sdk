package keeper_test

import (
	"bytes"
	"crypto/rand"
	"sort"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/valargroup/vote-sdk/crypto/elgamal"
	"github.com/valargroup/vote-sdk/testutil/proxytest"
	"github.com/valargroup/vote-sdk/x/vote/keeper"
	"github.com/valargroup/vote-sdk/x/vote/types"
)

// These tests pin that proposal 0, which holds the delegate pools, never
// reaches the tally path (docs/proxy-delegation/contracts-v1.md §5.6, §8):
// ValidateEntryBounds, completeness, partial decryption, MsgSubmitTally,
// VoteSummary and ProposalTally all leave it out or refuse it.

// addPool adds a ciphertext of value under eaPk to Pool[d] of roundID and
// returns it.
func (s *MsgServerTestSuite) addPool(roundID []byte, d uint32, eaPk *elgamal.PublicKey, value uint64) *elgamal.Ciphertext {
	ct, err := elgamal.Encrypt(eaPk, value, rand.Reader)
	s.Require().NoError(err)
	ctBytes, err := elgamal.MarshalCiphertext(ct)
	s.Require().NoError(err)
	s.Require().NoError(s.keeper.AddToTally(s.keeper.OpenKVStore(s.ctx), roundID, types.PoolProposalID, d, ctBytes))
	s.Require().NoError(s.keeper.IncrementShareCount(s.keeper.OpenKVStore(s.ctx), roundID, types.PoolProposalID, d))
	return ct
}

// TestPoolsStayOutOfTallyChecks checks the shared tally checks: proposal 0
// is never in bounds, whatever d, and the completeness checks of tallies and
// partial decryptions see only the round's proposals, so pools neither
// count as missing nor are expected.
func (s *MsgServerTestSuite) TestPoolsStayOutOfTallyChecks() {
	cs := s.setupTallyingRoundWithCrypto(msgPdRoundID, 2, validatorSet(3))
	s.addPool(msgPdRoundID, 1, cs.eaPk, 5)
	s.addPool(msgPdRoundID, 7, cs.eaPk, 9)
	kv := s.keeper.OpenKVStore(s.ctx)
	round, err := s.keeper.GetVoteRound(kv, msgPdRoundID)
	s.Require().NoError(err)

	for _, d := range []uint32{0, 1, 7, types.MaxVoteOptions, 1 << 30} {
		s.Require().ErrorIs(keeper.ValidateEntryBounds(round, types.PoolProposalID, d), types.ErrInvalidProposalID, "d = %d", d)
	}

	accumulators, err := s.keeper.CollectNonEmptyAccumulators(kv, round)
	s.Require().NoError(err)
	var keys [][2]uint32
	for key := range accumulators {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		return keys[i][0] < keys[j][0] || keys[i][0] == keys[j][0] && keys[i][1] < keys[j][1]
	})
	s.Require().Equal([][2]uint32{{1, 0}, {1, 1}, {2, 0}, {2, 1}}, keys)

	var tallyEntries []*types.TallyEntry
	var partialEntries []*types.PartialDecryptionEntry
	for _, key := range keys {
		tallyEntries = append(tallyEntries, &types.TallyEntry{ProposalId: key[0], VoteDecision: key[1]})
		partialEntries = append(partialEntries, validEntry(key[0], key[1]))
	}
	s.Require().NoError(s.keeper.ValidateTallyCompleteness(kv, round, tallyEntries))
	s.Require().NoError(s.keeper.ValidatePartialDecryptionCompleteness(kv, round, partialEntries))
}

// TestSubmitPartialDecryptionExcludesPools checks that a partial decryption
// covering every proposal's accumulator is accepted while pools exist, and
// that one also carrying a pool entry, with a valid DLEQ proof, is refused
// and stores nothing.
func (s *MsgServerTestSuite) TestSubmitPartialDecryptionExcludesPools() {
	validators := validatorSet(3)
	cs := s.setupTallyingRoundWithCrypto(msgPdRoundID, 2, validators)
	pool := s.addPool(msgPdRoundID, 7, cs.eaPk, 9)
	s.setBlockProposer(validators[0].ValidatorAddress)
	share := cs.shares[0].Value
	proof, err := elgamal.GeneratePartialDecryptDLEQ(share, pool.C1)
	s.Require().NoError(err)
	poolEntry := &types.PartialDecryptionEntry{
		ProposalId:     types.PoolProposalID,
		VoteDecision:   7,
		PartialDecrypt: pool.C1.Mul(share).ToAffineCompressed(),
		DleqProof:      proof,
	}
	msg := func(withPool bool) *types.MsgSubmitPartialDecryption {
		entries := cs.buildValidEntries(s)
		if withPool {
			entries = append(entries, poolEntry)
		}
		return &types.MsgSubmitPartialDecryption{
			VoteRoundId:    msgPdRoundID,
			Creator:        validators[0].ValidatorAddress,
			ValidatorIndex: 1,
			Entries:        entries,
		}
	}

	_, err = s.msgServer.SubmitPartialDecryption(s.ctx, msg(true))
	s.Require().ErrorIs(err, types.ErrInvalidProposalID)
	kv := s.keeper.OpenKVStore(s.ctx)
	has, err := s.keeper.HasPartialDecryptionsFromValidator(kv, msgPdRoundID, 1)
	s.Require().NoError(err)
	s.Require().False(has)

	_, err = s.msgServer.SubmitPartialDecryption(s.ctx, msg(false))
	s.Require().NoError(err)
	stored, err := s.keeper.GetPartialDecryption(kv, msgPdRoundID, 1, types.PoolProposalID, 7)
	s.Require().NoError(err)
	s.Require().Nil(stored)
}

// TestSubmitTallyExcludesPools checks that a tally covering the proposals'
// accumulators finalizes while a pool exists, with no result for proposal
// 0, and that a tally carrying a pool entry is refused even with that pool's
// partial decryptions stored and its value right.
func (s *MsgServerTestSuite) TestSubmitTallyExcludesPools() {
	roundID := bytes.Repeat([]byte{0x7A}, 32)
	s.setBlockProposer("sv1proposer")
	s.setupThresholdTallyRound(roundID, 2, validatorSet(2))
	s.storeThresholdPartials(roundID, 2, 2, []int{1, 2}, []tallyAccumulator{
		{proposalID: 1, decision: 0, value: 10},
		{proposalID: types.PoolProposalID, decision: 7, value: 99},
	})
	submit := func(entries ...*types.TallyEntry) error {
		cacheCtx, write := s.ctx.CacheContext()
		_, err := s.msgServer.SubmitTally(cacheCtx, &types.MsgSubmitTally{VoteRoundId: roundID, Creator: "sv1proposer", Entries: entries})
		if err == nil {
			write()
		}
		return err
	}
	direct := &types.TallyEntry{ProposalId: 1, VoteDecision: 0, TotalValue: 10}
	pool := &types.TallyEntry{ProposalId: types.PoolProposalID, VoteDecision: 7, TotalValue: 99}

	s.Require().ErrorIs(submit(direct, pool), types.ErrInvalidProposalID)
	s.Require().ErrorIs(submit(pool, direct), types.ErrInvalidProposalID)
	kv := s.keeper.OpenKVStore(s.ctx)
	round, err := s.keeper.GetVoteRound(kv, roundID)
	s.Require().NoError(err)
	s.Require().Equal(types.SessionStatus_SESSION_STATUS_TALLYING, round.Status)

	s.Require().NoError(submit(direct))
	round, err = s.keeper.GetVoteRound(kv, roundID)
	s.Require().NoError(err)
	s.Require().Equal(types.SessionStatus_SESSION_STATUS_FINALIZED, round.Status)
	results, err := s.keeper.GetAllTallyResults(kv, roundID)
	s.Require().NoError(err)
	s.Require().Len(results, 1)
	s.Require().Equal(uint32(1), results[0].ProposalId)
}

// TestVoteSummaryExcludesPools checks that VoteSummary lists only the
// round's proposals and their share counts, never a pool's.
func (s *MsgServerTestSuite) TestVoteSummaryExcludesPools() {
	cs := s.setupTallyingRoundWithCrypto(msgPdRoundID, 2, validatorSet(2))
	s.addPool(msgPdRoundID, 1, cs.eaPk, 5)
	s.addPool(msgPdRoundID, 1, cs.eaPk, 6)
	kv := s.keeper.OpenKVStore(s.ctx)
	s.Require().NoError(s.keeper.IncrementShareCount(kv, msgPdRoundID, 2, 1))

	summary, err := s.keeper.GetVoteSummary(kv, msgPdRoundID)
	s.Require().NoError(err)
	s.Require().Len(summary.Proposals, 2)
	var total uint64
	for _, proposal := range summary.Proposals {
		s.Require().NotEqual(types.PoolProposalID, proposal.Id)
		for _, option := range proposal.Options {
			total += option.BallotCount
		}
	}
	s.Require().Equal(uint64(1), total, "only proposal 2's share")
}

// TestProposalTallyRejectsPools checks ProposalTally with the gate open
// (plan OPS-12): proposal 0 and ids above len(proposals) are InvalidArgument
// and an unknown round is NotFound, so no query reads a pool; a proposal of
// the round reads as before.
func (s *QueryServerTestSuite) TestProposalTallyRejectsPools() {
	proxytest.SkipUnlessEnabled(s.T())
	roundID := bytes.Repeat([]byte{0x7B}, 32)
	kv := s.keeper.OpenKVStore(s.ctx)
	s.Require().NoError(s.keeper.SetVoteRound(kv, &types.VoteRound{VoteRoundId: roundID, Proposals: pdProposals()}))
	vote, pool := validCiphertextBytes(s.T(), 1), validCiphertextBytes(s.T(), 2)
	s.Require().NoError(s.keeper.AddToTally(kv, roundID, 1, 0, vote))
	s.Require().NoError(s.keeper.AddToTally(kv, roundID, types.PoolProposalID, 1, pool))
	queries := s.queryServer

	resp, err := queries.ProposalTally(s.ctx, &types.QueryProposalTallyRequest{VoteRoundId: roundID, ProposalId: 1})
	s.Require().NoError(err)
	s.Require().Equal(map[uint32][]byte{0: vote}, resp.Tally)
	resp, err = queries.ProposalTally(s.ctx, &types.QueryProposalTallyRequest{VoteRoundId: roundID, ProposalId: 2})
	s.Require().NoError(err)
	s.Require().Empty(resp.Tally)

	for _, c := range []struct {
		round    []byte
		proposal uint32
		code     codes.Code
	}{
		{roundID, types.PoolProposalID, codes.InvalidArgument},
		{roundID, 3, codes.InvalidArgument},
		{roundID, types.MaxProposals + 1, codes.InvalidArgument},
		{bytes.Repeat([]byte{0x7C}, 32), 1, codes.NotFound},
		{bytes.Repeat([]byte{0x7C}, 32), types.PoolProposalID, codes.NotFound},
	} {
		resp, err := queries.ProposalTally(s.ctx, &types.QueryProposalTallyRequest{VoteRoundId: c.round, ProposalId: c.proposal})
		s.Require().Nil(resp, "proposal %d", c.proposal)
		s.Require().Equal(c.code, status.Code(err), "proposal %d: %v", c.proposal, err)
	}
}
