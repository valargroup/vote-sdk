package helper

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFetchVoteRound_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/zally/v1/round/aabbccdd", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"round":{"vote_end_time":1700000000}}`))
	}))
	defer server.Close()

	submitter := NewChainSubmitter(server.URL)
	vet, err := submitter.FetchVoteRound("aabbccdd")
	require.NoError(t, err)
	assert.Equal(t, uint64(1700000000), vet)
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
