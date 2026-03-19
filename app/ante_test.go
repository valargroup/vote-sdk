package app_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	sdkmath "cosmossdk.io/math"

	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/tx/signing"
	authsigning "github.com/cosmos/cosmos-sdk/x/auth/signing"
	"github.com/cosmos/cosmos-sdk/x/authz"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	distrtypes "github.com/cosmos/cosmos-sdk/x/distribution/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"

	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"

	"github.com/valargroup/vote-sdk/crypto/elgamal"
	"github.com/valargroup/vote-sdk/testutil"
	votetypes "github.com/valargroup/vote-sdk/x/vote/types"
)

// ---------------------------------------------------------------------------
// MsgCreateValidator ante handler blocking tests
// ---------------------------------------------------------------------------

// TestMsgCreateValidatorBlockedPostGenesis verifies that MsgCreateValidator
// is rejected by CheckTx at block heights > 0 (post-genesis).
func TestMsgCreateValidatorBlockedPostGenesis(t *testing.T) {
	app := testutil.SetupTestApp(t)

	// Build a standard Cosmos tx containing MsgCreateValidator.
	// The tx doesn't need to be validly signed — the ante handler's
	// MsgCreateValidator check fires before signature verification.
	msgCreateVal := &stakingtypes.MsgCreateValidator{
		Description:       stakingtypes.Description{Moniker: "test-validator"},
		Commission:        stakingtypes.CommissionRates{Rate: sdkmath.LegacyNewDecWithPrec(1, 1), MaxRate: sdkmath.LegacyNewDecWithPrec(2, 1), MaxChangeRate: sdkmath.LegacyNewDecWithPrec(1, 2)},
		MinSelfDelegation: sdkmath.NewInt(1),
		ValidatorAddress:  "svvaloper1deadbeef",
		Pubkey:            nil,
		Value:             sdk.NewCoin(sdk.DefaultBondDenom, sdkmath.NewInt(10_000_000)),
	}

	txConfig := app.TxConfig()
	txBuilder := txConfig.NewTxBuilder()
	err := txBuilder.SetMsgs(msgCreateVal)
	require.NoError(t, err)
	txBuilder.SetGasLimit(200_000)

	txBuilder.SetFeeAmount(sdk.NewCoins())

	// Encode the unsigned tx. The ante handler's MsgCreateValidator check
	// fires before signature verification, so a valid signature is not needed.
	txBytes, err := txConfig.TxEncoder()(txBuilder.GetTx())
	require.NoError(t, err)

	// The app is at height > 0, so MsgCreateValidator should be blocked.
	require.Greater(t, app.Height, int64(0), "test app should be past genesis")

	resp := app.CheckTxSync(txBytes)
	require.NotEqual(t, uint32(0), resp.Code,
		"MsgCreateValidator should be rejected post-genesis")
	require.Contains(t, resp.Log, "MsgCreateValidator is disabled")

	// Also verify it's blocked via FinalizeBlock (DeliverTx path).
	result := app.DeliverVoteTx(txBytes)
	require.NotEqual(t, uint32(0), result.Code,
		"MsgCreateValidator should be rejected in DeliverTx post-genesis")
	require.Contains(t, result.Log, "MsgCreateValidator is disabled")
}

// TestGenesisValidatorCreationSucceeds verifies that the genesis flow
// (which uses standard MsgCreateValidator via gentx) succeeds. This is
// implicitly tested by SetupTestApp — if the genesis MsgCreateValidator
// were blocked, InitChain would panic.
func TestGenesisValidatorCreationSucceeds(t *testing.T) {
	// SetupTestApp calls InitChain with GenesisStateWithValSet, which
	// includes a gentx containing MsgCreateValidator. If our ante handler
	// blocked it at genesis height, this would panic.
	app := testutil.SetupTestApp(t)

	// Verify the genesis validator was actually created.
	valAddr := app.ValidatorOperAddr()
	require.NotEmpty(t, valAddr, "genesis validator should exist")
}

// ---------------------------------------------------------------------------
// Bank MsgSend / MsgMultiSend ante handler blocking tests
// ---------------------------------------------------------------------------

func TestBankMsgSendBlocked(t *testing.T) {
	app := testutil.SetupTestApp(t)

	msg := &banktypes.MsgSend{
		FromAddress: "sv1sender",
		ToAddress:   "sv1receiver",
		Amount:      sdk.NewCoins(sdk.NewCoin("usvote", sdkmath.NewInt(100))),
	}

	txConfig := app.TxConfig()
	txBuilder := txConfig.NewTxBuilder()
	require.NoError(t, txBuilder.SetMsgs(msg))
	txBuilder.SetGasLimit(200_000)
	txBuilder.SetFeeAmount(sdk.NewCoins())

	txBytes, err := txConfig.TxEncoder()(txBuilder.GetTx())
	require.NoError(t, err)

	resp := app.CheckTxSync(txBytes)
	require.NotEqual(t, uint32(0), resp.Code, "MsgSend should be rejected")
	require.Contains(t, resp.Log, "is not allowed on this chain")

	result := app.DeliverVoteTx(txBytes)
	require.NotEqual(t, uint32(0), result.Code, "MsgSend should be rejected in DeliverTx")
	require.Contains(t, result.Log, "is not allowed on this chain")
}

func TestBankMsgMultiSendBlocked(t *testing.T) {
	app := testutil.SetupTestApp(t)

	msg := &banktypes.MsgMultiSend{
		Inputs:  []banktypes.Input{{Address: "sv1sender", Coins: sdk.NewCoins(sdk.NewCoin("usvote", sdkmath.NewInt(100)))}},
		Outputs: []banktypes.Output{{Address: "sv1receiver", Coins: sdk.NewCoins(sdk.NewCoin("usvote", sdkmath.NewInt(100)))}},
	}

	txConfig := app.TxConfig()
	txBuilder := txConfig.NewTxBuilder()
	require.NoError(t, txBuilder.SetMsgs(msg))
	txBuilder.SetGasLimit(200_000)
	txBuilder.SetFeeAmount(sdk.NewCoins())

	txBytes, err := txConfig.TxEncoder()(txBuilder.GetTx())
	require.NoError(t, err)

	resp := app.CheckTxSync(txBytes)
	require.NotEqual(t, uint32(0), resp.Code, "MsgMultiSend should be rejected")
	require.Contains(t, resp.Log, "is not allowed on this chain")

	result := app.DeliverVoteTx(txBytes)
	require.NotEqual(t, uint32(0), result.Code, "MsgMultiSend should be rejected in DeliverTx")
	require.Contains(t, result.Log, "is not allowed on this chain")
}

// ---------------------------------------------------------------------------
// Table-driven: vote/ceremony messages blocked in standard Cosmos txs
// ---------------------------------------------------------------------------

