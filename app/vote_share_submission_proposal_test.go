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

func TestPrepareProposalDeduplicatesVoteShareSubmissionsAndPreservesOrder(t *testing.T) {
	ta := testutil.SetupTestApp(t)
	roundA := bytes.Repeat([]byte{0xA1}, types.RoundIDLen)
	roundB := bytes.Repeat([]byte{0xB2}, types.RoundIDLen)
	nonSubmissionA := []byte{0xAA, 0x01}
	nonSubmissionB := []byte{0xBB, 0x02}
	first := mustVoteShareSubmissionTx(t, roundA, 1, 0x11)
	duplicateVariant := mustVoteShareSubmissionTx(t, roundA, 1, 0x22)
	sameNullifierOtherRound := mustVoteShareSubmissionTx(t, roundB, 1, 0x33)
	differentNullifier := mustVoteShareSubmissionTx(t, roundA, 2, 0x44)

	resp := ta.CallPrepareProposalWithTxs([][]byte{
		nonSubmissionA,
		first,
		duplicateVariant,
		sameNullifierOtherRound,
		differentNullifier,
		nonSubmissionB,
	})

	require.Equal(t, [][]byte{
		nonSubmissionA,
		first,
		sameNullifierOtherRound,
		differentNullifier,
		nonSubmissionB,
	}, resp.Txs)
	require.Equal(t, abci.ResponseProcessProposal_ACCEPT, ta.CallProcessProposal(resp.Txs).Status)
}

func TestPrepareProposalDropsMalformedVoteShareSubmissionThatProcessProposalRejects(t *testing.T) {
	ta := testutil.SetupTestApp(t)
	malformed := []byte{voteapi.TagRevealShare}
	nonSubmission := []byte{0xAA, 0x01}

	resp := ta.CallPrepareProposalWithTxs([][]byte{malformed, nonSubmission})
	require.Equal(t, [][]byte{nonSubmission}, resp.Txs)
	require.Equal(t, abci.ResponseProcessProposal_REJECT, ta.CallProcessProposal([][]byte{malformed}).Status)
}

func TestVoteShareSubmissionProposalCapIsEnforcedByBothHandlers(t *testing.T) {
	ta := testutil.SetupTestApp(t)
	roundID := bytes.Repeat([]byte{0xC3}, types.RoundIDLen)
	txs := make([][]byte, svoteapp.MaxVoteShareSubmissionsPerBlock+1)
	for i := range txs {
		txs[i] = mustVoteShareSubmissionTx(t, roundID, uint64(i+1), byte(i))
	}

	resp := ta.CallPrepareProposalWithTxs(txs)
	require.Len(t, resp.Txs, svoteapp.MaxVoteShareSubmissionsPerBlock)
	require.Equal(t, txs[:svoteapp.MaxVoteShareSubmissionsPerBlock], resp.Txs)
	require.Equal(t, abci.ResponseProcessProposal_ACCEPT, ta.CallProcessProposal(resp.Txs).Status)
	require.Equal(t, abci.ResponseProcessProposal_REJECT, ta.CallProcessProposal(txs).Status)
}

func TestPrepareProposalBoundsFiveCopiesOfEightHundredVoteShareSubmissions(t *testing.T) {
	ta := testutil.SetupTestApp(t)
	roundID := bytes.Repeat([]byte{0xD4}, types.RoundIDLen)
	txs := make([][]byte, 0, 800*5)
	for share := uint64(1); share <= 800; share++ {
		for copyIndex := byte(0); copyIndex < 5; copyIndex++ {
			txs = append(txs, mustVoteShareSubmissionTx(t, roundID, share, copyIndex))
		}
	}

	resp := ta.CallPrepareProposalWithTxs(txs)
	require.Len(t, resp.Txs, svoteapp.MaxVoteShareSubmissionsPerBlock)
	require.Equal(t, abci.ResponseProcessProposal_ACCEPT, ta.CallProcessProposal(resp.Txs).Status)

	for i, txBytes := range resp.Txs {
		require.Equal(t, mustVoteShareSubmissionTx(t, roundID, uint64(i+1), 0), txBytes)
	}
}

func TestProcessProposalRejectsDuplicateVoteShareSubmissionVariant(t *testing.T) {
	ta := testutil.SetupTestApp(t)
	roundID := bytes.Repeat([]byte{0xE5}, types.RoundIDLen)
	first := mustVoteShareSubmissionTx(t, roundID, 7, 0x11)
	variant := mustVoteShareSubmissionTx(t, roundID, 7, 0x22)

	require.Equal(t, abci.ResponseProcessProposal_REJECT, ta.CallProcessProposal([][]byte{first, variant}).Status)
}

func mustVoteShareSubmissionTx(t *testing.T, roundID []byte, nullifierID uint64, proofByte byte) []byte {
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
