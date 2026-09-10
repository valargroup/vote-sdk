package helper

import (
	"encoding/hex"
	"net/http"
	"strconv"
	"time"

	"cosmossdk.io/log"
)

// diagnosticRequestID accepts only a 128-bit hex correlation token. It is never
// a metric label and must not contain a round, share, or wallet identifier.
func diagnosticRequestID(value string) string {
	if len(value) != 32 {
		return ""
	}
	if _, err := hex.DecodeString(value); err != nil {
		return ""
	}
	return value
}

// ingressDiagnostics measures the route boundary outside the Sentry wrapper.
// Requests without a valid diagnostic ID incur no diagnostic logging. The
// response timing stops at the first header write, not at socket delivery.
func ingressDiagnostics(next http.Handler, logger log.Logger) http.Handler {
	if !helperDiagnosticsEnabled() {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := diagnosticRequestID(r.Header.Get("X-Vote-Request-ID"))
		if id == "" {
			next.ServeHTTP(w, r)
			return
		}
		started := time.Now()
		w.Header().Set("X-Vote-Request-ID", id)
		w.Header().Set("X-Vote-Handler-Started-Us", strconv.FormatInt(started.UnixMicro(), 10))
		response := &diagnosticResponseWriter{ResponseWriter: w, started: started}
		defer func() {
			logger.Info("helper ingress timing", "request_id", id,
				"route", "shares", "method", "POST", "protocol", r.Proto,
				"started_unix_us", started.UnixMicro(), "duration_us", time.Since(started).Microseconds(),
				"status", response.status)
		}()
		next.ServeHTTP(response, r)
	})
}

type diagnosticResponseWriter struct {
	http.ResponseWriter
	started time.Time
	status  int
}

func (w *diagnosticResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *diagnosticResponseWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	w.Header().Set("X-Vote-Handler-Duration-Us", strconv.FormatInt(time.Since(w.started).Microseconds(), 10))
	w.ResponseWriter.WriteHeader(status)
}

func (w *diagnosticResponseWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(p)
}
