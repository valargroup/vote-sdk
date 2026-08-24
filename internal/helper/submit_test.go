package helper

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewChainSubmitter_ClosesIdleConnectionsBeforeChainAPI(t *testing.T) {
	var newConnections atomic.Int32
	idleConnectionClosed := make(chan struct{}, 1)

	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			t.Errorf("read request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tx_hash":"abc123","code":0,"log":""}`))
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

	submitter := NewChainSubmitter(server.URL)
	transport, ok := submitter.httpClient.Transport.(*http.Transport)
	require.True(t, ok)
	defaultTransport, ok := http.DefaultTransport.(*http.Transport)
	require.True(t, ok)
	assert.NotSame(t, defaultTransport, transport)
	assert.Equal(t, chainHTTPIdleConnTimeout, transport.IdleConnTimeout)
	assert.Less(t, transport.IdleConnTimeout, 10*time.Second)
	t.Cleanup(submitter.httpClient.CloseIdleConnections)

	// Shorten the client timeout so the test observes the same lifecycle
	// without waiting for the production five-second interval.
	transport.IdleConnTimeout = 20 * time.Millisecond

	_, err := submitter.SubmitRevealShare(&MsgRevealShareJSON{})
	require.NoError(t, err)

	select {
	case <-idleConnectionClosed:
	case <-time.After(time.Second):
		t.Fatal("client did not close the idle connection")
	}

	_, err = submitter.SubmitRevealShare(&MsgRevealShareJSON{})
	require.NoError(t, err)
	assert.Equal(t, int32(2), newConnections.Load())
}

func TestFetchVoteRound_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/shielded-vote/v1/round/aabbccdd", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"round":{"created_at_time":1699900000,"vote_end_time":1700000000}}`))
	}))
	defer server.Close()

	submitter := NewChainSubmitter(server.URL)
	info, err := submitter.FetchVoteRound("aabbccdd")
	require.NoError(t, err)
	assert.Equal(t, uint64(1699900000), info.CreatedAtTime)
	assert.Equal(t, uint64(1700000000), info.VoteEndTime)
}

func TestFetchVoteRound_NotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"error":"round not found"}`))
	}))
	defer server.Close()

	submitter := NewChainSubmitter(server.URL)
	_, err := submitter.FetchVoteRound("nonexistent")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "404")
}

func TestFetchVoteRound_InvalidJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`not json`))
	}))
	defer server.Close()

	submitter := NewChainSubmitter(server.URL)
	_, err := submitter.FetchVoteRound("aabbccdd")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse response")
}

func TestIsDuplicateNullifier(t *testing.T) {
	tests := []struct {
		name string
		code uint32
		want bool
	}{
		{"duplicate nullifier code", 2, true},
		{"round not found code", 3, false},
		{"round not active code", 4, false},
		{"zero (success)", 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, IsDuplicateNullifier(tt.code))
		})
	}
}
