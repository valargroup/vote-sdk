package helper

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

func TestQueueMetricsFollowEffectiveSchedule(t *testing.T) {
	store := newTestStore(t)
	store.metrics = newStoreMetrics(prometheus.NewRegistry())
	now := time.Unix(1_800_000_000, 0)
	store.now = func() time.Time { return now }
	store.schedule["opaque-ready"] = now.Add(-12 * time.Second)
	store.schedule["opaque-future"] = now.Add(time.Hour)
	store.inFlight["opaque-processing"] = inFlightShare{}
	registry := prometheus.NewRegistry()
	metrics := newStoreMetrics(registry)
	metrics.store.Store(store)
	families, err := registry.Gather()
	require.NoError(t, err)
	counts := map[string]float64{}
	for _, family := range families {
		switch family.GetName() {
		case "svote_helper_queue_depth":
			for _, metric := range family.Metric {
				counts[metric.Label[0].GetValue()] = metric.Gauge.GetValue()
			}
		case "svote_helper_queue_oldest_ready_age_seconds":
			require.Equal(t, float64(12), family.Metric[0].Gauge.GetValue())
		}
	}
	require.Equal(t, map[string]float64{"ready": 1, "not_yet_due": 1, "processing": 1, "pending": 3}, counts)
}

func TestQueueConfirmationTimingUsesDurableReceipt(t *testing.T) {
	store := newTestStore(t)
	store.metrics = newStoreMetrics(prometheus.NewRegistry())
	now := time.Now().Truncate(time.Second)
	store.now = func() time.Time { return now }
	payload := testPayload("round-timing", 0)
	enqueueInserted(t, store, payload)
	ready := store.TakeReady()
	require.Len(t, ready, 1)
	before := histogramSampleCount(t, store.metrics.confirmation)
	now = now.Add(7 * time.Second)
	store.MarkSubmitted(payload.VoteRoundID, payload.EncShare.ShareIndex, payload.ProposalID, payload.TreePosition)
	require.Equal(t, before+1, histogramSampleCount(t, store.metrics.confirmation))
	store.MarkSubmitted(payload.VoteRoundID, payload.EncShare.ShareIndex, payload.ProposalID, payload.TreePosition)
	require.Equal(t, before+1, histogramSampleCount(t, store.metrics.confirmation))
}