// TestDualAnteHandler_StandardTxRestrictions is a table-driven test that
// verifies the two structural restrictions on standard Cosmos txs:
//  1. Multi-message txs are rejected (eliminates the noop-signer attack class).
//  2. Vote/ceremony messages are rejected even in single-message txs
//     (defense-in-depth: isVoteModuleMsg fires first, MessageWhitelistDecorator backs it up).
//  3. Allowed single-message txs pass through to the standard ante chain.
func TestDualAnteHandler_StandardTxRestrictions(t *testing.T) {
	ta := testutil.SetupTestApp(t)
	roundID := ta.SeedVotingSession(testutil.ValidCreateVotingSession())

	delMsg := testutil.ValidDelegation(roundID, 0x70)
	delTx := testutil.MustEncodeVoteTx(delMsg)
	delResult := ta.DeliverVoteTx(delTx)
	require.Equal(t, uint32(0), delResult.Code, "setup delegation should succeed")
	anchorHeight := uint64(ta.Height)

	signerAddr := sdk.AccAddress(ta.ValPrivKey.PubKey().Address())
	carrier := &banktypes.MsgSend{
		FromAddress: signerAddr.String(),
		ToAddress:   signerAddr.String(),
		Amount:      sdk.NewCoins(sdk.NewInt64Coin(sdk.DefaultBondDenom, 1)),
	}

	t.Run("multi-msg rejected", func(t *testing.T) {
		tests := []struct {
			name string
			msgs []sdk.Msg
		}{
			{
				name: "MsgSend+MsgSend",
				msgs: []sdk.Msg{carrier, carrier},
			},
			{
				name: "MsgSend+MsgDelegateVote",
				msgs: []sdk.Msg{carrier, &votetypes.MsgDelegateVote{
					Rk: bytes.Repeat([]byte{0xAA}, 32), SpendAuthSig: bytes.Repeat([]byte{0xBB}, 64),
					SignedNoteNullifier: bytes.Repeat([]byte{0xCC}, 32),
					CmxNew: testutil.FpLE(0xBEEF), VanCmx: testutil.FpLE(0xDEAD),
					GovNullifiers: [][]byte{bytes.Repeat([]byte{0xFF}, 32)},
					Proof: []byte{0x42}, VoteRoundId: roundID,
					Sighash: bytes.Repeat([]byte{0x99}, 32),
				}},
			},
			{
				name: "MsgSend+MsgRevealShare",
				msgs: []sdk.Msg{carrier, &votetypes.MsgRevealShare{
					ShareNullifier: bytes.Repeat([]byte{0xE1}, 32),
					EncShare: elgamal.IdentityCiphertextBytes(),
					ProposalId: 1, VoteDecision: 1, Proof: []byte{0x42},
					VoteRoundId: roundID, VoteCommTreeAnchorHeight: anchorHeight,
				}},
			},
			{
				name: "MsgSend+MsgDealExecutiveAuthorityKey",
				msgs: []sdk.Msg{carrier, &votetypes.MsgDealExecutiveAuthorityKey{
					Creator: signerAddr.String(), EaPk: bytes.Repeat([]byte{0xAA}, 32),
					Threshold: 2,
				}},
			},
		}

		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				txBytes := mustBuildMultiMsgSignedTx(t, ta, tc.msgs...)
				checkResp := ta.CheckTxSync(txBytes)
				t.Logf("CheckTx code=%d log=%q", checkResp.Code, checkResp.Log)
				require.NotEqual(t, uint32(0), checkResp.Code)
				require.Contains(t, checkResp.Log, "multi-message transactions are not supported")
			})
		}
	})

	t.Run("single-msg vote/ceremony blocked", func(t *testing.T) {
		tests := []struct {
			name string
			msg  sdk.Msg
		}{
			{
				name: "MsgDelegateVote",
				msg: &votetypes.MsgDelegateVote{
					Rk: bytes.Repeat([]byte{0xAA}, 32), SpendAuthSig: bytes.Repeat([]byte{0xBB}, 64),
					SignedNoteNullifier: bytes.Repeat([]byte{0xCC}, 32),
					CmxNew: testutil.FpLE(0xBEEF), VanCmx: testutil.FpLE(0xDEAD),
					GovNullifiers: [][]byte{bytes.Repeat([]byte{0xFF}, 32)},
					Proof: []byte{0x42}, VoteRoundId: roundID,
					Sighash: bytes.Repeat([]byte{0x99}, 32),
				},
			},
			{
				name: "MsgCastVote",
				msg: &votetypes.MsgCastVote{
					VanNullifier: bytes.Repeat([]byte{0xD1}, 32),
					VoteAuthorityNoteNew: testutil.FpLE(0xA1A1), VoteCommitment: testutil.FpLE(0xB2B2),
					ProposalId: 1, Proof: []byte{0x42}, VoteRoundId: roundID,
					VoteCommTreeAnchorHeight: anchorHeight,
					VoteAuthSig: bytes.Repeat([]byte{0xC3}, 64), RVpk: bytes.Repeat([]byte{0xE4}, 32),
				},
			},
			{
				name: "MsgRevealShare",
				msg: &votetypes.MsgRevealShare{
					ShareNullifier: bytes.Repeat([]byte{0xE1}, 32),
					EncShare: elgamal.IdentityCiphertextBytes(),
					ProposalId: 1, VoteDecision: 1, Proof: []byte{0x42},
					VoteRoundId: roundID, VoteCommTreeAnchorHeight: anchorHeight,
				},
			},
			{
				name: "MsgDealExecutiveAuthorityKey",
				msg: &votetypes.MsgDealExecutiveAuthorityKey{
					Creator: signerAddr.String(), EaPk: bytes.Repeat([]byte{0xAA}, 32),
					Threshold: 2,
				},
			},
			{
				name: "MsgAckExecutiveAuthorityKey",
				msg: &votetypes.MsgAckExecutiveAuthorityKey{
					Creator: signerAddr.String(), AckSignature: bytes.Repeat([]byte{0xBB}, 32),
					VoteRoundId: bytes.Repeat([]byte{0xCC}, 32),
				},
			},
			{
				name: "MsgSubmitPartialDecryption",
				msg: &votetypes.MsgSubmitPartialDecryption{
					VoteRoundId: bytes.Repeat([]byte{0xDD}, 32), Creator: signerAddr.String(),
					ValidatorIndex: 1,
				},
			},
			{
				name: "MsgSubmitTally",
				msg: &votetypes.MsgSubmitTally{
					VoteRoundId: roundID, Creator: signerAddr.String(),
					Entries: []*votetypes.TallyEntry{{ProposalId: 1, VoteDecision: 1, TotalValue: 0}},
				},
			},
		}

		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				txBytes := buildSignedTxWithKey(t, ta, ta.ValPrivKey, 0, 0, tc.msg)
				checkResp := ta.CheckTxSync(txBytes)
				t.Logf("CheckTx code=%d log=%q", checkResp.Code, checkResp.Log)
				require.NotEqual(t, uint32(0), checkResp.Code)
				require.Contains(t, checkResp.Log, "not allowed in standard Cosmos transactions")
			})
		}
	})

	t.Run("single-msg allowed", func(t *testing.T) {
		tests := []struct {
			name string
			msg  sdk.Msg
		}{
			{
				name: "MsgRegisterPallasKey",
				msg: &votetypes.MsgRegisterPallasKey{
					Creator: signerAddr.String(), PallasPk: bytes.Repeat([]byte{0x01}, 32),
				},
			},
		}

		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				txBytes := buildSignedTxWithKey(t, ta, ta.ValPrivKey, 0, 0, tc.msg)
				checkResp := ta.CheckTxSync(txBytes)
				t.Logf("CheckTx code=%d log=%q", checkResp.Code, checkResp.Log)
				require.NotContains(t, checkResp.Log, "not allowed in standard Cosmos transactions")
				require.NotContains(t, checkResp.Log, "multi-message transactions are not supported")
				require.NotContains(t, checkResp.Log, "is disabled")
			})
		}
	})
}

// ---------------------------------------------------------------------------
// Noop-signer bypass: multi-message standard Tx with vote module messages
// ---------------------------------------------------------------------------

