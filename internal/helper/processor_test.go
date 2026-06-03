package helper

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"cosmossdk.io/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockProver returns a fixed proof and nullifier.
type mockProver struct {
	callCount atomic.Int32
	err       error
}

func (m *mockProver) GenerateShareRevealProof(
	merklePath []byte,
	shareComms [16][32]byte,
	primaryBlind [32]byte,
	encC1 [32]byte,
	encC2 [32]byte,
	shareIndex uint32,
	proposalID, voteDecision uint32,
	roundID [32]byte,
) (proof []byte, nullifier [32]byte, treeRoot [32]byte, err error) {
	m.callCount.Add(1)
	if m.err != nil {
		return nil, nullifier, treeRoot, m.err
	}
	proof = make([]byte, 128)
	for i := range proof {
		proof[i] = 0xAA
	}
	nullifier[0] = 0xBB
	treeRoot[0] = 0xCC
	return proof, nullifier, treeRoot, nil
}

type trackingProver struct {
	sleep       time.Duration
	inFlight    atomic.Int32
	maxInFlight atomic.Int32
}

func (p *trackingProver) GenerateShareRevealProof(
	merklePath []byte,
	shareComms [16][32]byte,
	primaryBlind [32]byte,
	encC1 [32]byte,
	encC2 [32]byte,
	shareIndex uint32,
	proposalID, voteDecision uint32,
	roundID [32]byte,
) (proof []byte, nullifier [32]byte, treeRoot [32]byte, err error) {
	current := p.inFlight.Add(1)
	defer p.inFlight.Add(-1)

	for {
		seen := p.maxInFlight.Load()
		if current <= seen || p.maxInFlight.CompareAndSwap(seen, current) {
			break
		}
	}

	time.Sleep(p.sleep)
	proof = make([]byte, 64)
	nullifier[0] = 0x11
	treeRoot[0] = 0x22
	return proof, nullifier, treeRoot, nil
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func preloadFailedAttempts(t *testing.T, store *ShareStore, roundID string, attempts int) {
	t.Helper()

	key := schedKey(roundID, 0, 1, 0)
	for i := range attempts {
		ready := store.TakeReady()
		require.Len(t, ready, 1, "failed attempt %d", i)
		store.MarkFailed(roundID, 0, 1, 0)

		store.mu.Lock()
		store.schedule[key] = time.Now().Add(-time.Second)
		store.mu.Unlock()
	}

	share, ok := store.loadShare(roundID, 0, 1, 0)
	require.True(t, ok)
	require.Equal(t, ShareStateReceived, share.State)
	require.Equal(t, attempts, share.Attempts)
}

// mockTreeReader implements TreeReader for tests.
type mockTreeReader struct {
	leafCount    uint64
	anchorHeight uint64
	leaves       map[uint64][]byte
	err          error
	pathErr      error
}

func (m *mockTreeReader) ForRound(_ []byte) TreeReader { return m }

func (m *mockTreeReader) GetTreeStatus() (TreeStatus, error) {
	if m.err != nil {
		return TreeStatus{}, m.err
	}
	return TreeStatus{
		LeafCount:    m.leafCount,
		AnchorHeight: m.anchorHeight,
	}, nil
}

func (m *mockTreeReader) MerklePath(_ uint64, _ uint32) ([]byte, error) {
	if m.pathErr != nil {
		return nil, m.pathErr
	}
	if m.err != nil {
		return nil, m.err
	}
	return make([]byte, 772), nil
}

func (m *mockTreeReader) LeafAt(position uint64) ([]byte, error) {
	if m.err != nil {
		return nil, m.err
	}
	if m.leaves != nil {
		return m.leaves[position], nil
	}
	return nil, nil
}

func newMockTreeReader() *mockTreeReader {
	return &mockTreeReader{
		leafCount:    1,
		anchorHeight: 1,
	}
}

func alwaysConfirmedShare(_ string, _ []byte) (bool, error) {
	return true, nil
}

func forceConfirmingDue(t *testing.T, s *ShareStore) {
	t.Helper()
	_, err := s.db.Exec("UPDATE shares SET confirm_after = 0 WHERE state = ?", ShareStateConfirming)
	require.NoError(t, err)
}

type roundAwareTreeState struct {
	leafCounts  map[string]uint64
	pathDelay   time.Duration
	inFlight    atomic.Int32
	maxInFlight atomic.Int32
}

type roundAwareTreeReader struct {
	state   *roundAwareTreeState
	roundID string
}

func newRoundAwareTreeReader(leafCounts map[string]uint64, pathDelay time.Duration) *roundAwareTreeReader {
	return &roundAwareTreeReader{
		state: &roundAwareTreeState{
			leafCounts: leafCounts,
			pathDelay:  pathDelay,
		},
	}
}

func (r *roundAwareTreeReader) ForRound(roundID []byte) TreeReader {
	return &roundAwareTreeReader{
		state:   r.state,
		roundID: hex.EncodeToString(roundID),
	}
}

func (r *roundAwareTreeReader) GetTreeStatus() (TreeStatus, error) {
	leafCount, ok := r.state.leafCounts[r.roundID]
	if !ok {
		return TreeStatus{}, fmt.Errorf("unexpected round_id %q", r.roundID)
	}
	return TreeStatus{LeafCount: leafCount, AnchorHeight: 1}, nil
}

func (r *roundAwareTreeReader) MerklePath(position uint64, _ uint32) ([]byte, error) {
	current := r.state.inFlight.Add(1)
	defer r.state.inFlight.Add(-1)
	for {
		seen := r.state.maxInFlight.Load()
		if current <= seen || r.state.maxInFlight.CompareAndSwap(seen, current) {
			break
		}
	}
	time.Sleep(r.state.pathDelay)

	leafCount, ok := r.state.leafCounts[r.roundID]
	if !ok {
		return nil, fmt.Errorf("unexpected round_id %q", r.roundID)
	}
	if position >= leafCount {
		return nil, fmt.Errorf("tree_position %d out of range for round %s", position, r.roundID)
	}
	return make([]byte, 772), nil
}

func (r *roundAwareTreeReader) LeafAt(position uint64) ([]byte, error) {
	leafCount, ok := r.state.leafCounts[r.roundID]
	if !ok {
		return nil, fmt.Errorf("unexpected round_id %q", r.roundID)
	}
	if position >= leafCount {
		return nil, nil
	}
	return make([]byte, 32), nil
}

func TestProcessor_ProcessBatch_Success(t *testing.T) {
	store := newTestStore(t)
	prover := &mockProver{}
	tree := newMockTreeReader()

	// Fake chain server that accepts submissions.
	chainServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"tx_hash":"AABB","code":0,"log":""}`))
	}))
	defer chainServer.Close()

	submitter := NewChainSubmitter(chainServer.URL)
	proc := NewProcessor(store, tree, prover, submitter, log.NewNopLogger(), 2, nil, WithShareConfirmationChecker(alwaysConfirmedShare))

	// Enqueue a share (zero delay in test store means immediately ready).
	roundID := hex.EncodeToString(make([]byte, 32))
	p := testPayload(roundID, 0)
	p.TreePosition = 0
	enqueueAndRequireInserted(t, store, p)

	// Process the batch — processBatch calls TakeReady internally.
	proc.processBatch(context.Background())

	// Verify the prover was called.
	assert.Equal(t, int32(1), prover.callCount.Load())

	// Verify share is waiting on committed chain-state confirmation.
	status := store.Status()
	assert.Equal(t, 0, status[roundID].Submitted)
	assert.Equal(t, 1, status[roundID].Pending)

	forceConfirmingDue(t, store)
	proc.confirmBroadcasts(context.Background())

	status = store.Status()
	assert.Equal(t, 1, status[roundID].Submitted)
	assert.Equal(t, 0, status[roundID].Pending)
}

func TestProcessor_ProcessBatch_DoesNotWaitForConfirmation(t *testing.T) {
	store := newTestStore(t)
	prover := &mockProver{}
	tree := newMockTreeReader()

	chainServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"tx_hash":"AABB","code":0,"log":""}`))
	}))
	defer chainServer.Close()

	submitter := NewChainSubmitter(chainServer.URL)
	proc := NewProcessor(
		store,
		tree,
		prover,
		submitter,
		log.NewNopLogger(),
		2,
		nil,
		WithShareConfirmationChecker(func(_ string, _ []byte) (bool, error) {
			t.Fatal("processBatch should not poll committed state inline")
			return false, nil
		}),
	)

	roundID := hex.EncodeToString(make([]byte, 32))
	p := testPayload(roundID, 0)
	p.TreePosition = 0
	enqueueAndRequireInserted(t, store, p)

	proc.processBatch(context.Background())

	assert.Equal(t, int32(1), prover.callCount.Load())
	status := store.Status()
	assert.Equal(t, 0, status[roundID].Submitted)
	assert.Equal(t, 1, status[roundID].Pending)
}

