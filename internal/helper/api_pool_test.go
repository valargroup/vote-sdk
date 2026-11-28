package helper

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cosmossdk.io/log"

	"github.com/valargroup/vote-sdk/testutil/proxytest"
	"github.com/valargroup/vote-sdk/x/vote/types"
)

// These tests cover pool shares, proposal_id = 0 with vote_decision = d, at
// the helper (docs/proxy-delegation/contracts-v1.md §11.8): with the gate
// open (-tags proxyenabled), the proxy flag is checked with the round choice, the commitment (the
// DC) next, with an absent leaf a retryable 503, and only then d against
// next_index, in one registry read.

// poolTestNext is next_index in these tests: delegates 1 to 3 exist.
const poolTestNext = 4

// poolTestVCHash stands in for the vote commitment hash: it encodes the
// proposal and decision, so a leaf matches one (proposal, decision) only.
func poolTestVCHash(_, _ [32]byte, proposalID, voteDecision uint32) ([32]byte, error) {
	var vc [32]byte
	binary.LittleEndian.PutUint32(vc[0:], proposalID)
	binary.LittleEndian.PutUint32(vc[4:], voteDecision)
	vc[31] = 0x01
	return vc, nil
}

// poolTestHelper is a helper API with every validator configured, which
// records the order of its chain reads.
type poolTestHelper struct {
	router *mux.Router
	store  *ShareStore
	tree   *vcMockTree
	// reads lists the choice validator ("choice") and registry ("next_index")
	// reads in order.
	reads      []string
	proxyRound bool
	nilReader  bool
	readerErr  error
}

// newPoolTestHelper returns a helper API for a proxy round, with an empty
// tree and next_index poolTestNext.
func newPoolTestHelper(t *testing.T) *poolTestHelper {
	t.Helper()
	h := &poolTestHelper{store: newTestStore(t), tree: &vcMockTree{leaves: map[uint64][]byte{}}, proxyRound: true}
	api := &apiHandler{
		getStore:              func() *ShareStore { return h.store },
		getAPIToken:           func() string { return "" },
		getExposeQueueStatus:  func() bool { return false },
		getExposeQueueSummary: func() bool { return true },
		getIngressAllowed:     func() bool { return true },
		getTree:               func() TreeReader { return h.tree },
		getVCHash:             func() VCHashFunc { return poolTestVCHash },
		getRoundStatus: func() RoundStatusChecker {
			return func(string) (bool, error) { return true, nil }
		},
		getPayloadValidator: func() SharePayloadValidator {
			return func([32]byte, [16][32]byte, [32]byte, [32]byte, [32]byte, uint32) (bool, error) { return true, nil }
		},
		getChoiceValidator: func() ShareChoiceValidator { return h.choice },
		getNextDelegateIndex: func() NextDelegateIndexReader {
			if h.nilReader {
				return nil
			}
			return h.nextIndex
		},
		logger:  log.NewNopLogger(),
		metrics: defaultHelperMetrics,
	}
	h.router = mux.NewRouter()
	api.registerRoutes(h.router)
	return h
}

// choice is the round choice validator: proposal 0 exists only in a proxy
// round.
func (h *poolTestHelper) choice(_ string, proposalID, _ uint32) error {
	h.reads = append(h.reads, "choice")
	if proposalID == types.PoolProposalID && !h.proxyRound {
		return fmt.Errorf("%w: not a proxy round", types.ErrProxyDelegationDisabled)
	}
	return nil
}

// nextIndex is the registry read.
func (h *poolTestHelper) nextIndex() (uint32, error) {
	h.reads = append(h.reads, "next_index")
	return poolTestNext, h.readerErr
}

// placeLeaf stores the commitment of (proposalID, voteDecision) at tree
// position 0.
func (h *poolTestHelper) placeLeaf(proposalID, voteDecision uint32) {
	vc, _ := poolTestVCHash([32]byte{}, [32]byte{}, proposalID, voteDecision)
	h.tree.leaves[0] = vc[:]
}

