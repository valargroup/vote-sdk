package keeper_test

import (
	"bytes"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"cosmossdk.io/log"
	storetypes "cosmossdk.io/store/types"

	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/cosmos/cosmos-sdk/testutil"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/z-cale/zally/x/vote/keeper"
	"github.com/z-cale/zally/x/vote/types"
)

// ---------------------------------------------------------------------------
// Query server test suite
// ---------------------------------------------------------------------------

type QueryServerTestSuite struct {
	suite.Suite
	ctx         sdk.Context
	keeper      keeper.Keeper
	queryServer types.QueryServer
	msgServer   types.MsgServer
}

func TestQueryServerTestSuite(t *testing.T) {
	suite.Run(t, new(QueryServerTestSuite))
}

func (s *QueryServerTestSuite) SetupTest() {
	key := storetypes.NewKVStoreKey(types.StoreKey)
	tkey := storetypes.NewTransientStoreKey("transient_test")
	testCtx := testutil.DefaultContextWithDB(s.T(), key, tkey)

	s.ctx = testCtx.Ctx.WithBlockTime(time.Unix(1_000_000, 0).UTC()).WithBlockHeight(10)
	storeService := runtime.NewKVStoreService(key)
	s.keeper = keeper.NewKeeper(storeService, "zvote1authority", log.NewNopLogger(), nil)
	s.queryServer = keeper.NewQueryServerImpl(s.keeper)
	s.msgServer = keeper.NewMsgServerImpl(s.keeper)
}

// ---------------------------------------------------------------------------
// CommitmentTreeAtHeight
// ---------------------------------------------------------------------------

func (s *QueryServerTestSuite) TestCommitmentTreeAtHeight_NilRequest() {
	_, err := s.queryServer.CommitmentTreeAtHeight(s.ctx, nil)
	s.Require().Error(err)
	s.Require().Equal(codes.InvalidArgument, status.Code(err))
}

func (s *QueryServerTestSuite) TestCommitmentTreeAtHeight_NotFound() {
	_, err := s.queryServer.CommitmentTreeAtHeight(s.ctx, &types.QueryCommitmentTreeRequest{Height: 999})
	s.Require().Error(err)
	s.Require().Equal(codes.NotFound, status.Code(err))
}

func (s *QueryServerTestSuite) TestCommitmentTreeAtHeight_Found() {
	// Store a root at height 50 with a block-leaf-index mapping.
	kvStore := s.keeper.OpenKVStore(s.ctx)
	root := bytes.Repeat([]byte{0xAB}, 32)
	s.Require().NoError(s.keeper.SetCommitmentRootAtHeight(kvStore, 50, root))
	s.Require().NoError(s.keeper.SetBlockLeafIndex(kvStore, 50, 0, 2))

	resp, err := s.queryServer.CommitmentTreeAtHeight(s.ctx, &types.QueryCommitmentTreeRequest{Height: 50})
	s.Require().NoError(err)
	s.Require().NotNil(resp.Tree)
	s.Require().Equal(root, resp.Tree.Root)
	s.Require().Equal(uint64(50), resp.Tree.Height)
	s.Require().Equal(uint64(2), resp.Tree.NextIndex)
}

// ---------------------------------------------------------------------------
// LatestCommitmentTree
// ---------------------------------------------------------------------------

func (s *QueryServerTestSuite) TestLatestCommitmentTree_NilRequest() {
	_, err := s.queryServer.LatestCommitmentTree(s.ctx, nil)
	s.Require().Error(err)
	s.Require().Equal(codes.InvalidArgument, status.Code(err))
}

func (s *QueryServerTestSuite) TestLatestCommitmentTree_EmptyTree() {
	resp, err := s.queryServer.LatestCommitmentTree(s.ctx, &types.QueryLatestTreeRequest{})
	s.Require().NoError(err)
	s.Require().NotNil(resp.Tree)
	s.Require().Equal(uint64(0), resp.Tree.NextIndex)
	s.Require().Nil(resp.Tree.Root)
}

