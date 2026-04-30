package sentry

import (
	"context"
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
	event := &sentrylib.Event{
		Message: "TypeError: Object [object Object] has no method 'updateFrom'",
	}
	if !shouldDropEvent(event) {
		t.Fatalf("expected event to be dropped by message signature")
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

	parent := sentrylib.StartSpan(context.Background(), "http.server", sentrylib.WithTransactionName("POST /shielded-vote/v1/cast-vote"))
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

func initTestSentry(t *testing.T) *captureTransport {
	t.Helper()

	transport := &captureTransport{}
	err := sentrylib.Init(sentrylib.ClientOptions{
		Dsn:              "https://public@example.com/1",
		EnableTracing:    true,
		TracesSampleRate: 1.0,
		Transport:        transport,
	})
	if err != nil {
		t.Fatalf("sentry init: %v", err)
	}
	sentryEnabled.Store(true)
	t.Cleanup(func() {
		sentryEnabled.Store(false)
		sentrylib.CurrentHub().BindClient(nil)
	})

	return transport
}
