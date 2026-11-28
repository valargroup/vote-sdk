package sentry

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"cosmossdk.io/log"
	sentrylib "github.com/getsentry/sentry-go"
	sentryhttp "github.com/getsentry/sentry-go/http"
	"github.com/gorilla/mux"
)

type captureTransport struct {
	mu     sync.Mutex
	events []*sentrylib.Event
}

func (t *captureTransport) Flush(time.Duration) bool {
	return true
}

func (t *captureTransport) FlushWithContext(context.Context) bool {
	return true
}

func (t *captureTransport) Configure(sentrylib.ClientOptions) {}

func (t *captureTransport) SendEvent(event *sentrylib.Event) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.events = append(t.events, event)
}

func (t *captureTransport) Close() {}

func (t *captureTransport) Events() []*sentrylib.Event {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]*sentrylib.Event(nil), t.events...)
}

func TestShouldDropEvent_MessageSignature(t *testing.T) {
	tests := []struct {
		name    string
		message string
	}{
		{
			name:    "frontend updateFrom noise",
			message: "TypeError: Object [object Object] has no method 'updateFrom'",
		},
		{
			name:    "duplicate nullifier noise",
			message: "chain rejected tx (code 2): nullifier already spent",
		},
		{
			name:    "helper warming status noise",
			message: `submit: chain returned 503: {"status":"warming","started_at":"2026-06-05T20:23:39.398477557Z","duration_ms":37672}`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			event := &sentrylib.Event{Message: tc.message}
			if !shouldDropEvent(event) {
				t.Fatalf("expected event to be dropped by message signature")
			}
		})
	}
}

func TestShouldDropEvent_ExceptionSignature(t *testing.T) {
	event := &sentrylib.Event{
		Exception: []sentrylib.Exception{
			{
				Type:  "TypeError",
				Value: "Object [object Object] has no method 'updateFrom'",
			},
		},
	}
	if !shouldDropEvent(event) {
		t.Fatalf("expected event to be dropped by exception signature")
	}
}

func TestShouldDropEvent_AllowsUnrelatedError(t *testing.T) {
	event := &sentrylib.Event{
		Message: "context deadline exceeded",
		Exception: []sentrylib.Exception{
			{Type: "TimeoutError", Value: "request timed out"},
		},
	}
	if shouldDropEvent(event) {
		t.Fatalf("did not expect unrelated event to be dropped")
	}
}

func TestFilterNoisyErrorEvents(t *testing.T) {
	event := &sentrylib.Event{
		Message: "TypeError: Object [object Object] has no method 'updateFrom'",
	}
	if got := filterNoisyErrorEvents(event, nil); got != nil {
		t.Fatalf("expected noisy event to be dropped")
	}
	clean := &sentrylib.Event{Message: "database is locked"}
	if got := filterNoisyErrorEvents(clean, nil); got == nil {
		t.Fatalf("expected clean event to pass through")
	}
}

func TestScrubSensitiveRequestEvent(t *testing.T) {
	event := &sentrylib.Event{
		ServerName: "helper-a",
		User:       sentrylib.User{IPAddress: "198.51.100.1"},
		Request: &sentrylib.Request{
			URL:    "https://helper.example/shielded-vote/v1/shares",
			Method: "POST",
			Data:   `{"primary_blind":"secret share material"}`,
			Headers: map[string]string{
				"Content-Type":       "application/json",
				"x-helper-token":     "operator-secret",
				"cF-CoNnEcTiNg-Ip":   "198.51.100.1",
				"X-Forwarded-For":    "198.51.100.1",
				"X-Real-IP":          "198.51.100.1",
				"Forwarded":          "for=198.51.100.1",
				"True-Client-IP":     "198.51.100.1",
				"X-Custom-Client-IP": "198.51.100.1",
			},
			Env: map[string]string{"REMOTE_ADDR": "198.51.100.1", "REMOTE_PORT": "1234"},
		},
	}

	got := scrubEvent(event)
	if got.Request.Data != "" {
		t.Fatalf("request data was not scrubbed")
	}
	if got.User.IPAddress != "" || len(got.Request.Env) != 0 {
		t.Fatal("client address fields were not scrubbed")
	}
	if want := map[string]string{"Content-Type": "application/json"}; !reflect.DeepEqual(got.Request.Headers, want) {
		t.Fatalf("headers = %v, want %v", got.Request.Headers, want)
	}
	if got.ServerName != "helper-a" {
		t.Fatal("server name was changed")
	}
}

