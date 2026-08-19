package app_test

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/stretchr/testify/require"

	abci "github.com/cometbft/cometbft/abci/types"

	voteapi "github.com/valargroup/vote-sdk/api"
	svoteapp "github.com/valargroup/vote-sdk/app"
	"github.com/valargroup/vote-sdk/testutil"
	"github.com/valargroup/vote-sdk/x/vote/types"
)

func TestPrepareProposalDeduplicatesRevealsAndPreservesOrder(t *testing.T) {
	ta := testutil.SetupTestApp(t)
	roundA := bytes.Repeat([]byte{0xA1}, types.RoundIDLen)
	roundB := bytes.Repeat([]byte{0xB2}, types.RoundIDLen)
	nonRevealA := []byte{0xAA, 0x01}
	nonRevealB := []byte{0xBB, 0x02}
	first := mustRevealTx(t, roundA, 1, 0x11)
	duplicateVariant := mustRevealTx(t, roundA, 1, 0x22)
	sameNullifierOtherRound := mustRevealTx(t, roundB, 1, 0x33)
	differentNullifier := mustRevealTx(t, roundA, 2, 0x44)

	resp := ta.CallPrepareProposalWithTxs([][]byte{
		nonRevealA,
		first,
		duplicateVariant,
		sameNullifierOtherRound,
		differentNullifier,
		nonRevealB,
	})

	require.Equal(t, [][]byte{
		nonRevealA,
		first,
		sameNullifierOtherRound,
		differentNullifier,
		nonRevealB,
	}, resp.Txs)
	require.Equal(t, abci.ResponseProcessProposal_ACCEPT, ta.CallProcessProposal(resp.Txs).Status)
}

func TestPrepareProposalDropsMalformedRevealThatProcessProposalRejects(t *testing.T) {
	ta := testutil.SetupTestApp(t)
	malformed := []byte{voteapi.TagRevealShare}
	nonReveal := []byte{0xAA, 0x01}

	resp := ta.CallPrepareProposalWithTxs([][]byte{malformed, nonReveal})
	require.Equal(t, [][]byte{nonReveal}, resp.Txs)
	require.Equal(t, abci.ResponseProcessProposal_REJECT, ta.CallProcessProposal([][]byte{malformed}).Status)
}

func TestRevealProposalCapIsEnforcedByBothHandlers(t *testing.T) {
	ta := testutil.SetupTestApp(t)
	roundID := bytes.Repeat([]byte{0xC3}, types.RoundIDLen)
	txs := make([][]byte, svoteapp.MaxRevealSharesPerBlock+1)
	for i := range txs {
		txs[i] = mustRevealTx(t, roundID, uint64(i+1), byte(i))
	}

	resp := ta.CallPrepareProposalWithTxs(txs)
	require.Len(t, resp.Txs, svoteapp.MaxRevealSharesPerBlock)
	require.Equal(t, txs[:svoteapp.MaxRevealSharesPerBlock], resp.Txs)
	require.Equal(t, abci.ResponseProcessProposal_ACCEPT, ta.CallProcessProposal(resp.Txs).Status)
	require.Equal(t, abci.ResponseProcessProposal_REJECT, ta.CallProcessProposal(txs).Status)
}

func TestPrepareProposalBoundsFiveCopiesOfEightHundredShares(t *testing.T) {
	ta := testutil.SetupTestApp(t)
	roundID := bytes.Repeat([]byte{0xD4}, types.RoundIDLen)
	txs := make([][]byte, 0, 800*5)
	for share := uint64(1); share <= 800; share++ {
		for copyIndex := byte(0); copyIndex < 5; copyIndex++ {
			txs = append(txs, mustRevealTx(t, roundID, share, copyIndex))
		}
	}

	resp := ta.CallPrepareProposalWithTxs(txs)
	require.Len(t, resp.Txs, svoteapp.MaxRevealSharesPerBlock)
	require.Equal(t, abci.ResponseProcessProposal_ACCEPT, ta.CallProcessProposal(resp.Txs).Status)

	for i, txBytes := range resp.Txs {
		require.Equal(t, mustRevealTx(t, roundID, uint64(i+1), 0), txBytes)
	}
}

func TestProcessProposalRejectsDuplicateRevealVariant(t *testing.T) {
	ta := testutil.SetupTestApp(t)
	roundID := bytes.Repeat([]byte{0xE5}, types.RoundIDLen)
	first := mustRevealTx(t, roundID, 7, 0x11)
	variant := mustRevealTx(t, roundID, 7, 0x22)

	require.Equal(t, abci.ResponseProcessProposal_REJECT, ta.CallProcessProposal([][]byte{first, variant}).Status)
}

func mustRevealTx(t *testing.T, roundID []byte, nullifierID uint64, proofByte byte) []byte {
	t.Helper()
	nullifier := make([]byte, 32)
	binary.BigEndian.PutUint64(nullifier[24:], nullifierID)
	txBytes, err := voteapi.EncodeVoteTx(&types.MsgRevealShare{
		VoteRoundId:              append([]byte(nil), roundID...),
		ShareNullifier:           nullifier,
		EncShare:                 bytes.Repeat([]byte{0x42}, 64),
		ProposalId:               1,
		VoteDecision:             1,
		Proof:                    []byte{proofByte},
		VoteCommTreeAnchorHeight: 1,
	})
	require.NoError(t, err)
	return txBytes
}
