package helper

import (
	"context"

	"cosmossdk.io/log"

	"github.com/valargroup/vote-sdk/sentry"
)

// InitSentry delegates to the shared sentry package.
func InitSentry(dsn, release, serverName string, logger log.Logger) error {
	return sentry.InitSentry(dsn, release, serverName, logger)
}

// FlushSentry delegates to the shared sentry package.
func FlushSentry() {
	sentry.FlushSentry()
}

// CaptureErr delegates to the shared sentry package.
func CaptureErr(err error, tags map[string]string) {
	sentry.CaptureErr(err, tags)
}

// TraceSpan wraps a Sentry performance span.
type TraceSpan = sentry.TraceSpan

// StartTrace starts a Sentry performance span.
func StartTrace(ctx context.Context, operation, description string, tags map[string]string, data map[string]interface{}) (context.Context, *TraceSpan) {
	return sentry.StartSpan(ctx, operation, description, tags, data)
}
