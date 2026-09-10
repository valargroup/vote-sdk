package helper

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"testing"
	"time"

	"cosmossdk.io/log"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testCommitmentDeduper(check ShareNullifierChecker) ProcessorOption {
	return WithPreProofShareDeduper(
		func(_, _ [32]byte, _, _ uint32) ([32]byte, error) { return [32]byte{}, nil },
		func(_ [32]byte, index uint32, _ [32]byte) ([32]byte, error) { return [32]byte{byte(index)}, nil },
		check,
	)
}

func TestProcessorInactiveRoundReconcilesCommitment(t *testing.T) {
	for _, tc := range []struct {
		name      string
		committed bool
		checkErr  error
		wantState ShareState
		attempts  int
		invalid   bool
	}{
		{name: "committed", committed: true, wantState: ShareStateSubmitted},
		{name: "uncommitted", wantState: ShareStateReceived, attempts: 1},
		{name: "check unavailable", checkErr: assert.AnError, wantState: ShareStateReceived},
		{name: "invalid local witness", invalid: true, wantState: ShareStateReceived, attempts: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newTestStore(t)
			roundID := hex.EncodeToString(make([]byte, 32))
			payload := testPayload(roundID, 0)
			if tc.invalid {
				payload.PrimaryBlind = "invalid"
			}
			enqueueAndRequireInserted(t, store, payload)
			ready := store.TakeReady()
			require.Len(t, ready, 1)
			now := time.Now()
			state := retryState{
				FirstAttempt: now.Add(-72 * time.Hour), FinalAttempt: now.Add(-24 * time.Hour),
				NextSlot: 5, LastAttempt: now.Add(-24 * time.Hour), LastHeight: 100,
			}
			require.NoError(t, store.reserveProofAttempt(ready[0], state))
			store.MarkRetry(roundID, 0, 1, 0)
			store.schedule[schedKey(roundID, 0, 1, 0)] = now.Add(-time.Second)
			checks := 0
			prover := &mockProver{}
			proc := NewProcessor(store, nil, prover, nil, log.NewNopLogger(), 1,
				func(string) (bool, error) { return false, nil },
				testCommitmentDeduper(func(string, []byte) (bool, error) {
					checks++
					return tc.committed, tc.checkErr
				}),
			)
			proc.metrics = newHelperMetrics(prometheus.NewRegistry())
			proc.processBatch(context.Background())
			outcome, stage := "inactive", "round_status"
			if tc.committed {
				outcome, stage = "confirmed", "confirmation"
			} else if tc.checkErr != nil {
				outcome, stage = "retry", failureStageCommitmentCheck
			} else if tc.invalid {
				outcome, stage = "failed", failureStageCommitmentCheck
			}
			assert.Equal(t, float64(1), testutil.ToFloat64(proc.metrics.shareProcessingAttempts.WithLabelValues(outcome, stage)))
			assert.Zero(t, testutil.ToFloat64(proc.metrics.shareProcessingInFlight))
			share, ok := store.loadShare(roundID, 0, 1, 0)
			require.True(t, ok)
			if tc.invalid {
				assert.Zero(t, checks)
			} else {
				assert.Equal(t, 1, checks)
			}
			assert.Equal(t, tc.wantState, share.State)
			assert.Equal(t, tc.attempts, share.Attempts)
			assert.Zero(t, prover.callCount.Load())
			if tc.committed {
				assert.Empty(t, share.Payload.PrimaryBlind)
				assert.Empty(t, share.Payload.EncShare.C1)
			} else {
				assert.Equal(t, payload.PrimaryBlind, share.Payload.PrimaryBlind)
			}
		})
	}
}

