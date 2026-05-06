package keeper_test

import (
	"context"
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"

	svtest "github.com/valargroup/vote-sdk/testutil"
	"github.com/valargroup/vote-sdk/x/vote/types"
)

func coordinatorPayload(t *testing.T, typeURL string, msg proto.Message) *anypb.Any {
	t.Helper()
	bz, err := proto.Marshal(msg)
	require.NoError(t, err)
	return &anypb.Any{TypeUrl: typeURL, Value: bz}
}

func coordinatorPayloadForMessage(t *testing.T, msg proto.Message) *anypb.Any {
	t.Helper()
	typeURL := "/" + string(msg.ProtoReflect().Descriptor().FullName())
	return coordinatorPayload(t, typeURL, msg)
}

func (s *MsgServerTestSuite) proposeCoordinatorAction(ctx context.Context, creator string, msg proto.Message) (*types.MsgProposeCoordinatorActionResponse, error) {
	return s.msgServer.ProposeCoordinatorAction(ctx, &types.MsgProposeCoordinatorAction{
		Creator: creator,
		Payload: coordinatorPayloadForMessage(s.T(), msg),
	})
}

func (s *MsgServerTestSuite) createVotingSessionViaCoordinator(ctx sdk.Context, msg *types.MsgCreateVotingSession) (*types.MsgCreateVotingSessionResponse, error) {
	if _, err := s.proposeCoordinatorAction(ctx, msg.Creator, msg); err != nil {
		return nil, err
	}
	return &types.MsgCreateVotingSessionResponse{
		VoteRoundId: computeExpectedRoundID(msg, uint64(ctx.BlockHeight())),
	}, nil
}

func (s *MsgServerTestSuite) TestCoordinatorAction_UpdateManagersThresholdFlow() {
	manager1 := svtest.TestAccAddr(0x31)
	manager2 := svtest.TestAccAddr(0x32)
	manager3 := svtest.TestAccAddr(0x33)
	kv := s.keeper.OpenKVStore(s.ctx)
	s.Require().NoError(s.keeper.SetVoteManagers(kv, &types.VoteManagerSet{
		Addresses: []string{manager1, manager2},
		Threshold: 2,
	}))

	payload := coordinatorPayload(s.T(), "/svote.v1.MsgUpdateVoteManagers", &types.MsgUpdateVoteManagers{
		Creator:         manager1,
		NewVoteManagers: []string{manager1},
		NewThreshold:    1,
	})
	proposeResp, err := s.msgServer.ProposeCoordinatorAction(s.ctx, &types.MsgProposeCoordinatorAction{
		Creator: manager1,
		Payload: payload,
	})
	s.Require().NoError(err)
	s.Require().False(proposeResp.Executed)

	action, err := s.keeper.GetCoordinatorAction(kv, proposeResp.ActionId)
	s.Require().NoError(err)
	s.Require().Equal(types.CoordinatorActionStatus_COORDINATOR_ACTION_STATUS_PENDING, action.Status)
	s.Require().Equal([]string{manager1}, action.Approvals)

	_, err = s.msgServer.ApproveCoordinatorAction(s.ctx, &types.MsgApproveCoordinatorAction{
		Creator:  manager1,
		ActionId: proposeResp.ActionId,
	})
	s.Require().ErrorIs(err, types.ErrCoordinatorAlreadyApproved)

	_, err = s.msgServer.ApproveCoordinatorAction(s.ctx, &types.MsgApproveCoordinatorAction{
		Creator:  manager3,
		ActionId: proposeResp.ActionId,
	})
	s.Require().ErrorIs(err, types.ErrNotAuthorized)

	approveResp, err := s.msgServer.ApproveCoordinatorAction(s.ctx, &types.MsgApproveCoordinatorAction{
		Creator:  manager2,
		ActionId: proposeResp.ActionId,
	})
	s.Require().NoError(err)
	s.Require().True(approveResp.Executed)

	action, err = s.keeper.GetCoordinatorAction(kv, proposeResp.ActionId)
	s.Require().NoError(err)
	s.Require().Equal(types.CoordinatorActionStatus_COORDINATOR_ACTION_STATUS_EXECUTED, action.Status)
	s.Require().Equal([]string{manager1, manager2}, action.Approvals)

	policy, err := s.keeper.GetVoteManagers(kv)
	s.Require().NoError(err)
	s.Require().Equal([]string{manager1}, policy.Addresses)
	s.Require().Equal(uint32(1), policy.Threshold)
}

