package helper

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"cosmossdk.io/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProcessorCommitmentLifecycle(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"tx_hash":"OK","code":0}`))
	}))
	defer server.Close()

	for _, seed := range []string{"fresh", "migrated", "waiting", "exhausted", "past cutoff", "corrupt schedule"} {
		for _, phase := range []string{"active", "awaiting closure", "closed"} {
			for _, outcome := range []string{"committed", "uncommitted", "unavailable", "invalid"} {
				for _, restart := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/%s/%s/restart=%t", seed, phase, outcome, restart), func(t *testing.T) {
						now := time.Now().Truncate(time.Second)
						end := now.Add(time.Hour)
						if phase != "active" {
							end = now.Add(-time.Minute)
						}
						path := filepath.Join(t.TempDir(), "helper.db")
						store, err := NewShareStore(path, func(string) (RoundInfo, error) {
							return RoundInfo{CreatedAtTime: uint64(now.Add(-7 * 24 * time.Hour).Unix()), VoteEndTime: uint64(end.Unix())}, nil
						})
						require.NoError(t, err)
						t.Cleanup(func() { store.Close() })
						roundID := hex.EncodeToString(make([]byte, 32))
						payload := testPayload(roundID, 0)
						if outcome == "invalid" {
							payload.PrimaryBlind = "invalid"
						}
						enqueueAndRequireInserted(t, store, payload)
						_, err = store.db.Exec("UPDATE shares SET received_at = ?", end.Add(-time.Hour).Unix())
						require.NoError(t, err)
						attempts := 0
						switch seed {
						case "migrated":
							attempts = 4
							_, err = store.db.Exec("UPDATE shares SET attempts = ?", attempts)
							require.NoError(t, err)
							_, err = store.db.Exec("ALTER TABLE shares DROP COLUMN retry_state")
							require.NoError(t, err)
							require.NoError(t, store.Close())
							store, err = NewShareStore(path, nil)
							require.NoError(t, err)
						case "waiting", "exhausted", "past cutoff":
							ready := store.TakeReady()
							require.Len(t, ready, 1)
							first, final := now.Add(-time.Hour), now.Add(30*time.Minute)
							if phase != "active" || seed == "past cutoff" {
								first, final = now.Add(-72*time.Hour), now.Add(-24*time.Hour)
							}
							state := retryState{FirstAttempt: first, FinalAttempt: final, NextSlot: 4, LastAttempt: first, LastHeight: 99}
							if seed == "exhausted" {
								state.NextSlot = 5
							}
							require.NoError(t, store.reserveProofAttempt(ready[0], state))
							store.MarkRetry(roundID, 0, 1, 0)
						case "corrupt schedule":
							_, err = store.db.Exec("UPDATE shares SET retry_state = 'invalid'")
							require.NoError(t, err)
						}
						if restart {
							require.NoError(t, store.Close())
							store, err = NewShareStore(path, nil)
							require.NoError(t, err)
						}

						committed, unavailable := outcome == "committed", outcome == "unavailable"
						checks := 0
						prover := &mockProver{}
						tree := newMockTreeReader()
						tree.blockHeight.Store(100)
						var logs bytes.Buffer
						proc := NewProcessor(store, tree, prover, NewChainSubmitter(server.URL), log.NewLogger(&logs, log.OutputJSONOption()), 1,
							func(string) (bool, error) { return phase != "closed", nil },
							WithProcessingReadinessCheck(func() bool { return true }),
							WithRoundClosureCheck(func(string) (bool, error) { return phase == "closed", nil }),
							testCommitmentDeduper(func(string, []byte) (bool, error) {
								checks++
								if unavailable {
									return false, assert.AnError
								}
								return committed, nil
							}))
						proc.now = func() time.Time { return now }
						store.schedule[schedKey(roundID, 0, 1, 0)] = time.Now().Add(-time.Second)
						proc.processBatch(context.Background())

						share, ok := store.loadShare(roundID, 0, 1, 0)
						require.True(t, ok)
						wantState := ShareStateReceived
						if committed {
							wantState = ShareStateSubmitted
						} else if outcome == "invalid" || (phase == "closed" && !unavailable) {
							attempts++
							if attempts == 5 {
								wantState = ShareStateFailed
							}
						}
						assert.Equal(t, wantState, share.State)
						assert.Equal(t, attempts, share.Attempts)
						wantProofs := int32(0)
						if phase != "closed" && (seed == "fresh" || seed == "migrated") && (outcome == "uncommitted" || unavailable) {
							wantProofs = 1
						}
						assert.Equal(t, wantProofs, prover.callCount.Load())
						wantChecks := 1
						if outcome == "invalid" {
							wantChecks = 0
						}
						assert.Equal(t, wantChecks, checks, "one commitment decision per queue pass")
						if wantState == ShareStateReceived {
							next, scheduled := store.NextScheduledTime()
							require.True(t, scheduled)
							assert.True(t, next.After(time.Now()), "polling must back off even without a retry state")
						}
						assert.Empty(t, store.TakeReady(), "a completed pass cannot immediately reclaim the same share")
						if committed {
							assert.Empty(t, share.Payload.PrimaryBlind)
						} else {
							assert.Equal(t, payload.PrimaryBlind, share.Payload.PrimaryBlind)
						}

						proc.cleanupClosedRounds()
						if phase != "closed" || unavailable {
							assert.Equal(t, 1, store.Status()[roundID].Total)
							assert.NotContains(t, logs.String(), "round closed with unsubmitted shares")
							if phase == "closed" {
								// A later successful lookup finishes cleanup without proving.
								committed, unavailable = true, false
								proc.cleanupClosedRounds()
								assert.Zero(t, store.Status()[roundID].Total)
								assert.NotContains(t, logs.String(), "round closed with unsubmitted shares")
								assert.Zero(t, prover.callCount.Load())
							}
						} else {
							assert.Zero(t, store.Status()[roundID].Total)
							if outcome == "committed" {
								assert.NotContains(t, logs.String(), "round closed with unsubmitted shares")
							} else {
								assert.Contains(t, logs.String(), `"unsubmitted":1`)
							}
						}
					})
				}
			}
		}
	}
}
