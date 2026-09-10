package helper

import (
	"net/http"
	"time"

	"github.com/gorilla/mux"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var helperDurationBuckets = []float64{
	0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5,
	1, 2.5, 5, 10, 15, 20, 30, 60, 120, 180,
}

type helperMetrics struct {
	shareSubmissionRequests *prometheus.CounterVec
	shareSubmissionInFlight prometheus.Gauge
	shareSubmissionDuration *prometheus.HistogramVec
	shareSubmissionStages   *prometheus.HistogramVec
	shareProcessingAttempts *prometheus.CounterVec
	shareProcessingInFlight prometheus.Gauge
	shareProcessingDuration *prometheus.HistogramVec
	shareProcessingStages   *prometheus.HistogramVec
}

func newHelperMetrics(registerer prometheus.Registerer) *helperMetrics {
	m := &helperMetrics{
		shareSubmissionRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "svote_helper_share_submission_requests_total",
			Help: "Total wallet share submission requests by bounded outcome and reason.",
		}, []string{"outcome", "reason"}),
		shareSubmissionInFlight: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "svote_helper_share_submission_in_flight",
			Help: "Number of wallet share submission requests currently being handled.",
		}),
		shareSubmissionDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "svote_helper_share_submission_duration_seconds",
			Help:    "End-to-end wallet share submission request duration by bounded outcome and reason.",
			Buckets: helperDurationBuckets,
		}, []string{"outcome", "reason"}),
		shareSubmissionStages: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "svote_helper_share_submission_stage_duration_seconds",
			Help:    "Wallet share submission stage duration by stage and result.",
			Buckets: helperDurationBuckets,
		}, []string{"stage", "result"}),
		shareProcessingAttempts: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "svote_helper_share_processing_attempts_total",
			Help: "Total background share processing attempts by bounded outcome and final stage.",
		}, []string{"outcome", "stage"}),
		shareProcessingInFlight: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "svote_helper_share_processing_in_flight",
			Help: "Number of background share processing attempts currently in flight.",
		}),
		shareProcessingDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "svote_helper_share_processing_duration_seconds",
			Help:    "End-to-end background share processing duration by bounded outcome and final stage.",
			Buckets: helperDurationBuckets,
		}, []string{"outcome", "stage"}),
		shareProcessingStages: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "svote_helper_share_processing_stage_duration_seconds",
			Help:    "Background share processing stage duration by stage and result.",
			Buckets: helperDurationBuckets,
		}, []string{"stage", "result"}),
	}

	registerer.MustRegister(
		m.shareSubmissionRequests,
		m.shareSubmissionInFlight,
		m.shareSubmissionDuration,
		m.shareSubmissionStages,
		m.shareProcessingAttempts,
		m.shareProcessingInFlight,
		m.shareProcessingDuration,
		m.shareProcessingStages,
	)
	return m
}

var defaultHelperMetrics = newHelperMetrics(prometheus.DefaultRegisterer)

type shareSubmissionObservation struct {
	metrics  *helperMetrics
	start    time.Time
	outcome  string
	reason   string
	recorded bool
}

func (m *helperMetrics) beginShareSubmission() *shareSubmissionObservation {
	m.shareSubmissionInFlight.Inc()
	return &shareSubmissionObservation{
		metrics: m,
		start:   time.Now(),
		outcome: "failed",
		reason:  "panic_or_unclassified",
	}
}

func (o *shareSubmissionObservation) recordOutcome(outcome, reason string) {
	o.outcome = outcome
	o.reason = reason
	o.recorded = true
	o.metrics.shareSubmissionRequests.WithLabelValues(outcome, reason).Inc()
	recordShareSubmissionOutcome(outcome, reason)
}

func (o *shareSubmissionObservation) finish() {
	if !o.recorded {
		o.metrics.shareSubmissionRequests.WithLabelValues(o.outcome, o.reason).Inc()
		recordShareSubmissionOutcome(o.outcome, o.reason)
	}
	o.metrics.shareSubmissionDuration.WithLabelValues(o.outcome, o.reason).Observe(time.Since(o.start).Seconds())
	o.metrics.shareSubmissionInFlight.Dec()
}

func (m *helperMetrics) observeSubmissionStage(stage string, start time.Time, err error) {
	m.shareSubmissionStages.WithLabelValues(stage, metricResult(err)).Observe(time.Since(start).Seconds())
}

type shareProcessingObservation struct {
	metrics *helperMetrics
	start   time.Time
}

func (m *helperMetrics) beginShareProcessing() *shareProcessingObservation {
	m.shareProcessingInFlight.Inc()
	return &shareProcessingObservation{metrics: m, start: time.Now()}
}

func (o *shareProcessingObservation) finish(outcome, stage string) {
	o.metrics.shareProcessingAttempts.WithLabelValues(outcome, stage).Inc()
	o.metrics.shareProcessingDuration.WithLabelValues(outcome, stage).Observe(time.Since(o.start).Seconds())
	o.metrics.shareProcessingInFlight.Dec()
}

func (m *helperMetrics) observeProcessingStage(stage string, start time.Time, err error) {
	m.shareProcessingStages.WithLabelValues(stage, metricResult(err)).Observe(time.Since(start).Seconds())
}

func metricResult(err error) string {
	if err != nil {
		return "error"
	}
	return "ok"
}

// RegisterMetricsRoute exposes the process-wide Prometheus registry using the
// conventional unparameterized metrics endpoint. Cosmos SDK's Prometheus sink
// registers with the same default registry, so this handler serves both the
// direct svote collectors and SDK telemetry.
func RegisterMetricsRoute(router *mux.Router) {
	registerMetricsRoute(router, prometheus.DefaultGatherer)
}

func registerMetricsRoute(router *mux.Router, gatherer prometheus.Gatherer) {
	router.Handle("/metrics", promhttp.HandlerFor(gatherer, promhttp.HandlerOpts{})).Methods(http.MethodGet)
}
