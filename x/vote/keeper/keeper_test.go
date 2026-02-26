package keeper_test

import (
	"bytes"
	"encoding/binary"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"

	"cosmossdk.io/log"
	storetypes "cosmossdk.io/store/types"

	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/cosmos/cosmos-sdk/testutil"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/z-cale/zally/crypto/votetree"
	"github.com/z-cale/zally/x/vote/keeper"
	"github.com/z-cale/zally/x/vote/types"
)

// fpLE returns a 32-byte little-endian Pallas Fp encoding of v (canonical for 0 <= v < 2^64).
// Use for commitment tree leaves so the votetree FFI accepts them.
func fpLE(v uint64) []byte {
	buf := make([]byte, 32)
	binary.LittleEndian.PutUint64(buf[:8], v)
	return buf
}

// ---------------------------------------------------------------------------
// Test constants
// ---------------------------------------------------------------------------

var (
	testBlockTime  = time.Unix(1_000_000, 0).UTC()
	activeEndTime  = uint64(2_000_000)
	expiredEndTime = uint64(999_999)
	testRoundID    = bytes.Repeat([]byte{0x01}, 32)
)

// ---------------------------------------------------------------------------
// Test suite
// ---------------------------------------------------------------------------

type KeeperTestSuite struct {
	suite.Suite
	ctx    sdk.Context
	keeper *keeper.Keeper
}

func TestKeeperTestSuite(t *testing.T) {
	suite.Run(t, new(KeeperTestSuite))
}

func (s *KeeperTestSuite) SetupTest() {
	key := storetypes.NewKVStoreKey(types.StoreKey)
	tkey := storetypes.NewTransientStoreKey("transient_test")
	testCtx := testutil.DefaultContextWithDB(s.T(), key, tkey)

	s.ctx = testCtx.Ctx.WithBlockTime(testBlockTime)
	storeService := runtime.NewKVStoreService(key)
	s.keeper = keeper.NewKeeper(storeService, "zvote1authority", log.NewNopLogger(), nil)
}

// ---------------------------------------------------------------------------
// Nullifier set
// ---------------------------------------------------------------------------

func (s *KeeperTestSuite) TestNullifier_SetAndHas() {
	nf := bytes.Repeat([]byte{0xAA}, 32)

	tests := []struct {
		name      string
		setup     func()
		nullifier []byte
		expectHas bool
	}{
		{
			name:      "new nullifier not present",
			nullifier: nf,
			expectHas: false,
		},
		{
			name: "nullifier present after Set",
			setup: func() {
				kv := s.keeper.OpenKVStore(s.ctx)
				s.Require().NoError(s.keeper.SetNullifier(kv, types.NullifierTypeGov, testRoundID, nf))
			},
			nullifier: nf,
			expectHas: true,
		},
		{
			name: "different nullifier still absent",
			setup: func() {
				kv := s.keeper.OpenKVStore(s.ctx)
				s.Require().NoError(s.keeper.SetNullifier(kv, types.NullifierTypeGov, testRoundID, nf))
			},
			nullifier: bytes.Repeat([]byte{0xBB}, 32),
			expectHas: false,
		},
	}

	for _, tc := range tests {
		s.Run(tc.name, func() {
			s.SetupTest()
			if tc.setup != nil {
				tc.setup()
			}
			kv := s.keeper.OpenKVStore(s.ctx)
			has, err := s.keeper.HasNullifier(kv, types.NullifierTypeGov, testRoundID, tc.nullifier)
			s.Require().NoError(err)
			s.Require().Equal(tc.expectHas, has)
		})
	}
}

func (s *KeeperTestSuite) TestNullifier_MultipleIndependent() {
	s.SetupTest()
	kv := s.keeper.OpenKVStore(s.ctx)

	nf1 := bytes.Repeat([]byte{0x01}, 32)
	nf2 := bytes.Repeat([]byte{0x02}, 32)
	nf3 := bytes.Repeat([]byte{0x03}, 32)

	// Record nf1 and nf2, leave nf3 unrecorded.
	s.Require().NoError(s.keeper.SetNullifier(kv, types.NullifierTypeGov, testRoundID, nf1))
	s.Require().NoError(s.keeper.SetNullifier(kv, types.NullifierTypeGov, testRoundID, nf2))

	has1, err := s.keeper.HasNullifier(kv, types.NullifierTypeGov, testRoundID, nf1)
	s.Require().NoError(err)
	s.Require().True(has1)

	has2, err := s.keeper.HasNullifier(kv, types.NullifierTypeGov, testRoundID, nf2)
	s.Require().NoError(err)
	s.Require().True(has2)

	has3, err := s.keeper.HasNullifier(kv, types.NullifierTypeGov, testRoundID, nf3)
	s.Require().NoError(err)
	s.Require().False(has3)
}

// ---------------------------------------------------------------------------
// Nullifier scoping: cross-type and cross-round isolation
// ---------------------------------------------------------------------------

func (s *KeeperTestSuite) TestNullifier_CrossTypeIsolation() {
	s.SetupTest()
	kv := s.keeper.OpenKVStore(s.ctx)

	nf := bytes.Repeat([]byte{0xAA}, 32)

	// Record as gov nullifier.
	s.Require().NoError(s.keeper.SetNullifier(kv, types.NullifierTypeGov, testRoundID, nf))

	// Same bytes should NOT be found in vote-authority-note or share namespace.
	hasVoteAuthorityNote, err := s.keeper.HasNullifier(kv, types.NullifierTypeVoteAuthorityNote, testRoundID, nf)
	s.Require().NoError(err)
	s.Require().False(hasVoteAuthorityNote, "gov nullifier must not collide with vote-authority-note namespace")

	hasShare, err := s.keeper.HasNullifier(kv, types.NullifierTypeShare, testRoundID, nf)
	s.Require().NoError(err)
	s.Require().False(hasShare, "gov nullifier must not collide with share namespace")

	// Same bytes SHOULD be found in gov namespace.
	hasGov, err := s.keeper.HasNullifier(kv, types.NullifierTypeGov, testRoundID, nf)
	s.Require().NoError(err)
	s.Require().True(hasGov)
}

