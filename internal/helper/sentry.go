package helper

import (
	"cosmossdk.io/log"

	"github.com/valargroup/vote-sdk/sentry"
)

// InitSentry delegates to the shared sentry package.
func InitSentry(dsn, release string, logger log.Logger) error {
	return sentry.InitSentry(dsn, release, logger)
}

// FlushSentry delegates to the shared sentry package.
func FlushSentry() {
	sentry.FlushSentry()
}

// CaptureErr delegates to the shared sentry package.
func CaptureErr(err error, tags map[string]string) {
	sentry.CaptureErr(err, tags)
}
