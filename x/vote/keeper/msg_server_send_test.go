package keeper_test

import (
	"fmt"

	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"

	"github.com/valargroup/vote-sdk/x/vote/types"
)

func (s *MsgServerTestSuite) TestCoordinatorAction_AuthorizedSendUsesVoteFundingModule() {
	s.SetupTest()
	bk := newMockBankKeeper()
	s.setupWithMockBankKeeper(bk)

	vm := testAccAddr(1)
	prospectiveValidator := testAccAddr(10)
	s.seedVoteManagers(vm)

	payload := coordinatorPayload(s.T(), "/svote.v1.MsgAuthorizedSend", &types.MsgAuthorizedSend{
		Creator:   vm,
		ToAddress: prospectiveValidator,
		Amount:    "500",
	})
	resp, err := s.msgServer.ProposeCoordinatorAction(s.ctx, &types.MsgProposeCoordinatorAction{
		Creator: vm,
		Payload: payload,
	})
	s.Require().NoError(err)
	s.Require().True(resp.Executed)
	s.Require().Len(bk.moduleSendCalls, 1)
	s.Require().Equal(types.VoteFundingModuleName, bk.moduleSendCalls[0].Module)
	s.Require().Equal(prospectiveValidator, bk.moduleSendCalls[0].To.String())
	s.Require().Equal(sdk.NewCoins(sdk.NewInt64Coin(sdk.DefaultBondDenom, 500)), bk.moduleSendCalls[0].Amt)
}

func (s *MsgServerTestSuite) TestCoordinatorAction_AuthorizedSendRejectsCreatorMismatch() {
	s.SetupTest()
	bk := newMockBankKeeper()
	s.setupWithMockBankKeeper(bk)

	vm := testAccAddr(1)
	otherManager := testAccAddr(2)
	recipient := testAccAddr(10)
	kv := s.keeper.OpenKVStore(s.ctx)
	s.Require().NoError(s.keeper.SetVoteManagers(kv, &types.VoteManagerSet{
		Addresses: []string{vm, otherManager},
		Threshold: 1,
	}))

	payload := coordinatorPayload(s.T(), "/svote.v1.MsgAuthorizedSend", &types.MsgAuthorizedSend{
		Creator:   otherManager,
		ToAddress: recipient,
		Amount:    "500",
	})
	_, err := s.msgServer.ProposeCoordinatorAction(s.ctx, &types.MsgProposeCoordinatorAction{
		Creator: vm,
		Payload: payload,
	})
	s.Require().ErrorIs(err, types.ErrInvalidCoordinatorAction)
	s.Require().Contains(err.Error(), "must match proposer")
	s.Require().Empty(bk.moduleSendCalls)

	var pending []*types.CoordinatorAction
	s.Require().NoError(s.keeper.IteratePendingCoordinatorActions(kv, func(action *types.CoordinatorAction) bool {
		pending = append(pending, action)
		return false
	}))
	s.Require().Empty(pending)
}

func (s *MsgServerTestSuite) TestAuthorizedSend_InvalidCreatorAddress() {
	s.SetupTest()
	bk := newMockBankKeeper()
	s.setupWithMockBankKeeper(bk)
	vm := testAccAddr(1)
	s.seedVoteManagers(vm)

	_, err := s.proposeCoordinatorAction(s.ctx, vm, &types.MsgAuthorizedSend{
		Creator:   "not_valid",
		ToAddress: testAccAddr(2),
		Amount:    "100",
	})
	s.Require().Error(err)
	s.Require().Contains(err.Error(), "creator")
	s.Require().Empty(bk.moduleSendCalls)
}

func (s *MsgServerTestSuite) TestAuthorizedSend_InvalidToAddress() {
	s.SetupTest()
	bk := newMockBankKeeper()
	s.setupWithMockBankKeeper(bk)
	s.seedVoteManagers(testAccAddr(1))

	vm := testAccAddr(1)
	_, err := s.proposeCoordinatorAction(s.ctx, vm, &types.MsgAuthorizedSend{
		Creator:   vm,
		ToAddress: "bad_addr",
		Amount:    "100",
	})
	s.Require().Error(err)
	s.Require().Contains(err.Error(), "invalid to_address")
}