func (s *KeeperTestSuite) TestNullifier_CrossRoundIsolation() {
	s.SetupTest()
	kv := s.keeper.OpenKVStore(s.ctx)

	roundA := bytes.Repeat([]byte{0x0A}, 32)
	roundB := bytes.Repeat([]byte{0x0B}, 32)
	nf := bytes.Repeat([]byte{0xAA}, 32)

	// Record in round A.
	s.Require().NoError(s.keeper.SetNullifier(kv, types.NullifierTypeGov, roundA, nf))

	// Same bytes should NOT be found in round B.
	hasB, err := s.keeper.HasNullifier(kv, types.NullifierTypeGov, roundB, nf)
	s.Require().NoError(err)
	s.Require().False(hasB, "nullifier in round A must not collide with round B")

	// Should still be found in round A.
	hasA, err := s.keeper.HasNullifier(kv, types.NullifierTypeGov, roundA, nf)
	s.Require().NoError(err)
	s.Require().True(hasA)
}

// ---------------------------------------------------------------------------
// Vote rounds
// ---------------------------------------------------------------------------

func (s *KeeperTestSuite) TestVoteRound_SetAndGet() {
	tests := []struct {
		name        string
		round       *types.VoteRound
		lookupID    []byte
		expectFound bool
	}{
		{
			name: "store and retrieve round",
			round: &types.VoteRound{
				VoteRoundId:       testRoundID,
				SnapshotHeight:    100,
				SnapshotBlockhash: bytes.Repeat([]byte{0x01}, 32),
				ProposalsHash:     bytes.Repeat([]byte{0x02}, 32),
				VoteEndTime:       activeEndTime,
				NullifierImtRoot:  bytes.Repeat([]byte{0x03}, 32),
				NcRoot:            bytes.Repeat([]byte{0x04}, 32),
				Creator:           "zvote1creator",
			},
			lookupID:    testRoundID,
			expectFound: true,
		},
		{
			name:        "missing round returns ErrRoundNotFound",
			lookupID:    bytes.Repeat([]byte{0xFF}, 32),
			expectFound: false,
		},
	}

	for _, tc := range tests {
		s.Run(tc.name, func() {
			s.SetupTest()
			kv := s.keeper.OpenKVStore(s.ctx)

			if tc.round != nil {
				s.Require().NoError(s.keeper.SetVoteRound(kv, tc.round))
			}

			got, err := s.keeper.GetVoteRound(kv, tc.lookupID)
			if tc.expectFound {
				s.Require().NoError(err)
				s.Require().Equal(tc.round.VoteRoundId, got.VoteRoundId)
				s.Require().Equal(tc.round.SnapshotHeight, got.SnapshotHeight)
				s.Require().Equal(tc.round.VoteEndTime, got.VoteEndTime)
				s.Require().Equal(tc.round.Creator, got.Creator)
			} else {
				s.Require().ErrorIs(err, types.ErrRoundNotFound)
				s.Require().Nil(got)
			}
		})
	}
}

func (s *KeeperTestSuite) TestVoteRound_OverwriteExisting() {
	s.SetupTest()
	kv := s.keeper.OpenKVStore(s.ctx)

	round := &types.VoteRound{
		VoteRoundId: testRoundID,
		VoteEndTime: 1000,
		Creator:     "original",
	}
	s.Require().NoError(s.keeper.SetVoteRound(kv, round))

	// Overwrite with new end time.
	round.VoteEndTime = 9999
	round.Creator = "updated"
	s.Require().NoError(s.keeper.SetVoteRound(kv, round))

	got, err := s.keeper.GetVoteRound(kv, testRoundID)
	s.Require().NoError(err)
	s.Require().Equal(uint64(9999), got.VoteEndTime)
	s.Require().Equal("updated", got.Creator)
}

func (s *KeeperTestSuite) TestVoteRound_MultipleRoundsIndependent() {
	s.SetupTest()
	kv := s.keeper.OpenKVStore(s.ctx)

	id1 := bytes.Repeat([]byte{0x01}, 32)
	id2 := bytes.Repeat([]byte{0x02}, 32)

	round1 := &types.VoteRound{VoteRoundId: id1, VoteEndTime: 1000, Creator: "alice"}
	round2 := &types.VoteRound{VoteRoundId: id2, VoteEndTime: 2000, Creator: "bob"}

	s.Require().NoError(s.keeper.SetVoteRound(kv, round1))
	s.Require().NoError(s.keeper.SetVoteRound(kv, round2))

	got1, err := s.keeper.GetVoteRound(kv, id1)
	s.Require().NoError(err)
	s.Require().Equal("alice", got1.Creator)

	got2, err := s.keeper.GetVoteRound(kv, id2)
	s.Require().NoError(err)
	s.Require().Equal("bob", got2.Creator)
}

// ---------------------------------------------------------------------------
// Commitment tree
// ---------------------------------------------------------------------------

