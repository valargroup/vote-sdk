package keeper

import (
	"context"

	"cosmossdk.io/errors"

	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	govtypes "github.com/cosmos/cosmos-sdk/x/gov/types"
	slashingtypes "github.com/cosmos/cosmos-sdk/x/slashing/types"
)

var _ slashingtypes.MsgServer = msgServer{}

type msgServer struct {
	Keeper
}

func NewMsgServerImpl(keeper Keeper) slashingtypes.MsgServer {
	return &msgServer{Keeper: keeper}
}

func (k msgServer) UpdateParams(goCtx context.Context, msg *slashingtypes.MsgUpdateParams) (*slashingtypes.MsgUpdateParamsResponse, error) {
	if k.authority != msg.Authority {
		return nil, errors.Wrapf(govtypes.ErrInvalidSigner, "invalid authority; expected %s, got %s", k.authority, msg.Authority)
	}
	if err := msg.Params.Validate(); err != nil {
		return nil, err
	}
	if err := k.SetParams(goCtx, msg.Params); err != nil {
		return nil, err
	}
	return &slashingtypes.MsgUpdateParamsResponse{}, nil
}

func (k msgServer) Unjail(goCtx context.Context, msg *slashingtypes.MsgUnjail) (*slashingtypes.MsgUnjailResponse, error) {
	valAddr, err := k.sk.ValidatorAddressCodec().StringToBytes(msg.ValidatorAddr)
	if err != nil {
		return nil, sdkerrors.ErrInvalidAddress.Wrapf("validator input address: %s", err)
	}
	ctx := sdk.UnwrapSDKContext(goCtx)
	if err := k.Keeper.Unjail(ctx, valAddr); err != nil {
		return nil, err
	}
	return &slashingtypes.MsgUnjailResponse{}, nil
}
