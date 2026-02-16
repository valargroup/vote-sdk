package app_test

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
	"golang.org/x/crypto/blake2b"

	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/z-cale/zally/crypto/elgamal"
	"github.com/z-cale/zally/testutil"
	"github.com/z-cale/zally/x/vote/types"
)

// ---------------------------------------------------------------------------
// Integration test suite
// ---------------------------------------------------------------------------

// ABCIIntegrationSuite tests the complete ABCI pipeline:
// raw tx bytes → CustomTxDecoder → DualAnteHandler → MsgServer → EndBlocker → state
//
// Uses real depinject wiring, real IAVL store, real module manager.
// No CometBFT process or network — just BaseApp method calls.
type ABCIIntegrationSuite struct {
	suite.Suite
	app *testutil.TestApp
}

func TestABCIIntegration(t *testing.T) {
	suite.Run(t, new(ABCIIntegrationSuite))
}

func (s *ABCIIntegrationSuite) SetupTest() {
	s.app = testutil.SetupTestApp(s.T())
}

// queryCtx returns an sdk.Context for reading committed state.
// Uses NewUncachedContext because after Commit() the finalizeBlockState is nil.
func (s *ABCIIntegrationSuite) queryCtx() sdk.Context {
	return s.app.NewUncachedContext(false, cmtproto.Header{Height: s.app.Height})
}

// ---------------------------------------------------------------------------
// 6.2.1: Full Voting Lifecycle (Happy Path)
// ---------------------------------------------------------------------------

func (s *ABCIIntegrationSuite) TestFullVotingLifecycle() {
	// Step 1: Create voting session.
	setupMsg := testutil.ValidCreateVotingSessionAt(s.app.Time)
	setupTx := testutil.MustEncodeVoteTx(setupMsg)

	result := s.app.DeliverVoteTx(setupTx)
	s.Require().Equal(uint32(0), result.Code, "CreateVotingSession should succeed, got: %s", result.Log)

	// Derive the round ID and verify it was stored.
	roundID := computeRoundID(setupMsg)
	ctx := s.queryCtx()
	kvStore := s.app.VoteKeeper().OpenKVStore(ctx)

	round, err := s.app.VoteKeeper().GetVoteRound(kvStore, roundID)
	s.Require().NoError(err)
	s.Require().Equal(setupMsg.Creator, round.Creator)
	s.Require().Equal(setupMsg.SnapshotHeight, round.SnapshotHeight)

	// Step 2: Delegate vote.
	delegationMsg := testutil.ValidDelegation(roundID, 0x10)
	delegationTx := testutil.MustEncodeVoteTx(delegationMsg)

	result = s.app.DeliverVoteTx(delegationTx)
	s.Require().Equal(uint32(0), result.Code, "DelegateVote should succeed, got: %s", result.Log)

	// Verify nullifiers recorded.
	ctx = s.queryCtx()
	kvStore = s.app.VoteKeeper().OpenKVStore(ctx)
	for _, nf := range delegationMsg.GovNullifiers {
		has, err := s.app.VoteKeeper().HasNullifier(kvStore, types.NullifierTypeGov, roundID, nf)
		s.Require().NoError(err)
		s.Require().True(has, "gov nullifier should be recorded after delegation")
	}

	// Verify commitment tree advanced by 1 (only van_cmx; cmx_new is not in the tree).
	treeState, err := s.app.VoteKeeper().GetCommitmentTreeState(kvStore)
	s.Require().NoError(err)
	s.Require().Equal(uint64(1), treeState.NextIndex)

	// Step 3: EndBlocker already ran during the delegation's FinalizeBlock,
	// computing the tree root at that block height.
	anchorHeight := uint64(s.app.Height)

	ctx = s.queryCtx()
	kvStore = s.app.VoteKeeper().OpenKVStore(ctx)
	root, err := s.app.VoteKeeper().GetCommitmentRootAtHeight(kvStore, anchorHeight)
	s.Require().NoError(err)
	s.Require().NotNil(root, "EndBlocker should have computed a tree root at height %d", anchorHeight)
	s.Require().Len(root, 32)

	// Step 4: Cast vote using the anchor height from step 3.
	castVoteMsg := testutil.ValidCastVote(roundID, anchorHeight, 0x30)
	castVoteTx := testutil.MustEncodeVoteTx(castVoteMsg)

	result = s.app.DeliverVoteTx(castVoteTx)
	s.Require().Equal(uint32(0), result.Code, "CastVote should succeed, got: %s", result.Log)

	// Verify vote-authority-note nullifier recorded.
	ctx = s.queryCtx()
	kvStore = s.app.VoteKeeper().OpenKVStore(ctx)
	has, err := s.app.VoteKeeper().HasNullifier(kvStore, types.NullifierTypeVoteAuthorityNote, roundID, castVoteMsg.VanNullifier)
	s.Require().NoError(err)
	s.Require().True(has, "vote-authority-note nullifier should be recorded")

	// Tree advanced by 2 more (vote_authority_note_new + vote_commitment): 1 + 2 = 3.
	treeState, err = s.app.VoteKeeper().GetCommitmentTreeState(kvStore)
	s.Require().NoError(err)
	s.Require().Equal(uint64(3), treeState.NextIndex)

	// EndBlocker already computed a new root for this block (tree grew).
	revealAnchor := uint64(s.app.Height)

	// Step 5: Reveal share.
	revealMsg := testutil.ValidRevealShare(roundID, revealAnchor, 0x50)
	revealTx := testutil.MustEncodeVoteTx(revealMsg)

	result = s.app.DeliverVoteTx(revealTx)
	s.Require().Equal(uint32(0), result.Code, "RevealShare should succeed, got: %s", result.Log)

	// Step 6: Verify tally.
	ctx = s.queryCtx()
	kvStore = s.app.VoteKeeper().OpenKVStore(ctx)
	tally, err := s.app.VoteKeeper().GetTally(kvStore, roundID, revealMsg.ProposalId, revealMsg.VoteDecision)
	s.Require().NoError(err)
	s.Require().Equal(revealMsg.EncShare, tally)

	// Verify share nullifier recorded.
	has, err = s.app.VoteKeeper().HasNullifier(kvStore, types.NullifierTypeShare, roundID, revealMsg.ShareNullifier)
	s.Require().NoError(err)
	s.Require().True(has, "share nullifier should be recorded")
}