func (s *KeeperTestSuite) TestCommitmentTreeState_DefaultAndSet() {
	tests := []struct {
		name          string
		seedIndex     *uint64 // if non-nil, seed the tree state with this NextIndex
		expectedIndex uint64
	}{
		{
			name:          "default state has NextIndex 0",
			expectedIndex: 0,
		},
		{
			name:          "explicit state preserved",
			seedIndex:     uint64Ptr(42),
			expectedIndex: 42,
		},
	}

	for _, tc := range tests {
		s.Run(tc.name, func() {
			s.SetupTest()
			kv := s.keeper.OpenKVStore(s.ctx)
			if tc.seedIndex != nil {
				s.Require().NoError(s.keeper.SetCommitmentTreeState(kv, &types.CommitmentTreeState{NextIndex: *tc.seedIndex}))
			}
			state, err := s.keeper.GetCommitmentTreeState(kv)
			s.Require().NoError(err)
			s.Require().Equal(tc.expectedIndex, state.NextIndex)
		})
	}
}

func uint64Ptr(v uint64) *uint64 { return &v }

func (s *KeeperTestSuite) TestAppendCommitment_SequentialIndices() {
	s.SetupTest()
	kv := s.keeper.OpenKVStore(s.ctx)

	commitments := [][]byte{
		fpLE(0xA1),
		fpLE(0xA2),
		fpLE(0xA3),
	}

	for i, cm := range commitments {
		idx, err := s.keeper.AppendCommitment(kv, cm)
		s.Require().NoError(err)
		s.Require().Equal(uint64(i), idx, "commitment %d should get sequential index", i)
	}

	// Verify tree state was updated.
	state, err := s.keeper.GetCommitmentTreeState(kv)
	s.Require().NoError(err)
	s.Require().Equal(uint64(3), state.NextIndex)

	// Verify each leaf is readable at its index.
	for i, cm := range commitments {
		leaf, err := kv.Get(types.CommitmentLeafKey(uint64(i)))
		s.Require().NoError(err)
		s.Require().Equal(cm, leaf)
	}
}

func (s *KeeperTestSuite) TestAppendCommitment_ContinuesFromExistingState() {
	s.SetupTest()
	kv := s.keeper.OpenKVStore(s.ctx)

	// Seed the tree state to start at index 100.
	s.Require().NoError(s.keeper.SetCommitmentTreeState(kv, &types.CommitmentTreeState{NextIndex: 100}))

	idx, err := s.keeper.AppendCommitment(kv, fpLE(0xFF))
	s.Require().NoError(err)
	s.Require().Equal(uint64(100), idx)

	state, err := s.keeper.GetCommitmentTreeState(kv)
	s.Require().NoError(err)
	s.Require().Equal(uint64(101), state.NextIndex)
}

// ---------------------------------------------------------------------------
// Commitment tree roots
// ---------------------------------------------------------------------------

func (s *KeeperTestSuite) TestCommitmentRoot_SetAndGetByHeight() {
	tests := []struct {
		name        string
		height      uint64
		root        []byte
		lookupH     uint64
		expectRoot  []byte
	}{
		{
			name:       "store and retrieve root at height 10",
			height:     10,
			root:       bytes.Repeat([]byte{0xCC}, 32),
			lookupH:    10,
			expectRoot: bytes.Repeat([]byte{0xCC}, 32),
		},
		{
			name:       "missing height returns nil",
			height:     10,
			root:       bytes.Repeat([]byte{0xCC}, 32),
			lookupH:    999,
			expectRoot: nil,
		},
	}

	for _, tc := range tests {
		s.Run(tc.name, func() {
			s.SetupTest()
			kv := s.keeper.OpenKVStore(s.ctx)

			s.Require().NoError(s.keeper.SetCommitmentRootAtHeight(kv, tc.height, tc.root))

			got, err := s.keeper.GetCommitmentRootAtHeight(kv, tc.lookupH)
			s.Require().NoError(err)
			s.Require().Equal(tc.expectRoot, got)
		})
	}
}

func (s *KeeperTestSuite) TestCommitmentRoot_MultipleHeights() {
	s.SetupTest()
	kv := s.keeper.OpenKVStore(s.ctx)

	root5 := bytes.Repeat([]byte{0x05}, 32)
	root10 := bytes.Repeat([]byte{0x0A}, 32)
	root15 := bytes.Repeat([]byte{0x0F}, 32)

	s.Require().NoError(s.keeper.SetCommitmentRootAtHeight(kv, 5, root5))
	s.Require().NoError(s.keeper.SetCommitmentRootAtHeight(kv, 10, root10))
	s.Require().NoError(s.keeper.SetCommitmentRootAtHeight(kv, 15, root15))

	got5, err := s.keeper.GetCommitmentRootAtHeight(kv, 5)
	s.Require().NoError(err)
	s.Require().Equal(root5, got5)

	got10, err := s.keeper.GetCommitmentRootAtHeight(kv, 10)
	s.Require().NoError(err)
	s.Require().Equal(root10, got10)

	got15, err := s.keeper.GetCommitmentRootAtHeight(kv, 15)
	s.Require().NoError(err)
	s.Require().Equal(root15, got15)
}

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

	ctA10 := bytes.Repeat([]byte{0xA1}, 64)
	ctA11 := bytes.Repeat([]byte{0xA2}, 64)
	ctA20 := bytes.Repeat([]byte{0xA3}, 64)
	ctB10 := bytes.Repeat([]byte{0xB1}, 64)

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
// Validation helpers (ValidateRoundForVoting, ValidateRoundForShares,
// ValidateRoundActive, CheckNullifiersUnique)
// ---------------------------------------------------------------------------