func TestProcessorCleanupInvalidRowsDoNotBlockRound(t *testing.T) {
	for _, tc := range []struct {
		name           string
		mutate         func(*SharePayload)
		failVC, failNF bool
		state          ShareState
	}{
		{name: "invalid shares hash", mutate: func(p *SharePayload) { p.SharesHash = "invalid" }},
		{name: "short shares hash", mutate: func(p *SharePayload) { p.SharesHash = "AQ==" }},
		{name: "invalid primary blind", mutate: func(p *SharePayload) { p.PrimaryBlind = "invalid" }},
		{name: "legacy missing blind", state: ShareStateFailed, mutate: func(p *SharePayload) { p.PrimaryBlind = "" }},
		{name: "commitment hash failure", failVC: true},
		{name: "nullifier hash failure", failNF: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, err := NewShareStore(":memory:", nil)
			require.NoError(t, err)
			t.Cleanup(func() { store.Close() })
			end := uint64(time.Now().Add(-time.Minute).Unix())
			roundID := hex.EncodeToString(make([]byte, 32))
			export := QueueExport{
				Version: QueueExportVersion, RoundID: roundID,
				Round: QueueExportRound{CreatedAtTime: end - 3600, VoteEndTime: end},
			}
			for index := range uint32(2) {
				payload := testPayload(roundID, index)
				hash := [32]byte{byte(index)}
				payload.SharesHash = base64.StdEncoding.EncodeToString(hash[:])
				if index == 0 && tc.mutate != nil {
					tc.mutate(&payload)
				}
				export.Rows = append(export.Rows, QueueExportRow{
					ShareIndex: index, SharesHash: payload.SharesHash, ProposalID: payload.ProposalID,
					VoteDecision: payload.VoteDecision, EncShare: payload.EncShare,
					PrimaryBlind: payload.PrimaryBlind, ShareComms: payload.ShareComms,
					State: ShareStateReceived, VoteEndTime: end, ReceivedAt: end - 20,
				})
			}
			result, err := store.ImportQueue(export, QueueImportOptions{})
			require.NoError(t, err)
			require.Equal(t, 2, result.Inserted)
			_, err = store.db.Exec("UPDATE shares SET state = ? WHERE share_index = 0", tc.state)
			require.NoError(t, err)
			checks := 0
			var logs bytes.Buffer
			proc := NewProcessor(store, nil, nil, nil, log.NewLogger(&logs, log.OutputJSONOption()), 1, nil,
				WithProcessingReadinessCheck(func() bool { return true }),
				WithRoundClosureCheck(func(string) (bool, error) { return true, nil }),
				WithPreProofShareDeduper(
					func(_ [32]byte, hash [32]byte, _, _ uint32) ([32]byte, error) {
						if tc.failVC && hash[0] == 0 {
							return [32]byte{}, assert.AnError
						}
						return hash, nil
					},
					func(hash [32]byte, index uint32, _ [32]byte) ([32]byte, error) {
						if tc.failNF && index == 0 {
							return [32]byte{}, assert.AnError
						}
						return hash, nil
					},
					func(_ string, nullifier []byte) (bool, error) {
						checks++
						assert.Equal(t, byte(1), nullifier[0])
						return true, nil
					},
				),
			)
			proc.cleanupClosedRounds()
			assert.Zero(t, store.Status()[roundID].Total, "an invalid row must not retain the round's witnesses")
			assert.Equal(t, 1, checks, "the valid row must still be reconciled")
			assert.Contains(t, logs.String(), "round closed with unsubmitted shares")
			assert.Contains(t, logs.String(), `"unsubmitted":1`)
			assert.Contains(t, logs.String(), `"submitted":1`)
			assert.NotContains(t, logs.String(), "retaining shares")
		})
	}
}

func TestProcessorCleanupReconcilesCommitment(t *testing.T) {
	for _, queue := range []struct {
		name  string
		state ShareState
	}{
		{"pending", ShareStateReceived},
		{"failed", ShareStateFailed},
	} {
		for _, tc := range []struct {
			name      string
			committed bool
			checkErr  error
		}{
			{name: "committed", committed: true},
			{name: "uncommitted"},
			{name: "check unavailable", checkErr: assert.AnError},
		} {
			t.Run(queue.name+"/"+tc.name, func(t *testing.T) {
				end := uint64(time.Now().Add(-time.Minute).Unix())
				store, err := NewShareStore(":memory:", func(string) (RoundInfo, error) {
					return RoundInfo{CreatedAtTime: end - 3600, VoteEndTime: end}, nil
				})
				require.NoError(t, err)
				t.Cleanup(func() { store.Close() })
				roundID := hex.EncodeToString(make([]byte, 32))
				for index := range uint32(3) {
					enqueueAndRequireInserted(t, store, testPayload(roundID, index))
				}
				require.Len(t, store.TakeReady(), 3)
				store.MarkSubmitted(roundID, 1, 1, 0)
				_, err = store.db.Exec("UPDATE shares SET state = ?, received_at = ? WHERE share_index = 0", queue.state, end-20)
				require.NoError(t, err)
				// The submitted row is already scrubbed. The third row arrived
				// after closure and is excluded from unsubmitted-share alerts.
				_, err = store.db.Exec("UPDATE shares SET received_at = ? WHERE share_index = 1", end-20)
				require.NoError(t, err)
				checks := 0
				committed, checkErr := tc.committed, tc.checkErr
				var logs bytes.Buffer
				prover := &mockProver{}
				proc := NewProcessor(store, nil, prover, nil, log.NewLogger(&logs), 1, nil,
					WithProcessingReadinessCheck(func() bool { return true }),
					WithRoundClosureCheck(func(string) (bool, error) { return true, nil }),
					testCommitmentDeduper(func(_ string, nullifier []byte) (bool, error) {
						checks++
						assert.Zero(t, nullifier[0], "only the unconfirmed pre-close share needs checking")
						return committed, checkErr
					}),
				)
				proc.cleanupClosedRounds()
				assert.Equal(t, 1, checks)
				assert.Zero(t, prover.callCount.Load())
				if checkErr != nil {
					require.Equal(t, 3, store.Status()[roundID].Total)
					share, ok := store.loadShare(roundID, 0, 1, 0)
					require.True(t, ok)
					assert.NotEmpty(t, share.Payload.PrimaryBlind)
					assert.NotContains(t, logs.String(), "round closed with unsubmitted shares")
					committed, checkErr = true, nil
					proc.cleanupClosedRounds()
					assert.Equal(t, 2, checks)
				}
				assert.Zero(t, store.Status()[roundID].Total)
				if !tc.committed && tc.checkErr == nil {
					assert.Contains(t, logs.String(), "round closed with unsubmitted shares")
				} else {
					assert.NotContains(t, logs.String(), "round closed with unsubmitted shares")
				}
			})
		}
	}
}
