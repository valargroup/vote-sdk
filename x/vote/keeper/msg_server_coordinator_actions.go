package keeper

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"

	"github.com/valargroup/vote-sdk/x/vote/types"
)

const coordinatorActionTTLSeconds uint64 = 7 * 24 * 60 * 60

// ProposeCoordinatorAction creates a threshold-gated coordinator action. The
// proposer is recorded as the first approval; threshold=1 executes immediately.
func (ms msgServer) ProposeCoordinatorAction(goCtx context.Context, msg *types.MsgProposeCoordinatorAction) (*types.MsgProposeCoordinatorActionResponse, error) {
	if err := msg.ValidateBasic(); err != nil {
		return nil, err
	}

	proposer, err := ms.k.ValidateVoteManagerApprover(goCtx, msg.Creator)
	if err != nil {
		return nil, err
	}
	if err := ms.validateCoordinatorPayload(goCtx, msg.Payload, proposer); err != nil {
		return nil, err
	}

	ctx := sdk.UnwrapSDKContext(goCtx)
	kvStore := ms.k.OpenKVStore(ctx)
	actionID, err := ms.k.AllocateCoordinatorActionID(kvStore)
	if err != nil {
		return nil, err
	}

	createdAt := uint64(ctx.BlockTime().Unix())
	action := &types.CoordinatorAction{
		ActionId:  actionID,
		Payload:   msg.Payload,
		Proposer:  proposer,
		Approvals: []string{proposer},
		Status:    types.CoordinatorActionStatus_COORDINATOR_ACTION_STATUS_PENDING,
		CreatedAt: createdAt,
		ExpiresAt: createdAt + coordinatorActionTTLSeconds,
	}

	executed, err := ms.maybeExecuteCoordinatorAction(goCtx, action)
	if err != nil {
		return nil, err
	}
	if err := ms.k.SetCoordinatorAction(kvStore, action); err != nil {
		return nil, err
	}

	ctx.EventManager().EmitEvent(sdk.NewEvent(
		types.EventTypeProposeCoordinatorAction,
		sdk.NewAttribute(types.AttributeKeyActionID, strconv.FormatUint(action.ActionId, 10)),
		sdk.NewAttribute(types.AttributeKeyActionType, action.Payload.TypeUrl),
		sdk.NewAttribute(types.AttributeKeyCreator, proposer),
		sdk.NewAttribute(types.AttributeKeyApprovalCount, strconv.Itoa(len(action.Approvals))),
	))
	if executed {
		ms.emitCoordinatorExecutionEvent(goCtx, ctx, action)
	}

	return &types.MsgProposeCoordinatorActionResponse{ActionId: actionID, Executed: executed}, nil
}

// ApproveCoordinatorAction adds one distinct current coordinator approval to a
// pending action and executes it once the current policy threshold is met.
func (ms msgServer) ApproveCoordinatorAction(goCtx context.Context, msg *types.MsgApproveCoordinatorAction) (*types.MsgApproveCoordinatorActionResponse, error) {
	if err := msg.ValidateBasic(); err != nil {
		return nil, err
	}

	approver, err := ms.k.ValidateVoteManagerApprover(goCtx, msg.Creator)
	if err != nil {
		return nil, err
	}

	ctx := sdk.UnwrapSDKContext(goCtx)
	kvStore := ms.k.OpenKVStore(ctx)
	action, err := ms.k.GetCoordinatorAction(kvStore, msg.ActionId)
	if err != nil {
		return nil, err
	}
	if action.Status != types.CoordinatorActionStatus_COORDINATOR_ACTION_STATUS_PENDING {
		return nil, fmt.Errorf("%w: action %d is %s", types.ErrInvalidCoordinatorAction, msg.ActionId, action.Status.String())
	}
	if coordinatorActionExpired(ctx, action) {
		action.Status = types.CoordinatorActionStatus_COORDINATOR_ACTION_STATUS_EXPIRED
		if err := ms.k.SetCoordinatorAction(kvStore, action); err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("%w: action %d expired at %d", types.ErrCoordinatorActionExpired, msg.ActionId, action.ExpiresAt)
	}
	if addressInList(approver, action.Approvals) {
		return nil, fmt.Errorf("%w: %s", types.ErrCoordinatorAlreadyApproved, approver)
	}

	action.Approvals = append(action.Approvals, approver)
	executed, err := ms.maybeExecuteCoordinatorAction(goCtx, action)
	if err != nil {
		return nil, err
	}
	if err := ms.k.SetCoordinatorAction(kvStore, action); err != nil {
		return nil, err
	}

	ctx.EventManager().EmitEvent(sdk.NewEvent(
		types.EventTypeApproveCoordinatorAction,
		sdk.NewAttribute(types.AttributeKeyActionID, strconv.FormatUint(action.ActionId, 10)),
		sdk.NewAttribute(types.AttributeKeyActionType, action.Payload.TypeUrl),
		sdk.NewAttribute(types.AttributeKeyCreator, approver),
		sdk.NewAttribute(types.AttributeKeyApprovalCount, strconv.Itoa(len(action.Approvals))),
	))
	if executed {
		ms.emitCoordinatorExecutionEvent(goCtx, ctx, action)
	}

	return &types.MsgApproveCoordinatorActionResponse{ActionId: msg.ActionId, Executed: executed}, nil
}

