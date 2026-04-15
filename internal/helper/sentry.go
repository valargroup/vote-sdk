package helper

import (
	"fmt"
	"sync/atomic"
	"time"

	"cosmossdk.io/log"
	"github.com/getsentry/sentry-go"
)

var sentryEnabled atomic.Bool

// InitSentry initializes the Sentry SDK with the given DSN. If dsn is empty,
// Sentry remains disabled and all capture calls become no-ops. The release
// string is attached to every event for deploy correlation (typically the
// binary version from ldflags).
func InitSentry(dsn, release string, logger log.Logger) error {
	if dsn == "" {
		return nil
	}
	err := sentry.Init(sentry.ClientOptions{
		Dsn:              dsn,
		Release:          release,
		SampleRate:       1.0,
		AttachStacktrace: true,
	})
	if err != nil {
		return fmt.Errorf("sentry init: %w", err)
	}
	sentryEnabled.Store(true)
	logger.Info("sentry error tracking enabled")
	return nil
}

// FlushSentry drains buffered events before shutdown.
func FlushSentry() {
	if sentryEnabled.Load() {
		sentry.Flush(2 * time.Second)
	}
}

// CaptureErr sends an error to Sentry with optional string tags for context
// (e.g. round_id, share_index). No-op when Sentry is not initialized.
func CaptureErr(err error, tags map[string]string) {
	if err == nil || !sentryEnabled.Load() {
		return
	}
	if len(tags) > 0 {
		hub := sentry.CurrentHub().Clone()
		hub.ConfigureScope(func(scope *sentry.Scope) {
			for k, v := range tags {
				scope.SetTag(k, v)
			}
		})
		hub.CaptureException(err)
		return
	}
	sentry.CaptureException(err)
}
