package keeper_test

import (
	"bytes"

	"github.com/mikelodder7/curvey"

	"github.com/valargroup/vote-sdk/crypto/elgamal"
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

	sk, pk := testTallyKeypair(17)
	ct1 := testTallyCiphertextBytes(s, pk, 11, 3)
	ct2 := testTallyCiphertextBytes(s, pk, 13, 5)

	// First add: stores directly.
	s.Require().NoError(s.keeper.AddToTally(kv, testRoundID, 1, 1, ct1))
	got, err := s.keeper.GetTally(kv, testRoundID, 1, 1)
	s.Require().NoError(err)
	s.Require().Equal(ct1, got, "first add should store the ciphertext directly")

	// Second add: homomorphically accumulates and stores a different ciphertext.
	s.Require().NoError(s.keeper.AddToTally(kv, testRoundID, 1, 1, ct2))
	got, err = s.keeper.GetTally(kv, testRoundID, 1, 1)
	s.Require().NoError(err)
	s.Require().NotEqual(ct1, got, "second add should update the accumulator")
	acc, err := elgamal.UnmarshalCiphertext(got)
	s.Require().NoError(err)
	s.Require().False(acc.C1.IsIdentity(), "normal accumulation must keep C1 non-identity")
	s.Require().True(elgamal.DecryptToPoint(sk, acc).Equal(elgamal.ValuePoint(24)), "normal accumulation must remain decryptable")
}

func (s *KeeperTestSuite) TestTally_FirstShareValidation() {
	pk := testTallyPublicKey(17)
	identityC1 := &elgamal.Ciphertext{
		C1: new(curvey.PointPallas).Identity(),
		C2: elgamal.PallasGenerator().Mul(new(curvey.ScalarPallas).New(7)),
	}
	identityC1Bytes, err := elgamal.MarshalCiphertext(identityC1)
	s.Require().NoError(err)
	identityC2 := &elgamal.Ciphertext{
		C1: elgamal.PallasGenerator().Mul(new(curvey.ScalarPallas).New(7)),
		C2: new(curvey.PointPallas).Identity(),
	}
	identityC2Bytes, err := elgamal.MarshalCiphertext(identityC2)
	s.Require().NoError(err)

	tests := []struct {
		name        string
		encShare    []byte
		expectErr   string
		expectStore bool
	}{
		{
			name:        "rejects identity C1",
			encShare:    identityC1Bytes,
			expectErr:   "first enc_share C1 must not be the identity point",
			expectStore: false,
		},
		{
			name:        "rejects identity C2",
			encShare:    identityC2Bytes,
			expectErr:   "first enc_share C2 must not be the identity point",
			expectStore: false,
		},
		{
			name:        "accepts randomized encryption of zero",
			encShare:    testTallyCiphertextBytes(s, pk, 0, 9),
			expectStore: true,
		},
	}

	for _, tc := range tests {
		s.Run(tc.name, func() {
			s.SetupTest()
			kv := s.keeper.OpenKVStore(s.ctx)

			err := s.keeper.AddToTally(kv, testRoundID, 1, 0, tc.encShare)
			if tc.expectErr != "" {
				s.Require().Error(err)
				s.Require().Contains(err.Error(), tc.expectErr)
			} else {
				s.Require().NoError(err)
			}

			got, err := s.keeper.GetTally(kv, testRoundID, 1, 0)
			s.Require().NoError(err)
			if !tc.expectStore {
				s.Require().Nil(got, "rejected first share must not create an accumulator")
				return
			}

			s.Require().Equal(tc.encShare, got)
			ct, err := elgamal.UnmarshalCiphertext(got)
			s.Require().NoError(err)
			s.Require().False(ct.C1.IsIdentity(), "accepted first share must keep C1 non-identity")
			s.Require().False(ct.C2.IsIdentity(), "accepted first share must keep C2 non-identity")
		})
	}
}

