package app_test

import (
	"bytes"
	"crypto/rand"
	"testing"
	"time"

	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/mikelodder7/curvey"

	voteapi "github.com/valargroup/vote-sdk/api"
	"github.com/valargroup/vote-sdk/crypto/ecies"
	"github.com/valargroup/vote-sdk/crypto/elgamal"
	"github.com/valargroup/vote-sdk/crypto/shamir"
	"github.com/valargroup/vote-sdk/testutil"
	"github.com/valargroup/vote-sdk/x/vote/types"
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
	roundID := s.app.SeedVotingSession(setupMsg)

	// Verify the round was stored.
	ctx := s.queryCtx()
	kvStore := s.app.VoteKeeper().OpenKVStore(ctx)

	round, err := s.app.VoteKeeper().GetVoteRound(kvStore, roundID)
	s.Require().NoError(err)
	s.Require().Equal(setupMsg.Creator, round.Creator)
	s.Require().Equal(setupMsg.SnapshotHeight, round.SnapshotHeight)

	// Step 2: Delegate vote.
	delegationMsg := testutil.ValidDelegation(roundID, 0x10)
	delegationTx := testutil.MustEncodeVoteTx(delegationMsg)

	result := s.app.DeliverVoteTx(delegationTx)
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
	treeState, err := s.app.VoteKeeper().GetCommitmentTreeState(kvStore, roundID)
	s.Require().NoError(err)
	s.Require().Equal(uint64(1), treeState.NextIndex)

	// Step 3: EndBlocker already ran during the delegation's FinalizeBlock,
	// computing the tree root at that block height.
	anchorHeight := uint64(s.app.Height)

	ctx = s.queryCtx()
	kvStore = s.app.VoteKeeper().OpenKVStore(ctx)
	root, err := s.app.VoteKeeper().GetCommitmentRootAtHeight(kvStore, roundID, anchorHeight)
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
	treeState, err = s.app.VoteKeeper().GetCommitmentTreeState(kvStore, roundID)
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
	roundID := s.app.SeedVotingSession(setupMsg)

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
	roundID := s.app.SeedVotingSession(setupMsg)

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
	roundID := s.app.SeedVotingSession(setupMsg)

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
	expiredRoundID := s.app.SeedVotingSession(expiredMsg)

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
	roundID := s.app.SeedVotingSession(setupMsg)

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
	roundID := s.app.SeedVotingSession(setupMsg)

	// Register first delegation → 2 leaves in tree.
	delegation1 := testutil.ValidDelegation(roundID, 0x10)
	s.app.DeliverVoteTx(testutil.MustEncodeVoteTx(delegation1))

	// The FinalizeBlock for the delegation already ran EndBlocker.
	h1 := uint64(s.app.Height)

	ctx := s.queryCtx()
	kvStore := s.app.VoteKeeper().OpenKVStore(ctx)
	root1, err := s.app.VoteKeeper().GetCommitmentRootAtHeight(kvStore, roundID, h1)
	s.Require().NoError(err)
	s.Require().NotNil(root1, "root should be stored at height %d", h1)

	// Register second delegation → 4 leaves total.
	delegation2 := testutil.ValidDelegation(roundID, 0x20)
	s.app.DeliverVoteTx(testutil.MustEncodeVoteTx(delegation2))
	h2 := uint64(s.app.Height)

	ctx = s.queryCtx()
	kvStore = s.app.VoteKeeper().OpenKVStore(ctx)
	root2, err := s.app.VoteKeeper().GetCommitmentRootAtHeight(kvStore, roundID, h2)
	s.Require().NoError(err)
	s.Require().NotNil(root2, "root should be stored at height %d", h2)

	// Roots should differ because the tree grew.
	s.Require().NotEqual(root1, root2, "roots should differ after tree growth")

	// Commit an empty block — tree unchanged → no new root stored.
	s.app.NextBlock()
	h3 := uint64(s.app.Height)

	ctx = s.queryCtx()
	kvStore = s.app.VoteKeeper().OpenKVStore(ctx)
	root3, err := s.app.VoteKeeper().GetCommitmentRootAtHeight(kvStore, roundID, h3)
	s.Require().NoError(err)
	s.Require().Nil(root3, "no root should be stored at height %d (tree unchanged)", h3)

	// Previous roots still accessible.
	root1Again, err := s.app.VoteKeeper().GetCommitmentRootAtHeight(kvStore, roundID, h1)
	s.Require().NoError(err)
	s.Require().Equal(root1, root1Again)
}

// ---------------------------------------------------------------------------
// 6.2.9: EndBlocker Status Transition (ACTIVE → TALLYING)
// ---------------------------------------------------------------------------