// TestNoopSignerBypass_DelegateVote demonstrates that a MsgDelegateVote could
// previously be piggybacked in a standard Cosmos SDK multi-message transaction alongside a
// legitimately-signed MsgSend. Because MsgDelegateVote has no
// cosmos.msg.v1.signer proto annotation, GetSigners() returns [] for it.
// The union of signers across both messages is just the MsgSend signer (len=1),
// and with 1 valid signature the ante chain's sig checks pass. The
// MsgDelegateVote handler then executes without any ZKP or RedPallas
// verification, consuming nullifiers and polluting the commitment tree.
//
// Attack path:
//
//	TxRaw first byte = 0x0A (protobuf) → CustomTxDecoder → standardDecoder
//	DualAnteHandler: tx is NOT *VoteTxWrapper → standardHandler
//	Tx.ValidateBasic: len(sigs)=1 > 0 → passes
//	SigVerifyDecorator: len(sigs)==len(signers) → 1==1 → passes
//	MsgServiceRouter dispatches both messages to handlers
//	DelegateVote() runs WITHOUT ZKP/RedPallas → nullifier consumed, van_cmx appended
//
// This is now fixed.
func TestNoopSignerBypass_DelegateVote(t *testing.T) {
	ta := testutil.SetupTestApp(t)

	// Seed an ACTIVE voting round so the DelegateVote handler doesn't
	// reject with "round not found".
	roundID := ta.SeedVotingSession(testutil.ValidCreateVotingSession())

	// Build the exploit payload: a MsgDelegateVote with completely fake
	// cryptographic material. In the normal vote path this would be rejected
	// by the ZKP verifier and RedPallas sig check, but via the standard
	// Cosmos tx path those checks never run.
	fakeGovNullifier := bytes.Repeat([]byte{0xFF}, 32)
	fakeVanCmx := testutil.FpLE(0xDEAD)
	voteMsg := &votetypes.MsgDelegateVote{
		Rk:                  bytes.Repeat([]byte{0xAA}, 32),
		SpendAuthSig:        bytes.Repeat([]byte{0xBB}, 64),
		SignedNoteNullifier: bytes.Repeat([]byte{0xCC}, 32),
		CmxNew:              testutil.FpLE(0xBEEF),
		VanCmx:              fakeVanCmx,
		GovNullifiers:       [][]byte{fakeGovNullifier},
		Proof:               []byte{0x42},
		VoteRoundId:         roundID,
		Sighash:             bytes.Repeat([]byte{0x99}, 32),
	}
	require.NoError(t, voteMsg.ValidateBasic(), "exploit payload must pass ValidateBasic")

	// Build the carrier: a self-transfer MsgSend that provides a real signer.
	signerAddr := sdk.AccAddress(ta.ValPrivKey.PubKey().Address())
	sendMsg := &banktypes.MsgSend{
		FromAddress: signerAddr.String(),
		ToAddress:   signerAddr.String(),
		Amount:      sdk.NewCoins(sdk.NewInt64Coin(sdk.DefaultBondDenom, 1)),
	}

	// Build a multi-message standard Cosmos SDK tx: [MsgSend, MsgDelegateVote].
	// MsgSend contributes 1 signer; MsgDelegateVote contributes 0 (no signer annotation).
	// Union = 1 signer → 1 signature needed → all ante checks pass.
	txBytes := mustBuildMultiMsgSignedTx(t, ta, sendMsg, voteMsg)

	// --- CheckTx: the tx should be accepted into the mempool ---
	checkResp := ta.CheckTxSync(txBytes)

	// --- FinalizeBlock (DeliverTx): the tx should be included in a block ---
	result := ta.DeliverVoteTx(txBytes)

	// If the exploit succeeds, BOTH CheckTx and DeliverTx return code 0.
	// The DelegateVote handler runs without ZKP verification:
	//   - fakeGovNullifier is consumed in the nullifier set
	//   - fakeVanCmx is appended to the commitment tree
	t.Logf("CheckTx  code=%d log=%q", checkResp.Code, checkResp.Log)
	t.Logf("DeliverTx code=%d log=%q", result.Code, result.Log)

	if checkResp.Code == 0 && result.Code == 0 {
		t.Fatal("VULNERABILITY CONFIRMED: MsgDelegateVote executed via standard Cosmos " +
			"tx path without ZKP or RedPallas verification. Fake nullifiers consumed " +
			"and fake commitment appended to tree.")
	}

	// If we reach here, the exploit was blocked — verify the rejection reason.
	t.Logf("Exploit was blocked (good). CheckTx code=%d, DeliverTx code=%d", checkResp.Code, result.Code)
}

// TestNoopSignerBypass_CastVote is the MsgCastVote variant of the noop-signer
// bypass. Same vector: multi-message tx [MsgSend, MsgCastVote] where MsgCastVote
// contributes 0 signers.
//
// See description in TestNoopSignerBypass_DelegateVote for details.
func TestNoopSignerBypass_CastVote(t *testing.T) {
	ta := testutil.SetupTestApp(t)

	roundID := ta.SeedVotingSession(testutil.ValidCreateVotingSession())

	// Deliver a legitimate delegation first so the commitment tree has a root
	// at a known height (required by CastVote's anchor height validation).
	delMsg := testutil.ValidDelegation(roundID, 0x10)
	delTx := testutil.MustEncodeVoteTx(delMsg)
	delResult := ta.DeliverVoteTx(delTx)
	require.Equal(t, uint32(0), delResult.Code, "setup delegation should succeed")

	anchorHeight := uint64(ta.Height)

	// Build the exploit CastVote with fake ZKP material.
	castMsg := &votetypes.MsgCastVote{
		VanNullifier:             bytes.Repeat([]byte{0xD1}, 32),
		VoteAuthorityNoteNew:     testutil.FpLE(0xA1A1),
		VoteCommitment:           testutil.FpLE(0xB2B2),
		ProposalId:               1,
		Proof:                    []byte{0x42},
		VoteRoundId:              roundID,
		VoteCommTreeAnchorHeight: anchorHeight,
		VoteAuthSig:              bytes.Repeat([]byte{0xC3}, 64),
		RVpk:                     bytes.Repeat([]byte{0xE4}, 32),
	}
	require.NoError(t, castMsg.ValidateBasic())

	signerAddr := sdk.AccAddress(ta.ValPrivKey.PubKey().Address())
	sendMsg := &banktypes.MsgSend{
		FromAddress: signerAddr.String(),
		ToAddress:   signerAddr.String(),
		Amount:      sdk.NewCoins(sdk.NewInt64Coin(sdk.DefaultBondDenom, 1)),
	}

	txBytes := mustBuildMultiMsgSignedTx(t, ta, sendMsg, castMsg)
	result := ta.DeliverVoteTx(txBytes)

	t.Logf("DeliverTx code=%d log=%q", result.Code, result.Log)
	if result.Code == 0 {
		t.Fatal("VULNERABILITY CONFIRMED: MsgCastVote executed via standard Cosmos " +
			"tx path without ZKP or RedPallas verification.")
	}
}

// TestNoopSignerBypass_RevealShare is the MsgRevealShare variant of the noop-signer
// bypass. Same vector: multi-message tx [MsgSend, MsgRevealShare] where MsgRevealShare
// contributes 0 signers. MsgRevealShare accepts both ACTIVE and TALLYING rounds, so
// this uses the same ACTIVE-round setup as the other tests.
//
// Unlike MsgDelegateVote/MsgCastVote (which require RedPallas + ZKP), MsgRevealShare
// only requires ZKP #3. If the bypass succeeds, a fake enc_share is recorded for the
// proposal, corrupting the tally accumulator.
//
// See description in TestNoopSignerBypass_DelegateVote for details.
// This is now fixed.
func TestNoopSignerBypass_RevealShare(t *testing.T) {
	ta := testutil.SetupTestApp(t)

	roundID := ta.SeedVotingSession(testutil.ValidCreateVotingSession())

	// Deliver a legitimate delegation so the commitment tree has a root
	// at a known height (required by RevealShare's anchor height validation).
	delMsg := testutil.ValidDelegation(roundID, 0x20)
	delTx := testutil.MustEncodeVoteTx(delMsg)
	delResult := ta.DeliverVoteTx(delTx)
	require.Equal(t, uint32(0), delResult.Code, "setup delegation should succeed")

	anchorHeight := uint64(ta.Height)

	// Build the exploit RevealShare with fake ZKP material but a valid
	// ElGamal ciphertext. Using a valid ciphertext (identity = Enc(0))
	// ensures the handler's enc_share deserialization succeeds, proving
	// the bypass reaches all the way through to state mutation.
	revealMsg := &votetypes.MsgRevealShare{
		ShareNullifier:           bytes.Repeat([]byte{0xE1}, 32),
		EncShare:                 elgamal.IdentityCiphertextBytes(),
		ProposalId:               1,
		VoteDecision:             1,
		Proof:                    []byte{0x42},
		VoteRoundId:              roundID,
		VoteCommTreeAnchorHeight: anchorHeight,
	}
	require.NoError(t, revealMsg.ValidateBasic())

	signerAddr := sdk.AccAddress(ta.ValPrivKey.PubKey().Address())
	sendMsg := &banktypes.MsgSend{
		FromAddress: signerAddr.String(),
		ToAddress:   signerAddr.String(),
		Amount:      sdk.NewCoins(sdk.NewInt64Coin(sdk.DefaultBondDenom, 1)),
	}

	txBytes := mustBuildMultiMsgSignedTx(t, ta, sendMsg, revealMsg)
	result := ta.DeliverVoteTx(txBytes)

	t.Logf("DeliverTx code=%d log=%q", result.Code, result.Log)
	if result.Code == 0 {
		t.Fatal("VULNERABILITY CONFIRMED: MsgRevealShare executed via standard Cosmos " +
			"tx path without ZKP verification. Fake enc_share corrupts tally accumulator.")
	}
}

