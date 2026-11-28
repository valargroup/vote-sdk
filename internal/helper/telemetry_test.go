package helper

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"cosmossdk.io/log"
	"github.com/stretchr/testify/require"

	"github.com/valargroup/vote-sdk/sentry/sentrytest"
)

// linkingSpanKeys are the span tag and data keys that would link a share's
// reveal to its leaf (plan PRV-6); see StartTrace.
var linkingSpanKeys = []string{"proposal_id", "tree_position", "submit_at", "vote_decision", "delegate_index", "d"}

// TestSpansOmitLinkingKeys runs a share through every traced helper step,
// from the HTTP submission through the pre-proof check, proving and the
// chain submission, then fails if any span's tags or data carry a key that
// would link the share's reveal to its leaf, if an HTTP transaction is named
// by its raw path rather than its route, or if an event carries the raw path,
// query or body of its request.
func TestSpansOmitLinkingKeys(t *testing.T) {
	capture := sentrytest.Capture(t)

	router, store := newTestRouter(t)
	roundID := hex.EncodeToString(make([]byte, 32))
	payload := testPayload(roundID, 3)
	payload.ProposalID, payload.VoteDecision, payload.TreePosition = 2, 1, 0
	body, err := json.Marshal(payload)
	require.NoError(t, err)
	submit := httptest.NewRecorder()
	router.ServeHTTP(submit, httptest.NewRequest(http.MethodPost, "/shielded-vote/v1/shares", strings.NewReader(string(body))))
	require.Equal(t, http.StatusOK, submit.Code, submit.Body.String())
	nullifier := strings.Repeat("ab", 32)
	router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/shielded-vote/v1/share-status/"+roundID+"/"+nullifier, nil))

	chain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tx_hash":"OK","code":0,"log":""}`))
	}))
	defer chain.Close()
	prover := &mockProver{}
	proc := NewProcessor(store, newMockTreeReader(), prover, NewChainSubmitter(chain.URL), log.NewNopLogger(), 1, nil,
		WithPreProofShareDeduper(
			func([32]byte, [32]byte, uint32, uint32) ([32]byte, error) { return [32]byte{0x44}, nil },
			func([32]byte, uint32, [32]byte) ([32]byte, error) { return [32]byte{0x55}, nil },
			func(string, []byte) (bool, error) { return false, nil },
		),
	)
	proc.processBatch(context.Background())
	require.Equal(t, int32(1), prover.callCount.Load())

	seen := map[string]bool{}
	for _, event := range capture.Events() {
		require.NotContains(t, event.Transaction, roundID, "transactions are named by route")
		require.NotContains(t, event.Transaction, nullifier, "transactions are named by route")
		if event.Request != nil {
			require.Contains(t, []string{
				"http://example.com/shielded-vote/v1/shares",
				"http://example.com/shielded-vote/v1/share-status/{roundId}/{nullifier}",
			}, event.Request.URL, "request URLs are route templates")
			require.Empty(t, event.Request.QueryString)
			require.Empty(t, event.Request.Data)
		}
		seen[event.Transaction] = true
		trace := event.Contexts["trace"]
		data, _ := trace["data"].(map[string]interface{})
		requireNoLinkingKeys(t, event.Transaction, event.Tags, data)
		for _, span := range event.Spans {
			seen[span.Description] = true
			requireNoLinkingKeys(t, span.Description, span.Tags, span.Data)
		}
	}
	for _, name := range []string{
		"POST /shielded-vote/v1/shares",
		"helper.share_enqueued",
		"GET /shielded-vote/v1/share-status/{roundId}/{nullifier}",
		"helper.process_share",
		"helper.preproof_share_nullifier_check",
		"helper.generate_share_reveal_proof",
		"POST /shielded-vote/v1/reveal-share",
	} {
		require.True(t, seen[name], "no span or transaction %q in %v", name, seen)
	}
}

// TestShareFailureEventsOmitLinkingValues fails one share on a tree position
// past the tree's end and one pool share on a chain rejection that names its
// delegate index, then fails if any event Sentry would receive carries
// either number.
func TestShareFailureEventsOmitLinkingValues(t *testing.T) {
	capture := sentrytest.Capture(t)
	// Built, not written out, so source lines Sentry quotes from this file in
	// a stack trace never contain it.
	raw := strconv.Itoa(987654320 + 1)
	value, err := strconv.ParseUint(raw, 10, 32)
	require.NoError(t, err)

	store := newTestStore(t)
	roundID := hex.EncodeToString(make([]byte, 32))
	pastTree := testPayload(roundID, 0)
	pastTree.TreePosition = value
	enqueueAndRequireInserted(t, store, pastTree)
	poolShare := testPayload(roundID, 1)
	poolShare.ProposalID, poolShare.VoteDecision = 0, uint32(value)
	enqueueAndRequireInserted(t, store, poolShare)

	chain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tx_hash":"AB","code":53,"log":"delegate ` + raw + ` is not below next_index 20: delegate not found"}`))
	}))
	defer chain.Close()
	// Two workers, so one batch takes both shares.
	proc := NewProcessor(store, newMockTreeReader(), &mockProver{}, NewChainSubmitter(chain.URL), log.NewNopLogger(), 2, nil,
		WithPreProofShareDeduper(
			func([32]byte, [32]byte, uint32, uint32) ([32]byte, error) { return [32]byte{0x44}, nil },
			func([32]byte, uint32, [32]byte) ([32]byte, error) { return [32]byte{0x55}, nil },
			func(string, []byte) (bool, error) { return false, nil },
		),
	)
	proc.processBatch(context.Background())

	var failures []string
	for _, event := range capture.Events() {
		encoded, err := json.Marshal(event)
		require.NoError(t, err)
		require.NotContains(t, string(encoded), raw)
		for _, exception := range event.Exception {
			failures = append(failures, exception.Value)
		}
	}
	require.Contains(t, strings.Join(failures, "\n"), "tree_position [redacted] out of range")
	require.Contains(t, strings.Join(failures, "\n"), "delegate [redacted] is not below next_index 20")
}

