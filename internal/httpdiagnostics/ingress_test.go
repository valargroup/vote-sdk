package httpdiagnostics

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cosmossdk.io/log"
	"github.com/stretchr/testify/require"
)

func TestCorrelatedTimelinePreservesBodyStatusAndWriterInterfaces(t *testing.T) {
	t.Setenv("SENTRY_ENVIRONMENT", "staging")
	t.Setenv("SVOTE_HELPER_DIAGNOSTICS", "1")
	var logs bytes.Buffer
	handler := Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		HandlerEntry(r.Context())
		require.Implements(t, (*http.Flusher)(nil), w)
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.Equal(t, "private-payload", string(body))
		ObserveRPC(r.Context(), "broadcast", time.Now(), errors.New("private failure"), 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, err = w.Write([]byte(`{"ok":true}`))
		require.NoError(t, err)
	}), log.NewLogger(&logs, log.OutputJSONOption()))
	// Nested helper/outer application wrappers must not emit two ingress records.
	handler = Wrap(handler, log.NewLogger(&logs, log.OutputJSONOption()))
	request := httptest.NewRequest("POST", "/shielded-vote/v1/delegate-and-cast-vote-batch", strings.NewReader("private-payload"))
	request.Header.Set("X-Vote-Request-ID", strings.Repeat("a", 32))
	request.Header.Set("Authorization", "private-token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	require.Equal(t, 202, response.Code)
	require.JSONEq(t, `{"ok":true}`, response.Body.String())
	require.NotEmpty(t, response.Header().Get("X-Vote-Body-Read-Us"))
	require.Equal(t, strings.Repeat("a", 32), response.Header().Get("X-Vote-Request-ID"))
	for _, private := range []string{"private-payload", "private-token", "private failure", "/shielded-vote/v1/"} {
		require.NotContains(t, logs.String(), private)
	}
	ingress := 0
	for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		var record map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &record))
		if record["message"] == "vote HTTP timing" {
			ingress++
			require.Equal(t, true, record["body_complete"])
			require.Equal(t, float64(202), record["status"])
		}
	}
	require.Equal(t, 1, ingress)
}

func TestRouteVocabularyExcludesIdentifiersAndUnexpectedMethods(t *testing.T) {
	for _, test := range []struct{ method, path, want string }{
		{"POST", "/shielded-vote/v1/shares", "shares"},
		{"GET", "/shielded-vote/v1/tx/" + strings.Repeat("a", 64), "chain_status"},
		{"GET", "/shielded-vote/v1/tx/private", ""},
		{"GET", "/shielded-vote/v1/share-status/private", ""},
		{"GET", "/shielded-vote/v1/shares", ""},
		{"POST", "/private", ""},
	} {
		require.Equal(t, test.want, Route(httptest.NewRequest(test.method, test.path, nil)))
	}
}

func TestInformationalHeadersAndImplicitSuccess(t *testing.T) {
	t.Setenv("SENTRY_ENVIRONMENT", "staging")
	t.Setenv("SVOTE_HELPER_DIAGNOSTICS", "1")
	handler := Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusEarlyHints)
		w.WriteHeader(http.StatusNoContent)
	}), log.NewNopLogger())
	request := httptest.NewRequest("POST", "/shielded-vote/v1/shares", nil)
	request.Header.Set("X-Vote-Request-ID", strings.Repeat("a", 32))
	// httptest retains the first header status, but the diagnostic header must
	// identify the final response, not freeze at an informational write.
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	require.NotEmpty(t, response.Header().Get("X-Vote-Handler-Duration-Us"))
	require.NotPanics(t, func() { ObserveRPC(context.Background(), "broadcast", time.Now(), nil, 1) })
}
