package ante

import (
	"context"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/valargroup/vote-sdk/x/vote/types"
)

var verificationDurationBuckets = []float64{
	0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5,
	1, 2.5, 5, 10, 15, 20, 30, 60, 120, 180,
}

type verificationMetrics struct {
	attempts      *prometheus.CounterVec
	inFlight      *prometheus.GaugeVec
	duration      *prometheus.HistogramVec
	stageDuration *prometheus.HistogramVec
	batchSize     *prometheus.HistogramVec
}

func newVerificationMetrics(registerer prometheus.Registerer) *verificationMetrics {
	m := &verificationMetrics{
		attempts: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "svote_vote_tx_verification_attempts_total",
			Help: "Total vote transaction verification attempts by bounded transaction type, mode, outcome, and final stage.",
		}, []string{"tx_type", "mode", "outcome", "stage"}),
		inFlight: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "svote_vote_tx_verification_in_flight",
			Help: "Number of vote transaction verification attempts currently in flight.",
		}, []string{"tx_type", "mode"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "svote_vote_tx_verification_duration_seconds",
			Help:    "End-to-end vote transaction verification duration by bounded transaction type, mode, and outcome.",
			Buckets: verificationDurationBuckets,
		}, []string{"tx_type", "mode", "outcome"}),
		stageDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "svote_vote_tx_verification_stage_duration_seconds",
			Help:    "Vote transaction verification stage duration by bounded transaction type, mode, stage, and result.",
			Buckets: verificationDurationBuckets,
		}, []string{"tx_type", "mode", "stage", "result"}),
		batchSize: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "svote_vote_tx_verification_batch_size",
			Help:    "Number of cast votes in a verified batch transaction by bounded transaction type.",
			Buckets: []float64{1, 2, 5, 10, 20, 30, 40, 50},
		}, []string{"tx_type"}),
	}

	registerer.MustRegister(m.attempts, m.inFlight, m.duration, m.stageDuration, m.batchSize)
	return m
}

var defaultVerificationMetrics = newVerificationMetrics(prometheus.DefaultRegisterer)

type verificationObservation struct {
	metrics *verificationMetrics
	txType  string
	mode    string
	stage   string
	started time.Time
}

func (m *verificationMetrics) begin(ctx context.Context, msg types.VoteMessage, isRecheck bool) *verificationObservation {
	txType := voteMessageType(msg)
	mode := verificationMode(ctx, isRecheck)

	m.inFlight.WithLabelValues(txType, mode).Inc()
	switch typed := msg.(type) {
	case *types.MsgCastVoteBatch:
		m.batchSize.WithLabelValues(txType).Observe(float64(len(typed.Votes)))
	case *types.MsgDelegateAndCastVoteBatch:
		if typed.Batch != nil {
			m.batchSize.WithLabelValues(txType).Observe(float64(len(typed.Batch.Votes)))
		}
	}

	return &verificationObservation{
		metrics: m,
		txType:  txType,
		mode:    mode,
		stage:   "start",
		started: time.Now(),
	}
}

func verificationMode(ctx context.Context, isRecheck bool) string {
	if isRecheck {
		return "recheck_tx"
	}
	if checkTxContext, ok := ctx.(interface{ IsCheckTx() bool }); ok && checkTxContext.IsCheckTx() {
		return "check_tx"
	}
	return "finalize_block"
}

func (o *verificationObservation) observe(stage string, fn func() error) error {
	o.stage = stage
	started := time.Now()
	err := fn()
	result := "ok"
	if err != nil {
		result = "error"
	}
	o.metrics.stageDuration.WithLabelValues(o.txType, o.mode, stage, result).Observe(time.Since(started).Seconds())
	return err
}

func (o *verificationObservation) finish(err error) {
	outcome := "success"
	finalStage := "complete"
	if err != nil {
		outcome = "error"
		finalStage = o.stage
	}
	o.recordFinal(outcome, finalStage)
}

func (o *verificationObservation) finishPanic() {
	o.recordFinal("error", o.stage)
}

func (o *verificationObservation) recordFinal(outcome, finalStage string) {
	o.metrics.attempts.WithLabelValues(o.txType, o.mode, outcome, finalStage).Inc()
	o.metrics.duration.WithLabelValues(o.txType, o.mode, outcome).Observe(time.Since(o.started).Seconds())
	o.metrics.inFlight.WithLabelValues(o.txType, o.mode).Dec()
}

func voteMessageType(msg types.VoteMessage) string {
	switch msg.(type) {
	case *types.MsgCreateVotingSession:
		return "create_voting_session"
	case *types.MsgDelegateVote:
		return "delegate_vote"
	case *types.MsgCastVote:
		return "cast_vote"
	case *types.MsgCastVoteBatch:
		return "cast_vote_batch"
	case *types.MsgDelegateAndCastVoteBatch:
		return "delegate_and_cast_vote_batch"
	case *types.MsgRevealShare:
		return "reveal_share"
	case *types.MsgSubmitTally:
		return "submit_tally"
	default:
		return "unknown"
	}
}
