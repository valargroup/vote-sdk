package keeper

import (
	"context"
	"encoding/hex"
	"fmt"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/valargroup/vote-sdk/x/vote/types"
)

// SetEndorser creates, rotates, or clears an endorser mapping. Direct external
// submission is blocked by the app-level whitelist; operators use coordinator
// actions, which call executeSetEndorser after threshold approval.
func (ms msgServer) SetEndorser(goCtx context.Context, msg *types.MsgSetEndorser) (*types.MsgSetEndorserResponse, error) {
	if err := msg.ValidateBasic(); err != nil {
		return nil, err
	}
	if err := ms.k.ValidateVoteManagerOnly(goCtx, msg.Creator); err != nil {
		return nil, err
	}

	return ms.executeSetEndorser(goCtx, msg)
}

func (ms msgServer) executeSetEndorser(goCtx context.Context, msg *types.MsgSetEndorser) (*types.MsgSetEndorserResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	if err := msg.ValidateBasic(); err != nil {
		return nil, err
	}
	if err := types.ValidateEndorserID(msg.EndorserId); err != nil {
		return nil, err
	}

	kvStore := ms.k.OpenKVStore(ctx)
	normalized := ""
	if msg.Address == "" {
		if err := ms.k.DeleteEndorser(kvStore, msg.EndorserId); err != nil {
			return nil, err
		}
	} else {
		addr, err := normalizeBech32Addr(msg.Address)
		if err != nil {
			return nil, fmt.Errorf("%w: address %q is not a valid bech32 address: %v", types.ErrInvalidField, msg.Address, err)
		}
		normalized = addr
		accAddr, err := sdk.AccAddressFromBech32(normalized)
		if err != nil {
			return nil, fmt.Errorf("%w: address %q is not a valid bech32 address: %v", types.ErrInvalidField, normalized, err)
		}
		if ms.k.accountKeeper == nil {
			return nil, fmt.Errorf("%w: account keeper is not configured", types.ErrInvalidField)
		}
		if ms.k.accountKeeper.GetAccount(goCtx, accAddr) == nil {
			ms.k.accountKeeper.SetAccount(goCtx, ms.k.accountKeeper.NewAccountWithAddress(goCtx, accAddr))
		}
		if err := ms.k.SetEndorser(kvStore, msg.EndorserId, normalized); err != nil {
			return nil, err
		}
	}

	ctx.EventManager().EmitEvent(sdk.NewEvent(
		types.EventTypeSetEndorser,
		sdk.NewAttribute(types.AttributeKeyCreator, msg.Creator),
		sdk.NewAttribute(types.AttributeKeyEndorserID, msg.EndorserId),
		sdk.NewAttribute(types.AttributeKeyEndorserAddress, normalized),
	))

	return &types.MsgSetEndorserResponse{}, nil
}

// EndorseRound records one endorser_id/round endorsement. Endorser address only.
func (ms msgServer) EndorseRound(goCtx context.Context, msg *types.MsgEndorseRound) (*types.MsgEndorseRoundResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)
	if err := types.ValidateEndorserID(msg.EndorserId); err != nil {
		return nil, err
	}

	kvStore := ms.k.OpenKVStore(ctx)
	authorizedAddress, found, err := ms.k.GetEndorser(kvStore, msg.EndorserId)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("%w: %s", types.ErrEndorserNotFound, msg.EndorserId)
	}

	creator, err := normalizeBech32Addr(msg.Creator)
	if err != nil {
		return nil, fmt.Errorf("%w: creator %q is not a valid bech32 address: %v", types.ErrNotAuthorized, msg.Creator, err)
	}
	if creator != authorizedAddress {
		return nil, fmt.Errorf("%w: sender %s is not mapped to endorser %s", types.ErrNotAuthorized, creator, msg.EndorserId)
	}

	if _, err := ms.k.GetVoteRound(kvStore, msg.VoteRoundId); err != nil {
		return nil, err
	}
	if err := ms.k.AddEndorsedRound(kvStore, msg.EndorserId, msg.VoteRoundId); err != nil {
		return nil, err
	}

	ctx.EventManager().EmitEvent(sdk.NewEvent(
		types.EventTypeEndorseRound,
		sdk.NewAttribute(types.AttributeKeyCreator, msg.Creator),
		sdk.NewAttribute(types.AttributeKeyEndorserID, msg.EndorserId),
		sdk.NewAttribute(types.AttributeKeyRoundID, hex.EncodeToString(msg.VoteRoundId)),
	))

	return &types.MsgEndorseRoundResponse{}, nil
}

// ClearRoundEndorsement clears one endorser_id/round endorsement. Endorser address only.
func (ms msgServer) ClearRoundEndorsement(goCtx context.Context, msg *types.MsgClearRoundEndorsement) (*types.MsgClearRoundEndorsementResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)
	if err := types.ValidateEndorserID(msg.EndorserId); err != nil {
		return nil, err
	}

	kvStore := ms.k.OpenKVStore(ctx)
	authorizedAddress, found, err := ms.k.GetEndorser(kvStore, msg.EndorserId)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("%w: %s", types.ErrEndorserNotFound, msg.EndorserId)
	}

	creator, err := normalizeBech32Addr(msg.Creator)
	if err != nil {
		return nil, fmt.Errorf("%w: creator %q is not a valid bech32 address: %v", types.ErrNotAuthorized, msg.Creator, err)
	}
	if creator != authorizedAddress {
		return nil, fmt.Errorf("%w: sender %s is not mapped to endorser %s", types.ErrNotAuthorized, creator, msg.EndorserId)
	}

	if _, err := ms.k.GetVoteRound(kvStore, msg.VoteRoundId); err != nil {
		return nil, err
	}
	if err := ms.k.DeleteEndorsedRound(kvStore, msg.EndorserId, msg.VoteRoundId); err != nil {
		return nil, err
	}

	ctx.EventManager().EmitEvent(sdk.NewEvent(
		types.EventTypeClearRoundEndorsement,
		sdk.NewAttribute(types.AttributeKeyCreator, msg.Creator),
		sdk.NewAttribute(types.AttributeKeyEndorserID, msg.EndorserId),
		sdk.NewAttribute(types.AttributeKeyRoundID, hex.EncodeToString(msg.VoteRoundId)),
	))

	return &types.MsgClearRoundEndorsementResponse{}, nil
}
