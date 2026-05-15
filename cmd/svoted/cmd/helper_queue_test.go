package cmd

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

func executeHelperQueueCmd(t *testing.T, args ...string) string {
	t.Helper()
	cmd := helperQueueCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	require.NoError(t, cmd.Execute())
	return out.String()
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