// ---------------------------------------------------------------------------
// 6.2.2: Nullifier Double-Spend Prevention
// ---------------------------------------------------------------------------

func (s *ABCIIntegrationSuite) TestNullifierDoubleSpend() {
	// Create voting session.
	setupMsg := testutil.ValidCreateVotingSessionAt(s.app.Time)
	s.app.DeliverVoteTx(testutil.MustEncodeVoteTx(setupMsg))
	roundID := computeRoundID(setupMsg)

	// First delegation succeeds.
	delegation1 := testutil.ValidDelegation(roundID, 0x10)
	result := s.app.DeliverVoteTx(testutil.MustEncodeVoteTx(delegation1))
	s.Require().Equal(uint32(0), result.Code, "first delegation should succeed")

	// Second delegation with overlapping nullifier fails.
	delegation2 := testutil.ValidDelegation(roundID, 0x10) // same seed = same nullifiers
	result = s.app.DeliverVoteTx(testutil.MustEncodeVoteTx(delegation2))
	s.Require().NotEqual(uint32(0), result.Code, "duplicate nullifier should be rejected")
	s.Require().Contains(result.Log, "nullifier already spent")
}

// ---------------------------------------------------------------------------
// 6.2.3: CheckTx vs RecheckTx
// ---------------------------------------------------------------------------

func (s *ABCIIntegrationSuite) TestCheckTxVsRecheckTx() {
	// Create voting session.
	setupMsg := testutil.ValidCreateVotingSessionAt(s.app.Time)
	s.app.DeliverVoteTx(testutil.MustEncodeVoteTx(setupMsg))
	roundID := computeRoundID(setupMsg)

	// CheckTx (New) for a delegation should succeed.
	delegation := testutil.ValidDelegation(roundID, 0x20)
	delegationTx := testutil.MustEncodeVoteTx(delegation)

	checkResp := s.app.CheckTxSync(delegationTx)
	s.Require().Equal(uint32(0), checkResp.Code, "CheckTx should pass for fresh delegation, got: %s", checkResp.Log)

	// Deliver the delegation (consumes nullifiers).
	result := s.app.DeliverVoteTx(delegationTx)
	s.Require().Equal(uint32(0), result.Code, "deliver should succeed")

	// RecheckTx for the same delegation should now fail (nullifiers consumed).
	recheckResp := s.app.RecheckTxSync(delegationTx)
	s.Require().NotEqual(uint32(0), recheckResp.Code, "RecheckTx should fail for consumed nullifiers")
	s.Require().Contains(recheckResp.Log, "nullifier already spent")
}

// ---------------------------------------------------------------------------
// 6.2.4: Commitment Tree Anchor Validation
// ---------------------------------------------------------------------------

func (s *ABCIIntegrationSuite) TestCommitmentTreeAnchorValidation() {
	// Create voting session and delegate vote.
	setupMsg := testutil.ValidCreateVotingSessionAt(s.app.Time)
	s.app.DeliverVoteTx(testutil.MustEncodeVoteTx(setupMsg))
	roundID := computeRoundID(setupMsg)

	delegation := testutil.ValidDelegation(roundID, 0x10)
	s.app.DeliverVoteTx(testutil.MustEncodeVoteTx(delegation))

	// EndBlocker already ran during the delegation's FinalizeBlock.
	validAnchor := uint64(s.app.Height)

	// Cast vote with valid anchor should succeed.
	castVote := testutil.ValidCastVote(roundID, validAnchor, 0x40)
	result := s.app.DeliverVoteTx(testutil.MustEncodeVoteTx(castVote))
	s.Require().Equal(uint32(0), result.Code, "valid anchor should succeed, got: %s", result.Log)

	// Cast vote with non-existent anchor height should fail.
	badAnchor := validAnchor + 999
	badCastVote := testutil.ValidCastVote(roundID, badAnchor, 0x60)
	result = s.app.DeliverVoteTx(testutil.MustEncodeVoteTx(badCastVote))
	s.Require().NotEqual(uint32(0), result.Code, "invalid anchor should fail")
	s.Require().Contains(result.Log, "invalid commitment tree anchor height")
}

// ---------------------------------------------------------------------------
// 6.2.5: Expired Round Rejection
// ---------------------------------------------------------------------------

func (s *ABCIIntegrationSuite) TestExpiredRoundRejection() {
	// Create a session that is already expired relative to block time.
	expiredMsg := testutil.ExpiredCreateVotingSessionAt(s.app.Time)
	s.app.DeliverVoteTx(testutil.MustEncodeVoteTx(expiredMsg))
	expiredRoundID := computeRoundID(expiredMsg)

	// Delegation against the expired round should fail.
	delegation := testutil.ValidDelegation(expiredRoundID, 0x70)
	result := s.app.DeliverVoteTx(testutil.MustEncodeVoteTx(delegation))
	s.Require().NotEqual(uint32(0), result.Code, "expired round should reject delegation")
	s.Require().Contains(result.Log, "vote round is not active")
}

// ---------------------------------------------------------------------------
// 6.2.6: Malformed Transactions
// ---------------------------------------------------------------------------