func (s *MsgServerTestSuite) TestAuthorizedSend_ZeroAmount() {
	s.SetupTest()
	bk := newMockBankKeeper()
	s.setupWithMockBankKeeper(bk)
	s.seedVoteManagers(testAccAddr(1))

	vm := testAccAddr(1)
	_, err := s.proposeCoordinatorAction(s.ctx, vm, &types.MsgAuthorizedSend{
		Creator:   vm,
		ToAddress: testAccAddr(2),
		Amount:    "0",
	})
	s.Require().Error(err)
	s.Require().Contains(err.Error(), "amount must be a positive integer string")
}

func (s *MsgServerTestSuite) TestAuthorizedSend_NegativeAmount() {
	s.SetupTest()
	bk := newMockBankKeeper()
	s.setupWithMockBankKeeper(bk)
	s.seedVoteManagers(testAccAddr(1))

	vm := testAccAddr(1)
	_, err := s.proposeCoordinatorAction(s.ctx, vm, &types.MsgAuthorizedSend{
		Creator:   vm,
		ToAddress: testAccAddr(2),
		Amount:    "-500",
	})
	s.Require().Error(err)
	s.Require().Contains(err.Error(), "amount must be a positive integer string")
}

func (s *MsgServerTestSuite) TestAuthorizedSend_NonNumericAmount() {
	s.SetupTest()
	bk := newMockBankKeeper()
	s.setupWithMockBankKeeper(bk)
	s.seedVoteManagers(testAccAddr(1))

	vm := testAccAddr(1)
	_, err := s.proposeCoordinatorAction(s.ctx, vm, &types.MsgAuthorizedSend{
		Creator:   vm,
		ToAddress: testAccAddr(2),
		Amount:    "abc",
	})
	s.Require().Error(err)
	s.Require().Contains(err.Error(), "amount must be a positive integer string")
}

func (s *MsgServerTestSuite) TestCoordinatorAction_AuthorizedSendReturnsBankError() {
	s.SetupTest()
	bk := newMockBankKeeper()
	bk.moduleSendErr = fmt.Errorf("insufficient funds")
	s.setupWithMockBankKeeper(bk)

	vm := testAccAddr(1)
	recipient := testAccAddr(10)
	s.seedVoteManagers(vm)

	payload := coordinatorPayload(s.T(), "/svote.v1.MsgAuthorizedSend", &types.MsgAuthorizedSend{
		Creator:   vm,
		ToAddress: recipient,
		Amount:    "42",
	})
	_, err := s.msgServer.ProposeCoordinatorAction(s.ctx, &types.MsgProposeCoordinatorAction{
		Creator: vm,
		Payload: payload,
	})
	s.Require().Error(err)
	s.Require().Contains(err.Error(), "send from vote_funding module failed")
}

func (s *MsgServerTestSuite) TestCoordinatorAction_AuthorizedSendEmitsEvent() {
	s.SetupTest()
	bk := newMockBankKeeper()
	s.setupWithMockBankKeeper(bk)

	vm := testAccAddr(1)
	recipient := testAccAddr(10)
	s.seedVoteManagers(vm)

	payload := coordinatorPayload(s.T(), "/svote.v1.MsgAuthorizedSend", &types.MsgAuthorizedSend{
		Creator:   vm,
		ToAddress: recipient,
		Amount:    "42",
	})
	_, err := s.msgServer.ProposeCoordinatorAction(s.ctx, &types.MsgProposeCoordinatorAction{
		Creator: vm,
		Payload: payload,
	})
	s.Require().NoError(err)

	moduleAddr := authtypes.NewModuleAddress(types.VoteFundingModuleName).String()
	var found bool
	for _, e := range s.ctx.EventManager().Events() {
		if e.Type == types.EventTypeAuthorizedSend {
			found = true
			for _, attr := range e.Attributes {
				switch attr.Key {
				case types.AttributeKeyCreator:
					s.Require().Equal(vm, attr.Value)
				case types.AttributeKeySender:
					s.Require().Equal(moduleAddr, attr.Value)
				case types.AttributeKeyRecipient:
					s.Require().Equal(recipient, attr.Value)
				case types.AttributeKeyAmount:
					s.Require().Equal("42usvote", attr.Value)
				}
			}
		}
	}
	s.Require().True(found, "expected %s event", types.EventTypeAuthorizedSend)
}