// TestNoopSignerBypass_RevealShare is the MsgRevealShare variant of the noop-signer
// bypass. Same vector: multi-message tx [MsgSend, MsgRevealShare] where MsgRevealShare
// contributes 0 signers. MsgRevealShare accepts both ACTIVE and TALLYING rounds, so
// this uses the same ACTIVE-round setup as the other tests.
//
// Unlike MsgDelegateVote/MsgCastVote (which require RedPallas + ZKP), MsgRevealShare
// only requires ZKP #3. If the bypass succeeds, a fake enc_share is recorded for the
// proposal, corrupting the tally accumulator.
//
// This vector was never possible. The original attack required standard Cosmos
// message to be the first in a multi msg. With both MsgRevealShare tx.ValidateBasic()
// fails due to requiring at least one signer.
func TestNoopSignerBypass_DoubleRevealShare(t *testing.T) {
	ta := testutil.SetupTestApp(t)

	roundID := ta.SeedVotingSession(testutil.ValidCreateVotingSession())

	// Deliver a legitimate delegation so the commitment tree has a root
	// at a known height (required by RevealShare's anchor height validation).
	delMsg := testutil.ValidDelegation(roundID, 0x20)
	delTx := testutil.MustEncodeVoteTx(delMsg)
	delResult := ta.DeliverVoteTx(delTx)
	require.Equal(t, uint32(0), delResult.Code, "setup delegation should succeed")

	anchorHeight := uint64(ta.Height)

	// Build the exploit RevealShare with fake ZKP material but a valid
	// ElGamal ciphertext. Using a valid ciphertext (identity = Enc(0))
	// ensures the handler's enc_share deserialization succeeds, proving
	// the bypass reaches all the way through to state mutation.
	revealMsg := &votetypes.MsgRevealShare{
		ShareNullifier:           bytes.Repeat([]byte{0xE1}, 32),
		EncShare:                 elgamal.IdentityCiphertextBytes(),
		ProposalId:               1,
		VoteDecision:             1,
		Proof:                    []byte{0x42},
		VoteRoundId:              roundID,
		VoteCommTreeAnchorHeight: anchorHeight,
	}
	require.NoError(t, revealMsg.ValidateBasic())

	revealMsg2 := &votetypes.MsgRevealShare{
		ShareNullifier:           bytes.Repeat([]byte{0xE2}, 32),
		EncShare:                 elgamal.IdentityCiphertextBytes(),
		ProposalId:               1,
		VoteDecision:             1,
		Proof:                    []byte{0x42},
		VoteRoundId:              roundID,
		VoteCommTreeAnchorHeight: anchorHeight,
	}
	require.NoError(t, revealMsg.ValidateBasic())

	txConfig := ta.TxConfig()
	txBuilder := txConfig.NewTxBuilder()
	err := txBuilder.SetMsgs(revealMsg, revealMsg2)
	require.NoError(t, err)
	txBuilder.SetGasLimit(300_000)
	txBuilder.SetFeeAmount(sdk.NewCoins())

	txBytes, err := txConfig.TxEncoder()(txBuilder.GetTx())
	require.NoError(t, err)

	result := ta.DeliverVoteTx(txBytes)

	t.Logf("DeliverTx code=%d log=%q", result.Code, result.Log)
	if result.Code == 0 {
		t.Fatal("VULNERABILITY CONFIRMED: MsgRevealShare executed via standard Cosmos " +
			"tx path without ZKP verification. Fake enc_share corrupts tally accumulator.")
	}
}

// ---------------------------------------------------------------------------
// Authz MsgExec bypass: wrapping vote messages in authz.MsgExec
// ---------------------------------------------------------------------------
//
// Even though x/authz is not registered as a module on this chain,
// these tests verify that vote messages cannot be smuggled through
// authz.MsgExec. If authz were ever added, MsgExec would dispatch the
// wrapped message directly to its handler — bypassing the vote ante
// pipeline (ZKP/RedPallas). The tests prove the vector is blocked at
// either the codec level (InterfaceRegistry doesn't know MsgExec) or
// the handler level (no authz MsgServer registered).

// TestAuthzExecBypass_DelegateVote wraps a MsgDelegateVote (with fake ZKP
// material) inside authz.MsgExec and submits it as a standard Cosmos tx.
func TestAuthzExecBypass_DelegateVote(t *testing.T) {
	ta := testutil.SetupTestApp(t)

	roundID := ta.SeedVotingSession(testutil.ValidCreateVotingSession())

	signerAddr := sdk.AccAddress(ta.ValPrivKey.PubKey().Address())

	fakeDelegate := &votetypes.MsgDelegateVote{
		Rk:                  bytes.Repeat([]byte{0xAA}, 32),
		SpendAuthSig:        bytes.Repeat([]byte{0xBB}, 64),
		SignedNoteNullifier: bytes.Repeat([]byte{0xCC}, 32),
		CmxNew:              testutil.FpLE(0xBEEF),
		VanCmx:              testutil.FpLE(0xDEAD),
		GovNullifiers:       [][]byte{bytes.Repeat([]byte{0xFF}, 32)},
		Proof:               []byte{0x42},
		VoteRoundId:         roundID,
		Sighash:             bytes.Repeat([]byte{0x99}, 32),
	}

	execMsg := authz.NewMsgExec(signerAddr, []sdk.Msg{fakeDelegate})

	txConfig := ta.TxConfig()
	txBuilder := txConfig.NewTxBuilder()
	if err := txBuilder.SetMsgs(&execMsg); err != nil {
		// Codec-level rejection: InterfaceRegistry doesn't recognize MsgExec
		// or cannot pack the inner message into Any. This is a valid defense.
		t.Logf("MsgExec(MsgDelegateVote) rejected at codec level (good): %v", err)
		return
	}

	txBytes, err := tryBuildSignedTx(t, ta, &execMsg)
	if err != nil {
		t.Logf("MsgExec(MsgDelegateVote) rejected during tx construction (good): %v", err)
		return
	}

	checkResp := ta.CheckTxSync(txBytes)
	result := ta.DeliverVoteTx(txBytes)

	t.Logf("CheckTx  code=%d log=%q", checkResp.Code, checkResp.Log)
	t.Logf("DeliverTx code=%d log=%q", result.Code, result.Log)

	if checkResp.Code == 0 && result.Code == 0 {
		t.Fatal("VULNERABILITY CONFIRMED: MsgExec(MsgDelegateVote) executed via authz " +
			"dispatch without ZKP or RedPallas verification.")
	}

	t.Logf("MsgExec(MsgDelegateVote) blocked at handler level (good)")
}

