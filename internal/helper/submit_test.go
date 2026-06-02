package helper

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

// RoundTrip runs the test transport callback.
func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
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

func TestSubmitRevealShare_RetriesTransportError(t *testing.T) {
	attempts := 0
	submitter := NewChainSubmitter("http://chain.test")
	submitter.httpClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			attempts++
			require.Equal(t, "/shielded-vote/v1/reveal-share", req.URL.Path)
			body, err := io.ReadAll(req.Body)
			require.NoError(t, err)
			assert.Contains(t, string(body), `"proposal_id":7`)
			if attempts == 1 {
				return nil, testConnectionResetErr()
			}
			return jsonResponse(req, http.StatusOK, `{"tx_hash":"AABB","code":0,"log":""}`), nil
		}),
	}

	result, err := submitter.SubmitRevealShareContext(context.Background(), testRevealShareJSON())
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "AABB", result.TxHash)
	assert.Equal(t, 2, attempts)
}

func TestSubmitRevealShare_RetriesHTTPClientTimeout(t *testing.T) {
	attempts := 0
	submitter := NewChainSubmitter("http://chain.test")
	submitter.httpClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			attempts++
			if attempts == 1 {
				return nil, &url.Error{
					Op:  req.Method,
					URL: req.URL.String(),
					Err: context.DeadlineExceeded,
				}
			}
			return jsonResponse(req, http.StatusOK, `{"tx_hash":"AABB","code":0,"log":""}`), nil
		}),
	}

	result, err := submitter.SubmitRevealShareContext(context.Background(), testRevealShareJSON())
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "AABB", result.TxHash)
	assert.Equal(t, 2, attempts)
}

func TestSubmitRevealShare_DoesNotRetryNonTransientTransportError(t *testing.T) {
	attempts := 0
	submitter := NewChainSubmitter("http://chain.test")
	submitter.httpClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			attempts++
			return nil, &net.DNSError{
				Err:        "no such host",
				Name:       req.URL.Hostname(),
				IsNotFound: true,
			}
		}),
	}

	_, err := submitter.SubmitRevealShareContext(context.Background(), testRevealShareJSON())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no such host")
	assert.Equal(t, 1, attempts)
}

func TestSubmitRevealShare_DoesNotRetryChainResponse(t *testing.T) {
	attempts := 0
	submitter := NewChainSubmitter("http://chain.test")
	submitter.httpClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			attempts++
			return jsonResponse(req, http.StatusInternalServerError, `{"error":"boom"}`), nil
		}),
	}

	_, err := submitter.SubmitRevealShareContext(context.Background(), testRevealShareJSON())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "chain returned 500")
	assert.Equal(t, 1, attempts)
}

func TestSubmitRevealShare_DoesNotRetryChainRejection(t *testing.T) {
	attempts := 0
	submitter := NewChainSubmitter("http://chain.test")
	submitter.httpClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			attempts++
			return jsonResponse(req, http.StatusUnprocessableEntity, `{"tx_hash":"","code":5,"log":"vote round is not active"}`), nil
		}),
	}

	result, err := submitter.SubmitRevealShareContext(context.Background(), testRevealShareJSON())
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, uint32(5), result.Code)
	assert.Equal(t, 1, attempts)
}

func TestSubmitRevealShare_DoesNotRetryCanceledContext(t *testing.T) {
	attempts := 0
	submitter := NewChainSubmitter("http://chain.test")
	submitter.httpClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			attempts++
			return nil, req.Context().Err()
		}),
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := submitter.SubmitRevealShareContext(ctx, testRevealShareJSON())
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, 1, attempts)
}

// jsonResponse builds a minimal HTTP response for the submitter tests.
func jsonResponse(req *http.Request, status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}
}

// testRevealShareJSON returns a minimal payload for submitter tests.
func testRevealShareJSON() *MsgRevealShareJSON {
	return &MsgRevealShareJSON{
		ShareNullifier:           "nf",
		EncShare:                 "share",
		ProposalID:               7,
		VoteDecision:             1,
		Proof:                    "proof",
		VoteRoundID:              "round",
		VoteCommTreeAnchorHeight: 42,
	}
}

// testConnectionResetErr builds the wrapped syscall error returned by Go's
// network stack when a TCP peer resets a connection.
func testConnectionResetErr() error {
	return &net.OpError{
		Op:  "read",
		Net: "tcp",
		Err: &os.SyscallError{
			Syscall: "read",
			Err:     syscall.ECONNRESET,
		},
	}
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
