package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestNewHandlerClosesIdleConnectionsBeforeCometRPC(t *testing.T) {
	var newConnections atomic.Int32
	idleConnectionClosed := make(chan struct{}, 1)

	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			t.Errorf("read request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"code":0,"hash":"abc123","log":""}}`))
	}))
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		switch state {
		case http.StateNew:
			newConnections.Add(1)
		case http.StateClosed:
			select {
			case idleConnectionClosed <- struct{}{}:
			default:
			}
		}
	}
	server.Start()
	t.Cleanup(server.Close)

	handler := NewHandler(HandlerConfig{CometRPCEndpoint: server.URL})
	transport, ok := handler.client.Transport.(*http.Transport)
	require.True(t, ok)
	defaultTransport, ok := http.DefaultTransport.(*http.Transport)
	require.True(t, ok)
	assert.NotSame(t, defaultTransport, transport)
	assert.Equal(t, cometRPCIdleConnTimeout, transport.IdleConnTimeout)
	assert.Less(t, transport.IdleConnTimeout, 10*time.Second)
	t.Cleanup(handler.client.CloseIdleConnections)

	// Shorten the client timeout so the test observes the same lifecycle
	// without waiting for the production five-second interval.
	transport.IdleConnTimeout = 20 * time.Millisecond

	txBytes := []byte("first transaction")
	_, err := handler.cometBroadcastTxSync(context.Background(), txBytes, txHashHex(txBytes))
	require.NoError(t, err)

	select {
	case <-idleConnectionClosed:
	case <-time.After(time.Second):
		t.Fatal("client did not close the idle connection")
	}

	txBytes = []byte("second transaction")
	_, err = handler.cometBroadcastTxSync(context.Background(), txBytes, txHashHex(txBytes))
	require.NoError(t, err)
	assert.Equal(t, int32(2), newConnections.Load())
}

func TestIsUnknownBroadcastError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "broken pipe", err: fmt.Errorf("write request: %w", syscall.EPIPE), want: true},
		{name: "connection reset", err: fmt.Errorf("read response: %w", syscall.ECONNRESET), want: true},
		{name: "connection refused", err: fmt.Errorf("dial: %w", syscall.ECONNREFUSED), want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isUnknownBroadcastError(tt.err))
		})
	}
}

func TestCometBroadcastTxSyncWithRetryReconcilesBrokenPipeBeforeRetry(t *testing.T) {
	var methods []string
	handler := NewHandler(HandlerConfig{CometRPCEndpoint: "http://comet.invalid"})
	handler.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		var rpcRequest struct {
			Method string `json:"method"`
		}
		require.NoError(t, json.NewDecoder(req.Body).Decode(&rpcRequest))
		methods = append(methods, rpcRequest.Method)

		switch len(methods) {
		case 1:
			return nil, syscall.EPIPE
		case 2:
			return jsonResponse(`{"jsonrpc":"2.0","id":1,"result":{"height":"42","tx_result":{"code":0,"log":"","events":[]}}}`), nil
		default:
			t.Fatalf("unexpected RPC call %d (%s)", len(methods), rpcRequest.Method)
			return nil, nil
		}
	})

	txBytes := []byte("transaction")
	result, err := handler.cometBroadcastTxSyncWithRetry(context.Background(), txBytes, "test")
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, txHashHex(txBytes), result.TxHash)
	assert.Equal(t, []string{"broadcast_tx_sync", "tx"}, methods)
}

func jsonResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestCometBroadcastTxSyncWithRetryReturnsErrorWhenOutcomeRemainsUnknown(t *testing.T) {
	comet := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("{"))
	}))
	defer comet.Close()

	handler := NewHandler(HandlerConfig{CometRPCEndpoint: comet.URL})
	txBytes := []byte("ambiguous transaction")

	result, err := handler.cometBroadcastTxSyncWithRetry(context.Background(), txBytes, "test")
	if err == nil {
		t.Fatal("expected unknown broadcast outcome to return an error")
	}
	if result != nil {
		t.Fatalf("expected no successful result, got %+v", result)
	}
	if !strings.Contains(err.Error(), "broadcast outcome unknown after retries") {
		t.Fatalf("expected unknown-outcome error, got %v", err)
	}
	if !strings.Contains(err.Error(), txHashHex(txBytes)) {
		t.Fatalf("expected error to contain transaction hash, got %v", err)
	}
}