func TestProcessor_ProcessBatch_MissingConfirmationCheckerDoesNotStickConfirming(t *testing.T) {
	store := newTestStore(t)
	prover := &mockProver{}
	tree := newMockTreeReader()

	chainServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"tx_hash":"AABB","code":0,"log":""}`))
	}))
	defer chainServer.Close()

	submitter := NewChainSubmitter(chainServer.URL)
	proc := NewProcessor(store, tree, prover, submitter, log.NewNopLogger(), 2, nil)

	roundID := hex.EncodeToString(make([]byte, 32))
	p := testPayload(roundID, 0)
	p.TreePosition = 0
	enqueueAndRequireInserted(t, store, p)

	proc.processBatch(context.Background())

	assert.Equal(t, int32(1), prover.callCount.Load())
	status := store.Status()
	assert.Equal(t, 0, status[roundID].Submitted)
	assert.Equal(t, 1, status[roundID].Pending)

	var state, attempts int
	var shareNullifier string
	err := store.db.QueryRow(
		`SELECT state, attempts, share_nullifier
		   FROM shares
		  WHERE round_id = ? AND share_index = ? AND proposal_id = ? AND tree_position = ?`,
		roundID, 0, 1, 0,
	).Scan(&state, &attempts, &shareNullifier)
	require.NoError(t, err)
	assert.Equal(t, int(ShareStateReceived), state)
	assert.Equal(t, 1, attempts)
	assert.Empty(t, shareNullifier)
	assert.Empty(t, store.TakeConfirmingReady(time.Now(), 10))
}

func TestProcessor_ConfirmBroadcasts_UnconfirmedTimeoutRetries(t *testing.T) {
	store := newTestStore(t)
	prover := &mockProver{}
	tree := newMockTreeReader()

	chainServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"tx_hash":"AABB","code":0,"log":""}`))
	}))
	defer chainServer.Close()

	submitter := NewChainSubmitter(chainServer.URL)
	proc := NewProcessor(
		store,
		tree,
		prover,
		submitter,
		log.NewNopLogger(),
		2,
		nil,
		WithShareConfirmationChecker(func(_ string, _ []byte) (bool, error) {
			return false, nil
		}),
	)

	roundID := hex.EncodeToString(make([]byte, 32))
	p := testPayload(roundID, 0)
	p.TreePosition = 0
	enqueueAndRequireInserted(t, store, p)

	proc.processBatch(context.Background())

	_, err := store.db.Exec(
		"UPDATE shares SET confirm_after = 0, broadcast_at = ? WHERE state = ?",
		time.Now().Add(-confirmationTimeout-time.Second).Unix(),
		ShareStateConfirming,
	)
	require.NoError(t, err)

	proc.confirmBroadcasts(context.Background())

	status := store.Status()
	assert.Equal(t, 0, status[roundID].Submitted)
	assert.Equal(t, 1, status[roundID].Pending)

	var state, attempts int
	var c1, c2, comms, blind, shareNullifier string
	err = store.db.QueryRow(
		`SELECT state, attempts, enc_share_c1, enc_share_c2, share_comms, primary_blind, share_nullifier
		   FROM shares
		  WHERE round_id = ? AND share_index = ? AND proposal_id = ? AND tree_position = ?`,
		roundID, 0, 1, 0,
	).Scan(&state, &attempts, &c1, &c2, &comms, &blind, &shareNullifier)
	require.NoError(t, err)
	assert.Equal(t, int(ShareStateReceived), state)
	assert.Equal(t, 1, attempts)
	assert.NotEmpty(t, c1)
	assert.NotEmpty(t, c2)
	assert.NotEmpty(t, comms)
	assert.NotEmpty(t, blind)
	assert.Empty(t, shareNullifier)
}

