package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cosmos/cosmos-sdk/client/flags"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/valargroup/vote-sdk/internal/helper"
)

func TestHelperQueueCmdExportImport(t *testing.T) {
	roundID := strings.Repeat("ab", 32)
	sourceDB := filepath.Join(t.TempDir(), "source.db")
	destDB := filepath.Join(t.TempDir(), "dest.db")
	outPath := filepath.Join(t.TempDir(), "queue.json")
	now := uint64(time.Now().Unix())
	fetcher := func(roundID string) (helper.RoundInfo, error) {
		return helper.RoundInfo{CreatedAtTime: now, VoteEndTime: now + 3600}, nil
	}

	source, err := helper.NewShareStore(sourceDB, fetcher)
	require.NoError(t, err)
	result, err := source.Enqueue(queueCmdTestPayload(roundID, 0))
	require.NoError(t, err)
	require.Equal(t, helper.EnqueueInserted, result)
	require.NoError(t, source.Close())

	output := executeHelperQueueCmd(t,
		"--db-path", sourceDB,
		"export-queue",
		"--round-id", roundID,
		"--out", outPath,
	)
	assert.Contains(t, output, "exported 1 queue rows")

	output = executeHelperQueueCmd(t,
		"--db-path", destDB,
		"import-queue",
		"--in", outPath,
		"--force-ready",
	)
	assert.Contains(t, output, "inserted=1")
	assert.Contains(t, output, "skipped_terminal=0")

	dest, err := helper.NewShareStore(destDB, fetcher)
	require.NoError(t, err)
	defer dest.Close()
	status := dest.Status()
	assert.Equal(t, 1, status[roundID].Pending)
}

func TestHelperQueueCmdUsesHomeDefaultDBPath(t *testing.T) {
	roundID := strings.Repeat("cd", 32)
	homeDir := t.TempDir()
	setViperHome(t, t.TempDir())
	dbPath := filepath.Join(homeDir, "helper.db")
	outPath := filepath.Join(t.TempDir(), "queue.json")
	now := uint64(time.Now().Unix())
	fetcher := func(roundID string) (helper.RoundInfo, error) {
		return helper.RoundInfo{CreatedAtTime: now, VoteEndTime: now + 3600}, nil
	}

	store, err := helper.NewShareStore(dbPath, fetcher)
	require.NoError(t, err)
	result, err := store.Enqueue(queueCmdTestPayload(roundID, 0))
	require.NoError(t, err)
	require.Equal(t, helper.EnqueueInserted, result)
	require.NoError(t, store.Close())

	output := executeHelperQueueCmd(t,
		"--home", homeDir,
		"export-queue",
		"--round-id", roundID,
		"--out", outPath,
	)
	assert.Contains(t, output, "exported 1 queue rows")
}

func TestHelperQueueCmdUsesConfiguredHomeDefaultDBPath(t *testing.T) {
	roundID := strings.Repeat("34", 32)
	homeDir := t.TempDir()
	setViperHome(t, homeDir)
	dbPath := filepath.Join(homeDir, "helper.db")
	outPath := filepath.Join(t.TempDir(), "queue.json")
	now := uint64(time.Now().Unix())
	fetcher := func(roundID string) (helper.RoundInfo, error) {
		return helper.RoundInfo{CreatedAtTime: now, VoteEndTime: now + 3600}, nil
	}

	store, err := helper.NewShareStore(dbPath, fetcher)
	require.NoError(t, err)
	result, err := store.Enqueue(queueCmdTestPayload(roundID, 0))
	require.NoError(t, err)
	require.Equal(t, helper.EnqueueInserted, result)
	require.NoError(t, store.Close())

	output := executeHelperQueueCmd(t,
		"export-queue",
		"--round-id", roundID,
		"--out", outPath,
	)
	assert.Contains(t, output, "exported 1 queue rows")
}

func TestHelperQueueCmdUsesConfiguredDBPath(t *testing.T) {
	roundID := strings.Repeat("56", 32)
	homeDir := t.TempDir()
	dbPath := filepath.Join(t.TempDir(), "configured-helper.db")
	setViperHome(t, homeDir)
	setViperString(t, "helper.db_path", dbPath)
	outPath := filepath.Join(t.TempDir(), "queue.json")
	now := uint64(time.Now().Unix())
	fetcher := func(roundID string) (helper.RoundInfo, error) {
		return helper.RoundInfo{CreatedAtTime: now, VoteEndTime: now + 3600}, nil
	}

	store, err := helper.NewShareStore(dbPath, fetcher)
	require.NoError(t, err)
	result, err := store.Enqueue(queueCmdTestPayload(roundID, 0))
	require.NoError(t, err)
	require.Equal(t, helper.EnqueueInserted, result)
	require.NoError(t, store.Close())

	output := executeHelperQueueCmd(t,
		"export-queue",
		"--round-id", roundID,
		"--out", outPath,
	)
	assert.Contains(t, output, "exported 1 queue rows")
	assert.NoFileExists(t, filepath.Join(homeDir, "helper.db"))
}