func (s *ABCIIntegrationSuite) TestMalformedTransactions() {
	tests := []struct {
		name    string
		txBytes []byte
	}{
		{
			name:    "empty bytes",
			txBytes: []byte{},
		},
		{
			name:    "single byte (tag only)",
			txBytes: []byte{0x01},
		},
		{
			name:    "valid tag with corrupted protobuf",
			txBytes: append([]byte{0x02}, []byte{0xFF, 0xFF, 0xFF}...),
		},
		{
			name:    "invalid tag with payload",
			txBytes: append([]byte{0xFF}, []byte{0x00, 0x01, 0x02}...),
		},
	}

	for _, tc := range tests {
		s.Run(tc.name, func() {
			// These should not panic — they should return a non-zero error code.
			result := s.app.DeliverVoteTx(tc.txBytes)
			s.Require().NotEqual(uint32(0), result.Code, "malformed tx should fail: %s", tc.name)
		})
	}
}

func (s *ABCIIntegrationSuite) TestMalformedEmptyRequiredFields() {
	// Valid protobuf structure but with empty required fields → ValidateBasic error.
	msg := &types.MsgDelegateVote{
		// All fields zero/empty — should fail ValidateBasic.
	}
	txBytes := testutil.MustEncodeVoteTx(msg)
	result := s.app.DeliverVoteTx(txBytes)
	s.Require().NotEqual(uint32(0), result.Code, "empty fields should fail ValidateBasic")
}

// ---------------------------------------------------------------------------
// 6.2.7: Concurrent Submissions in Same Block
// ---------------------------------------------------------------------------

func (s *ABCIIntegrationSuite) TestConcurrentSubmissionsInSameBlock() {
	// Create voting session.
	setupMsg := testutil.ValidCreateVotingSessionAt(s.app.Time)
	s.app.DeliverVoteTx(testutil.MustEncodeVoteTx(setupMsg))
	roundID := computeRoundID(setupMsg)

	// Submit 5 delegations with unique nullifiers in the same block.
	var txs [][]byte
	for i := byte(0); i < 5; i++ {
		seed := byte(0xA0) + i*2 // non-overlapping nullifier seeds
		delegation := testutil.ValidDelegation(roundID, seed)
		txs = append(txs, testutil.MustEncodeVoteTx(delegation))
	}

	results := s.app.DeliverVoteTxs(txs)
	s.Require().Len(results, 5)
	for i, r := range results {
		s.Require().Equal(uint32(0), r.Code, "delegation %d should succeed, got: %s", i, r.Log)
	}

	// Verify all nullifiers are recorded.
	ctx := s.queryCtx()
	kvStore := s.app.VoteKeeper().OpenKVStore(ctx)
	for i := byte(0); i < 5; i++ {
		seed := byte(0xA0) + i*2
		nf := testutil.MakeNullifier(seed)
		has, err := s.app.VoteKeeper().HasNullifier(kvStore, types.NullifierTypeGov, roundID, nf)
		s.Require().NoError(err)
		s.Require().True(has, "nullifier %d should be recorded", i)
	}

	// Now submit delegations where one has duplicate nullifiers from previous block.
	var txs2 [][]byte
	// Duplicate nullifiers (same seed as first delegation above).
	dupDelegation := testutil.ValidDelegation(roundID, 0xA0)
	txs2 = append(txs2, testutil.MustEncodeVoteTx(dupDelegation))
	// Fresh delegation with unique nullifiers.
	freshDelegation := testutil.ValidDelegation(roundID, 0xF0)
	txs2 = append(txs2, testutil.MustEncodeVoteTx(freshDelegation))

	results2 := s.app.DeliverVoteTxs(txs2)
	s.Require().Len(results2, 2)
	s.Require().NotEqual(uint32(0), results2[0].Code, "duplicate nullifier should fail")
	s.Require().Equal(uint32(0), results2[1].Code, "fresh delegation should succeed, got: %s", results2[1].Log)
}

// ---------------------------------------------------------------------------
// 6.2.8: EndBlocker Tree Root Snapshots
// ---------------------------------------------------------------------------

func (s *ABCIIntegrationSuite) TestEndBlockerTreeRootSnapshots() {
	// Create voting session.
	setupMsg := testutil.ValidCreateVotingSessionAt(s.app.Time)
	s.app.DeliverVoteTx(testutil.MustEncodeVoteTx(setupMsg))
	roundID := computeRoundID(setupMsg)

	// Register first delegation → 2 leaves in tree.
	delegation1 := testutil.ValidDelegation(roundID, 0x10)
	s.app.DeliverVoteTx(testutil.MustEncodeVoteTx(delegation1))

	// The FinalizeBlock for the delegation already ran EndBlocker.
	h1 := uint64(s.app.Height)

	ctx := s.queryCtx()
	kvStore := s.app.VoteKeeper().OpenKVStore(ctx)
	root1, err := s.app.VoteKeeper().GetCommitmentRootAtHeight(kvStore, h1)
	s.Require().NoError(err)
	s.Require().NotNil(root1, "root should be stored at height %d", h1)

	// Register second delegation → 4 leaves total.
	delegation2 := testutil.ValidDelegation(roundID, 0x20)
	s.app.DeliverVoteTx(testutil.MustEncodeVoteTx(delegation2))
	h2 := uint64(s.app.Height)

	ctx = s.queryCtx()
	kvStore = s.app.VoteKeeper().OpenKVStore(ctx)
	root2, err := s.app.VoteKeeper().GetCommitmentRootAtHeight(kvStore, h2)
	s.Require().NoError(err)
	s.Require().NotNil(root2, "root should be stored at height %d", h2)

	// Roots should differ because the tree grew.
	s.Require().NotEqual(root1, root2, "roots should differ after tree growth")

	// Commit an empty block — tree unchanged → no new root stored.
	s.app.NextBlock()
	h3 := uint64(s.app.Height)

	ctx = s.queryCtx()
	kvStore = s.app.VoteKeeper().OpenKVStore(ctx)
	root3, err := s.app.VoteKeeper().GetCommitmentRootAtHeight(kvStore, h3)
	s.Require().NoError(err)
	s.Require().Nil(root3, "no root should be stored at height %d (tree unchanged)", h3)

	// Previous roots still accessible.
	root1Again, err := s.app.VoteKeeper().GetCommitmentRootAtHeight(kvStore, h1)
	s.Require().NoError(err)
	s.Require().Equal(root1, root1Again)
}

