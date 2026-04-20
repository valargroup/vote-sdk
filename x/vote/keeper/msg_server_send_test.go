package keeper_test

import (
	sdkmath "cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/valargroup/vote-sdk/x/vote/types"
)

// accToValoper converts an account address to its valoper equivalent
// (same bytes, different bech32 prefix). This mirrors the conversion
// in the AuthorizedSend handler.
func accToValoper(accBech32 string) string {
	acc, _ := sdk.AccAddressFromBech32(accBech32)
	return sdk.ValAddress(acc).String()
}

// ---------------------------------------------------------------------------
// AuthorizedSend — vote-manager sender tests
// ---------------------------------------------------------------------------

func (s *MsgServerTestSuite) TestAuthorizedSend_VoteManagerCanSendToAnyone() {
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
	s.Require().NoError(err)
	s.Require().Len(bk.sendCalls, 1)

	from, _ := sdk.AccAddressFromBech32(vm)
	to, _ := sdk.AccAddressFromBech32(recipient)
	s.Require().Equal(from, bk.sendCalls[0].From)
	s.Require().Equal(to, bk.sendCalls[0].To)
	s.Require().Equal(
		sdk.NewCoins(sdk.NewCoin("usvote", sdkmath.NewInt(1_000_000))),
		bk.sendCalls[0].Amt,
	)
}

func (s *MsgServerTestSuite) TestAuthorizedSend_VoteManagerCanSendToValidator() {
	s.SetupTest()
	bk := newMockBankKeeper()
	s.setupWithMockBankKeeper(bk)

	vm := testAccAddr(1)
	valAcc := testAccAddr(10)
	s.seedVoteManagers(vm)
	s.setupWithMockStaking(accToValoper(valAcc))

	_, err := s.msgServer.AuthorizedSend(s.ctx, &types.MsgAuthorizedSend{
		FromAddress: vm,
		ToAddress:   valAcc,
		Amount:      "500",
		Denom:       "usvote",
	})
	s.Require().NoError(err)
	s.Require().Len(bk.sendCalls, 1)
}

func (s *MsgServerTestSuite) TestAuthorizedSend_VoteManagerCanSendToOtherVoteManager() {
	s.SetupTest()
	bk := newMockBankKeeper()
	s.setupWithMockBankKeeper(bk)

	vmA := testAccAddr(1)
	vmB := testAccAddr(2)
	s.seedVoteManagers(vmA, vmB)

	_, err := s.msgServer.AuthorizedSend(s.ctx, &types.MsgAuthorizedSend{
		FromAddress: vmA,
		ToAddress:   vmB,
		Amount:      "100",
		Denom:       "usvote",
	})
	s.Require().NoError(err)
	s.Require().Len(bk.sendCalls, 1)
}

// ---------------------------------------------------------------------------
// AuthorizedSend — validator tests
// ---------------------------------------------------------------------------

func (s *MsgServerTestSuite) TestAuthorizedSend_ValidatorCanSendToVoteManager() {
	// Parametrize over every vm in a multi-vm set — proves the recipient
	// check iterates the full set (not e.g. only vms[0]).
	vmA := testAccAddr(1)
	vmB := testAccAddr(2)
	vmC := testAccAddr(3)
	valAcc := testAccAddr(10)

	for _, recipient := range []string{vmA, vmB, vmC} {
		s.Run("recipient="+recipient, func() {
			s.SetupTest()
			bk := newMockBankKeeper()
			s.setupWithMockBankKeeper(bk)
			s.seedVoteManagers(vmA, vmB, vmC)
			s.setupWithMockStaking(accToValoper(valAcc))

			_, err := s.msgServer.AuthorizedSend(s.ctx, &types.MsgAuthorizedSend{
				FromAddress: valAcc,
				ToAddress:   recipient,
				Amount:      "100",
				Denom:       "usvote",
			})
			s.Require().NoError(err)
			s.Require().Len(bk.sendCalls, 1)
		})
	}
}

func (s *MsgServerTestSuite) TestAuthorizedSend_ValidatorCanSendToOtherValidator() {
	s.SetupTest()
	bk := newMockBankKeeper()
	s.setupWithMockBankKeeper(bk)

	vm := testAccAddr(1)
	val1Acc := testAccAddr(10)
	val2Acc := testAccAddr(11)
	s.seedVoteManagers(vm)
	s.setupWithMockStaking(accToValoper(val1Acc), accToValoper(val2Acc))

	_, err := s.msgServer.AuthorizedSend(s.ctx, &types.MsgAuthorizedSend{
		FromAddress: val1Acc,
		ToAddress:   val2Acc,
		Amount:      "200",
		Denom:       "usvote",
	})
	s.Require().NoError(err)
	s.Require().Len(bk.sendCalls, 1)
}

func (s *MsgServerTestSuite) TestAuthorizedSend_ValidatorCannotSendToNonValidator() {
	s.SetupTest()
	bk := newMockBankKeeper()
	s.setupWithMockBankKeeper(bk)

	vm := testAccAddr(1)
	valAcc := testAccAddr(10)
	random := testAccAddr(99)
	s.seedVoteManagers(vm)
	s.setupWithMockStaking(accToValoper(valAcc))

	_, err := s.msgServer.AuthorizedSend(s.ctx, &types.MsgAuthorizedSend{
		FromAddress: valAcc,
		ToAddress:   random,
		Amount:      "100",
		Denom:       "usvote",
	})
	s.Require().Error(err)
	s.Require().Contains(err.Error(), "can only send to a vote manager or another bonded validator")
	s.Require().Empty(bk.sendCalls)
}

