//go:build !proxyenabled

package keeper_test

import (
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	svtest "github.com/valargroup/vote-sdk/testutil"
	"github.com/valargroup/vote-sdk/testutil/proxytest"
	"github.com/valargroup/vote-sdk/x/vote/types"
)

// These tests pin proxy delegation's dormant behavior
// (docs/proxy-delegation/contracts-v1.md §9), so they build only without the
// proxyenabled tag.

// requireProxyDelegationDisabled fails unless err is ErrProxyDelegationDisabled
// with its registered code 69 in codespace vote.
func requireProxyDelegationDisabled(t *testing.T, err error) {
	t.Helper()
	proxytest.RequireCode(t, err, types.ErrProxyDelegationDisabled, 69)
}

func (s *MsgServerTestSuite) TestCreateVotingSession_ProxyFlagDormant() {
	s.seedEligibleValidators(2)
	s.seedVoteManagers(svtest.DefaultVoteManagerAddress)
	msg := validSetupMsg()
	msg.ProxyDelegation = true

	_, err := s.proposeCoordinatorAction(s.ctx, msg.Creator, msg)
	requireProxyDelegationDisabled(s.T(), err)

	kv := s.keeper.OpenKVStore(s.ctx)
	_, err = s.keeper.GetVoteRound(kv, computeExpectedRoundID(msg, uint64(s.ctx.BlockHeight())))
	s.Require().ErrorIs(err, types.ErrRoundNotFound)

	// The same payload without the flag still creates the round.
	msg.ProxyDelegation = false
	resp, err := s.createVotingSessionViaCoordinator(s.ctx, msg)
	s.Require().NoError(err)
	round, err := s.keeper.GetVoteRound(kv, resp.VoteRoundId)
	s.Require().NoError(err)
	s.Require().Nil(round.ProxyDelegation)
}

// storePendingAction stores a pending action with payload, approved by its
// proposer only, as a genesis import could hold it without a proposal.
func (s *MsgServerTestSuite) storePendingAction(actionID uint64, proposer string, payload proto.Message) {
	kv := s.keeper.OpenKVStore(s.ctx)
	createdAt := uint64(s.ctx.BlockTime().Unix())
	s.Require().NoError(s.keeper.SetCoordinatorAction(kv, &types.CoordinatorAction{
		ActionId:  actionID,
		Payload:   coordinatorPayloadForMessage(s.T(), payload),
		Proposer:  proposer,
		Approvals: []string{proposer},
		Status:    types.CoordinatorActionStatus_COORDINATOR_ACTION_STATUS_PENDING,
		CreatedAt: createdAt,
		ExpiresAt: createdAt + 3600,
	}))
}

// TestCreateVotingSession_ProxyFlagDormantAtExecution checks that the keeper
// also refuses the round flag when a stored action reaches execution.
func (s *MsgServerTestSuite) TestCreateVotingSession_ProxyFlagDormantAtExecution() {
	s.seedEligibleValidators(2)
	manager := svtest.DefaultVoteManagerAddress
	s.seedVoteManagers(manager)
	msg := validSetupMsg()
	msg.ProxyDelegation = true
	s.storePendingAction(1, manager, msg)

	_, err := s.msgServer.ApproveCoordinatorAction(s.ctx, &types.MsgApproveCoordinatorAction{Creator: manager, ActionId: 1})
	requireProxyDelegationDisabled(s.T(), err)
	_, err = s.keeper.GetVoteRound(s.keeper.OpenKVStore(s.ctx), computeExpectedRoundID(msg, uint64(s.ctx.BlockHeight())))
	s.Require().ErrorIs(err, types.ErrRoundNotFound)
}

// TestProxyCoordinatorPayloadsDormant checks that a stored proxy delegation
// action reaching execution stays an unsupported coordinator action, as
// before the payloads were registered, which at threshold 1 it does on one
// approval because the floor is dormant too. The app tests check the
// refusal at proposal.
func (s *MsgServerTestSuite) TestProxyCoordinatorPayloadsDormant() {
	manager := svtest.DefaultVoteManagerAddress
	s.seedVoteManagers(manager)
	for i, payload := range []proto.Message{
		&types.MsgSetProxyDelegationParams{Creator: manager, MaxDelegates: 5},
		&types.MsgSetProxyDelegationPause{Creator: manager, Paused: true},
		&types.MsgSetDelegateStatus{
			Creator:         manager,
			DelegateIndexes: []uint32{1},
			Action:          types.DelegateStatusActionSuspend,
			Reason:          types.DelegateStatusReasonOther,
		},
	} {
		actionID := uint64(100 + i)
		s.storePendingAction(actionID, manager, payload)
		_, err := s.msgServer.ApproveCoordinatorAction(s.ctx, &types.MsgApproveCoordinatorAction{Creator: manager, ActionId: actionID})
		s.Require().ErrorIs(err, types.ErrUnsupportedCoordinatorAction)
		s.Require().EqualError(err, "unsupported coordinator action: /"+string(proto.MessageName(payload)))
	}
	has, err := s.keeper.OpenKVStore(s.ctx).Has(types.ProxyDelegationParamsKey)
	s.Require().NoError(err)
	s.Require().False(has)
}

func (s *QueryServerTestSuite) TestProxyDelegationQueriesDormant() {
	queries := map[string]func() (any, error){
		"ProxyDelegates": func() (any, error) {
			return s.queryServer.ProxyDelegates(s.ctx, &types.QueryProxyDelegatesRequest{})
		},
		"ProxyDelegate": func() (any, error) {
			return s.queryServer.ProxyDelegate(s.ctx, &types.QueryProxyDelegateRequest{DelegateIndex: 1})
		},
		"ProxyDelegateByKey": func() (any, error) {
			return s.queryServer.ProxyDelegateByKey(s.ctx, &types.QueryProxyDelegateByKeyRequest{Dk: make([]byte, 32)})
		},
		"DelegateBallots": func() (any, error) {
			return s.queryServer.DelegateBallots(s.ctx, &types.QueryDelegateBallotsRequest{VoteRoundId: make([]byte, 32)})
		},
		"ProxyDelegationParams": func() (any, error) {
			return s.queryServer.ProxyDelegationParams(s.ctx, &types.QueryProxyDelegationParamsRequest{})
		},
	}
	for name, query := range queries {
		s.Run(name, func() {
			_, err := query()
			s.Require().Equal(codes.Unimplemented, status.Code(err), "error: %v", err)
		})
	}
}

// TestProxyHandlersDormant checks that with the gate closed the 0x0B
// handler answers a bare ErrProxyDelegationDisabled before any other check,
// well-formed or not, and writes nothing.
func TestProxyHandlersDormant(t *testing.T) {
	s := new(ProxyRegistryTestSuite)
	s.SetT(t)
	s.SetupTest()
	s.openRegistry()
	_, err := s.msgServer.DelegateOp(s.ctx, s.registerOp(newEntryRegistration(proxytest.Key("alice/1"), "alice"), 1))
	requireProxyDelegationDisabled(t, err)
	_, err = s.msgServer.DelegateOp(s.ctx, &types.MsgDelegateOp{})
	requireProxyDelegationDisabled(t, err)
	s.requireNoRegistrations()
}