// ---------------------------------------------------------------------------
// Noop-signer bypass: ceremony messages in multi-message standard Tx
// ---------------------------------------------------------------------------

// TestNoopSignerBypass_DealExecutiveAuthorityKey verifies that a ceremony
// message (MsgDealExecutiveAuthorityKey) cannot be smuggled into a standard
// Cosmos tx via the noop-signer multi-message vector.
//
// Like vote messages, ceremony messages have no cosmos.msg.v1.signer proto
// annotation, so GetSigners() returns []. A multi-msg tx [MsgSend, MsgDeal]
// would pass the standard ante chain with 1 signer / 1 signature.
//
// Without the isVoteModuleMsg check and MessageWhitelistDecorator, the only
// remaining defense is the handler-level ValidateProposerIsCreator call,
// which rejects during DeliverTx (creator != proposer). But CheckTx would
// still accept the tx into the mempool — a DoS vector.
//
// Two layers block this: isVoteModuleMsg in NewDualAnteHandler fires first
// (before any decorator), and MessageWhitelistDecorator in the standard
// ante chain provides a second barrier.
func TestNoopSignerBypass_DealExecutiveAuthorityKey(t *testing.T) {
	ta := testutil.SetupTestApp(t)

	signerAddr := sdk.AccAddress(ta.ValPrivKey.PubKey().Address())

	sendMsg := &banktypes.MsgSend{
		FromAddress: signerAddr.String(),
		ToAddress:   signerAddr.String(),
		Amount:      sdk.NewCoins(sdk.NewInt64Coin(sdk.DefaultBondDenom, 1)),
	}

	fakeDeal := &votetypes.MsgDealExecutiveAuthorityKey{
		Creator:   signerAddr.String(),
		EaPk:      bytes.Repeat([]byte{0xAA}, 32),
		Payloads:  nil,
		Threshold: 2,
	}

	txBytes := mustBuildMultiMsgSignedTx(t, ta, sendMsg, fakeDeal)

	checkResp := ta.CheckTxSync(txBytes)
	result := ta.DeliverVoteTx(txBytes)

	t.Logf("CheckTx  code=%d log=%q", checkResp.Code, checkResp.Log)
	t.Logf("DeliverTx code=%d log=%q", result.Code, result.Log)

	require.NotEqual(t, uint32(0), checkResp.Code,
		"MsgDealExecutiveAuthorityKey should be rejected in CheckTx")
	require.Contains(t, checkResp.Log, "multi-message transactions are not supported")
}

// TestNoopSignerBypass_AckExecutiveAuthorityKey verifies that
// MsgAckExecutiveAuthorityKey is blocked in standard Cosmos txs.
func TestNoopSignerBypass_AckExecutiveAuthorityKey(t *testing.T) {
	ta := testutil.SetupTestApp(t)

	signerAddr := sdk.AccAddress(ta.ValPrivKey.PubKey().Address())

	sendMsg := &banktypes.MsgSend{
		FromAddress: signerAddr.String(),
		ToAddress:   signerAddr.String(),
		Amount:      sdk.NewCoins(sdk.NewInt64Coin(sdk.DefaultBondDenom, 1)),
	}

	fakeAck := &votetypes.MsgAckExecutiveAuthorityKey{
		Creator:      signerAddr.String(),
		AckSignature: bytes.Repeat([]byte{0xBB}, 32),
		VoteRoundId:  bytes.Repeat([]byte{0xCC}, 32),
	}

	txBytes := mustBuildMultiMsgSignedTx(t, ta, sendMsg, fakeAck)

	checkResp := ta.CheckTxSync(txBytes)
	result := ta.DeliverVoteTx(txBytes)

	t.Logf("CheckTx  code=%d log=%q", checkResp.Code, checkResp.Log)
	t.Logf("DeliverTx code=%d log=%q", result.Code, result.Log)

	require.NotEqual(t, uint32(0), checkResp.Code,
		"MsgAckExecutiveAuthorityKey should be rejected in CheckTx")
	require.Contains(t, checkResp.Log, "multi-message transactions are not supported")
}

// TestNoopSignerBypass_SubmitPartialDecryption verifies that
// MsgSubmitPartialDecryption is blocked in standard Cosmos txs.
func TestNoopSignerBypass_SubmitPartialDecryption(t *testing.T) {
	ta := testutil.SetupTestApp(t)

	signerAddr := sdk.AccAddress(ta.ValPrivKey.PubKey().Address())

	sendMsg := &banktypes.MsgSend{
		FromAddress: signerAddr.String(),
		ToAddress:   signerAddr.String(),
		Amount:      sdk.NewCoins(sdk.NewInt64Coin(sdk.DefaultBondDenom, 1)),
	}

	fakePartial := &votetypes.MsgSubmitPartialDecryption{
		VoteRoundId:    bytes.Repeat([]byte{0xDD}, 32),
		Creator:        signerAddr.String(),
		ValidatorIndex: 1,
		Entries:        nil,
	}

	txBytes := mustBuildMultiMsgSignedTx(t, ta, sendMsg, fakePartial)

	checkResp := ta.CheckTxSync(txBytes)
	result := ta.DeliverVoteTx(txBytes)

	t.Logf("CheckTx  code=%d log=%q", checkResp.Code, checkResp.Log)
	t.Logf("DeliverTx code=%d log=%q", result.Code, result.Log)

	require.NotEqual(t, uint32(0), checkResp.Code,
		"MsgSubmitPartialDecryption should be rejected in CheckTx")
	require.Contains(t, checkResp.Log, "multi-message transactions are not supported")
}