// ---------------------------------------------------------------------------
// AuthorizedSend — unauthorized sender tests
// ---------------------------------------------------------------------------

func (s *MsgServerTestSuite) TestAuthorizedSend_NonPrivilegedSenderRejected() {
	s.SetupTest()
	bk := newMockBankKeeper()
	s.setupWithMockBankKeeper(bk)

	vm := testAccAddr(1)
	random := testAccAddr(50)
	s.seedVoteManagers(vm)
	s.setupWithMockStaking() // no validators

	_, err := s.msgServer.AuthorizedSend(s.ctx, &types.MsgAuthorizedSend{
		FromAddress: random,
		ToAddress:   vm,
		Amount:      "100",
		Denom:       "usvote",
	})
	s.Require().Error(err)
	s.Require().Contains(err.Error(), "neither a vote manager nor a bonded validator")
	s.Require().Empty(bk.sendCalls)
}

func (s *MsgServerTestSuite) TestAuthorizedSend_NoVoteManagersSet_ValidatorRejected() {
	s.SetupTest()
	bk := newMockBankKeeper()
	s.setupWithMockBankKeeper(bk)

	valAcc := testAccAddr(10)
	recipient := testAccAddr(20)
	// No vote-manager set seeded.
	s.setupWithMockStaking(accToValoper(valAcc))

	_, err := s.msgServer.AuthorizedSend(s.ctx, &types.MsgAuthorizedSend{
		FromAddress: valAcc,
		ToAddress:   recipient,
		Amount:      "100",
		Denom:       "usvote",
	})
	s.Require().Error(err)
	// Validator sending to non-validator, non-vote-manager should fail.
	s.Require().Contains(err.Error(), "can only send to a vote manager or another bonded validator")
}

// ---------------------------------------------------------------------------
// AuthorizedSend — field validation tests
// ---------------------------------------------------------------------------

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

// ---------------------------------------------------------------------------
// AuthorizedSend — event emission
// ---------------------------------------------------------------------------

func (s *MsgServerTestSuite) TestAuthorizedSend_EmitsEvent() {
	s.SetupTest()
	bk := newMockBankKeeper()
	s.setupWithMockBankKeeper(bk)

	vm := testAccAddr(1)
	recipient := testAccAddr(2)
	s.seedVoteManagers(vm)

	_, err := s.msgServer.AuthorizedSend(s.ctx, &types.MsgAuthorizedSend{
		FromAddress: vm,
		ToAddress:   recipient,
		Amount:      "42",
		Denom:       "usvote",
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

// ---------------------------------------------------------------------------
// AuthorizedSend — revoked-vm balance freeze
// ---------------------------------------------------------------------------
//
// After MsgUpdateVoteManagers removes a vote manager, their remaining balance
// is one-way frozen: they can't send to anyone (not a vote manager, not a
// validator), and bonded validators can't send to them either. Active vote
// managers can still send to them. These three tests pin that behavior.

func (s *MsgServerTestSuite) TestAuthorizedSend_RevokedVoteManagerCannotSend() {
	s.SetupTest()
	bk := newMockBankKeeper()
	s.setupWithMockBankKeeper(bk)
	s.setupWithMockStaking() // no validators bonded

	vmA := testAccAddr(1)
	revoked := testAccAddr(2)
	s.seedVoteManagers(vmA)

	_, err := s.msgServer.AuthorizedSend(s.ctx, &types.MsgAuthorizedSend{
		FromAddress: revoked,
		ToAddress:   vmA,
		Amount:      "1",
		Denom:       "usvote",
	})
	s.Require().ErrorIs(err, types.ErrUnauthorizedSend)
	s.Require().Empty(bk.sendCalls)
}

func (s *MsgServerTestSuite) TestAuthorizedSend_VoteManagerCanSendToRevokedVoteManager() {
	s.SetupTest()
	bk := newMockBankKeeper()
	s.setupWithMockBankKeeper(bk)

	vmA := testAccAddr(1)
	revoked := testAccAddr(2)
	s.seedVoteManagers(vmA)

	// Vote-managers-send-to-anyone takes the early-return path; no validator check.
	_, err := s.msgServer.AuthorizedSend(s.ctx, &types.MsgAuthorizedSend{
		FromAddress: vmA,
		ToAddress:   revoked,
		Amount:      "1",
		Denom:       "usvote",
	})
	s.Require().NoError(err)
	s.Require().Len(bk.sendCalls, 1)
}

func (s *MsgServerTestSuite) TestAuthorizedSend_ValidatorCannotSendToRevokedVoteManager() {
	s.SetupTest()
	bk := newMockBankKeeper()
	s.setupWithMockBankKeeper(bk)

	vmA := testAccAddr(1)
	valAcc := testAccAddr(10)
	revoked := testAccAddr(2)
	s.seedVoteManagers(vmA)
	s.setupWithMockStaking(accToValoper(valAcc))

	_, err := s.msgServer.AuthorizedSend(s.ctx, &types.MsgAuthorizedSend{
		FromAddress: valAcc,
		ToAddress:   revoked,
		Amount:      "1",
		Denom:       "usvote",
	})
	s.Require().ErrorIs(err, types.ErrUnauthorizedSend)
	s.Require().Empty(bk.sendCalls)
}