// ---------------------------------------------------------------------------
// 6.2.9: EndBlocker Status Transition (ACTIVE → TALLYING)
// ---------------------------------------------------------------------------

func (s *ABCIIntegrationSuite) TestEndBlockerStatusTransition() {
	// Create a session that expires 10 seconds from now.
	voteEndTime := s.app.Time.Add(10 * time.Second)
	setupMsg := &types.MsgCreateVotingSession{
		Creator:           "zvote1admin",
		SnapshotHeight:    100,
		SnapshotBlockhash: bytes.Repeat([]byte{0xAA}, 32),
		ProposalsHash:     bytes.Repeat([]byte{0xBB}, 32),
		VoteEndTime:       uint64(voteEndTime.Unix()),
		NullifierImtRoot:  bytes.Repeat([]byte{0xCC}, 32),
		NcRoot:            bytes.Repeat([]byte{0xDD}, 32),
		EaPk:              bytes.Repeat([]byte{0xEE}, 32),
		VkZkp1:            bytes.Repeat([]byte{0x11}, 64),
		VkZkp2:            bytes.Repeat([]byte{0x22}, 64),
		VkZkp3:            bytes.Repeat([]byte{0x33}, 64),
		Proposals:         testutil.SampleProposals(),
	}
	result := s.app.DeliverVoteTx(testutil.MustEncodeVoteTx(setupMsg))
	s.Require().Equal(uint32(0), result.Code, "create session should succeed, got: %s", result.Log)

	roundID := computeRoundID(setupMsg)

	// Verify round is ACTIVE.
	ctx := s.queryCtx()
	kvStore := s.app.VoteKeeper().OpenKVStore(ctx)
	round, err := s.app.VoteKeeper().GetVoteRound(kvStore, roundID)
	s.Require().NoError(err)
	s.Require().Equal(types.SessionStatus_SESSION_STATUS_ACTIVE, round.Status)

	// Advance past the VoteEndTime — EndBlocker should transition to TALLYING.
	s.app.NextBlockAtTime(voteEndTime.Add(1 * time.Second))

	ctx = s.queryCtx()
	kvStore = s.app.VoteKeeper().OpenKVStore(ctx)
	round, err = s.app.VoteKeeper().GetVoteRound(kvStore, roundID)
	s.Require().NoError(err)
	s.Require().Equal(types.SessionStatus_SESSION_STATUS_TALLYING, round.Status,
		"round should transition to TALLYING after EndBlocker")
}

// ---------------------------------------------------------------------------
// 6.2.10: TALLYING Phase — Both RevealShare and DelegateVote Rejected
// ---------------------------------------------------------------------------

func (s *ABCIIntegrationSuite) TestTallyingPhaseMessageAcceptance() {
	// Create a session expiring 60 seconds from now — enough headroom for
	// several DeliverVoteTx calls (each advances time by 5 seconds).
	voteEndTime := s.app.Time.Add(60 * time.Second)
	setupMsg := &types.MsgCreateVotingSession{
		Creator:           "zvote1admin",
		SnapshotHeight:    200,
		SnapshotBlockhash: bytes.Repeat([]byte{0x1A}, 32),
		ProposalsHash:     bytes.Repeat([]byte{0x1B}, 32),
		VoteEndTime:       uint64(voteEndTime.Unix()),
		NullifierImtRoot:  bytes.Repeat([]byte{0x1C}, 32),
		NcRoot:            bytes.Repeat([]byte{0x1D}, 32),
		EaPk:              bytes.Repeat([]byte{0x1E}, 32),
		VkZkp1:            bytes.Repeat([]byte{0x11}, 64),
		VkZkp2:            bytes.Repeat([]byte{0x22}, 64),
		VkZkp3:            bytes.Repeat([]byte{0x33}, 64),
		Proposals:         testutil.SampleProposals(),
	}
	result := s.app.DeliverVoteTx(testutil.MustEncodeVoteTx(setupMsg))
	s.Require().Equal(uint32(0), result.Code, "create session should succeed")

	roundID := computeRoundID(setupMsg)

	// Delegate while ACTIVE to populate the tree.
	delegation := testutil.ValidDelegation(roundID, 0x10)
	result = s.app.DeliverVoteTx(testutil.MustEncodeVoteTx(delegation))
	s.Require().Equal(uint32(0), result.Code, "delegation during ACTIVE should succeed")

	// Get anchor height for cast vote / reveal share.
	anchorHeight := uint64(s.app.Height)

	// Cast vote while ACTIVE.
	castVote := testutil.ValidCastVote(roundID, anchorHeight, 0x30)
	result = s.app.DeliverVoteTx(testutil.MustEncodeVoteTx(castVote))
	s.Require().Equal(uint32(0), result.Code, "cast vote during ACTIVE should succeed")

	// Need updated anchor for reveal (tree grew again).
	revealAnchor := uint64(s.app.Height)

	// Advance past the VoteEndTime to trigger TALLYING.
	s.app.NextBlockAtTime(voteEndTime.Add(1 * time.Second))

	// Verify round is now TALLYING.
	ctx := s.queryCtx()
	kvStore := s.app.VoteKeeper().OpenKVStore(ctx)
	round, err := s.app.VoteKeeper().GetVoteRound(kvStore, roundID)
	s.Require().NoError(err)
	s.Require().Equal(types.SessionStatus_SESSION_STATUS_TALLYING, round.Status)

	// RevealShare should be accepted during TALLYING (shares are revealed after voting ends).
	revealMsg := testutil.ValidRevealShare(roundID, revealAnchor, 0x50)
	result = s.app.DeliverVoteTx(testutil.MustEncodeVoteTx(revealMsg))
	s.Require().Equal(uint32(0), result.Code, "reveal share during TALLYING should succeed, got: %s", result.Log)

	// DelegateVote should be rejected during TALLYING.
	delegation2 := testutil.ValidDelegation(roundID, 0x60)
	result = s.app.DeliverVoteTx(testutil.MustEncodeVoteTx(delegation2))
	s.Require().NotEqual(uint32(0), result.Code, "delegation during TALLYING should be rejected")
	s.Require().Contains(result.Log, "vote round is not active")
}

