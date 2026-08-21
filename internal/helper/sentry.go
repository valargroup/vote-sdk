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

// CaptureErrWithGrouping delegates to the shared Sentry package and uses the
// stable parts to keep alert issues separate by environment and helper.
func CaptureErrWithGrouping(err error, tags map[string]string, fingerprintParts ...string) {
	sentry.CaptureErrWithGrouping(err, tags, fingerprintParts...)
}

// TraceSpan wraps a Sentry performance span.
type TraceSpan = sentry.TraceSpan

// StartTrace starts a Sentry performance span.
func StartTrace(ctx context.Context, operation, description string, tags map[string]string, data map[string]interface{}) (context.Context, *TraceSpan) {
	return sentry.StartSpan(ctx, operation, description, tags, data)
}