// TestScrubEventDropsRequestBodyQueryAndCookies checks that no event keeps a
// request body, query string or cookie, on any route.
func TestScrubEventDropsRequestBodyQueryAndCookies(t *testing.T) {
	for _, route := range []string{"/shielded-vote/v1/reveal-share", "/shielded-vote/v1/delegate-op", "/api/voting-config"} {
		event := scrubEvent(&sentrylib.Event{Request: &sentrylib.Request{
			URL:         "https://chain.example" + route,
			Method:      "POST",
			Data:        `{"proposal_id":2,"vote_decision":3}`,
			QueryString: "start_after=17",
			Cookies:     "session=1",
		}})
		if event.Request.Data != "" || event.Request.QueryString != "" || event.Request.Cookies != "" {
			t.Fatalf("%s: request = %+v, want no body, query or cookies", route, event.Request)
		}
	}
}

// TestScrubEventRemovesLinkingKeysAndValues checks that an event keeps no
// linking key and no number written after one, wherever it sits, and keeps
// the other values next to them.
func TestScrubEventRemovesLinkingKeysAndValues(t *testing.T) {
	event := scrubEvent(&sentrylib.Event{
		Message: "share failed: tree_position 7 out of range (tree has 5 leaves)",
		Exception: []sentrylib.Exception{
			{Value: "chain rejected tx (code 53): delegate 17 is not below next_index 20: delegate not found"},
			{Value: `invalid share {"proposal_id":2,"vote_decision":3,"share_index":4}`},
			{Value: "compute merkle path: votetree: position 9 out of range or height 12 has no checkpoint"},
			{Value: "imported submit_at (1790000000) >= vote_end_time (1790000001)"},
			{Value: "missing accumulator (proposal=4, decision=5), d=6"},
		},
		Tags:     map[string]string{"round_id": "ab", "share_index": "3", "proposal_id": "2", "d": "17"},
		Extra:    map[string]interface{}{"tree_position": 7, "stage": "submit"},
		Contexts: map[string]sentrylib.Context{"trace": {"data": map[string]interface{}{"submit_at": 1, "vote_decision": 3, "outcome": "ok"}}},
		Spans: []*sentrylib.Span{{
			Tags: map[string]string{"delegate_index": "17", "rpc.method": "tx"},
			Data: map[string]interface{}{"proposal_id": 2, "attempt": 1},
		}},
		Breadcrumbs: []*sentrylib.Breadcrumb{{
			Message: "looked up delegate 17",
			Data: map[string]interface{}{
				"url": "https://chain.example/shielded-vote/v1/delegates/17?start_after=16", "http.query": "start_after=16",
				"method": "GET", "delegate_index": 17,
			},
		}},
	})

	if want := "share failed: tree_position [redacted] out of range (tree has 5 leaves)"; event.Message != want {
		t.Errorf("message = %q, want %q", event.Message, want)
	}
	var values []string
	for _, exception := range event.Exception {
		values = append(values, exception.Value)
	}
	if want := []string{
		"chain rejected tx (code 53): delegate [redacted] is not below next_index 20: delegate not found",
		`invalid share {"proposal_id":[redacted],"vote_decision":[redacted],"share_index":4}`,
		"compute merkle path: votetree: position [redacted] out of range or height 12 has no checkpoint",
		"imported submit_at ([redacted]) >= vote_end_time (1790000001)",
		"missing accumulator (proposal=[redacted], decision=[redacted]), d=[redacted]",
	}; !reflect.DeepEqual(values, want) {
		t.Errorf("exception values = %q, want %q", values, want)
	}
	if want := map[string]string{"round_id": "ab", "share_index": "3"}; !reflect.DeepEqual(event.Tags, want) {
		t.Errorf("tags = %v, want %v", event.Tags, want)
	}
	if want := map[string]interface{}{"stage": "submit"}; !reflect.DeepEqual(event.Extra, want) {
		t.Errorf("extra = %v, want %v", event.Extra, want)
	}
	if want := map[string]interface{}{"outcome": "ok"}; !reflect.DeepEqual(event.Contexts["trace"]["data"], want) {
		t.Errorf("trace data = %v, want %v", event.Contexts["trace"]["data"], want)
	}
	span := event.Spans[0]
	if !reflect.DeepEqual(span.Tags, map[string]string{"rpc.method": "tx"}) || !reflect.DeepEqual(span.Data, map[string]interface{}{"attempt": 1}) {
		t.Errorf("span tags %v and data %v keep a linking key", span.Tags, span.Data)
	}
	breadcrumb := event.Breadcrumbs[0]
	if breadcrumb.Message != "looked up delegate [redacted]" {
		t.Errorf("breadcrumb message = %q", breadcrumb.Message)
	}
	if want := map[string]interface{}{"url": "https://chain.example", "method": "GET"}; !reflect.DeepEqual(breadcrumb.Data, want) {
		t.Errorf("breadcrumb data = %v, want %v", breadcrumb.Data, want)
	}
}

