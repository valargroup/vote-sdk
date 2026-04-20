package sentry

import (
	"testing"

	sentrylib "github.com/getsentry/sentry-go"
)

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
