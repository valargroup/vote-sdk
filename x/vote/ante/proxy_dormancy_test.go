//go:build !proxyenabled

package ante_test

import (
	"bytes"

	errorsmod "cosmossdk.io/errors"

	svtest "github.com/valargroup/vote-sdk/testutil"
	"github.com/valargroup/vote-sdk/x/vote/ante"
	"github.com/valargroup/vote-sdk/x/vote/types"
)

// TestProxyDelegationMsgsDormant checks that the dormant gate is the first
// check of ValidateVoteTx for both proxy delegation Msgs
// (docs/proxy-delegation/contracts-v1.md §5.1 step 1, §9): well-formed or
// not, in every mode, they are ErrProxyDelegationDisabled with code 69.
func (s *ValidateTestSuite) TestProxyDelegationMsgsDormant() {
	s.setupActiveRound()
	batch := &types.MsgProxyDelegationBatch{
		VoteRoundId:              testRoundID,
		VoteCommTreeAnchorHeight: 10,
		Dcs: []*types.ProxyDelegationAction{{
			VanNullifier:              bytes.Repeat([]byte{0x11}, 32),
			RVpk:                      append([]byte(nil), svtest.DummyPallasPoint...),
			VoteAuthorityNoteNew:      bytes.Repeat([]byte{0x12}, 32),
			ProxyDelegationCommitment: bytes.Repeat([]byte{0x13}, 32),
			DcSlotNullifier:           bytes.Repeat([]byte{0x14}, 32),
			Proof:                     []byte("mock-zkp4-proof"),
			VoteAuthSig:               bytes.Repeat([]byte{0x15}, 64),
		}},
		ExpiryHeight: 100,
	}
	s.Require().NoError(batch.ValidateBasic())
	ballot := &types.MsgDelegateOp{Ballot: &types.DelegateBallot{
		VoteRoundId:   testRoundID,
		DelegateIndex: 1,
		Options:       []uint32{0, 1},
		DkSig:         bytes.Repeat([]byte{0x21}, 64),
	}}
	s.Require().NoError(ballot.ValidateBasic())

	msgs := map[string]types.VoteMessage{
		"well-formed batch":  batch,
		"malformed batch":    &types.MsgProxyDelegationBatch{},
		"well-formed ballot": ballot,
		"malformed op":       &types.MsgDelegateOp{},
	}
	modes := map[string]ante.ValidateOpts{
		"finalize": mockOpts(),
		"recheck":  recheckOpts(),
	}
	for msgName, msg := range msgs {
		for modeName, opts := range modes {
			s.Run(msgName+"/"+modeName, func() {
				err := ante.ValidateVoteTx(s.ctx, msg, s.keeper, opts)
				s.Require().ErrorIs(err, types.ErrProxyDelegationDisabled)
				codespace, code, _ := errorsmod.ABCIInfo(err, false)
				s.Require().Equal(types.ModuleName, codespace)
				s.Require().Equal(uint32(69), code)
			})
		}
	}
}
