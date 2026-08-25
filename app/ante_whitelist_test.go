package app_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	sdkmath "cosmossdk.io/math"
	upgradetypes "cosmossdk.io/x/upgrade/types"

	sdk "github.com/cosmos/cosmos-sdk/types"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	distrtypes "github.com/cosmos/cosmos-sdk/x/distribution/types"
	slashingtypes "github.com/cosmos/cosmos-sdk/x/slashing/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
	"google.golang.org/protobuf/types/known/anypb"

	"github.com/valargroup/vote-sdk/app"
	"github.com/valargroup/vote-sdk/testutil"
	votetypes "github.com/valargroup/vote-sdk/x/vote/types"
)

// ---------------------------------------------------------------------------
// MessageWhitelistDecorator — blocked messages
// ---------------------------------------------------------------------------

func TestWhitelist_BankMsgSendBlocked(t *testing.T) {
	ta := testutil.SetupTestApp(t)

	msg := &banktypes.MsgSend{
		FromAddress: "sv1sender",
		ToAddress:   "sv1receiver",
		Amount:      sdk.NewCoins(sdk.NewCoin("usvote", sdkmath.NewInt(100))),
	}

	txBytes := buildUnsignedTx(t, ta, msg)

	resp := ta.CheckTxSync(txBytes)
	require.NotEqual(t, uint32(0), resp.Code)
	require.Contains(t, resp.Log, "is not allowed on this chain")
	require.Contains(t, resp.Log, "MsgSend")
}

func TestWhitelist_BankMsgMultiSendBlocked(t *testing.T) {
	ta := testutil.SetupTestApp(t)

	msg := &banktypes.MsgMultiSend{
		Inputs:  []banktypes.Input{{Address: "sv1sender", Coins: sdk.NewCoins(sdk.NewCoin("usvote", sdkmath.NewInt(100)))}},
		Outputs: []banktypes.Output{{Address: "sv1receiver", Coins: sdk.NewCoins(sdk.NewCoin("usvote", sdkmath.NewInt(100)))}},
	}

	txBytes := buildUnsignedTx(t, ta, msg)

	resp := ta.CheckTxSync(txBytes)
	require.NotEqual(t, uint32(0), resp.Code)
	require.Contains(t, resp.Log, "is not allowed on this chain")
	require.Contains(t, resp.Log, "MsgMultiSend")
}

func TestWhitelist_VoteMsgAuthorizedSendBlocked(t *testing.T) {
	ta := testutil.SetupTestApp(t)

	signerAddr := sdk.AccAddress(ta.ValPrivKey.PubKey().Address())
	msg := &votetypes.MsgAuthorizedSend{
		Creator:   signerAddr.String(),
		ToAddress: signerAddr.String(),
		Amount:    "100",
	}

	txBytes := buildSignedTxWithKey(t, ta, ta.ValPrivKey, 0, 0, msg)

	resp := ta.CheckTxSync(txBytes)
	require.NotEqual(t, uint32(0), resp.Code)
	require.Contains(t, resp.Log, "no message handler found")
	require.Contains(t, resp.Log, "MsgAuthorizedSend")
}

func TestWhitelist_DistributionMsgFundCommunityPoolBlocked(t *testing.T) {
	ta := testutil.SetupTestApp(t)

	signerAddr := sdk.AccAddress(ta.ValPrivKey.PubKey().Address())
	msg := &distrtypes.MsgFundCommunityPool{
		Depositor: signerAddr.String(),
		Amount:    sdk.NewCoins(sdk.NewCoin("usvote", sdkmath.NewInt(100))),
	}

	txBytes := buildUnsignedTx(t, ta, msg)

	resp := ta.CheckTxSync(txBytes)
	require.NotEqual(t, uint32(0), resp.Code)
	require.Contains(t, resp.Log, "unable to resolve type URL")
	require.Contains(t, resp.Log, "MsgFundCommunityPool")
}

