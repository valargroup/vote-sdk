package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cosmossdk.io/log"
	"github.com/gorilla/mux"
)

func newHealthTestAdmin(servers []ServiceEntry, client *http.Client) *Admin {
	if client == nil {
		client = &http.Client{Timeout: time.Second}
	}
	return &Admin{
		logger:           log.NewNopLogger(),
		cached:           &VotingConfig{VoteServers: servers},
		healthClient:     client,
		voteServerHealth: make(map[string]VoteServerHealth),
	}
}

func latestBlockHandler(height string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"block": map[string]interface{}{
				"header": map[string]string{"height": height},
			},
		})
	}
}

func TestProbeVoteServersHealthy(t *testing.T) {
	t.Parallel()

	ts := httptest.NewServer(latestBlockHandler("123"))
	defer ts.Close()

	a := newHealthTestAdmin([]ServiceEntry{{URL: ts.URL, Label: "primary"}}, nil)
	if err := a.ProbeVoteServers(context.Background()); err != nil {
		t.Fatal(err)
	}

	rows, err := a.GetVoteServerHealth()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("want 1 row, got %d", len(rows))
	}
	row := rows[0]
	if row.State != VoteServerHealthUp {
		t.Fatalf("state: got %q want %q; error=%q", row.State, VoteServerHealthUp, row.Error)
	}
	if row.LastCheckedAt == 0 || row.LastSuccessAt == 0 {
		t.Fatalf("timestamps not set: %+v", row)
	}
	if row.Height == nil || *row.Height != 123 {
		t.Fatalf("height: %+v", row.Height)
	}
	if row.StatusCode == nil || *row.StatusCode != http.StatusOK {
		t.Fatalf("status: %+v", row.StatusCode)
	}
}

func TestProbeVoteServersNon200IsDown(t *testing.T) {
	t.Parallel()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer ts.Close()

	a := newHealthTestAdmin([]ServiceEntry{{URL: ts.URL, Label: "secondary"}}, nil)
	if err := a.ProbeVoteServers(context.Background()); err != nil {
		t.Fatal(err)
	}

	rows, err := a.GetVoteServerHealth()
	if err != nil {
		t.Fatal(err)
	}
	row := rows[0]
	if row.State != VoteServerHealthDown {
		t.Fatalf("state: got %q want %q", row.State, VoteServerHealthDown)
	}
	if row.Error != "HTTP 503" {
		t.Fatalf("error: got %q", row.Error)
	}
	if row.StatusCode == nil || *row.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status: %+v", row.StatusCode)
	}
	if row.LastSuccessAt != 0 {
		t.Fatalf("last_success_at should be unset, got %d", row.LastSuccessAt)
	}
}

func TestProbeVoteServersTimeoutIsDown(t *testing.T) {
	t.Parallel()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(100 * time.Millisecond)
		_, _ = w.Write([]byte(`{"block":{"header":{"height":"1"}}}`))
	}))
	defer ts.Close()

	a := newHealthTestAdmin(
		[]ServiceEntry{{URL: ts.URL, Label: "slow"}},
		&http.Client{Timeout: 10 * time.Millisecond},
	)
	if err := a.ProbeVoteServers(context.Background()); err != nil {
		t.Fatal(err)
	}

	rows, err := a.GetVoteServerHealth()
	if err != nil {
		t.Fatal(err)
	}
	row := rows[0]
	if row.State != VoteServerHealthDown {
		t.Fatalf("state: got %q want %q", row.State, VoteServerHealthDown)
	}
	if row.Error == "" {
		t.Fatalf("expected timeout error")
	}
	if row.LastSuccessAt != 0 {
		t.Fatalf("last_success_at should be unset, got %d", row.LastSuccessAt)
	}
}

func TestProbeVoteServersLargeLatestBlockBodyIsUp(t *testing.T) {
	t.Parallel()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"block":{"header":{"height":"456"},"data":{"txs":["`))
		_, _ = w.Write([]byte(strings.Repeat("a", 2<<20)))
		_, _ = w.Write([]byte(`"]}}}`))
	}))
	defer ts.Close()

	a := newHealthTestAdmin([]ServiceEntry{{URL: ts.URL, Label: "large"}}, nil)
	if err := a.ProbeVoteServers(context.Background()); err != nil {
		t.Fatal(err)
	}

	rows, err := a.GetVoteServerHealth()
	if err != nil {
		t.Fatal(err)
	}
	row := rows[0]
	if row.State != VoteServerHealthUp {
		t.Fatalf("state: got %q want %q; error=%q", row.State, VoteServerHealthUp, row.Error)
	}
	if row.Height == nil || *row.Height != 456 {
		t.Fatalf("height: %+v", row.Height)
	}
}