func TestScrubSensitiveRequestEventWithoutRequest(t *testing.T) {
	if scrubEvent(nil) != nil {
		t.Fatal("nil event was changed")
	}
	event := &sentrylib.Event{User: sentrylib.User{IPAddress: "198.51.100.1"}}
	if got := scrubEvent(event); got.User.IPAddress != "" {
		t.Fatal("client address was not scrubbed without a request")
	}
}

func TestHelperHTTPEventsScrubRequestMetadata(t *testing.T) {
	for _, tc := range []struct {
		name        string
		environment string
		panic       bool
		wantTypes   []string
	}{
		{"production success", "production", false, []string{"transaction"}},
		{"production panic", "production", true, []string{"", "transaction"}},
		{"staging panic", "staging", true, []string{""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			transport := initTestSentryWithEnvironment(t, tc.environment)
			req := httptest.NewRequest(http.MethodPost, "https://helper.example/shielded-vote/v1/shares",
				strings.NewReader(`{"primary_blind":"synthetic share material"}`))
			req.RemoteAddr = "198.51.100.1:1234"
			req.Header.Set("Accept", "application/json")
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Content-Length", strconv.FormatInt(req.ContentLength, 10))
			for _, header := range []string{
				"X-Forwarded-For", "X-Real-IP", "CF-Connecting-IP", "CF-Connecting-IPv6",
				"True-Client-IP", "Forwarded", "X-Envoy-External-Address", "X-Custom-Client-IP",
			} {
				req.Header.Set(header, "198.51.100.1")
			}
			req.Header.Set("X-Helper-Token", "synthetic-token")
			req.Header.Set("User-Agent", "synthetic-agent")
			handler := sentryhttp.New(sentryhttp.Options{Repanic: false}).Handle(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if _, err := io.Copy(io.Discard, r.Body); err != nil {
					t.Fatal(err)
				}
				if tc.panic {
					panic("synthetic handler failure")
				}
				w.WriteHeader(http.StatusOK)
			}))
			handler.ServeHTTP(httptest.NewRecorder(), req)

			events := transport.Events()
			if len(events) != len(tc.wantTypes) {
				t.Fatalf("sent %d events, want %d", len(events), len(tc.wantTypes))
			}
			wantHeaders := map[string]string{
				"Accept": "application/json", "Content-Type": "application/json",
				"Content-Length": strconv.FormatInt(req.ContentLength, 10), "Host": "helper.example",
			}
			for i, event := range events {
				if event.Type != tc.wantTypes[i] {
					t.Fatalf("event type = %q, want %q", event.Type, tc.wantTypes[i])
				}
				if event.Request == nil {
					t.Fatal("missing request metadata")
				}
				if !reflect.DeepEqual(event.Request.Headers, wantHeaders) {
					t.Errorf("headers = %v, want %v", event.Request.Headers, wantHeaders)
				}
				if event.Request.Data != "" || len(event.Request.Env) != 0 || event.User.IPAddress != "" {
					t.Error("request body or client address fields were not scrubbed")
				}
			}
		})
	}
}