func (s *ABCIIntegrationSuite) TestEndBlockerStatusTransition() {
	// Create a session that expires 10 seconds from now.
	voteEndTime := s.app.Time.Add(10 * time.Second)
	setupMsg := testutil.ValidCreateVotingSessionWithEndTime(voteEndTime)
	roundID := s.app.SeedVotingSession(setupMsg)

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
	setupMsg := testutil.ValidCreateVotingSessionWithEndTime(voteEndTime)
	roundID := s.app.SeedVotingSession(setupMsg)

	// Delegate while ACTIVE to populate the tree.
	delegation := testutil.ValidDelegation(roundID, 0x10)
	result := s.app.DeliverVoteTx(testutil.MustEncodeVoteTx(delegation))
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

	// RevealShare should be rejected during TALLYING — shares must land
	// before the vote window closes to prevent stale tally corruption.
	revealMsg := testutil.ValidRevealShare(roundID, revealAnchor, 0x50)
	result = s.app.DeliverVoteTx(testutil.MustEncodeVoteTx(revealMsg))
	s.Require().NotEqual(uint32(0), result.Code, "reveal share during TALLYING should be rejected")
	s.Require().Contains(result.Log, "vote round is not active")

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
		Creator:           "sv1admin",
		SnapshotHeight:    300,
		SnapshotBlockhash: bytes.Repeat([]byte{0x2A}, 32),
		ProposalsHash:     bytes.Repeat([]byte{0x2B}, 32),
		VoteEndTime:       uint64(soonEnd.Unix()),
		NullifierImtRoot:  bytes.Repeat([]byte{0x2C}, 32),
		NcRoot:            bytes.Repeat([]byte{0x2D}, 32),
		Proposals:         testutil.SampleProposals(),
	}
	soonRoundID := s.app.SeedVotingSession(soonMsg)

	lateMsg := &types.MsgCreateVotingSession{
		Creator:           "sv1admin",
		SnapshotHeight:    400,
		SnapshotBlockhash: bytes.Repeat([]byte{0x3A}, 32),
		ProposalsHash:     bytes.Repeat([]byte{0x3B}, 32),
		VoteEndTime:       uint64(lateEnd.Unix()),
		NullifierImtRoot:  bytes.Repeat([]byte{0x3C}, 32),
		NcRoot:            bytes.Repeat([]byte{0x3D}, 32),
		Proposals:         testutil.SampleProposals(),
	}
	lateRoundID := s.app.SeedVotingSession(lateMsg)

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
	// Create a session with 2 proposals (ProposalIds 1 and 2; 1-indexed).
	setupMsg := testutil.ValidCreateVotingSessionAt(s.app.Time)
	roundID := s.app.SeedVotingSession(setupMsg)

	// Delegate to populate the tree.
	delegation := testutil.ValidDelegation(roundID, 0x10)
	result := s.app.DeliverVoteTx(testutil.MustEncodeVoteTx(delegation))
	s.Require().Equal(uint32(0), result.Code, "delegation should succeed")

	anchorHeight := uint64(s.app.Height)

	// CastVote with valid proposal_id (1) should succeed.
	castVote := testutil.ValidCastVote(roundID, anchorHeight, 0x30)
	result = s.app.DeliverVoteTx(testutil.MustEncodeVoteTx(castVote))
	s.Require().Equal(uint32(0), result.Code, "cast vote with valid proposal_id should succeed, got: %s", result.Log)

	// Capture the anchor height for the reveal share now, while the tree root
	// exists at this height. The failed bad cast vote below will bump the app
	// height without adding tree leaves, so no root will be stored there.
	revealAnchor := uint64(s.app.Height)

	// CastVote with invalid proposal_id (5) should fail.
	badCastVote := testutil.ValidCastVote(roundID, anchorHeight, 0x40)
	badCastVote.ProposalId = 5
	result = s.app.DeliverVoteTx(testutil.MustEncodeVoteTx(badCastVote))
	s.Require().NotEqual(uint32(0), result.Code, "cast vote with invalid proposal_id should fail")
	s.Require().Contains(result.Log, "invalid proposal ID")

	// RevealShare with valid proposal_id (1) should succeed.
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
// ---------------------------------------------------------------------------
// 6.2.14: SubmitTally — Authorization (Non-Proposer Rejected)
// ---------------------------------------------------------------------------

func (s *ABCIIntegrationSuite) TestSubmitTallyNonProposerRejected() {
	// Create a session expiring 10 seconds from now.
	voteEndTime := s.app.Time.Add(10 * time.Second)
	setupMsg := testutil.ValidCreateVotingSessionWithEndTime(voteEndTime)
	roundID := s.app.SeedVotingSession(setupMsg)

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
	result := s.app.DeliverVoteTx(testutil.MustEncodeVoteTx(badTallyMsg))
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
	roundID := s.app.SeedVotingSession(setupMsg)

	// Verify round is ACTIVE.
	ctx := s.queryCtx()
	kvStore := s.app.VoteKeeper().OpenKVStore(ctx)
	round, err := s.app.VoteKeeper().GetVoteRound(kvStore, roundID)
	s.Require().NoError(err)
	s.Require().Equal(types.SessionStatus_SESSION_STATUS_ACTIVE, round.Status)

	// Submit tally against ACTIVE round should fail (even from a valid validator).
	tallyMsg := testutil.ValidSubmitTally(roundID, s.app.ValidatorOperAddr())
	result := s.app.DeliverVoteTx(testutil.MustEncodeVoteTx(tallyMsg))
	s.Require().NotEqual(uint32(0), result.Code, "submit tally against ACTIVE round should fail")
	s.Require().Contains(result.Log, "not in tallying state")
}

// ---------------------------------------------------------------------------
// 6.2.18: MsgAckExecutiveAuthorityKey Mempool Blocking
// ---------------------------------------------------------------------------

func TestAckExecutiveAuthorityKeyMempoolBlocking(t *testing.T) {
	app, _, pallasPk, _, eaPk := testutil.SetupTestAppWithPallasKey(t)

	eaPkBytes := eaPk.Point.ToAffineCompressed()
	valAddr := app.ValidatorOperAddr()

	// Seed a DEALT ceremony so the ack message is otherwise valid.
	validators := []*types.ValidatorPallasKey{
		{ValidatorAddress: valAddr, PallasPk: pallasPk.Point.ToAffineCompressed()},
	}
	app.SeedDealtCeremony(eaPkBytes, eaPkBytes, validators)

	// Encode a MsgAckExecutiveAuthorityKey.
	ackMsg := &types.MsgAckExecutiveAuthorityKey{
		Creator:      valAddr,
		AckSignature: types.ComputeAckBinding(eaPkBytes, valAddr, nil),
	}

	txBytes, err := voteapi.EncodeCeremonyTx(ackMsg, voteapi.TagAckExecutiveAuthorityKey)
	require.NoError(t, err)

	// CheckTx should reject — acks cannot be submitted via mempool.
	checkResp := app.CheckTxSync(txBytes)
	require.NotEqual(t, uint32(0), checkResp.Code, "CheckTx should reject MsgAckExecutiveAuthorityKey")
	require.Contains(t, checkResp.Log, "cannot be submitted via mempool")
}

