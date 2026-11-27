//go:build !proxyenabled

package app_test

import (
	"bytes"
	"testing"

	errorsmod "cosmossdk.io/errors"
	abci "github.com/cometbft/cometbft/abci/types"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"

	voteapi "github.com/valargroup/vote-sdk/api"
	"github.com/valargroup/vote-sdk/testutil"
	"github.com/valargroup/vote-sdk/x/vote/types"
)

// TestDormantProxyDelegationTagsAreNotVoteTxs checks that 0x09 and 0x0B
// bodies go to the standard decoder, which refuses them as before.
func TestDormantProxyDelegationTagsAreNotVoteTxs(t *testing.T) {
	ta := testutil.SetupTestApp(t)
	batch := &types.MsgProxyDelegationBatch{
		VoteRoundId:              bytes.Repeat([]byte{0x01}, 32),
		VoteCommTreeAnchorHeight: 10,
		Dcs: []*types.ProxyDelegationAction{{
			VanNullifier:              bytes.Repeat([]byte{0x11}, 32),
			RVpk:                      append([]byte(nil), testutil.DummyPallasPoint...),
			VoteAuthorityNoteNew:      bytes.Repeat([]byte{0x12}, 32),
			ProxyDelegationCommitment: bytes.Repeat([]byte{0x13}, 32),
			DcSlotNullifier:           bytes.Repeat([]byte{0x14}, 32),
			Proof:                     []byte("mock-zkp4-proof"),
			VoteAuthSig:               bytes.Repeat([]byte{0x15}, 64),
		}},
		ExpiryHeight: 100,
	}
	op := &types.MsgDelegateOp{Retire: &types.DelegateRetire{
		DelegateIndex: 1,
		DkSig:         bytes.Repeat([]byte{0x22}, 64),
	}}
	for tag, msg := range map[byte]proto.Message{
		voteapi.TagProxyDelegationBatch: batch,
		voteapi.TagDelegateOp:           op,
	} {
		body, err := (proto.MarshalOptions{Deterministic: true}).Marshal(msg)
		require.NoError(t, err)
		txBytes := append([]byte{tag}, body...)
		_, err = ta.TxDecode(txBytes)
		require.ErrorIs(t, err, sdkerrors.ErrTxDecode, "tag 0x%02x", tag)

		check := ta.CheckTxSync(txBytes)
		require.Equal(t, sdkerrors.ErrTxDecode.ABCICode(), check.Code, check.Log)
		require.Equal(t, sdkerrors.ErrTxDecode.Codespace(), check.Codespace)

		result := ta.DeliverVoteTx(txBytes)
		require.Equal(t, sdkerrors.ErrTxDecode.ABCICode(), result.Code, result.Log)
		require.Equal(t, sdkerrors.ErrTxDecode.Codespace(), result.Codespace)
		require.Zero(t, result.GasUsed)
	}
}

// TestDormantProxyCoordinatorPayloadsAreUnsupported checks the three proxy
// delegation payloads end to end while dormant: registered (contracts-v1
// §5.5), a signed coordinator proposal carrying one now decodes, and the
// handler refuses it as an unsupported action, storing nothing (§9).
func TestDormantProxyCoordinatorPayloadsAreUnsupported(t *testing.T) {
	ta := testutil.SetupTestApp(t)
	manager := ta.ValidatorAccAddr()
	ta.SeedVoteManagers(manager)

	for _, payload := range []proto.Message{
		&types.MsgSetProxyDelegationParams{Creator: manager, MaxDelegates: 5},
		&types.MsgSetProxyDelegationPause{Creator: manager, Paused: true},
		&types.MsgSetDelegateStatus{
			Creator:         manager,
			DelegateIndexes: []uint32{1},
			Action:          types.DelegateStatusActionSuspend,
			Reason:          types.DelegateStatusReasonOther,
		},
	} {
		typeURL := "/" + string(proto.MessageName(payload))
		// MustBuildSignedCoordinatorActionTx also requires the tx to decode.
		result := ta.DeliverVoteTx(ta.MustBuildSignedCoordinatorActionTx(manager, payload))
		require.NotZero(t, result.Code, typeURL)
		require.Contains(t, result.Log, "unsupported coordinator action: "+typeURL)
	}

	ctx := ta.NewUncachedContext(false, cmtproto.Header{Height: ta.Height})
	kv := ta.VoteKeeper().OpenKVStore(ctx)
	hasParams, err := kv.Has(types.ProxyDelegationParamsKey)
	require.NoError(t, err)
	require.False(t, hasParams)
	require.NoError(t, ta.VoteKeeper().IterateCoordinatorActions(kv, func(action *types.CoordinatorAction) bool {
		t.Errorf("unexpected stored action %d of type %s", action.ActionId, action.Payload.GetTypeUrl())
		return false
	}))
}

