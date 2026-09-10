package ante

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"

	"github.com/valargroup/vote-sdk/x/vote/types"
)

func TestVerificationMetricsRecordBatchLatencyAndSize(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics := newVerificationMetrics(registry)
	msg := &types.MsgDelegateAndCastVoteBatch{
		Batch: &types.MsgCastVoteBatch{Votes: []*types.MsgCastVote{{}, {}}},
	}

	observation := metrics.begin(context.Background(), msg, false)
	observation.started = time.Now().Add(-16 * time.Second)
	require.NoError(t, observation.observe("cast_proof", func() error { return nil }))
	observation.finish(nil)

	require.Equal(t, float64(1), testutil.ToFloat64(metrics.attempts.WithLabelValues(
		"delegate_and_cast_vote_batch", "finalize_block", "success", "complete",
	)))
	require.Equal(t, float64(0), testutil.ToFloat64(metrics.inFlight.WithLabelValues(
		"delegate_and_cast_vote_batch", "finalize_block",
	)))
	require.Equal(t, uint64(1), verificationHistogramCount(t, metrics.stageDuration.WithLabelValues(
		"delegate_and_cast_vote_batch", "finalize_block", "cast_proof", "ok",
	)))
	require.Equal(t, uint64(1), verificationHistogramCount(t, metrics.batchSize.WithLabelValues(
		"delegate_and_cast_vote_batch",
	)))

	duration := verificationHistogram(t, metrics.duration.WithLabelValues(
		"delegate_and_cast_vote_batch", "finalize_block", "success",
	))
	require.Equal(t, uint64(0), verificationBucketCount(t, duration, 15))
	require.Equal(t, uint64(1), verificationBucketCount(t, duration, 20))
}

func TestVerificationMetricsRecordFailureStage(t *testing.T) {
	metrics := newVerificationMetrics(prometheus.NewRegistry())
	observation := metrics.begin(context.Background(), &types.MsgDelegateVote{}, false)
	wantErr := errors.New("invalid proof")

	err := observation.observe("delegation_proof", func() error { return wantErr })
	observation.finish(err)

	require.ErrorIs(t, err, wantErr)
	require.Equal(t, float64(1), testutil.ToFloat64(metrics.attempts.WithLabelValues(
		"delegate_vote", "finalize_block", "error", "delegation_proof",
	)))
	require.Equal(t, uint64(1), verificationHistogramCount(t, metrics.stageDuration.WithLabelValues(
		"delegate_vote", "finalize_block", "delegation_proof", "error",
	)))
}

func TestValidateVoteTxRecordsAndRepanicsStagePanic(t *testing.T) {
	metrics := newVerificationMetrics(prometheus.NewRegistry())
	originalMetrics := defaultVerificationMetrics
	defaultVerificationMetrics = metrics
	t.Cleanup(func() {
		defaultVerificationMetrics = originalMetrics
	})

	require.PanicsWithValue(t, "verifier panic", func() {
		_ = ValidateVoteTx(context.Background(), panicVoteMessage{}, nil, ValidateOpts{})
	})
	require.Equal(t, float64(1), testutil.ToFloat64(metrics.attempts.WithLabelValues(
		"unknown", "finalize_block", "error", "basic_validation",
	)))
	require.Equal(t, float64(0), testutil.ToFloat64(metrics.inFlight.WithLabelValues(
		"unknown", "finalize_block",
	)))
	require.Equal(t, uint64(1), verificationHistogramCount(t, metrics.duration.WithLabelValues(
		"unknown", "finalize_block", "error",
	)))
}

type panicVoteMessage struct{}

func (panicVoteMessage) ValidateBasic() error {
	panic("verifier panic")
}

func (panicVoteMessage) GetVoteRoundId() []byte {
	return nil
}

func (panicVoteMessage) GetNullifiers() [][]byte {
	return nil
}

func (panicVoteMessage) GetNullifierType() types.NullifierType {
	return 0
}

func (panicVoteMessage) AcceptsTallyingRound() bool {
	return false
}

func TestVoteMessageType(t *testing.T) {
	tests := []struct {
		name string
		msg  types.VoteMessage
		want string
	}{
		{name: "delegate", msg: &types.MsgDelegateVote{}, want: "delegate_vote"},
		{name: "cast batch", msg: &types.MsgCastVoteBatch{}, want: "cast_vote_batch"},
		{name: "delegate and cast", msg: &types.MsgDelegateAndCastVoteBatch{}, want: "delegate_and_cast_vote_batch"},
		{name: "reveal share", msg: &types.MsgRevealShare{}, want: "reveal_share"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, voteMessageType(tt.msg))
		})
	}
}

func TestVerificationMode(t *testing.T) {
	tests := []struct {
		name      string
		ctx       context.Context
		isRecheck bool
		want      string
	}{
		{name: "finalize block", ctx: context.Background(), want: "finalize_block"},
		{name: "check tx", ctx: checkTxTestContext{Context: context.Background()}, want: "check_tx"},
		{name: "recheck takes precedence", ctx: checkTxTestContext{Context: context.Background()}, isRecheck: true, want: "recheck_tx"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, verificationMode(tt.ctx, tt.isRecheck))
		})
	}
}

type checkTxTestContext struct {
	context.Context
}

func (checkTxTestContext) IsCheckTx() bool {
	return true
}

func verificationHistogramCount(t *testing.T, observer prometheus.Observer) uint64 {
	t.Helper()
	return verificationHistogram(t, observer).GetSampleCount()
}

func verificationHistogram(t *testing.T, observer prometheus.Observer) *dto.Histogram {
	t.Helper()
	metric, ok := observer.(prometheus.Metric)
	require.True(t, ok)
	encoded := &dto.Metric{}
	require.NoError(t, metric.Write(encoded))
	require.NotNil(t, encoded.Histogram)
	return encoded.Histogram
}

func verificationBucketCount(t *testing.T, histogram *dto.Histogram, upperBound float64) uint64 {
	t.Helper()
	for _, bucket := range histogram.Bucket {
		if bucket.GetUpperBound() == upperBound {
			return bucket.GetCumulativeCount()
		}
	}
	require.FailNow(t, "histogram bucket not found", "upper bound: %v", upperBound)
	return 0
}
