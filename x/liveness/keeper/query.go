package keeper

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"cosmossdk.io/store/prefix"

	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/cosmos/cosmos-sdk/types/query"
	slashingtypes "github.com/cosmos/cosmos-sdk/x/slashing/types"
	"github.com/valargroup/vote-sdk/x/liveness/types"
)

var _ slashingtypes.QueryServer = Keeper{}

func (k Keeper) Params(ctx context.Context, req *slashingtypes.QueryParamsRequest) (*slashingtypes.QueryParamsResponse, error) {
	if req == nil {
		return nil, status.Errorf(codes.InvalidArgument, "empty request")
	}
	params, err := k.GetParams(ctx)
	return &slashingtypes.QueryParamsResponse{Params: params}, err
}

func (k Keeper) SigningInfo(ctx context.Context, req *slashingtypes.QuerySigningInfoRequest) (*slashingtypes.QuerySigningInfoResponse, error) {
	if req == nil {
		return nil, status.Errorf(codes.InvalidArgument, "empty request")
	}
	if req.ConsAddress == "" {
		return nil, status.Errorf(codes.InvalidArgument, "invalid request")
	}
	consAddr, err := k.sk.ConsensusAddressCodec().StringToBytes(req.ConsAddress)
	if err != nil {
		return nil, err
	}
	signingInfo, err := k.GetValidatorSigningInfo(ctx, consAddr)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "SigningInfo not found for validator %s", req.ConsAddress)
	}
	return &slashingtypes.QuerySigningInfoResponse{ValSigningInfo: signingInfo}, nil
}

func (k Keeper) SigningInfos(ctx context.Context, req *slashingtypes.QuerySigningInfosRequest) (*slashingtypes.QuerySigningInfosResponse, error) {
	if req == nil {
		return nil, status.Errorf(codes.InvalidArgument, "empty request")
	}
	store := k.storeService.OpenKVStore(ctx)
	var signInfos []slashingtypes.ValidatorSigningInfo
	sigInfoStore := prefix.NewStore(runtime.KVStoreAdapter(store), types.ValidatorSigningInfoKeyPrefix)
	pageRes, err := query.Paginate(sigInfoStore, req.Pagination, func(_, value []byte) error {
		var info slashingtypes.ValidatorSigningInfo
		if err := k.cdc.Unmarshal(value, &info); err != nil {
			return err
		}
		signInfos = append(signInfos, info)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &slashingtypes.QuerySigningInfosResponse{Info: signInfos, Pagination: pageRes}, nil
}