// TestNoopSignerBypass_RevealShare_TallyCorruption is an end-to-end tally
// corruption repro using the noop-signer multi-message attack vector.
//
// Attack: a standard Cosmos tx containing [MsgSend, MsgRevealShare]. Because
// MsgRevealShare has no cosmos.msg.v1.signer annotation, GetSigners() returns
// [] for it. The signer union is just the MsgSend signer (len=1), so 1
// valid signature passes all ante checks. The MsgRevealShare handler then
// executes without ZKP #3, accumulating an attacker-chosen ciphertext into
// the tally.
//
// The test:
//  1. Sets up a round with a real EA keypair (encrypt + decrypt possible).
//  2. Delivers a legitimate RevealShare encrypting value=1, decrypts the tally
//     accumulator via BSGS, and verifies it equals 1.
//  3. Builds the exploit tx: [MsgSend (self-transfer), MsgRevealShare(Enc(41))]
//     with a single valid signature from the MsgSend signer.
//  4. If the bypass succeeds: reads the tally back, decrypts it, and proves it
//     shifted from Enc(1) to Enc(42), share count inflated from 1 to 2 —
//     the election result is forged.
//  5. If blocked: verifies the tally is byte-identical to the pre-attack snapshot.
//
// This is now mitigated and is not an open problem.
func TestNoopSignerBypass_RevealShare_TallyCorruption(t *testing.T) {
	ta, eaPk, eaSkBytes := testutil.SetupTestAppWithEAKey(t)
	eaSk, err := elgamal.UnmarshalSecretKey(eaSkBytes)
	require.NoError(t, err)

	roundID := ta.SeedVotingSession(testutil.ValidCreateVotingSession())

	// --- Phase 1: Delegation + legitimate reveal ---

	delMsg := testutil.ValidDelegation(roundID, 0x50)
	delTx := testutil.MustEncodeVoteTx(delMsg)
	delResult := ta.DeliverVoteTx(delTx)
	require.Equal(t, uint32(0), delResult.Code, "setup delegation should succeed")

	anchorHeight := uint64(ta.Height)

	legitimateCt, err := elgamal.Encrypt(eaPk, 1, rand.Reader)
	require.NoError(t, err)
	legitimateCtBytes, err := elgamal.MarshalCiphertext(legitimateCt)
	require.NoError(t, err)

	revealMsg := testutil.ValidRevealShareReal(roundID, anchorHeight, 0x50, 1, 1, legitimateCtBytes)
	revealTx := testutil.MustEncodeVoteTx(revealMsg)
	revealResult := ta.DeliverVoteTx(revealTx)
	require.Equal(t, uint32(0), revealResult.Code, "legitimate RevealShare should succeed")

	// --- Phase 2: Snapshot pre-attack tally, decrypt, verify value=1 ---

	ctx := ta.NewUncachedContext(false, cmtproto.Header{Height: ta.Height})
	kvStore := ta.VoteKeeper().OpenKVStore(ctx)

	tallyBefore, err := ta.VoteKeeper().GetTally(kvStore, roundID, 1, 1)
	require.NoError(t, err)
	require.NotNil(t, tallyBefore, "tally accumulator should exist after legitimate reveal")

	sharesBefore, err := ta.VoteKeeper().GetShareCount(kvStore, roundID, 1, 1)
	require.NoError(t, err)
	require.Equal(t, uint64(1), sharesBefore)

	ctBefore, err := elgamal.UnmarshalCiphertext(tallyBefore)
	require.NoError(t, err)
	vGBefore := elgamal.DecryptToPoint(eaSk, ctBefore)
	bsgs := elgamal.NewBSGSTable(1 << 16)
	valBefore, err := bsgs.Solve(vGBefore)
	require.NoError(t, err)
	require.Equal(t, uint64(1), valBefore, "pre-attack tally should decrypt to 1")

	t.Logf("Pre-attack tally: value=%d, shares=%d (healthy)", valBefore, sharesBefore)

	// --- Phase 3: Multi-msg exploit [MsgSend, MsgRevealShare(Enc(41))] ---
	//
	// MsgSend contributes 1 signer. MsgRevealShare contributes 0 (no signer
	// annotation). Union = 1 → 1 signature → all ante checks pass.

	attackerValue := uint64(41)
	attackerCt, err := elgamal.Encrypt(eaPk, attackerValue, rand.Reader)
	require.NoError(t, err)
	attackerCtBytes, err := elgamal.MarshalCiphertext(attackerCt)
	require.NoError(t, err)

	signerAddr := sdk.AccAddress(ta.ValPrivKey.PubKey().Address())

	carrierSend := &banktypes.MsgSend{
		FromAddress: signerAddr.String(),
		ToAddress:   signerAddr.String(),
		Amount:      sdk.NewCoins(sdk.NewInt64Coin(sdk.DefaultBondDenom, 1)),
	}

	fakeReveal := &votetypes.MsgRevealShare{
		ShareNullifier:           bytes.Repeat([]byte{0xF1}, 32),
		EncShare:                 attackerCtBytes,
		ProposalId:               1,
		VoteDecision:             1,
		Proof:                    []byte{0x42},
		VoteRoundId:              roundID,
		VoteCommTreeAnchorHeight: anchorHeight,
	}

	txBytes := mustBuildMultiMsgSignedTx(t, ta, carrierSend, fakeReveal)

	checkResp := ta.CheckTxSync(txBytes)
	result := ta.DeliverVoteTx(txBytes)

	t.Logf("CheckTx  code=%d log=%q", checkResp.Code, checkResp.Log)
	t.Logf("DeliverTx code=%d log=%q", result.Code, result.Log)

	// --- Phase 4: Post-attack tally inspection ---

	ctx = ta.NewUncachedContext(false, cmtproto.Header{Height: ta.Height})
	kvStore = ta.VoteKeeper().OpenKVStore(ctx)

	tallyAfter, err := ta.VoteKeeper().GetTally(kvStore, roundID, 1, 1)
	require.NoError(t, err)

	sharesAfter, err := ta.VoteKeeper().GetShareCount(kvStore, roundID, 1, 1)
	require.NoError(t, err)

	if checkResp.Code == 0 && result.Code == 0 {
		ctAfter, err := elgamal.UnmarshalCiphertext(tallyAfter)
		require.NoError(t, err)
		vGAfter := elgamal.DecryptToPoint(eaSk, ctAfter)
		valAfter, bsgsErr := bsgs.Solve(vGAfter)

		corruptedStr := "undecryptable (overflow)"
		if bsgsErr == nil {
			corruptedStr = fmt.Sprintf("%d", valAfter)
		}

		t.Fatalf("VULNERABILITY CONFIRMED: tally corrupted by noop-signer multi-msg bypass!\n"+
			"  Before: value=%d, shares=%d\n"+
			"  After:  value=%s, shares=%d\n"+
			"  Attack tx: [MsgSend, MsgRevealShare(Enc(%d))]\n"+
			"  MsgRevealShare executed without ZKP #3 verification.\n"+
			"  Expected honest tally: 1  |  Corrupted tally: %s\n"+
			"  The election result is forged.",
			valBefore, sharesBefore,
			corruptedStr, sharesAfter,
			attackerValue, corruptedStr,
		)
	}

	// Attack blocked — verify tally integrity preserved.
	assertTallyUnchanged(t, ta, roundID, tallyBefore, sharesBefore)
	t.Logf("Multi-msg [MsgSend, MsgRevealShare] blocked; tally integrity preserved (value=%d, shares=%d)",
		valBefore, sharesBefore)
}

// assertTallyUnchanged verifies that the on-chain tally accumulator and share
// count for (proposal=1, decision=1) are identical to the pre-attack snapshot.
func assertTallyUnchanged(t *testing.T, ta *testutil.TestApp, roundID, expectedTally []byte, expectedShares uint64) {
	t.Helper()

	ctx := ta.NewUncachedContext(false, cmtproto.Header{Height: ta.Height})
	kvStore := ta.VoteKeeper().OpenKVStore(ctx)

	tallyNow, err := ta.VoteKeeper().GetTally(kvStore, roundID, 1, 1)
	require.NoError(t, err)
	require.Equal(t, expectedTally, tallyNow, "tally accumulator should be unchanged after blocked attack")

	sharesNow, err := ta.VoteKeeper().GetShareCount(kvStore, roundID, 1, 1)
	require.NoError(t, err)
	require.Equal(t, expectedShares, sharesNow, "share count should be unchanged after blocked attack")
}

