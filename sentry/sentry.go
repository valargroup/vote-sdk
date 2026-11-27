package sentry

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"cosmossdk.io/log"
	sentrylib "github.com/getsentry/sentry-go"
	sentryhttp "github.com/getsentry/sentry-go/http"
	"github.com/gorilla/mux"
)

var (
	sentryEnabled  atomic.Bool
	tracingEnabled atomic.Bool
)

const reducedTraceSampleRate = 0.1

// TraceSpan is a small wrapper around a Sentry span that keeps callers from
// depending on sentry-go directly. Methods are safe to call when tracing is
// disabled.
type TraceSpan struct {
	span *sentrylib.Span
}

// knownNoisyErrorSignatures are non-actionable error strings observed in
// Sentry that do not originate from vote-sdk runtime code.
var knownNoisyErrorSignatures = []string{
	"has no method 'updatefrom'",
	"nullifier already spent",
	`"status":"warming"`,
}

// InitSentry initializes the Sentry SDK with the given DSN. If dsn is empty,
// Sentry remains disabled and all capture calls become no-ops. The release
// string is attached to every event for deploy correlation (typically the
// binary version from ldflags). serverName identifies the specific node
// (e.g. the CometBFT moniker "val1") so events from different validators
// can be distinguished in the Sentry dashboard.
func InitSentry(dsn, release, serverName string, logger log.Logger) error {
	sentryEnabled.Store(false)
	tracingEnabled.Store(false)
	if dsn == "" {
		return nil
	}
	env := os.Getenv("SENTRY_ENVIRONMENT")
	if env == "" {
		env = "production"
	}
	enableTracing := env != "staging"
	err := sentrylib.Init(sentrylib.ClientOptions{
		Dsn:              dsn,
		Release:          release,
		Environment:      env,
		ServerName:       serverName,
		SampleRate:       1.0,
		TracesSampler:    newTraceSampler(env),
		SendDefaultPII:   false,
		AttachStacktrace: true,
		EnableTracing:    enableTracing,
		BeforeSend:       filterNoisyErrorEvents,
		BeforeSendTransaction: func(event *sentrylib.Event, _ *sentrylib.EventHint) *sentrylib.Event {
			return scrubEvent(event)
		},
	})
	if err != nil {
		return fmt.Errorf("sentry init: %w", err)
	}
	if serverName != "" {
		sentrylib.ConfigureScope(func(scope *sentrylib.Scope) {
			scope.SetTag("validator", serverName)
		})
	}
	tracingEnabled.Store(enableTracing)
	sentryEnabled.Store(true)
	logger.Info(
		"sentry error tracking enabled",
		"server_name", serverName,
		"environment", env,
		"tracing_enabled", enableTracing,
	)

	return nil
}

func newTraceSampler(environment string) sentrylib.TracesSampler {
	return func(ctx sentrylib.SamplingContext) float64 {
		return traceSampleRate(environment, ctx.Span.Name)
	}
}

func traceSampleRate(environment, transactionName string) float64 {
	if environment == "staging" {
		return 0
	}
	if environment != "production" {
		return 1
	}

	switch {
	case transactionName == "helper.process_share":
		return reducedTraceSampleRate
	case strings.HasPrefix(transactionName, "GET /shielded-vote/v1/share-status/"):
		return reducedTraceSampleRate
	case strings.HasPrefix(transactionName, "GET /shielded-vote/v1/round/"):
		return reducedTraceSampleRate
	case transactionName == "GET /shielded-vote/v1/vote-managers":
		return reducedTraceSampleRate
	default:
		return 1
	}
}

func filterNoisyErrorEvents(event *sentrylib.Event, _ *sentrylib.EventHint) *sentrylib.Event {
	event = scrubEvent(event)
	if event == nil {
		return nil
	}
	if shouldDropEvent(event) {
		return nil
	}
	return event
}

// linkingKeys name the values that would link a share's reveal to its leaf,
// or a wallet to the delegates it looked at (plan PRV-6). scrubEvent drops
// every tag, extra, context, span and breadcrumb entry under one of them.
var linkingKeys = []string{"proposal_id", "tree_position", "submit_at", "vote_decision", "delegate_index", "d"}

// linkingValue matches a linking key, or a word error messages use for one,
// followed by a number, as in "tree_position 7", `"proposal_id":2`,
// "proposal=3, decision=1", "delegate 17 is not below next_index 20" or
// "votetree: position 9 out of range". scrubEvent redacts the number.
var linkingValue = regexp.MustCompile(`(?i)\b(proposal_id|proposal|vote_decision|decision|tree_position|position|submit_at|delegate_index|delegate|d)([\s"':=(]+)(\d+)`)