func TestProcessor_ConfirmBroadcasts_CheckerErrorTimeoutRetries(t *testing.T) {
	store := newTestStore(t)
	prover := &mockProver{}
	tree := newMockTreeReader()

	chainServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"tx_hash":"AABB","code":0,"log":""}`))
	}))
	defer chainServer.Close()

	submitter := NewChainSubmitter(chainServer.URL)
	proc := NewProcessor(
		store,
		tree,
		prover,
		submitter,
		log.NewNopLogger(),
		2,
		nil,
		WithShareConfirmationChecker(func(_ string, _ []byte) (bool, error) {
			return false, assert.AnError
		}),
	)

	roundID := hex.EncodeToString(make([]byte, 32))
	p := testPayload(roundID, 0)
	p.TreePosition = 0
	enqueueAndRequireInserted(t, store, p)

	proc.processBatch(context.Background())

	_, err := store.db.Exec(
		"UPDATE shares SET confirm_after = 0, broadcast_at = ? WHERE state = ?",
		time.Now().Add(-confirmationTimeout-time.Second).Unix(),
		ShareStateConfirming,
	)
	require.NoError(t, err)

	proc.confirmBroadcasts(context.Background())

	var state, attempts int
	var shareNullifier string
	err = store.db.QueryRow(
		`SELECT state, attempts, share_nullifier
		   FROM shares
		  WHERE round_id = ? AND share_index = ? AND proposal_id = ? AND tree_position = ?`,
		roundID, 0, 1, 0,
	).Scan(&state, &attempts, &shareNullifier)
	require.NoError(t, err)
	assert.Equal(t, int(ShareStateReceived), state)
	assert.Equal(t, 1, attempts)
	assert.Empty(t, shareNullifier)
}

func TestProcessor_ProcessBatch_ProofFailureSpendsFailedAttempt(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{
			name: "generic failure",
			err:  assert.AnError,
		},
		{
			name: "invalid inputs",
			err:  fmt.Errorf("share reveal: invalid inputs"),
		},
		{
			name: "deserialization error",
			err:  fmt.Errorf("share reveal: deserialization error (non-canonical Fp or invalid curve point)"),
		},
		{
			name: "proof generation failed",
			err:  fmt.Errorf("share reveal: proof generation failed"),
		},
		{
			name: "unknown error code",
			err:  fmt.Errorf("share reveal: unknown error code -6"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newTestStore(t)
			prover := &mockProver{err: tt.err}
			tree := newMockTreeReader()

			chainServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				t.Fatal("should not submit when proof fails")
			}))
			defer chainServer.Close()

			submitter := NewChainSubmitter(chainServer.URL)
			proc := NewProcessor(store, tree, prover, submitter, log.NewNopLogger(), 2, nil)

			roundID := hex.EncodeToString(make([]byte, 32))
			p := testPayload(roundID, 0)
			p.TreePosition = 0
			enqueueAndRequireInserted(t, store, p)

			proc.processBatch(context.Background())

			status := store.Status()
			assert.Equal(t, 1, status[roundID].Pending)
			share, ok := store.loadShare(roundID, 0, 1, 0)
			require.True(t, ok)
			assert.Equal(t, 1, share.Attempts)
		})
	}
}

func TestProcessor_ProcessBatch_SubmitCancellationReturnsShareToPending(t *testing.T) {
	store := newTestStore(t)
	prover := &mockProver{}
	tree := newMockTreeReader()

	submitter := NewChainSubmitter("http://example.test")
	submitter.httpClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return nil, context.Canceled
		}),
	}
	proc := NewProcessor(store, tree, prover, submitter, log.NewNopLogger(), 2, nil)

	roundID := hex.EncodeToString(make([]byte, 32))
	p := testPayload(roundID, 0)
	p.TreePosition = 0
	enqueueAndRequireInserted(t, store, p)

	proc.processBatch(context.Background())

	status := store.Status()
	assert.Equal(t, 1, status[roundID].Pending)
	share, ok := store.loadShare(roundID, 0, 1, 0)
	require.True(t, ok)
	assert.Equal(t, ShareStateReceived, share.State)
	assert.Equal(t, 0, share.Attempts)
}

func TestProcessor_ProcessBatch_ChainRejects(t *testing.T) {
	store := newTestStore(t)
	prover := &mockProver{}
	tree := newMockTreeReader()

	// Chain returns non-zero code with a non-nullifier error.
	chainServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"tx_hash":"","code":5,"log":"vote round is not active"}`))
	}))
	defer chainServer.Close()

	submitter := NewChainSubmitter(chainServer.URL)
	proc := NewProcessor(store, tree, prover, submitter, log.NewNopLogger(), 2, nil)

	roundID := hex.EncodeToString(make([]byte, 32))
	p := testPayload(roundID, 0)
	p.TreePosition = 0
	enqueueAndRequireInserted(t, store, p)

	proc.processBatch(context.Background())

	status := store.Status()
	assert.Equal(t, 1, status[roundID].Pending) // back to pending for retry
	share, ok := store.loadShare(roundID, 0, 1, 0)
	require.True(t, ok)
	assert.Equal(t, 1, share.Attempts)
}

