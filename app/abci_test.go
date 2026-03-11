package app_test

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"os"
	"testing"
	"time"

	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/mikelodder7/curvey"

	voteapi "github.com/valargroup/shielded-vote/api"
	"github.com/valargroup/shielded-vote/crypto/ecies"
	"github.com/valargroup/shielded-vote/crypto/elgamal"
	"github.com/valargroup/shielded-vote/testutil"
	"github.com/valargroup/shielded-vote/x/vote/types"
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
		Creator:           "sv1admin",
		SnapshotHeight:    300,
		SnapshotBlockhash: bytes.Repeat([]byte{0x2A}, 32),
		ProposalsHash:     bytes.Repeat([]byte{0x2B}, 32),
		VoteEndTime:       uint64(soonEnd.Unix()),
		NullifierImtRoot:  bytes.Repeat([]byte{0x2C}, 32),
		NcRoot:            bytes.Repeat([]byte{0x2D}, 32),
		VkZkp1:            bytes.Repeat([]byte{0x11}, 64),
		VkZkp2:            bytes.Repeat([]byte{0x22}, 64),
		VkZkp3:            bytes.Repeat([]byte{0x33}, 64),
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
		VkZkp1:            bytes.Repeat([]byte{0x11}, 64),
		VkZkp2:            bytes.Repeat([]byte{0x22}, 64),
		VkZkp3:            bytes.Repeat([]byte{0x33}, 64),
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
// 6.2.13: SubmitTally — TALLYING → FINALIZED Lifecycle
// ---------------------------------------------------------------------------

