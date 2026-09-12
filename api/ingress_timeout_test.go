package api

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cosmossdk.io/log"
	"github.com/cosmos/cosmos-sdk/client"
	sdkapi "github.com/cosmos/cosmos-sdk/server/api"
	"github.com/cosmos/cosmos-sdk/server/config"
	"github.com/gorilla/mux"
	"github.com/stretchr/testify/require"
)

type failingRequestBody struct{ err error }

func (r failingRequestBody) Read([]byte) (int, error) { return 0, r.err }
func (failingRequestBody) Close() error               { return nil }

func TestIngressTimeoutNeverBroadcasts(t *testing.T) {
	var broadcasts atomic.Int64
	comet := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { broadcasts.Add(1) }))
	defer comet.Close()
	handler := NewHandler(HandlerConfig{CometRPCEndpoint: comet.URL})
	router := mux.NewRouter()
	handler.RegisterTxRoutes(router)
	token := strings.Repeat("a", 64)
	cases := []struct {
		name    string
		tokens  []string
		readErr error
		status  int
	}{
		{"supported", []string{token}, os.ErrDeadlineExceeded, 408},
		{"missing", nil, os.ErrDeadlineExceeded, 400},
		{"uppercase", []string{strings.Repeat("A", 64)}, os.ErrDeadlineExceeded, 400},
		{"short", []string{"bad"}, os.ErrDeadlineExceeded, 400},
		{"invalid_hex", []string{strings.Repeat("z", 64)}, os.ErrDeadlineExceeded, 400},
		{"duplicate", []string{token, token}, os.ErrDeadlineExceeded, 400},
		{"non_timeout", []string{token}, io.ErrUnexpectedEOF, 400},
	}
	for _, route := range []string{"delegate-vote", "cast-vote", "cast-vote-batch", "delegate-and-cast-vote-batch", "reveal-share"} {
		for _, tc := range cases {
			t.Run(route+"/"+tc.name, func(t *testing.T) {
				req := httptest.NewRequest("POST", "/shielded-vote/v1/"+route, nil)
				req.Body = failingRequestBody{err: tc.readErr}
				for _, value := range tc.tokens {
					req.Header.Add("X-Vote-Ingress-Attempt-V1", value)
				}
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, req)
				require.Equal(t, tc.status, rec.Code)
				if tc.status == 408 {
					require.JSONEq(t, fmt.Sprintf(`{"error":{"version":1,"code":"request_body_timeout","dispatch":"not_started","attempt":%q}}`, token), rec.Body.String())
					require.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
					require.Equal(t, "application/json", rec.Header().Get("Content-Type"))
				} else {
					require.NotContains(t, rec.Body.String(), "not_started")
				}
				require.Zero(t, broadcasts.Load())
			})
		}
	}
}

// This exercises the actual Cosmos API listener, not a handler-only substitute.
// The unpatched JSON-RPC pre-reader returns 400 before the vote handler runs.
func TestRESTListenerStalledUploadReturnsBoundTimeout(t *testing.T) {
	var broadcasts atomic.Int64
	comet := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { broadcasts.Add(1) }))
	defer comet.Close()
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	address := probe.Addr().String()
	require.NoError(t, probe.Close())
	server := sdkapi.New(client.Context{}, log.NewNopLogger(), nil)
	NewHandler(HandlerConfig{CometRPCEndpoint: comet.URL}).RegisterTxRoutes(server.Router)
	cfg := config.DefaultConfig()
	cfg.API.Address = "tcp://" + address
	cfg.API.RPCMaxBodyBytes = 64
	cfg.API.RPCReadTimeout = 1
	cfg.API.RPCWriteTimeout = 5
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan error, 1)
	go func() { finished <- server.Start(ctx, *cfg) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-finished:
		case <-time.After(5 * time.Second):
			t.Error("API server failed to stop")
		}
	})
	var conn net.Conn
	require.Eventually(t, func() bool { conn, err = net.DialTimeout("tcp", address, 50*time.Millisecond); return err == nil }, 5*time.Second, 10*time.Millisecond)
	defer conn.Close()
	require.NoError(t, conn.SetDeadline(time.Now().Add(4*time.Second)))
	token := strings.Repeat("b", 64)
	_, err = fmt.Fprintf(conn, "POST /shielded-vote/v1/cast-vote-batch HTTP/1.1\r\nHost: localhost\r\nContent-Type: application/json\r\nX-Vote-Ingress-Attempt-V1: %s\r\nContent-Length: 100\r\nConnection: close\r\n\r\n{", token)
	require.NoError(t, err)
	response, err := http.ReadResponse(bufio.NewReader(conn), nil)
	require.NoError(t, err)
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.Equal(t, 408, response.StatusCode, string(body))
	require.Contains(t, string(body), token)
	require.Contains(t, string(body), `"dispatch":"not_started"`)
	require.Zero(t, broadcasts.Load())

	// Removing JSON-RPC preprocessing must not remove the outer size bound.
	request, err := http.NewRequest("POST", "http://"+address+"/shielded-vote/v1/cast-vote-batch", strings.NewReader(strings.Repeat("x", 100)))
	require.NoError(t, err)
	request.Header.Set("X-Vote-Ingress-Attempt-V1", token)
	boundedClient := &http.Client{Timeout: 3 * time.Second}
	oversized, err := boundedClient.Do(request)
	require.NoError(t, err)
	defer oversized.Body.Close()
	failure, err := io.ReadAll(oversized.Body)
	require.NoError(t, err)
	require.Equal(t, 400, oversized.StatusCode)
	require.Contains(t, string(failure), "request body too large")
	require.NotContains(t, string(failure), "not_started")
	require.Zero(t, broadcasts.Load())
}