func TestProcessor_ProcessBatch_SystemSubmitErrorDoesNotSpendFailedAttempt(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
	}{
		{
			name:   "internal server error",
			status: http.StatusInternalServerError,
			body:   `{"error":"encode failed"}`,
		},
		{
			name:   "bad gateway",
			status: http.StatusBadGateway,
			body:   `{"error":"broadcast failed"}`,
		},
		{
			name:   "service unavailable",
			status: http.StatusServiceUnavailable,
			body:   `{"status":"warming"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newTestStore(t)
			prover := &mockProver{}
			tree := newMockTreeReader()

			chainServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.status)
				w.Write([]byte(tt.body))
			}))
			defer chainServer.Close()

			submitter := NewChainSubmitter(chainServer.URL)
			proc := NewProcessor(store, tree, prover, submitter, log.NewNopLogger(), 2, nil)

			roundID := hex.EncodeToString(make([]byte, 32))
			p := testPayload(roundID, 0)
			p.TreePosition = 0
			enqueueAndRequireInserted(t, store, p)

			proc.processBatch(context.Background())

			status := store.Status()
			assert.Equal(t, 1, status[roundID].Pending)
			assert.Equal(t, 0, status[roundID].Failed)
			share, ok := store.loadShare(roundID, 0, 1, 0)
			require.True(t, ok)
			assert.Equal(t, ShareStateReceived, share.State)
			assert.Equal(t, 0, share.Attempts)
		})
	}
}

func TestProcessor_ProcessBatch_RepeatedSystemSubmitErrorDoesNotSpendFailedAttempts(t *testing.T) {
	store := newTestStore(t)
	prover := &mockProver{}
	tree := newMockTreeReader()

	chainServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte(`{"status":"warming"}`))
	}))
	defer chainServer.Close()

	submitter := NewChainSubmitter(chainServer.URL)
	proc := NewProcessor(store, tree, prover, submitter, log.NewNopLogger(), 2, nil)

	roundID := hex.EncodeToString(make([]byte, 32))
	p := testPayload(roundID, 0)
	p.TreePosition = 0
	enqueueAndRequireInserted(t, store, p)

	for i := range 6 {
		proc.processBatch(context.Background())
		share, ok := store.loadShare(roundID, 0, 1, 0)
		require.True(t, ok, "retry %d", i)
		assert.Equal(t, ShareStateReceived, share.State)
		assert.Equal(t, 0, share.Attempts)

		store.mu.Lock()
		store.schedule[schedKey(roundID, 0, 1, 0)] = time.Now().Add(-time.Second)
		store.mu.Unlock()
	}

	status := store.Status()
	assert.Equal(t, 1, status[roundID].Pending)
	assert.Equal(t, 0, status[roundID].Failed)
}

func TestProcessor_ProcessBatch_SystemSubmitErrorPreservesFailedAttempts(t *testing.T) {
	store := newTestStore(t)
	prover := &mockProver{}
	tree := newMockTreeReader()
	responseStatus := atomic.Int32{}
	responseStatus.Store(http.StatusServiceUnavailable)

	chainServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		status := int(responseStatus.Load())
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if status == http.StatusBadRequest {
			w.Write([]byte(`{"error":"validation failed"}`))
			return
		}
		w.Write([]byte(`{"status":"warming"}`))
	}))
	defer chainServer.Close()

	submitter := NewChainSubmitter(chainServer.URL)
	proc := NewProcessor(store, tree, prover, submitter, log.NewNopLogger(), 2, nil)

	roundID := hex.EncodeToString(make([]byte, 32))
	p := testPayload(roundID, 0)
	p.TreePosition = 0
	enqueueAndRequireInserted(t, store, p)
	key := schedKey(roundID, 0, 1, 0)

	preloadFailedAttempts(t, store, roundID, 4)

	for i := range 2 {
		proc.processBatch(context.Background())

		share, ok := store.loadShare(roundID, 0, 1, 0)
		require.True(t, ok, "system retry %d", i)
		assert.Equal(t, ShareStateReceived, share.State)
		assert.Equal(t, 4, share.Attempts)

		store.mu.Lock()
		next, ok := store.schedule[key]
		store.mu.Unlock()
		require.True(t, ok)
		assert.True(t, time.Until(next) > 0)

		store.mu.Lock()
		store.schedule[key] = time.Now().Add(-time.Second)
		store.mu.Unlock()
	}

	responseStatus.Store(http.StatusBadRequest)
	proc.processBatch(context.Background())

	status := store.Status()
	assert.Equal(t, 0, status[roundID].Pending)
	assert.Equal(t, 1, status[roundID].Failed)
	share, ok := store.loadShare(roundID, 0, 1, 0)
	require.True(t, ok)
	assert.Equal(t, ShareStateFailed, share.State)
	assert.Equal(t, 5, share.Attempts)
}

func TestProcessor_ProcessBatch_SystemErrorsPreserveExistingFailedAttempts(t *testing.T) {
	tests := []struct {
		name      string
		tree      TreeReader
		submitter *ChainSubmitter
		active    RoundStatusChecker
	}{
		{
			name: "transport error",
			tree: newMockTreeReader(),
			submitter: func() *ChainSubmitter {
				submitter := NewChainSubmitter("http://example.test")
				submitter.httpClient = &http.Client{
					Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
						return nil, assert.AnError
					}),
				}
				return submitter
			}(),
		},
		{
			name:      "round status check error",
			tree:      newMockTreeReader(),
			submitter: NewChainSubmitter("http://example.test"),
			active: func(roundID string) (bool, error) {
				return false, assert.AnError
			},
		},
		{
			name:      "tree readiness error",
			tree:      &mockTreeReader{err: assert.AnError},
			submitter: NewChainSubmitter("http://example.test"),
		},
		{
			name: "merkle path error",
			tree: &mockTreeReader{
				leafCount:    1,
				anchorHeight: 1,
				pathErr:      assert.AnError,
			},
			submitter: NewChainSubmitter("http://example.test"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newTestStore(t)
			prover := &mockProver{}
			proc := NewProcessor(store, tt.tree, prover, tt.submitter, log.NewNopLogger(), 2, tt.active)

			roundID := hex.EncodeToString(make([]byte, 32))
			p := testPayload(roundID, 0)
			p.TreePosition = 0
			enqueueAndRequireInserted(t, store, p)
			key := schedKey(roundID, 0, 1, 0)
			preloadFailedAttempts(t, store, roundID, 4)

			proc.processBatch(context.Background())

			status := store.Status()
			assert.Equal(t, 1, status[roundID].Pending)
			assert.Equal(t, 0, status[roundID].Failed)
			share, ok := store.loadShare(roundID, 0, 1, 0)
			require.True(t, ok)
			assert.Equal(t, ShareStateReceived, share.State)
			assert.Equal(t, 4, share.Attempts)

			store.mu.Lock()
			_, ok = store.schedule[key]
			store.mu.Unlock()
			assert.True(t, ok)
		})
	}
}

func TestProcessor_ProcessBatch_SystemSubmitErrorNearVoteEndRetriesUrgently(t *testing.T) {
	now := time.Now()
	voteEndTime := uint64(now.Add(5 * time.Second).Unix())
	fetcher := func(roundID string) (RoundInfo, error) {
		return RoundInfo{CreatedAtTime: uint64(now.Add(-time.Hour).Unix()), VoteEndTime: voteEndTime}, nil
	}
	store, err := NewShareStore(":memory:", fetcher)
	require.NoError(t, err)
	defer store.Close()

	prover := &mockProver{}
	tree := newMockTreeReader()

	chainServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte(`{"status":"warming"}`))
	}))
	defer chainServer.Close()

	submitter := NewChainSubmitter(chainServer.URL)
	proc := NewProcessor(store, tree, prover, submitter, log.NewNopLogger(), 2, nil)

	roundID := hex.EncodeToString(make([]byte, 32))
	p := testPayload(roundID, 0)
	p.TreePosition = 0
	enqueueAndRequireInserted(t, store, p)

	proc.processBatch(context.Background())

	status := store.Status()
	assert.Equal(t, 1, status[roundID].Pending)
	share, ok := store.loadShare(roundID, 0, 1, 0)
	require.True(t, ok)
	assert.Equal(t, 0, share.Attempts)

	store.mu.Lock()
	next, ok := store.schedule[schedKey(roundID, 0, 1, 0)]
	store.mu.Unlock()
	require.True(t, ok)
	assert.WithinDuration(t, time.Now().Add(shareSystemRetryUrgentBackoff), next, 300*time.Millisecond)
}

func TestProcessor_ProcessBatch_BadRequestSpendsFailedAttempt(t *testing.T) {
	store := newTestStore(t)
	prover := &mockProver{}
	tree := newMockTreeReader()

	chainServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"validation failed"}`))
	}))
	defer chainServer.Close()

	submitter := NewChainSubmitter(chainServer.URL)
	proc := NewProcessor(store, tree, prover, submitter, log.NewNopLogger(), 2, nil)

	roundID := hex.EncodeToString(make([]byte, 32))
	p := testPayload(roundID, 0)
	p.TreePosition = 0
	enqueueAndRequireInserted(t, store, p)

	proc.processBatch(context.Background())

	status := store.Status()
	assert.Equal(t, 1, status[roundID].Pending)
	share, ok := store.loadShare(roundID, 0, 1, 0)
	require.True(t, ok)
	assert.Equal(t, 1, share.Attempts)
}

