package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"cosmossdk.io/log"
)

func uint64Ptr(v uint64) *uint64 { return &v }

func newTestLogger() log.Logger {
	return log.NewNopLogger()
}

// newVoteServer returns an httptest.Server that responds 200 on both
// /shielded-vote/v1/rounds and /shielded-vote/v1/status.
func newVoteServer(t *testing.T, roundsStatus, helperStatus int) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/shielded-vote/v1/rounds", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(roundsStatus)
		w.Write([]byte(`{"rounds":[]}`))
	})
	mux.HandleFunc("/shielded-vote/v1/status", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(helperStatus)
		w.Write([]byte(`{"status":"ok"}`))
	})
	return httptest.NewServer(mux)
}

// newPIRServer returns an httptest.Server that responds on /ready and /root.
func newPIRServer(t *testing.T, readyStatus int, rootHeight *uint64) *httptest.Server {
	t.Helper()
	return newPIRServerWithRoots(t, readyStatus, rootHeight, "aaa29", "aaa25")
}

func newPIRServerWithRoots(t *testing.T, readyStatus int, rootHeight *uint64, root29, root25 string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/ready", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(readyStatus)
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})
	mux.HandleFunc("/root", func(w http.ResponseWriter, _ *http.Request) {
		if readyStatus != http.StatusOK {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		resp := pirRootResponse{Root29: root29, Root25: root25, Height: rootHeight}
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(resp)
	})
	return httptest.NewServer(mux)
}

func TestCheckFleet_AllHealthy(t *testing.T) {
	vs := newVoteServer(t, 200, 200)
	defer vs.Close()
	pir := newPIRServer(t, 200, uint64Ptr(100))
	defer pir.Close()

	cfg := &VotingConfig{
		VoteServers:    []ServiceEntry{{URL: vs.URL, Label: "test-vote"}},
		PIRServers:     []ServiceEntry{{URL: pir.URL, Label: "test-pir"}},
		SnapshotHeight: uint64Ptr(100),
	}

	alerted := map[checkKey]bool{}
	client := &http.Client{}
	checkFleet(context.Background(), client, cfg, alerted, newTestLogger())

	if len(alerted) != 0 {
		t.Errorf("expected no alerts, got %d: %v", len(alerted), alerted)
	}
}

func TestCheckFleet_VoteRoundsDown(t *testing.T) {
	vs := newVoteServer(t, 503, 200)
	defer vs.Close()

	cfg := &VotingConfig{
		VoteServers: []ServiceEntry{{URL: vs.URL, Label: "test-vote"}},
	}

	alerted := map[checkKey]bool{}
	client := &http.Client{}
	checkFleet(context.Background(), client, cfg, alerted, newTestLogger())

	key := checkKey{url: vs.URL, check: "vote_rounds"}
	if !alerted[key] {
		t.Error("expected vote_rounds alert")
	}
	// Helper should still be healthy.
	helperKey := checkKey{url: vs.URL, check: "vote_helper"}
	if alerted[helperKey] {
		t.Error("did not expect vote_helper alert")
	}
}

func TestCheckFleet_HelperDown(t *testing.T) {
	vs := newVoteServer(t, 200, 503)
	defer vs.Close()

	cfg := &VotingConfig{
		VoteServers: []ServiceEntry{{URL: vs.URL, Label: "test-vote"}},
	}

	alerted := map[checkKey]bool{}
	client := &http.Client{}
	checkFleet(context.Background(), client, cfg, alerted, newTestLogger())

	key := checkKey{url: vs.URL, check: "vote_helper"}
	if !alerted[key] {
		t.Error("expected vote_helper alert")
	}
}

func TestCheckFleet_PIRNotReady(t *testing.T) {
	pir := newPIRServer(t, 503, nil)
	defer pir.Close()

	cfg := &VotingConfig{
		PIRServers:     []ServiceEntry{{URL: pir.URL, Label: "test-pir"}},
		SnapshotHeight: uint64Ptr(100),
	}

	alerted := map[checkKey]bool{}
	client := &http.Client{}
	checkFleet(context.Background(), client, cfg, alerted, newTestLogger())

	key := checkKey{url: pir.URL, check: "pir_ready"}
	if !alerted[key] {
		t.Error("expected pir_ready alert")
	}
}

func TestCheckFleet_PIRHeightMismatch(t *testing.T) {
	pir := newPIRServer(t, 200, uint64Ptr(90))
	defer pir.Close()

	cfg := &VotingConfig{
		PIRServers:     []ServiceEntry{{URL: pir.URL, Label: "test-pir"}},
		SnapshotHeight: uint64Ptr(100),
	}

	alerted := map[checkKey]bool{}
	client := &http.Client{}
	checkFleet(context.Background(), client, cfg, alerted, newTestLogger())

	key := checkKey{url: pir.URL, check: "pir_height_mismatch"}
	if !alerted[key] {
		t.Error("expected pir_height_mismatch alert")
	}
}

func TestCheckFleet_PIRHeightNil(t *testing.T) {
	pir := newPIRServer(t, 200, nil)
	defer pir.Close()

	cfg := &VotingConfig{
		PIRServers:     []ServiceEntry{{URL: pir.URL, Label: "test-pir"}},
		SnapshotHeight: uint64Ptr(100),
	}

	alerted := map[checkKey]bool{}
	client := &http.Client{}
	checkFleet(context.Background(), client, cfg, alerted, newTestLogger())

	key := checkKey{url: pir.URL, check: "pir_height_mismatch"}
	if !alerted[key] {
		t.Error("expected pir_height_mismatch alert when height is nil")
	}
}

