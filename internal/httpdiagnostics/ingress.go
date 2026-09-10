// Package httpdiagnostics correlates opted-in staging HTTP requests without
// changing protocol responses, request bodies, or processing decisions.
package httpdiagnostics

import (
	"context"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"cosmossdk.io/log"
	"github.com/felixge/httpsnoop"
)

type contextKey struct{}
type requestTrace struct {
	id, route string
	started   time.Time
	logger    log.Logger
}

// Enabled requires both explicit staging and diagnostics opt-in.
func Enabled() bool {
	return os.Getenv("SENTRY_ENVIRONMENT") == "staging" && os.Getenv("SVOTE_HELPER_DIAGNOSTICS") == "1"
}

// RequestID accepts only a random-token-shaped value, never an arbitrary header.
func RequestID(value string) string {
	if len(value) != 32 {
		return ""
	}
	if _, err := hex.DecodeString(value); err != nil {
		return ""
	}
	return strings.ToLower(value)
}

// Route returns a bounded vocabulary, excluding dynamic path identifiers.
func Route(r *http.Request) string {
	path := strings.TrimPrefix(r.URL.Path, "/shielded-vote/v1/")
	if path == r.URL.Path {
		return ""
	}
	if r.Method == http.MethodPost {
		switch path {
		case "shares":
			return "shares"
		case "delegate-vote":
			return "delegate_vote"
		case "cast-vote":
			return "cast_vote"
		case "cast-vote-batch":
			return "cast_vote_batch"
		case "delegate-and-cast-vote-batch":
			return "delegate_and_cast_vote_batch"
		}
	}
	if r.Method == http.MethodGet {
		if path == "status" {
			return "helper_status"
		}
		if hash, ok := strings.CutPrefix(path, "tx/"); ok && len(hash) == 64 {
			if _, err := hex.DecodeString(hash); err == nil {
				return "chain_status"
			}
		}
	}
	return ""
}

// Wrap records router entry, body consumption, first headers and response writes.
// It preserves optional ResponseWriter interfaces. A nested wrapper is a no-op.
// Durations end at application writes, not delivery to a network peer.
func Wrap(next http.Handler, logger log.Logger) http.Handler {
	if !Enabled() {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, route := RequestID(r.Header.Get("X-Vote-Request-ID")), Route(r)
		if id == "" || route == "" || r.Context().Value(contextKey{}) != nil {
			next.ServeHTTP(w, r)
			return
		}
		started := time.Now()
		trace := &requestTrace{id: id, route: route, started: started, logger: logger}
		r = r.WithContext(context.WithValue(r.Context(), contextKey{}, trace))
		body := &timedBody{started: started, length: r.ContentLength}
		if r.Body != nil {
			body.ReadCloser = r.Body
			r.Body = body
		}
		status, writeBytes, writeFailures := 0, int64(0), 0
		var headersAt, writeDuration time.Duration
		beforeHeader := func(code int) {
			// Informational responses do not finalize the response or its timing.
			if status != 0 || (code >= 100 && code < 200 && code != 101) {
				return
			}
			status = code
			headersAt = time.Since(started)
			w.Header().Set("X-Vote-Request-ID", id)
			w.Header().Set("X-Vote-Handler-Started-Us", strconv.FormatInt(started.UnixMicro(), 10))
			w.Header().Set("X-Vote-Handler-Duration-Us", strconv.FormatInt(headersAt.Microseconds(), 10))
			if body.complete {
				w.Header().Set("X-Vote-Body-Read-Us", strconv.FormatInt(body.completedAt.Microseconds(), 10))
			}
		}
		wrapped := httpsnoop.Wrap(w, httpsnoop.Hooks{
			WriteHeader: func(next httpsnoop.WriteHeaderFunc) httpsnoop.WriteHeaderFunc {
				return func(code int) { beforeHeader(code); next(code) }
			},
			Write: func(next httpsnoop.WriteFunc) httpsnoop.WriteFunc {
				return func(p []byte) (int, error) {
					beforeHeader(http.StatusOK)
					start := time.Now()
					n, err := next(p)
					writeDuration += time.Since(start)
					writeBytes += int64(n)
					if err != nil {
						writeFailures++
					}
					return n, err
				}
			},
			Flush: func(next httpsnoop.FlushFunc) httpsnoop.FlushFunc {
				return func() { beforeHeader(http.StatusOK); next() }
			},
			ReadFrom: func(next httpsnoop.ReadFromFunc) httpsnoop.ReadFromFunc {
				return func(src io.Reader) (int64, error) {
					beforeHeader(http.StatusOK)
					start := time.Now()
					n, err := next(src)
					writeDuration += time.Since(start)
					writeBytes += n
					if err != nil {
						writeFailures++
					}
					return n, err
				}
			},
		})
		defer func() {
			if status == 0 {
				status = http.StatusOK
			}
			logger.Info("vote HTTP timing", "request_id", id, "route", route, "method", r.Method, "protocol", r.Proto,
				"started_unix_us", started.UnixMicro(), "duration_us", time.Since(started).Microseconds(), "headers_us", headersAt.Microseconds(),
				"body_read_us", body.completedAt.Microseconds(), "body_complete", body.complete, "body_bytes", body.bytes,
				"response_write_us", writeDuration.Microseconds(), "response_bytes", writeBytes, "write_errors", writeFailures, "status", status)
		}()
		next.ServeHTTP(wrapped, r)
	})
}

type timedBody struct {
	io.ReadCloser
	started       time.Time
	length, bytes int64
	completedAt   time.Duration
	complete      bool
}

func (b *timedBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.bytes += int64(n)
	if !b.complete && (err == io.EOF || (b.length >= 0 && b.bytes >= b.length)) {
		b.completedAt = time.Since(b.started)
		b.complete = true
	}
	return n, err
}

// HandlerEntry records arrival inside the endpoint, after outer middleware.
func HandlerEntry(ctx context.Context) {
	if trace, ok := ctx.Value(contextKey{}).(*requestTrace); ok {
		trace.logger.Info("vote HTTP phase", "request_id", trace.id, "route", trace.route, "phase", "handler_entry", "after_us", time.Since(trace.started).Microseconds())
	}
}

// ObserveRPC records a bounded local CometBFT phase and error class. It never
// emits transaction hashes, RPC URLs, payloads or free-form error messages.
func ObserveRPC(ctx context.Context, phase string, started time.Time, err error, attempt int) {
	if phase != "broadcast" && phase != "status_lookup" {
		return
	}
	if trace, ok := ctx.Value(contextKey{}).(*requestTrace); ok {
		outcome := "ok"
		if err != nil {
			outcome = "error"
		}
		if ctx.Err() != nil {
			outcome = "context_done"
		}
		trace.logger.Info("vote HTTP phase", "request_id", trace.id, "route", trace.route, "phase", phase, "after_us", started.Sub(trace.started).Microseconds(), "duration_us", time.Since(started).Microseconds(), "attempt", attempt, "outcome", outcome)
	}
}