// TestZeroCostAttack_MsgSetWithdrawAddress demonstrates that the noop-signer
// tally corruption attack requires zero funds — only an on-chain account.
//
// Part A: A fresh keypair with NO on-chain account attempts the attack.
//
//	The tx is rejected because SigVerificationDecorator calls GetSignerAcc
//	which fails for non-existent accounts.
//
// Part B: The validator sends 1 usvote to the attacker, creating the account.
//
//	The attacker retries with [MsgSetWithdrawAddress, MsgRevealShare(Enc(41))].
//	MsgSetWithdrawAddress is a zero-cost carrier (no funds transferred).
//	The attack succeeds: tally shifts from Enc(1) to Enc(42), forging the result.
//
// This proves the attack surface is any on-chain account, not just validators.
func TestZeroCostAttack_MsgSetWithdrawAddress(t *testing.T) {
	ta, eaPk, eaSkBytes := testutil.SetupTestAppWithEAKey(t)
	eaSk, err := elgamal.UnmarshalSecretKey(eaSkBytes)
	require.NoError(t, err)

	roundID := ta.SeedVotingSession(testutil.ValidCreateVotingSession())

	// --- Setup: delegation + legitimate reveal (value=1) ---

	delMsg := testutil.ValidDelegation(roundID, 0x60)
	delTx := testutil.MustEncodeVoteTx(delMsg)
	delResult := ta.DeliverVoteTx(delTx)
	require.Equal(t, uint32(0), delResult.Code, "setup delegation should succeed")

	anchorHeight := uint64(ta.Height)

	legitimateCt, err := elgamal.Encrypt(eaPk, 1, rand.Reader)
	require.NoError(t, err)
	legitimateCtBytes, err := elgamal.MarshalCiphertext(legitimateCt)
	require.NoError(t, err)

	revealMsg := testutil.ValidRevealShareReal(roundID, anchorHeight, 0x60, 1, 1, legitimateCtBytes)
	revealTx := testutil.MustEncodeVoteTx(revealMsg)
	revealResult := ta.DeliverVoteTx(revealTx)
	require.Equal(t, uint32(0), revealResult.Code, "legitimate RevealShare should succeed")

	// Snapshot tally: value=1, shares=1.
	ctx := ta.NewUncachedContext(false, cmtproto.Header{Height: ta.Height})
	kvStore := ta.VoteKeeper().OpenKVStore(ctx)
	tallyBefore, err := ta.VoteKeeper().GetTally(kvStore, roundID, 1, 1)
	require.NoError(t, err)
	require.NotNil(t, tallyBefore)
	sharesBefore, err := ta.VoteKeeper().GetShareCount(kvStore, roundID, 1, 1)
	require.NoError(t, err)
	require.Equal(t, uint64(1), sharesBefore)

	// Prepare attacker ciphertext and payload (shared by both parts).
	attackerValue := uint64(41)
	attackerCt, err := elgamal.Encrypt(eaPk, attackerValue, rand.Reader)
	require.NoError(t, err)
	attackerCtBytes, err := elgamal.MarshalCiphertext(attackerCt)
	require.NoError(t, err)

	// Generate the attacker's keypair — completely separate from the validator.
	attackerPrivKey := secp256k1.GenPrivKey()
	attackerAddr := sdk.AccAddress(attackerPrivKey.PubKey().Address())

	fakeReveal := &votetypes.MsgRevealShare{
		ShareNullifier:           bytes.Repeat([]byte{0xF2}, 32),
		EncShare:                 attackerCtBytes,
		ProposalId:               1,
		VoteDecision:             1,
		Proof:                    []byte{0x42},
		VoteRoundId:              roundID,
		VoteCommTreeAnchorHeight: anchorHeight,
	}

	// ---------------------------------------------------------------
	// Part A: Attacker has NO on-chain account — attack should fail.
	// ---------------------------------------------------------------

	t.Log("--- Part A: unfunded attacker (no on-chain account) ---")

	carrierA := &distrtypes.MsgSetWithdrawAddress{
		DelegatorAddress: attackerAddr.String(),
		WithdrawAddress:  attackerAddr.String(),
	}

	txBytesA := buildSignedTxWithKey(t, ta, attackerPrivKey, 0, 0, carrierA, fakeReveal)

	checkRespA := ta.CheckTxSync(txBytesA)
	t.Logf("Part A CheckTx code=%d log=%q", checkRespA.Code, checkRespA.Log)
	require.NotEqual(t, uint32(0), checkRespA.Code,
		"unfunded attacker should be rejected (account does not exist)")

	assertTallyUnchanged(t, ta, roundID, tallyBefore, sharesBefore)
	t.Log("Part A passed: unfunded attacker rejected, tally intact")

	// ---------------------------------------------------------------
	// Part B: Fund attacker with 1 usvote to create the account.
	// MsgSend is blocked by the ante handler, so we use the bank
	// keeper directly to simulate account creation.
	// ---------------------------------------------------------------

	t.Log("--- Part B: fund attacker with 1 usvote, retry ---")

	valAddr := sdk.AccAddress(ta.ValPrivKey.PubKey().Address())
	ctx = ta.NewUncachedContext(false, cmtproto.Header{Height: ta.Height})
	err = ta.BankKeeper.SendCoins(ctx, valAddr, attackerAddr, sdk.NewCoins(sdk.NewInt64Coin(sdk.DefaultBondDenom, 1)))
	require.NoError(t, err, "funding attacker should succeed")

	// Verify the attacker account now exists.
	ctx = ta.NewUncachedContext(false, cmtproto.Header{Height: ta.Height})
	attackerAcc := ta.AccountKeeper.GetAccount(ctx, attackerAddr)
	require.NotNil(t, attackerAcc, "attacker account should exist after funding")
	t.Logf("Attacker account created: addr=%s balance=1usvote", attackerAddr)

	// Use a fresh nullifier so it doesn't collide with Part A's attempt.
	fakeRevealB := &votetypes.MsgRevealShare{
		ShareNullifier:           bytes.Repeat([]byte{0xF3}, 32),
		EncShare:                 attackerCtBytes,
		ProposalId:               1,
		VoteDecision:             1,
		Proof:                    []byte{0x42},
		VoteRoundId:              roundID,
		VoteCommTreeAnchorHeight: anchorHeight,
	}

	carrierB := &distrtypes.MsgSetWithdrawAddress{
		DelegatorAddress: attackerAddr.String(),
		WithdrawAddress:  attackerAddr.String(),
	}

	accNum := attackerAcc.GetAccountNumber()
	accSeq := attackerAcc.GetSequence()
	txBytesB := buildSignedTxWithKey(t, ta, attackerPrivKey, accNum, accSeq, carrierB, fakeRevealB)

	checkRespB := ta.CheckTxSync(txBytesB)
	resultB := ta.DeliverVoteTx(txBytesB)

	t.Logf("Part B CheckTx  code=%d log=%q", checkRespB.Code, checkRespB.Log)
	t.Logf("Part B DeliverTx code=%d log=%q", resultB.Code, resultB.Log)

	// --- Tally inspection ---

	ctx = ta.NewUncachedContext(false, cmtproto.Header{Height: ta.Height})
	kvStore = ta.VoteKeeper().OpenKVStore(ctx)
	tallyAfter, err := ta.VoteKeeper().GetTally(kvStore, roundID, 1, 1)
	require.NoError(t, err)
	sharesAfter, err := ta.VoteKeeper().GetShareCount(kvStore, roundID, 1, 1)
	require.NoError(t, err)

	if checkRespB.Code == 0 && resultB.Code == 0 {
		ctBefore, err := elgamal.UnmarshalCiphertext(tallyBefore)
		require.NoError(t, err)
		bsgs := elgamal.NewBSGSTable(1 << 16)
		vGBefore := elgamal.DecryptToPoint(eaSk, ctBefore)
		valBefore, err := bsgs.Solve(vGBefore)
		require.NoError(t, err)

		ctAfter, err := elgamal.UnmarshalCiphertext(tallyAfter)
		require.NoError(t, err)
		vGAfter := elgamal.DecryptToPoint(eaSk, ctAfter)
		valAfter, bsgsErr := bsgs.Solve(vGAfter)

		corruptedStr := "undecryptable (overflow)"
		if bsgsErr == nil {
			corruptedStr = fmt.Sprintf("%d", valAfter)
		}

		t.Fatalf("VULNERABILITY CONFIRMED: zero-cost tally corruption!\n"+
			"  Attacker funded with just 1 usvote (not transferred in attack).\n"+
			"  Carrier: MsgSetWithdrawAddress (zero-cost, no funds moved).\n"+
			"  Before: value=%d, shares=%d\n"+
			"  After:  value=%s, shares=%d\n"+
			"  Attack cost: 0 usvote (only needed 1 usvote to create account).\n"+
			"  The election result is forged by any on-chain account holder.",
			valBefore, sharesBefore,
			corruptedStr, sharesAfter,
		)
	}

	assertTallyUnchanged(t, ta, roundID, tallyBefore, sharesBefore)
	t.Logf("Part B blocked; tally integrity preserved (shares=%d)", sharesBefore)
}

