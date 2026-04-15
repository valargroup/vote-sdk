package sentry

import (
	"fmt"
	"sync/atomic"
	"time"

	"cosmossdk.io/log"
	sentrylib "github.com/getsentry/sentry-go"
)

var sentryEnabled atomic.Bool

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
	err := sentrylib.Init(sentrylib.ClientOptions{
		Dsn:              dsn,
		Release:          release,
		ServerName:       serverName,
		SampleRate:       1.0,
		AttachStacktrace: true,
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

	sentrylib.CaptureMessage(fmt.Sprintf("sentry initialized on %s", serverName))
	if !sentrylib.Flush(5 * time.Second) {
		logger.Warn("sentry startup check: flush timed out — event may not have been delivered")
	} else {
		logger.Info("sentry startup check: event delivered")
	}

	return nil
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
