package app_test

import (
	"encoding/binary"
	"testing"

	"github.com/stretchr/testify/suite"
	"golang.org/x/crypto/blake2b"

	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"

	sdk "github.com/cosmos/cosmos-sdk/types"

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

	// Verify commitment tree advanced by 2 (cmx_new + gov_comm).
	treeState, err := s.app.VoteKeeper().GetCommitmentTreeState(kvStore)
	s.Require().NoError(err)
	s.Require().Equal(uint64(2), treeState.NextIndex)

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

	// Tree advanced by 2 more (vote_authority_note_new + vote_commitment).
	treeState, err = s.app.VoteKeeper().GetCommitmentTreeState(kvStore)
	s.Require().NoError(err)
	s.Require().Equal(uint64(4), treeState.NextIndex)

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
	s.Require().Equal(revealMsg.VoteAmount, tally)

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
