package cmd

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	cmttypes "github.com/cometbft/cometbft/types"
	"github.com/cosmos/cosmos-sdk/x/genutil"
	genutiltypes "github.com/cosmos/cosmos-sdk/x/genutil/types"

	"github.com/valargroup/vote-sdk/app"
)

func TestSetGenesisBlockLimit(t *testing.T) {
	genesisPath := filepath.Join(t.TempDir(), "genesis.json")
	genesis := genutiltypes.NewAppGenesisWithVersion("test-chain", json.RawMessage(`{}`))
	genesis.Consensus.Params = cmttypes.DefaultConsensusParams()
	require.NoError(t, genutil.ExportGenesisFile(genesis, genesisPath))

	require.NoError(t, setGenesisBlockLimit(genesisPath))

	updated, err := genutiltypes.AppGenesisFromFile(genesisPath)
	require.NoError(t, err)
	require.Equal(t, app.MaxBlockBytes, updated.Consensus.Params.Block.MaxBytes)
	require.Equal(t, genesis.Consensus.Params.Block.MaxGas, updated.Consensus.Params.Block.MaxGas)
}
