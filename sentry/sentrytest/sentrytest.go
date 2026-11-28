// Package sentrytest captures what the shared Sentry client would send, for
// tests that check telemetry hygiene.
package sentrytest

import (
	"context"
	"sync"
	"testing"
	"time"

	"cosmossdk.io/log"
	sentrylib "github.com/getsentry/sentry-go"

	"github.com/valargroup/vote-sdk/sentry"
)

// Transport is a Sentry transport that keeps every event in memory.
type Transport struct {
	mu     sync.Mutex
	events []*sentrylib.Event
}

// Configure is a no-op; the transport needs no options.
func (t *Transport) Configure(sentrylib.ClientOptions) {}

// SendEvent keeps event.
func (t *Transport) SendEvent(event *sentrylib.Event) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.events = append(t.events, event)
}

// Flush reports that nothing is pending.
func (t *Transport) Flush(time.Duration) bool { return true }

// FlushWithContext reports that nothing is pending.
func (t *Transport) FlushWithContext(context.Context) bool { return true }

// Close is a no-op.
func (t *Transport) Close() {}

// Events returns the events sent so far, transactions included.
func (t *Transport) Events() []*sentrylib.Event {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]*sentrylib.Event(nil), t.events...)
}

// Capture enables Sentry with its production hooks and every transaction
// sampled, as outside production, and sends events to the returned transport
// until the test ends.
func Capture(t testing.TB) *Transport {
	t.Helper()
	t.Setenv("SENTRY_ENVIRONMENT", "development")
	if err := sentry.InitSentry("https://public@example.com/1", "test", "sentrytest", log.NewNopLogger()); err != nil {
		t.Fatalf("sentry init: %v", err)
	}
	initialized := sentrylib.CurrentHub().Client()
	options := initialized.Options()
	initialized.Close()
	transport := &Transport{}
	options.Transport = transport
	client, err := sentrylib.NewClient(options)
	if err != nil {
		t.Fatalf("sentry client: %v", err)
	}
	sentrylib.CurrentHub().BindClient(client)
	t.Cleanup(func() {
		client.Close()
		// An empty DSN turns tracing and capture back off.
		if err := sentry.InitSentry("", "", "", log.NewNopLogger()); err != nil {
			t.Errorf("sentry reset: %v", err)
		}
		sentrylib.CurrentHub().BindClient(nil)
	})
	return transport
}