func TestProcessor_ProcessBatch_DuplicateNullifierRequiresConfirmation(t *testing.T) {
	store := newTestStore(t)
	prover := &mockProver{}
	tree := newMockTreeReader()

	// Chain rejects with duplicate nullifier — another helper already
	// revealed this share (quorum mode).
	chainServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		w.Write([]byte(`{"tx_hash":"","code":2,"log":"nullifier already spent"}`))
	}))
	defer chainServer.Close()

	submitter := NewChainSubmitter(chainServer.URL)
	proc := NewProcessor(store, tree, prover, submitter, log.NewNopLogger(), 2, nil, WithShareConfirmationChecker(alwaysConfirmedShare))

	roundID := hex.EncodeToString(make([]byte, 32))
	p := testPayload(roundID, 0)
	p.TreePosition = 0
	enqueueAndRequireInserted(t, store, p)

	proc.processBatch(context.Background())

	// A duplicate nullifier is benign, but the helper still waits for the
	// committed nullifier set before counting it as submitted.
	status := store.Status()
	assert.Equal(t, 0, status[roundID].Submitted)
	assert.Equal(t, 1, status[roundID].Pending)

	forceConfirmingDue(t, store)
	proc.confirmBroadcasts(context.Background())

	status = store.Status()
	assert.Equal(t, 1, status[roundID].Submitted)
	assert.Equal(t, 0, status[roundID].Pending)
}