func TestProbeVoteServersHTTP200WithInvalidHeightIsUp(t *testing.T) {
	t.Parallel()

	ts := httptest.NewServer(latestBlockHandler("invalid"))
	defer ts.Close()

	a := newHealthTestAdmin([]ServiceEntry{{URL: ts.URL, Label: "no-height"}}, nil)
	if err := a.ProbeVoteServers(context.Background()); err != nil {
		t.Fatal(err)
	}

	rows, err := a.GetVoteServerHealth()
	if err != nil {
		t.Fatal(err)
	}
	row := rows[0]
	if row.State != VoteServerHealthUp {
		t.Fatalf("state: got %q want %q; error=%q", row.State, VoteServerHealthUp, row.Error)
	}
	if row.Height != nil {
		t.Fatalf("height should be optional, got %d", *row.Height)
	}
	if row.Error != "" {
		t.Fatalf("error should be clear on HTTP 200, got %q", row.Error)
	}
	if row.LastSuccessAt == 0 {
		t.Fatalf("expected success timestamp")
	}
}

func TestProbeVoteServersPreservesLastSuccessOnFailure(t *testing.T) {
	t.Parallel()

	healthy := true
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !healthy {
			http.Error(w, "offline", http.StatusBadGateway)
			return
		}
		latestBlockHandler("77")(w, r)
	}))
	defer ts.Close()

	a := newHealthTestAdmin([]ServiceEntry{{URL: ts.URL, Label: "primary"}}, nil)
	if err := a.ProbeVoteServers(context.Background()); err != nil {
		t.Fatal(err)
	}
	rows, err := a.GetVoteServerHealth()
	if err != nil {
		t.Fatal(err)
	}
	firstSuccess := rows[0].LastSuccessAt
	if firstSuccess == 0 {
		t.Fatalf("expected first success timestamp")
	}

	healthy = false
	if err := a.ProbeVoteServers(context.Background()); err != nil {
		t.Fatal(err)
	}
	rows, err = a.GetVoteServerHealth()
	if err != nil {
		t.Fatal(err)
	}
	row := rows[0]
	if row.State != VoteServerHealthDown {
		t.Fatalf("state: got %q want %q", row.State, VoteServerHealthDown)
	}
	if row.LastSuccessAt != firstSuccess {
		t.Fatalf("last_success_at changed: got %d want %d", row.LastSuccessAt, firstSuccess)
	}
}

func TestVoteServerHealthFollowsCurrentVotingConfig(t *testing.T) {
	t.Parallel()

	one := httptest.NewServer(latestBlockHandler("1"))
	defer one.Close()
	two := httptest.NewServer(latestBlockHandler("2"))
	defer two.Close()

	a := newHealthTestAdmin([]ServiceEntry{
		{URL: one.URL, Label: "one"},
		{URL: two.URL, Label: "two"},
	}, nil)
	if err := a.ProbeVoteServers(context.Background()); err != nil {
		t.Fatal(err)
	}

	a.mu.Lock()
	a.cached = &VotingConfig{VoteServers: []ServiceEntry{{URL: one.URL, Label: "one"}}}
	a.mu.Unlock()

	if err := a.ProbeVoteServers(context.Background()); err != nil {
		t.Fatal(err)
	}
	rows, err := a.GetVoteServerHealth()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].URL != one.URL {
		t.Fatalf("health rows did not follow config: %+v", rows)
	}
}

func TestHandleGetVoteServerHealthAdminNil(t *testing.T) {
	t.Parallel()

	r := mux.NewRouter()
	RegisterRoutes(r, func() *Admin { return nil }, log.NewNopLogger())

	req := httptest.NewRequest(http.MethodGet, "/api/vote-server-health", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503, got %d: %s", w.Code, w.Body.String())
	}
}