// TestShareFailureEventKeepsItsOwnTrace fails one share's Merkle path while
// the other worker proves a second share, then fails unless the failure's
// event carries the failing share's own trace. Workers sharing one Sentry
// scope gave the event the proving share's span instead, read while its
// worker wrote it, which -race also reports.
func TestShareFailureEventKeepsItsOwnTrace(t *testing.T) {
	capture := sentrytest.Capture(t)
	store := newTestStore(t)
	roundID := hex.EncodeToString(make([]byte, 32))
	failing := testPayload(roundID, 0)
	failing.TreePosition = 1
	enqueueAndRequireInserted(t, store, failing)
	enqueueAndRequireInserted(t, store, testPayload(roundID, 1))

	prover := newGatedProver()
	tree := &pathGatedTree{mockTreeReader: newMockTreeReader(), failAt: failing.TreePosition, prover: prover, reached: make(chan struct{})}
	tree.leafCount = 2
	chain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tx_hash":"OK","code":0,"log":""}`))
	}))
	defer chain.Close()
	proc := NewProcessor(store, tree, prover, NewChainSubmitter(chain.URL), log.NewNopLogger(), 2, nil,
		WithPreProofShareDeduper(
			func([32]byte, [32]byte, uint32, uint32) ([32]byte, error) { return [32]byte{0x44}, nil },
			func([32]byte, uint32, [32]byte) ([32]byte, error) { return [32]byte{0x55}, nil },
			func(string, []byte) (bool, error) { return false, nil },
		),
	)
	proc.processBatch(context.Background())

	shareTraces := map[string]interface{}{}
	var failureShare string
	var failureTrace interface{}
	for _, event := range capture.Events() {
		if event.Type == "transaction" && event.Transaction == "helper.process_share" {
			shareTraces[event.Tags["share_index"]] = event.Contexts["trace"]["trace_id"]
		} else if len(event.Exception) > 0 {
			failureShare, failureTrace = event.Tags["share_index"], event.Contexts["trace"]["trace_id"]
		}
	}
	require.Len(t, shareTraces, 2)
	require.NotEqual(t, shareTraces["0"], shareTraces["1"])
	require.Equal(t, "0", failureShare)
	require.Equal(t, shareTraces["0"], failureTrace, "the failure event carries another share's trace")
}

// pathGatedTree is a mockTreeReader that orders two shares: the Merkle path
// for any position but failAt waits until failAt's is requested, and
// failAt's waits for prover to start the other share's proof, releases it
// and fails.
type pathGatedTree struct {
	*mockTreeReader
	failAt  uint64
	prover  *gatedProver
	reached chan struct{}
}

// ForRound returns t for every round.
func (t *pathGatedTree) ForRound([]byte) TreeReader { return t }

// MerklePath gates and fails failAt's path as pathGatedTree describes, and
// otherwise returns the mock's path.
func (t *pathGatedTree) MerklePath(position uint64, anchorHeight uint32) ([]byte, error) {
	if position != t.failAt {
		<-t.reached
		return t.mockTreeReader.MerklePath(position, anchorHeight)
	}
	close(t.reached)
	<-t.prover.started
	t.prover.release <- struct{}{}
	return nil, errors.New("merkle path unavailable")
}

// TestStoreErrorEventsOmitLinkingTags checks that a share store error, which
// turns its log fields into Sentry tags, reaches Sentry without the share's
// proposal_id or tree_position, and with its other tags.
func TestStoreErrorEventsOmitLinkingTags(t *testing.T) {
	capture := sentrytest.Capture(t)
	store := newTestStore(t)
	store.captureErr = CaptureErr
	// Built, not written out, so source lines Sentry quotes from this file in
	// a stack trace never contain it.
	treePosition := uint64(987654320 + 1)
	store.logError("MarkSubmitted: db update failed",
		"round_id", "ab", "share_index", uint32(3), "proposal_id", uint32(2), "tree_position", treePosition,
		"error", errors.New("database is locked"))

	events := capture.Events()
	require.Len(t, events, 1)
	require.Equal(t, "ab", events[0].Tags["round_id"])
	require.Equal(t, "3", events[0].Tags["share_index"])
	require.NotContains(t, events[0].Tags, "proposal_id")
	require.NotContains(t, events[0].Tags, "tree_position")
	encoded, err := json.Marshal(events[0])
	require.NoError(t, err)
	require.NotContains(t, string(encoded), strconv.FormatUint(treePosition, 10))
}

// requireNoLinkingKeys fails if the span named name has a linking key among
// its tags or data.
func requireNoLinkingKeys(t *testing.T, name string, tags map[string]string, data map[string]interface{}) {
	t.Helper()
	for _, key := range linkingSpanKeys {
		require.NotContains(t, tags, key, "span %q tags", name)
		require.NotContains(t, data, key, "span %q data", name)
	}
}