// ---------------------------------------------------------------------------
// 6.2.11: EndBlocker Selective Transition (Only Expired Rounds)
// ---------------------------------------------------------------------------

func (s *ABCIIntegrationSuite) TestEndBlockerSelectiveTransition() {
	// Create two sessions: one expiring soon, one in the distant future.
	soonEnd := s.app.Time.Add(10 * time.Second)
	lateEnd := s.app.Time.Add(24 * time.Hour)

	soonMsg := &types.MsgCreateVotingSession{
		Creator:           "zvote1admin",
		SnapshotHeight:    300,
		SnapshotBlockhash: bytes.Repeat([]byte{0x2A}, 32),
		ProposalsHash:     bytes.Repeat([]byte{0x2B}, 32),
		VoteEndTime:       uint64(soonEnd.Unix()),
		NullifierImtRoot:  bytes.Repeat([]byte{0x2C}, 32),
		NcRoot:            bytes.Repeat([]byte{0x2D}, 32),
		EaPk:              bytes.Repeat([]byte{0x2E}, 32),
		VkZkp1:            bytes.Repeat([]byte{0x11}, 64),
		VkZkp2:            bytes.Repeat([]byte{0x22}, 64),
		VkZkp3:            bytes.Repeat([]byte{0x33}, 64),
		Proposals:         testutil.SampleProposals(),
	}
	s.app.DeliverVoteTx(testutil.MustEncodeVoteTx(soonMsg))
	soonRoundID := computeRoundID(soonMsg)

	lateMsg := &types.MsgCreateVotingSession{
		Creator:           "zvote1admin",
		SnapshotHeight:    400,
		SnapshotBlockhash: bytes.Repeat([]byte{0x3A}, 32),
		ProposalsHash:     bytes.Repeat([]byte{0x3B}, 32),
		VoteEndTime:       uint64(lateEnd.Unix()),
		NullifierImtRoot:  bytes.Repeat([]byte{0x3C}, 32),
		NcRoot:            bytes.Repeat([]byte{0x3D}, 32),
		EaPk:              bytes.Repeat([]byte{0x3E}, 32),
		VkZkp1:            bytes.Repeat([]byte{0x11}, 64),
		VkZkp2:            bytes.Repeat([]byte{0x22}, 64),
		VkZkp3:            bytes.Repeat([]byte{0x33}, 64),
		Proposals:         testutil.SampleProposals(),
	}
	s.app.DeliverVoteTx(testutil.MustEncodeVoteTx(lateMsg))
	lateRoundID := computeRoundID(lateMsg)

	// Advance past soonEnd but before lateEnd.
	s.app.NextBlockAtTime(soonEnd.Add(1 * time.Second))

	ctx := s.queryCtx()
	kvStore := s.app.VoteKeeper().OpenKVStore(ctx)

	// Soon-ending round should be TALLYING.
	soonRound, err := s.app.VoteKeeper().GetVoteRound(kvStore, soonRoundID)
	s.Require().NoError(err)
	s.Require().Equal(types.SessionStatus_SESSION_STATUS_TALLYING, soonRound.Status,
		"expired round should transition to TALLYING")

	// Late-ending round should still be ACTIVE.
	lateRound, err := s.app.VoteKeeper().GetVoteRound(kvStore, lateRoundID)
	s.Require().NoError(err)
	s.Require().Equal(types.SessionStatus_SESSION_STATUS_ACTIVE, lateRound.Status,
		"non-expired round should remain ACTIVE")
}

// ---------------------------------------------------------------------------
// 6.2.12: Proposal ID Validation
// ---------------------------------------------------------------------------