func (s *QueryServerTestSuite) TestLatestCommitmentTree_WithCommitments() {
	kvStore := s.keeper.OpenKVStore(s.ctx)

	// Append some commitments (canonical Pallas Fp encodings for votetree FFI).
	_, err := s.keeper.AppendCommitment(kvStore, fpLeaf(1))
	s.Require().NoError(err)
	_, err = s.keeper.AppendCommitment(kvStore, fpLeaf(2))
	s.Require().NoError(err)

	// Compute and store root (simulating EndBlocker).
	state, err := s.keeper.GetCommitmentTreeState(kvStore)
	s.Require().NoError(err)
	root, err := s.keeper.ComputeTreeRoot(kvStore, state.NextIndex)
	s.Require().NoError(err)
	state.Root = root
	state.Height = 10
	s.Require().NoError(s.keeper.SetCommitmentTreeState(kvStore, state))

	resp, err := s.queryServer.LatestCommitmentTree(s.ctx, &types.QueryLatestTreeRequest{})
	s.Require().NoError(err)
	s.Require().NotNil(resp.Tree)
	s.Require().Equal(uint64(2), resp.Tree.NextIndex)
	s.Require().NotNil(resp.Tree.Root)
	s.Require().Equal(uint64(10), resp.Tree.Height)
}

// ---------------------------------------------------------------------------
// VoteRound
// ---------------------------------------------------------------------------

func (s *QueryServerTestSuite) TestVoteRound_NilRequest() {
	_, err := s.queryServer.VoteRound(s.ctx, nil)
	s.Require().Error(err)
	s.Require().Equal(codes.InvalidArgument, status.Code(err))
}

func (s *QueryServerTestSuite) TestVoteRound_EmptyID() {
	_, err := s.queryServer.VoteRound(s.ctx, &types.QueryVoteRoundRequest{})
	s.Require().Error(err)
	s.Require().Equal(codes.InvalidArgument, status.Code(err))
}

func (s *QueryServerTestSuite) TestVoteRound_NotFound() {
	_, err := s.queryServer.VoteRound(s.ctx, &types.QueryVoteRoundRequest{
		VoteRoundId: bytes.Repeat([]byte{0xFF}, 32),
	})
	s.Require().Error(err)
	s.Require().Equal(codes.NotFound, status.Code(err))
}

func (s *QueryServerTestSuite) TestVoteRound_Found() {
	// Create a vote round via MsgServer.
	resp, err := s.msgServer.CreateVotingSession(s.ctx, &types.MsgCreateVotingSession{
		Creator:           "zvote1creator",
		SnapshotHeight:    100,
		SnapshotBlockhash: bytes.Repeat([]byte{0x01}, 32),
		ProposalsHash:     bytes.Repeat([]byte{0x02}, 32),
		VoteEndTime:       2_000_000,
		NullifierImtRoot:  bytes.Repeat([]byte{0x03}, 32),
		NcRoot:            bytes.Repeat([]byte{0x04}, 32),
	})
	s.Require().NoError(err)
	roundID := resp.VoteRoundId

	// Query it.
	qResp, err := s.queryServer.VoteRound(s.ctx, &types.QueryVoteRoundRequest{
		VoteRoundId: roundID,
	})
	s.Require().NoError(err)
	s.Require().NotNil(qResp.Round)
	s.Require().Equal(roundID, qResp.Round.VoteRoundId)
	s.Require().Equal("zvote1creator", qResp.Round.Creator)
	s.Require().Equal(uint64(100), qResp.Round.SnapshotHeight)
	s.Require().Equal(uint64(2_000_000), qResp.Round.VoteEndTime)
}

// ---------------------------------------------------------------------------
// ProposalTally
// ---------------------------------------------------------------------------

func (s *QueryServerTestSuite) TestProposalTally_NilRequest() {
	_, err := s.queryServer.ProposalTally(s.ctx, nil)
	s.Require().Error(err)
	s.Require().Equal(codes.InvalidArgument, status.Code(err))
}

func (s *QueryServerTestSuite) TestProposalTally_EmptyRoundID() {
	_, err := s.queryServer.ProposalTally(s.ctx, &types.QueryProposalTallyRequest{})
	s.Require().Error(err)
	s.Require().Equal(codes.InvalidArgument, status.Code(err))
}

func (s *QueryServerTestSuite) TestProposalTally_NoVotes() {
	roundID := bytes.Repeat([]byte{0x01}, 32)
	resp, err := s.queryServer.ProposalTally(s.ctx, &types.QueryProposalTallyRequest{
		VoteRoundId: roundID,
		ProposalId:  1,
	})
	s.Require().NoError(err)
	s.Require().NotNil(resp.Tally)
	s.Require().Empty(resp.Tally)
}

