package sentry

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"cosmossdk.io/log"
	sentrylib "github.com/getsentry/sentry-go"
	sentryhttp "github.com/getsentry/sentry-go/http"
)

type captureTransport struct {
	mu     sync.Mutex
	events []*sentrylib.Event
}

func (t *captureTransport) Flush(time.Duration) bool {
	return true
}

func (t *captureTransport) FlushWithContext(context.Context) bool {
	return true
}

func (t *captureTransport) Configure(sentrylib.ClientOptions) {}

func (t *captureTransport) SendEvent(event *sentrylib.Event) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.events = append(t.events, event)
}

func (t *captureTransport) Close() {}

func (t *captureTransport) Events() []*sentrylib.Event {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]*sentrylib.Event(nil), t.events...)
}

func TestShouldDropEvent_MessageSignature(t *testing.T) {
	tests := []struct {
		name    string
		message string
	}{
		{
			name:    "frontend updateFrom noise",
			message: "TypeError: Object [object Object] has no method 'updateFrom'",
		},
		{
			name:    "duplicate nullifier noise",
			message: "chain rejected tx (code 2): nullifier already spent",
		},
		{
			name:    "helper warming status noise",
			message: `submit: chain returned 503: {"status":"warming","started_at":"2026-06-05T20:23:39.398477557Z","duration_ms":37672}`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			event := &sentrylib.Event{Message: tc.message}
			if !shouldDropEvent(event) {
				t.Fatalf("expected event to be dropped by message signature")
			}
		})
	}
}

func TestShouldDropEvent_ExceptionSignature(t *testing.T) {
	event := &sentrylib.Event{
		Exception: []sentrylib.Exception{
			{
				Type:  "TypeError",
				Value: "Object [object Object] has no method 'updateFrom'",
			},
		},
	}
	if !shouldDropEvent(event) {
		t.Fatalf("expected event to be dropped by exception signature")
	}
}

func TestShouldDropEvent_AllowsUnrelatedError(t *testing.T) {
	event := &sentrylib.Event{
		Message: "context deadline exceeded",
		Exception: []sentrylib.Exception{
			{Type: "TimeoutError", Value: "request timed out"},
		},
	}
	if shouldDropEvent(event) {
		t.Fatalf("did not expect unrelated event to be dropped")
	}
}

func TestFilterNoisyErrorEvents(t *testing.T) {
	event := &sentrylib.Event{
		Message: "TypeError: Object [object Object] has no method 'updateFrom'",
	}
	if got := filterNoisyErrorEvents(event, nil); got != nil {
		t.Fatalf("expected noisy event to be dropped")
	}
	clean := &sentrylib.Event{Message: "database is locked"}
	if got := filterNoisyErrorEvents(clean, nil); got == nil {
		t.Fatalf("expected clean event to pass through")
	}
}

func TestScrubSensitiveRequestEvent(t *testing.T) {
	event := &sentrylib.Event{
		ServerName: "helper-a",
		User:       sentrylib.User{IPAddress: "198.51.100.1"},
		Request: &sentrylib.Request{
			URL:    "https://helper.example/shielded-vote/v1/shares",
			Method: "POST",
			Data:   `{"primary_blind":"secret share material"}`,
			Headers: map[string]string{
				"Content-Type":       "application/json",
				"x-helper-token":     "operator-secret",
				"cF-CoNnEcTiNg-Ip":   "198.51.100.1",
				"X-Forwarded-For":    "198.51.100.1",
				"X-Real-IP":          "198.51.100.1",
				"Forwarded":          "for=198.51.100.1",
				"True-Client-IP":     "198.51.100.1",
				"X-Custom-Client-IP": "198.51.100.1",
			},
			Env: map[string]string{"REMOTE_ADDR": "198.51.100.1", "REMOTE_PORT": "1234"},
		},
	}

	got := scrubSensitiveRequestEvent(event)
	if got.Request.Data != "" {
		t.Fatalf("request data was not scrubbed")
	}
	if got.User.IPAddress != "" || len(got.Request.Env) != 0 {
		t.Fatal("client address fields were not scrubbed")
	}
	if want := map[string]string{"Content-Type": "application/json"}; !reflect.DeepEqual(got.Request.Headers, want) {
		t.Fatalf("headers = %v, want %v", got.Request.Headers, want)
	}
	if got.ServerName != "helper-a" {
		t.Fatal("server name was changed")
	}
}

func TestScrubSensitiveRequestEventWithoutRequest(t *testing.T) {
	if scrubSensitiveRequestEvent(nil) != nil {
		t.Fatal("nil event was changed")
	}
	event := &sentrylib.Event{User: sentrylib.User{IPAddress: "198.51.100.1"}}
	if got := scrubSensitiveRequestEvent(event); got.User.IPAddress != "" {
		t.Fatal("client address was not scrubbed without a request")
	}
}