func (s *MsgServerTestSuite) TestCoordinatorAction_DuplicateApprovalRechecksCurrentPolicy() {
	manager1 := svtest.TestAccAddr(0x34)
	manager2 := svtest.TestAccAddr(0x35)
	kv := s.keeper.OpenKVStore(s.ctx)
	s.Require().NoError(s.keeper.SetVoteManagers(kv, &types.VoteManagerSet{
		Addresses: []string{manager1, manager2},
		Threshold: 2,
	}))

	restoreResp, err := s.msgServer.ProposeCoordinatorAction(s.ctx, &types.MsgProposeCoordinatorAction{
		Creator: manager1,
		Payload: coordinatorPayload(s.T(), "/svote.v1.MsgUpdateVoteManagers", &types.MsgUpdateVoteManagers{
			Creator:         manager1,
			NewVoteManagers: []string{manager1, manager2},
			NewThreshold:    2,
		}),
	})
	s.Require().NoError(err)
	s.Require().False(restoreResp.Executed)

	shrinkResp, err := s.msgServer.ProposeCoordinatorAction(s.ctx, &types.MsgProposeCoordinatorAction{
		Creator: manager1,
		Payload: coordinatorPayload(s.T(), "/svote.v1.MsgUpdateVoteManagers", &types.MsgUpdateVoteManagers{
			Creator:         manager1,
			NewVoteManagers: []string{manager1},
			NewThreshold:    1,
		}),
	})
	s.Require().NoError(err)
	s.Require().False(shrinkResp.Executed)

	approveResp, err := s.msgServer.ApproveCoordinatorAction(s.ctx, &types.MsgApproveCoordinatorAction{
		Creator:  manager2,
		ActionId: shrinkResp.ActionId,
	})
	s.Require().NoError(err)
	s.Require().True(approveResp.Executed)

	policy, err := s.keeper.GetVoteManagers(kv)
	s.Require().NoError(err)
	s.Require().Equal([]string{manager1}, policy.Addresses)
	s.Require().Equal(uint32(1), policy.Threshold)

	approveResp, err = s.msgServer.ApproveCoordinatorAction(s.ctx, &types.MsgApproveCoordinatorAction{
		Creator:  manager1,
		ActionId: restoreResp.ActionId,
	})
	s.Require().NoError(err)
	s.Require().True(approveResp.Executed)

	action, err := s.keeper.GetCoordinatorAction(kv, restoreResp.ActionId)
	s.Require().NoError(err)
	s.Require().Equal(types.CoordinatorActionStatus_COORDINATOR_ACTION_STATUS_EXECUTED, action.Status)

	policy, err = s.keeper.GetVoteManagers(kv)
	s.Require().NoError(err)
	s.Require().Equal([]string{manager1, manager2}, policy.Addresses)
	s.Require().Equal(uint32(2), policy.Threshold)
}

func (s *MsgServerTestSuite) TestCoordinatorAction_ThresholdOneExecutesImmediately() {
	manager1 := svtest.TestAccAddr(0x41)
	manager2 := svtest.TestAccAddr(0x42)
	kv := s.keeper.OpenKVStore(s.ctx)
	s.Require().NoError(s.keeper.SetVoteManagers(kv, &types.VoteManagerSet{
		Addresses: []string{manager1, manager2},
		Threshold: 1,
	}))

	payload := coordinatorPayload(s.T(), "/svote.v1.MsgUpdateVoteManagers", &types.MsgUpdateVoteManagers{
		Creator:         manager1,
		NewVoteManagers: []string{manager1, manager2},
		NewThreshold:    2,
	})
	resp, err := s.msgServer.ProposeCoordinatorAction(s.ctx, &types.MsgProposeCoordinatorAction{
		Creator: manager1,
		Payload: payload,
	})
	s.Require().NoError(err)
	s.Require().True(resp.Executed)

	policy, err := s.keeper.GetVoteManagers(kv)
	s.Require().NoError(err)
	s.Require().Equal(uint32(2), policy.Threshold)
}

func (s *MsgServerTestSuite) TestCoordinatorAction_ApprovalRejectsExpiredAction() {
	manager1 := svtest.TestAccAddr(0x51)
	manager2 := svtest.TestAccAddr(0x52)
	kv := s.keeper.OpenKVStore(s.ctx)
	s.Require().NoError(s.keeper.SetVoteManagers(kv, &types.VoteManagerSet{
		Addresses: []string{manager1, manager2},
		Threshold: 2,
	}))

	payload := coordinatorPayload(s.T(), "/svote.v1.MsgUpdateVoteManagers", &types.MsgUpdateVoteManagers{
		Creator:         manager1,
		NewVoteManagers: []string{manager1, manager2},
		NewThreshold:    1,
	})
	resp, err := s.msgServer.ProposeCoordinatorAction(s.ctx, &types.MsgProposeCoordinatorAction{
		Creator: manager1,
		Payload: payload,
	})
	s.Require().NoError(err)
	s.Require().False(resp.Executed)

	action, err := s.keeper.GetCoordinatorAction(kv, resp.ActionId)
	s.Require().NoError(err)
	action.ExpiresAt = 1
	s.Require().NoError(s.keeper.SetCoordinatorAction(kv, action))

	_, err = s.msgServer.ApproveCoordinatorAction(s.ctx, &types.MsgApproveCoordinatorAction{
		Creator:  manager2,
		ActionId: resp.ActionId,
	})
	s.Require().ErrorIs(err, types.ErrCoordinatorActionExpired)

	action, err = s.keeper.GetCoordinatorAction(kv, resp.ActionId)
	s.Require().NoError(err)
	s.Require().Equal(types.CoordinatorActionStatus_COORDINATOR_ACTION_STATUS_EXPIRED, action.Status)
}

func (s *MsgServerTestSuite) TestCoordinatorAction_AuthorizedSendRequiresSourceApproval() {
	manager1 := svtest.TestAccAddr(0x61)
	manager2 := svtest.TestAccAddr(0x62)
	recipient := svtest.TestAccAddr(0x63)
	kv := s.keeper.OpenKVStore(s.ctx)
	s.Require().NoError(s.keeper.SetVoteManagers(kv, &types.VoteManagerSet{
		Addresses: []string{manager1, manager2},
		Threshold: 1,
	}))
	bk := newMockBankKeeper()
	s.setupWithMockBankKeeper(bk)

	payload := coordinatorPayload(s.T(), "/svote.v1.MsgAuthorizedSend", &types.MsgAuthorizedSend{
		FromAddress: manager2,
		ToAddress:   recipient,
		Amount:      "10",
		Denom:       "usvote",
	})
	_, err := s.msgServer.ProposeCoordinatorAction(s.ctx, &types.MsgProposeCoordinatorAction{
		Creator: manager1,
		Payload: payload,
	})
	s.Require().ErrorIs(err, types.ErrNotAuthorized)
	s.Require().Empty(bk.sendCalls)
}
