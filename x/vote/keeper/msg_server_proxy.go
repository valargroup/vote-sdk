package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/valargroup/vote-sdk/x/vote/types"
)

// ProxyDelegationBatch handles MsgProxyDelegationBatch, tag 0x09
// (docs/proxy-delegation/contracts-v1.md §5.2). Batches are not accepted
// yet, so it is ErrProxyDelegationDisabled whatever the gate.
func (ms msgServer) ProxyDelegationBatch(context.Context, *types.MsgProxyDelegationBatch) (*types.MsgProxyDelegationBatchResponse, error) {
	return nil, types.ErrProxyDelegationDisabled
}

// DelegateOp handles MsgDelegateOp, tag 0x0B (contracts-v1 §5.3). A closed
// gate is ErrProxyDelegationDisabled. Otherwise it re-runs ValidateBasic and
// every check of the op, signatures included, then applies the register or
// retire and returns the index of the entry it touched. A ballot is not
// accepted yet.
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
		err = errDelegateBallotsNotAccepted()
	}
	if err != nil {
		return nil, err
	}
	return &types.MsgDelegateOpResponse{DelegateIndex: delegateIndex}, nil
}
