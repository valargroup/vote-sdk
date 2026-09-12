package cmd

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	cmtcfg "github.com/cometbft/cometbft/config"
	cmttypes "github.com/cometbft/cometbft/types"
	svrcmd "github.com/cosmos/cosmos-sdk/server/cmd"
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

func TestInitAppConfigEnablesPrometheusTelemetry(t *testing.T) {
	_, rawConfig := initAppConfig()
	config, ok := rawConfig.(CustomAppConfig)
	require.True(t, ok)
	require.True(t, config.Telemetry.Enabled)
	require.Equal(t, int64(60), config.Telemetry.PrometheusRetentionTime)
}

func TestInitCommandSetsBlockLimitAtConfiguredGenesisPath(t *testing.T) {
	home := t.TempDir()
	config := initCometBFTConfig()
	config.SetRoot(home)
	config.Genesis = filepath.Join("config", "custom-genesis.json")
	cmtcfg.EnsureRoot(home)
	cmtcfg.WriteConfigFile(filepath.Join(home, "config", "config.toml"), config)

	root := NewRootCmd()
	root.SetContext(svrcmd.CreateExecuteContext(context.Background()))
	root.SetArgs([]string{"init", "test-node", "--chain-id", "test-chain", "--home", home})
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	require.NoError(t, root.Execute())

	// Check the rendered file that subsequent starts actually read.
	appConfig, err := os.ReadFile(filepath.Join(home, "config", "app.toml"))
	require.NoError(t, err)
	require.Contains(t, string(appConfig), "rpc-read-timeout = 15")

	updated, err := genutiltypes.AppGenesisFromFile(config.GenesisFile())
	require.NoError(t, err)
	require.Equal(t, app.MaxBlockBytes, updated.Consensus.Params.Block.MaxBytes)
	require.NoFileExists(t, filepath.Join(home, "config", "genesis.json"))
}