func (s *QueryServerTestSuite) TestProposalTally_WithVotes() {
	roundID := bytes.Repeat([]byte{0x01}, 32)
	kvStore := s.keeper.OpenKVStore(s.ctx)

	// Use 64-byte ciphertext stubs for tally entries.
	ct0 := bytes.Repeat([]byte{0xA1}, 64)
	ct1 := bytes.Repeat([]byte{0xA2}, 64)
	ct2 := bytes.Repeat([]byte{0xA3}, 64)

	// Add tallies for proposal 1: decision 0 and decision 1.
	s.Require().NoError(s.keeper.AddToTally(kvStore, roundID, 1, 0, ct0))
	s.Require().NoError(s.keeper.AddToTally(kvStore, roundID, 1, 1, ct1))
	// Add tally for proposal 2 (should NOT appear in proposal 1 query).
	s.Require().NoError(s.keeper.AddToTally(kvStore, roundID, 2, 0, ct2))

	resp, err := s.queryServer.ProposalTally(s.ctx, &types.QueryProposalTallyRequest{
		VoteRoundId: roundID,
		ProposalId:  1,
	})
	s.Require().NoError(err)
	s.Require().NotNil(resp.Tally)
	s.Require().Len(resp.Tally, 2)
	s.Require().Equal(ct0, resp.Tally[0])
	s.Require().Equal(ct1, resp.Tally[1])
}

func (s *QueryServerTestSuite) TestProposalTally_AccumulatesMultipleAdds() {
	roundID := bytes.Repeat([]byte{0x01}, 32)
	kvStore := s.keeper.OpenKVStore(s.ctx)

	// First ciphertext stored directly; second would HomomorphicAdd.
	// We test with the same stub which stores on first add.
	ct := bytes.Repeat([]byte{0xB1}, 64)
	s.Require().NoError(s.keeper.AddToTally(kvStore, roundID, 1, 0, ct))

	resp, err := s.queryServer.ProposalTally(s.ctx, &types.QueryProposalTallyRequest{
		VoteRoundId: roundID,
		ProposalId:  1,
	})
	s.Require().NoError(err)
	s.Require().Equal(ct, resp.Tally[0])
}

// ---------------------------------------------------------------------------
// CommitmentLeaves
// ---------------------------------------------------------------------------

// fpLeaf returns a 32-byte little-endian Pallas Fp encoding of a small integer.
func fpLeaf(v byte) []byte {
	leaf := make([]byte, 32)
	leaf[0] = v
	return leaf
}

func (s *QueryServerTestSuite) TestCommitmentLeaves_NilRequest() {
	_, err := s.queryServer.CommitmentLeaves(s.ctx, nil)
	s.Require().Error(err)
	s.Require().Equal(codes.InvalidArgument, status.Code(err))
}

func (s *QueryServerTestSuite) TestCommitmentLeaves_InvalidRange() {
	_, err := s.queryServer.CommitmentLeaves(s.ctx, &types.QueryCommitmentLeavesRequest{
		FromHeight: 10,
		ToHeight:   5,
	})
	s.Require().Error(err)
	s.Require().Equal(codes.InvalidArgument, status.Code(err))
}

func (s *QueryServerTestSuite) TestCommitmentLeaves_EmptyRange() {
	// No leaves have been stored, so the response should be empty.
	resp, err := s.queryServer.CommitmentLeaves(s.ctx, &types.QueryCommitmentLeavesRequest{
		FromHeight: 1,
		ToHeight:   10,
	})
	s.Require().NoError(err)
	s.Require().Empty(resp.Blocks)
}

func (s *QueryServerTestSuite) TestCommitmentLeaves_SingleBlock() {
	kvStore := s.keeper.OpenKVStore(s.ctx)

	// Append 2 leaves and record block leaf index at height 5.
	leaf0 := fpLeaf(0x10)
	leaf1 := fpLeaf(0x20)
	_, err := s.keeper.AppendCommitment(kvStore, leaf0)
	s.Require().NoError(err)
	_, err = s.keeper.AppendCommitment(kvStore, leaf1)
	s.Require().NoError(err)

	s.Require().NoError(s.keeper.SetBlockLeafIndex(kvStore, 5, 0, 2))

	resp, err := s.queryServer.CommitmentLeaves(s.ctx, &types.QueryCommitmentLeavesRequest{
		FromHeight: 1,
		ToHeight:   10,
	})
	s.Require().NoError(err)
	s.Require().Len(resp.Blocks, 1)
	s.Require().Equal(uint64(5), resp.Blocks[0].Height)
	s.Require().Equal(uint64(0), resp.Blocks[0].StartIndex)
	s.Require().Len(resp.Blocks[0].Leaves, 2)
	s.Require().Equal(leaf0, resp.Blocks[0].Leaves[0])
	s.Require().Equal(leaf1, resp.Blocks[0].Leaves[1])
}