// scrubEvent removes from an event or transaction, before it is sent, what
// could identify a client or link a wallet to what it submitted or looked at:
//   - the client address, and every request header but accept, content-type,
//     content-length and host;
//   - the request's body, query string, cookies and environment;
//     RouteTracing has already reduced its URL to the route template;
//   - linkingKeys entries, and linkingValue numbers in the message, exception
//     values and breadcrumb messages;
//   - breadcrumb URLs but for their scheme and host, since a breadcrumb has
//     no route template.
func scrubEvent(event *sentrylib.Event) *sentrylib.Event {
	if event == nil {
		return nil
	}
	event.User.IPAddress = ""
	if req := event.Request; req != nil {
		req.Env = nil
		req.Data = ""
		req.QueryString = ""
		req.Cookies = ""
		// Keep basic HTTP metadata without depending on proxy-specific header names.
		for header := range req.Headers {
			switch strings.ToLower(header) {
			case "accept", "content-type", "content-length", "host":
			default:
				delete(req.Headers, header)
			}
		}
	}

	event.Message = redactLinkingValues(event.Message)
	for i := range event.Exception {
		event.Exception[i].Value = redactLinkingValues(event.Exception[i].Value)
	}
	deleteLinkingKeys(event.Tags)
	deleteLinkingKeys(event.Extra)
	for _, context := range event.Contexts {
		deleteLinkingKeys(context)
		if data, ok := context["data"].(map[string]interface{}); ok {
			deleteLinkingKeys(data)
		}
	}
	for _, span := range event.Spans {
		deleteLinkingKeys(span.Tags)
		deleteLinkingKeys(span.Data)
	}
	for _, breadcrumb := range event.Breadcrumbs {
		breadcrumb.Message = redactLinkingValues(breadcrumb.Message)
		deleteLinkingKeys(breadcrumb.Data)
		delete(breadcrumb.Data, "http.query")
		delete(breadcrumb.Data, "http.fragment")
		if raw, ok := breadcrumb.Data["url"].(string); ok {
			breadcrumb.Data["url"] = origin(raw)
		}
	}
	return event
}

// redactLinkingValues replaces each number linkingValue finds in text.
func redactLinkingValues(text string) string {
	return linkingValue.ReplaceAllString(text, "${1}${2}[redacted]")
}

// deleteLinkingKeys deletes every linkingKeys entry from m.
func deleteLinkingKeys[V any](m map[string]V) {
	for _, key := range linkingKeys {
		delete(m, key)
	}
}

// origin returns rawURL's scheme and host, such as "https://chain.example",
// or "" if it has neither.
func origin(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
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
	captureErr(context.Background(), err, tags, nil)
}

// CaptureErrWithGrouping sends an error with a stable caller-defined issue
// fingerprint. The configured server name scopes the fingerprint to the
// process emitting the event. Callers should use stable operational dimensions
// and omit retry-specific values.
func CaptureErrWithGrouping(err error, tags map[string]string, fingerprintParts ...string) {
	CaptureErrWithGroupingContext(context.Background(), err, tags, fingerprintParts...)
}

// CaptureErrWithGroupingContext is CaptureErrWithGrouping on the hub ctx
// carries, if any, so the event joins the trace of the span ctx is in.
func CaptureErrWithGroupingContext(ctx context.Context, err error, tags map[string]string, fingerprintParts ...string) {
	if len(fingerprintParts) == 0 {
		captureErr(ctx, err, tags, nil)
		return
	}

	serverName := "unknown"
	if client := sentrylib.CurrentHub().Client(); client != nil {
		if configured := client.Options().ServerName; configured != "" {
			serverName = configured
		} else if hostname, err := os.Hostname(); err == nil && hostname != "" {
			serverName = hostname
		}
	}
	fingerprint := make([]string, 0, len(fingerprintParts)+1)
	fingerprint = append(fingerprint, fingerprintParts...)
	fingerprint = append(fingerprint, serverName)
	captureErr(ctx, err, tags, fingerprint)
}

func captureErr(ctx context.Context, err error, tags map[string]string, fingerprint []string) {
	if err == nil || !sentryEnabled.Load() {
		return
	}
	hub := sentrylib.GetHubFromContext(ctx)
	if hub == nil {
		hub = sentrylib.CurrentHub()
	}
	if len(tags) > 0 || len(fingerprint) > 0 {
		hub = hub.Clone()
		hub.ConfigureScope(func(scope *sentrylib.Scope) {
			for k, v := range tags {
				scope.SetTag(k, v)
			}
			if len(fingerprint) > 0 {
				scope.SetFingerprint(fingerprint)
			}
		})
	}
	hub.CaptureException(err)
}

