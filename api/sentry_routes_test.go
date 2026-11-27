package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/valargroup/vote-sdk/sentry/sentrytest"
)

// TestSentryEventsCarryRouteTemplates requests every REST route, the proxy
// delegation routes among them (open under -tags proxyenabled), with one raw
// delegate index in every path variable, the query and the body. It fails if
// an event Sentry would receive carries that index, or a request URL other
// than its route template (plan PRV-6).
func TestSentryEventsCarryRouteTemplates(t *testing.T) {
	transport := sentrytest.Capture(t)
	comet := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("a route reached CometBFT")
	}))
	defer comet.Close()
	router := proxyRouter(comet.URL, func(string, []byte) (proto.Message, error) {
		return nil, status.Error(codes.NotFound, "not found")
	})

	const index = "987654321"
	pathVariable := regexp.MustCompile(`\{[^}]+\}`)
	urls := map[string]string{}
	requests := 0
	require.NoError(t, router.Walk(func(route *mux.Route, _ *mux.Router, _ []*mux.Route) error {
		template, err := route.GetPathTemplate()
		if err != nil {
			return err
		}
		methods, err := route.GetMethods()
		if err != nil {
			return err
		}
		for _, method := range methods {
			var body io.Reader
			if method == http.MethodPost {
				body = strings.NewReader(`{"ballot":{"delegate_index":` + index + `}}`)
			}
			path := pathVariable.ReplaceAllString(template, index) + "?start_after=" + index
			router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(method, path, body))
			urls[method+" "+template] = "http://example.com" + template
			requests++
		}
		return nil
	}))
	require.Contains(t, urls, "GET /shielded-vote/v1/delegates/{index}")
	require.Contains(t, urls, "POST /shielded-vote/v1/delegate-op")

	events := transport.Events()
	require.Len(t, events, requests, "one transaction per request")
	for _, event := range events {
		encoded, err := json.Marshal(event)
		require.NoError(t, err)
		require.NotContains(t, string(encoded), index, "event for %s", event.Transaction)
		require.Contains(t, urls, event.Transaction, "transactions are named by route")
		require.NotNil(t, event.Request, event.Transaction)
		require.Equal(t, urls[event.Transaction], event.Request.URL, event.Transaction)
	}
}