func TestCheckFleet_NoDoubleAlert(t *testing.T) {
	vs := newVoteServer(t, 503, 200)
	defer vs.Close()

	cfg := &VotingConfig{
		VoteServers: []ServiceEntry{{URL: vs.URL, Label: "test-vote"}},
	}

	alerted := map[checkKey]bool{}
	client := &http.Client{}

	// First tick — should alert.
	checkFleet(context.Background(), client, cfg, alerted, newTestLogger())
	key := checkKey{url: vs.URL, check: "vote_rounds"}
	if !alerted[key] {
		t.Fatal("expected alert after first tick")
	}

	// Second tick — should NOT re-alert (key already in map).
	// The alerted map entry persists, confirming dedup.
	checkFleet(context.Background(), client, cfg, alerted, newTestLogger())
	if !alerted[key] {
		t.Error("alert state should persist across ticks")
	}
}

func TestCheckFleet_Recovery(t *testing.T) {
	// Start with a failing server.
	failVS := newVoteServer(t, 503, 200)

	cfg := &VotingConfig{
		VoteServers: []ServiceEntry{{URL: failVS.URL, Label: "test-vote"}},
	}

	alerted := map[checkKey]bool{}
	client := &http.Client{}

	checkFleet(context.Background(), client, cfg, alerted, newTestLogger())
	key := checkKey{url: failVS.URL, check: "vote_rounds"}
	if !alerted[key] {
		t.Fatal("expected alert")
	}
	failVS.Close()

	// Replace with a healthy server at the same URL is not possible with
	// httptest, so we simulate by creating a new healthy server and
	// updating the config entry.
	okVS := newVoteServer(t, 200, 200)
	defer okVS.Close()

	// Move the alert key to the new URL to simulate the same endpoint recovering.
	delete(alerted, key)
	newKey := checkKey{url: okVS.URL, check: "vote_rounds"}
	alerted[newKey] = true

	cfg.VoteServers = []ServiceEntry{{URL: okVS.URL, Label: "test-vote"}}
	checkFleet(context.Background(), client, cfg, alerted, newTestLogger())

	if alerted[newKey] {
		t.Error("expected recovery to clear the alert")
	}
}

func TestCheckFleet_NoHeightCheckWithoutCanonical(t *testing.T) {
	pir := newPIRServer(t, 200, uint64Ptr(90))
	defer pir.Close()

	cfg := &VotingConfig{
		PIRServers:     []ServiceEntry{{URL: pir.URL, Label: "test-pir"}},
		SnapshotHeight: nil,
	}

	alerted := map[checkKey]bool{}
	client := &http.Client{}
	checkFleet(context.Background(), client, cfg, alerted, newTestLogger())

	key := checkKey{url: pir.URL, check: "pir_height_mismatch"}
	if alerted[key] {
		t.Error("should not check height when snapshot_height is nil in config")
	}
}

func TestCheckFleet_PIRRootConsistencyMatch(t *testing.T) {
	pir1 := newPIRServerWithRoots(t, 200, uint64Ptr(100), "abc29", "abc25")
	defer pir1.Close()
	pir2 := newPIRServerWithRoots(t, 200, uint64Ptr(100), "abc29", "abc25")
	defer pir2.Close()

	cfg := &VotingConfig{
		PIRServers: []ServiceEntry{
			{URL: pir1.URL, Label: "pir-primary"},
			{URL: pir2.URL, Label: "pir-backup"},
		},
		SnapshotHeight: uint64Ptr(100),
	}

	alerted := map[checkKey]bool{}
	client := &http.Client{}
	checkFleet(context.Background(), client, cfg, alerted, newTestLogger())

	for k := range alerted {
		if k.check == "pir_root_mismatch" {
			t.Errorf("did not expect pir_root_mismatch alert, got key %+v", k)
		}
	}
}

func TestCheckFleet_PIRRootConsistencyMismatch(t *testing.T) {
	pir1 := newPIRServerWithRoots(t, 200, uint64Ptr(100), "abc29", "abc25")
	defer pir1.Close()
	pir2 := newPIRServerWithRoots(t, 200, uint64Ptr(100), "def29", "def25")
	defer pir2.Close()

	cfg := &VotingConfig{
		PIRServers: []ServiceEntry{
			{URL: pir1.URL, Label: "pir-primary"},
			{URL: pir2.URL, Label: "pir-backup"},
		},
		SnapshotHeight: uint64Ptr(100),
	}

	alerted := map[checkKey]bool{}
	client := &http.Client{}
	checkFleet(context.Background(), client, cfg, alerted, newTestLogger())

	key := checkKey{url: pir2.URL, check: "pir_root_mismatch"}
	if !alerted[key] {
		t.Error("expected pir_root_mismatch alert on the divergent replica")
	}
}

func TestCheckFleet_PIRRootConsistencyRecovery(t *testing.T) {
	pir1 := newPIRServerWithRoots(t, 200, uint64Ptr(100), "abc29", "abc25")
	defer pir1.Close()
	pir2 := newPIRServerWithRoots(t, 200, uint64Ptr(100), "abc29", "abc25")
	defer pir2.Close()

	// Pre-seed an old mismatch alert.
	alerted := map[checkKey]bool{
		{url: pir2.URL, check: "pir_root_mismatch"}: true,
	}

	cfg := &VotingConfig{
		PIRServers: []ServiceEntry{
			{URL: pir1.URL, Label: "pir-primary"},
			{URL: pir2.URL, Label: "pir-backup"},
		},
		SnapshotHeight: uint64Ptr(100),
	}

	client := &http.Client{}
	checkFleet(context.Background(), client, cfg, alerted, newTestLogger())

	key := checkKey{url: pir2.URL, check: "pir_root_mismatch"}
	if alerted[key] {
		t.Error("expected pir_root_mismatch to be cleared after roots converge")
	}
}