func (s *QueryServerTestSuite) TestCommitmentLeaves_MultipleBlocks() {
	kvStore := s.keeper.OpenKVStore(s.ctx)

	// Block 5: 2 leaves (index 0, 1)
	_, err := s.keeper.AppendCommitment(kvStore, fpLeaf(0x01))
	s.Require().NoError(err)
	_, err = s.keeper.AppendCommitment(kvStore, fpLeaf(0x02))
	s.Require().NoError(err)
	s.Require().NoError(s.keeper.SetBlockLeafIndex(kvStore, 5, 0, 2))

	// Block 8: 1 leaf (index 2)
	_, err = s.keeper.AppendCommitment(kvStore, fpLeaf(0x03))
	s.Require().NoError(err)
	s.Require().NoError(s.keeper.SetBlockLeafIndex(kvStore, 8, 2, 1))

	// Block 12: 3 leaves (index 3, 4, 5)
	_, err = s.keeper.AppendCommitment(kvStore, fpLeaf(0x04))
	s.Require().NoError(err)
	_, err = s.keeper.AppendCommitment(kvStore, fpLeaf(0x05))
	s.Require().NoError(err)
	_, err = s.keeper.AppendCommitment(kvStore, fpLeaf(0x06))
	s.Require().NoError(err)
	s.Require().NoError(s.keeper.SetBlockLeafIndex(kvStore, 12, 3, 3))

	// Query all blocks.
	resp, err := s.queryServer.CommitmentLeaves(s.ctx, &types.QueryCommitmentLeavesRequest{
		FromHeight: 1,
		ToHeight:   20,
	})
	s.Require().NoError(err)
	s.Require().Len(resp.Blocks, 3)

	s.Require().Equal(uint64(5), resp.Blocks[0].Height)
	s.Require().Equal(uint64(0), resp.Blocks[0].StartIndex)
	s.Require().Len(resp.Blocks[0].Leaves, 2)

	s.Require().Equal(uint64(8), resp.Blocks[1].Height)
	s.Require().Equal(uint64(2), resp.Blocks[1].StartIndex)
	s.Require().Len(resp.Blocks[1].Leaves, 1)

	s.Require().Equal(uint64(12), resp.Blocks[2].Height)
	s.Require().Equal(uint64(3), resp.Blocks[2].StartIndex)
	s.Require().Len(resp.Blocks[2].Leaves, 3)

	// Query subset: only block 8.
	resp, err = s.queryServer.CommitmentLeaves(s.ctx, &types.QueryCommitmentLeavesRequest{
		FromHeight: 6,
		ToHeight:   10,
	})
	s.Require().NoError(err)
	s.Require().Len(resp.Blocks, 1)
	s.Require().Equal(uint64(8), resp.Blocks[0].Height)
}

// ---------------------------------------------------------------------------
// BlockLeafIndex keeper tests
// ---------------------------------------------------------------------------

func (s *QueryServerTestSuite) TestBlockLeafIndex_SetAndGet() {
	kvStore := s.keeper.OpenKVStore(s.ctx)

	s.Require().NoError(s.keeper.SetBlockLeafIndex(kvStore, 10, 0, 3))

	start, count, found, err := s.keeper.GetBlockLeafIndex(kvStore, 10)
	s.Require().NoError(err)
	s.Require().True(found)
	s.Require().Equal(uint64(0), start)
	s.Require().Equal(uint64(3), count)
}

func (s *QueryServerTestSuite) TestBlockLeafIndex_NotFound() {
	kvStore := s.keeper.OpenKVStore(s.ctx)

	_, _, found, err := s.keeper.GetBlockLeafIndex(kvStore, 999)
	s.Require().NoError(err)
	s.Require().False(found)
}