func TestHelperQueueCmdReadsDBPathFromAppToml(t *testing.T) {
	roundID := strings.Repeat("78", 32)
	homeDir := t.TempDir()
	configDir := filepath.Join(homeDir, "config")
	require.NoError(t, os.MkdirAll(configDir, 0o755))
	dbPath := filepath.Join(t.TempDir(), "configured-helper.db")
	setViperString(t, "helper.db_path", "")
	outPath := filepath.Join(t.TempDir(), "queue.json")
	now := uint64(time.Now().Unix())
	fetcher := func(roundID string) (helper.RoundInfo, error) {
		return helper.RoundInfo{CreatedAtTime: now, VoteEndTime: now + 3600}, nil
	}

	require.NoError(t, os.WriteFile(
		filepath.Join(configDir, "app.toml"),
		[]byte("[helper]\ndb_path = \""+dbPath+"\"\n"),
		0o600,
	))
	store, err := helper.NewShareStore(dbPath, fetcher)
	require.NoError(t, err)
	result, err := store.Enqueue(queueCmdTestPayload(roundID, 0))
	require.NoError(t, err)
	require.Equal(t, helper.EnqueueInserted, result)
	require.NoError(t, store.Close())

	output := executeHelperQueueCmd(t,
		"--home", homeDir,
		"export-queue",
		"--round-id", roundID,
		"--out", outPath,
	)
	assert.Contains(t, output, "exported 1 queue rows")
	assert.NoFileExists(t, filepath.Join(homeDir, "helper.db"))
}

func TestHelperQueueCmdExportRefusesExistingOutput(t *testing.T) {
	roundID := strings.Repeat("ef", 32)
	sourceDB := filepath.Join(t.TempDir(), "source.db")
	outPath := filepath.Join(t.TempDir(), "queue.json")
	now := uint64(time.Now().Unix())
	fetcher := func(roundID string) (helper.RoundInfo, error) {
		return helper.RoundInfo{CreatedAtTime: now, VoteEndTime: now + 3600}, nil
	}

	source, err := helper.NewShareStore(sourceDB, fetcher)
	require.NoError(t, err)
	result, err := source.Enqueue(queueCmdTestPayload(roundID, 0))
	require.NoError(t, err)
	require.Equal(t, helper.EnqueueInserted, result)
	require.NoError(t, source.Close())

	require.NoError(t, os.WriteFile(outPath, []byte("keep me"), 0o644))
	_, err = executeHelperQueueCmdErr(t,
		"--db-path", sourceDB,
		"export-queue",
		"--round-id", roundID,
		"--out", outPath,
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "export file already exists")
	raw, readErr := os.ReadFile(outPath)
	require.NoError(t, readErr)
	assert.Equal(t, "keep me", string(raw))
}

func TestHelperQueueCmdImportConflictMessage(t *testing.T) {
	roundID := strings.Repeat("12", 32)
	destDB := filepath.Join(t.TempDir(), "dest.db")
	inPath := filepath.Join(t.TempDir(), "queue.json")
	now := uint64(time.Now().Unix())
	fetcher := func(roundID string) (helper.RoundInfo, error) {
		return helper.RoundInfo{CreatedAtTime: now, VoteEndTime: now + 3600}, nil
	}

	store, err := helper.NewShareStore(destDB, fetcher)
	require.NoError(t, err)
	existing := queueCmdTestPayload(roundID, 0)
	result, err := store.Enqueue(existing)
	require.NoError(t, err)
	require.Equal(t, helper.EnqueueInserted, result)
	require.NoError(t, store.Close())

	incoming := existing
	incoming.PrimaryBlind = "AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE="
	export := helper.QueueExport{
		Version: helper.QueueExportVersion,
		RoundID: roundID,
		Round: helper.QueueExportRound{
			CreatedAtTime: now,
			VoteEndTime:   now + 3600,
		},
		Rows: []helper.QueueExportRow{
			{
				ShareIndex:   incoming.EncShare.ShareIndex,
				SharesHash:   incoming.SharesHash,
				ProposalID:   incoming.ProposalID,
				VoteDecision: incoming.VoteDecision,
				EncShare:     incoming.EncShare,
				TreePosition: incoming.TreePosition,
				ShareComms:   incoming.ShareComms,
				PrimaryBlind: incoming.PrimaryBlind,
				State:        helper.ShareStateReceived,
				VoteEndTime:  now + 3600,
				Processable:  true,
			},
		},
	}
	raw, err := json.Marshal(export)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(inPath, raw, 0o600))

	output, err := executeHelperQueueCmdErr(t,
		"--db-path", destDB,
		"import-queue",
		"--in", inPath,
	)
	require.Error(t, err)
	assert.Contains(t, output, "inserted=0")
	assert.Contains(t, output, "conflicts=1")
	assert.Contains(t, err.Error(), "import inserted 0 rows but found 1 conflicting rows")
}

func executeHelperQueueCmd(t *testing.T, args ...string) string {
	t.Helper()
	output, err := executeHelperQueueCmdErr(t, args...)
	require.NoError(t, err)
	return output
}

func executeHelperQueueCmdErr(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := helperQueueCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func setViperHome(t *testing.T, homeDir string) {
	t.Helper()
	setViperString(t, flags.FlagHome, homeDir)
}

func setViperString(t *testing.T, key string, value string) {
	t.Helper()
	previousValue := viper.GetString(key)
	viper.Set(key, value)
	t.Cleanup(func() {
		viper.Set(key, previousValue)
	})
}

func queueCmdTestPayload(roundID string, shareIndex uint32) helper.SharePayload {
	const zeroB64 = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
	comms := make([]string, 16)
	for i := range comms {
		comms[i] = zeroB64
	}
	return helper.SharePayload{
		SharesHash:   zeroB64,
		ProposalID:   1,
		VoteDecision: 0,
		EncShare: helper.EncryptedShareWire{
			C1:         zeroB64,
			C2:         zeroB64,
			ShareIndex: shareIndex,
		},
		TreePosition: uint64(shareIndex),
		VoteRoundID:  roundID,
		ShareComms:   comms,
		PrimaryBlind: zeroB64,
	}
}
