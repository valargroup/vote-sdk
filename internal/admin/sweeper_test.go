package admin

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"cosmossdk.io/log"
	_ "modernc.org/sqlite"
)

func TestRunPendingSweeper_DeletesExpiredRows(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "sweep.db")
	st, err := NewStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	past := time.Now().Unix() - 500
	if err := st.UpsertPendingRegistration(PendingRegistration{
		OperatorAddress: "sv1sweep",
		URL:             "https://s.example",
		Moniker:         "s",
		RequestedAt:     past,
		ExpiresAt:       past + 1,
	}); err != nil {
		t.Fatal(err)
	}

	a := &Admin{
		configURL:   "http://invalid.local",
		logger:      log.NewNopLogger(),
		store:       st,
		checkBonded: func(string) bool { return false },
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go RunPendingSweeper(ctx, a, 25*time.Millisecond, log.NewNopLogger())

	raw, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()

	deadline := time.After(3 * time.Second)
	for {
		var n int
		if err := raw.QueryRow(`SELECT COUNT(*) FROM pending_registrations`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			return
		}
		select {
		case <-deadline:
			t.Fatal("sweeper did not delete expired row in time")
		case <-time.After(15 * time.Millisecond):
		}
	}
}
