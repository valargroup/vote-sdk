package helper

import (
	"context"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cosmossdk.io/log"
	"github.com/gorilla/mux"
	metricsprom "github.com/hashicorp/go-metrics/prometheus"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"
)

func TestMetricsRouteExposesSDKAndHelperMetrics(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics := newHelperMetrics(registry)
	sdkSink, err := metricsprom.NewPrometheusSinkFrom(metricsprom.PrometheusOpts{
		Name:       "cosmos_sdk_test_sink",
		Registerer: registry,
		Expiration: time.Minute,
	})
	require.NoError(t, err)
	sdkSink.SetGauge([]string{"cosmos", "sdk", "latest_height"}, 42)

	observation := metrics.beginShareSubmission()
	observation.start = time.Now().Add(-16 * time.Second)
	observation.recordOutcome("accepted", "queued")
	observation.finish()
	metrics.observeSubmissionStage("enqueue", time.Now().Add(-16*time.Second), nil)

	router := mux.NewRouter()
	registerMetricsRoute(router, registry)
	// Cosmos SDK registers its handler after the application routes. Mirror
	// that ordering and prove the first route serves both metric sources.
	router.HandleFunc("/metrics", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}).Methods(http.MethodGet)
	request := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	require.Equal(t, http.StatusOK, response.Code)
	require.Contains(t, response.Header().Get("Content-Type"), "text/plain")
	body := response.Body.String()
	require.Contains(t, body, "cosmos_sdk_latest_height 42")
	require.Contains(t, body, "svote_helper_share_submission_requests_total")
	require.Contains(t, body, `svote_helper_share_submission_duration_seconds_bucket{outcome="accepted",reason="queued",le="15"} 0`)
	require.Contains(t, body, `svote_helper_share_submission_duration_seconds_bucket{outcome="accepted",reason="queued",le="20"} 1`)
	require.NotContains(t, body, "round_id")
	require.NotContains(t, body, "share_index")
	require.NotContains(t, body, "proposal_id")
	require.NotContains(t, body, "tree_position")
}

func TestSubmitShareRecordsPrometheusOutcome(t *testing.T) {
	counter := defaultHelperMetrics.shareSubmissionRequests.WithLabelValues("rejected", "invalid_json")
	before := testutil.ToFloat64(counter)

	router, _ := newTestRouter(t)
	request := httptest.NewRequest(http.MethodPost, "/shielded-vote/v1/shares", strings.NewReader("{"))
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	require.Equal(t, http.StatusBadRequest, response.Code)
	require.Equal(t, before+1, testutil.ToFloat64(counter))
	require.Equal(t, float64(0), testutil.ToFloat64(defaultHelperMetrics.shareSubmissionInFlight))
}

func TestProcessorRecordsRetryStage(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics := newHelperMetrics(registry)
	store := newTestStore(t)
	tree := newMockTreeReader()
	processor := NewProcessor(store, tree, &mockProver{}, nil, log.NewNopLogger(), 1, nil)
	processor.metrics = metrics

	roundID := hex.EncodeToString(make([]byte, 32))
	payload := testPayload(roundID, 0)
	payload.TreePosition = tree.leafCount
	enqueueInserted(t, store, payload)
	ready := store.TakeReady()
	require.Len(t, ready, 1)

	processor.processQueuedShare(context.Background(), ready[0])

	require.Equal(t, float64(1), testutil.ToFloat64(
		metrics.shareProcessingAttempts.WithLabelValues("retry", failureStageTreeStatus),
	))
	require.Equal(t, float64(0), testutil.ToFloat64(metrics.shareProcessingInFlight))
	require.Equal(t, uint64(1), histogramSampleCount(t,
		metrics.shareProcessingDuration.WithLabelValues("retry", failureStageTreeStatus),
	))
	require.Equal(t, uint64(1), histogramSampleCount(t,
		metrics.shareProcessingStages.WithLabelValues("tree_status", "error"),
	))
}

func TestMetricResult(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "success", want: "ok"},
		{name: "failure", err: errors.New("failed"), want: "error"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, metricResult(tt.err))
		})
	}
}

func histogramSampleCount(t *testing.T, observer prometheus.Observer) uint64 {
	t.Helper()
	metric, ok := observer.(prometheus.Metric)
	require.True(t, ok)
	encoded := &dto.Metric{}
	require.NoError(t, metric.Write(encoded))
	require.NotNil(t, encoded.Histogram)
	return encoded.GetHistogram().GetSampleCount()
}
