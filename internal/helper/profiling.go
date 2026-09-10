package helper

import (
	"os"
	"runtime"
	"sync"

	"cosmossdk.io/log"
)

// helperDiagnosticsEnabled is evaluated when constructing the helper/store.
// Production and unspecified environments cannot enable these diagnostics.
func helperDiagnosticsEnabled() bool {
	return os.Getenv("SENTRY_ENVIRONMENT") == "staging" && os.Getenv("SVOTE_HELPER_DIAGNOSTICS") == "1"
}

var diagnosticProfilingOnce sync.Once

// enableDiagnosticProfiling samples contention only on an explicitly opted-in
// staging process. The existing localhost pprof listener serves the profiles.
func enableDiagnosticProfiling(logger log.Logger) {
	if !helperDiagnosticsEnabled() || os.Getenv("SVOTE_HELPER_DIAGNOSTIC_PROFILES") != "1" {
		return
	}
	diagnosticProfilingOnce.Do(func() {
		runtime.SetMutexProfileFraction(100)
		runtime.SetBlockProfileRate(10_000_000)
		logger.Info("helper diagnostic profiling enabled", "mutex_sample_fraction", 100, "block_sample_ns", 10_000_000)
	})
}