func (s *KeeperTestSuite) TestValidateRoundForVoting() {
	tests := []struct {
		name        string
		setup       func()
		roundID     []byte
		expectErr   bool
		errContains string
	}{
		{
			name: "active round passes",
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
			name:        "missing round returns ErrRoundNotFound",
			roundID:     bytes.Repeat([]byte{0xFF}, 32),
			expectErr:   true,
			errContains: "vote round not found",
		},
		{
			name: "expired round returns ErrRoundNotActive",
			setup: func() {
				kv := s.keeper.OpenKVStore(s.ctx)
				s.Require().NoError(s.keeper.SetVoteRound(kv, &types.VoteRound{
					VoteRoundId: testRoundID,
					VoteEndTime: expiredEndTime,
					Status:      types.SessionStatus_SESSION_STATUS_ACTIVE,
				}))
			},
			roundID:     testRoundID,
			expectErr:   true,
			errContains: "vote round is not active",
		},
		{
			name: "round ending exactly at block time is expired",
			setup: func() {
				kv := s.keeper.OpenKVStore(s.ctx)
				s.Require().NoError(s.keeper.SetVoteRound(kv, &types.VoteRound{
					VoteRoundId: testRoundID,
					VoteEndTime: uint64(testBlockTime.Unix()), // exactly equal
					Status:      types.SessionStatus_SESSION_STATUS_ACTIVE,
				}))
			},
			roundID:     testRoundID,
			expectErr:   true,
			errContains: "vote round is not active",
		},
		{
			name: "tallying round rejected for voting",
			setup: func() {
				kv := s.keeper.OpenKVStore(s.ctx)
				s.Require().NoError(s.keeper.SetVoteRound(kv, &types.VoteRound{
					VoteRoundId: testRoundID,
					VoteEndTime: activeEndTime,
					Status:      types.SessionStatus_SESSION_STATUS_TALLYING,
				}))
			},
			roundID:     testRoundID,
			expectErr:   true,
			errContains: "vote round is not active",
		},
		{
			name: "finalized round rejected for voting",
			setup: func() {
				kv := s.keeper.OpenKVStore(s.ctx)
				s.Require().NoError(s.keeper.SetVoteRound(kv, &types.VoteRound{
					VoteRoundId: testRoundID,
					VoteEndTime: activeEndTime,
					Status:      types.SessionStatus_SESSION_STATUS_FINALIZED,
				}))
			},
			roundID:     testRoundID,
			expectErr:   true,
			errContains: "vote round is not active",
		},
	}

	for _, tc := range tests {
		s.Run(tc.name, func() {
			s.SetupTest()
			if tc.setup != nil {
				tc.setup()
			}
			err := s.keeper.ValidateRoundForVoting(s.ctx, tc.roundID)
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

// TestValidateRoundActive verifies the legacy wrapper delegates to ValidateRoundForVoting.
func (s *KeeperTestSuite) TestValidateRoundActive() {
	s.SetupTest()
	kv := s.keeper.OpenKVStore(s.ctx)
	s.Require().NoError(s.keeper.SetVoteRound(kv, &types.VoteRound{
		VoteRoundId: testRoundID,
		VoteEndTime: activeEndTime,
		Status:      types.SessionStatus_SESSION_STATUS_ACTIVE,
	}))
	s.Require().NoError(s.keeper.ValidateRoundActive(s.ctx, testRoundID))
}

// ---------------------------------------------------------------------------
// UpdateVoteRoundStatus
// ---------------------------------------------------------------------------

func (s *KeeperTestSuite) TestUpdateVoteRoundStatus() {
	s.SetupTest()
	kv := s.keeper.OpenKVStore(s.ctx)

	s.Require().NoError(s.keeper.SetVoteRound(kv, &types.VoteRound{
		VoteRoundId: testRoundID,
		VoteEndTime: activeEndTime,
		Status:      types.SessionStatus_SESSION_STATUS_ACTIVE,
	}))

	// Transition to TALLYING.
	s.Require().NoError(s.keeper.UpdateVoteRoundStatus(kv, testRoundID, types.SessionStatus_SESSION_STATUS_TALLYING))

	round, err := s.keeper.GetVoteRound(kv, testRoundID)
	s.Require().NoError(err)
	s.Require().Equal(types.SessionStatus_SESSION_STATUS_TALLYING, round.Status)

	// Transition to FINALIZED.
	s.Require().NoError(s.keeper.UpdateVoteRoundStatus(kv, testRoundID, types.SessionStatus_SESSION_STATUS_FINALIZED))

	round, err = s.keeper.GetVoteRound(kv, testRoundID)
	s.Require().NoError(err)
	s.Require().Equal(types.SessionStatus_SESSION_STATUS_FINALIZED, round.Status)

	// Missing round returns error.
	err = s.keeper.UpdateVoteRoundStatus(kv, bytes.Repeat([]byte{0xFF}, 32), types.SessionStatus_SESSION_STATUS_TALLYING)
	s.Require().ErrorIs(err, types.ErrRoundNotFound)
}

// ---------------------------------------------------------------------------
// IterateActiveRounds
// ---------------------------------------------------------------------------

func (s *KeeperTestSuite) TestIterateActiveRounds() {
	s.SetupTest()
	kv := s.keeper.OpenKVStore(s.ctx)

	id1 := bytes.Repeat([]byte{0x01}, 32)
	id2 := bytes.Repeat([]byte{0x02}, 32)
	id3 := bytes.Repeat([]byte{0x03}, 32)

	// Active round.
	s.Require().NoError(s.keeper.SetVoteRound(kv, &types.VoteRound{
		VoteRoundId: id1, VoteEndTime: activeEndTime, Status: types.SessionStatus_SESSION_STATUS_ACTIVE,
	}))
	// Tallying round (should be skipped).
	s.Require().NoError(s.keeper.SetVoteRound(kv, &types.VoteRound{
		VoteRoundId: id2, VoteEndTime: expiredEndTime, Status: types.SessionStatus_SESSION_STATUS_TALLYING,
	}))
	// Another active round.
	s.Require().NoError(s.keeper.SetVoteRound(kv, &types.VoteRound{
		VoteRoundId: id3, VoteEndTime: activeEndTime, Status: types.SessionStatus_SESSION_STATUS_ACTIVE,
	}))

	var activeIDs [][]byte
	err := s.keeper.IterateActiveRounds(kv, func(round *types.VoteRound) bool {
		id := make([]byte, len(round.VoteRoundId))
		copy(id, round.VoteRoundId)
		activeIDs = append(activeIDs, id)
		return false
	})
	s.Require().NoError(err)
	s.Require().Len(activeIDs, 2, "should yield only ACTIVE rounds")

	// Verify the correct IDs were returned (order follows KV store key ordering).
	s.Require().True(bytes.Equal(activeIDs[0], id1) || bytes.Equal(activeIDs[0], id3))
	s.Require().True(bytes.Equal(activeIDs[1], id1) || bytes.Equal(activeIDs[1], id3))
	s.Require().False(bytes.Equal(activeIDs[0], activeIDs[1]))
}

func (s *KeeperTestSuite) TestCheckNullifiersUnique() {
	nf1 := bytes.Repeat([]byte{0x01}, 32)
	nf2 := bytes.Repeat([]byte{0x02}, 32)
	nf3 := bytes.Repeat([]byte{0x03}, 32)

	tests := []struct {
		name        string
		setup       func()
		nullifiers  [][]byte
		expectErr   bool
		errContains string
	}{
		{
			name:       "all fresh nullifiers pass",
			nullifiers: [][]byte{nf1, nf2, nf3},
		},
		{
			name: "first nullifier already spent",
			setup: func() {
				kv := s.keeper.OpenKVStore(s.ctx)
				s.Require().NoError(s.keeper.SetNullifier(kv, types.NullifierTypeGov, testRoundID, nf1))
			},
			nullifiers:  [][]byte{nf1, nf2},
			expectErr:   true,
			errContains: "nullifier already spent",
		},
		{
			name: "second nullifier already spent",
			setup: func() {
				kv := s.keeper.OpenKVStore(s.ctx)
				s.Require().NoError(s.keeper.SetNullifier(kv, types.NullifierTypeGov, testRoundID, nf2))
			},
			nullifiers:  [][]byte{nf1, nf2},
			expectErr:   true,
			errContains: "nullifier already spent",
		},
		{
			name:       "empty nullifier list passes",
			nullifiers: [][]byte{},
		},
	}

	for _, tc := range tests {
		s.Run(tc.name, func() {
			s.SetupTest()
			if tc.setup != nil {
				tc.setup()
			}
			err := s.keeper.CheckNullifiersUnique(s.ctx, types.NullifierTypeGov, testRoundID, tc.nullifiers)
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

// ---------------------------------------------------------------------------
// ComputeTreeRoot
// ---------------------------------------------------------------------------

func (s *KeeperTestSuite) TestComputeTreeRoot() {
	tests := []struct {
		name       string
		leaves     [][]byte
		expectNil  bool
		expectLen  int
	}{
		{
			name:      "empty tree returns nil",
			leaves:    nil,
			expectNil: true,
		},
		{
			name:      "single leaf produces 32-byte root",
			leaves:    [][]byte{fpLE(1)},
			expectLen: 32,
		},
		{
			name: "two leaves produce 32-byte root",
			leaves: [][]byte{
				fpLE(1),
				fpLE(2),
			},
			expectLen: 32,
		},
	}

	for _, tc := range tests {
		s.Run(tc.name, func() {
			s.SetupTest()
			kv := s.keeper.OpenKVStore(s.ctx)

			for _, leaf := range tc.leaves {
				_, err := s.keeper.AppendCommitment(kv, leaf)
				s.Require().NoError(err)
			}

			root, err := s.keeper.ComputeTreeRoot(kv, uint64(len(tc.leaves)), 1)
			s.Require().NoError(err)
			if tc.expectNil {
				s.Require().Nil(root)
			} else {
				s.Require().Len(root, tc.expectLen)
			}
		})
	}
}

func (s *KeeperTestSuite) TestComputeTreeRoot_DeterministicAndDistinct() {
	s.SetupTest()
	kv := s.keeper.OpenKVStore(s.ctx)

	_, err := s.keeper.AppendCommitment(kv, fpLE(1))
	s.Require().NoError(err)

	root1, err := s.keeper.ComputeTreeRoot(kv, 1, 1)
	s.Require().NoError(err)

	// Same state produces same root.
	root1Again, err := s.keeper.ComputeTreeRoot(kv, 1, 1)
	s.Require().NoError(err)
	s.Require().Equal(root1, root1Again)

	// Add another leaf — root changes.
	_, err = s.keeper.AppendCommitment(kv, fpLE(2))
	s.Require().NoError(err)

	root2, err := s.keeper.ComputeTreeRoot(kv, 2, 2)
	s.Require().NoError(err)
	s.Require().NotEqual(root1, root2)
}

// ---------------------------------------------------------------------------
// Incremental tree handle tests
// ---------------------------------------------------------------------------

// TestComputeTreeRoot_Incremental verifies that the stateful tree produces the
// same root as a fresh full rebuild when leaves are appended incrementally.
func (s *KeeperTestSuite) TestComputeTreeRoot_Incremental() {
	s.SetupTest()
	kv := s.keeper.OpenKVStore(s.ctx)

	// Append 3 leaves across 3 simulated blocks.
	_, err := s.keeper.AppendCommitment(kv, fpLE(1))
	s.Require().NoError(err)
	root1, err := s.keeper.ComputeTreeRoot(kv, 1, 1)
	s.Require().NoError(err)

	_, err = s.keeper.AppendCommitment(kv, fpLE(2))
	s.Require().NoError(err)
	root2, err := s.keeper.ComputeTreeRoot(kv, 2, 2)
	s.Require().NoError(err)

	_, err = s.keeper.AppendCommitment(kv, fpLE(3))
	s.Require().NoError(err)
	root3, err := s.keeper.ComputeTreeRoot(kv, 3, 3)
	s.Require().NoError(err)

	// All roots are distinct.
	s.Require().NotEqual(root1, root2)
	s.Require().NotEqual(root2, root3)

	// root3 must match what a cold-start fresh rebuild produces.
	freshKeeper := keeper.NewKeeper(
		s.keeper.StoreServiceForTest(),
		"zvote1authority",
		log.NewNopLogger(),
		nil,
	)
	freshRoot, err := freshKeeper.ComputeTreeRoot(kv, 3, 3)
	s.Require().NoError(err)
	s.Require().Equal(root3, freshRoot, "incremental root must match cold-start root")
}

// TestComputeTreeRoot_DeltaAppend verifies that calling ComputeTreeRoot with a
// new nextIndex only reads and appends the delta leaves (cursor behavior).
func (s *KeeperTestSuite) TestComputeTreeRoot_DeltaAppend() {
	s.SetupTest()
	kv := s.keeper.OpenKVStore(s.ctx)

	// Add 5 leaves.
	for i := uint64(1); i <= 5; i++ {
		_, err := s.keeper.AppendCommitment(kv, fpLE(i))
		s.Require().NoError(err)
	}

	// First call: cold-start loads all 5 leaves.
	root5, err := s.keeper.ComputeTreeRoot(kv, 5, 5)
	s.Require().NoError(err)
	s.Require().Len(root5, 32)
	s.Require().Equal(uint64(5), s.keeper.TreeCursorForTest())

	// Add 3 more leaves.
	for i := uint64(6); i <= 8; i++ {
		_, err := s.keeper.AppendCommitment(kv, fpLE(i))
		s.Require().NoError(err)
	}

	// Second call: should only append leaves [5,8).
	root8, err := s.keeper.ComputeTreeRoot(kv, 8, 8)
	s.Require().NoError(err)
	s.Require().Len(root8, 32)
	s.Require().NotEqual(root5, root8)
	s.Require().Equal(uint64(8), s.keeper.TreeCursorForTest())
}

// TestComputeTreeRoot_RollbackRebuild verifies that a cursor > nextIndex
// (rollback scenario) triggers a full rebuild and produces the correct root.
func (s *KeeperTestSuite) TestComputeTreeRoot_RollbackRebuild() {
	s.SetupTest()
	kv := s.keeper.OpenKVStore(s.ctx)

	// Append 5 leaves and compute root.
	for i := uint64(1); i <= 5; i++ {
		_, err := s.keeper.AppendCommitment(kv, fpLE(i))
		s.Require().NoError(err)
	}
	root5, err := s.keeper.ComputeTreeRoot(kv, 5, 5)
	s.Require().NoError(err)
	s.Require().Equal(uint64(5), s.keeper.TreeCursorForTest())

	// Simulate rollback: nextIndex goes back to 3 (cursor > nextIndex).
	root3Rollback, err := s.keeper.ComputeTreeRoot(kv, 3, 3)
	s.Require().NoError(err)
	s.Require().Len(root3Rollback, 32)
	s.Require().NotEqual(root5, root3Rollback)
	// After rollback, cursor is reset to 3.
	s.Require().Equal(uint64(3), s.keeper.TreeCursorForTest())

	// root3Rollback must match a fresh rebuild from 3 leaves.
	freshKeeper := keeper.NewKeeper(
		s.keeper.StoreServiceForTest(),
		"zvote1authority",
		log.NewNopLogger(),
		nil,
	)
	freshRoot3, err := freshKeeper.ComputeTreeRoot(kv, 3, 3)
	s.Require().NoError(err)
	s.Require().Equal(root3Rollback, freshRoot3, "post-rollback root must match fresh rebuild")
}

// TestComputeTreeRoot_RollbackWithStaleShards verifies that a rollback
// following a delta-append (which writes shard data to KV) produces the
// correct root. This is the harder scenario: without the TruncateKVData call
// added to the rollback path, ShardTree would read the stale pre-rollback
// shards and place newly-appended leaves at wrong positions, producing an
// incorrect root.
//
// Sequence:
//  1. ComputeTreeRoot(kv, 1, 1) — cold start, treeCursor=1, no delta,
//     no shard data written to KV.
//  2. ComputeTreeRoot(kv, 5, 5) — delta-append indices 1-4; Rust writes
//     shard data for those 4 leaves to KV.  treeCursor=5.
//  3. ComputeTreeRoot(kv, 3, 3) — rollback: treeCursor(5) > nextIndex(3).
//     TruncateKVData clears the stale shards, then AppendFromKV(0,3) builds
//     a fresh 3-leaf tree.
//
// The rollback root is compared against an ephemeral NewEphemeralTreeHandle
// for the same 3 leaves — this reference is completely independent of KV state.
func (s *KeeperTestSuite) TestComputeTreeRoot_RollbackWithStaleShards() {
	s.SetupTest()
	kv := s.keeper.OpenKVStore(s.ctx)

	// Append 5 leaves up-front (indices 0-4).
	for i := uint64(1); i <= 5; i++ {
		_, err := s.keeper.AppendCommitment(kv, fpLE(i))
		s.Require().NoError(err)
	}

	// Step 1: cold start at nextIndex=1. treeCursor becomes 1; no delta is
	// performed so no shard data is written.
	_, err := s.keeper.ComputeTreeRoot(kv, 1, 1)
	s.Require().NoError(err)
	s.Require().Equal(uint64(1), s.keeper.TreeCursorForTest())

	// Step 2: delta-append indices 1-4. Rust writes shard data for 4 leaves
	// to KV. After this, treeCursor=5 and KV contains stale shard data.
	_, err = s.keeper.ComputeTreeRoot(kv, 5, 5)
	s.Require().NoError(err)
	s.Require().Equal(uint64(5), s.keeper.TreeCursorForTest())

	// Step 3: rollback — treeCursor(5) > nextIndex(3). TruncateKVData removes
	// the stale shards before AppendFromKV(0,3) rebuilds the tree.
	rollbackRoot, err := s.keeper.ComputeTreeRoot(kv, 3, 3)
	s.Require().NoError(err)
	s.Require().Len(rollbackRoot, 32)
	s.Require().Equal(uint64(3), s.keeper.TreeCursorForTest())

	// The reference root is built by an ephemeral in-memory tree from the same
	// 3 leaves (fpLE(1), fpLE(2), fpLE(3)). This is completely independent of
	// KV state and serves as ground truth.
	refHandle, err := votetree.NewEphemeralTreeHandle()
	s.Require().NoError(err)
	defer refHandle.Close()
	s.Require().NoError(refHandle.AppendBatch([][]byte{fpLE(1), fpLE(2), fpLE(3)}))
	s.Require().NoError(refHandle.Checkpoint(1))
	refRoot, err := refHandle.Root()
	s.Require().NoError(err)
	s.Require().Equal(refRoot, rollbackRoot,
		"post-rollback root must match ephemeral reference for the same 3 leaves")
}

// TestComputeTreeRoot_ColdStartNoNewLeaves verifies that a cold-start keeper
// returns the correct root for a block that adds no new leaves. This exercises
// the latest_checkpoint restoration path: on cold start TreeServer restores
// latest_checkpoint from KV so Root() is correct even when appended=false
// (no new checkpoint is created for the second call).
func (s *KeeperTestSuite) TestComputeTreeRoot_ColdStartNoNewLeaves() {
	s.SetupTest()
	kv := s.keeper.OpenKVStore(s.ctx)

	// Append 4 leaves and compute root at height 10.
	for i := uint64(1); i <= 4; i++ {
		_, err := s.keeper.AppendCommitment(kv, fpLE(i))
		s.Require().NoError(err)
	}
	root1, err := s.keeper.ComputeTreeRoot(kv, 4, 10)
	s.Require().NoError(err)
	s.Require().Len(root1, 32)

	// Simulate a node restart: new keeper with the same KV store but a nil
	// tree handle (cold start). No leaves are appended between H1 and H2.
	freshKeeper := keeper.NewKeeper(
		s.keeper.StoreServiceForTest(),
		"zvote1authority",
		log.NewNopLogger(),
		nil,
	)

	// Call at a later block height but with the same nextIndex. The keeper
	// must restore latest_checkpoint from KV and return the correct root
	// without attempting a duplicate checkpoint (which would panic).
	root2, err := freshKeeper.ComputeTreeRoot(kv, 4, 20)
	s.Require().NoError(err)
	s.Require().Equal(root1, root2, "cold-start root must match original root when no new leaves added")
}

// TestComputeTreeRoot_IdempotentSameBlock verifies that calling ComputeTreeRoot
// twice at the same block height (same nextIndex) returns the same root.
func (s *KeeperTestSuite) TestComputeTreeRoot_IdempotentSameBlock() {
	s.SetupTest()
	kv := s.keeper.OpenKVStore(s.ctx)

	_, err := s.keeper.AppendCommitment(kv, fpLE(42))
	s.Require().NoError(err)

	root1, err := s.keeper.ComputeTreeRoot(kv, 1, 10)
	s.Require().NoError(err)

	// Call again at same height, same nextIndex — should return same root.
	root2, err := s.keeper.ComputeTreeRoot(kv, 1, 10)
	s.Require().NoError(err)
	s.Require().Equal(root1, root2, "same block, same leaves must produce same root")
}

// ---------------------------------------------------------------------------
// BlockLeafIndex
// ---------------------------------------------------------------------------

func (s *KeeperTestSuite) TestBlockLeafIndex() {
	type writeEntry struct {
		height, start, count uint64
	}
	type readCheck struct {
		height    uint64
		wantStart uint64
		wantCount uint64
		wantFound bool
	}

	tests := []struct {
		name   string
		writes []writeEntry
		reads  []readCheck
	}{
		{
			name:   "basic round-trip",
			writes: []writeEntry{{10, 0, 5}},
			reads:  []readCheck{{10, 0, 5, true}},
		},
		{
			name:   "not found",
			writes: nil,
			reads:  []readCheck{{999, 0, 0, false}},
		},
		{
			name: "multiple heights",
			writes: []writeEntry{
				{5, 0, 2},
				{10, 2, 3},
				{15, 5, 1},
			},
			reads: []readCheck{
				{5, 0, 2, true},
				{10, 2, 3, true},
				{15, 5, 1, true},
			},
		},
		{
			name: "overwrite same height",
			writes: []writeEntry{
				{10, 0, 2},
				{10, 0, 7}, // second write overwrites
			},
			reads: []readCheck{{10, 0, 7, true}},
		},
		{
			name:   "large uint64 values",
			writes: []writeEntry{{999999, 1<<48 + 42, 1<<32 + 7}},
			reads:  []readCheck{{999999, 1<<48 + 42, 1<<32 + 7, true}},
		},
	}

	for _, tc := range tests {
		s.Run(tc.name, func() {
			s.SetupTest()
			kv := s.keeper.OpenKVStore(s.ctx)

			for _, w := range tc.writes {
				s.Require().NoError(s.keeper.SetBlockLeafIndex(kv, w.height, w.start, w.count))
			}
			for _, r := range tc.reads {
				start, count, found, err := s.keeper.GetBlockLeafIndex(kv, r.height)
				s.Require().NoError(err)
				s.Require().Equal(r.wantFound, found, "found mismatch for height %d", r.height)
				if r.wantFound {
					s.Require().Equal(r.wantStart, start, "start mismatch for height %d", r.height)
					s.Require().Equal(r.wantCount, count, "count mismatch for height %d", r.height)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// GetCommitmentLeaves
// ---------------------------------------------------------------------------

func (s *KeeperTestSuite) TestGetCommitmentLeaves() {
	// blockSetup describes leaves to append and the index entry to write.
	type blockSetup struct {
		height   uint64
		start    uint64
		leafVals []byte // each byte becomes an fpLeaf
	}
	// wantBlock describes expected output for one block.
	type wantBlock struct {
		height   uint64
		start    uint64
		leafVals []byte
	}

	tests := []struct {
		name       string
		blocks     []blockSetup
		fromHeight uint64
		toHeight   uint64
		want       []wantBlock
	}{
		{
			name:       "empty store",
			fromHeight: 0,
			toHeight:   100,
			want:       nil,
		},
		{
			name: "single block",
			blocks: []blockSetup{
				{height: 5, start: 0, leafVals: []byte{0x10, 0x20}},
			},
			fromHeight: 1,
			toHeight:   10,
			want: []wantBlock{
				{height: 5, start: 0, leafVals: []byte{0x10, 0x20}},
			},
		},
		{
			name: "multiple blocks full range",
			blocks: []blockSetup{
				{height: 5, start: 0, leafVals: []byte{0x01, 0x02}},
				{height: 8, start: 2, leafVals: []byte{0x03}},
				{height: 12, start: 3, leafVals: []byte{0x04, 0x05, 0x06}},
			},
			fromHeight: 0,
			toHeight:   20,
			want: []wantBlock{
				{height: 5, start: 0, leafVals: []byte{0x01, 0x02}},
				{height: 8, start: 2, leafVals: []byte{0x03}},
				{height: 12, start: 3, leafVals: []byte{0x04, 0x05, 0x06}},
			},
		},
		{
			name: "subset range filters correctly",
			blocks: []blockSetup{
				{height: 5, start: 0, leafVals: []byte{0x01}},
				{height: 10, start: 1, leafVals: []byte{0x02}},
				{height: 15, start: 2, leafVals: []byte{0x03}},
			},
			fromHeight: 7,
			toHeight:   12,
			want: []wantBlock{
				{height: 10, start: 1, leafVals: []byte{0x02}},
			},
		},
		{
			name: "exact height (from == to)",
			blocks: []blockSetup{
				{height: 7, start: 0, leafVals: []byte{0x42}},
			},
			fromHeight: 7,
			toHeight:   7,
			want: []wantBlock{
				{height: 7, start: 0, leafVals: []byte{0x42}},
			},
		},
		{
			name: "leaf content byte-exact",
			blocks: []blockSetup{
				{height: 1, start: 0, leafVals: []byte{0xAA, 0xBB, 0xCC}},
			},
			fromHeight: 1,
			toHeight:   1,
			want: []wantBlock{
				{height: 1, start: 0, leafVals: []byte{0xAA, 0xBB, 0xCC}},
			},
		},
	}

	for _, tc := range tests {
		s.Run(tc.name, func() {
			s.SetupTest()
			kv := s.keeper.OpenKVStore(s.ctx)

			// Seed: append leaves and write block-index entries.
			for _, b := range tc.blocks {
				for _, v := range b.leafVals {
					_, err := s.keeper.AppendCommitment(kv, fpLeaf(v))
					s.Require().NoError(err)
				}
				s.Require().NoError(s.keeper.SetBlockLeafIndex(kv, b.height, b.start, uint64(len(b.leafVals))))
			}

			got, err := s.keeper.GetCommitmentLeaves(kv, tc.fromHeight, tc.toHeight)
			s.Require().NoError(err)
			s.Require().Len(got, len(tc.want))

			for i, w := range tc.want {
				s.Require().Equal(w.height, got[i].Height, "block %d height", i)
				s.Require().Equal(w.start, got[i].StartIndex, "block %d start_index", i)
				s.Require().Len(got[i].Leaves, len(w.leafVals), "block %d leaf count", i)
				for j, v := range w.leafVals {
					s.Require().Equal(fpLeaf(v), got[i].Leaves[j], "block %d leaf %d", i, j)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Metadata accessors
// ---------------------------------------------------------------------------

func (s *KeeperTestSuite) TestGetAuthority() {
	s.SetupTest()
	s.Require().Equal("zvote1authority", s.keeper.GetAuthority())
}

func (s *KeeperTestSuite) TestLogger() {
	s.SetupTest()
	s.Require().NotNil(s.keeper.Logger())
}
