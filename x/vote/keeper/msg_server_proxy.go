package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/valargroup/vote-sdk/x/vote/types"
)

// ProxyDelegationBatch handles MsgProxyDelegationBatch, tag 0x09
// (docs/proxy-delegation/contracts-v1.md §5.2). A closed gate is
// ErrProxyDelegationDisabled; otherwise it applies the batch with
// executeProxyDelegationBatch.
func (ms msgServer) ProxyDelegationBatch(goCtx context.Context, msg *types.MsgProxyDelegationBatch) (*types.MsgProxyDelegationBatchResponse, error) {
	if !types.ProxyDelegationEnabled {
		return nil, types.ErrProxyDelegationDisabled
	}
	return ms.k.executeProxyDelegationBatch(sdk.UnwrapSDKContext(goCtx), msg)
}

// DelegateOp handles MsgDelegateOp, tag 0x0B (contracts-v1 §5.3). A closed
// gate is ErrProxyDelegationDisabled. Otherwise it re-runs ValidateBasic and
// every check of the op, signatures included, then applies the register,
// retire or ballot and returns the index of the entry it touched.
func (ms msgServer) DelegateOp(goCtx context.Context, msg *types.MsgDelegateOp) (*types.MsgDelegateOpResponse, error) {
	if !types.ProxyDelegationEnabled {
		return nil, types.ErrProxyDelegationDisabled
	}
	if err := msg.ValidateBasic(); err != nil {
		return nil, err
	}
	ctx := sdk.UnwrapSDKContext(goCtx)
	var (
		delegateIndex uint32
		err           error
	)
	switch {
	case msg.Register != nil:
		delegateIndex, err = ms.k.executeDelegateRegister(ctx, msg.Register)
	case msg.Retire != nil:
		delegateIndex, err = ms.k.executeDelegateRetire(ctx, msg.Retire)
	default: // ValidateBasic leaves only the ballot.
		delegateIndex, err = ms.k.executeDelegateBallot(ctx, msg.Ballot)
	}
	if err != nil {
		return nil, err
	}
	return &types.MsgDelegateOpResponse{DelegateIndex: delegateIndex}, nil
}