// ---------------------------------------------------------------------------
// 6.2.22: Multi-Validator Ceremony — Timeout, Re-Deal
// ---------------------------------------------------------------------------

// TestMultiValidatorCeremony_TimeoutMissTracking uses 4 validators (1 real +
// 3 phantom) to exercise the timeout path where acks fall below the 1/2
// threshold. With 4 validators, 1 ack gives 1*2=2 < 4 — below threshold.
// The EndBlocker resets the ceremony to REGISTERING for re-deal.
// Repeats 3 cycles to verify reset behavior across rounds.
func TestMultiValidatorCeremony_TimeoutMissTracking(t *testing.T) {
	app, _, pallasPk, _, _ := testutil.SetupTestAppWithPallasKey(t)

	valAddr := app.ValidatorOperAddr()

	app.RegisterPallasKey(pallasPk)

	// Generate 3 phantom validators (4 total).
	_, phantomPk1 := elgamal.KeyGen(rand.Reader)
	_, phantomPk2 := elgamal.KeyGen(rand.Reader)
	_, phantomPk3 := elgamal.KeyGen(rand.Reader)
	phantom1Addr := sdk.ValAddress(bytes.Repeat([]byte{0xB1}, 20)).String()
	phantom2Addr := sdk.ValAddress(bytes.Repeat([]byte{0xB2}, 20)).String()
	phantom3Addr := sdk.ValAddress(bytes.Repeat([]byte{0xB3}, 20)).String()

	validators := []*types.ValidatorPallasKey{
		{ValidatorAddress: valAddr, PallasPk: pallasPk.Point.ToAffineCompressed()},
		{ValidatorAddress: phantom1Addr, PallasPk: phantomPk1.Point.ToAffineCompressed()},
		{ValidatorAddress: phantom2Addr, PallasPk: phantomPk2.Point.ToAffineCompressed()},
		{ValidatorAddress: phantom3Addr, PallasPk: phantomPk3.Point.ToAffineCompressed()},
	}
	roundID := app.SeedRegisteringCeremony(validators)

	G := elgamal.PallasGenerator()
	phantomAddrs := []string{phantom1Addr, phantom2Addr, phantom3Addr}

	for cycle := 1; cycle <= 3; cycle++ {
		// Pre-seed 3 phantom DKG contributions so the proposer's
		// 4th contribution via PrepareProposal triggers finalizeDKG → DEALT.
		seedPhantomDKGContributions(t, app, roundID, validators, valAddr, phantomAddrs, G)

		// Step 1: DKG contribution from proposer via pipeline.
		app.NextBlockWithPrepareProposal()

		round := app.MustGetVoteRound(roundID)
		require.Equal(t, types.CeremonyStatus_CEREMONY_STATUS_DEALT, round.CeremonyStatus,
			"cycle %d: ceremony should be DEALT after auto-deal", cycle)

		// Step 2: PrepareProposal fires auto-ack → 1/4 < 1/2 → stays DEALT.
		app.NextBlockWithPrepareProposal()

		round = app.MustGetVoteRound(roundID)
		require.Equal(t, types.CeremonyStatus_CEREMONY_STATUS_DEALT, round.CeremonyStatus,
			"cycle %d: ceremony should still be DEALT (1/4 below threshold)", cycle)
		require.Len(t, round.CeremonyAcks, 1,
			"cycle %d: should have 1 ack from real validator", cycle)

		// Step 3: Advance 31 minutes past deal time → EndBlocker timeout.
		// CeremonyPhaseStart was set when the deal was processed.
		timeoutTime := time.Unix(int64(round.CeremonyPhaseStart+round.CeremonyPhaseTimeout)+1, 0)
		app.NextBlockAtTime(timeoutTime)

		round = app.MustGetVoteRound(roundID)
		require.Equal(t, types.CeremonyStatus_CEREMONY_STATUS_REGISTERING, round.CeremonyStatus,
			"cycle %d: ceremony should reset to REGISTERING after timeout", cycle)
		require.Equal(t, types.SessionStatus_SESSION_STATUS_PENDING, round.Status,
			"cycle %d: round should stay PENDING after timeout reset", cycle)
		require.Nil(t, round.CeremonyAcks,
			"cycle %d: acks should be cleared after timeout reset", cycle)

		// Verify ceremony log entries for the timeout reset.
		require.NotEmpty(t, round.CeremonyLog,
			"cycle %d: ceremony log should not be empty", cycle)
	}

	// After 3 cycles, verify final state.
	round := app.MustGetVoteRound(roundID)
	require.Equal(t, types.CeremonyStatus_CEREMONY_STATUS_REGISTERING, round.CeremonyStatus)
	require.Equal(t, types.SessionStatus_SESSION_STATUS_PENDING, round.Status)
}

// ---------------------------------------------------------------------------
// 6.2.23: Validator Recovery After Missed Ceremony
// ---------------------------------------------------------------------------