func TestHelperHTTPEventsScrubRequestMetadata(t *testing.T) {
	for _, tc := range []struct {
		name        string
		environment string
		panic       bool
		wantTypes   []string
	}{
		{"production success", "production", false, []string{"transaction"}},
		{"production panic", "production", true, []string{"", "transaction"}},
		{"staging panic", "staging", true, []string{""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			transport := initTestSentryWithEnvironment(t, tc.environment)
			req := httptest.NewRequest(http.MethodPost, "https://helper.example/shielded-vote/v1/shares",
				strings.NewReader(`{"primary_blind":"synthetic share material"}`))
			req.RemoteAddr = "198.51.100.1:1234"
			req.Header.Set("Accept", "application/json")
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Content-Length", strconv.FormatInt(req.ContentLength, 10))
			for _, header := range []string{
				"X-Forwarded-For", "X-Real-IP", "CF-Connecting-IP", "CF-Connecting-IPv6",
				"True-Client-IP", "Forwarded", "X-Envoy-External-Address", "X-Custom-Client-IP",
			} {
				req.Header.Set(header, "198.51.100.1")
			}
			req.Header.Set("X-Helper-Token", "synthetic-token")
			req.Header.Set("User-Agent", "synthetic-agent")
			handler := sentryhttp.New(sentryhttp.Options{Repanic: false}).Handle(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if _, err := io.Copy(io.Discard, r.Body); err != nil {
					t.Fatal(err)
				}
				if tc.panic {
					panic("synthetic handler failure")
				}
				w.WriteHeader(http.StatusOK)
			}))
			handler.ServeHTTP(httptest.NewRecorder(), req)

			events := transport.Events()
			if len(events) != len(tc.wantTypes) {
				t.Fatalf("sent %d events, want %d", len(events), len(tc.wantTypes))
			}
			wantHeaders := map[string]string{
				"Accept": "application/json", "Content-Type": "application/json",
				"Content-Length": strconv.FormatInt(req.ContentLength, 10), "Host": "helper.example",
			}
			for i, event := range events {
				if event.Type != tc.wantTypes[i] {
					t.Fatalf("event type = %q, want %q", event.Type, tc.wantTypes[i])
				}
				if event.Request == nil {
					t.Fatal("missing request metadata")
				}
				if !reflect.DeepEqual(event.Request.Headers, wantHeaders) {
					t.Errorf("headers = %v, want %v", event.Request.Headers, wantHeaders)
				}
				if event.Request.Data != "" || len(event.Request.Env) != 0 || event.User.IPAddress != "" {
					t.Error("request body or client address fields were not scrubbed")
				}
			}
		})
	}
}

func TestTraceSampleRate(t *testing.T) {
	tests := []struct {
		name            string
		environment     string
		transactionName string
		want            float64
	}{
		{
			name:            "staging helper",
			environment:     "staging",
			transactionName: "helper.process_share",
			want:            0,
		},
		{
			name:            "staging critical write",
			environment:     "staging",
			transactionName: "POST /shielded-vote/v1/cast-vote",
			want:            0,
		},
		{
			name:            "production helper",
			environment:     "production",
			transactionName: "helper.process_share",
			want:            reducedTraceSampleRate,
		},
		{
			name:            "production concrete share status",
			environment:     "production",
			transactionName: "GET /shielded-vote/v1/share-status/round-1/nullifier-1",
			want:            reducedTraceSampleRate,
		},
		{
			name:            "production concrete round",
			environment:     "production",
			transactionName: "GET /shielded-vote/v1/round/round-1",
			want:            reducedTraceSampleRate,
		},
		{
			name:            "production vote managers",
			environment:     "production",
			transactionName: "GET /shielded-vote/v1/vote-managers",
			want:            reducedTraceSampleRate,
		},
		{
			name:            "production rounds prefix boundary",
			environment:     "production",
			transactionName: "GET /shielded-vote/v1/rounds",
			want:            1,
		},
		{
			name:            "production share status prefix boundary",
			environment:     "production",
			transactionName: "GET /shielded-vote/v1/share-status",
			want:            1,
		},
		{
			name:            "production critical write",
			environment:     "production",
			transactionName: "POST /shielded-vote/v1/reveal-share",
			want:            1,
		},
		{
			name:            "unknown environment remains fully traced",
			environment:     "development",
			transactionName: "helper.process_share",
			want:            1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := traceSampleRate(tc.environment, tc.transactionName); got != tc.want {
				t.Fatalf("traceSampleRate(%q, %q) = %v, want %v", tc.environment, tc.transactionName, got, tc.want)
			}
		})
	}
}