func (ms msgServer) maybeExecuteCoordinatorAction(goCtx context.Context, action *types.CoordinatorAction) (bool, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)
	if coordinatorActionExpired(ctx, action) {
		action.Status = types.CoordinatorActionStatus_COORDINATOR_ACTION_STATUS_EXPIRED
		return false, fmt.Errorf("%w: action %d expired at %d", types.ErrCoordinatorActionExpired, action.ActionId, action.ExpiresAt)
	}

	count, threshold, currentApprovals, err := ms.currentCoordinatorApprovalCount(goCtx, action.Approvals)
	if err != nil {
		return false, err
	}
	if count < threshold {
		return false, nil
	}

	if err := ms.executeCoordinatorPayload(goCtx, action.Payload, currentApprovals); err != nil {
		return false, err
	}
	action.Status = types.CoordinatorActionStatus_COORDINATOR_ACTION_STATUS_EXECUTED
	action.ExecutedAt = uint64(ctx.BlockTime().Unix())
	return true, nil
}

func (ms msgServer) currentCoordinatorApprovalCount(goCtx context.Context, approvals []string) (uint32, uint32, []string, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)
	kvStore := ms.k.OpenKVStore(ctx)
	set, err := ms.k.GetVoteManagers(kvStore)
	if err != nil {
		return 0, 0, nil, err
	}
	if set == nil || len(set.Addresses) == 0 {
		return 0, 0, nil, fmt.Errorf("%w", types.ErrNoVoteManagers)
	}

	managerSet := make(map[string]struct{}, len(set.Addresses))
	for _, manager := range set.Addresses {
		managerSet[manager] = struct{}{}
	}

	seen := make(map[string]struct{}, len(approvals))
	currentApprovals := make([]string, 0, len(approvals))
	for _, approval := range approvals {
		canonical, err := normalizeBech32Addr(approval)
		if err != nil {
			continue
		}
		if _, dup := seen[canonical]; dup {
			continue
		}
		seen[canonical] = struct{}{}
		if _, ok := managerSet[canonical]; ok {
			currentApprovals = append(currentApprovals, canonical)
		}
	}
	return uint32(len(currentApprovals)), set.Threshold, currentApprovals, nil
}

func (ms msgServer) validateCoordinatorPayload(goCtx context.Context, payload *anypb.Any, proposer string) error {
	switch coordinatorPayloadType(payload) {
	case "svote.v1.MsgCreateVotingSession":
		msg := &types.MsgCreateVotingSession{}
		if err := unmarshalAnyPayload(payload, msg); err != nil {
			return err
		}
		if err := msg.ValidateBasic(); err != nil {
			return err
		}
		return validatePayloadCreator(msg.Creator, proposer)
	case "svote.v1.MsgUpdateVoteManagers":
		msg := &types.MsgUpdateVoteManagers{}
		if err := unmarshalAnyPayload(payload, msg); err != nil {
			return err
		}
		if err := msg.ValidateBasic(); err != nil {
			return err
		}
		return validatePayloadCreator(msg.Creator, proposer)
	case "svote.v1.MsgScheduleUpgrade":
		msg := &types.MsgScheduleUpgrade{}
		if err := unmarshalAnyPayload(payload, msg); err != nil {
			return err
		}
		if err := msg.ValidateBasic(); err != nil {
			return err
		}
		return validatePayloadCreator(msg.Creator, proposer)
	case "svote.v1.MsgCancelUpgrade":
		msg := &types.MsgCancelUpgrade{}
		if err := unmarshalAnyPayload(payload, msg); err != nil {
			return err
		}
		if err := msg.ValidateBasic(); err != nil {
			return err
		}
		return validatePayloadCreator(msg.Creator, proposer)
	case "svote.v1.MsgSetEndorser":
		msg := &types.MsgSetEndorser{}
		if err := unmarshalAnyPayload(payload, msg); err != nil {
			return err
		}
		if err := msg.ValidateBasic(); err != nil {
			return err
		}
		return validatePayloadCreator(msg.Creator, proposer)
	case "svote.v1.MsgAuthorizedSend":
		msg := &types.MsgAuthorizedSend{}
		if err := unmarshalAnyPayload(payload, msg); err != nil {
			return err
		}
		fromAddr, _, _, err := validateAuthorizedSendFields(msg)
		if err != nil {
			return err
		}
		senderIsVoteManager, err := ms.k.IsVoteManager(goCtx, fromAddr.String())
		if err != nil {
			return err
		}
		if !senderIsVoteManager {
			return fmt.Errorf("%w: coordinator-funded sends must originate from a vote manager", types.ErrUnauthorizedSend)
		}
		return nil
	default:
		return fmt.Errorf("%w: %s", types.ErrUnsupportedCoordinatorAction, payload.GetTypeUrl())
	}
}

