package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/z-cale/zally/x/vote/types"
)

var _ types.QueryServer = queryServer{}

type queryServer struct {
	types.UnimplementedQueryServer
	k Keeper
}

// NewQueryServerImpl returns an implementation of the vote QueryServer interface.
func NewQueryServerImpl(keeper Keeper) types.QueryServer {
	return &queryServer{k: keeper}
}

// CommitmentTreeAtHeight returns the commitment tree root at a specific anchor height.
func (qs queryServer) CommitmentTreeAtHeight(goCtx context.Context, req *types.QueryCommitmentTreeRequest) (*types.QueryCommitmentTreeResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "empty request")
	}

	ctx := sdk.UnwrapSDKContext(goCtx)
	kvStore := qs.k.OpenKVStore(ctx)

	root, err := qs.k.GetCommitmentRootAtHeight(kvStore, req.Height)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to get root: %v", err)
	}
	if root == nil {
		return nil, status.Errorf(codes.NotFound, "no commitment root at height %d", req.Height)
	}

	// Derive next_index at this height from the block-to-leaf-index mapping.
	// EndBlocker stores (startIndex, count) per height; next_index = start + count.
	var nextIndex uint64
	startIndex, count, found, err := qs.k.GetBlockLeafIndex(kvStore, req.Height)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to get block leaf index: %v", err)
	}
	if found {
		nextIndex = startIndex + count
	}

	return &types.QueryCommitmentTreeResponse{
		Tree: &types.CommitmentTreeState{
			Root:      root,
			Height:    req.Height,
			NextIndex: nextIndex,
		},
	}, nil
}

// LatestCommitmentTree returns the latest commitment tree state including
// the current root, height, and next leaf index.
func (qs queryServer) LatestCommitmentTree(goCtx context.Context, req *types.QueryLatestTreeRequest) (*types.QueryLatestTreeResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "empty request")
	}

	ctx := sdk.UnwrapSDKContext(goCtx)
	kvStore := qs.k.OpenKVStore(ctx)

	state, err := qs.k.GetCommitmentTreeState(kvStore)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to get tree state: %v", err)
	}

	return &types.QueryLatestTreeResponse{Tree: state}, nil
}

// VoteRound returns information about a specific vote round.
func (qs queryServer) VoteRound(goCtx context.Context, req *types.QueryVoteRoundRequest) (*types.QueryVoteRoundResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "empty request")
	}
	if len(req.VoteRoundId) == 0 {
		return nil, status.Error(codes.InvalidArgument, "vote_round_id is required")
	}

	ctx := sdk.UnwrapSDKContext(goCtx)
	kvStore := qs.k.OpenKVStore(ctx)

	round, err := qs.k.GetVoteRound(kvStore, req.VoteRoundId)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "vote round not found: %v", err)
	}

	return &types.QueryVoteRoundResponse{Round: round}, nil
}

// ProposalTally returns the accumulated tally for a proposal within a vote round.
// The tally maps each vote decision to the total accumulated vote amount.
func (qs queryServer) ProposalTally(goCtx context.Context, req *types.QueryProposalTallyRequest) (*types.QueryProposalTallyResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "empty request")
	}
	if len(req.VoteRoundId) == 0 {
		return nil, status.Error(codes.InvalidArgument, "vote_round_id is required")
	}

	ctx := sdk.UnwrapSDKContext(goCtx)
	kvStore := qs.k.OpenKVStore(ctx)

	tally, err := qs.k.GetProposalTally(kvStore, req.VoteRoundId, req.ProposalId)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to get tally: %v", err)
	}

	return &types.QueryProposalTallyResponse{Tally: tally}, nil
}

// TallyResults returns finalized tally results for a vote round (after MsgSubmitTally).
func (qs queryServer) TallyResults(goCtx context.Context, req *types.QueryTallyResultsRequest) (*types.QueryTallyResultsResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "empty request")
	}
	if len(req.VoteRoundId) == 0 {
		return nil, status.Error(codes.InvalidArgument, "vote_round_id is required")
	}

	ctx := sdk.UnwrapSDKContext(goCtx)
	kvStore := qs.k.OpenKVStore(ctx)

	results, err := qs.k.GetAllTallyResults(kvStore, req.VoteRoundId)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to get tally results: %v", err)
	}

	return &types.QueryTallyResultsResponse{Results: results}, nil
}

// CommitmentLeaves returns the commitment tree leaves organized by block height.
// This endpoint enables remote clients implementing TreeSyncApi to sync the
// vote commitment tree without parsing full Cosmos blocks.
func (qs queryServer) CommitmentLeaves(goCtx context.Context, req *types.QueryCommitmentLeavesRequest) (*types.QueryCommitmentLeavesResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "empty request")
	}
	if req.ToHeight < req.FromHeight {
		return nil, status.Errorf(codes.InvalidArgument, "to_height (%d) must be >= from_height (%d)", req.ToHeight, req.FromHeight)
	}

	ctx := sdk.UnwrapSDKContext(goCtx)
	kvStore := qs.k.OpenKVStore(ctx)

	blocks, err := qs.k.GetCommitmentLeaves(kvStore, req.FromHeight, req.ToHeight)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to get commitment leaves: %v", err)
	}

	return &types.QueryCommitmentLeavesResponse{Blocks: blocks}, nil
}
