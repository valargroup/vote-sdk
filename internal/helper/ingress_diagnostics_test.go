package helper

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"cosmossdk.io/log"
	"github.com/stretchr/testify/require"
)

func TestIngressDiagnosticsDoesNotChangeResponse(t *testing.T) {
	t.Setenv("SENTRY_ENVIRONMENT", "staging")
	t.Setenv("SVOTE_HELPER_DIAGNOSTICS", "1")
	for _, id := range []string{"", "secret", "0123456789abcdef0123456789abcdef", "0123456789abcdef0123456789abcdeg"} {
		t.Run(id, func(t *testing.T) {
			var logs bytes.Buffer
			handler := ingressDiagnostics(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusConflict)
				_, _ = w.Write([]byte(`{"status":"conflict"}`))
			}), log.NewLogger(&logs))
			request := httptest.NewRequest(http.MethodPost, "/shielded-vote/v1/shares", strings.NewReader("private-payload"))
			request.Header.Set("X-Vote-Request-ID", id)
			request.Header.Set("Authorization", "private-token")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			require.NotContains(t, logs.String(), "private-payload")
			require.NotContains(t, logs.String(), "private-token")
			require.Equal(t, http.StatusConflict, response.Code)
			require.JSONEq(t, `{"status":"conflict"}`, response.Body.String())
			if diagnosticRequestID(id) == "" {
				require.Empty(t, response.Header().Get("X-Vote-Request-ID"))
				require.Empty(t, response.Header().Get("X-Vote-Handler-Duration-Us"))
			} else {
				require.Equal(t, id, response.Header().Get("X-Vote-Request-ID"))
				started, err := strconv.ParseInt(response.Header().Get("X-Vote-Handler-Started-Us"), 10, 64)
				require.NoError(t, err)
				require.Positive(t, started)
				_, err = strconv.ParseUint(response.Header().Get("X-Vote-Handler-Duration-Us"), 10, 64)
				require.NoError(t, err)
			}
		})
	}
}

func TestIngressDiagnosticsImplicitStatus(t *testing.T) {
	t.Setenv("SENTRY_ENVIRONMENT", "staging")
	t.Setenv("SVOTE_HELPER_DIAGNOSTICS", "1")
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/shielded-vote/v1/shares", nil)
	r.Header.Set("X-Vote-Request-ID", "0123456789abcdef0123456789abcdef")
	ingressDiagnostics(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) }), log.NewNopLogger()).ServeHTTP(w, r)
	require.Equal(t, http.StatusOK, w.Code)
	require.NotEmpty(t, w.Header().Get("X-Vote-Handler-Duration-Us"))
}

func TestDiagnosticsRequireExplicitStagingOptIn(t *testing.T) {
	for _, env := range []string{"", "production", "staging"} {
		for _, flag := range []string{"", "0", "1"} {
			t.Run(env+"/"+flag, func(t *testing.T) {
				t.Setenv("SENTRY_ENVIRONMENT", env)
				t.Setenv("SVOTE_HELPER_DIAGNOSTICS", flag)
				enabled := env == "staging" && flag == "1"
				require.Equal(t, enabled, helperDiagnosticsEnabled())
				store := newTestStore(t)
				require.Equal(t, enabled, store.metrics != nil)
				var logs bytes.Buffer
				w := httptest.NewRecorder()
				r := httptest.NewRequest("POST", "/shielded-vote/v1/shares", nil)
				r.Header.Set("X-Vote-Request-ID", "0123456789abcdef0123456789abcdef")
				ingressDiagnostics(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(202) }), log.NewLogger(&logs)).ServeHTTP(w, r)
				require.Equal(t, 202, w.Code)
				require.Equal(t, enabled, w.Header().Get("X-Vote-Handler-Duration-Us") != "")
				require.Equal(t, enabled, logs.Len() > 0)
			})
		}
	}
}