// TestCeremonyRecovery_ValidatorRejoinsAfterMiss exercises the recovery path
// where a validator that missed a ceremony cycle comes back online and
// successfully acks in the next cycle, pushing the ceremony past the 1/2
// threshold to CONFIRMED.
//
// Setup: 4 validators (1 real proposer + 3 phantom). With 4 validators,
// the 1/2 threshold requires 2*2=4 >= 4, so 2 acks are needed.
//
// Cycle 1: timeout (only real validator acks, 1*3=3 < 4).
// Cycle 2: phantom1 manually acks → 2 acks total → timeout confirms + strips.
func TestCeremonyRecovery_ValidatorRejoinsAfterMiss(t *testing.T) {
	app, _, pallasPk, _, _ := testutil.SetupTestAppWithPallasKey(t)

	valAddr := app.ValidatorOperAddr()

	app.RegisterPallasKey(pallasPk)

	// Generate 3 phantom validators (4 total).
	_, phantomPk1 := elgamal.KeyGen(rand.Reader)
	_, phantomPk2 := elgamal.KeyGen(rand.Reader)
	_, phantomPk3 := elgamal.KeyGen(rand.Reader)
	phantom1Addr := sdk.ValAddress(bytes.Repeat([]byte{0xC1}, 20)).String()
	phantom2Addr := sdk.ValAddress(bytes.Repeat([]byte{0xC2}, 20)).String()
	phantom3Addr := sdk.ValAddress(bytes.Repeat([]byte{0xC3}, 20)).String()

	validators := []*types.ValidatorPallasKey{
		{ValidatorAddress: valAddr, PallasPk: pallasPk.Point.ToAffineCompressed()},
		{ValidatorAddress: phantom1Addr, PallasPk: phantomPk1.Point.ToAffineCompressed()},
		{ValidatorAddress: phantom2Addr, PallasPk: phantomPk2.Point.ToAffineCompressed()},
		{ValidatorAddress: phantom3Addr, PallasPk: phantomPk3.Point.ToAffineCompressed()},
	}
	roundID := app.SeedRegisteringCeremony(validators)

	G := elgamal.PallasGenerator()
	phantomAddrs := []string{phantom1Addr, phantom2Addr, phantom3Addr}

	// -----------------------------------------------------------------------
	// Cycle 1 — Timeout: only real validator acks, phantoms miss.
	// -----------------------------------------------------------------------

	// Pre-seed 3 phantom DKG contributions for cycle 1.
	seedPhantomDKGContributions(t, app, roundID, validators, valAddr, phantomAddrs, G)

	// Block 1: DKG contribution from proposer via pipeline → DEALT.
	app.NextBlockWithPrepareProposal()

	round := app.MustGetVoteRound(roundID)
	require.Equal(t, types.CeremonyStatus_CEREMONY_STATUS_DEALT, round.CeremonyStatus,
		"cycle 1: ceremony should be DEALT after deal")

	// Block 2: PrepareProposal fires auto-ack from real validator → still DEALT.
	app.NextBlockWithPrepareProposal()

	round = app.MustGetVoteRound(roundID)
	require.Equal(t, types.CeremonyStatus_CEREMONY_STATUS_DEALT, round.CeremonyStatus,
		"cycle 1: ceremony should still be DEALT (1/4 below threshold)")
	require.Len(t, round.CeremonyAcks, 1, "cycle 1: should have 1 ack from real validator")

	// Block 3: Advance past timeout → EndBlocker resets to REGISTERING.
	timeoutTime := time.Unix(int64(round.CeremonyPhaseStart+round.CeremonyPhaseTimeout)+1, 0)
	app.NextBlockAtTime(timeoutTime)

	round = app.MustGetVoteRound(roundID)
	require.Equal(t, types.CeremonyStatus_CEREMONY_STATUS_REGISTERING, round.CeremonyStatus,
		"cycle 1: ceremony should reset to REGISTERING after timeout")

	// -----------------------------------------------------------------------
	// Cycle 2 — Recovery: phantom1 acks manually, ceremony confirms.
	// -----------------------------------------------------------------------

	// Pre-seed 3 phantom DKG contributions for cycle 2.
	seedPhantomDKGContributions(t, app, roundID, validators, valAddr, phantomAddrs, G)

	// Block 4: DKG contribution from proposer via pipeline → DEALT.
	app.NextBlockWithPrepareProposal()

	round = app.MustGetVoteRound(roundID)
	require.Equal(t, types.CeremonyStatus_CEREMONY_STATUS_DEALT, round.CeremonyStatus,
		"cycle 2: ceremony should be DEALT after auto-deal")

	// Block 5: PrepareProposal fires auto-ack from real validator → still DEALT.
	app.NextBlockWithPrepareProposal()

	ctx := app.NewUncachedContext(false, cmtproto.Header{Height: app.Height})
	kvStore := app.VoteKeeper().OpenKVStore(ctx)
	round, err := app.VoteKeeper().GetVoteRound(kvStore, roundID)
	require.NoError(t, err)
	require.Equal(t, types.CeremonyStatus_CEREMONY_STATUS_DEALT, round.CeremonyStatus,
		"cycle 2: ceremony should still be DEALT (1/4 below threshold)")
	require.Len(t, round.CeremonyAcks, 1, "cycle 2: should have 1 ack from real validator")

	// Block 6: Write phantom1's ack directly to state. In production,
	// phantom1 would ack when they are the block proposer
	// (ValidateProposerIsCreator enforces creator == proposer).
	seedPhantomAcks(round, app.Height, phantom1Addr)
	require.NoError(t, app.VoteKeeper().SetVoteRound(kvStore, round))
	app.NextBlock()

	// Fast path requires ALL validators to ack, so 2/4 stays DEALT.
	// Advance past timeout → EndBlocker confirms with >= 1/2 acks and strips
	// non-ackers (phantom2, phantom3).
	timeoutTime = time.Unix(int64(round.CeremonyPhaseStart+round.CeremonyPhaseTimeout)+1, 0)
	app.NextBlockAtTime(timeoutTime)

	// -----------------------------------------------------------------------
	// Assertions: ceremony confirmed via timeout, non-ackers stripped.
	// -----------------------------------------------------------------------

	round = app.MustGetVoteRound(roundID)

	// Round should be ACTIVE with ceremony CONFIRMED.
	require.Equal(t, types.SessionStatus_SESSION_STATUS_ACTIVE, round.Status,
		"round should be ACTIVE after timeout with 2/4 acks")
	require.Equal(t, types.CeremonyStatus_CEREMONY_STATUS_CONFIRMED, round.CeremonyStatus,
		"ceremony should be CONFIRMED")

	// 2 acks: real validator + phantom1.
	require.Len(t, round.CeremonyAcks, 2, "should have 2 acks (real + phantom1)")

	// Non-ackers stripped: only 2 validators remain.
	require.Len(t, round.CeremonyValidators, 2,
		"CeremonyValidators should have 2 entries (non-ackers stripped)")

}

