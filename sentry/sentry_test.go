package sentry

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	sentrylib "github.com/getsentry/sentry-go"
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
		Request: &sentrylib.Request{
			URL:    "https://helper.example/shielded-vote/v1/shares",
			Method: "POST",
			Data:   `{"primary_blind":"secret share material"}`,
			Headers: map[string]string{
				"Content-Type":   "application/json",
				"x-helper-token": "operator-secret",
			},
		},
	}

	got := scrubSensitiveRequestEvent(event)
	if got.Request.Data != "" {
		t.Fatalf("request data was not scrubbed")
	}
	if _, ok := got.Request.Headers["x-helper-token"]; ok {
		t.Fatalf("helper token header was not scrubbed")
	}
	if got.Request.Headers["Content-Type"] != "application/json" {
		t.Fatalf("non-sensitive header was removed")
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

	transport := &captureTransport{}
	enableTracing := environment != "staging"
	err := sentrylib.Init(sentrylib.ClientOptions{
		Dsn:           "https://public@example.com/1",
		Environment:   environment,
		EnableTracing: enableTracing,
		ServerName:    "helper-a",
		TracesSampler: newTraceSampler(environment),
		Transport:     transport,
	})
	if err != nil {
		t.Fatalf("sentry init: %v", err)
	}
	sentryEnabled.Store(true)
	tracingEnabled.Store(enableTracing)
	t.Cleanup(func() {
		sentryEnabled.Store(false)
		tracingEnabled.Store(false)
		sentrylib.CurrentHub().BindClient(nil)
	})

	return transport
}
