package keeper_test

import (
	"bytes"

	"cosmossdk.io/log"

	svtest "github.com/valargroup/shielded-vote/testutil"
	"github.com/valargroup/shielded-vote/x/vote/keeper"
	"github.com/valargroup/shielded-vote/x/vote/types"
)

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
		name       string
		height     uint64
		root       []byte
		lookupH    uint64
		expectRoot []byte
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
// Voting validation
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
		name      string
		leaves    [][]byte
		expectNil bool
		expectLen int
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

	// Simulate what EndBlocker does: persist state.Height so a freshKeeper
	// takes the O(1) restart path instead of the O(N) first-boot replay.
	treeState, err := s.keeper.GetCommitmentTreeState(kv)
	s.Require().NoError(err)
	treeState.Height = 3
	s.Require().NoError(s.keeper.SetCommitmentTreeState(kv, treeState))

	// root3 must match what a cold-start restart produces.
	freshKeeper := keeper.NewKeeper(
		s.keeper.StoreServiceForTest(),
		svtest.TestAuthority,
		log.NewNopLogger(),
		nil,
		nil,
	)
	freshRoot, err := freshKeeper.ComputeTreeRoot(kv, 3, 3)
	s.Require().NoError(err)
	s.Require().Equal(root3, freshRoot, "restart root must match incremental root")
}

// TestComputeTreeRoot_DeltaAppend verifies that calling ComputeTreeRoot with a
// new nextIndex only reads and appends the delta leaves.
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
	s.Require().Equal(uint64(5), s.keeper.TreeSizeForTest())

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
	s.Require().Equal(uint64(8), s.keeper.TreeSizeForTest())
}

// TestComputeTreeRoot_ColdStartNoNewLeaves verifies that a cold-start keeper
// returns the correct root for a block that adds no new leaves. With the
// O(N) replay path, the fresh keeper replays all existing leaves via
// AppendFromKV and checkpoints them, producing the same root as the original.
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

	// Simulate what EndBlocker does: persist state.Height so a freshKeeper
	// takes the O(1) restart path (lazy-loads shard data from KV) rather than
	// the O(N) first-boot replay, which would conflict with existing shard data.
	treeState, err := s.keeper.GetCommitmentTreeState(kv)
	s.Require().NoError(err)
	treeState.Height = 10
	s.Require().NoError(s.keeper.SetCommitmentTreeState(kv, treeState))

	// Simulate a node restart: new keeper with the same KV store but a nil
	// tree handle. The fresh keeper takes the O(1) restart path: it creates a
	// handle at nextIndex=4, reads max_checkpoint=10 from KV, and returns the
	// root at checkpoint 10 without replaying any leaves.
	freshKeeper := keeper.NewKeeper(
		s.keeper.StoreServiceForTest(),
		svtest.TestAuthority,
		log.NewNopLogger(),
		nil,
		nil,
	)

	// Call at a later block height with the same nextIndex. Since no new leaves
	// were added, needsCheckpoint=false and root() returns the existing root at
	// checkpoint 10. Root must equal root1.
	root2, err := freshKeeper.ComputeTreeRoot(kv, 4, 20)
	s.Require().NoError(err)
	s.Require().Equal(root1, root2, "restart root must match original root when no new leaves added")
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
