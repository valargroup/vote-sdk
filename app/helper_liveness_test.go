package app

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestLocalNodeReadyForHelper_HealthyWithinThreeMinutes(t *testing.T) {
	latest := time.Now().Add(-2 * time.Minute).UTC().Format(time.RFC3339Nano)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/status", r.URL.Path)
		_, _ = fmt.Fprintf(w, `{"result":{"sync_info":{"latest_block_time":"%s","catching_up":false}}}`, latest)
	}))
	t.Cleanup(server.Close)

	app := &SvoteApp{cometRPC: server.URL}
	healthy, err := app.localNodeReadyForHelper(helperMaxBlockStaleness)
	require.NoError(t, err)
	require.True(t, healthy)
}

func TestLocalNodeReadyForHelper_StaleAfterThreeMinutes(t *testing.T) {
	latest := time.Now().Add(-(helperMaxBlockStaleness + time.Second)).UTC().Format(time.RFC3339Nano)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/status", r.URL.Path)
		_, _ = fmt.Fprintf(w, `{"result":{"sync_info":{"latest_block_time":"%s"}}}`, latest)
	}))
	t.Cleanup(server.Close)

	app := &SvoteApp{cometRPC: server.URL}
	healthy, err := app.localNodeReadyForHelper(helperMaxBlockStaleness)
	require.NoError(t, err)
	require.False(t, healthy)
}

func TestLocalNodeReadyForHelper_CatchingUp(t *testing.T) {
	latest := time.Now().UTC().Format(time.RFC3339Nano)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/status", r.URL.Path)
		_, _ = fmt.Fprintf(w, `{"result":{"sync_info":{"latest_block_time":"%s","catching_up":true}}}`, latest)
	}))
	t.Cleanup(server.Close)

	app := &SvoteApp{cometRPC: server.URL}
	healthy, err := app.localNodeReadyForHelper(helperMaxBlockStaleness)
	require.NoError(t, err)
	require.False(t, healthy)
}

func TestLocalNodeReadyForHelper_StatusRPCFailure(t *testing.T) {
	app := &SvoteApp{cometRPC: "http://127.0.0.1:1"}
	healthy, err := app.localNodeReadyForHelper(helperMaxBlockStaleness)
	require.Error(t, err)
	require.False(t, healthy)
}
