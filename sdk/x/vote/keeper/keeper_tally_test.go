package keeper_test

import (
	"bytes"

	"github.com/z-cale/zally/x/vote/types"
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
// ValidateRoundForShares
// ---------------------------------------------------------------------------

func (s *KeeperTestSuite) TestValidateRoundForShares() {
	tests := []struct {
		name        string
		setup       func()
		roundID     []byte
		expectErr   bool
		errContains string
	}{
		{
			name: "active round with future end time accepted",
			setup: func() {
				kv := s.keeper.OpenKVStore(s.ctx)
				s.Require().NoError(s.keeper.SetVoteRound(kv, &types.VoteRound{
					VoteRoundId: testRoundID,
					VoteEndTime: activeEndTime,
					Status:      types.SessionStatus_SESSION_STATUS_ACTIVE,
				}))
			},
			roundID: testRoundID,
		},
		{
			name: "active round with expired end time still accepted (pre-EndBlocker transition)",
			setup: func() {
				kv := s.keeper.OpenKVStore(s.ctx)
				s.Require().NoError(s.keeper.SetVoteRound(kv, &types.VoteRound{
					VoteRoundId: testRoundID,
					VoteEndTime: expiredEndTime,
					Status:      types.SessionStatus_SESSION_STATUS_ACTIVE,
				}))
			},
			roundID: testRoundID,
		},
		{
			name: "tallying round accepted",
			setup: func() {
				kv := s.keeper.OpenKVStore(s.ctx)
				s.Require().NoError(s.keeper.SetVoteRound(kv, &types.VoteRound{
					VoteRoundId: testRoundID,
					VoteEndTime: expiredEndTime,
					Status:      types.SessionStatus_SESSION_STATUS_TALLYING,
				}))
			},
			roundID: testRoundID,
		},
		{
			name: "finalized round rejected",
			setup: func() {
				kv := s.keeper.OpenKVStore(s.ctx)
				s.Require().NoError(s.keeper.SetVoteRound(kv, &types.VoteRound{
					VoteRoundId: testRoundID,
					VoteEndTime: expiredEndTime,
					Status:      types.SessionStatus_SESSION_STATUS_FINALIZED,
				}))
			},
			roundID:     testRoundID,
			expectErr:   true,
			errContains: "vote round is not active",
		},
		{
			name:        "missing round returns ErrRoundNotFound",
			roundID:     bytes.Repeat([]byte{0xFF}, 32),
			expectErr:   true,
			errContains: "vote round not found",
		},
	}

	for _, tc := range tests {
		s.Run(tc.name, func() {
			s.SetupTest()
			if tc.setup != nil {
				tc.setup()
			}
			err := s.keeper.ValidateRoundForShares(s.ctx, tc.roundID)
			if tc.expectErr {
				s.Require().Error(err)
				if tc.errContains != "" {
					s.Require().Contains(err.Error(), tc.errContains)
				}
			} else {
				s.Require().NoError(err)
			}
		})
	}
}
