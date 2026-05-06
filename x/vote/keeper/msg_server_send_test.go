package keeper_test

import (
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/valargroup/vote-sdk/x/vote/types"
)

// accToValoper converts an account address to its valoper equivalent.
func accToValoper(accBech32 string) string {
	acc, _ := sdk.AccAddressFromBech32(accBech32)
	return sdk.ValAddress(acc).String()
}

func (s *MsgServerTestSuite) TestAuthorizedSend_DirectVoteManagerSendRequiresCoordinatorAction() {
	s.SetupTest()
	bk := newMockBankKeeper()
	s.setupWithMockBankKeeper(bk)

	vm := testAccAddr(1)
	recipient := testAccAddr(2)
	s.seedVoteManagers(vm)

	_, err := s.msgServer.AuthorizedSend(s.ctx, &types.MsgAuthorizedSend{
		FromAddress: vm,
		ToAddress:   recipient,
		Amount:      "1000000",
		Denom:       "usvote",
	})
	s.Require().ErrorIs(err, types.ErrCoordinatorActionRequired)
	s.Require().Empty(bk.sendCalls)
}

func (s *MsgServerTestSuite) TestAuthorizedSend_DirectBondedValidatorSendRequiresCoordinatorAction() {
	s.SetupTest()
	bk := newMockBankKeeper()
	s.setupWithMockBankKeeper(bk)

	vm := testAccAddr(1)
	valAcc := testAccAddr(10)
	s.seedVoteManagers(vm)
	s.setupWithMockStaking(accToValoper(valAcc))

	_, err := s.msgServer.AuthorizedSend(s.ctx, &types.MsgAuthorizedSend{
		FromAddress: valAcc,
		ToAddress:   vm,
		Amount:      "100",
		Denom:       "usvote",
	})
	s.Require().ErrorIs(err, types.ErrCoordinatorActionRequired)
	s.Require().Empty(bk.sendCalls)
}

func (s *MsgServerTestSuite) TestCoordinatorAction_AuthorizedSendVoteManagerCanFundProspectiveValidator() {
	s.SetupTest()
	bk := newMockBankKeeper()
	s.setupWithMockBankKeeper(bk)

	vm := testAccAddr(1)
	prospectiveValidator := testAccAddr(10)
	s.seedVoteManagers(vm)

	payload := coordinatorPayload(s.T(), "/svote.v1.MsgAuthorizedSend", &types.MsgAuthorizedSend{
		FromAddress: vm,
		ToAddress:   prospectiveValidator,
		Amount:      "500",
		Denom:       "usvote",
	})
	resp, err := s.msgServer.ProposeCoordinatorAction(s.ctx, &types.MsgProposeCoordinatorAction{
		Creator: vm,
		Payload: payload,
	})
	s.Require().NoError(err)
	s.Require().True(resp.Executed)
	s.Require().Len(bk.sendCalls, 1)
}

func (s *MsgServerTestSuite) TestCoordinatorAction_AuthorizedSendRejectsNonCoordinatorSource() {
	s.SetupTest()
	bk := newMockBankKeeper()
	s.setupWithMockBankKeeper(bk)

	vm := testAccAddr(1)
	nonCoordinator := testAccAddr(2)
	recipient := testAccAddr(10)
	s.seedVoteManagers(vm)

	payload := coordinatorPayload(s.T(), "/svote.v1.MsgAuthorizedSend", &types.MsgAuthorizedSend{
		FromAddress: nonCoordinator,
		ToAddress:   recipient,
		Amount:      "500",
		Denom:       "usvote",
	})
	_, err := s.msgServer.ProposeCoordinatorAction(s.ctx, &types.MsgProposeCoordinatorAction{
		Creator: vm,
		Payload: payload,
	})
	s.Require().ErrorIs(err, types.ErrUnauthorizedSend)
	s.Require().Empty(bk.sendCalls)
}

func (s *MsgServerTestSuite) TestAuthorizedSend_InvalidFromAddress() {
	s.SetupTest()
	bk := newMockBankKeeper()
	s.setupWithMockBankKeeper(bk)

	_, err := s.msgServer.AuthorizedSend(s.ctx, &types.MsgAuthorizedSend{
		FromAddress: "not_valid",
		ToAddress:   testAccAddr(1),
		Amount:      "100",
		Denom:       "usvote",
	})
	s.Require().Error(err)
	s.Require().Contains(err.Error(), "invalid from_address")
}

func (s *MsgServerTestSuite) TestAuthorizedSend_InvalidToAddress() {
	s.SetupTest()
	bk := newMockBankKeeper()
	s.setupWithMockBankKeeper(bk)
	s.seedVoteManagers(testAccAddr(1))

	_, err := s.msgServer.AuthorizedSend(s.ctx, &types.MsgAuthorizedSend{
		FromAddress: testAccAddr(1),
		ToAddress:   "bad_addr",
		Amount:      "100",
		Denom:       "usvote",
	})
	s.Require().Error(err)
	s.Require().Contains(err.Error(), "invalid to_address")
}