// submit posts p and returns the response.
func (h *poolTestHelper) submit(t *testing.T, p SharePayload) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(p)
	require.NoError(t, err)
	w := httptest.NewRecorder()
	h.router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/shielded-vote/v1/shares", strings.NewReader(string(body))))
	return w
}

// poolPayload returns a layout-1 pool share for delegate d: one payload,
// share index 0 and submit_at 0, at tree position 0.
func poolPayload(d uint32) SharePayload {
	p := testPayload(apiTestRoundID, 0)
	p.ProposalID, p.VoteDecision = types.PoolProposalID, d
	return p
}

// requireErrorBody fails unless w is status with the body
// {"status":"error","error":code}.
func requireErrorBody(t *testing.T, w *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	require.Equal(t, status, w.Code, w.Body.String())
	require.JSONEq(t, fmt.Sprintf(`{"status":"error","error":%q}`, code), w.Body.String())
}

// TestPoolShareAccepted checks that a pool share whose DC is in the tree and
// whose delegate is registered is queued like a single-share vote, after one
// choice read and then one registry read.
func TestPoolShareAccepted(t *testing.T) {
	proxytest.SkipUnlessEnabled(t)
	h := newPoolTestHelper(t)
	h.placeLeaf(types.PoolProposalID, 3)
	w := h.submit(t, poolPayload(3))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.JSONEq(t, `{"status":"queued"}`, w.Body.String())
	require.Equal(t, []string{"choice", "next_index"}, h.reads)

	ready := h.store.TakeReady()
	require.Len(t, ready, 1, "submit_at 0 schedules it at arrival")
	assert.Equal(t, types.PoolProposalID, ready[0].Payload.ProposalID)
	assert.Equal(t, uint32(3), ready[0].Payload.VoteDecision)
	assert.Equal(t, uint32(0), ready[0].Payload.EncShare.ShareIndex)
}

// TestPoolShareRejections checks each pool share answer of §11.8 with its
// body, that nothing is queued, and the order of the reads: the proxy flag
// with the round choice, then the commitment, whose absent leaf is a 503
// with Retry-After even for an unregistered d, and only then the registry.
func TestPoolShareRejections(t *testing.T) {
	proxytest.SkipUnlessEnabled(t)
	for _, c := range []struct {
		name   string
		d      uint32
		setup  func(*poolTestHelper)
		status int
		code   string
		reads  []string
		// retryAfter is the Retry-After header the answer must carry.
		retryAfter string
	}{
		{name: "delegate 0", d: 0, setup: func(h *poolTestHelper) { h.placeLeaf(types.PoolProposalID, 0) },
			status: http.StatusUnprocessableEntity, code: poolErrDelegateNotFound},
		{name: "delegate at next_index", d: poolTestNext, setup: func(h *poolTestHelper) { h.placeLeaf(types.PoolProposalID, poolTestNext) },
			status: http.StatusUnprocessableEntity, code: poolErrDelegateNotFound, reads: []string{"choice", "next_index"}},
		{name: "round without the proxy flag", d: 9, setup: func(h *poolTestHelper) { h.proxyRound = false },
			status: http.StatusUnprocessableEntity, code: poolErrProxyDelegationDisabled, reads: []string{"choice"}},
		{name: "DC not yet in the tree, unregistered delegate", d: 9,
			status: http.StatusServiceUnavailable, code: poolErrLeafNotFound, reads: []string{"choice"}, retryAfter: leafNotFoundRetryAfter},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newPoolTestHelper(t)
			if c.setup != nil {
				c.setup(h)
			}
			w := h.submit(t, poolPayload(c.d))
			requireErrorBody(t, w, c.status, c.code)
			require.Equal(t, c.retryAfter, w.Header().Get("Retry-After"))
			require.Equal(t, c.reads, h.reads)
			require.Empty(t, h.store.Status())
		})
	}
}