func (s *ABCIIntegrationSuite) TestProposalIdValidation() {
	// Create a session with 2 proposals (IDs 0 and 1).
	setupMsg := testutil.ValidCreateVotingSessionAt(s.app.Time)
	result := s.app.DeliverVoteTx(testutil.MustEncodeVoteTx(setupMsg))
	s.Require().Equal(uint32(0), result.Code, "create session should succeed, got: %s", result.Log)

	roundID := computeRoundID(setupMsg)

	// Delegate to populate the tree.
	delegation := testutil.ValidDelegation(roundID, 0x10)
	result = s.app.DeliverVoteTx(testutil.MustEncodeVoteTx(delegation))
	s.Require().Equal(uint32(0), result.Code, "delegation should succeed")

	anchorHeight := uint64(s.app.Height)

	// CastVote with valid proposal_id (0) should succeed.
	castVote := testutil.ValidCastVote(roundID, anchorHeight, 0x30)
	result = s.app.DeliverVoteTx(testutil.MustEncodeVoteTx(castVote))
	s.Require().Equal(uint32(0), result.Code, "cast vote with valid proposal_id should succeed, got: %s", result.Log)

	// Capture the anchor height for the reveal share now, while the tree root
	// exists at this height. The failed bad cast vote below will bump the app
	// height without adding tree leaves, so no root will be stored there.
	revealAnchor := uint64(s.app.Height)

	// CastVote with invalid proposal_id (5) should fail.
	// Recompute sighash after changing ProposalId so ante passes and we hit proposal_id validation.
	badCastVote := testutil.ValidCastVote(roundID, anchorHeight, 0x40)
	badCastVote.ProposalId = 5
	badCastVote.Sighash = types.ComputeCastVoteSighash(badCastVote)
	result = s.app.DeliverVoteTx(testutil.MustEncodeVoteTx(badCastVote))
	s.Require().NotEqual(uint32(0), result.Code, "cast vote with invalid proposal_id should fail")
	s.Require().Contains(result.Log, "invalid proposal ID")

	// RevealShare with valid proposal_id (0) should succeed.
	revealMsg := testutil.ValidRevealShare(roundID, revealAnchor, 0x50)
	result = s.app.DeliverVoteTx(testutil.MustEncodeVoteTx(revealMsg))
	s.Require().Equal(uint32(0), result.Code, "reveal share with valid proposal_id should succeed, got: %s", result.Log)

	// RevealShare with invalid proposal_id (5) should fail.
	badRevealMsg := testutil.ValidRevealShare(roundID, revealAnchor, 0x60)
	badRevealMsg.ProposalId = 5
	result = s.app.DeliverVoteTx(testutil.MustEncodeVoteTx(badRevealMsg))
	s.Require().NotEqual(uint32(0), result.Code, "reveal share with invalid proposal_id should fail")
	s.Require().Contains(result.Log, "invalid proposal ID")
}

// ---------------------------------------------------------------------------
// 6.2.13: SubmitTally — TALLYING → FINALIZED Lifecycle
// ---------------------------------------------------------------------------

func (s *ABCIIntegrationSuite) TestSubmitTallyLifecycle() {
	// Create a session expiring 30 seconds from now.
	voteEndTime := s.app.Time.Add(30 * time.Second)
	setupMsg := &types.MsgCreateVotingSession{
		Creator:           "zvote1admin",
		SnapshotHeight:    500,
		SnapshotBlockhash: bytes.Repeat([]byte{0x4A}, 32),
		ProposalsHash:     bytes.Repeat([]byte{0x4B}, 32),
		VoteEndTime:       uint64(voteEndTime.Unix()),
		NullifierImtRoot:  bytes.Repeat([]byte{0x4C}, 32),
		NcRoot:            bytes.Repeat([]byte{0x4D}, 32),
		EaPk:              bytes.Repeat([]byte{0x4E}, 32),
		VkZkp1:            bytes.Repeat([]byte{0x11}, 64),
		VkZkp2:            bytes.Repeat([]byte{0x22}, 64),
		VkZkp3:            bytes.Repeat([]byte{0x33}, 64),
		Proposals:         testutil.SampleProposals(),
	}
	result := s.app.DeliverVoteTx(testutil.MustEncodeVoteTx(setupMsg))
	s.Require().Equal(uint32(0), result.Code, "create session should succeed, got: %s", result.Log)

	roundID := computeRoundID(setupMsg)

	// Delegate while ACTIVE to populate the tree.
	delegation := testutil.ValidDelegation(roundID, 0x10)
	result = s.app.DeliverVoteTx(testutil.MustEncodeVoteTx(delegation))
	s.Require().Equal(uint32(0), result.Code, "delegation should succeed")

	anchorHeight := uint64(s.app.Height)

	// Cast vote while ACTIVE.
	castVote := testutil.ValidCastVote(roundID, anchorHeight, 0x30)
	result = s.app.DeliverVoteTx(testutil.MustEncodeVoteTx(castVote))
	s.Require().Equal(uint32(0), result.Code, "cast vote should succeed")

	revealAnchor := uint64(s.app.Height)

	// Reveal share while ACTIVE (before TALLYING transition).
	revealMsg := testutil.ValidRevealShare(roundID, revealAnchor, 0x50)
	result = s.app.DeliverVoteTx(testutil.MustEncodeVoteTx(revealMsg))
	s.Require().Equal(uint32(0), result.Code, "reveal share during ACTIVE should succeed, got: %s", result.Log)

	// Advance past VoteEndTime → triggers TALLYING.
	s.app.NextBlockAtTime(voteEndTime.Add(1 * time.Second))

	// Verify round is TALLYING.
	ctx := s.queryCtx()
	kvStore := s.app.VoteKeeper().OpenKVStore(ctx)
	round, err := s.app.VoteKeeper().GetVoteRound(kvStore, roundID)
	s.Require().NoError(err)
	s.Require().Equal(types.SessionStatus_SESSION_STATUS_TALLYING, round.Status)

	// RevealShare should still be accepted during TALLYING.
	revealMsgTallying := testutil.ValidRevealShare(roundID, revealAnchor, 0x60)
	result = s.app.DeliverVoteTx(testutil.MustEncodeVoteTx(revealMsgTallying))
	s.Require().Equal(uint32(0), result.Code, "reveal share during TALLYING should succeed, got: %s", result.Log)

	// Submit tally to finalize (use the genesis validator's operator address).
	submitTallyMsg := testutil.ValidSubmitTally(roundID, s.app.ValidatorOperAddr())
	result = s.app.DeliverVoteTx(testutil.MustEncodeVoteTx(submitTallyMsg))
	s.Require().Equal(uint32(0), result.Code, "submit tally should succeed, got: %s", result.Log)

	// Verify round is now FINALIZED.
	ctx = s.queryCtx()
	kvStore = s.app.VoteKeeper().OpenKVStore(ctx)
	round, err = s.app.VoteKeeper().GetVoteRound(kvStore, roundID)
	s.Require().NoError(err)
	s.Require().Equal(types.SessionStatus_SESSION_STATUS_FINALIZED, round.Status,
		"round should be FINALIZED after SubmitTally")

	// Verify tally accumulator is preserved.
	tally, err := s.app.VoteKeeper().GetTally(kvStore, roundID, revealMsg.ProposalId, revealMsg.VoteDecision)
	s.Require().NoError(err)
	s.Require().Equal(revealMsg.EncShare, tally, "tally should be preserved after finalization")

	// Verify finalized tally results are stored and queryable.
	tallyResults, err := s.app.VoteKeeper().GetAllTallyResults(kvStore, roundID)
	s.Require().NoError(err)
	s.Require().Len(tallyResults, 1)
	s.Require().Equal(uint32(1), tallyResults[0].ProposalId) // 1-indexed; ValidSubmitTally uses proposal 1
	s.Require().Equal(uint32(1), tallyResults[0].VoteDecision)
	// TotalValue in TallyEntry is the EA-claimed plaintext; no longer compared to the encrypted share.
	s.Require().NotZero(tallyResults[0].TotalValue)

	// RevealShare should fail after FINALIZED.
	revealMsg2 := testutil.ValidRevealShare(roundID, revealAnchor, 0x70)
	result = s.app.DeliverVoteTx(testutil.MustEncodeVoteTx(revealMsg2))
	s.Require().NotEqual(uint32(0), result.Code, "reveal share should be rejected after FINALIZED")
	s.Require().Contains(result.Log, "vote round is not active")
}

