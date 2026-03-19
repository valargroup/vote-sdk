package keeper_test

import (
	"bytes"

	"github.com/valargroup/vote-sdk/x/vote/types"
)

// ---------------------------------------------------------------------------
// Tally accumulator (ElGamal ciphertext)
// ---------------------------------------------------------------------------

func (s *KeeperTestSuite) TestTally_DefaultNil() {
	s.SetupTest()
	kv := s.keeper.OpenKVStore(s.ctx)

	got, err := s.keeper.GetTally(kv, testRoundID, 1, 1)
	s.Require().NoError(err)
	s.Require().Nil(got, "uninitialized tally should be nil")
}

func (s *KeeperTestSuite) TestTally_AddAndAccumulate() {
	s.SetupTest()
	kv := s.keeper.OpenKVStore(s.ctx)

	// Create a 64-byte ciphertext stub.
	ct1 := bytes.Repeat([]byte{0x11}, 64)

	// First add: stores directly.
	s.Require().NoError(s.keeper.AddToTally(kv, testRoundID, 1, 1, ct1))
	got, err := s.keeper.GetTally(kv, testRoundID, 1, 1)
	s.Require().NoError(err)
	s.Require().Equal(ct1, got, "first add should store the ciphertext directly")

	// Note: We can't test real HomomorphicAdd with stub bytes (they won't
	// deserialize as valid Pallas points). The msg_server_test uses real
	// ElGamal ciphertexts for HomomorphicAdd integration testing.
}

func (s *KeeperTestSuite) TestTally_IndependentTuples() {
	s.SetupTest()
	kv := s.keeper.OpenKVStore(s.ctx)

	roundA := bytes.Repeat([]byte{0x0A}, 32)
	roundB := bytes.Repeat([]byte{0x0B}, 32)

	ctA10 := validCiphertextBytes(s.T(), 1)
	ctA11 := validCiphertextBytes(s.T(), 2)
	ctA20 := validCiphertextBytes(s.T(), 3)
	ctB10 := validCiphertextBytes(s.T(), 4)

	// Store ciphertexts in different (round, proposal, decision) tuples.
	s.Require().NoError(s.keeper.AddToTally(kv, roundA, 1, 0, ctA10))
	s.Require().NoError(s.keeper.AddToTally(kv, roundA, 1, 1, ctA11))
	s.Require().NoError(s.keeper.AddToTally(kv, roundA, 2, 0, ctA20))
	s.Require().NoError(s.keeper.AddToTally(kv, roundB, 1, 0, ctB10))

	got, err := s.keeper.GetTally(kv, roundA, 1, 0)
	s.Require().NoError(err)
	s.Require().Equal(ctA10, got)

	got, err = s.keeper.GetTally(kv, roundA, 1, 1)
	s.Require().NoError(err)
	s.Require().Equal(ctA11, got)

	got, err = s.keeper.GetTally(kv, roundA, 2, 0)
	s.Require().NoError(err)
	s.Require().Equal(ctA20, got)

	got, err = s.keeper.GetTally(kv, roundB, 1, 0)
	s.Require().NoError(err)
	s.Require().Equal(ctB10, got)

	// Unset tuple returns nil.
	got, err = s.keeper.GetTally(kv, roundB, 2, 0)
	s.Require().NoError(err)
	s.Require().Nil(got)
}

// ---------------------------------------------------------------------------
// Tally completeness
// ---------------------------------------------------------------------------

