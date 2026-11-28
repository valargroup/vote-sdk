package keeper

import (
	"context"
	"errors"

	"cosmossdk.io/core/store"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/valargroup/vote-sdk/x/vote/types"
)

const (
	// proxyQueryDefaultLimit is a registry page's size when limit is 0
	// (docs/proxy-delegation/contracts-v1.md §8).
	proxyQueryDefaultLimit = 100
	// proxyQueryMaxLimit is the largest page; a larger limit is
	// InvalidArgument.
	proxyQueryMaxLimit = 1000
)

// ProxyDelegates pages through the proxy delegate registry by index
// (contracts-v1 §8). It returns the entries with an index above start_after
// in ascending order, at most limit of them, and next_start_after as the
// last index returned while more remain, else 0.
func (qs queryServer) ProxyDelegates(goCtx context.Context, req *types.QueryProxyDelegatesRequest) (*types.QueryProxyDelegatesResponse, error) {
	if !types.ProxyDelegationEnabled {
		return nil, errProxyDelegationQueryDormant()
	}
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "empty request")
	}
	limit, err := proxyQueryPageLimit(req.Limit)
	if err != nil {
		return nil, err
	}
	kvStore := qs.k.OpenKVStore(sdk.UnwrapSDKContext(goCtx))
	resp := &types.QueryProxyDelegatesResponse{}
	if err := qs.k.IterateProxyDelegateEntries(kvStore, req.StartAfter, func(entry *types.ProxyDelegateEntry) bool {
		if len(resp.Delegates) == limit {
			resp.NextStartAfter = resp.Delegates[limit-1].Index
			return true
		}
		resp.Delegates = append(resp.Delegates, entry)
		return false
	}); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to iterate proxy delegates: %v", err)
	}
	return resp, nil
}

// ProxyDelegate returns one proxy delegate entry by index (contracts-v1 §8).
// An unknown index, 0 included, is NotFound.
func (qs queryServer) ProxyDelegate(goCtx context.Context, req *types.QueryProxyDelegateRequest) (*types.QueryProxyDelegateResponse, error) {
	if !types.ProxyDelegationEnabled {
		return nil, errProxyDelegationQueryDormant()
	}
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "empty request")
	}
	entry, err := proxyQueryEntry(qs.k, qs.k.OpenKVStore(sdk.UnwrapSDKContext(goCtx)), req.DelegateIndex)
	if err != nil {
		return nil, err
	}
	return &types.QueryProxyDelegateResponse{Delegate: entry}, nil
}

// ProxyDelegateByKey returns the entry a delegate key was registered to
// (contracts-v1 §8). A key never registered is NotFound.
func (qs queryServer) ProxyDelegateByKey(goCtx context.Context, req *types.QueryProxyDelegateByKeyRequest) (*types.QueryProxyDelegateByKeyResponse, error) {
	if !types.ProxyDelegationEnabled {
		return nil, errProxyDelegationQueryDormant()
	}
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "empty request")
	}
	if len(req.Dk) != 32 {
		return nil, status.Errorf(codes.InvalidArgument, "dk must be exactly 32 bytes, got %d", len(req.Dk))
	}
	kvStore := qs.k.OpenKVStore(sdk.UnwrapSDKContext(goCtx))
	delegateIndex, found, err := qs.k.GetProxyUsedDelegateKey(kvStore, req.Dk)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to look up delegate key: %v", err)
	}
	if !found {
		return nil, status.Errorf(codes.NotFound, "delegate key %x was never registered", req.Dk)
	}
	entry, err := proxyQueryEntry(qs.k, kvStore, delegateIndex)
	if err != nil {
		return nil, err
	}
	return &types.QueryProxyDelegateByKeyResponse{Delegate: entry}, nil
}

// ProxyDelegateByHandle returns the live entry registered to a canonical
// handle at a provider (contracts-v1 §8). A handle that is not canonical is
// InvalidArgument, and one without a live entry is NotFound.
func (qs queryServer) ProxyDelegateByHandle(goCtx context.Context, req *types.QueryProxyDelegateByHandleRequest) (*types.QueryProxyDelegateByHandleResponse, error) {
	if !types.ProxyDelegationEnabled {
		return nil, errProxyDelegationQueryDormant()
	}
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "empty request")
	}
	if err := types.ValidateDelegateHandle(req.Provider, req.Handle); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	kvStore := qs.k.OpenKVStore(sdk.UnwrapSDKContext(goCtx))
	delegateIndex, found, err := qs.k.GetProxyDelegateByHandle(kvStore, req.Provider, req.Handle)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to look up handle: %v", err)
	}
	if !found {
		return nil, status.Errorf(codes.NotFound, "provider %d handle %q has no live delegate entry", req.Provider, req.Handle)
	}
	entry, err := proxyQueryEntry(qs.k, kvStore, delegateIndex)
	if err != nil {
		return nil, err
	}
	return &types.QueryProxyDelegateByHandleResponse{Delegate: entry}, nil
}

// DelegateBallots pages through a round's delegate ballots (contracts-v1
// §8). Ballots are not stored yet, so it is gRPC Unimplemented whatever the
// gate.
func (qs queryServer) DelegateBallots(context.Context, *types.QueryDelegateBallotsRequest) (*types.QueryDelegateBallotsResponse, error) {
	return nil, status.Error(codes.Unimplemented, "delegate ballots are not served yet")
}

// ProxyDelegationParams returns the registry params and the next delegate
// index (contracts-v1 §8). Unset values read as their defaults (§9).
func (qs queryServer) ProxyDelegationParams(goCtx context.Context, req *types.QueryProxyDelegationParamsRequest) (*types.QueryProxyDelegationParamsResponse, error) {
	if !types.ProxyDelegationEnabled {
		return nil, errProxyDelegationQueryDormant()
	}
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "empty request")
	}
	kvStore := qs.k.OpenKVStore(sdk.UnwrapSDKContext(goCtx))
	params, err := qs.k.GetProxyDelegationParams(kvStore)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to get proxy delegation params: %v", err)
	}
	next, err := qs.k.GetProxyNextDelegateIndex(kvStore)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to get next delegate index: %v", err)
	}
	return &types.QueryProxyDelegationParamsResponse{Params: params, NextDelegateIndex: next}, nil
}

// proxyQueryPageLimit returns a registry page's size: 0 means the default,
// and a limit above the maximum is InvalidArgument (contracts-v1 §8).
func proxyQueryPageLimit(limit uint32) (int, error) {
	switch {
	case limit == 0:
		return proxyQueryDefaultLimit, nil
	case limit > proxyQueryMaxLimit:
		return 0, status.Errorf(codes.InvalidArgument, "limit must be at most %d, got %d", proxyQueryMaxLimit, limit)
	default:
		return int(limit), nil
	}
}

// proxyQueryEntry returns entry delegateIndex as a query answer: NotFound when
// the registry has none, Internal on a store failure.
func proxyQueryEntry(k *Keeper, kvStore store.KVStore, delegateIndex uint32) (*types.ProxyDelegateEntry, error) {
	entry, err := k.GetProxyDelegateEntry(kvStore, delegateIndex)
	if errors.Is(err, types.ErrDelegateNotFound) {
		return nil, status.Errorf(codes.NotFound, "delegate %d not found", delegateIndex)
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to get delegate %d: %v", delegateIndex, err)
	}
	return entry, nil
}

// errProxyDelegationQueryDormant is each proxy delegation query's answer
// while the gate is closed: gRPC Unimplemented (contracts-v1 §9).
func errProxyDelegationQueryDormant() error {
	return status.Error(codes.Unimplemented, "proxy delegation is not enabled")
}