// ---------------------------------------------------------------------------
// 6.2.14: SubmitTally — Authorization (Non-Proposer Rejected)
// ---------------------------------------------------------------------------

func (s *ABCIIntegrationSuite) TestSubmitTallyNonProposerRejected() {
	// Create a session expiring 10 seconds from now.
	voteEndTime := s.app.Time.Add(10 * time.Second)
	setupMsg := &types.MsgCreateVotingSession{
		Creator:           "zvote1admin",
		SnapshotHeight:    600,
		SnapshotBlockhash: bytes.Repeat([]byte{0x5A}, 32),
		ProposalsHash:     bytes.Repeat([]byte{0x5B}, 32),
		VoteEndTime:       uint64(voteEndTime.Unix()),
		NullifierImtRoot:  bytes.Repeat([]byte{0x5C}, 32),
		NcRoot:            bytes.Repeat([]byte{0x5D}, 32),
		EaPk:              bytes.Repeat([]byte{0x5E}, 32),
		VkZkp1:            bytes.Repeat([]byte{0x11}, 64),
		VkZkp2:            bytes.Repeat([]byte{0x22}, 64),
		VkZkp3:            bytes.Repeat([]byte{0x33}, 64),
		Proposals:         testutil.SampleProposals(),
	}
	result := s.app.DeliverVoteTx(testutil.MustEncodeVoteTx(setupMsg))
	s.Require().Equal(uint32(0), result.Code, "create session should succeed")

	roundID := computeRoundID(setupMsg)

	// Advance past VoteEndTime → TALLYING.
	s.app.NextBlockAtTime(voteEndTime.Add(1 * time.Second))

	// Verify TALLYING.
	ctx := s.queryCtx()
	kvStore := s.app.VoteKeeper().OpenKVStore(ctx)
	round, err := s.app.VoteKeeper().GetVoteRound(kvStore, roundID)
	s.Require().NoError(err)
	s.Require().Equal(types.SessionStatus_SESSION_STATUS_TALLYING, round.Status)

	// Submit tally with a creator that doesn't match the block proposer should fail.
	// Use a valid valoper address that is not the genesis validator.
	fakeValoper := sdk.ValAddress(bytes.Repeat([]byte{0xFF}, 20)).String()
	badTallyMsg := testutil.ValidSubmitTallyWithEntries(roundID, fakeValoper, []*types.TallyEntry{
		{ProposalId: 1, VoteDecision: 0, TotalValue: 0},
	})
	result = s.app.DeliverVoteTx(testutil.MustEncodeVoteTx(badTallyMsg))
	s.Require().NotEqual(uint32(0), result.Code, "submit tally with non-proposer creator should fail")
	s.Require().Contains(result.Log, "does not match block proposer")

	// Submit tally with the block proposer's validator address should succeed.
	goodTallyMsg := testutil.ValidSubmitTallyWithEntries(roundID, s.app.ValidatorOperAddr(), []*types.TallyEntry{
		{ProposalId: 1, VoteDecision: 0, TotalValue: 0},

	})
	result = s.app.DeliverVoteTx(testutil.MustEncodeVoteTx(goodTallyMsg))
	s.Require().Equal(uint32(0), result.Code, "submit tally from block proposer should succeed, got: %s", result.Log)
}

// ---------------------------------------------------------------------------
// 6.2.15: SubmitTally — Cannot Finalize Active Round
// ---------------------------------------------------------------------------

func (s *ABCIIntegrationSuite) TestSubmitTallyRejectsActiveRound() {
	// Create an active session (not expired).
	setupMsg := testutil.ValidCreateVotingSessionAt(s.app.Time)
	result := s.app.DeliverVoteTx(testutil.MustEncodeVoteTx(setupMsg))
	s.Require().Equal(uint32(0), result.Code, "create session should succeed")

	roundID := computeRoundID(setupMsg)

	// Verify round is ACTIVE.
	ctx := s.queryCtx()
	kvStore := s.app.VoteKeeper().OpenKVStore(ctx)
	round, err := s.app.VoteKeeper().GetVoteRound(kvStore, roundID)
	s.Require().NoError(err)
	s.Require().Equal(types.SessionStatus_SESSION_STATUS_ACTIVE, round.Status)

	// Submit tally against ACTIVE round should fail (even from a valid validator).
	tallyMsg := testutil.ValidSubmitTally(roundID, s.app.ValidatorOperAddr())
	result = s.app.DeliverVoteTx(testutil.MustEncodeVoteTx(tallyMsg))
	s.Require().NotEqual(uint32(0), result.Code, "submit tally against ACTIVE round should fail")
	s.Require().Contains(result.Log, "not in tallying state")
}