// ---------------------------------------------------------------------------
// Full Lifecycle E2E: Ceremony → Vote → Tally (Single Validator, n=1, t=1)
//
// Validates the degenerate single-share Shamir case works end-to-end.
// With min_ceremony_validators=1 (the default), a single validator handles
// deal, ack, partial decrypt, and tally through the same threshold code paths.

func TestFullLifecycle_SingleValidator(t *testing.T) {
	app, _, pallasPk, _, _ := testutil.SetupTestAppWithPallasKey(t)

	proposerAddr := app.ValidatorOperAddr()

	validators := []*types.ValidatorPallasKey{
		{ValidatorAddress: proposerAddr, PallasPk: pallasPk.Point.ToAffineCompressed(), ShamirIndex: 1},
	}
	voteEndTime := app.Time.Add(60 * time.Second)

	roundID := make([]byte, 32)
	roundID[0] = 0xF2

	ctx := app.NewUncachedContext(false, cmtproto.Header{Height: app.Height})
	kvStore := app.VoteKeeper().OpenKVStore(ctx)
	round := &types.VoteRound{
		VoteRoundId:        roundID,
		Status:             types.SessionStatus_SESSION_STATUS_PENDING,
		CeremonyStatus:     types.CeremonyStatus_CEREMONY_STATUS_REGISTERING,
		CeremonyValidators: validators,
		VoteEndTime:        uint64(voteEndTime.Unix()),
		Proposals:          testutil.SampleProposals(),
		NullifierImtRoot:   bytes.Repeat([]byte{0x01}, 32),
		NcRoot:             bytes.Repeat([]byte{0x02}, 32),
	}
	require.NoError(t, app.VoteKeeper().SetVoteRound(kvStore, round))
	app.NextBlock()

	// Block 1: DKG contribution from proposer via pipeline → DEALT (Shamir t=1, n=1).
	app.NextBlockWithPrepareProposal()

	round = app.MustGetVoteRound(roundID)
	require.Equal(t, types.CeremonyStatus_CEREMONY_STATUS_DEALT, round.CeremonyStatus)
	require.EqualValues(t, 1, round.Threshold, "t=1 for single validator")
	require.Len(t, round.FeldmanCommitments, 1)

	// Block 2: auto-ack → single validator acks → CONFIRMED + ACTIVE.
	app.NextBlockWithPrepareProposal()

	round = app.MustGetVoteRound(roundID)
	require.Equal(t, types.SessionStatus_SESSION_STATUS_ACTIVE, round.Status,
		"round should be ACTIVE after single-validator ceremony")
	require.Len(t, round.CeremonyAcks, 1)

	eaPk, err := elgamal.UnmarshalPublicKey(round.EaPk)
	require.NoError(t, err)

	// --- Voting ---

	delegation := testutil.ValidDelegation(roundID, 0x10)
	result := app.DeliverVoteTx(testutil.MustEncodeVoteTx(delegation))
	require.Equal(t, uint32(0), result.Code, "delegation should succeed, got: %s", result.Log)

	anchorHeight := uint64(app.Height)

	castVote := testutil.ValidCastVote(roundID, anchorHeight, 0x30)
	result = app.DeliverVoteTx(testutil.MustEncodeVoteTx(castVote))
	require.Equal(t, uint32(0), result.Code, "cast vote should succeed, got: %s", result.Log)

	revealAnchor := uint64(app.Height)

	ct, err := elgamal.Encrypt(eaPk, 42, rand.Reader)
	require.NoError(t, err)
	encShare, err := elgamal.MarshalCiphertext(ct)
	require.NoError(t, err)

	revealMsg := testutil.ValidRevealShareReal(roundID, revealAnchor, 0x50, 1, 1, encShare)
	result = app.DeliverVoteTx(testutil.MustEncodeVoteTx(revealMsg))
	require.Equal(t, uint32(0), result.Code, "reveal share should succeed, got: %s", result.Log)

	// --- Tally ---

	app.NextBlockAtTime(voteEndTime.Add(1 * time.Second))

	round = app.MustGetVoteRound(roundID)
	require.Equal(t, types.SessionStatus_SESSION_STATUS_TALLYING, round.Status)

	// Block N: partial decrypt (D_1 = share * C1) injected.
	app.NextBlockWithPrepareProposal()

	ctx = app.NewUncachedContext(false, cmtproto.Header{Height: app.Height})
	kvStore = app.VoteKeeper().OpenKVStore(ctx)
	count, err := app.VoteKeeper().CountPartialDecryptionValidators(kvStore, roundID)
	require.NoError(t, err)
	require.Equal(t, 1, count, "single validator should have submitted partial decryption")

	// Block N+1: tally combiner fires (Lagrange with single partial) → FINALIZED.
	app.NextBlockWithPrepareProposal()

	ctx = app.NewUncachedContext(false, cmtproto.Header{Height: app.Height})
	kvStore = app.VoteKeeper().OpenKVStore(ctx)
	round, err = app.VoteKeeper().GetVoteRound(kvStore, roundID)
	require.NoError(t, err)
	require.Equal(t, types.SessionStatus_SESSION_STATUS_FINALIZED, round.Status,
		"round should be FINALIZED after single-validator tally")

	tallyResults, err := app.VoteKeeper().GetAllTallyResults(kvStore, roundID)
	require.NoError(t, err)
	require.Len(t, tallyResults, 1)
	require.Equal(t, uint64(42), tallyResults[0].TotalValue,
		"decrypted tally should match encrypted value of 42")
}

// ---------------------------------------------------------------------------
// Full Lifecycle E2E: DKG Ceremony → Vote → Tally (n=3, t=2)
//
// Drives the complete pipeline with Joint-Feldman DKG instead of a single dealer:
//   REGISTERING → 3 DKG contributions (phantom1, phantom2 pre-seeded; proposer via pipeline)
//   → finalizeDKG → DEALT
//   → ack (proposer via pipeline + 2 phantom acks seeded) → ACTIVE
//   → delegate → cast → reveal (real ElGamal to combined ea_pk)
//   → EndBlocker TALLYING
//   → partial decrypt (proposer via pipeline + phantom1 seeded; phantom2 absent) → Lagrange → FINALIZED
//
// This test validates that DKG-derived combined Shamir shares produce the same
// correct Lagrange-reconstructed decryption as the single-dealer path.
// ---------------------------------------------------------------------------

