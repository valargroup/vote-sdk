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
// AuthorizedSend — vote manager tests
// ---------------------------------------------------------------------------

func (s *MsgServerTestSuite) TestAuthorizedSend_VoteManagerCanSendToAnyone() {
	s.SetupTest()
	bk := newMockBankKeeper()
	s.setupWithMockBankKeeper(bk)

	mgr := testAccAddr(1)
	recipient := testAccAddr(2)
	s.seedVoteManager(mgr)

	_, err := s.msgServer.AuthorizedSend(s.ctx, &types.MsgAuthorizedSend{
		FromAddress: mgr,
		ToAddress:   recipient,
		Amount:      "1000000",
		Denom:       "usvote",
	})
	s.Require().NoError(err)
	s.Require().Len(bk.sendCalls, 1)

	from, _ := sdk.AccAddressFromBech32(mgr)
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

	mgr := testAccAddr(1)
	valAcc := testAccAddr(10)
	s.seedVoteManager(mgr)
	s.setupWithMockStaking(accToValoper(valAcc))

	_, err := s.msgServer.AuthorizedSend(s.ctx, &types.MsgAuthorizedSend{
		FromAddress: mgr,
		ToAddress:   valAcc,
		Amount:      "500",
		Denom:       "usvote",
	})
	s.Require().NoError(err)
	s.Require().Len(bk.sendCalls, 1)
}

// ---------------------------------------------------------------------------
// AuthorizedSend — validator tests
// ---------------------------------------------------------------------------

func (s *MsgServerTestSuite) TestAuthorizedSend_ValidatorCanSendToVoteManager() {
	s.SetupTest()
	bk := newMockBankKeeper()
	s.setupWithMockBankKeeper(bk)

	mgr := testAccAddr(1)
	valAcc := testAccAddr(10)
	s.seedVoteManager(mgr)
	s.setupWithMockStaking(accToValoper(valAcc))

	_, err := s.msgServer.AuthorizedSend(s.ctx, &types.MsgAuthorizedSend{
		FromAddress: valAcc,
		ToAddress:   mgr,
		Amount:      "100",
		Denom:       "usvote",
	})
	s.Require().NoError(err)
	s.Require().Len(bk.sendCalls, 1)
}

func (s *MsgServerTestSuite) TestAuthorizedSend_ValidatorCanSendToOtherValidator() {
	s.SetupTest()
	bk := newMockBankKeeper()
	s.setupWithMockBankKeeper(bk)

	mgr := testAccAddr(1)
	val1Acc := testAccAddr(10)
	val2Acc := testAccAddr(11)
	s.seedVoteManager(mgr)
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

	mgr := testAccAddr(1)
	valAcc := testAccAddr(10)
	random := testAccAddr(99)
	s.seedVoteManager(mgr)
	s.setupWithMockStaking(accToValoper(valAcc))

	_, err := s.msgServer.AuthorizedSend(s.ctx, &types.MsgAuthorizedSend{
		FromAddress: valAcc,
		ToAddress:   random,
		Amount:      "100",
		Denom:       "usvote",
	})
	s.Require().Error(err)
	s.Require().Contains(err.Error(), "can only send to the vote manager or another bonded validator")
	s.Require().Empty(bk.sendCalls)
}

// ---------------------------------------------------------------------------
// AuthorizedSend — unauthorized sender tests
// ---------------------------------------------------------------------------

func (s *MsgServerTestSuite) TestAuthorizedSend_NonPrivilegedSenderRejected() {
	s.SetupTest()
	bk := newMockBankKeeper()
	s.setupWithMockBankKeeper(bk)

	mgr := testAccAddr(1)
	random := testAccAddr(50)
	s.seedVoteManager(mgr)
	s.setupWithMockStaking() // no validators

	_, err := s.msgServer.AuthorizedSend(s.ctx, &types.MsgAuthorizedSend{
		FromAddress: random,
		ToAddress:   mgr,
		Amount:      "100",
		Denom:       "usvote",
	})
	s.Require().Error(err)
	s.Require().Contains(err.Error(), "neither the vote manager nor a bonded validator")
	s.Require().Empty(bk.sendCalls)
}

func (s *MsgServerTestSuite) TestAuthorizedSend_NoVoteManagerSet_ValidatorRejected() {
	s.SetupTest()
	bk := newMockBankKeeper()
	s.setupWithMockBankKeeper(bk)

	valAcc := testAccAddr(10)
	recipient := testAccAddr(20)
	// No vote manager seeded.
	s.setupWithMockStaking(accToValoper(valAcc))

	_, err := s.msgServer.AuthorizedSend(s.ctx, &types.MsgAuthorizedSend{
		FromAddress: valAcc,
		ToAddress:   recipient,
		Amount:      "100",
		Denom:       "usvote",
	})
	s.Require().Error(err)
	// Validator sending to non-validator, non-manager should fail.
	s.Require().Contains(err.Error(), "can only send to the vote manager or another bonded validator")
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
	s.seedVoteManager(testAccAddr(1))

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
	s.seedVoteManager(testAccAddr(1))

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
	s.seedVoteManager(testAccAddr(1))

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
	s.seedVoteManager(testAccAddr(1))

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
	s.seedVoteManager(testAccAddr(1))

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

	mgr := testAccAddr(1)
	recipient := testAccAddr(2)
	s.seedVoteManager(mgr)

	_, err := s.msgServer.AuthorizedSend(s.ctx, &types.MsgAuthorizedSend{
		FromAddress: mgr,
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
					s.Require().Equal(mgr, attr.Value)
				case types.AttributeKeyRecipient:
					s.Require().Equal(recipient, attr.Value)
				}
			}
		}
	}
	s.Require().True(found, "expected %s event", types.EventTypeAuthorizedSend)
}