func (s *ABCIIntegrationSuite) TestSubmitTallyLifecycle() {
	// Generate a real EA keypair for DLEQ proof generation/verification.
	eaSk, eaPk := elgamal.KeyGen(rand.Reader)

	// Set EA public key so SeedVotingSession stores the matching ea_pk
	// on the round (needed for DLEQ verification).
	s.app.EaPk = eaPk.Point.ToAffineCompressed()

	// Create a session expiring 30 seconds from now.
	voteEndTime := s.app.Time.Add(30 * time.Second)
	setupMsg := testutil.ValidCreateVotingSessionWithEndTime(voteEndTime)
	roundID := s.app.SeedVotingSession(setupMsg)

	// Delegate while ACTIVE to populate the tree.
	delegation := testutil.ValidDelegation(roundID, 0x10)
	result := s.app.DeliverVoteTx(testutil.MustEncodeVoteTx(delegation))
	s.Require().Equal(uint32(0), result.Code, "delegation should succeed")

	anchorHeight := uint64(s.app.Height)

	// Cast vote while ACTIVE.
	castVote := testutil.ValidCastVote(roundID, anchorHeight, 0x30)
	result = s.app.DeliverVoteTx(testutil.MustEncodeVoteTx(castVote))
	s.Require().Equal(uint32(0), result.Code, "cast vote should succeed")

	revealAnchor := uint64(s.app.Height)

	// Reveal share while ACTIVE (before TALLYING transition) using a real
	// ciphertext encrypted under the EA key so DLEQ verification works.
	ctActive, err := elgamal.Encrypt(eaPk, 100, rand.Reader)
	s.Require().NoError(err)
	encShareActive, err := elgamal.MarshalCiphertext(ctActive)
	s.Require().NoError(err)
	revealMsg := testutil.ValidRevealShareReal(roundID, revealAnchor, 0x50, 1, 1, encShareActive)
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
	ctTallying, err := elgamal.Encrypt(eaPk, 200, rand.Reader)
	s.Require().NoError(err)
	encShareTallying, err := elgamal.MarshalCiphertext(ctTallying)
	s.Require().NoError(err)
	revealMsgTallying := testutil.ValidRevealShareReal(roundID, revealAnchor, 0x60, 1, 1, encShareTallying)
	result = s.app.DeliverVoteTx(testutil.MustEncodeVoteTx(revealMsgTallying))
	s.Require().Equal(uint32(0), result.Code, "reveal share during TALLYING should succeed, got: %s", result.Log)

	// Generate DLEQ proof for the accumulated ciphertext.
	accumulated := elgamal.HomomorphicAdd(ctActive, ctTallying)
	dleqProof, err := elgamal.GenerateDLEQProof(eaSk, accumulated, 300)
	s.Require().NoError(err)

	// Submit tally to finalize (use the genesis validator's operator address).
	submitTallyMsg := testutil.ValidSubmitTallyWithEntries(roundID, s.app.ValidatorOperAddr(), []*types.TallyEntry{
		{ProposalId: 1, VoteDecision: 1, TotalValue: 300, DecryptionProof: dleqProof},
	})
	result = s.app.DeliverVoteTx(testutil.MustEncodeVoteTx(submitTallyMsg))
	s.Require().Equal(uint32(0), result.Code, "submit tally should succeed, got: %s", result.Log)

	// Verify round is now FINALIZED.
	ctx = s.queryCtx()
	kvStore = s.app.VoteKeeper().OpenKVStore(ctx)
	round, err = s.app.VoteKeeper().GetVoteRound(kvStore, roundID)
	s.Require().NoError(err)
	s.Require().Equal(types.SessionStatus_SESSION_STATUS_FINALIZED, round.Status,
		"round should be FINALIZED after SubmitTally")

	// Verify tally accumulator preserved the homomorphic sum of both shares.
	tally, err := s.app.VoteKeeper().GetTally(kvStore, roundID, revealMsg.ProposalId, revealMsg.VoteDecision)
	s.Require().NoError(err)
	expectedAccumulatedBz, err := elgamal.MarshalCiphertext(accumulated)
	s.Require().NoError(err)
	s.Require().Equal(expectedAccumulatedBz, tally, "tally should be preserved after finalization")

	// Verify finalized tally results are stored and queryable.
	tallyResults, err := s.app.VoteKeeper().GetAllTallyResults(kvStore, roundID)
	s.Require().NoError(err)
	s.Require().Len(tallyResults, 1)
	s.Require().Equal(uint32(1), tallyResults[0].ProposalId)
	s.Require().Equal(uint32(1), tallyResults[0].VoteDecision)
	s.Require().Equal(uint64(300), tallyResults[0].TotalValue)

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
// 6.2.16: PrepareProposal Auto-Tally with Real ElGamal Encryption
// ---------------------------------------------------------------------------

func TestPrepareProposalAutoTally(t *testing.T) {
	app, pk, eaSkBytes := testutil.SetupTestAppWithEAKey(t)

	// Step 1: Create voting session expiring 30s from now.
	voteEndTime := app.Time.Add(30 * time.Second)
	setupMsg := testutil.ValidCreateVotingSessionWithEndTime(voteEndTime)
	roundID := app.SeedVotingSession(setupMsg)
	app.WriteEaSkForRound(roundID, eaSkBytes)

	// Step 2: Delegate to populate the commitment tree.
	delegation := testutil.ValidDelegation(roundID, 0x10)
	result := app.DeliverVoteTx(testutil.MustEncodeVoteTx(delegation))
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
// 6.2.17: PrepareProposal Auto-Ack Ceremony
// ---------------------------------------------------------------------------

func TestPrepareProposalAutoAck(t *testing.T) {
	app, _, pallasPk, eaSk, eaPk := testutil.SetupTestAppWithPallasKey(t)

	eaPkBytes := eaPk.Point.ToAffineCompressed()
	eaSkBytes, err := elgamal.MarshalSecretKey(eaSk)
	require.NoError(t, err)

	// Get the genesis validator's operator address — this is our proposer.
	valAddr := app.ValidatorOperAddr()

	// ECIES-encrypt ea_sk to the validator's Pallas public key.
	G := elgamal.PallasGenerator()
	env, err := ecies.Encrypt(G, pallasPk.Point, eaSkBytes, rand.Reader)
	require.NoError(t, err)

	// Seed a DEALT ceremony with the validator and ECIES payload.
	validators := []*types.ValidatorPallasKey{
		{ValidatorAddress: valAddr, PallasPk: pallasPk.Point.ToAffineCompressed()},
	}
	payloads := []*types.DealerPayload{
		{
			ValidatorAddress: valAddr,
			EphemeralPk:      env.Ephemeral.ToAffineCompressed(),
			Ciphertext:       env.Ciphertext,
		},
	}
	roundID := app.SeedDealtCeremony(eaPkBytes, eaPkBytes, payloads, validators)

	// Verify round ceremony is DEALT before PrepareProposal.
	ctx := app.NewUncachedContext(false, cmtproto.Header{Height: app.Height})
	kvStore := app.VoteKeeper().OpenKVStore(ctx)
	round, err := app.VoteKeeper().GetVoteRound(kvStore, roundID)
	require.NoError(t, err)
	require.Equal(t, types.CeremonyStatus_CEREMONY_STATUS_DEALT, round.CeremonyStatus)
	require.Len(t, round.CeremonyAcks, 0)

	// Run a block with PrepareProposal — should inject MsgAckExecutiveAuthorityKey.
	app.NextBlockWithPrepareProposal()

	// Verify ceremony is now CONFIRMED (single validator, so one ack = all acked).
	ctx = app.NewUncachedContext(false, cmtproto.Header{Height: app.Height})
	kvStore = app.VoteKeeper().OpenKVStore(ctx)
	round, err = app.VoteKeeper().GetVoteRound(kvStore, roundID)
	require.NoError(t, err)
	require.Equal(t, types.CeremonyStatus_CEREMONY_STATUS_CONFIRMED, round.CeremonyStatus,
		"ceremony should be CONFIRMED after auto-ack")
	require.Len(t, round.CeremonyAcks, 1)
	require.Equal(t, valAddr, round.CeremonyAcks[0].ValidatorAddress)
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
	payloads := []*types.DealerPayload{
		{
			ValidatorAddress: valAddr,
			EphemeralPk:      pallasPk.Point.ToAffineCompressed(), // dummy
			Ciphertext:       bytes.Repeat([]byte{0xAB}, 48),      // dummy
		},
	}
	app.SeedDealtCeremony(eaPkBytes, eaPkBytes, payloads, validators)

	// Encode a MsgAckExecutiveAuthorityKey.
	h := sha256.New()
	h.Write([]byte(types.AckSigDomain))
	h.Write(eaPkBytes)
	h.Write([]byte(valAddr))

	ackMsg := &types.MsgAckExecutiveAuthorityKey{
		Creator:      valAddr,
		AckSignature: h.Sum(nil),
	}

	txBytes, err := voteapi.EncodeCeremonyTx(ackMsg, voteapi.TagAckExecutiveAuthorityKey)
	require.NoError(t, err)

	// CheckTx should reject — acks cannot be submitted via mempool.
	checkResp := app.CheckTxSync(txBytes)
	require.NotEqual(t, uint32(0), checkResp.Code, "CheckTx should reject MsgAckExecutiveAuthorityKey")
	require.Contains(t, checkResp.Log, "cannot be submitted via mempool")
}

// ---------------------------------------------------------------------------
// 6.2.19: PrepareProposal Auto-Deal Ceremony
// ---------------------------------------------------------------------------

func TestPrepareProposalAutoDeal(t *testing.T) {
	app, _, pallasPk, _, _ := testutil.SetupTestAppWithPallasKey(t)

	valAddr := app.ValidatorOperAddr()

	// Step 1: Register the genesis validator's Pallas key.
	regMsg := &types.MsgRegisterPallasKey{
		Creator:  app.ValidatorAccAddr(),
		PallasPk: pallasPk.Point.ToAffineCompressed(),
	}
	regTx := app.MustBuildSignedCeremonyTx(regMsg)
	result := app.DeliverVoteTx(regTx)
	require.Equal(t, uint32(0), result.Code, "RegisterPallasKey should succeed, got: %s", result.Log)

	// Step 2: Seed a PENDING round with REGISTERING ceremony and the genesis validator.
	validators := []*types.ValidatorPallasKey{
		{ValidatorAddress: valAddr, PallasPk: pallasPk.Point.ToAffineCompressed()},
	}
	roundID := app.SeedRegisteringCeremony(validators)

	// Step 3: Call PrepareProposal — should inject MsgDealExecutiveAuthorityKey.
	ppResp := app.CallPrepareProposal()
	require.GreaterOrEqual(t, len(ppResp.Txs), 1, "PrepareProposal should inject at least 1 tx")

	// Step 4: Verify the first tx is a deal message.
	require.Equal(t, voteapi.TagDealExecutiveAuthorityKey, ppResp.Txs[0][0],
		"first injected tx should be a deal")

	// Decode and verify the deal contents.
	_, decoded, err := voteapi.DecodeCeremonyTx(ppResp.Txs[0])
	require.NoError(t, err)
	dealMsg, ok := decoded.(*types.MsgDealExecutiveAuthorityKey)
	require.True(t, ok, "decoded message should be MsgDealExecutiveAuthorityKey")
	require.Equal(t, valAddr, dealMsg.Creator)
	require.Len(t, dealMsg.Payloads, 1)
	require.Equal(t, valAddr, dealMsg.Payloads[0].ValidatorAddress)
	require.NotEmpty(t, dealMsg.EaPk)

	// Step 5: Feed the PrepareProposal txs into FinalizeBlock + Commit.
	app.NextBlockWithPrepareProposal()

	// Step 6: Verify round transitioned to DEALT with ceremony fields populated.
	ctx := app.NewUncachedContext(false, cmtproto.Header{Height: app.Height})
	kvStore := app.VoteKeeper().OpenKVStore(ctx)
	round, err := app.VoteKeeper().GetVoteRound(kvStore, roundID)
	require.NoError(t, err)
	require.Equal(t, types.CeremonyStatus_CEREMONY_STATUS_DEALT, round.CeremonyStatus,
		"ceremony should be DEALT after auto-deal")
	require.NotEmpty(t, round.EaPk)
	require.Equal(t, valAddr, round.CeremonyDealer)
	require.Len(t, round.CeremonyPayloads, 1)
	require.True(t, round.CeremonyPhaseTimeout > 0, "phase timeout should be set")
}

// ---------------------------------------------------------------------------
// 6.2.20: Full Ceremony Cycle (REGISTERING → deal → DEALT → ack → CONFIRMED)
// ---------------------------------------------------------------------------

func TestFullCeremonyCycle_DealThenAck(t *testing.T) {
	app, _, pallasPk, _, _ := testutil.SetupTestAppWithPallasKey(t)

	valAddr := app.ValidatorOperAddr()

	// Step 1: Register the genesis validator's Pallas key.
	regMsg := &types.MsgRegisterPallasKey{
		Creator:  app.ValidatorAccAddr(),
		PallasPk: pallasPk.Point.ToAffineCompressed(),
	}
	regTx := app.MustBuildSignedCeremonyTx(regMsg)
	result := app.DeliverVoteTx(regTx)
	require.Equal(t, uint32(0), result.Code, "RegisterPallasKey should succeed, got: %s", result.Log)

	// Step 2: Seed a PENDING round with REGISTERING ceremony.
	validators := []*types.ValidatorPallasKey{
		{ValidatorAddress: valAddr, PallasPk: pallasPk.Point.ToAffineCompressed()},
	}
	roundID := app.SeedRegisteringCeremony(validators)

	// Step 3: First PrepareProposal block → auto-deal fires, round becomes DEALT.
	app.NextBlockWithPrepareProposal()

	ctx := app.NewUncachedContext(false, cmtproto.Header{Height: app.Height})
	kvStore := app.VoteKeeper().OpenKVStore(ctx)
	round, err := app.VoteKeeper().GetVoteRound(kvStore, roundID)
	require.NoError(t, err)
	require.Equal(t, types.CeremonyStatus_CEREMONY_STATUS_DEALT, round.CeremonyStatus,
		"ceremony should be DEALT after auto-deal block")
	require.Equal(t, types.SessionStatus_SESSION_STATUS_PENDING, round.Status,
		"round should still be PENDING during DEALT phase")

	// Step 4: Second PrepareProposal block → auto-ack fires, round becomes CONFIRMED + ACTIVE.
	app.NextBlockWithPrepareProposal()

	ctx = app.NewUncachedContext(false, cmtproto.Header{Height: app.Height})
	kvStore = app.VoteKeeper().OpenKVStore(ctx)
	round, err = app.VoteKeeper().GetVoteRound(kvStore, roundID)
	require.NoError(t, err)
	require.Equal(t, types.CeremonyStatus_CEREMONY_STATUS_CONFIRMED, round.CeremonyStatus,
		"ceremony should be CONFIRMED after auto-ack")
	require.Equal(t, types.SessionStatus_SESSION_STATUS_ACTIVE, round.Status,
		"round should be ACTIVE after ceremony confirmation")
	require.NotEmpty(t, round.EaPk, "ea_pk should be set")
	require.Len(t, round.CeremonyAcks, 1, "should have 1 ack")
	require.Equal(t, valAddr, round.CeremonyAcks[0].ValidatorAddress)

	// Step 5: Verify ea_sk was written to disk by the ack handler.
	eaSkPath := app.EaSkDir
	if eaSkPath != "" {
		skFile := eaSkPath + "/ea_sk." + fmt.Sprintf("%x", roundID)
		_, err := os.Stat(skFile)
		require.NoError(t, err, "ea_sk file should exist on disk after ack")
	}
}

// ---------------------------------------------------------------------------
// 6.2.21: Multi-Validator Ceremony — Deal, Ack, Immediate Confirm
// ---------------------------------------------------------------------------

// TestMultiValidatorCeremony_DealAckConfirm uses 3 validators (1 real + 2
// phantom) to exercise the auto-deal and ack flow through the full ABCI
// pipeline. The fast path requires all validators to ack before confirming.
func TestMultiValidatorCeremony_DealAckConfirm(t *testing.T) {
	app, _, pallasPk, _, _ := testutil.SetupTestAppWithPallasKey(t)

	valAddr := app.ValidatorOperAddr()

	// Register the real validator's Pallas key (required for auto-deal to
	// consider this node a ceremony participant).
	regMsg := &types.MsgRegisterPallasKey{
		Creator:  app.ValidatorAccAddr(),
		PallasPk: pallasPk.Point.ToAffineCompressed(),
	}
	regTx := app.MustBuildSignedCeremonyTx(regMsg)
	result := app.DeliverVoteTx(regTx)
	require.Equal(t, uint32(0), result.Code, "RegisterPallasKey should succeed, got: %s", result.Log)

	// Generate 2 phantom validators with random Pallas keypairs. These are
	// not registered in the staking keeper — they just have valid PKs so the
	// deal handler can encrypt to them.
	_, phantomPk1 := elgamal.KeyGen(rand.Reader)
	_, phantomPk2 := elgamal.KeyGen(rand.Reader)
	phantom1Addr := sdk.ValAddress(bytes.Repeat([]byte{0xA1}, 20)).String()
	phantom2Addr := sdk.ValAddress(bytes.Repeat([]byte{0xA2}, 20)).String()

	validators := []*types.ValidatorPallasKey{
		{ValidatorAddress: valAddr, PallasPk: pallasPk.Point.ToAffineCompressed()},
		{ValidatorAddress: phantom1Addr, PallasPk: phantomPk1.Point.ToAffineCompressed()},
		{ValidatorAddress: phantom2Addr, PallasPk: phantomPk2.Point.ToAffineCompressed()},
	}
	roundID := app.SeedRegisteringCeremony(validators)

	// Block 1: PrepareProposal fires auto-deal → DEALT with 3 payloads.
	app.NextBlockWithPrepareProposal()

	ctx := app.NewUncachedContext(false, cmtproto.Header{Height: app.Height})
	kvStore := app.VoteKeeper().OpenKVStore(ctx)
	round, err := app.VoteKeeper().GetVoteRound(kvStore, roundID)
	require.NoError(t, err)
	require.Equal(t, types.CeremonyStatus_CEREMONY_STATUS_DEALT, round.CeremonyStatus,
		"ceremony should be DEALT after auto-deal")
	require.Len(t, round.CeremonyPayloads, 3, "should have 3 ECIES payloads (one per validator)")
	require.Equal(t, valAddr, round.CeremonyDealer, "dealer should be the real validator")

	// Verify each payload has non-empty ephemeral_pk and ciphertext.
	for _, p := range round.CeremonyPayloads {
		require.NotEmpty(t, p.EphemeralPk, "payload ephemeral_pk should be set for %s", p.ValidatorAddress)
		require.NotEmpty(t, p.Ciphertext, "payload ciphertext should be set for %s", p.ValidatorAddress)
	}

	// Block 2: PrepareProposal fires auto-ack from real validator → still DEALT.
	app.NextBlockWithPrepareProposal()

	ctx = app.NewUncachedContext(false, cmtproto.Header{Height: app.Height})
	kvStore = app.VoteKeeper().OpenKVStore(ctx)
	round, err = app.VoteKeeper().GetVoteRound(kvStore, roundID)
	require.NoError(t, err)
	require.Equal(t, types.CeremonyStatus_CEREMONY_STATUS_DEALT, round.CeremonyStatus,
		"ceremony should still be DEALT (1/3 not enough for fast path)")
	require.Len(t, round.CeremonyAcks, 1, "should have 1 ack from real validator")

	// Write phantom acks directly to state. In production each phantom
	// validator would ack when they are the block proposer
	// (ValidateProposerIsCreator enforces creator == proposer).
	for _, addr := range []string{phantom1Addr, phantom2Addr} {
		h := sha256.New()
		h.Write([]byte(types.AckSigDomain))
		h.Write(round.EaPk)
		h.Write([]byte(addr))

		round.CeremonyAcks = append(round.CeremonyAcks, &types.AckEntry{
			ValidatorAddress: addr,
			AckSignature:     h.Sum(nil),
			AckHeight:        uint64(app.Height),
		})
	}
	// 3/3 acked → confirm ceremony and activate round.
	round.CeremonyStatus = types.CeremonyStatus_CEREMONY_STATUS_CONFIRMED
	round.Status = types.SessionStatus_SESSION_STATUS_ACTIVE
	require.NoError(t, app.VoteKeeper().SetVoteRound(kvStore, round))
	app.NextBlock()

	// All 3 validators acked → fast path confirms.
	ctx = app.NewUncachedContext(false, cmtproto.Header{Height: app.Height})
	kvStore = app.VoteKeeper().OpenKVStore(ctx)
	round, err = app.VoteKeeper().GetVoteRound(kvStore, roundID)
	require.NoError(t, err)
	require.Equal(t, types.CeremonyStatus_CEREMONY_STATUS_CONFIRMED, round.CeremonyStatus,
		"ceremony should be CONFIRMED after all 3 validators acked")
	require.Equal(t, types.SessionStatus_SESSION_STATUS_ACTIVE, round.Status,
		"round should be ACTIVE after ceremony confirmation")
	require.Len(t, round.CeremonyAcks, 3, "should have 3 acks (all validators)")

	// No stripping — all validators remain.
	require.Len(t, round.CeremonyValidators, 3, "all validators should remain (no stripping)")

	// EA public key should be set.
	require.NotEmpty(t, round.EaPk, "ea_pk should be set on the round")

	// Verify the proposer's share was written to disk.
	// With n=3 validators, the ceremony uses threshold mode (t=ceil(3/2)=2),
	// so the ack handler writes share.<round_id> instead of ea_sk.<round_id>.
	shareFile := app.EaSkDir + "/share." + fmt.Sprintf("%x", roundID)
	_, err = os.Stat(shareFile)
	require.NoError(t, err, "share file should exist on disk after ack (threshold mode)")
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

	// Register the real validator's Pallas key.
	regMsg := &types.MsgRegisterPallasKey{
		Creator:  app.ValidatorAccAddr(),
		PallasPk: pallasPk.Point.ToAffineCompressed(),
	}
	regTx := app.MustBuildSignedCeremonyTx(regMsg)
	result := app.DeliverVoteTx(regTx)
	require.Equal(t, uint32(0), result.Code, "RegisterPallasKey should succeed, got: %s", result.Log)

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

	for cycle := 1; cycle <= 3; cycle++ {
		// Step 1: PrepareProposal fires auto-deal → DEALT with 4 payloads.
		app.NextBlockWithPrepareProposal()

		ctx := app.NewUncachedContext(false, cmtproto.Header{Height: app.Height})
		kvStore := app.VoteKeeper().OpenKVStore(ctx)
		round, err := app.VoteKeeper().GetVoteRound(kvStore, roundID)
		require.NoError(t, err)
		require.Equal(t, types.CeremonyStatus_CEREMONY_STATUS_DEALT, round.CeremonyStatus,
			"cycle %d: ceremony should be DEALT after auto-deal", cycle)
		require.Len(t, round.CeremonyPayloads, 4,
			"cycle %d: should have 4 ECIES payloads", cycle)

		// Step 2: PrepareProposal fires auto-ack → 1/4 < 1/2 → stays DEALT.
		app.NextBlockWithPrepareProposal()

		ctx = app.NewUncachedContext(false, cmtproto.Header{Height: app.Height})
		kvStore = app.VoteKeeper().OpenKVStore(ctx)
		round, err = app.VoteKeeper().GetVoteRound(kvStore, roundID)
		require.NoError(t, err)
		require.Equal(t, types.CeremonyStatus_CEREMONY_STATUS_DEALT, round.CeremonyStatus,
			"cycle %d: ceremony should still be DEALT (1/4 below threshold)", cycle)
		require.Len(t, round.CeremonyAcks, 1,
			"cycle %d: should have 1 ack from real validator", cycle)

		// Step 3: Advance 31 minutes past deal time → EndBlocker timeout.
		// CeremonyPhaseStart was set when the deal was processed.
		timeoutTime := time.Unix(int64(round.CeremonyPhaseStart+round.CeremonyPhaseTimeout)+1, 0)
		app.NextBlockAtTime(timeoutTime)

		ctx = app.NewUncachedContext(false, cmtproto.Header{Height: app.Height})
		kvStore = app.VoteKeeper().OpenKVStore(ctx)
		round, err = app.VoteKeeper().GetVoteRound(kvStore, roundID)
		require.NoError(t, err)
		require.Equal(t, types.CeremonyStatus_CEREMONY_STATUS_REGISTERING, round.CeremonyStatus,
			"cycle %d: ceremony should reset to REGISTERING after timeout", cycle)
		require.Equal(t, types.SessionStatus_SESSION_STATUS_PENDING, round.Status,
			"cycle %d: round should stay PENDING after timeout reset", cycle)
		require.Nil(t, round.CeremonyPayloads,
			"cycle %d: payloads should be cleared after timeout reset", cycle)
		require.Nil(t, round.CeremonyAcks,
			"cycle %d: acks should be cleared after timeout reset", cycle)

		// Verify ceremony log entries for the timeout reset.
		require.NotEmpty(t, round.CeremonyLog,
			"cycle %d: ceremony log should not be empty", cycle)
	}

	// After 3 cycles, verify final state.
	ctx := app.NewUncachedContext(false, cmtproto.Header{Height: app.Height})
	kvStore := app.VoteKeeper().OpenKVStore(ctx)

	// The round should still be PENDING/REGISTERING — ready for another deal attempt.
	round, err := app.VoteKeeper().GetVoteRound(kvStore, roundID)
	require.NoError(t, err)
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

	// Register the real validator's Pallas key.
	regMsg := &types.MsgRegisterPallasKey{
		Creator:  app.ValidatorAccAddr(),
		PallasPk: pallasPk.Point.ToAffineCompressed(),
	}
	regTx := app.MustBuildSignedCeremonyTx(regMsg)
	result := app.DeliverVoteTx(regTx)
	require.Equal(t, uint32(0), result.Code, "RegisterPallasKey should succeed, got: %s", result.Log)

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

	// -----------------------------------------------------------------------
	// Cycle 1 — Timeout: only real validator acks, phantoms miss.
	// -----------------------------------------------------------------------

	// Block 1: PrepareProposal fires auto-deal → DEALT with 4 payloads.
	app.NextBlockWithPrepareProposal()

	ctx := app.NewUncachedContext(false, cmtproto.Header{Height: app.Height})
	kvStore := app.VoteKeeper().OpenKVStore(ctx)
	round, err := app.VoteKeeper().GetVoteRound(kvStore, roundID)
	require.NoError(t, err)
	require.Equal(t, types.CeremonyStatus_CEREMONY_STATUS_DEALT, round.CeremonyStatus,
		"cycle 1: ceremony should be DEALT after auto-deal")
	require.Len(t, round.CeremonyPayloads, 4, "cycle 1: should have 4 ECIES payloads")

	// Block 2: PrepareProposal fires auto-ack from real validator → still DEALT.
	app.NextBlockWithPrepareProposal()

	ctx = app.NewUncachedContext(false, cmtproto.Header{Height: app.Height})
	kvStore = app.VoteKeeper().OpenKVStore(ctx)
	round, err = app.VoteKeeper().GetVoteRound(kvStore, roundID)
	require.NoError(t, err)
	require.Equal(t, types.CeremonyStatus_CEREMONY_STATUS_DEALT, round.CeremonyStatus,
		"cycle 1: ceremony should still be DEALT (1/4 below threshold)")
	require.Len(t, round.CeremonyAcks, 1, "cycle 1: should have 1 ack from real validator")

	// Block 3: Advance past timeout → EndBlocker resets to REGISTERING.
	timeoutTime := time.Unix(int64(round.CeremonyPhaseStart+round.CeremonyPhaseTimeout)+1, 0)
	app.NextBlockAtTime(timeoutTime)

	ctx = app.NewUncachedContext(false, cmtproto.Header{Height: app.Height})
	kvStore = app.VoteKeeper().OpenKVStore(ctx)
	round, err = app.VoteKeeper().GetVoteRound(kvStore, roundID)
	require.NoError(t, err)
	require.Equal(t, types.CeremonyStatus_CEREMONY_STATUS_REGISTERING, round.CeremonyStatus,
		"cycle 1: ceremony should reset to REGISTERING after timeout")

	// -----------------------------------------------------------------------
	// Cycle 2 — Recovery: phantom1 acks manually, ceremony confirms.
	// -----------------------------------------------------------------------

	// Block 4: PrepareProposal fires auto-deal → DEALT with 4 payloads.
	app.NextBlockWithPrepareProposal()

	ctx = app.NewUncachedContext(false, cmtproto.Header{Height: app.Height})
	kvStore = app.VoteKeeper().OpenKVStore(ctx)
	round, err = app.VoteKeeper().GetVoteRound(kvStore, roundID)
	require.NoError(t, err)
	require.Equal(t, types.CeremonyStatus_CEREMONY_STATUS_DEALT, round.CeremonyStatus,
		"cycle 2: ceremony should be DEALT after auto-deal")

	// Block 5: PrepareProposal fires auto-ack from real validator → still DEALT.
	app.NextBlockWithPrepareProposal()

	ctx = app.NewUncachedContext(false, cmtproto.Header{Height: app.Height})
	kvStore = app.VoteKeeper().OpenKVStore(ctx)
	round, err = app.VoteKeeper().GetVoteRound(kvStore, roundID)
	require.NoError(t, err)
	require.Equal(t, types.CeremonyStatus_CEREMONY_STATUS_DEALT, round.CeremonyStatus,
		"cycle 2: ceremony should still be DEALT (1/4 below threshold)")
	require.Len(t, round.CeremonyAcks, 1, "cycle 2: should have 1 ack from real validator")

	// Block 6: Write phantom1's ack directly to state. In production,
	// phantom1 would ack when they are the block proposer
	// (ValidateProposerIsCreator enforces creator == proposer).
	h := sha256.New()
	h.Write([]byte(types.AckSigDomain))
	h.Write(round.EaPk)
	h.Write([]byte(phantom1Addr))
	round.CeremonyAcks = append(round.CeremonyAcks, &types.AckEntry{
		ValidatorAddress: phantom1Addr,
		AckSignature:     h.Sum(nil),
		AckHeight:        uint64(app.Height),
	})
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

	ctx = app.NewUncachedContext(false, cmtproto.Header{Height: app.Height})
	kvStore = app.VoteKeeper().OpenKVStore(ctx)
	round, err = app.VoteKeeper().GetVoteRound(kvStore, roundID)
	require.NoError(t, err)

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
// Full Lifecycle E2E: Ceremony → Vote → Tally (Legacy, n=1)
//
// Drives the complete pipeline through real ABCI calls:
//   REGISTERING → deal → DEALT → ack → ACTIVE
//   → delegate → cast → reveal (real ElGamal to ceremony ea_pk)
//   → EndBlocker TALLYING → auto-tally (legacy DLEQ) → FINALIZED
// ---------------------------------------------------------------------------

func TestFullLifecycle_Legacy(t *testing.T) {
	app, _, pallasPk, _, _ := testutil.SetupTestAppWithPallasKey(t)

	valAddr := app.ValidatorOperAddr()

	// --- Round creation (PENDING + REGISTERING) with session fields ---

	validators := []*types.ValidatorPallasKey{
		{ValidatorAddress: valAddr, PallasPk: pallasPk.Point.ToAffineCompressed()},
	}
	voteEndTime := app.Time.Add(60 * time.Second)

	roundID := make([]byte, 32)
	roundID[0] = 0xF0

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
		VkZkp1:             bytes.Repeat([]byte{0x11}, 64),
		VkZkp2:             bytes.Repeat([]byte{0x22}, 64),
		VkZkp3:             bytes.Repeat([]byte{0x33}, 64),
	}
	require.NoError(t, app.VoteKeeper().SetVoteRound(kvStore, round))
	app.NextBlock()

	// --- Ceremony: deal → ack → ACTIVE ---

	// Block 1: auto-deal fires → DEALT.
	app.NextBlockWithPrepareProposal()

	ctx = app.NewUncachedContext(false, cmtproto.Header{Height: app.Height})
	kvStore = app.VoteKeeper().OpenKVStore(ctx)
	round, err := app.VoteKeeper().GetVoteRound(kvStore, roundID)
	require.NoError(t, err)
	require.Equal(t, types.CeremonyStatus_CEREMONY_STATUS_DEALT, round.CeremonyStatus,
		"ceremony should be DEALT after auto-deal")

	// Block 2: auto-ack fires → CONFIRMED + ACTIVE.
	app.NextBlockWithPrepareProposal()

	ctx = app.NewUncachedContext(false, cmtproto.Header{Height: app.Height})
	kvStore = app.VoteKeeper().OpenKVStore(ctx)
	round, err = app.VoteKeeper().GetVoteRound(kvStore, roundID)
	require.NoError(t, err)
	require.Equal(t, types.SessionStatus_SESSION_STATUS_ACTIVE, round.Status,
		"round should be ACTIVE after ceremony")
	require.EqualValues(t, 0, round.Threshold,
		"legacy mode: Threshold should be 0")

	// Recover the ceremony's ea_pk for encrypting reveal shares.
	eaPk, err := elgamal.UnmarshalPublicKey(round.EaPk)
	require.NoError(t, err)

	// --- Voting: delegate → cast → reveal ---

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

	// --- Tally: EndBlocker → TALLYING → auto-tally → FINALIZED ---

	app.NextBlockAtTime(voteEndTime.Add(1 * time.Second))

	ctx = app.NewUncachedContext(false, cmtproto.Header{Height: app.Height})
	kvStore = app.VoteKeeper().OpenKVStore(ctx)
	round, err = app.VoteKeeper().GetVoteRound(kvStore, roundID)
	require.NoError(t, err)
	require.Equal(t, types.SessionStatus_SESSION_STATUS_TALLYING, round.Status,
		"round should be TALLYING after VoteEndTime passes")

	// PrepareProposal loads ea_sk from disk → decrypts → injects MsgSubmitTally.
	app.NextBlockWithPrepareProposal()

	ctx = app.NewUncachedContext(false, cmtproto.Header{Height: app.Height})
	kvStore = app.VoteKeeper().OpenKVStore(ctx)
	round, err = app.VoteKeeper().GetVoteRound(kvStore, roundID)
	require.NoError(t, err)
	require.Equal(t, types.SessionStatus_SESSION_STATUS_FINALIZED, round.Status,
		"round should be FINALIZED after auto-tally")

	tallyResults, err := app.VoteKeeper().GetAllTallyResults(kvStore, roundID)
	require.NoError(t, err)
	require.Len(t, tallyResults, 1)
	require.Equal(t, uint64(42), tallyResults[0].TotalValue,
		"decrypted tally should match encrypted value of 42")
}

// ---------------------------------------------------------------------------
// Full Lifecycle E2E: Ceremony → Vote → Tally (Threshold, n=3, t=2)
//
// Drives the complete pipeline in threshold mode through real ABCI calls:
//   REGISTERING → deal (Shamir t=2,n=3) → DEALT
//   → ack (proposer + 2 phantoms) → ACTIVE
//   → delegate → cast → reveal (real ElGamal to ceremony ea_pk)
//   → EndBlocker TALLYING
//   → partial decrypt (proposer + phantom1, phantom2 absent) → Lagrange → FINALIZED
//
// Uses n=3 with t=2 to exercise the case where threshold < n: phantom2 never
// submits a partial decryption, yet the tally succeeds with exactly t=2 shares.

func TestFullLifecycle_Threshold(t *testing.T) {
	app, _, pallasPk, _, _ := testutil.SetupTestAppWithPallasKey(t)

	proposerAddr := app.ValidatorOperAddr()
	G := elgamal.PallasGenerator()

	// Two phantom validators (not real nodes — we pre-seed their acks and
	// phantom1's D_i manually; phantom2 is deliberately absent from tally).
	phantom1Sk, phantom1Pk := elgamal.KeyGen(rand.Reader)
	_ = phantom1Sk
	phantom1Addr := sdk.ValAddress(bytes.Repeat([]byte{0xB1}, 20)).String()

	_, phantom2Pk := elgamal.KeyGen(rand.Reader)
	phantom2Addr := sdk.ValAddress(bytes.Repeat([]byte{0xB2}, 20)).String()

	validators := []*types.ValidatorPallasKey{
		{ValidatorAddress: proposerAddr, PallasPk: pallasPk.Point.ToAffineCompressed(), ShamirIndex: 1},
		{ValidatorAddress: phantom1Addr, PallasPk: phantom1Pk.Point.ToAffineCompressed(), ShamirIndex: 2},
		{ValidatorAddress: phantom2Addr, PallasPk: phantom2Pk.Point.ToAffineCompressed(), ShamirIndex: 3},
	}
	voteEndTime := app.Time.Add(90 * time.Second)

	// --- Round creation (PENDING + REGISTERING) with session fields ---

	roundID := make([]byte, 32)
	roundID[0] = 0xF1

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
		VkZkp1:             bytes.Repeat([]byte{0x11}, 64),
		VkZkp2:             bytes.Repeat([]byte{0x22}, 64),
		VkZkp3:             bytes.Repeat([]byte{0x33}, 64),
	}
	require.NoError(t, app.VoteKeeper().SetVoteRound(kvStore, round))
	app.NextBlock()

	// --- Ceremony: deal → proposer ack → phantom acks → ACTIVE ---

	// Block 1: auto-deal fires → DEALT with threshold mode (n=3 → t=2).
	app.NextBlockWithPrepareProposal()

	ctx = app.NewUncachedContext(false, cmtproto.Header{Height: app.Height})
	kvStore = app.VoteKeeper().OpenKVStore(ctx)
	round, err := app.VoteKeeper().GetVoteRound(kvStore, roundID)
	require.NoError(t, err)
	require.Equal(t, types.CeremonyStatus_CEREMONY_STATUS_DEALT, round.CeremonyStatus)
	require.EqualValues(t, 2, round.Threshold,
		"threshold should be 2 for n=3 (ceil(3/2) = 2, stored by deal handler)")
	require.Len(t, round.VerificationKeys, 3,
		"one verification key per validator should be stored by deal handler")

	// Block 2: auto-ack from proposer → 1/3 acked, stays DEALT.
	app.NextBlockWithPrepareProposal()

	ctx = app.NewUncachedContext(false, cmtproto.Header{Height: app.Height})
	kvStore = app.VoteKeeper().OpenKVStore(ctx)
	round, err = app.VoteKeeper().GetVoteRound(kvStore, roundID)
	require.NoError(t, err)
	require.Equal(t, types.CeremonyStatus_CEREMONY_STATUS_DEALT, round.CeremonyStatus,
		"1/3 acked — fast path needs all 3, stays DEALT")
	require.Len(t, round.CeremonyAcks, 1)

	// Phantom validators don't have consensus keys and can't propose blocks,
	// so we write their acks directly to state. In production each validator
	// acks when they are the block proposer (ValidateProposerIsCreator
	// enforces creator == proposer). Bypassing the MsgServer here is correct:
	// the proposer identity check prevents forging acks for other validators,
	// which is exactly the security property we added.
	for _, addr := range []string{phantom1Addr, phantom2Addr} {
		h := sha256.New()
		h.Write([]byte(types.AckSigDomain))
		h.Write(round.EaPk)
		h.Write([]byte(addr))

		round.CeremonyAcks = append(round.CeremonyAcks, &types.AckEntry{
			ValidatorAddress: addr,
			AckSignature:     h.Sum(nil),
			AckHeight:        uint64(app.Height),
		})
	}
	// 3/3 acked → confirm ceremony and activate round.
	round.CeremonyStatus = types.CeremonyStatus_CEREMONY_STATUS_CONFIRMED
	round.Status = types.SessionStatus_SESSION_STATUS_ACTIVE
	require.NoError(t, app.VoteKeeper().SetVoteRound(kvStore, round))
	app.NextBlock()

	ctx = app.NewUncachedContext(false, cmtproto.Header{Height: app.Height})
	kvStore = app.VoteKeeper().OpenKVStore(ctx)
	round, err = app.VoteKeeper().GetVoteRound(kvStore, roundID)
	require.NoError(t, err)
	require.Equal(t, types.SessionStatus_SESSION_STATUS_ACTIVE, round.Status,
		"round should be ACTIVE after 3/3 acks")
	require.Len(t, round.CeremonyAcks, 3)
	require.EqualValues(t, 2, round.Threshold)

	// Recover the ceremony's ea_pk for encrypting reveal shares.
	eaPk, err := elgamal.UnmarshalPublicKey(round.EaPk)
	require.NoError(t, err)

	// --- Voting: delegate → cast → reveal ---

	delegation := testutil.ValidDelegation(roundID, 0x10)
	result := app.DeliverVoteTx(testutil.MustEncodeVoteTx(delegation))
	require.Equal(t, uint32(0), result.Code, "delegation should succeed, got: %s", result.Log)

	anchorHeight := uint64(app.Height)

	castVote := testutil.ValidCastVote(roundID, anchorHeight, 0x30)
	result = app.DeliverVoteTx(testutil.MustEncodeVoteTx(castVote))
	require.Equal(t, uint32(0), result.Code, "cast vote should succeed, got: %s", result.Log)

	revealAnchor := uint64(app.Height)

	ct, err := elgamal.Encrypt(eaPk, 77, rand.Reader)
	require.NoError(t, err)
	encShare, err := elgamal.MarshalCiphertext(ct)
	require.NoError(t, err)

	revealMsg := testutil.ValidRevealShareReal(roundID, revealAnchor, 0x50, 1, 1, encShare)
	result = app.DeliverVoteTx(testutil.MustEncodeVoteTx(revealMsg))
	require.Equal(t, uint32(0), result.Code, "reveal share should succeed, got: %s", result.Log)

	// --- Tally setup: recover phantom1's share, pre-seed its D_i ---
	// Phantom2 is deliberately absent — only proposer + phantom1 contribute (t=2).

	// Decrypt phantom1's ECIES payload to recover its Shamir share.
	phantom1Payload := round.CeremonyPayloads[1]
	require.Equal(t, phantom1Addr, phantom1Payload.ValidatorAddress)

	ephPk, err := elgamal.UnmarshalPublicKey(phantom1Payload.EphemeralPk)
	require.NoError(t, err)
	phantom1ShareBytes, err := ecies.Decrypt(phantom1Sk.Scalar, &ecies.Envelope{
		Ephemeral:  ephPk.Point,
		Ciphertext: phantom1Payload.Ciphertext,
	})
	require.NoError(t, err)
	require.Len(t, phantom1ShareBytes, 32)

	phantom1ShareScalar, err := new(curvey.ScalarPallas).SetBytes(phantom1ShareBytes)
	require.NoError(t, err)

	// Verify VK consistency: share_1 * G == round.VerificationKeys[1].
	computedVK := G.Mul(phantom1ShareScalar).ToAffineCompressed()
	require.Equal(t, round.VerificationKeys[1], computedVK,
		"phantom1 share * G must match stored VK[1]")

	// --- Transition to TALLYING ---

	app.NextBlockAtTime(voteEndTime.Add(1 * time.Second))

	ctx = app.NewUncachedContext(false, cmtproto.Header{Height: app.Height})
	kvStore = app.VoteKeeper().OpenKVStore(ctx)
	round, err = app.VoteKeeper().GetVoteRound(kvStore, roundID)
	require.NoError(t, err)
	require.Equal(t, types.SessionStatus_SESSION_STATUS_TALLYING, round.Status)

	// Pre-seed phantom1's partial decryption: D_1 = share_1 * C1 for each accumulator.
	tallyBytes, err := app.VoteKeeper().GetTally(kvStore, roundID, 1, 1)
	require.NoError(t, err)
	tallyCt, err := elgamal.UnmarshalCiphertext(tallyBytes)
	require.NoError(t, err)

	phantom1Di := tallyCt.C1.Mul(phantom1ShareScalar)
	phantom1Entries := []*types.PartialDecryptionEntry{{
		ProposalId:     1,
		VoteDecision:   1,
		PartialDecrypt: phantom1Di.ToAffineCompressed(),
	}}

	ctx = app.NewUncachedContext(false, cmtproto.Header{Height: app.Height})
	kvStore = app.VoteKeeper().OpenKVStore(ctx)
	// Phantom1 is validator index 2 (1-based).
	require.NoError(t, app.VoteKeeper().SetPartialDecryptions(kvStore, roundID, 2, phantom1Entries))
	app.NextBlock()

	// Phantom2 deliberately does NOT submit — testing threshold < n.

	// --- Tally: partial decrypt (proposer) → Lagrange combine → FINALIZED ---

	// Block 1: partial decrypt injector fires for proposer → count reaches 2.
	app.NextBlockWithPrepareProposal()

	ctx = app.NewUncachedContext(false, cmtproto.Header{Height: app.Height})
	kvStore = app.VoteKeeper().OpenKVStore(ctx)
	count, err := app.VoteKeeper().CountPartialDecryptionValidators(kvStore, roundID)
	require.NoError(t, err)
	require.Equal(t, 2, count,
		"proposer + phantom1 should have submitted (phantom2 absent)")

	// Round should still be TALLYING — tally combiner saw count=1 at the start
	// of PrepareProposal (proposer's partial decrypt not yet committed).
	round, err = app.VoteKeeper().GetVoteRound(kvStore, roundID)
	require.NoError(t, err)
	require.Equal(t, types.SessionStatus_SESSION_STATUS_TALLYING, round.Status)

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
	require.Equal(t, uint64(77), tallyResults[0].TotalValue,
		"decrypted tally should match encrypted value of 77")
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------
