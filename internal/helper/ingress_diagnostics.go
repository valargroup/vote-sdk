package helper

import (
	"cosmossdk.io/log"
	"github.com/valargroup/vote-sdk/internal/httpdiagnostics"
	"net/http"
)

func diagnosticRequestID(value string) string { return httpdiagnostics.RequestID(value) }

// Retains diagnostic coverage for standalone helper routers and test harnesses.
// The application's outer middleware owns the trace when present.
func ingressDiagnostics(next http.Handler, logger log.Logger) http.Handler {
	return httpdiagnostics.Wrap(next, logger)
}