// buildSignedTxWithKey builds a standard Cosmos SDK transaction signed by an
// arbitrary secp256k1 key. Unlike mustBuildMultiMsgSignedTx (which always uses
// the validator key), this accepts explicit account number and sequence so it
// works for freshly-created accounts.
func buildSignedTxWithKey(t *testing.T, ta *testutil.TestApp, privKey *secp256k1.PrivKey, accNum, accSeq uint64, msgs ...sdk.Msg) []byte {
	t.Helper()

	txConfig := ta.TxConfig()
	accAddr := sdk.AccAddress(privKey.PubKey().Address())

	txBuilder := txConfig.NewTxBuilder()
	err := txBuilder.SetMsgs(msgs...)
	require.NoError(t, err)
	txBuilder.SetGasLimit(300_000)
	txBuilder.SetFeeAmount(sdk.NewCoins())

	signMode, err := authsigning.APISignModeToInternal(txConfig.SignModeHandler().DefaultMode())
	require.NoError(t, err)

	sigData := signing.SingleSignatureData{SignMode: signMode, Signature: nil}
	sig := signing.SignatureV2{
		PubKey:   privKey.PubKey(),
		Data:     &sigData,
		Sequence: accSeq,
	}
	err = txBuilder.SetSignatures(sig)
	require.NoError(t, err)

	signerData := authsigning.SignerData{
		ChainID:       "svote-test-1",
		AccountNumber: accNum,
		Sequence:      accSeq,
		PubKey:        privKey.PubKey(),
		Address:       accAddr.String(),
	}
	signBytes, err := authsigning.GetSignBytesAdapter(
		context.Background(), txConfig.SignModeHandler(), signMode, signerData, txBuilder.GetTx())
	require.NoError(t, err)

	sigBytes, err := privKey.Sign(signBytes)
	require.NoError(t, err)

	sigData = signing.SingleSignatureData{SignMode: signMode, Signature: sigBytes}
	sig = signing.SignatureV2{
		PubKey:   privKey.PubKey(),
		Data:     &sigData,
		Sequence: accSeq,
	}
	err = txBuilder.SetSignatures(sig)
	require.NoError(t, err)

	txBytes, err := txConfig.TxEncoder()(txBuilder.GetTx())
	require.NoError(t, err)
	return txBytes
}

// mustBuildMultiMsgSignedTx builds a standard Cosmos SDK transaction containing
// multiple messages, signs it with the genesis validator's secp256k1 key, and
// returns the encoded tx bytes.
func mustBuildMultiMsgSignedTx(t *testing.T, ta *testutil.TestApp, msgs ...sdk.Msg) []byte {
	t.Helper()

	txConfig := ta.TxConfig()
	privKey := ta.ValPrivKey
	accAddr := sdk.AccAddress(privKey.PubKey().Address())

	ctx := ta.NewUncachedContext(false, cmtproto.Header{Height: ta.Height})
	acc := ta.AccountKeeper.GetAccount(ctx, accAddr)
	require.NotNil(t, acc, "validator account not found")

	accNum := acc.GetAccountNumber()
	accSeq := acc.GetSequence()

	txBuilder := txConfig.NewTxBuilder()
	err := txBuilder.SetMsgs(msgs...)
	require.NoError(t, err)
	txBuilder.SetGasLimit(300_000)
	txBuilder.SetFeeAmount(sdk.NewCoins())

	signMode, err := authsigning.APISignModeToInternal(txConfig.SignModeHandler().DefaultMode())
	require.NoError(t, err)

	sigData := signing.SingleSignatureData{
		SignMode:  signMode,
		Signature: nil,
	}
	sig := signing.SignatureV2{
		PubKey:   privKey.PubKey(),
		Data:     &sigData,
		Sequence: accSeq,
	}
	err = txBuilder.SetSignatures(sig)
	require.NoError(t, err)

	signerData := authsigning.SignerData{
		ChainID:       "svote-test-1",
		AccountNumber: accNum,
		Sequence:      accSeq,
		PubKey:        privKey.PubKey(),
		Address:       accAddr.String(),
	}
	signBytes, err := authsigning.GetSignBytesAdapter(
		context.Background(), txConfig.SignModeHandler(), signMode, signerData, txBuilder.GetTx())
	require.NoError(t, err)

	sigBytes, err := privKey.Sign(signBytes)
	require.NoError(t, err)

	sigData = signing.SingleSignatureData{
		SignMode:  signMode,
		Signature: sigBytes,
	}
	sig = signing.SignatureV2{
		PubKey:   privKey.PubKey(),
		Data:     &sigData,
		Sequence: accSeq,
	}
	err = txBuilder.SetSignatures(sig)
	require.NoError(t, err)

	txBytes, err := txConfig.TxEncoder()(txBuilder.GetTx())
	require.NoError(t, err)

	return txBytes
}

// tryBuildSignedTx is like mustBuildMultiMsgSignedTx but returns an error
// instead of calling t.Fatal. Used for authz bypass tests where codec-level
// rejection (MsgExec not registered in InterfaceRegistry) is a valid outcome.
func tryBuildSignedTx(t *testing.T, ta *testutil.TestApp, msgs ...sdk.Msg) ([]byte, error) {
	t.Helper()

	txConfig := ta.TxConfig()
	privKey := ta.ValPrivKey
	accAddr := sdk.AccAddress(privKey.PubKey().Address())

	ctx := ta.NewUncachedContext(false, cmtproto.Header{Height: ta.Height})
	acc := ta.AccountKeeper.GetAccount(ctx, accAddr)
	if acc == nil {
		return nil, fmt.Errorf("validator account not found")
	}

	accNum := acc.GetAccountNumber()
	accSeq := acc.GetSequence()

	txBuilder := txConfig.NewTxBuilder()
	if err := txBuilder.SetMsgs(msgs...); err != nil {
		return nil, fmt.Errorf("SetMsgs: %w", err)
	}
	txBuilder.SetGasLimit(300_000)
	txBuilder.SetFeeAmount(sdk.NewCoins())

	signMode, err := authsigning.APISignModeToInternal(txConfig.SignModeHandler().DefaultMode())
	if err != nil {
		return nil, fmt.Errorf("sign mode: %w", err)
	}

	sigData := signing.SingleSignatureData{
		SignMode:  signMode,
		Signature: nil,
	}
	sig := signing.SignatureV2{
		PubKey:   privKey.PubKey(),
		Data:     &sigData,
		Sequence: accSeq,
	}
	if err := txBuilder.SetSignatures(sig); err != nil {
		return nil, fmt.Errorf("SetSignatures (empty): %w", err)
	}

	signerData := authsigning.SignerData{
		ChainID:       "svote-test-1",
		AccountNumber: accNum,
		Sequence:      accSeq,
		PubKey:        privKey.PubKey(),
		Address:       accAddr.String(),
	}
	signBytes, err := authsigning.GetSignBytesAdapter(
		context.Background(), txConfig.SignModeHandler(), signMode, signerData, txBuilder.GetTx())
	if err != nil {
		return nil, fmt.Errorf("GetSignBytesAdapter: %w", err)
	}

	sigBytes, err := privKey.Sign(signBytes)
	if err != nil {
		return nil, fmt.Errorf("Sign: %w", err)
	}

	sigData = signing.SingleSignatureData{
		SignMode:  signMode,
		Signature: sigBytes,
	}
	sig = signing.SignatureV2{
		PubKey:   privKey.PubKey(),
		Data:     &sigData,
		Sequence: accSeq,
	}
	if err := txBuilder.SetSignatures(sig); err != nil {
		return nil, fmt.Errorf("SetSignatures (signed): %w", err)
	}

	txBytes, err := txConfig.TxEncoder()(txBuilder.GetTx())
	if err != nil {
		return nil, fmt.Errorf("TxEncoder: %w", err)
	}

	return txBytes, nil
}
