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

// ProposeCoordinatorAction validates and stores a coordinator action. The
// proposer is recorded as the first approval, so threshold 1 actions execute in
// this transaction. The response Executed field is true only when execution
// happened before this method returned.
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

// ApproveCoordinatorAction adds one coordinator approval and executes the action
// once the current policy threshold is met. If the approver already approved,
// this rechecks execution so actions can become live after a policy change; it
// still rejects the duplicate when the action is not ready to execute.
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
		executed, err := ms.maybeExecuteCoordinatorAction(goCtx, action)
		if err != nil {
			return nil, err
		}
		if executed {
			if err := ms.k.SetCoordinatorAction(kvStore, action); err != nil {
				return nil, err
			}
			ms.emitCoordinatorExecutionEvent(goCtx, ctx, action)
			return &types.MsgApproveCoordinatorActionResponse{ActionId: msg.ActionId, Executed: true}, nil
		}
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

// maybeExecuteCoordinatorAction executes action.Payload when the action has
// enough current coordinator approvals. It mutates action in place. The return
// value is true only when this call executed the payload and marked the action
// executed.
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

// currentCoordinatorApprovalCount recounts stored approvals against the current
// coordinator policy. It returns the count, current threshold, and the normalized
// approvals that still belong to current coordinators. Removed coordinators,
// duplicate approvals, and malformed addresses do not count.
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

// validateCoordinatorPayload checks that the proposed payload is one of the
// coordinator controlled actions and that it is valid before an action ID is
// allocated. For creator based messages, the embedded creator must match the
// proposer. For sends, the source account must be a current coordinator.
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

// executeCoordinatorPayload runs the already approved payload. Callers must
// perform proposer, expiry, and threshold checks before reaching this helper.
// The approvals argument is the current coordinator approvals that counted
// toward execution; sends use it to require source account approval.
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
		_, err := ms.executeAuthorizedSend(goCtx, msg, approvals)
		return err
	default:
		return fmt.Errorf("%w: %s", types.ErrUnsupportedCoordinatorAction, payload.GetTypeUrl())
	}
}

// validatePayloadCreator binds a coordinator proposal to the signer that
// created the embedded action payload.
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

// unmarshalAnyPayload decodes the Any value into the concrete message chosen by
// the caller. The TypeUrl is used for dispatch before this helper is called.
func unmarshalAnyPayload(any *anypb.Any, msg proto.Message) error {
	if any == nil {
		return fmt.Errorf("%w: payload cannot be nil", types.ErrInvalidCoordinatorAction)
	}
	if err := proto.Unmarshal(any.Value, msg); err != nil {
		return fmt.Errorf("%w: failed to decode %s: %v", types.ErrInvalidCoordinatorAction, any.TypeUrl, err)
	}
	return nil
}

// coordinatorPayloadType normalizes Any TypeUrl values for switch dispatch. It
// accepts both "/svote.v1.MsgX" and fully qualified URL style type URLs.
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

// coordinatorActionExpired treats zero ExpiresAt as unset and otherwise expires
// only after the stored Unix time has passed.
func coordinatorActionExpired(ctx sdk.Context, action *types.CoordinatorAction) bool {
	return action.ExpiresAt != 0 && uint64(ctx.BlockTime().Unix()) > action.ExpiresAt
}

// emitCoordinatorExecutionEvent emits the best current view of approval count
// and threshold. If the recount fails, it falls back to the stored approval
// count so execution itself is not hidden by event formatting.
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