func TestTraceSampleRate(t *testing.T) {
	tests := []struct {
		name            string
		environment     string
		transactionName string
		want            float64
	}{
		{
			name:            "staging helper",
			environment:     "staging",
			transactionName: "helper.process_share",
			want:            0,
		},
		{
			name:            "staging critical write",
			environment:     "staging",
			transactionName: "POST /shielded-vote/v1/cast-vote",
			want:            0,
		},
		{
			name:            "production helper",
			environment:     "production",
			transactionName: "helper.process_share",
			want:            reducedTraceSampleRate,
		},
		{
			name:            "production concrete share status",
			environment:     "production",
			transactionName: "GET /shielded-vote/v1/share-status/round-1/nullifier-1",
			want:            reducedTraceSampleRate,
		},
		{
			name:            "production concrete round",
			environment:     "production",
			transactionName: "GET /shielded-vote/v1/round/round-1",
			want:            reducedTraceSampleRate,
		},
		{
			name:            "production share status route",
			environment:     "production",
			transactionName: "GET /shielded-vote/v1/share-status/{roundId}/{nullifier}",
			want:            reducedTraceSampleRate,
		},
		{
			name:            "production round route",
			environment:     "production",
			transactionName: "GET /shielded-vote/v1/round/{round_id}",
			want:            reducedTraceSampleRate,
		},
		{
			name:            "production vote managers",
			environment:     "production",
			transactionName: "GET /shielded-vote/v1/vote-managers",
			want:            reducedTraceSampleRate,
		},
		{
			name:            "production rounds prefix boundary",
			environment:     "production",
			transactionName: "GET /shielded-vote/v1/rounds",
			want:            1,
		},
		{
			name:            "production share status prefix boundary",
			environment:     "production",
			transactionName: "GET /shielded-vote/v1/share-status",
			want:            1,
		},
		{
			name:            "production critical write",
			environment:     "production",
			transactionName: "POST /shielded-vote/v1/reveal-share",
			want:            1,
		},
		{
			name:            "unknown environment remains fully traced",
			environment:     "development",
			transactionName: "helper.process_share",
			want:            1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := traceSampleRate(tc.environment, tc.transactionName); got != tc.want {
				t.Fatalf("traceSampleRate(%q, %q) = %v, want %v", tc.environment, tc.transactionName, got, tc.want)
			}
		})
	}
}

func TestStagingDisablesTracingButKeepsErrors(t *testing.T) {
	transport := initTestSentryWithEnvironment(t, "staging")

	_, span := StartSpan(context.Background(), "helper.process_share", "helper.process_share", nil, nil)
	span.Finish(nil)
	_, transaction := StartTransaction(context.Background(), "POST /shielded-vote/v1/cast-vote", nil, nil)
	transaction.Finish(nil)
	CaptureErr(errors.New("staging database unavailable"), nil)

	events := transport.Events()
	if len(events) != 1 {
		t.Fatalf("sent %d events, want only the error event", len(events))
	}
	if len(events[0].Exception) != 1 {
		t.Fatalf("event has %d exceptions, want 1", len(events[0].Exception))
	}
	if events[0].Exception[0].Value != "staging database unavailable" {
		t.Fatalf("exception value = %q, want staging database unavailable", events[0].Exception[0].Value)
	}
}

func TestStartSpanCreatesSearchableRootSpan(t *testing.T) {
	transport := initTestSentry(t)

	_, span := StartSpan(context.Background(), "zkp.prove", "helper.generate_share_reveal_proof", map[string]string{
		"round_id": "round-1",
	}, map[string]interface{}{
		"proof_bytes": 128,
	})
	span.Finish(nil)

	events := transport.Events()
	if len(events) != 1 {
		t.Fatalf("sent %d events, want 1", len(events))
	}
	event := events[0]
	if event.Transaction != "helper.generate_share_reveal_proof" {
		t.Fatalf("transaction = %q, want helper.generate_share_reveal_proof", event.Transaction)
	}
	trace := event.Contexts["trace"]
	if trace["op"] != "zkp.prove" {
		t.Fatalf("trace op = %v, want zkp.prove", trace["op"])
	}
	if event.Tags["round_id"] != "round-1" {
		t.Fatalf("round_id tag = %q, want round-1", event.Tags["round_id"])
	}
}

