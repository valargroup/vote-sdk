package ante_test

import (
	errorsmod "cosmossdk.io/errors"

	svtest "github.com/valargroup/vote-sdk/testutil"
	"github.com/valargroup/vote-sdk/x/vote/ante"
	"github.com/valargroup/vote-sdk/x/vote/types"
)

// TestSharedHelperCodesFollowTheGate checks the result codes of the shared
// vote helpers through the ante (docs/proxy-delegation/contracts-v1.md §0,
// §9): while proxy delegation is dormant, failed 0x02, 0x06 and 0x07
// transactions keep the codes they return today, mostly code 1 in codespace
// undefined; with the gate open (-tags proxyenabled) each reports its
// registered code. Both builds pin the same message.
func (s *ValidateTestSuite) TestSharedHelperCodesFollowTheGate() {
	castBatch := func() types.VoteMessage { return newValidMsgCastVoteBatch() }
	for _, c := range []struct {
		name  string
		setup func()
		msg   func() types.VoteMessage
		opts  ante.ValidateOpts
		want  *errorsmod.Error
		// text is the error message, the same in both builds.
		text string
		// dormantCode is today's code: 1 in codespace undefined, unless
		// the helper already returns the registered error bare.
		dormantCode uint32
	}{
		{name: "spent VAN nullifier", setup: func() {
			s.setupActiveRound()
			s.recordNullifier(types.NullifierTypeVoteAuthorityNote, testRoundID, newValidMsgCastVoteBatch().Votes[1].VanNullifier)
		}, msg: castBatch, opts: mockOpts(), want: types.ErrDuplicateNullifier,
			text: "nullifier already spent: 5f000000000000004a7c4a7fb979379e8c08a385886a3f24342aed8a6251e1b7", dormantCode: 1},
		{name: "round not found", msg: castBatch, opts: mockOpts(), want: types.ErrRoundNotFound,
			text: "vote round not found", dormantCode: 3},
		{name: "invalid cast proof", setup: s.setupActiveRound, msg: castBatch, opts: failZKPOpts(), want: types.ErrInvalidProof,
			text: "votes[0]: invalid zero-knowledge proof: vote commitment: mock: vote commitment proof verification failed", dormantCode: 1},
		{name: "invalid cast field", setup: s.setupActiveRound, msg: func() types.VoteMessage {
			msg := newValidMsgCastVoteBatch()
			msg.Votes[0].VanNullifier = nil
			return msg
		}, opts: mockOpts(), want: types.ErrInvalidField,
			text: "basic validation failed: votes[0]: invalid message field: van_nullifier must be 32 bytes, got 0", dormantCode: 1},
	} {
		s.Run(c.name, func() {
			s.SetupTest()
			if c.setup != nil {
				c.setup()
			}

			err := ante.ValidateVoteTx(s.ctx, c.msg(), s.keeper, c.opts)
			s.Require().ErrorIs(err, c.want)
			s.Require().EqualError(err, c.text)
			codespace, code, _ := errorsmod.ABCIInfo(err, false)
			if types.ProxyDelegationEnabled {
				s.Require().Equal(types.ModuleName, codespace, "enabled: %v", err)
				s.Require().Equal(c.want.ABCICode(), code, "enabled: %v", err)
				return
			}
			s.Require().Equal(c.dormantCode, code, "dormant: %v", err)
			if c.dormantCode == 1 {
				s.Require().Equal(errorsmod.UndefinedCodespace, codespace)
			}
		})
	}
}

// TestSharedHelperCodesInRecheck checks the gate on the paths RecheckTx
// keeps: the round and nullifier checks.
func (s *ValidateTestSuite) TestSharedHelperCodesInRecheck() {
	s.setupActiveRound()
	msg := svtest.ValidCastVote(testRoundID, 10, 0x61)
	s.recordNullifier(types.NullifierTypeVoteAuthorityNote, testRoundID, msg.VanNullifier)

	_, code, _ := errorsmod.ABCIInfo(ante.ValidateVoteTx(s.ctx.WithIsReCheckTx(true), msg, s.keeper, recheckOpts()), false)
	if types.ProxyDelegationEnabled {
		s.Require().Equal(types.ErrDuplicateNullifier.ABCICode(), code)
	} else {
		s.Require().Equal(uint32(1), code)
	}
}
