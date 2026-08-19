package app

import (
	"testing"

	cmttypes "github.com/cometbft/cometbft/types"
	"github.com/stretchr/testify/require"
)

func TestTrimProposalToMaxTxBytes(t *testing.T) {
	txs := [][]byte{
		make([]byte, 100),
		make([]byte, 200),
	}
	firstSize := cmttypes.ComputeProtoSizeForTxs([]cmttypes.Tx{txs[0]})
	totalSize := cmttypes.ComputeProtoSizeForTxs([]cmttypes.Tx{txs[0], txs[1]})

	trimmed, dropped := trimProposalToMaxTxBytes(txs, totalSize)
	require.Equal(t, txs, trimmed)
	require.Zero(t, dropped)

	trimmed, dropped = trimProposalToMaxTxBytes(txs, totalSize-1)
	require.Equal(t, txs[:1], trimmed)
	require.Equal(t, 1, dropped)

	trimmed, dropped = trimProposalToMaxTxBytes(txs, firstSize-1)
	require.Empty(t, trimmed)
	require.Equal(t, 2, dropped)

	trimmed, dropped = trimProposalToMaxTxBytes(txs, 0)
	require.Empty(t, trimmed)
	require.Equal(t, 2, dropped)

	trimmed, dropped = trimProposalToMaxTxBytes(txs, -1)
	require.Equal(t, txs, trimmed)
	require.Zero(t, dropped)
}