// TestDormantPoolSharesAreRejectedAsToday checks a proposal-0 reveal end to
// end while dormant (contracts-v1 §5.4, §9): CheckTx and FinalizeBlock refuse
// it at ValidateBasic with today's code 1 and message, the proposer drops it
// as malformed, and a block holding it is rejected.
func TestDormantPoolSharesAreRejectedAsToday(t *testing.T) {
	ta := testutil.SetupTestApp(t)
	msg := testutil.ValidRevealShare(bytes.Repeat([]byte{0x01}, 32), 1, 0x11)
	msg.ProposalId, msg.VoteDecision = types.PoolProposalID, 1
	txBytes, err := voteapi.EncodeVoteTx(msg)
	require.NoError(t, err)

	const log = "invalid message field: proposal_id must be 1..50, got 0"
	check := ta.CheckTxSync(txBytes)
	require.Equal(t, uint32(1), check.Code)
	require.Equal(t, log, check.Log)
	result := ta.DeliverVoteTx(txBytes)
	require.Equal(t, uint32(1), result.Code)
	require.Equal(t, log, result.Log)

	other := []byte{0xAA, 0x01}
	require.Equal(t, [][]byte{other}, ta.CallPrepareProposalWithTxs([][]byte{txBytes, other}).Txs)
	require.Equal(t, abci.ResponseProcessProposal_REJECT, ta.CallProcessProposal([][]byte{txBytes}).Status)
}

// TestDormantValidateBasicFailuresKeepCodeOne checks end to end that while
// dormant a stateless failure of 0x02, 0x03, 0x05, 0x06 or 0x07 still reports
// today's code 1 in codespace undefined from CheckTx and FinalizeBlock: BaseApp
// runs ValidateBasic before the vote ante, and only activation surfaces the
// registered code (contracts-v1 §0).
func TestDormantValidateBasicFailuresKeepCodeOne(t *testing.T) {
	ta := testutil.SetupTestApp(t)
	roundID := bytes.Repeat([]byte{0x01}, 32)
	delegation := testutil.ValidDelegation(roundID, 0x11)
	delegation.Rk = make([]byte, 31)
	cast := testutil.ValidCastVote(roundID, 10, 0x12)
	cast.VoteAuthSig = make([]byte, 63)
	tally := testutil.ValidSubmitTally(roundID, "")
	batch := &types.MsgCastVoteBatch{Votes: []*types.MsgCastVote{testutil.ValidCastVote(roundID, 10, 0x13)}}
	batch.Votes[0].RVpk = make([]byte, 32)
	composite := &types.MsgDelegateAndCastVoteBatch{
		Delegation: testutil.ValidDelegation(roundID, 0x14),
		Batch:      &types.MsgCastVoteBatch{Votes: []*types.MsgCastVote{testutil.ValidCastVote(roundID, 0, 0x15)}},
	}
	composite.Delegation.VanCmx = make([]byte, 31)

	for _, msg := range []types.VoteMessage{delegation, cast, tally, batch, composite} {
		txBytes := testutil.MustEncodeVoteTx(msg)
		wantLog := msg.ValidateBasic().Error()

		check := ta.CheckTxSync(txBytes)
		require.Equal(t, uint32(1), check.Code, "%T: %s", msg, check.Log)
		require.Equal(t, errorsmod.UndefinedCodespace, check.Codespace, "%T", msg)
		require.Equal(t, wantLog, check.Log)

		result := ta.DeliverVoteTx(txBytes)
		require.Equal(t, uint32(1), result.Code, "%T: %s", msg, result.Log)
		require.Equal(t, errorsmod.UndefinedCodespace, result.Codespace, "%T", msg)
		require.Equal(t, wantLog, result.Log)
	}
}