func (s *KeeperTestSuite) TestTally_RejectsDegenerateCiphertextAfterFirstShare() {
	s.SetupTest()
	kv := s.keeper.OpenKVStore(s.ctx)

	pk := testTallyPublicKey(17)
	ct1 := testTallyCiphertextBytes(s, pk, 4, 11)

	s.Require().NoError(s.keeper.AddToTally(kv, testRoundID, 1, 0, ct1))

	tests := []struct {
		name      string
		encShare  []byte
		expectErr string
	}{
		{
			name:      "rejects incoming identity C2",
			encShare:  testTallyCiphertextBytesFromPoints(s, elgamal.PallasGenerator().Mul(new(curvey.ScalarPallas).New(5)), new(curvey.PointPallas).Identity()),
			expectErr: "enc_share C2 must not be the identity point",
		},
		{
			name:      "rejects accumulated identity C1",
			encShare:  testTallyCiphertextBytesWithScalar(s, pk, 6, new(curvey.ScalarPallas).New(11).Neg()),
			expectErr: "accumulated tally C1 must not be the identity point",
		},
		{
			name: "rejects accumulated identity C2",
			encShare: testTallyCiphertextBytesFromPoints(
				s,
				elgamal.PallasGenerator().Mul(new(curvey.ScalarPallas).New(5)),
				testTallyCiphertextPoint(s, ct1, "seed accumulator").C2.Neg(),
			),
			expectErr: "accumulated tally C2 must not be the identity point",
		},
	}

	for _, tc := range tests {
		s.Run(tc.name, func() {
			err := s.keeper.AddToTally(kv, testRoundID, 1, 0, tc.encShare)
			s.Require().Error(err)
			s.Require().Contains(err.Error(), tc.expectErr)

			got, err := s.keeper.GetTally(kv, testRoundID, 1, 0)
			s.Require().NoError(err)
			s.Require().Equal(ct1, got, "rejected degenerate share must leave the existing accumulator unchanged")
		})
	}
}

func testTallyPublicKey(seed int) *elgamal.PublicKey {
	_, pk := testTallyKeypair(seed)
	return pk
}

func testTallyKeypair(seed int) (*elgamal.SecretKey, *elgamal.PublicKey) {
	sk := &elgamal.SecretKey{Scalar: new(curvey.ScalarPallas).New(seed)}
	pk := &elgamal.PublicKey{Point: elgamal.PallasGenerator().Mul(sk.Scalar)}
	return sk, pk
}

func testTallyCiphertextBytes(s *KeeperTestSuite, pk *elgamal.PublicKey, value uint64, randomness int) []byte {
	return testTallyCiphertextBytesWithScalar(s, pk, value, new(curvey.ScalarPallas).New(randomness))
}

func testTallyCiphertextBytesWithScalar(s *KeeperTestSuite, pk *elgamal.PublicKey, value uint64, r curvey.Scalar) []byte {
	s.T().Helper()
	ct, err := elgamal.EncryptWithRandomness(pk, value, r)
	s.Require().NoError(err)
	ctBytes, err := elgamal.MarshalCiphertext(ct)
	s.Require().NoError(err)
	return ctBytes
}

func testTallyCiphertextBytesFromPoints(s *KeeperTestSuite, c1, c2 curvey.Point) []byte {
	s.T().Helper()
	ctBytes, err := elgamal.MarshalCiphertext(&elgamal.Ciphertext{C1: c1, C2: c2})
	s.Require().NoError(err)
	return ctBytes
}