func TestProcessor_PreProofDuplicateNullifierSkipsProof(t *testing.T) {
	store := newTestStore(t)
	prover := &mockProver{}
	tree := &mockTreeReader{err: assert.AnError}

	chainServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("should not submit when pre-proof nullifier check finds existing reveal")
	}))
	defer chainServer.Close()

	submitter := NewChainSubmitter(chainServer.URL)
	expectedRoundID := [32]byte{0x11}
	expectedSharesHash := [32]byte{0x22}
	expectedPrimaryBlind := [32]byte{0x33}
	expectedCommitment := [32]byte{0x44}
	expectedNullifier := [32]byte{0x55}
	var checkerCalls atomic.Int32
	proc := NewProcessor(
		store,
		tree,
		prover,
		submitter,
		log.NewNopLogger(),
		2,
		nil,
		WithPreProofShareDeduper(
			func(roundID, sharesHash [32]byte, proposalID, voteDecision uint32) ([32]byte, error) {
				require.Equal(t, expectedRoundID, roundID)
				require.Equal(t, expectedSharesHash, sharesHash)
				require.Equal(t, uint32(1), proposalID)
				require.Equal(t, uint32(0), voteDecision)
				return expectedCommitment, nil
			},
			func(voteCommitment [32]byte, shareIndex uint32, primaryBlind [32]byte) ([32]byte, error) {
				require.Equal(t, expectedCommitment, voteCommitment)
				require.Equal(t, uint32(0), shareIndex)
				require.Equal(t, expectedPrimaryBlind, primaryBlind)
				return expectedNullifier, nil
			},
			func(roundIDHex string, shareNullifier []byte) (bool, error) {
				checkerCalls.Add(1)
				require.Equal(t, hex.EncodeToString(expectedRoundID[:]), roundIDHex)
				require.Equal(t, expectedNullifier[:], shareNullifier)
				return true, nil
			},
		),
	)

	roundID := hex.EncodeToString(expectedRoundID[:])
	p := testPayload(roundID, 0)
	p.SharesHash = base64.StdEncoding.EncodeToString(expectedSharesHash[:])
	p.PrimaryBlind = base64.StdEncoding.EncodeToString(expectedPrimaryBlind[:])
	p.TreePosition = 100
	enqueueAndRequireInserted(t, store, p)

	proc.processBatch(context.Background())

	assert.Equal(t, int32(1), checkerCalls.Load())
	assert.Equal(t, int32(0), prover.callCount.Load())
	status := store.Status()
	assert.Equal(t, 1, status[roundID].Submitted)
	assert.Equal(t, 0, status[roundID].Pending)
}