func (s *KeeperTestSuite) TestCollectNonEmptyAccumulators() {
	s.SetupTest()
	kv := s.keeper.OpenKVStore(s.ctx)

	round := &types.VoteRound{
		VoteRoundId: testRoundID,
		Status:      types.SessionStatus_SESSION_STATUS_TALLYING,
		Proposals: []*types.Proposal{
			{Id: 1, Title: "P1", Options: []*types.VoteOption{{Index: 0, Label: "Yes"}, {Index: 1, Label: "No"}}},
			{Id: 2, Title: "P2", Options: []*types.VoteOption{{Index: 0, Label: "Yes"}, {Index: 1, Label: "No"}}},
		},
	}
	s.Require().NoError(s.keeper.SetVoteRound(kv, round))

	// No accumulators yet.
	acc, err := s.keeper.CollectNonEmptyAccumulators(kv, round)
	s.Require().NoError(err)
	s.Require().Empty(acc)

	// Add accumulators for (1,0) and (2,1).
	s.Require().NoError(s.keeper.AddToTally(kv, testRoundID, 1, 0, validCiphertextBytes(s.T(), 10)))
	s.Require().NoError(s.keeper.AddToTally(kv, testRoundID, 2, 1, validCiphertextBytes(s.T(), 20)))

	acc, err = s.keeper.CollectNonEmptyAccumulators(kv, round)
	s.Require().NoError(err)
	s.Require().Len(acc, 2)
	s.Require().True(acc[[2]uint32{1, 0}])
	s.Require().True(acc[[2]uint32{2, 1}])
	s.Require().False(acc[[2]uint32{1, 1}])
}

func (s *KeeperTestSuite) TestValidateTallyCompleteness() {
	s.SetupTest()
	kv := s.keeper.OpenKVStore(s.ctx)

	round := &types.VoteRound{
		VoteRoundId: testRoundID,
		Status:      types.SessionStatus_SESSION_STATUS_TALLYING,
		Proposals: []*types.Proposal{
			{Id: 1, Title: "P1", Options: []*types.VoteOption{{Index: 0, Label: "Yes"}, {Index: 1, Label: "No"}}},
			{Id: 2, Title: "P2", Options: []*types.VoteOption{{Index: 0, Label: "Yes"}, {Index: 1, Label: "No"}}},
		},
	}
	s.Require().NoError(s.keeper.SetVoteRound(kv, round))

	s.Run("no accumulators: empty entries accepted", func() {
		err := s.keeper.ValidateTallyCompleteness(kv, round, nil)
		s.Require().NoError(err)
	})

	s.Run("no accumulators: extra zero-entries accepted", func() {
		entries := []*types.TallyEntry{
			{ProposalId: 1, VoteDecision: 0, TotalValue: 0},
		}
		err := s.keeper.ValidateTallyCompleteness(kv, round, entries)
		s.Require().NoError(err)
	})

	// Seed accumulators for (1,0) and (2,1).
	s.Require().NoError(s.keeper.AddToTally(kv, testRoundID, 1, 0, validCiphertextBytes(s.T(), 10)))
	s.Require().NoError(s.keeper.AddToTally(kv, testRoundID, 2, 1, validCiphertextBytes(s.T(), 20)))

	s.Run("complete entries accepted", func() {
		entries := []*types.TallyEntry{
			{ProposalId: 1, VoteDecision: 0, TotalValue: 10},
			{ProposalId: 2, VoteDecision: 1, TotalValue: 20},
		}
		err := s.keeper.ValidateTallyCompleteness(kv, round, entries)
		s.Require().NoError(err)
	})

	s.Run("superset entries accepted (extra zero-vote entries)", func() {
		entries := []*types.TallyEntry{
			{ProposalId: 1, VoteDecision: 0, TotalValue: 10},
			{ProposalId: 1, VoteDecision: 1, TotalValue: 0},
			{ProposalId: 2, VoteDecision: 1, TotalValue: 20},
		}
		err := s.keeper.ValidateTallyCompleteness(kv, round, entries)
		s.Require().NoError(err)
	})

	s.Run("empty entries rejected when accumulators exist", func() {
		err := s.keeper.ValidateTallyCompleteness(kv, round, nil)
		s.Require().Error(err)
		s.Require().ErrorIs(err, types.ErrTallyMismatch)
		s.Require().Contains(err.Error(), "missing entry for accumulator")
	})

	s.Run("partial entries rejected (missing one accumulator)", func() {
		entries := []*types.TallyEntry{
			{ProposalId: 1, VoteDecision: 0, TotalValue: 10},
		}
		err := s.keeper.ValidateTallyCompleteness(kv, round, entries)
		s.Require().Error(err)
		s.Require().ErrorIs(err, types.ErrTallyMismatch)
		s.Require().Contains(err.Error(), "proposal=2, decision=1")
	})
}