// withOwnHub returns ctx if it already carries a span or a hub of its own,
// such as RouteTracing's per-request hub, and otherwise ctx with a clone of
// the current hub. A span makes itself the span of its hub's scope, and every
// event captured on that hub reads it without its lock, so a root span on the
// shared current hub would be read by other goroutines' events, even after it
// ends, while its own goroutine writes it.
func withOwnHub(ctx context.Context) context.Context {
	if sentrylib.SpanFromContext(ctx) != nil || sentrylib.GetHubFromContext(ctx) != nil {
		return ctx
	}
	return sentrylib.SetHubOnContext(ctx, sentrylib.CurrentHub().Clone())
}

// StartTransaction starts a root performance transaction when Sentry tracing is
// enabled, on its own hub (see withOwnHub). It returns the transaction context
// so child spans attach to it.
func StartTransaction(ctx context.Context, name string, tags map[string]string, data map[string]interface{}) (context.Context, *TraceSpan) {
	if !tracingEnabled.Load() {
		return ctx, &TraceSpan{}
	}
	span := sentrylib.StartTransaction(withOwnHub(ctx), name)
	setSpanAttributes(span, tags, data)
	return span.Context(), &TraceSpan{span: span}
}

// StartSpan starts a performance span when Sentry tracing is enabled. If ctx
// does not already contain a Sentry span, the span becomes the root transaction
// so background work is still sent to Sentry, on its own hub (see withOwnHub).
func StartSpan(ctx context.Context, operation, description string, tags map[string]string, data map[string]interface{}) (context.Context, *TraceSpan) {
	if !tracingEnabled.Load() {
		return ctx, &TraceSpan{}
	}

	options := []sentrylib.SpanOption{sentrylib.WithDescription(description)}
	if sentrylib.SpanFromContext(ctx) == nil {
		ctx = withOwnHub(ctx)
		transactionName := description
		if transactionName == "" {
			transactionName = operation
		}
		options = append(options, sentrylib.WithTransactionName(transactionName))
	}

	span := sentrylib.StartSpan(ctx, operation, options...)
	setSpanAttributes(span, tags, data)
	return span.Context(), &TraceSpan{span: span}
}

// RouteTracing returns middleware that reports each request as a Sentry
// http.server transaction, as sentryhttp does, but by its gorilla/mux route
// template, such as "GET /shielded-vote/v1/share-status/{roundId}/{nullifier}",
// never by the raw path, which carries round ids, nullifiers, hashes and
// delegate indices and keys (plan PRV-6). The template names the transaction
// and is the URL of every event the request reports, its transaction and any
// panic. It recovers panics, re-panicking when repanic is set.
func RouteTracing(repanic bool) func(http.Handler) http.Handler {
	traced := sentryhttp.New(sentryhttp.Options{Repanic: repanic})
	return func(next http.Handler) http.Handler {
		handler := traced.Handle(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			template := ""
			if route := mux.CurrentRoute(r); route != nil {
				template, _ = route.GetPathTemplate()
			}
			// sentryhttp uses the hub it finds on the context and attaches
			// the request to that hub's scope, so this processor sees every
			// event the request reports.
			hub := sentrylib.CurrentHub().Clone()
			hub.Scope().AddEventProcessor(func(event *sentrylib.Event, _ *sentrylib.EventHint) *sentrylib.Event {
				if event.Request != nil {
					// Built by hand: url.URL.String would escape the braces.
					event.Request.URL = origin(event.Request.URL) + template
				}
				return event
			})
			r = r.WithContext(sentrylib.SetHubOnContext(r.Context(), hub))
			// sentryhttp names the transaction by Pattern when set.
			r.Pattern = template
			if template == "" {
				r.Pattern = "(unrouted)"
			}
			handler.ServeHTTP(w, r)
		})
	}
}

// SetData attaches diagnostic data to a span.
func (s *TraceSpan) SetData(name string, value interface{}) {
	if s == nil || s.span == nil {
		return
	}
	s.span.SetData(name, value)
}

// Finish closes the span and marks it OK or failed based on err.
func (s *TraceSpan) Finish(err error) {
	if s == nil || s.span == nil {
		return
	}
	if err != nil {
		s.span.Status = sentrylib.SpanStatusInternalError
	} else {
		s.span.Status = sentrylib.SpanStatusOK
	}
	s.span.Finish()
}

func setSpanAttributes(span *sentrylib.Span, tags map[string]string, data map[string]interface{}) {
	for k, v := range tags {
		span.SetTag(k, v)
	}
	for k, v := range data {
		span.SetData(k, v)
	}
}