func TestProcessor_PreProofNullifierNotRevealedFallsThroughToProof(t *testing.T) {
	store := newTestStore(t)
	prover := &mockProver{}
	tree := newMockTreeReader()

	chainServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"tx_hash":"OK","code":0,"log":""}`))
	}))
	defer chainServer.Close()

	submitter := NewChainSubmitter(chainServer.URL)
	expectedCommitment := [32]byte{0x44}
	expectedNullifier := [32]byte{0x55}
	var checkerCalls atomic.Int32
	proc := NewProcessor(
		store,
		tree,
		prover,
		submitter,
		log.NewNopLogger(),
		2,
		nil,
		WithShareConfirmationChecker(alwaysConfirmedShare),
		WithPreProofShareDeduper(
			func(roundID, sharesHash [32]byte, proposalID, voteDecision uint32) ([32]byte, error) {
				return expectedCommitment, nil
			},
			func(voteCommitment [32]byte, shareIndex uint32, primaryBlind [32]byte) ([32]byte, error) {
				require.Equal(t, expectedCommitment, voteCommitment)
				return expectedNullifier, nil
			},
			func(roundIDHex string, shareNullifier []byte) (bool, error) {
				checkerCalls.Add(1)
				require.Equal(t, expectedNullifier[:], shareNullifier)
				return false, nil
			},
		),
	)

	roundID := hex.EncodeToString(make([]byte, 32))
	p := testPayload(roundID, 0)
	enqueueAndRequireInserted(t, store, p)

	proc.processBatch(context.Background())

	assert.Equal(t, int32(1), checkerCalls.Load())
	assert.Equal(t, int32(1), prover.callCount.Load())
	status := store.Status()
	assert.Equal(t, 0, status[roundID].Submitted)
	assert.Equal(t, 1, status[roundID].Pending)

	forceConfirmingDue(t, store)
	proc.confirmBroadcasts(context.Background())

	status = store.Status()
	assert.Equal(t, 1, status[roundID].Submitted)
	assert.Equal(t, 0, status[roundID].Pending)
}

func TestProcessor_PreProofNullifierCheckErrorFallsThroughToProof(t *testing.T) {
	store := newTestStore(t)
	prover := &mockProver{}
	tree := newMockTreeReader()

	chainServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"tx_hash":"OK","code":0,"log":""}`))
	}))
	defer chainServer.Close()

	submitter := NewChainSubmitter(chainServer.URL)
	expectedCommitment := [32]byte{0x44}
	var shareHashCalls atomic.Int32
	proc := NewProcessor(
		store,
		tree,
		prover,
		submitter,
		log.NewNopLogger(),
		2,
		nil,
		WithShareConfirmationChecker(alwaysConfirmedShare),
		WithPreProofShareDeduper(
			func(roundID, sharesHash [32]byte, proposalID, voteDecision uint32) ([32]byte, error) {
				return expectedCommitment, nil
			},
			func(voteCommitment [32]byte, shareIndex uint32, primaryBlind [32]byte) ([32]byte, error) {
				shareHashCalls.Add(1)
				require.Equal(t, expectedCommitment, voteCommitment)
				return [32]byte{}, assert.AnError
			},
			func(roundIDHex string, shareNullifier []byte) (bool, error) {
				t.Fatal("checker should not run when nullifier hashing fails")
				return false, nil
			},
		),
	)

	roundID := hex.EncodeToString(make([]byte, 32))
	p := testPayload(roundID, 0)
	enqueueAndRequireInserted(t, store, p)

	proc.processBatch(context.Background())

	assert.Equal(t, int32(1), shareHashCalls.Load())
	assert.Equal(t, int32(1), prover.callCount.Load())
	status := store.Status()
	assert.Equal(t, 0, status[roundID].Submitted)
	assert.Equal(t, 1, status[roundID].Pending)

	forceConfirmingDue(t, store)
	proc.confirmBroadcasts(context.Background())

	status = store.Status()
	assert.Equal(t, 1, status[roundID].Submitted)
	assert.Equal(t, 0, status[roundID].Pending)
}

func TestProcessor_Run_CancelContext(t *testing.T) {
	store := newTestStore(t)
	prover := &mockProver{}
	tree := newMockTreeReader()
	submitter := NewChainSubmitter("http://localhost:0")
	proc := NewProcessor(store, tree, prover, submitter, log.NewNopLogger(), 2, nil)

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- proc.Run(ctx)
	}()

	// Let it enter the deterministic wait.
	time.Sleep(20 * time.Millisecond)
	cancel()

	err := <-done
	assert.ErrorIs(t, err, context.Canceled)
}