func TestStartSpanKeepsParentTransactionName(t *testing.T) {
	transport := initTestSentry(t)

	parent := sentrylib.StartSpan(
		context.Background(),
		"http.server",
		sentrylib.WithTransactionName("POST /shielded-vote/v1/cast-vote"),
		sentrylib.WithSpanSampled(sentrylib.SampledTrue),
	)
	_, child := StartSpan(parent.Context(), "zkp.prove", "helper.generate_share_reveal_proof", nil, nil)
	child.Finish(nil)
	parent.Finish()

	events := transport.Events()
	if len(events) != 1 {
		t.Fatalf("sent %d events, want 1", len(events))
	}
	event := events[0]
	if event.Transaction != "POST /shielded-vote/v1/cast-vote" {
		t.Fatalf("transaction = %q, want parent transaction name", event.Transaction)
	}
	if len(event.Spans) != 1 {
		t.Fatalf("event has %d child spans, want 1", len(event.Spans))
	}
	if event.Spans[0].Op != "zkp.prove" {
		t.Fatalf("child op = %q, want zkp.prove", event.Spans[0].Op)
	}
	if event.Spans[0].Description != "helper.generate_share_reveal_proof" {
		t.Fatalf("child description = %q, want helper.generate_share_reveal_proof", event.Spans[0].Description)
	}
}

// TestRootSpansKeepOffTheCurrentHub checks that root spans started from a
// context without a hub never become the span of the shared current hub,
// whose events other goroutines capture, and that an error captured with a
// span's context joins that span's trace.
func TestRootSpansKeepOffTheCurrentHub(t *testing.T) {
	transport := initTestSentry(t)

	ctx, span := StartSpan(context.Background(), "helper.dedupe", "helper.preproof_share_nullifier_check", nil, nil)
	_, transaction := StartTransaction(context.Background(), "helper.reconcile", nil, nil)
	for _, root := range []*TraceSpan{span, transaction} {
		if sentrylib.CurrentHub().Scope().GetSpan() == root.span {
			t.Fatalf("root span %q is the current hub's span", root.span.Name)
		}
	}
	CaptureErrWithGroupingContext(ctx, errors.New("share nullifier lookup failed"), nil, "helper_share_failure")
	span.Finish(nil)
	transaction.Finish(nil)

	events := transport.Events()
	if len(events) != 3 || len(events[0].Exception) != 1 {
		t.Fatalf("sent %d events, want the error and two transactions", len(events))
	}
	if got := events[0].Contexts["trace"]["trace_id"]; got != span.span.TraceID {
		t.Fatalf("error trace_id = %v, want its span's %v", got, span.span.TraceID)
	}
}

// TestRouteTracingNamesTransactionsByRoute checks that RouteTracing names a
// request's transaction by its mux route template, never by the raw path,
// and still recovers panics.
func TestRouteTracingNamesTransactionsByRoute(t *testing.T) {
	transport := initTestSentryWithEnvironment(t, "development")
	router := mux.NewRouter()
	trace := RouteTracing(false)
	router.Handle("/shielded-vote/v1/share-status/{roundId}/{nullifier}", trace(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if mux.Vars(r)["nullifier"] == "panic" {
			panic("synthetic handler failure")
		}
		w.WriteHeader(http.StatusOK)
	}))).Methods(http.MethodGet)

	const route = "GET /shielded-vote/v1/share-status/{roundId}/{nullifier}"
	for _, nullifier := range []string{"nullifier-1", "panic"} {
		router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/shielded-vote/v1/share-status/round-1/"+nullifier, nil))
	}

	var transactions []string
	for _, event := range transport.Events() {
		if event.Type == "transaction" {
			transactions = append(transactions, event.Transaction)
		}
	}
	if !reflect.DeepEqual(transactions, []string{route, route}) {
		t.Fatalf("transactions = %q, want two %q", transactions, route)
	}
}