func TestWhitelist_StakingMsgDelegateBlocked(t *testing.T) {
	ta := testutil.SetupTestApp(t)

	signerAddr := sdk.AccAddress(ta.ValPrivKey.PubKey().Address())
	msg := &stakingtypes.MsgDelegate{
		DelegatorAddress: signerAddr.String(),
		ValidatorAddress: sdk.ValAddress(signerAddr).String(),
		Amount:           sdk.NewCoin("usvote", sdkmath.NewInt(100)),
	}

	txBytes := buildUnsignedTx(t, ta, msg)

	resp := ta.CheckTxSync(txBytes)
	require.NotEqual(t, uint32(0), resp.Code)
	require.Contains(t, resp.Log, "is not allowed on this chain")
	require.Contains(t, resp.Log, "MsgDelegate")
}

func TestWhitelist_DistributionMsgSetWithdrawAddressBlocked(t *testing.T) {
	ta := testutil.SetupTestApp(t)

	signerAddr := sdk.AccAddress(ta.ValPrivKey.PubKey().Address())
	msg := &distrtypes.MsgSetWithdrawAddress{
		DelegatorAddress: signerAddr.String(),
		WithdrawAddress:  signerAddr.String(),
	}

	txBytes := buildUnsignedTx(t, ta, msg)

	resp := ta.CheckTxSync(txBytes)
	require.NotEqual(t, uint32(0), resp.Code)
	require.Contains(t, resp.Log, "unable to resolve type URL")
	require.Contains(t, resp.Log, "MsgSetWithdrawAddress")
}

// ---------------------------------------------------------------------------
// MessageWhitelistDecorator — allowed messages pass through
// ---------------------------------------------------------------------------

func TestWhitelist_AllowedMessagesPassThrough(t *testing.T) {
	ta := testutil.SetupTestApp(t)
	signerAddr := sdk.AccAddress(ta.ValPrivKey.PubKey().Address())

	tests := []struct {
		name string
		msg  sdk.Msg
	}{
		{
			name: "MsgRegisterPallasKey",
			msg: &votetypes.MsgRegisterPallasKey{
				Creator:  signerAddr.String(),
				PallasPk: make([]byte, 32),
			},
		},
		{
			name: "MsgRotatePallasKey",
			msg: &votetypes.MsgRotatePallasKey{
				Creator:     signerAddr.String(),
				NewPallasPk: make([]byte, 32),
			},
		},
		{
			name: "MsgUnjail",
			msg: &slashingtypes.MsgUnjail{
				ValidatorAddr: sdk.ValAddress(signerAddr).String(),
			},
		},
		{
			name: "MsgProposeCoordinatorAction",
			msg: &votetypes.MsgProposeCoordinatorAction{
				Creator: signerAddr.String(),
				Payload: &anypb.Any{TypeUrl: "/svote.v1.MsgCancelUpgrade", Value: []byte{0x01}},
			},
		},
		{
			name: "MsgApproveCoordinatorAction",
			msg: &votetypes.MsgApproveCoordinatorAction{
				Creator:  signerAddr.String(),
				ActionId: 1,
			},
		},
		{
			name: "MsgEndorseRound",
			msg: &votetypes.MsgEndorseRound{
				Creator:     signerAddr.String(),
				EndorserId:  "zodl",
				VoteRoundId: make([]byte, votetypes.RoundIDLen),
			},
		},
		{
			name: "MsgClearRoundEndorsement",
			msg: &votetypes.MsgClearRoundEndorsement{
				Creator:     signerAddr.String(),
				EndorserId:  "zodl",
				VoteRoundId: make([]byte, votetypes.RoundIDLen),
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			txBytes := buildSignedTxWithKey(t, ta, ta.ValPrivKey, 0, 0, tc.msg)
			resp := ta.CheckTxSync(txBytes)
			t.Logf("CheckTx code=%d log=%q", resp.Code, resp.Log)
			require.NotContains(t, resp.Log, "is not allowed on this chain",
				"%s should pass the whitelist", tc.name)
		})
	}
}