func TestStagingDisablesTracingButKeepsErrors(t *testing.T) {
	transport := initTestSentryWithEnvironment(t, "staging")

	_, span := StartSpan(context.Background(), "helper.process_share", "helper.process_share", nil, nil)
	span.Finish(nil)
	_, transaction := StartTransaction(context.Background(), "POST /shielded-vote/v1/cast-vote", nil, nil)
	transaction.Finish(nil)
	CaptureErr(errors.New("staging database unavailable"), nil)

	events := transport.Events()
	if len(events) != 1 {
		t.Fatalf("sent %d events, want only the error event", len(events))
	}
	if len(events[0].Exception) != 1 {
		t.Fatalf("event has %d exceptions, want 1", len(events[0].Exception))
	}
	if events[0].Exception[0].Value != "staging database unavailable" {
		t.Fatalf("exception value = %q, want staging database unavailable", events[0].Exception[0].Value)
	}
}

func TestStartSpanCreatesSearchableRootSpan(t *testing.T) {
	transport := initTestSentry(t)

	_, span := StartSpan(context.Background(), "zkp.prove", "helper.generate_share_reveal_proof", map[string]string{
		"round_id": "round-1",
	}, map[string]interface{}{
		"proof_bytes": 128,
	})
	span.Finish(nil)

	events := transport.Events()
	if len(events) != 1 {
		t.Fatalf("sent %d events, want 1", len(events))
	}
	event := events[0]
	if event.Transaction != "helper.generate_share_reveal_proof" {
		t.Fatalf("transaction = %q, want helper.generate_share_reveal_proof", event.Transaction)
	}
	trace := event.Contexts["trace"]
	if trace["op"] != "zkp.prove" {
		t.Fatalf("trace op = %v, want zkp.prove", trace["op"])
	}
	if event.Tags["round_id"] != "round-1" {
		t.Fatalf("round_id tag = %q, want round-1", event.Tags["round_id"])
	}
}

func TestStartSpanKeepsParentTransactionName(t *testing.T) {
	transport := initTestSentry(t)

	parent := sentrylib.StartSpan(
		context.Background(),
		"http.server",
		sentrylib.WithTransactionName("POST /shielded-vote/v1/cast-vote"),
		sentrylib.WithSpanSampled(sentrylib.SampledTrue),
	)
	_, child := StartSpan(parent.Context(), "zkp.prove", "helper.generate_share_reveal_proof", nil, nil)
	child.Finish(nil)
	parent.Finish()

	events := transport.Events()
	if len(events) != 1 {
		t.Fatalf("sent %d events, want 1", len(events))
	}
	event := events[0]
	if event.Transaction != "POST /shielded-vote/v1/cast-vote" {
		t.Fatalf("transaction = %q, want parent transaction name", event.Transaction)
	}
	if len(event.Spans) != 1 {
		t.Fatalf("event has %d child spans, want 1", len(event.Spans))
	}
	if event.Spans[0].Op != "zkp.prove" {
		t.Fatalf("child op = %q, want zkp.prove", event.Spans[0].Op)
	}
	if event.Spans[0].Description != "helper.generate_share_reveal_proof" {
		t.Fatalf("child description = %q, want helper.generate_share_reveal_proof", event.Spans[0].Description)
	}
}

func TestCaptureErrWithGrouping(t *testing.T) {
	transport := initTestSentry(t)

	CaptureErrWithGrouping(
		errors.New("temporary submit failure at height 123"),
		map[string]string{
			"alert":    "helper_share_failure",
			"round_id": "round-1",
			"stage":    "submit_http",
		},
		"helper_share_failure",
		"round-1",
		"submit_http",
	)

	events := transport.Events()
	if len(events) != 1 {
		t.Fatalf("sent %d events, want 1", len(events))
	}
	event := events[0]
	wantFingerprint := []string{
		"helper_share_failure",
		"round-1",
		"submit_http",
		"helper-a",
	}
	if !reflect.DeepEqual(event.Fingerprint, wantFingerprint) {
		t.Fatalf("fingerprint = %#v, want %#v", event.Fingerprint, wantFingerprint)
	}
	if event.Tags["alert"] != "helper_share_failure" {
		t.Fatalf("alert tag = %q, want helper_share_failure", event.Tags["alert"])
	}
}

func initTestSentry(t *testing.T) *captureTransport {
	return initTestSentryWithEnvironment(t, "production")
}

func initTestSentryWithEnvironment(t *testing.T, environment string) *captureTransport {
	t.Helper()

	t.Setenv("SENTRY_ENVIRONMENT", environment)
	if err := InitSentry("https://public@example.com/1", "test", "helper-a", log.NewNopLogger()); err != nil {
		t.Fatalf("sentry init: %v", err)
	}
	initialized := sentrylib.CurrentHub().Client()
	options := initialized.Options()
	initialized.Close()
	// Use the production options and hooks with an in-memory transport.
	transport := &captureTransport{}
	options.Transport = transport
	client, err := sentrylib.NewClient(options)
	if err != nil {
		t.Fatalf("sentry init: %v", err)
	}
	sentrylib.CurrentHub().BindClient(client)
	t.Cleanup(func() {
		client.Close()
		sentryEnabled.Store(false)
		tracingEnabled.Store(false)
		sentrylib.CurrentHub().BindClient(nil)
	})

	return transport
}
