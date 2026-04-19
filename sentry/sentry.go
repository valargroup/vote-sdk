package sentry

import (
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"cosmossdk.io/log"
	sentrylib "github.com/getsentry/sentry-go"
)

var sentryEnabled atomic.Bool

// knownNoisyErrorSignatures are non-actionable error strings observed in
// Sentry that do not originate from vote-sdk runtime code.
var knownNoisyErrorSignatures = []string{
	"has no method 'updatefrom'",
}

// InitSentry initializes the Sentry SDK with the given DSN. If dsn is empty,
// Sentry remains disabled and all capture calls become no-ops. The release
// string is attached to every event for deploy correlation (typically the
// binary version from ldflags). serverName identifies the specific node
// (e.g. the CometBFT moniker "val1") so events from different validators
// can be distinguished in the Sentry dashboard.
func InitSentry(dsn, release, serverName string, logger log.Logger) error {
	if dsn == "" {
		return nil
	}
	env := os.Getenv("SENTRY_ENVIRONMENT")
	if env == "" {
		env = "production"
	}
	err := sentrylib.Init(sentrylib.ClientOptions{
		Dsn:              dsn,
		Release:          release,
		Environment:      env,
		ServerName:       serverName,
		SampleRate:       1.0,
		TracesSampleRate: 1.0,
		AttachStacktrace: true,
		EnableTracing:    true,
		BeforeSend:       filterNoisyErrorEvents,
	})
	if err != nil {
		return fmt.Errorf("sentry init: %w", err)
	}
	if serverName != "" {
		sentrylib.ConfigureScope(func(scope *sentrylib.Scope) {
			scope.SetTag("validator", serverName)
		})
	}
	sentryEnabled.Store(true)
	logger.Info("sentry error tracking enabled", "server_name", serverName)

	return nil
}

func filterNoisyErrorEvents(event *sentrylib.Event, _ *sentrylib.EventHint) *sentrylib.Event {
	if event == nil {
		return nil
	}
	if shouldDropEvent(event) {
		return nil
	}
	return event
}

func shouldDropEvent(event *sentrylib.Event) bool {
	if matchesNoisySignature(event.Message) {
		return true
	}
	for _, ex := range event.Exception {
		if matchesNoisySignature(ex.Value) || matchesNoisySignature(ex.Type) {
			return true
		}
	}
	return false
}

func matchesNoisySignature(msg string) bool {
	if msg == "" {
		return false
	}
	lower := strings.ToLower(msg)
	for _, sig := range knownNoisyErrorSignatures {
		if strings.Contains(lower, sig) {
			return true
		}
	}
	return false
}

// FlushSentry drains buffered events before shutdown.
func FlushSentry() {
	if sentryEnabled.Load() {
		sentrylib.Flush(2 * time.Second)
	}
}

// CaptureErr sends an error to Sentry with optional string tags for context
// (e.g. round_id, share_index). No-op when Sentry is not initialized.
func CaptureErr(err error, tags map[string]string) {
	if err == nil || !sentryEnabled.Load() {
		return
	}
	if len(tags) > 0 {
		hub := sentrylib.CurrentHub().Clone()
		hub.ConfigureScope(func(scope *sentrylib.Scope) {
			for k, v := range tags {
				scope.SetTag(k, v)
			}
		})
		hub.CaptureException(err)
		return
	}
	sentrylib.CaptureException(err)
}
