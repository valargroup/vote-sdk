package helper

import (
	"database/sql"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// storeMetrics contains bounded operation labels only. Queue gauges are read
// from the authoritative scheduler once per scrape; no SQL or witness data is
// read by the collector. A scrape performs one O(pending) scan under the lock.
type storeMetrics struct {
	lockWait     *prometheus.HistogramVec
	lockHeld     *prometheus.HistogramVec
	sqlWrite     *prometheus.HistogramVec
	scan         *prometheus.HistogramVec
	confirmation prometheus.Histogram
	store        atomic.Pointer[ShareStore]
	depth        *prometheus.Desc
	oldest       *prometheus.Desc
}

func newStoreMetrics(reg prometheus.Registerer) *storeMetrics {
	hist := func(name, help string, labels ...string) *prometheus.HistogramVec {
		return prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: name, Help: help, Buckets: helperDurationBuckets}, labels)
	}
	m := &storeMetrics{
		lockWait: hist("svote_helper_store_lock_wait_seconds", "Wait for the shared queue mutex.", "operation"),
		lockHeld: hist("svote_helper_store_lock_held_seconds", "Time holding the shared queue mutex.", "operation"),
		sqlWrite: hist("svote_helper_store_sql_write_seconds", "Nontransactional queue SQL writes including database pool wait.", "statement", "result"),
		scan:     hist("svote_helper_scheduler_scan_seconds", "Scheduler scan and candidate selection duration excluding mutex wait.", "operation"),
		confirmation: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name: "svote_helper_enqueue_to_confirmation_seconds", Help: "Durable receipt to observed chain confirmation, including scheduled delay; excludes missing legacy timestamps.",
			Buckets: []float64{1, 5, 30, 60, 300, 1800, 3600, 21600, 86400, 172800, 604800, 2592000},
		}),
		depth:  prometheus.NewDesc("svote_helper_queue_depth", "Process-owned queue depth by effective schedule state; excludes terminal rows.", []string{"state"}, nil),
		oldest: prometheus.NewDesc("svote_helper_queue_oldest_ready_age_seconds", "Age since the oldest queued share's effective due time; zero when none are ready.", nil, nil),
	}
	reg.MustRegister(m.lockWait, m.lockHeld, m.sqlWrite, m.scan, m.confirmation, m)
	return m
}

func (m *storeMetrics) Describe(ch chan<- *prometheus.Desc) { ch <- m.depth; ch <- m.oldest }

func (m *storeMetrics) Collect(ch chan<- prometheus.Metric) {
	s := m.store.Load()
	if s == nil {
		return
	}
	now := s.now()
	unlock := s.lockMeasured("metrics")
	ready, future := 0, 0
	oldest := 0.0
	for _, due := range s.schedule {
		if due.After(now) {
			future++
		} else {
			ready++
			oldest = max(oldest, now.Sub(due).Seconds())
		}
	}
	processing := len(s.inFlight)
	unlock()
	for state, count := range map[string]int{"ready": ready, "not_yet_due": future, "processing": processing, "pending": ready + future + processing} {
		ch <- prometheus.MustNewConstMetric(m.depth, prometheus.GaugeValue, float64(count), state)
	}
	ch <- prometheus.MustNewConstMetric(m.oldest, prometheus.GaugeValue, oldest)
}

// lockMeasured returns the matching unlock; observation happens after unlock
// so Prometheus collection never extends the critical section.
func (s *ShareStore) lockMeasured(operation string) func() {
	if s.metrics == nil {
		s.mu.Lock()
		return s.mu.Unlock
	}
	started := time.Now()
	s.mu.Lock()
	acquired := time.Now()
	return func() {
		held := time.Since(acquired).Seconds()
		s.mu.Unlock()
		s.metrics.lockWait.WithLabelValues(operation).Observe(acquired.Sub(started).Seconds())
		s.metrics.lockHeld.WithLabelValues(operation).Observe(held)
	}
}

// execWrite retains SQL outcomes and records only a bounded statement class.
func (s *ShareStore) execWrite(query string, args ...any) (sql.Result, error) {
	if s.metrics == nil {
		return s.db.Exec(query, args...)
	}
	started := time.Now()
	result, err := s.db.Exec(query, args...)
	statement := "other"
	for _, verb := range []string{"INSERT", "UPDATE", "DELETE"} {
		if strings.HasPrefix(strings.TrimSpace(query), verb) {
			statement = strings.ToLower(verb)
			break
		}
	}
	s.metrics.sqlWrite.WithLabelValues(statement, metricResult(err)).Observe(time.Since(started).Seconds())
	return result, err
}

// Registration is lazy: disabled processes expose none of these new families.
var stagingStoreMetrics = sync.OnceValue(func() *storeMetrics { return newStoreMetrics(prometheus.DefaultRegisterer) })

func (s *ShareStore) measureScan(operation string) func() {
	if s.metrics == nil {
		return func() {}
	}
	started := time.Now()
	return func() { s.metrics.scan.WithLabelValues(operation).Observe(time.Since(started).Seconds()) }
}