func TestWhitelist_RawUpgradeMsgsBlocked(t *testing.T) {
	ta := testutil.SetupTestApp(t)

	tests := []struct {
		name string
		msg  sdk.Msg
	}{
		{
			name: "MsgSoftwareUpgrade",
			msg: &upgradetypes.MsgSoftwareUpgrade{
				Authority: "sv1rawupgradeauthority",
				Plan: upgradetypes.Plan{
					Name:   "raw-upgrade",
					Height: 100,
				},
			},
		},
		{
			name: "MsgCancelUpgrade",
			msg: &upgradetypes.MsgCancelUpgrade{
				Authority: "sv1rawupgradeauthority",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			txBytes := buildUnsignedTx(t, ta, tc.msg)
			resp := ta.CheckTxSync(txBytes)
			require.NotEqual(t, uint32(0), resp.Code)
			require.Contains(t, resp.Log, "is not allowed on this chain")
			require.Contains(t, resp.Log, tc.name)
		})
	}
}

// ---------------------------------------------------------------------------
// DefaultAllowedMessages covers expected types
// ---------------------------------------------------------------------------

func TestDefaultAllowedMessages_ContainsExpectedTypes(t *testing.T) {
	msgs := app.DefaultAllowedMessages()
	allowed := make(map[string]bool, len(msgs))
	for _, m := range msgs {
		allowed[m] = true
	}

	// Should be present
	require.True(t, allowed["/svote.v1.MsgProposeCoordinatorAction"])
	require.True(t, allowed["/svote.v1.MsgApproveCoordinatorAction"])
	require.True(t, allowed["/svote.v1.MsgRegisterPallasKey"])
	require.True(t, allowed["/svote.v1.MsgRotatePallasKey"])
	require.True(t, allowed["/svote.v1.MsgCreateValidatorWithPallasKey"])
	require.True(t, allowed["/svote.v1.MsgEndorseRound"])
	require.True(t, allowed["/svote.v1.MsgClearRoundEndorsement"])
	require.True(t, allowed["/cosmos.staking.v1beta1.MsgCreateValidator"])
	require.True(t, allowed["/cosmos.staking.v1beta1.MsgEditValidator"])
	require.True(t, allowed["/cosmos.slashing.v1beta1.MsgUnjail"])

	// Should NOT be present
	require.False(t, allowed["/cosmos.bank.v1beta1.MsgSend"])
	require.False(t, allowed["/cosmos.bank.v1beta1.MsgMultiSend"])
	require.False(t, allowed["/cosmos.staking.v1beta1.MsgDelegate"])
	require.False(t, allowed["/cosmos.staking.v1beta1.MsgUndelegate"])
	require.False(t, allowed["/cosmos.staking.v1beta1.MsgBeginRedelegate"])
	require.False(t, allowed["/cosmos.distribution.v1beta1.MsgWithdrawDelegatorReward"])
	require.False(t, allowed["/cosmos.distribution.v1beta1.MsgWithdrawValidatorCommission"])
	require.False(t, allowed["/svote.v1.MsgDelegateVote"])
	require.False(t, allowed["/svote.v1.MsgCastVote"])
	require.False(t, allowed["/svote.v1.MsgCastVoteBatch"])
	require.False(t, allowed["/svote.v1.MsgRevealShare"])
	require.False(t, allowed["/svote.v1.MsgCreateVotingSession"])
	require.False(t, allowed["/svote.v1.MsgUpdateVoteManagers"])
	require.False(t, allowed["/svote.v1.MsgAuthorizedSend"])
	require.False(t, allowed["/svote.v1.MsgScheduleUpgrade"])
	require.False(t, allowed["/svote.v1.MsgCancelUpgrade"])
	require.False(t, allowed["/svote.v1.MsgSetEndorser"])
	require.False(t, allowed["/cosmos.upgrade.v1beta1.MsgSoftwareUpgrade"])
	require.False(t, allowed["/cosmos.upgrade.v1beta1.MsgCancelUpgrade"])
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func buildUnsignedTx(t *testing.T, ta *testutil.TestApp, msgs ...sdk.Msg) []byte {
	t.Helper()
	txConfig := ta.TxConfig()
	txBuilder := txConfig.NewTxBuilder()
	require.NoError(t, txBuilder.SetMsgs(msgs...))
	txBuilder.SetGasLimit(200_000)
	txBuilder.SetFeeAmount(sdk.NewCoins())
	txBytes, err := txConfig.TxEncoder()(txBuilder.GetTx())
	require.NoError(t, err)
	return txBytes
}