// ---------------------------------------------------------------------------
// 6.2.16: PrepareProposal Auto-Tally with Real ElGamal Encryption
// ---------------------------------------------------------------------------

func TestPrepareProposalAutoTally(t *testing.T) {
	app, pk := testutil.SetupTestAppWithEAKey(t)

	// Step 1: Create voting session expiring 30s from now.
	voteEndTime := app.Time.Add(30 * time.Second)
	setupMsg := &types.MsgCreateVotingSession{
		Creator:           "zvote1admin",
		SnapshotHeight:    700,
		SnapshotBlockhash: bytes.Repeat([]byte{0x7A}, 32),
		ProposalsHash:     bytes.Repeat([]byte{0x7B}, 32),
		VoteEndTime:       uint64(voteEndTime.Unix()),
		NullifierImtRoot:  bytes.Repeat([]byte{0x7C}, 32),
		NcRoot:            bytes.Repeat([]byte{0x7D}, 32),
		EaPk:              bytes.Repeat([]byte{0x7E}, 32),
		VkZkp1:            bytes.Repeat([]byte{0x11}, 64),
		VkZkp2:            bytes.Repeat([]byte{0x22}, 64),
		VkZkp3:            bytes.Repeat([]byte{0x33}, 64),
		Proposals:         testutil.SampleProposals(),
	}
	result := app.DeliverVoteTx(testutil.MustEncodeVoteTx(setupMsg))
	require.Equal(t, uint32(0), result.Code, "create session should succeed, got: %s", result.Log)

	roundID := computeRoundID(setupMsg)

	// Step 2: Delegate to populate the commitment tree.
	delegation := testutil.ValidDelegation(roundID, 0x10)
	result = app.DeliverVoteTx(testutil.MustEncodeVoteTx(delegation))
	require.Equal(t, uint32(0), result.Code, "delegation should succeed, got: %s", result.Log)

	anchorHeight := uint64(app.Height)

	// Step 3: Cast vote.
	castVote := testutil.ValidCastVote(roundID, anchorHeight, 0x30)
	result = app.DeliverVoteTx(testutil.MustEncodeVoteTx(castVote))
	require.Equal(t, uint32(0), result.Code, "cast vote should succeed, got: %s", result.Log)

	revealAnchor := uint64(app.Height)

	// Step 4: Reveal share with a real ElGamal ciphertext encrypting v=42.
	ct, err := elgamal.Encrypt(pk, 42, rand.Reader)
	require.NoError(t, err)

	encShare, err := elgamal.MarshalCiphertext(ct)
	require.NoError(t, err)

	revealMsg := testutil.ValidRevealShareReal(roundID, revealAnchor, 0x50, 1, 1, encShare)
	result = app.DeliverVoteTx(testutil.MustEncodeVoteTx(revealMsg))
	require.Equal(t, uint32(0), result.Code, "reveal share should succeed, got: %s", result.Log)

	// Step 5: Advance past VoteEndTime → TALLYING.
	app.NextBlockAtTime(voteEndTime.Add(1 * time.Second))

	ctx := app.NewUncachedContext(false, cmtproto.Header{Height: app.Height})
	kvStore := app.VoteKeeper().OpenKVStore(ctx)
	round, err := app.VoteKeeper().GetVoteRound(kvStore, roundID)
	require.NoError(t, err)
	require.Equal(t, types.SessionStatus_SESSION_STATUS_TALLYING, round.Status,
		"round should be TALLYING after expiry")

	// Step 6: NextBlockWithPrepareProposal — PrepareProposal injects MsgSubmitTally,
	// then FinalizeBlock processes it.
	app.NextBlockWithPrepareProposal()

	// Step 7: Verify round is FINALIZED and tally result matches the encrypted value.
	ctx = app.NewUncachedContext(false, cmtproto.Header{Height: app.Height})
	kvStore = app.VoteKeeper().OpenKVStore(ctx)

	round, err = app.VoteKeeper().GetVoteRound(kvStore, roundID)
	require.NoError(t, err)
	require.Equal(t, types.SessionStatus_SESSION_STATUS_FINALIZED, round.Status,
		"round should be FINALIZED after auto-tally")

	tallyResults, err := app.VoteKeeper().GetAllTallyResults(kvStore, roundID)
	require.NoError(t, err)
	require.Len(t, tallyResults, 1)
	require.Equal(t, uint32(1), tallyResults[0].ProposalId)
	require.Equal(t, uint32(1), tallyResults[0].VoteDecision)
	require.Equal(t, uint64(42), tallyResults[0].TotalValue,
		"decrypted tally should match encrypted value of 42")
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// computeRoundID mirrors the deriveRoundID function from msg_server.go.
// Blake2b-256(snapshot_height || snapshot_blockhash || proposals_hash ||
//
//	vote_end_time || nullifier_imt_root || nc_root)
func computeRoundID(msg *types.MsgCreateVotingSession) []byte {
	h, _ := blake2b.New256(nil)
	var buf [8]byte

	binary.BigEndian.PutUint64(buf[:], msg.SnapshotHeight)
	h.Write(buf[:])
	h.Write(msg.SnapshotBlockhash)
	h.Write(msg.ProposalsHash)
	binary.BigEndian.PutUint64(buf[:], msg.VoteEndTime)
	h.Write(buf[:])
	h.Write(msg.NullifierImtRoot)
	h.Write(msg.NcRoot)

	return h.Sum(nil)
}
