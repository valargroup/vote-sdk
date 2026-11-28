//go:build !proxyenabled

package app_test

import (
	"bytes"
	"testing"

	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"

	voteapi "github.com/valargroup/vote-sdk/api"
	"github.com/valargroup/vote-sdk/testutil"
	"github.com/valargroup/vote-sdk/x/vote/keeper"
	"github.com/valargroup/vote-sdk/x/vote/types"
)

// TestProxyDelegationDormant checks end to end that everything proxy
// delegation adds stays off while the gate is closed (contracts-v1 §9): 0x09
// and 0x0B bodies go to the standard decoder, which refuses them as before;
// the three proxy coordinator payloads decode in a signed proposal but are
// unsupported actions; a proxy-flagged round is refused with code 69; the
// capability fields and registry queries are absent; and nothing is stored.
func TestProxyDelegationDormant(t *testing.T) {
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
	flagged := testutil.ValidCreateVotingSessionAt(ta.Time)
	flagged.Creator, flagged.ProxyDelegation = manager, true
	result := ta.DeliverVoteTx(ta.MustBuildSignedCoordinatorActionTx(manager, flagged))
	require.Equal(t, types.ErrProxyDelegationDisabled.ABCICode(), result.Code, result.Log)
	require.Equal(t, types.ModuleName, result.Codespace)

	ctx := ta.NewUncachedContext(false, cmtproto.Header{Height: ta.Height})
	qs := keeper.NewQueryServerImpl(ta.VoteKeeper())
	caps, err := qs.ProtocolCapabilities(ctx, &types.QueryProtocolCapabilitiesRequest{})
	require.NoError(t, err)
	require.Zero(t, caps.ProxyDelegationVersion)
	require.Empty(t, caps.CircuitFingerprints)
	_, err = qs.ProxyDelegates(ctx, &types.QueryProxyDelegatesRequest{})
	require.Equal(t, codes.Unimplemented, status.Code(err), "error: %v", err)

	kv := ta.VoteKeeper().OpenKVStore(ctx)
	hasParams, err := kv.Has(types.ProxyDelegationParamsKey)
	require.NoError(t, err)
	require.False(t, hasParams)
	require.NoError(t, ta.VoteKeeper().IterateCoordinatorActions(kv, func(action *types.CoordinatorAction) bool {
		t.Errorf("unexpected stored action %d of type %s", action.ActionId, action.Payload.GetTypeUrl())
		return false
	}))
	require.NoError(t, ta.VoteKeeper().IterateAllRounds(kv, func(round *types.VoteRound) bool {
		t.Errorf("unexpected stored round %x", round.VoteRoundId)
		return false
	}))
}