func TestDKGFullLifecycle(t *testing.T) {
	app, _, pallasPk, _, _ := testutil.SetupTestAppWithPallasKey(t)

	G := elgamal.PallasGenerator()
	proposerAddr := app.ValidatorOperAddr()

	phantom1Sk, phantom1Pk := elgamal.KeyGen(rand.Reader)
	_, phantom2Pk := elgamal.KeyGen(rand.Reader)

	phantom1Addr := sdk.ValAddress(bytes.Repeat([]byte{0xD1}, 20)).String()
	phantom2Addr := sdk.ValAddress(bytes.Repeat([]byte{0xD2}, 20)).String()

	validators := []*types.ValidatorPallasKey{
		{ValidatorAddress: proposerAddr, PallasPk: pallasPk.Point.ToAffineCompressed(), ShamirIndex: 1},
		{ValidatorAddress: phantom1Addr, PallasPk: phantom1Pk.Point.ToAffineCompressed(), ShamirIndex: 2},
		{ValidatorAddress: phantom2Addr, PallasPk: phantom2Pk.Point.ToAffineCompressed(), ShamirIndex: 3},
	}
	voteEndTime := app.Time.Add(90 * time.Second)

	roundID := make([]byte, 32)
	roundID[0] = 0xD0

	n := 3
	tVal := 2 // ceil(3/2)

	// -----------------------------------------------------------------------
	// Phase A: Generate phantom DKG state and pre-seed contributions
	// -----------------------------------------------------------------------

	phantom1Secret := new(curvey.ScalarPallas).Random(rand.Reader)
	phantom1Shares, phantom1Coeffs, err := shamir.Split(phantom1Secret, tVal, n)
	require.NoError(t, err)
	phantom1CommitPts, err := shamir.FeldmanCommit(G, phantom1Coeffs)
	require.NoError(t, err)

	phantom2Secret := new(curvey.ScalarPallas).Random(rand.Reader)
	phantom2Shares, phantom2Coeffs, err := shamir.Split(phantom2Secret, tVal, n)
	require.NoError(t, err)
	phantom2CommitPts, err := shamir.FeldmanCommit(G, phantom2Coeffs)
	require.NoError(t, err)
	_ = phantom2Coeffs

	// Build phantom1's contribution: encrypts shares for proposer and phantom2.
	phantom1Commitments := make([][]byte, tVal)
	for j, pt := range phantom1CommitPts {
		phantom1Commitments[j] = pt.ToAffineCompressed()
	}
	phantom1Payloads := make([]*types.DealerPayload, 0, 2)
	env, err := ecies.Encrypt(G, pallasPk.Point, phantom1Shares[0].Value.Bytes(), rand.Reader)
	require.NoError(t, err)
	phantom1Payloads = append(phantom1Payloads, &types.DealerPayload{
		ValidatorAddress: proposerAddr,
		EphemeralPk:      env.Ephemeral.ToAffineCompressed(),
		Ciphertext:       env.Ciphertext,
	})
	env, err = ecies.Encrypt(G, phantom2Pk.Point, phantom1Shares[2].Value.Bytes(), rand.Reader)
	require.NoError(t, err)
	phantom1Payloads = append(phantom1Payloads, &types.DealerPayload{
		ValidatorAddress: phantom2Addr,
		EphemeralPk:      env.Ephemeral.ToAffineCompressed(),
		Ciphertext:       env.Ciphertext,
	})

	// Build phantom2's contribution: encrypts shares for proposer and phantom1.
	phantom2Commitments := make([][]byte, tVal)
	for j, pt := range phantom2CommitPts {
		phantom2Commitments[j] = pt.ToAffineCompressed()
	}
	phantom2Payloads := make([]*types.DealerPayload, 0, 2)
	env, err = ecies.Encrypt(G, pallasPk.Point, phantom2Shares[0].Value.Bytes(), rand.Reader)
	require.NoError(t, err)
	phantom2Payloads = append(phantom2Payloads, &types.DealerPayload{
		ValidatorAddress: proposerAddr,
		EphemeralPk:      env.Ephemeral.ToAffineCompressed(),
		Ciphertext:       env.Ciphertext,
	})
	env, err = ecies.Encrypt(G, phantom1Pk.Point, phantom2Shares[1].Value.Bytes(), rand.Reader)
	require.NoError(t, err)
	phantom2Payloads = append(phantom2Payloads, &types.DealerPayload{
		ValidatorAddress: phantom1Addr,
		EphemeralPk:      env.Ephemeral.ToAffineCompressed(),
		Ciphertext:       env.Ciphertext,
	})

	// Seed round with phantom contributions already present.
	ctx := app.NewUncachedContext(false, cmtproto.Header{Height: app.Height})
	kvStore := app.VoteKeeper().OpenKVStore(ctx)
	round := &types.VoteRound{
		VoteRoundId:        roundID,
		Status:             types.SessionStatus_SESSION_STATUS_PENDING,
		CeremonyStatus:     types.CeremonyStatus_CEREMONY_STATUS_REGISTERING,
		CeremonyValidators: validators,
		VoteEndTime:        uint64(voteEndTime.Unix()),
		Proposals:          testutil.SampleProposals(),
		NullifierImtRoot:   bytes.Repeat([]byte{0x01}, 32),
		NcRoot:             bytes.Repeat([]byte{0x02}, 32),
		DkgContributions: []*types.DKGContribution{
			{
				ValidatorAddress:   phantom1Addr,
				FeldmanCommitments: phantom1Commitments,
				Payloads:           phantom1Payloads,
			},
			{
				ValidatorAddress:   phantom2Addr,
				FeldmanCommitments: phantom2Commitments,
				Payloads:           phantom2Payloads,
			},
		},
	}
	require.NoError(t, app.VoteKeeper().SetVoteRound(kvStore, round))
	app.NextBlock()

	// -----------------------------------------------------------------------
	// Phase B: Proposer contributes via pipeline → finalizeDKG → DEALT
	// -----------------------------------------------------------------------

	app.NextBlockWithPrepareProposal()

	round = app.MustGetVoteRound(roundID)
	require.Equal(t, types.CeremonyStatus_CEREMONY_STATUS_DEALT, round.CeremonyStatus,
		"ceremony should be DEALT after 3rd DKG contribution")
	require.Len(t, round.DkgContributions, 3)
	require.EqualValues(t, 2, round.Threshold)
	require.Len(t, round.FeldmanCommitments, 2, "t=2 combined Feldman commitments")
	require.NotEmpty(t, round.EaPk, "combined ea_pk must be set")

	// -----------------------------------------------------------------------
	// Phase C: Proposer acks via pipeline → combined share on disk
	// -----------------------------------------------------------------------

	app.NextBlockWithPrepareProposal()

	ctx = app.NewUncachedContext(false, cmtproto.Header{Height: app.Height})
	kvStore = app.VoteKeeper().OpenKVStore(ctx)
	round, err = app.VoteKeeper().GetVoteRound(kvStore, roundID)
	require.NoError(t, err)
	require.Equal(t, types.CeremonyStatus_CEREMONY_STATUS_DEALT, round.CeremonyStatus,
		"1/3 acked — stays DEALT")
	require.Len(t, round.CeremonyAcks, 1)

	// -----------------------------------------------------------------------
	// Phase D: Phantom acks — compute phantom1's combined share for tally
	// -----------------------------------------------------------------------

	// Phantom1 combined share = own partial + proposer's share + phantom2's share.
	phantom1OwnPartial := shamir.EvalPolynomial(phantom1Coeffs, 2) // ShamirIndex=2
	phantom1CombinedShare := phantom1OwnPartial

	for _, contrib := range round.DkgContributions {
		if contrib.ValidatorAddress == phantom1Addr {
			continue
		}
		for _, p := range contrib.Payloads {
			if p.ValidatorAddress != phantom1Addr {
				continue
			}
			ephPk, err := elgamal.UnmarshalPublicKey(p.EphemeralPk)
			require.NoError(t, err)
			shareBytes, err := ecies.Decrypt(phantom1Sk.Scalar, &ecies.Envelope{
				Ephemeral:  ephPk.Point,
				Ciphertext: p.Ciphertext,
			})
			require.NoError(t, err)
			shareScalar, err := new(curvey.ScalarPallas).SetBytes(shareBytes)
			require.NoError(t, err)
			phantom1CombinedShare = phantom1CombinedShare.Add(shareScalar)
		}
	}

	// Sanity: verify phantom1's combined share against combined commitments.
	combinedCommitPts := make([]curvey.Point, len(round.FeldmanCommitments))
	for j, c := range round.FeldmanCommitments {
		pt, err := elgamal.DecompressPallasPoint(c)
		require.NoError(t, err)
		combinedCommitPts[j] = pt
	}
	ok, err := shamir.VerifyFeldmanShare(G, combinedCommitPts, 2, phantom1CombinedShare)
	require.NoError(t, err)
	require.True(t, ok, "phantom1 combined share must verify against combined Feldman commitments")

	// Write phantom acks directly to state (production: each validator acks
	// when they propose a block; ValidateProposerIsCreator enforces this).
	seedPhantomAcks(round, app.Height, phantom1Addr, phantom2Addr)
	round.CeremonyStatus = types.CeremonyStatus_CEREMONY_STATUS_CONFIRMED
	round.Status = types.SessionStatus_SESSION_STATUS_ACTIVE
	require.NoError(t, app.VoteKeeper().SetVoteRound(kvStore, round))
	app.NextBlock()

	round = app.MustGetVoteRound(roundID)
	require.Equal(t, types.SessionStatus_SESSION_STATUS_ACTIVE, round.Status,
		"round should be ACTIVE after 3/3 acks")

	eaPk, err := elgamal.UnmarshalPublicKey(round.EaPk)
	require.NoError(t, err)

	// -----------------------------------------------------------------------
	// Phase E: Vote — delegate, cast, reveal with real ElGamal
	// -----------------------------------------------------------------------

	delegation := testutil.ValidDelegation(roundID, 0x10)
	result := app.DeliverVoteTx(testutil.MustEncodeVoteTx(delegation))
	require.Equal(t, uint32(0), result.Code, "delegation should succeed, got: %s", result.Log)

	anchorHeight := uint64(app.Height)

	castVote := testutil.ValidCastVote(roundID, anchorHeight, 0x30)
	result = app.DeliverVoteTx(testutil.MustEncodeVoteTx(castVote))
	require.Equal(t, uint32(0), result.Code, "cast vote should succeed, got: %s", result.Log)

	revealAnchor := uint64(app.Height)

	ct, err := elgamal.Encrypt(eaPk, 99, rand.Reader)
	require.NoError(t, err)
	encShare, err := elgamal.MarshalCiphertext(ct)
	require.NoError(t, err)

	revealMsg := testutil.ValidRevealShareReal(roundID, revealAnchor, 0x50, 1, 1, encShare)
	result = app.DeliverVoteTx(testutil.MustEncodeVoteTx(revealMsg))
	require.Equal(t, uint32(0), result.Code, "reveal share should succeed, got: %s", result.Log)

	// -----------------------------------------------------------------------
	// Phase F: Transition to TALLYING
	// -----------------------------------------------------------------------

	app.NextBlockAtTime(voteEndTime.Add(1 * time.Second))

	ctx = app.NewUncachedContext(false, cmtproto.Header{Height: app.Height})
	kvStore = app.VoteKeeper().OpenKVStore(ctx)
	round, err = app.VoteKeeper().GetVoteRound(kvStore, roundID)
	require.NoError(t, err)
	require.Equal(t, types.SessionStatus_SESSION_STATUS_TALLYING, round.Status)

	// -----------------------------------------------------------------------
	// Phase G: Partial decryptions — phantom1 seeded, proposer via pipeline
	// -----------------------------------------------------------------------

	// Pre-seed phantom1's partial decryption: D_1 = combined_share * C1.
	tallyBytes, err := app.VoteKeeper().GetTally(kvStore, roundID, 1, 1)
	require.NoError(t, err)
	tallyCt, err := elgamal.UnmarshalCiphertext(tallyBytes)
	require.NoError(t, err)

	phantom1Di := tallyCt.C1.Mul(phantom1CombinedShare)
	phantom1Entries := []*types.PartialDecryptionEntry{{
		ProposalId:     1,
		VoteDecision:   1,
		PartialDecrypt: phantom1Di.ToAffineCompressed(),
	}}

	ctx = app.NewUncachedContext(false, cmtproto.Header{Height: app.Height})
	kvStore = app.VoteKeeper().OpenKVStore(ctx)
	require.NoError(t, app.VoteKeeper().SetPartialDecryptions(kvStore, roundID, 2, phantom1Entries))
	app.NextBlock()

	// Phantom2 deliberately absent — testing threshold (t=2 of n=3).

	// Block 1: partial decrypt injector fires for proposer → count reaches 2.
	app.NextBlockWithPrepareProposal()

	ctx = app.NewUncachedContext(false, cmtproto.Header{Height: app.Height})
	kvStore = app.VoteKeeper().OpenKVStore(ctx)
	count, err := app.VoteKeeper().CountPartialDecryptionValidators(kvStore, roundID)
	require.NoError(t, err)
	require.Equal(t, 2, count,
		"proposer + phantom1 should have submitted (phantom2 absent)")

	// Block 2: tally combiner sees count=2 >= threshold=2 → Lagrange → FINALIZED.
	app.NextBlockWithPrepareProposal()

	ctx = app.NewUncachedContext(false, cmtproto.Header{Height: app.Height})
	kvStore = app.VoteKeeper().OpenKVStore(ctx)
	round, err = app.VoteKeeper().GetVoteRound(kvStore, roundID)
	require.NoError(t, err)
	require.Equal(t, types.SessionStatus_SESSION_STATUS_FINALIZED, round.Status,
		"round should be FINALIZED after threshold tally (t=2 of n=3)")

	tallyResults, err := app.VoteKeeper().GetAllTallyResults(kvStore, roundID)
	require.NoError(t, err)
	require.Len(t, tallyResults, 1)
	require.Equal(t, uint64(99), tallyResults[0].TotalValue,
		"decrypted tally must match encrypted value of 99 — proves DKG shares are correct")
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// seedPhantomDKGContributions reads the round, appends one DKG contribution per
// phantom address (with real ECIES payloads for the proposer, dummy for others),
// saves the round, and advances a block.
func seedPhantomDKGContributions(
	t *testing.T,
	app *testutil.TestApp,
	roundID []byte,
	validators []*types.ValidatorPallasKey,
	proposerAddr string,
	phantomAddrs []string,
	G curvey.Point,
) {
	t.Helper()

	n := len(validators)
	tVal := (n + 1) / 2

	ctx := app.NewUncachedContext(false, cmtproto.Header{Height: app.Height})
	kvStore := app.VoteKeeper().OpenKVStore(ctx)
	round, err := app.VoteKeeper().GetVoteRound(kvStore, roundID)
	require.NoError(t, err)

	for _, addr := range phantomAddrs {
		secret := new(curvey.ScalarPallas).Random(rand.Reader)
		shares, coeffs, err := shamir.Split(secret, tVal, n)
		require.NoError(t, err)
		commitPts, err := shamir.FeldmanCommit(G, coeffs)
		require.NoError(t, err)

		commitments := make([][]byte, len(commitPts))
		for j, pt := range commitPts {
			commitments[j] = pt.ToAffineCompressed()
		}

		var payloads []*types.DealerPayload
		for i, v := range validators {
			if v.ValidatorAddress == addr {
				continue
			}
			if v.ValidatorAddress == proposerAddr {
				recipientPk, err := elgamal.UnmarshalPublicKey(v.PallasPk)
				require.NoError(t, err)
				env, err := ecies.Encrypt(G, recipientPk.Point, shares[i].Value.Bytes(), rand.Reader)
				require.NoError(t, err)
				payloads = append(payloads, &types.DealerPayload{
					ValidatorAddress: v.ValidatorAddress,
					EphemeralPk:      env.Ephemeral.ToAffineCompressed(),
					Ciphertext:       env.Ciphertext,
				})
			} else {
				payloads = append(payloads, &types.DealerPayload{
					ValidatorAddress: v.ValidatorAddress,
					EphemeralPk:      bytes.Repeat([]byte{0xEE}, 32),
					Ciphertext:       bytes.Repeat([]byte{0xFF}, 48),
				})
			}
		}

		round.DkgContributions = append(round.DkgContributions, &types.DKGContribution{
			ValidatorAddress:   addr,
			FeldmanCommitments: commitments,
			Payloads:           payloads,
		})
	}

	require.NoError(t, app.VoteKeeper().SetVoteRound(kvStore, round))
	app.NextBlock()
}

// seedPhantomAcks appends ack entries to the round for the given validator
// addresses, computing the ack signature from the round's EaPk.
func seedPhantomAcks(round *types.VoteRound, height int64, addrs ...string) {
	for _, addr := range addrs {
		round.CeremonyAcks = append(round.CeremonyAcks, &types.AckEntry{
			ValidatorAddress: addr,
			AckSignature:     types.ComputeAckBinding(round.EaPk, addr, nil),
			AckHeight:        uint64(height),
		})
	}
}