// TestPoolShareOtherFailures checks the pool share answers that §11.8 leaves
// as they are for votes: a leaf holding another commitment is 400 before
// any registry read, and an unavailable or failing registry read is 503 or
// 500, never delegate_not_found.
func TestPoolShareOtherFailures(t *testing.T) {
	proxytest.SkipUnlessEnabled(t)
	t.Run("another commitment at tree_position", func(t *testing.T) {
		h := newPoolTestHelper(t)
		h.placeLeaf(types.PoolProposalID, 3)
		w := h.submit(t, poolPayload(9))
		require.Equal(t, http.StatusBadRequest, w.Code)
		require.Contains(t, w.Body.String(), "hash mismatch")
		require.Equal(t, []string{"choice"}, h.reads)
		require.Empty(t, h.store.Status())
	})
	t.Run("registry reader unavailable", func(t *testing.T) {
		h := newPoolTestHelper(t)
		h.placeLeaf(types.PoolProposalID, 3)
		h.nilReader = true
		requireErrorBody(t, h.submit(t, poolPayload(3)), http.StatusServiceUnavailable, "helper unavailable")
		require.Empty(t, h.store.Status())
	})
	t.Run("registry read fails", func(t *testing.T) {
		h := newPoolTestHelper(t)
		h.placeLeaf(types.PoolProposalID, 3)
		h.readerErr = errors.New("store closed")
		requireErrorBody(t, h.submit(t, poolPayload(3)), http.StatusInternalServerError, "internal error")
		require.Empty(t, h.store.Status())
	})
}

// TestValidatePayloadPoolShares checks validatePayload's proposal-0 branch
// with the gate open: d needs only to be at least 1, with no option bound,
// and d = 0 wraps ErrDelegateNotFound after every structural check.
func TestValidatePayloadPoolShares(t *testing.T) {
	proxytest.SkipUnlessEnabled(t)
	p := poolPayload(types.MaxVoteOptions)
	require.NoError(t, validatePayload(&p))
	p = poolPayload(0)
	require.ErrorIs(t, validatePayload(&p), types.ErrDelegateNotFound)

	p = poolPayload(0)
	p.PrimaryBlind = "not base64"
	err := validatePayload(&p)
	require.ErrorContains(t, err, "primary_blind")
	require.NotErrorIs(t, err, types.ErrDelegateNotFound)
}

// TestProcessorProvesPoolShare checks that the processor proves and submits a
// pool share as it does a vote: ZKP3 gets proposal 0 and d, the reveal
// carries them, and the accepted broadcast awaits its commit as any does.
func TestProcessorProvesPoolShare(t *testing.T) {
	store := newTestStore(t)
	prover := &recordingProver{}
	var submitted MsgRevealShareJSON
	chain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(body, &submitted))
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"tx_hash":"ABCD","code":0,"log":""}`))
	}))
	defer chain.Close()
	proc := NewProcessor(store, newMockTreeReader(), prover, NewChainSubmitter(chain.URL), log.NewNopLogger(), 1, nil)

	roundID := hex.EncodeToString(make([]byte, 32))
	p := poolPayload(1 << 20)
	p.VoteRoundID = roundID
	enqueueAndRequireInserted(t, store, p)
	ready := store.TakeReady()
	require.Len(t, ready, 1)
	require.ErrorContains(t, proc.processShare(context.Background(), ready[0]), "awaiting committed transaction ABCD")
	require.Equal(t, [2]uint32{types.PoolProposalID, 1 << 20}, prover.choice)
	require.Equal(t, types.PoolProposalID, submitted.ProposalID)
	require.Equal(t, uint32(1<<20), submitted.VoteDecision)
}

// recordingProver is mockProver that records the proposal and decision it
// proves.
type recordingProver struct {
	mockProver
	choice [2]uint32
}

// GenerateShareRevealProof records the proposal and decision, then proves
// as mockProver does.
func (p *recordingProver) GenerateShareRevealProof(
	merklePath []byte,
	shareComms [16][32]byte,
	primaryBlind [32]byte,
	encC1 [32]byte,
	encC2 [32]byte,
	shareIndex uint32,
	proposalID, voteDecision uint32,
	roundID [32]byte,
) (proof []byte, nullifier [32]byte, treeRoot [32]byte, err error) {
	p.choice = [2]uint32{proposalID, voteDecision}
	return p.mockProver.GenerateShareRevealProof(merklePath, shareComms, primaryBlind, encC1, encC2, shareIndex, proposalID, voteDecision, roundID)
}
