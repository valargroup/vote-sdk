package helper

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
	"testing"
	"time"

	"cosmossdk.io/log"
	"github.com/cosmos/cosmos-sdk/client"
	sdkapi "github.com/cosmos/cosmos-sdk/server/api"
	"github.com/cosmos/cosmos-sdk/server/config"
	"github.com/stretchr/testify/require"
)

type failedUpload struct{ err error }

func (r failedUpload) Read([]byte) (int, error) { return 0, r.err }

func TestHelperIngressTimeoutNeverEnqueues(t *testing.T) {
	token := strings.Repeat("a", 64)
	for _, prefix := range []string{"", validPayloadJSON()} {
		for _, tc := range []struct {
			name   string
			tokens []string
			err    error
			status int
		}{
			{"supported", []string{token}, os.ErrDeadlineExceeded, 408},
			{"missing", nil, os.ErrDeadlineExceeded, 400},
			{"duplicate", []string{token, token}, os.ErrDeadlineExceeded, 400},
			{"invalid", []string{"invalid"}, os.ErrDeadlineExceeded, 400},
			{"non_timeout", []string{token}, io.ErrUnexpectedEOF, 400},
		} {
			t.Run(fmt.Sprintf("prefix_%d/%s", len(prefix), tc.name), func(t *testing.T) {
				router, store := newTestRouter(t)
				req := httptest.NewRequest(http.MethodPost, "/shielded-vote/v1/shares", io.MultiReader(strings.NewReader(prefix), failedUpload{tc.err}))
				for _, value := range tc.tokens {
					req.Header.Add("X-Vote-Ingress-Attempt-V1", value)
				}
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, req)
				require.Equal(t, tc.status, rec.Code)
				if tc.status == 408 {
					require.JSONEq(t, fmt.Sprintf(`{"error":{"version":1,"code":"request_body_timeout","dispatch":"not_started","attempt":%q}}`, token), rec.Body.String())
					require.Equal(t, "application/json", rec.Header().Get("Content-Type"))
					require.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
				} else {
					require.NotContains(t, rec.Body.String(), "not_started")
				}
				require.Empty(t, store.Status())
			})
		}
	}
}

// A response failure after durable acceptance cannot produce a non-enqueue receipt.
type lostAcceptanceResponse struct {
	header    http.Header
	statuses  []int
	attempted []byte
}

func (w *lostAcceptanceResponse) Header() http.Header    { return w.header }
func (w *lostAcceptanceResponse) WriteHeader(status int) { w.statuses = append(w.statuses, status) }
func (w *lostAcceptanceResponse) Write(payload []byte) (int, error) {
	w.attempted = append(w.attempted, payload...)
	return 0, os.ErrDeadlineExceeded
}

func TestHelperLostAcceptanceResponseRemainsEnqueued(t *testing.T) {
	router, store := newTestRouter(t)
	req := httptest.NewRequest(http.MethodPost, "/shielded-vote/v1/shares", strings.NewReader(validPayloadJSON()))
	req.Header.Set("X-Vote-Ingress-Attempt-V1", strings.Repeat("b", 64))
	response := &lostAcceptanceResponse{header: make(http.Header)}
	router.ServeHTTP(response, req)
	require.Equal(t, []int{http.StatusOK}, response.statuses)
	require.NotContains(t, string(response.attempted), "not_started")
	require.Equal(t, 1, store.Status()[apiTestRoundID].Total)
	// Retrying the identical payload still reports durable duplicate acceptance.
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/shielded-vote/v1/shares", strings.NewReader(validPayloadJSON())))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"duplicate"`)
}

// Exercise both decode boundaries through the actual Cosmos REST listener.
func TestHelperRESTListenerStalledUploadReturnsBoundTimeout(t *testing.T) {
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	address := probe.Addr().String()
	require.NoError(t, probe.Close())
	server := sdkapi.New(client.Context{}, log.NewNopLogger(), nil)
	store := newTestStore(t)
	RegisterRoutes(server.Router, store, log.NewNopLogger())
	cfg := config.DefaultConfig()
	cfg.API.Address = "tcp://" + address
	cfg.API.RPCMaxBodyBytes = 1 << 20
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
	for _, prefix := range []string{"{", validPayloadJSON()} {
		t.Run(fmt.Sprintf("prefix_%d", len(prefix)), func(t *testing.T) {
			var conn net.Conn
			require.Eventually(t, func() bool { conn, err = net.DialTimeout("tcp", address, 50*time.Millisecond); return err == nil }, 5*time.Second, 10*time.Millisecond)
			defer conn.Close()
			require.NoError(t, conn.SetDeadline(time.Now().Add(4*time.Second)))
			token := strings.Repeat("c", 64)
			_, err = fmt.Fprintf(conn, "POST /shielded-vote/v1/shares HTTP/1.1\r\nHost: localhost\r\nContent-Type: application/json\r\nX-Vote-Ingress-Attempt-V1: %s\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", token, len(prefix)+100, prefix)
			require.NoError(t, err)
			response, err := http.ReadResponse(bufio.NewReader(conn), nil)
			require.NoError(t, err)
			defer response.Body.Close()
			receipt, err := io.ReadAll(response.Body)
			require.NoError(t, err)
			require.Equal(t, 408, response.StatusCode, string(receipt))
			require.JSONEq(t, fmt.Sprintf(`{"error":{"version":1,"code":"request_body_timeout","dispatch":"not_started","attempt":%q}}`, token), string(receipt))
			require.Empty(t, store.Status())
		})
	}
}