// TestRouteTracingReducesRequestsToRoute checks that every event a traced
// request reports, its transaction and a panic alike, carries the route
// template as its URL and neither the raw path's values, nor the query
// string, nor the body.
func TestRouteTracingReducesRequestsToRoute(t *testing.T) {
	transport := initTestSentryWithEnvironment(t, "development")
	router := mux.NewRouter()
	trace := RouteTracing(false)
	handler := trace(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			t.Error(err)
		}
		if r.URL.Query().Get("panic") != "" {
			panic("synthetic handler failure")
		}
		w.WriteHeader(http.StatusOK)
	}))
	router.Handle("/shielded-vote/v1/delegates/{index}", handler).Methods(http.MethodGet)
	router.Handle("/shielded-vote/v1/delegate-op", handler).Methods(http.MethodPost)

	// Built, not written out, so source lines Sentry quotes from this file
	// in the panic's stack trace never contain it.
	index := strconv.Itoa(987654320 + 1)
	for _, req := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/shielded-vote/v1/delegates/"+index+"?start_after="+index, nil),
		httptest.NewRequest(http.MethodGet, "/shielded-vote/v1/delegates/"+index+"?panic=1", nil),
		httptest.NewRequest(http.MethodPost, "/shielded-vote/v1/delegate-op",
			strings.NewReader(`{"ballot":{"delegate_index":`+index+`}}`)),
	} {
		router.ServeHTTP(httptest.NewRecorder(), req)
	}

	events := transport.Events()
	if len(events) != 4 {
		t.Fatalf("sent %d events, want 3 transactions and 1 panic", len(events))
	}
	routes := map[string]bool{
		"http://example.com/shielded-vote/v1/delegates/{index}": true,
		"http://example.com/shielded-vote/v1/delegate-op":       true,
	}
	for _, event := range events {
		if event.Request == nil {
			t.Fatalf("event %q has no request", event.Transaction)
		}
		if !routes[event.Request.URL] {
			t.Errorf("request URL = %q, want a route template", event.Request.URL)
		}
		if event.Request.QueryString != "" || event.Request.Data != "" {
			t.Errorf("request %+v keeps its query or body", event.Request)
		}
		encoded, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(encoded), index) {
			t.Errorf("event %s carries the raw index", encoded)
		}
	}
}

func TestCaptureErrWithGrouping(t *testing.T) {
	transport := initTestSentry(t)

	CaptureErrWithGrouping(
		errors.New("temporary submit failure at height 123"),
		map[string]string{
			"alert":    "helper_share_failure",
			"round_id": "round-1",
			"stage":    "submit_http",
		},
		"helper_share_failure",
		"round-1",
		"submit_http",
	)

	events := transport.Events()
	if len(events) != 1 {
		t.Fatalf("sent %d events, want 1", len(events))
	}
	event := events[0]
	wantFingerprint := []string{
		"helper_share_failure",
		"round-1",
		"submit_http",
		"helper-a",
	}
	if !reflect.DeepEqual(event.Fingerprint, wantFingerprint) {
		t.Fatalf("fingerprint = %#v, want %#v", event.Fingerprint, wantFingerprint)
	}
	if event.Tags["alert"] != "helper_share_failure" {
		t.Fatalf("alert tag = %q, want helper_share_failure", event.Tags["alert"])
	}
}

func initTestSentry(t *testing.T) *captureTransport {
	return initTestSentryWithEnvironment(t, "production")
}

func initTestSentryWithEnvironment(t *testing.T, environment string) *captureTransport {
	t.Helper()

	t.Setenv("SENTRY_ENVIRONMENT", environment)
	if err := InitSentry("https://public@example.com/1", "test", "helper-a", log.NewNopLogger()); err != nil {
		t.Fatalf("sentry init: %v", err)
	}
	initialized := sentrylib.CurrentHub().Client()
	options := initialized.Options()
	initialized.Close()
	// Use the production options and hooks with an in-memory transport.
	transport := &captureTransport{}
	options.Transport = transport
	client, err := sentrylib.NewClient(options)
	if err != nil {
		t.Fatalf("sentry init: %v", err)
	}
	sentrylib.CurrentHub().BindClient(client)
	t.Cleanup(func() {
		client.Close()
		sentryEnabled.Store(false)
		tracingEnabled.Store(false)
		sentrylib.CurrentHub().BindClient(nil)
	})

	return transport
}