func (ms msgServer) executeCoordinatorPayload(goCtx context.Context, payload *anypb.Any, approvals []string) error {
	switch coordinatorPayloadType(payload) {
	case "svote.v1.MsgCreateVotingSession":
		msg := &types.MsgCreateVotingSession{}
		if err := unmarshalAnyPayload(payload, msg); err != nil {
			return err
		}
		_, err := ms.executeCreateVotingSession(goCtx, msg)
		return err
	case "svote.v1.MsgUpdateVoteManagers":
		msg := &types.MsgUpdateVoteManagers{}
		if err := unmarshalAnyPayload(payload, msg); err != nil {
			return err
		}
		_, err := ms.executeUpdateVoteManagers(goCtx, msg)
		return err
	case "svote.v1.MsgScheduleUpgrade":
		msg := &types.MsgScheduleUpgrade{}
		if err := unmarshalAnyPayload(payload, msg); err != nil {
			return err
		}
		_, err := ms.executeScheduleUpgrade(goCtx, msg)
		return err
	case "svote.v1.MsgCancelUpgrade":
		msg := &types.MsgCancelUpgrade{}
		if err := unmarshalAnyPayload(payload, msg); err != nil {
			return err
		}
		_, err := ms.executeCancelUpgrade(goCtx, msg)
		return err
	case "svote.v1.MsgSetEndorser":
		msg := &types.MsgSetEndorser{}
		if err := unmarshalAnyPayload(payload, msg); err != nil {
			return err
		}
		_, err := ms.executeSetEndorser(goCtx, msg)
		return err
	case "svote.v1.MsgAuthorizedSend":
		msg := &types.MsgAuthorizedSend{}
		if err := unmarshalAnyPayload(payload, msg); err != nil {
			return err
		}
		_, err := ms.executeAuthorizedSend(goCtx, msg, true, approvals)
		return err
	default:
		return fmt.Errorf("%w: %s", types.ErrUnsupportedCoordinatorAction, payload.GetTypeUrl())
	}
}

func validatePayloadCreator(rawCreator string, proposer string) error {
	creator, err := normalizeBech32Addr(rawCreator)
	if err != nil {
		return fmt.Errorf("%w: payload creator %q is not a valid bech32 address: %v", types.ErrInvalidCoordinatorAction, rawCreator, err)
	}
	if creator != proposer {
		return fmt.Errorf("%w: payload creator %s must match proposer %s", types.ErrInvalidCoordinatorAction, creator, proposer)
	}
	return nil
}

func unmarshalAnyPayload(any *anypb.Any, msg proto.Message) error {
	if any == nil {
		return fmt.Errorf("%w: payload cannot be nil", types.ErrInvalidCoordinatorAction)
	}
	if err := proto.Unmarshal(any.Value, msg); err != nil {
		return fmt.Errorf("%w: failed to decode %s: %v", types.ErrInvalidCoordinatorAction, any.TypeUrl, err)
	}
	return nil
}

func coordinatorPayloadType(any *anypb.Any) string {
	if any == nil {
		return ""
	}
	typeURL := strings.TrimPrefix(any.TypeUrl, "/")
	if idx := strings.LastIndex(typeURL, "/"); idx >= 0 {
		return typeURL[idx+1:]
	}
	return typeURL
}

func coordinatorActionExpired(ctx sdk.Context, action *types.CoordinatorAction) bool {
	return action.ExpiresAt != 0 && uint64(ctx.BlockTime().Unix()) > action.ExpiresAt
}

func (ms msgServer) emitCoordinatorExecutionEvent(goCtx context.Context, ctx sdk.Context, action *types.CoordinatorAction) {
	approvalCount := strconv.Itoa(len(action.Approvals))
	threshold := approvalCount
	if count, currentThreshold, _, err := ms.currentCoordinatorApprovalCount(goCtx, action.Approvals); err == nil {
		approvalCount = strconv.FormatUint(uint64(count), 10)
		threshold = strconv.FormatUint(uint64(currentThreshold), 10)
	}
	ctx.EventManager().EmitEvent(sdk.NewEvent(
		types.EventTypeExecuteCoordinatorAction,
		sdk.NewAttribute(types.AttributeKeyActionID, strconv.FormatUint(action.ActionId, 10)),
		sdk.NewAttribute(types.AttributeKeyActionType, action.Payload.TypeUrl),
		sdk.NewAttribute(types.AttributeKeyApprovalCount, approvalCount),
		sdk.NewAttribute(types.AttributeKeyThreshold, threshold),
	))
}