func (s *MsgServerTestSuite) TestAuthorizedSend_ZeroAmount() {
	s.SetupTest()
	bk := newMockBankKeeper()
	s.setupWithMockBankKeeper(bk)
	s.seedVoteManagers(testAccAddr(1))

	_, err := s.msgServer.AuthorizedSend(s.ctx, &types.MsgAuthorizedSend{
		FromAddress: testAccAddr(1),
		ToAddress:   testAccAddr(2),
		Amount:      "0",
		Denom:       "usvote",
	})
	s.Require().Error(err)
	s.Require().Contains(err.Error(), "amount must be a positive integer string")
}

func (s *MsgServerTestSuite) TestAuthorizedSend_NegativeAmount() {
	s.SetupTest()
	bk := newMockBankKeeper()
	s.setupWithMockBankKeeper(bk)
	s.seedVoteManagers(testAccAddr(1))

	_, err := s.msgServer.AuthorizedSend(s.ctx, &types.MsgAuthorizedSend{
		FromAddress: testAccAddr(1),
		ToAddress:   testAccAddr(2),
		Amount:      "-500",
		Denom:       "usvote",
	})
	s.Require().Error(err)
	s.Require().Contains(err.Error(), "amount must be a positive integer string")
}

func (s *MsgServerTestSuite) TestAuthorizedSend_EmptyDenom() {
	s.SetupTest()
	bk := newMockBankKeeper()
	s.setupWithMockBankKeeper(bk)
	s.seedVoteManagers(testAccAddr(1))

	_, err := s.msgServer.AuthorizedSend(s.ctx, &types.MsgAuthorizedSend{
		FromAddress: testAccAddr(1),
		ToAddress:   testAccAddr(2),
		Amount:      "100",
		Denom:       "",
	})
	s.Require().Error(err)
	s.Require().Contains(err.Error(), "denom cannot be empty")
}

func (s *MsgServerTestSuite) TestAuthorizedSend_NonNumericAmount() {
	s.SetupTest()
	bk := newMockBankKeeper()
	s.setupWithMockBankKeeper(bk)
	s.seedVoteManagers(testAccAddr(1))

	_, err := s.msgServer.AuthorizedSend(s.ctx, &types.MsgAuthorizedSend{
		FromAddress: testAccAddr(1),
		ToAddress:   testAccAddr(2),
		Amount:      "abc",
		Denom:       "usvote",
	})
	s.Require().Error(err)
	s.Require().Contains(err.Error(), "amount must be a positive integer string")
}

func (s *MsgServerTestSuite) TestCoordinatorAction_AuthorizedSendEmitsEvent() {
	s.SetupTest()
	bk := newMockBankKeeper()
	s.setupWithMockBankKeeper(bk)

	vm := testAccAddr(1)
	recipient := testAccAddr(10)
	s.seedVoteManagers(vm)

	payload := coordinatorPayload(s.T(), "/svote.v1.MsgAuthorizedSend", &types.MsgAuthorizedSend{
		FromAddress: vm,
		ToAddress:   recipient,
		Amount:      "42",
		Denom:       "usvote",
	})
	_, err := s.msgServer.ProposeCoordinatorAction(s.ctx, &types.MsgProposeCoordinatorAction{
		Creator: vm,
		Payload: payload,
	})
	s.Require().NoError(err)

	var found bool
	for _, e := range s.ctx.EventManager().Events() {
		if e.Type == types.EventTypeAuthorizedSend {
			found = true
			for _, attr := range e.Attributes {
				switch attr.Key {
				case types.AttributeKeySender:
					s.Require().Equal(vm, attr.Value)
				case types.AttributeKeyRecipient:
					s.Require().Equal(recipient, attr.Value)
				}
			}
		}
	}
	s.Require().True(found, "expected %s event", types.EventTypeAuthorizedSend)
}

func (s *MsgServerTestSuite) TestCoordinatorAction_AuthorizedSendRevokedVoteManagerCannotFund() {
	s.SetupTest()
	bk := newMockBankKeeper()
	s.setupWithMockBankKeeper(bk)

	vm := testAccAddr(1)
	revoked := testAccAddr(2)
	s.seedVoteManagers(vm)

	payload := coordinatorPayload(s.T(), "/svote.v1.MsgAuthorizedSend", &types.MsgAuthorizedSend{
		FromAddress: revoked,
		ToAddress:   testAccAddr(10),
		Amount:      "1",
		Denom:       "usvote",
	})
	_, err := s.msgServer.ProposeCoordinatorAction(s.ctx, &types.MsgProposeCoordinatorAction{
		Creator: vm,
		Payload: payload,
	})
	s.Require().ErrorIs(err, types.ErrUnauthorizedSend)
	s.Require().Empty(bk.sendCalls)
}