func testTallyCiphertextPoint(s *KeeperTestSuite, ctBytes []byte, label string) *elgamal.Ciphertext {
	s.T().Helper()
	ct, err := elgamal.UnmarshalCiphertext(ctBytes)
	s.Require().NoError(err, label)
	return ct
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

func (s *KeeperTestSuite) TestVoteSummaryIncludesOptionDescriptions() {
	s.SetupTest()
	kv := s.keeper.OpenKVStore(s.ctx)

	round := &types.VoteRound{
		VoteRoundId: testRoundID,
		Status:      types.SessionStatus_SESSION_STATUS_ACTIVE,
		Description: "Round description",
		VoteEndTime: 123,
		Proposals: []*types.Proposal{
			{
				Id:          1,
				Title:       "P1",
				Description: "Proposal description",
				Options: []*types.VoteOption{
					{Index: 0, Label: "Yes", Description: "Approve the change."},
					{Index: 1, Label: "No", Description: "Reject the change."},
				},
			},
		},
	}
	s.Require().NoError(s.keeper.SetVoteRound(kv, round))
	s.Require().NoError(s.keeper.IncrementShareCount(kv, testRoundID, 1, 0))

	summary, err := s.keeper.GetVoteSummary(kv, testRoundID)
	s.Require().NoError(err)
	s.Require().Len(summary.Proposals, 1)
	s.Require().Len(summary.Proposals[0].Options, 2)
	s.Require().Equal("Proposal description", summary.Proposals[0].Description)
	s.Require().Equal("Approve the change.", summary.Proposals[0].Options[0].Description)
	s.Require().Equal("Reject the change.", summary.Proposals[0].Options[1].Description)
	s.Require().Equal(uint64(1), summary.Proposals[0].Options[0].BallotCount)
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

func (s *KeeperTestSuite) TestValidatePartialDecryptionEntries() {
	validPartial := validPointBytes(7)

	tests := []struct {
		name        string
		tallies     []uint32
		entries     []*types.PartialDecryptionEntry
		wantErrIs   error
		errContains string
	}{
		{
			name:    "valid entries cover every accumulator",
			tallies: []uint32{0},
			entries: []*types.PartialDecryptionEntry{
				{ProposalId: 1, VoteDecision: 0, PartialDecrypt: validPartial},
			},
		},
		{
			name:        "empty entries",
			tallies:     []uint32{0},
			entries:     nil,
			wantErrIs:   types.ErrInvalidField,
			errContains: "entries cannot be empty",
		},
		{
			name:    "missing accumulator entry",
			tallies: []uint32{0, 1},
			entries: []*types.PartialDecryptionEntry{
				{ProposalId: 1, VoteDecision: 0, PartialDecrypt: validPartial},
			},
			wantErrIs:   types.ErrInvalidField,
			errContains: "missing partial decryption for accumulator",
		},
		{
			name:    "invalid partial decrypt point",
			tallies: []uint32{0},
			entries: []*types.PartialDecryptionEntry{
				{ProposalId: 1, VoteDecision: 0, PartialDecrypt: []byte{0x01, 0x02}},
			},
			wantErrIs:   types.ErrInvalidField,
			errContains: "partial_decrypt is not a valid Pallas point",
		},
		{
			name:    "out of bounds proposal id",
			tallies: []uint32{0},
			entries: []*types.PartialDecryptionEntry{
				{ProposalId: 1, VoteDecision: 0, PartialDecrypt: validPartial},
				{ProposalId: 2, VoteDecision: 0, PartialDecrypt: validPartial},
			},
			wantErrIs:   types.ErrInvalidProposalID,
			errContains: "proposal_id",
		},
		{
			name:    "entry with no accumulator",
			tallies: []uint32{0},
			entries: []*types.PartialDecryptionEntry{
				{ProposalId: 1, VoteDecision: 0, PartialDecrypt: validPartial},
				{ProposalId: 1, VoteDecision: 1, PartialDecrypt: validPartial},
			},
			wantErrIs:   types.ErrInvalidField,
			errContains: "no accumulator",
		},
	}

	for _, tc := range tests {
		s.Run(tc.name, func() {
			s.SetupTest()
			kv := s.keeper.OpenKVStore(s.ctx)
			roundID := bytes.Repeat([]byte{0x9F}, 32)
			round := &types.VoteRound{
				VoteRoundId: roundID,
				Status:      types.SessionStatus_SESSION_STATUS_TALLYING,
				Proposals: []*types.Proposal{
					{Id: 1, Title: "Prop 1", Options: []*types.VoteOption{
						{Index: 0, Label: "Yes"},
						{Index: 1, Label: "No"},
					}},
				},
			}
			s.Require().NoError(s.keeper.SetVoteRound(kv, round))
			for _, decision := range tc.tallies {
				s.Require().NoError(s.keeper.AddToTally(kv, roundID, 1, decision, validCiphertextBytes(s.T(), uint64(decision+1))))
			}

			validatedEntries, err := s.keeper.ValidateAndDecodePartialDecryptionEntries(kv, round, tc.entries)
			if tc.wantErrIs != nil {
				s.Require().Error(err)
				s.Require().ErrorIs(err, tc.wantErrIs)
				s.Require().Contains(err.Error(), tc.errContains)
			} else {
				s.Require().NoError(err)
				s.Require().Len(validatedEntries, len(tc.entries))
				for i, validatedEntry := range validatedEntries {
					s.Require().Same(tc.entries[i], validatedEntry.Entry)
					s.Require().NotNil(validatedEntry.PartialDecrypt)
					s.Require().NotNil(validatedEntry.Accumulator)
				}
			}
		})
	}
}
