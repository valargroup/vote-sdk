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

	return &types.QueryCommitmentTreeResponse{
		Tree: &types.CommitmentTreeState{
			Root:   root,
			Height: req.Height,
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
