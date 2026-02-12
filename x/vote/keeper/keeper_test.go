package keeper_test

import (
	"bytes"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"

	"cosmossdk.io/log"
	storetypes "cosmossdk.io/store/types"

	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/cosmos/cosmos-sdk/testutil"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/z-cale/zally/x/vote/keeper"
	"github.com/z-cale/zally/x/vote/types"
)

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
	keeper keeper.Keeper
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
	s.keeper = keeper.NewKeeper(storeService, "zvote1authority", log.NewNopLogger())
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
		bytes.Repeat([]byte{0xA1}, 32),
		bytes.Repeat([]byte{0xA2}, 32),
		bytes.Repeat([]byte{0xA3}, 32),
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

	idx, err := s.keeper.AppendCommitment(kv, bytes.Repeat([]byte{0xFF}, 32))
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
// Tally accumulator
// ---------------------------------------------------------------------------

func (s *KeeperTestSuite) TestTally_DefaultZero() {
	s.SetupTest()
	kv := s.keeper.OpenKVStore(s.ctx)

	amount, err := s.keeper.GetTally(kv, testRoundID, 1, 1)
	s.Require().NoError(err)
	s.Require().Equal(uint64(0), amount)
}

func (s *KeeperTestSuite) TestTally_AddAndAccumulate() {
	tests := []struct {
		name     string
		adds     []uint64
		expected uint64
	}{
		{
			name:     "single add",
			adds:     []uint64{500},
			expected: 500,
		},
		{
			name:     "three adds accumulate",
			adds:     []uint64{100, 250, 150},
			expected: 500,
		},
		{
			name:     "add zero is no-op",
			adds:     []uint64{300, 0, 0, 200},
			expected: 500,
		},
	}

	for _, tc := range tests {
		s.Run(tc.name, func() {
			s.SetupTest()
			kv := s.keeper.OpenKVStore(s.ctx)

			for _, a := range tc.adds {
				s.Require().NoError(s.keeper.AddToTally(kv, testRoundID, 1, 1, a))
			}

			got, err := s.keeper.GetTally(kv, testRoundID, 1, 1)
			s.Require().NoError(err)
			s.Require().Equal(tc.expected, got)
		})
	}
}

func (s *KeeperTestSuite) TestTally_IndependentTuples() {
	s.SetupTest()
	kv := s.keeper.OpenKVStore(s.ctx)

	roundA := bytes.Repeat([]byte{0x0A}, 32)
	roundB := bytes.Repeat([]byte{0x0B}, 32)

	// (roundA, proposal=1, decision=0)
	s.Require().NoError(s.keeper.AddToTally(kv, roundA, 1, 0, 100))
	// (roundA, proposal=1, decision=1)
	s.Require().NoError(s.keeper.AddToTally(kv, roundA, 1, 1, 200))
	// (roundA, proposal=2, decision=0)
	s.Require().NoError(s.keeper.AddToTally(kv, roundA, 2, 0, 300))
	// (roundB, proposal=1, decision=0)
	s.Require().NoError(s.keeper.AddToTally(kv, roundB, 1, 0, 400))

	got, err := s.keeper.GetTally(kv, roundA, 1, 0)
	s.Require().NoError(err)
	s.Require().Equal(uint64(100), got)

	got, err = s.keeper.GetTally(kv, roundA, 1, 1)
	s.Require().NoError(err)
	s.Require().Equal(uint64(200), got)

	got, err = s.keeper.GetTally(kv, roundA, 2, 0)
	s.Require().NoError(err)
	s.Require().Equal(uint64(300), got)

	got, err = s.keeper.GetTally(kv, roundB, 1, 0)
	s.Require().NoError(err)
	s.Require().Equal(uint64(400), got)

	// Unset tuple returns zero.
	got, err = s.keeper.GetTally(kv, roundB, 2, 0)
	s.Require().NoError(err)
	s.Require().Equal(uint64(0), got)
}

// ---------------------------------------------------------------------------
// Validation helpers (ValidateRoundActive, CheckNullifiersUnique)
// ---------------------------------------------------------------------------

func (s *KeeperTestSuite) TestValidateRoundActive() {
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
			err := s.keeper.ValidateRoundActive(s.ctx, tc.roundID)
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
			leaves:    [][]byte{bytes.Repeat([]byte{0xA1}, 32)},
			expectLen: 32,
		},
		{
			name: "two leaves produce 32-byte root",
			leaves: [][]byte{
				bytes.Repeat([]byte{0xA1}, 32),
				bytes.Repeat([]byte{0xA2}, 32),
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

			root, err := s.keeper.ComputeTreeRoot(kv, uint64(len(tc.leaves)))
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

	_, err := s.keeper.AppendCommitment(kv, bytes.Repeat([]byte{0x01}, 32))
	s.Require().NoError(err)

	root1, err := s.keeper.ComputeTreeRoot(kv, 1)
	s.Require().NoError(err)

	// Same state produces same root.
	root1Again, err := s.keeper.ComputeTreeRoot(kv, 1)
	s.Require().NoError(err)
	s.Require().Equal(root1, root1Again)

	// Add another leaf — root changes.
	_, err = s.keeper.AppendCommitment(kv, bytes.Repeat([]byte{0x02}, 32))
	s.Require().NoError(err)

	root2, err := s.keeper.ComputeTreeRoot(kv, 2)
	s.Require().NoError(err)
	s.Require().NotEqual(root1, root2)
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