func TestProcessor_Run_ImmediateEnqueueWakesProcessor(t *testing.T) {
	now := uint64(time.Now().Unix())
	store, err := NewShareStore(filepath.Join(t.TempDir(), "helper.db"), func(roundID string) (RoundInfo, error) {
		return RoundInfo{CreatedAtTime: now, VoteEndTime: now + testVoteEndOffset}, nil
	})
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	prover := &mockProver{}
	tree := newMockTreeReader()

	chainServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"tx_hash":"OK","code":0,"log":""}`))
	}))
	defer chainServer.Close()

	submitter := NewChainSubmitter(chainServer.URL)
	proc := NewProcessor(store, tree, prover, submitter, log.NewNopLogger(), 2, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- proc.Run(ctx)
	}()

	roundID := hex.EncodeToString(make([]byte, 32))
	p := testPayload(roundID, 0)
	p.TreePosition = 0
	enqueueAndRequireInserted(t, store, p)

	require.Eventually(t, func() bool {
		status := store.Status()
		return prover.callCount.Load() == 1 && status[roundID].Pending == 1 && status[roundID].Submitted == 0
	}, time.Second, 10*time.Millisecond)

	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
}

func TestProcessor_TreePositionOutOfRange(t *testing.T) {
	store := newTestStore(t)
	prover := &mockProver{}
	tree := newMockTreeReader() // only 1 leaf at index 0

	chainServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("should not submit when tree position is out of range")
	}))
	defer chainServer.Close()

	submitter := NewChainSubmitter(chainServer.URL)
	proc := NewProcessor(store, tree, prover, submitter, log.NewNopLogger(), 2, nil)

	roundID := hex.EncodeToString(make([]byte, 32))
	p := testPayload(roundID, 0)
	p.TreePosition = 999 // out of range

	// Directly call processShare.
	share := QueuedShare{Payload: p}
	_, err := proc.processShare(context.Background(), share)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "out of range")
}

func TestProcessor_MaxConcurrentFallback(t *testing.T) {
	store := newTestStore(t)
	prover := &mockProver{}
	tree := newMockTreeReader()

	chainServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"tx_hash":"OK","code":0,"log":""}`))
	}))
	defer chainServer.Close()

	submitter := NewChainSubmitter(chainServer.URL)
	proc := NewProcessor(store, tree, prover, submitter, log.NewNopLogger(), 0, nil, WithShareConfirmationChecker(alwaysConfirmedShare))
	assert.Equal(t, 1, proc.maxConcurrent)

	roundID := hex.EncodeToString(make([]byte, 32))
	p := testPayload(roundID, 0)
	p.TreePosition = 0
	enqueueAndRequireInserted(t, store, p)

	proc.processBatch(context.Background())
	forceConfirmingDue(t, store)
	proc.confirmBroadcasts(context.Background())

	status := store.Status()
	assert.Equal(t, 1, status[roundID].Submitted)
}

// Verify that maxConcurrent=1 is honored.
func TestProcessor_ProcessBatch_Sequential(t *testing.T) {
	store := newTestStore(t)
	prover := &trackingProver{sleep: 60 * time.Millisecond}
	tree := newMockTreeReader()

	chainServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"tx_hash":"OK","code":0,"log":""}`))
	}))
	defer chainServer.Close()

	submitter := NewChainSubmitter(chainServer.URL)
	proc := NewProcessor(store, tree, prover, submitter, log.NewNopLogger(), 1, nil, WithShareConfirmationChecker(alwaysConfirmedShare))

	roundID := hex.EncodeToString(make([]byte, 32))
	for i := 0; i < 4; i++ {
		p := testPayload(roundID, uint32(i))
		p.TreePosition = 0
		enqueueAndRequireInserted(t, store, p)
	}

	proc.processBatch(context.Background())
	forceConfirmingDue(t, store)
	proc.confirmBroadcasts(context.Background())

	maxSeen := prover.maxInFlight.Load()
	assert.Equal(t, int32(1), maxSeen)

	status := store.Status()
	assert.Equal(t, 4, status[roundID].Submitted)
}

func TestProcessor_ProcessBatch_ConcurrentRoundsUseScopedTreeReaders(t *testing.T) {
	store := newTestStore(t)
	prover := &mockProver{}

	roundABytes := make([]byte, 32)
	roundABytes[31] = 1
	roundA := hex.EncodeToString(roundABytes)
	roundBBytes := make([]byte, 32)
	roundBBytes[31] = 2
	roundB := hex.EncodeToString(roundBBytes)
	tree := newRoundAwareTreeReader(map[string]uint64{
		roundA: 1,
		roundB: 2,
	}, 50*time.Millisecond)

	chainServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"tx_hash":"OK","code":0,"log":""}`))
	}))
	defer chainServer.Close()

	submitter := NewChainSubmitter(chainServer.URL)
	proc := NewProcessor(store, tree, prover, submitter, log.NewNopLogger(), 2, nil, WithShareConfirmationChecker(alwaysConfirmedShare))

	pA := testPayload(roundA, 0)
	pA.TreePosition = 0
	enqueueAndRequireInserted(t, store, pA)

	pB := testPayload(roundB, 0)
	pB.TreePosition = 1
	enqueueAndRequireInserted(t, store, pB)

	proc.processBatch(context.Background())
	forceConfirmingDue(t, store)
	proc.confirmBroadcasts(context.Background())

	assert.Equal(t, int32(2), prover.callCount.Load())
	assert.Equal(t, int32(2), tree.state.maxInFlight.Load())

	status := store.Status()
	assert.Equal(t, 1, status[roundA].Submitted)
	assert.Equal(t, 1, status[roundB].Submitted)
}

func TestValidatePayload(t *testing.T) {
	// Build a valid 64-character hex round ID (32 bytes).
	roundID := hex.EncodeToString(make([]byte, 32))
	b64_32 := base64.StdEncoding.EncodeToString(make([]byte, 32))

	comms := make([]string, 16)
	for i := range comms {
		comms[i] = b64_32
	}

	valid := SharePayload{
		SharesHash:   b64_32,
		ProposalID:   1,
		VoteDecision: 0,
		EncShare:     EncryptedShareWire{C1: b64_32, C2: b64_32, ShareIndex: 0},
		TreePosition: 0,
		VoteRoundID:  roundID,
		ShareComms:   comms,
		PrimaryBlind: b64_32,
	}

	t.Run("valid", func(t *testing.T) {
		p := valid
		assert.NoError(t, validatePayload(&p))
	})

	t.Run("short round_id", func(t *testing.T) {
		p := valid
		p.VoteRoundID = "aabb"
		err := validatePayload(&p)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "vote_round_id")
	})

}
