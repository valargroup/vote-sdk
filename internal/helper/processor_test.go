package helper

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
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

type gatedProver struct {
	started     chan uint32
	release     chan struct{}
	inFlight    atomic.Int32
	maxInFlight atomic.Int32
}

func newGatedProver() *gatedProver {
	return &gatedProver{
		started: make(chan uint32, 16),
		release: make(chan struct{}, 16),
	}
}

func (p *gatedProver) GenerateShareRevealProof(
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

	p.started <- shareIndex
	<-p.release

	proof = make([]byte, 64)
	nullifier[0] = byte(shareIndex + 1)
	treeRoot[0] = 0x22
	return proof, nullifier, treeRoot, nil
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

// processBatch runs one bounded cycle for focused per-share outcome tests.
// Production dispatch is exercised through Processor.Run.
func (p *Processor) processBatch(ctx context.Context) bool {
	if p.isNodeReady != nil && !p.isNodeReady() {
		return false
	}
	ready := p.store.TakeReadyBatch(p.maxConcurrent)
	if len(ready) == 0 {
		return true
	}
	var workers sync.WaitGroup
	for _, share := range ready {
		workers.Add(1)
		go func() {
			defer workers.Done()
			p.processQueuedShare(ctx, share)
		}()
	}
	workers.Wait()
	return true
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
	blockHeight  atomic.Uint64
	leaves       map[uint64][]byte
	err          error
	pathErr      error
}

func (m *mockTreeReader) ForRound(_ []byte) TreeReader { return m }

func (m *mockTreeReader) LatestBlockHeight() uint64 { return m.blockHeight.Load() }

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
	tree := &mockTreeReader{
		leafCount:    1,
		anchorHeight: 1,
	}
	tree.blockHeight.Store(1)
	return tree
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

func (r *roundAwareTreeReader) LatestBlockHeight() uint64 { return 1 }

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

func TestProcessor_ProcessBatch_WaitsForCheckTxBeforeProof(t *testing.T) {
	store := newTestStore(t)
	prover := &mockProver{}
	tree := newMockTreeReader()
	var checkTxReady atomic.Bool
	var submitCalls atomic.Int32

	chainServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		submitCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		w.Write([]byte(`{"tx_hash":"","code":2,"log":"nullifier already spent"}`))
	}))
	defer chainServer.Close()

	proc := NewProcessor(
		store,
		tree,
		prover,
		NewChainSubmitter(chainServer.URL),
		log.NewNopLogger(),
		1,
		func(roundID string) (bool, error) {
			if !checkTxReady.Load() {
				return false, ErrCheckTxNotReady
			}
			return true, nil
		},
	)

	roundID := hex.EncodeToString(make([]byte, 32))
	enqueueAndRequireInserted(t, store, testPayload(roundID, 0))
	proc.processBatch(context.Background())

	assert.Equal(t, int32(0), prover.callCount.Load())
	assert.Equal(t, int32(0), submitCalls.Load())
	share, ok := store.loadShare(roundID, 0, 1, 0)
	require.True(t, ok)
	assert.Equal(t, ShareStateReceived, share.State)
	assert.Equal(t, 0, share.Attempts)

	checkTxReady.Store(true)
	store.mu.Lock()
	store.schedule[schedKey(roundID, 0, 1, 0)] = time.Now().Add(-time.Second)
	store.mu.Unlock()
	proc.processBatch(context.Background())

	assert.Equal(t, int32(1), prover.callCount.Load())
	assert.Equal(t, int32(1), submitCalls.Load())
	status := store.Status()
	assert.Equal(t, 1, status[roundID].Submitted)
}

func TestProcessor_ProcessBatch_WaitsForNodeReadiness(t *testing.T) {
	store := newTestStore(t)
	prover := &mockProver{}
	tree := newMockTreeReader()
	var nodeReady atomic.Bool
	var submitCalls atomic.Int32

	chainServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		submitCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"tx_hash":"","code":2,"log":"nullifier already spent"}`))
	}))
	defer chainServer.Close()

	proc := NewProcessor(
		store,
		tree,
		prover,
		NewChainSubmitter(chainServer.URL),
		log.NewNopLogger(),
		1,
		nil,
		WithProcessingReadinessCheck(nodeReady.Load),
	)

	roundID := hex.EncodeToString(make([]byte, 32))
	enqueueAndRequireInserted(t, store, testPayload(roundID, 0))
	scheduledBefore, ok := store.NextScheduledTime()
	require.True(t, ok)
	require.False(t, proc.processBatch(context.Background()))

	assert.Equal(t, int32(0), prover.callCount.Load())
	assert.Equal(t, int32(0), submitCalls.Load())
	scheduledAfter, ok := store.NextScheduledTime()
	require.True(t, ok)
	assert.Equal(t, scheduledBefore, scheduledAfter)
	share, ok := store.loadShare(roundID, 0, 1, 0)
	require.True(t, ok)
	assert.Equal(t, ShareStateReceived, share.State)
	assert.Equal(t, 0, share.Attempts)

	nodeReady.Store(true)
	require.True(t, proc.processBatch(context.Background()))

	assert.Equal(t, int32(1), prover.callCount.Load())
	assert.Equal(t, int32(1), submitCalls.Load())
	status := store.Status()
	assert.Equal(t, 1, status[roundID].Submitted)
}

func TestProcessor_ProcessBatch_RechecksNodeReadinessBetweenBatches(t *testing.T) {
	store := newTestStore(t)
	prover := &mockProver{}
	tree := newMockTreeReader()
	var nodeReady atomic.Bool
	nodeReady.Store(true)
	var submitCalls atomic.Int32

	chainServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		submitCalls.Add(1)
		nodeReady.Store(false)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"tx_hash":"","code":2,"log":"nullifier already spent"}`))
	}))
	defer chainServer.Close()

	proc := NewProcessor(
		store,
		tree,
		prover,
		NewChainSubmitter(chainServer.URL),
		log.NewNopLogger(),
		1,
		nil,
		WithProcessingReadinessCheck(nodeReady.Load),
	)

	roundID := hex.EncodeToString(make([]byte, 32))
	enqueueAndRequireInserted(t, store, testPayload(roundID, 0))
	enqueueAndRequireInserted(t, store, testPayload(roundID, 1))

	require.True(t, proc.processBatch(context.Background()))
	assert.Equal(t, int32(1), prover.callCount.Load())
	assert.Equal(t, int32(1), submitCalls.Load())
	status := store.Status()[roundID]
	assert.Equal(t, 1, status.Submitted)
	assert.Equal(t, 1, status.Pending)

	require.False(t, proc.processBatch(context.Background()))
	assert.Equal(t, int32(1), prover.callCount.Load())
	assert.Equal(t, int32(1), submitCalls.Load())

	var received, submitted int
	for shareIndex := range uint32(2) {
		share, ok := store.loadShare(roundID, shareIndex, 1, 0)
		require.True(t, ok)
		assert.Equal(t, 0, share.Attempts)
		switch share.State {
		case ShareStateReceived:
			received++
		case ShareStateSubmitted:
			submitted++
		}
	}
	assert.Equal(t, 1, received)
	assert.Equal(t, 1, submitted)

	nodeReady.Store(true)
	require.True(t, proc.processBatch(context.Background()))
	assert.Equal(t, int32(2), prover.callCount.Load())
	assert.Equal(t, int32(2), submitCalls.Load())
	status = store.Status()[roundID]
	assert.Equal(t, 2, status.Submitted)
	assert.Equal(t, 0, status.Pending)
}

func TestProcessor_SubmitHeightCacheEvictsOlderBlocks(t *testing.T) {
	proc := &Processor{
		submitKeys:        make(map[string]struct{}),
		stalledRetryCount: make(map[string]uint8),
	}
	roundID := hex.EncodeToString(make([]byte, 32))
	shareA := QueuedShare{Payload: SharePayload{
		VoteRoundID:  roundID,
		ProposalID:   1,
		TreePosition: 1,
		EncShare: EncryptedShareWire{
			ShareIndex: 1,
		},
	}}
	shareB := QueuedShare{Payload: SharePayload{
		VoteRoundID:  roundID,
		ProposalID:   1,
		TreePosition: 2,
		EncShare: EncryptedShareWire{
			ShareIndex: 2,
		},
	}}

	require.True(t, proc.claimSubmitHeight(shareA, 10))
	require.True(t, proc.claimSubmitHeight(shareB, 10))
	assert.Len(t, proc.submitKeys, 2)
	assert.Equal(t, uint8(1), proc.nextStalledRetryCount(shareA, 10))
	assert.Equal(t, uint8(2), proc.nextStalledRetryCount(shareA, 10))

	assert.False(t, proc.submittedAtHeight(shareA, 11))
	assert.Equal(t, uint64(11), proc.submitHeight)
	assert.Empty(t, proc.submitKeys)
	assert.Empty(t, proc.stalledRetryCount)
	assert.Equal(t, uint8(1), proc.nextStalledRetryCount(shareA, 11))

	assert.False(t, proc.claimSubmitHeight(shareB, 10), "a stale observation must not rotate the cache backwards")
	require.True(t, proc.claimSubmitHeight(shareA, 11))
	assert.False(t, proc.claimSubmitHeight(shareA, 11))
	assert.Len(t, proc.submitKeys, 1)
	assert.NotContains(t, proc.stalledRetryCount, shareScheduleKey(shareA))
}

func TestProcessor_BroadcastScheduleBoundsProofsUntilCommitment(t *testing.T) {
	store := newTestStore(t)
	prover := &mockProver{}
	tree := newMockTreeReader()
	var submitCalls atomic.Int32
	var committed atomic.Bool
	var checkUnavailable atomic.Bool
	chainServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		submitCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"tx_hash":"AABB","code":0,"log":""}`))
	}))
	defer chainServer.Close()
	proc := NewProcessor(store, tree, prover, NewChainSubmitter(chainServer.URL), log.NewNopLogger(), 2, nil,
		WithPreProofShareDeduper(
			func(_, _ [32]byte, _, _ uint32) ([32]byte, error) { return [32]byte{}, nil },
			func(_ [32]byte, _ uint32, _ [32]byte) ([32]byte, error) { return [32]byte{}, nil },
			func(_ string, _ []byte) (bool, error) {
				if checkUnavailable.Load() {
					return false, assert.AnError
				}
				return committed.Load(), nil
			},
		),
	)
	now := time.Now()
	proc.now = func() time.Time { return now }
	roundID := hex.EncodeToString(make([]byte, 32))
	payload := testPayload(roundID, 0)
	enqueueAndRequireInserted(t, store, payload)
	key := schedKey(roundID, 0, 1, 0)
	run := func() QueuedShare {
		t.Helper()
		store.mu.Lock()
		_, scheduled := store.schedule[key]
		store.schedule[key] = time.Now().Add(-time.Second)
		store.mu.Unlock()
		require.True(t, scheduled)
		proc.processBatch(context.Background())
		share, ok := store.loadShare(roundID, 0, 1, 0)
		require.True(t, ok)
		require.Zero(t, share.Attempts)
		return share
	}
	share := run()
	plan, err := decodeRelayPlan(share.relayPlan)
	require.NoError(t, err)
	require.Len(t, plan.Slots, 5)
	assert.Equal(t, int32(1), prover.callCount.Load())

	// Even an unavailable commitment checker cannot bypass the proof budget.
	checkUnavailable.Store(true)
	// New blocks alone do not trigger a proof before the next slot.
	for height := uint64(2); height <= 6; height++ {
		tree.blockHeight.Store(height)
		now = now.Add(time.Second)
		share = run()
		assert.Equal(t, ShareStateReceived, share.State)
		assert.Equal(t, payload.PrimaryBlind, share.Payload.PrimaryBlind)
	}
	assert.Equal(t, int32(1), prover.callCount.Load())

	for slot := 1; slot < len(plan.Slots); slot++ {
		now = plan.Slots[slot]
		tree.blockHeight.Store(uint64(slot + 10))
		share = run()
		assert.Equal(t, ShareStateReceived, share.State)
		assert.Equal(t, int32(slot+1), prover.callCount.Load())
		assert.Equal(t, int32(slot+1), submitCalls.Load())
	}

	// Exhaustion remains pending even as blocks and time continue advancing.
	for height := uint64(20); height <= 25; height++ {
		now = now.Add(time.Minute)
		tree.blockHeight.Store(height)
		share = run()
		assert.Equal(t, ShareStateReceived, share.State)
		assert.Equal(t, payload.PrimaryBlind, share.Payload.PrimaryBlind)
	}
	assert.Equal(t, int32(5), prover.callCount.Load())
	assert.Equal(t, int32(5), submitCalls.Load())

	checkUnavailable.Store(false)
	committed.Store(true)
	share = run()
	assert.Equal(t, ShareStateSubmitted, share.State)
	assert.Empty(t, share.Payload.EncShare.C1)
	assert.Empty(t, share.Payload.EncShare.C2)
	assert.Empty(t, share.Payload.ShareComms)
	assert.Empty(t, share.Payload.PrimaryBlind)
	assert.Equal(t, int32(5), prover.callCount.Load())
	_, scheduled := store.NextScheduledTime()
	assert.False(t, scheduled)
}

// advanceRelaySlot moves a focused test to its next persisted proof slot.
func advanceRelaySlot(t *testing.T, proc *Processor, store *ShareStore, roundID string) {
	t.Helper()
	share, ok := store.loadShare(roundID, 0, 1, 0)
	require.True(t, ok)
	plan, err := decodeRelayPlan(share.relayPlan)
	require.NoError(t, err)
	require.NotNil(t, plan)
	require.Less(t, plan.Next, len(plan.Slots))
	now := plan.Slots[plan.Next]
	proc.now = func() time.Time { return now }
}

func TestProcessor_ProcessShare_RejectsSuccessWithoutTxHash(t *testing.T) {
	store := newTestStore(t)
	prover := &mockProver{}
	tree := newMockTreeReader()

	chainServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"code":0,"log":""}`))
	}))
	defer chainServer.Close()

	proc := NewProcessor(
		store,
		tree,
		prover,
		NewChainSubmitter(chainServer.URL),
		log.NewNopLogger(),
		1,
		nil,
	)
	roundID := hex.EncodeToString(make([]byte, 32))
	payload := testPayload(roundID, 0)

	enqueueAndRequireInserted(t, store, payload)
	ready := store.TakeReady()
	require.Len(t, ready, 1)
	err := proc.processShare(context.Background(), ready[0])
	require.Error(t, err)
	assert.Contains(t, err.Error(), "without a transaction hash")
	action, stage := classifyShareFailure(err)
	assert.Equal(t, shareFailureFail, action)
	assert.Equal(t, failureStageSubmitChain, stage)
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

func TestHelperShareFailureFingerprintSeparatesQueueActions(t *testing.T) {
	retry := helperShareFailureFingerprint("round-1", failureStageSubmitHTTP, "retry")
	failed := helperShareFailureFingerprint("round-1", failureStageSubmitHTTP, "failed")

	assert.Equal(t, []string{
		helperShareFailureAlert,
		"round-1",
		failureStageSubmitHTTP,
		"retry",
	}, retry)
	assert.NotEqual(t, retry, failed)
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
	var submitCalls atomic.Int32

	// Chain returns non-zero code with a non-nullifier error.
	chainServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		submitCalls.Add(1)
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

	key := schedKey(roundID, 0, 1, 0)
	store.mu.Lock()
	store.schedule[key] = time.Now().Add(-time.Second)
	store.mu.Unlock()
	proc.processBatch(context.Background())

	share, ok = store.loadShare(roundID, 0, 1, 0)
	require.True(t, ok)
	assert.Equal(t, 1, share.Attempts)
	assert.Equal(t, int32(1), prover.callCount.Load())
	assert.Equal(t, int32(1), submitCalls.Load())

	tree.blockHeight.Store(2)
	advanceRelaySlot(t, proc, store, roundID)
	store.mu.Lock()
	store.schedule[key] = time.Now().Add(-time.Second)
	store.mu.Unlock()
	proc.processBatch(context.Background())

	share, ok = store.loadShare(roundID, 0, 1, 0)
	require.True(t, ok)
	assert.Equal(t, 2, share.Attempts)
	assert.Equal(t, int32(2), prover.callCount.Load())
	assert.Equal(t, int32(2), submitCalls.Load())
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
	var submitCalls atomic.Int32

	chainServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		submitCalls.Add(1)
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
	assert.Equal(t, int32(1), prover.callCount.Load())
	assert.Equal(t, int32(1), submitCalls.Load())
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
	tree.blockHeight.Store(2)
	advanceRelaySlot(t, proc, store, roundID)
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
			tree: func() TreeReader {
				tree := newMockTreeReader()
				tree.pathErr = assert.AnError
				return tree
			}(),
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

func TestProcessor_ProcessBatch_DuplicateNullifierTreatedAsSuccess(t *testing.T) {
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
	proc := NewProcessor(store, tree, prover, submitter, log.NewNopLogger(), 2, nil)

	roundID := hex.EncodeToString(make([]byte, 32))
	p := testPayload(roundID, 0)
	p.TreePosition = 0
	enqueueAndRequireInserted(t, store, p)

	proc.processBatch(context.Background())

	// Share should be marked as submitted (not retried), because the
	// duplicate nullifier means the vote was already revealed on-chain.
	status := store.Status()
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
		return prover.callCount.Load() == 1 && status[roundID].Pending == 1
	}, time.Second, 10*time.Millisecond)

	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
}

func TestProcessor_Run_RefillsFreedSlotWithoutBatchBarrier(t *testing.T) {
	store := newTestStore(t)
	prover := newGatedProver()
	tree := newMockTreeReader()

	chainServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"tx_hash":"","code":2,"log":"nullifier already spent"}`))
	}))
	defer chainServer.Close()

	proc := NewProcessor(store, tree, prover, NewChainSubmitter(chainServer.URL), log.NewNopLogger(), 2, nil)
	roundID := hex.EncodeToString(make([]byte, 32))
	for i := range uint32(2) {
		enqueueAndRequireInserted(t, store, testPayload(roundID, i))
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- proc.Run(ctx) }()

	started := map[uint32]bool{
		<-prover.started: true,
		<-prover.started: true,
	}
	require.Len(t, started, 2)

	enqueueAndRequireInserted(t, store, testPayload(roundID, 2))
	prover.release <- struct{}{}
	select {
	case third := <-prover.started:
		assert.False(t, started[third], "a queued share should fill the freed slot")
	case <-time.After(time.Second):
		t.Fatal("third share did not start when one worker became free")
	}
	assert.Equal(t, int32(2), prover.maxInFlight.Load())

	prover.release <- struct{}{}
	prover.release <- struct{}{}
	require.Eventually(t, func() bool {
		return store.Status()[roundID].Submitted == 3
	}, time.Second, 10*time.Millisecond)

	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
}

func TestProcessor_Run_PausesRefillUntilNodeReady(t *testing.T) {
	store := newTestStore(t)
	prover := newGatedProver()
	tree := newMockTreeReader()
	var nodeReady atomic.Bool
	nodeReady.Store(true)

	chainServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"tx_hash":"","code":2,"log":"nullifier already spent"}`))
	}))
	defer chainServer.Close()

	proc := NewProcessor(
		store,
		tree,
		prover,
		NewChainSubmitter(chainServer.URL),
		log.NewNopLogger(),
		2,
		nil,
		WithProcessingReadinessCheck(nodeReady.Load),
	)
	proc.readinessRetry = 20 * time.Millisecond

	roundID := hex.EncodeToString(make([]byte, 32))
	for i := range uint32(3) {
		enqueueAndRequireInserted(t, store, testPayload(roundID, i))
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- proc.Run(ctx) }()

	<-prover.started
	<-prover.started
	nodeReady.Store(false)
	prover.release <- struct{}{}

	select {
	case shareIndex := <-prover.started:
		t.Fatalf("share %d started while node readiness was false", shareIndex)
	case <-time.After(60 * time.Millisecond):
	}

	nodeReady.Store(true)
	select {
	case <-prover.started:
	case <-time.After(time.Second):
		t.Fatal("queued share did not resume after node readiness recovered")
	}

	prover.release <- struct{}{}
	prover.release <- struct{}{}
	require.Eventually(t, func() bool {
		return store.Status()[roundID].Submitted == 3
	}, time.Second, 10*time.Millisecond)
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
}

func TestProcessor_Run_CancellationWaitsAndReturnsTakenShares(t *testing.T) {
	store := newTestStore(t)
	prover := newGatedProver()
	tree := newMockTreeReader()
	submitter := NewChainSubmitter("http://localhost:0")
	proc := NewProcessor(store, tree, prover, submitter, log.NewNopLogger(), 2, nil)

	roundID := hex.EncodeToString(make([]byte, 32))
	for i := range uint32(3) {
		enqueueAndRequireInserted(t, store, testPayload(roundID, i))
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- proc.Run(ctx) }()
	<-prover.started
	<-prover.started

	cancel()
	select {
	case err := <-done:
		t.Fatalf("Run returned before non-cancelable proofs exited: %v", err)
	case <-time.After(30 * time.Millisecond):
	}

	prover.release <- struct{}{}
	prover.release <- struct{}{}
	require.ErrorIs(t, <-done, context.Canceled)

	status := store.Status()[roundID]
	assert.Equal(t, 3, status.Pending)
	for i := range uint32(3) {
		share, ok := store.loadShare(roundID, i, 1, 0)
		require.True(t, ok)
		assert.Equal(t, ShareStateReceived, share.State)
		assert.Zero(t, share.Attempts)
	}
}

func TestProcessor_Run_MaintenanceRunsUnderDeepQueue(t *testing.T) {
	now := uint64(time.Now().Unix())
	maintenanceRoundBytes := make([]byte, 32)
	maintenanceRoundBytes[31] = 1
	maintenanceRound := hex.EncodeToString(maintenanceRoundBytes)
	deepRoundBytes := make([]byte, 32)
	deepRoundBytes[31] = 2
	deepRound := hex.EncodeToString(deepRoundBytes)

	store, err := NewShareStore(filepath.Join(t.TempDir(), "helper.db"), func(roundID string) (RoundInfo, error) {
		if roundID == maintenanceRound {
			return RoundInfo{CreatedAtTime: now - 100, VoteEndTime: now - 1}, nil
		}
		return RoundInfo{CreatedAtTime: now, VoteEndTime: now + testVoteEndOffset}, nil
	})
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	enqueueAndRequireInserted(t, store, testPayload(maintenanceRound, 0))
	store.mu.Lock()
	delete(store.schedule, schedKey(maintenanceRound, 0, 1, 0))
	store.mu.Unlock()

	for i := range uint32(80) {
		enqueueAndRequireInserted(t, store, testPayload(deepRound, i))
	}

	prover := &trackingProver{sleep: 3 * time.Millisecond}
	chainServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"tx_hash":"","code":2,"log":"nullifier already spent"}`))
	}))
	defer chainServer.Close()

	var closureChecks atomic.Int32
	proc := NewProcessor(
		store,
		newMockTreeReader(),
		prover,
		NewChainSubmitter(chainServer.URL),
		log.NewNopLogger(),
		2,
		nil,
		WithProcessingReadinessCheck(func() bool { return true }),
		WithRoundClosureCheck(func(roundID string) (bool, error) {
			if roundID == maintenanceRound {
				closureChecks.Add(1)
			}
			return false, nil
		}),
	)
	proc.maintenanceEvery = 15 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- proc.Run(ctx) }()

	require.Eventually(t, func() bool {
		return closureChecks.Load() >= 2
	}, 2*time.Second, 10*time.Millisecond)
	assert.LessOrEqual(t, prover.maxInFlight.Load(), int32(2))

	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
}

func TestProcessor_Run_MaintenanceWaitsForActiveWorkerBeforePurge(t *testing.T) {
	now := uint64(time.Now().Unix())
	closedBytes := make([]byte, 32)
	closedBytes[31] = 3
	closedRound := hex.EncodeToString(closedBytes)
	pendingBytes := make([]byte, 32)
	pendingBytes[31] = 4
	pendingRound := hex.EncodeToString(pendingBytes)

	store, err := NewShareStore(filepath.Join(t.TempDir(), "helper.db"), func(roundID string) (RoundInfo, error) {
		if roundID == closedRound {
			return RoundInfo{CreatedAtTime: now - 100, VoteEndTime: now - 1}, nil
		}
		return RoundInfo{CreatedAtTime: now, VoteEndTime: now + testVoteEndOffset}, nil
	})
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	closedShare := testPayload(closedRound, 0)
	enqueueAndRequireInserted(t, store, closedShare)
	pendingShare := testPayload(pendingRound, 0)
	pendingShare.SubmitAt = now + 60
	enqueueAndRequireInserted(t, store, pendingShare)

	prover := newGatedProver()
	chainServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"tx_hash":"","code":2,"log":"nullifier already spent"}`))
	}))
	defer chainServer.Close()

	var closed atomic.Bool
	proc := NewProcessor(
		store,
		newMockTreeReader(),
		prover,
		NewChainSubmitter(chainServer.URL),
		log.NewNopLogger(),
		1,
		nil,
		WithRoundClosureCheck(func(roundID string) (bool, error) {
			return roundID == closedRound && closed.Load(), nil
		}),
		WithProcessingReadinessCheck(func() bool { return true }),
	)
	proc.maintenanceEvery = 10 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- proc.Run(ctx) }()

	// The share is active and still owns its witness when the round becomes
	// closed. Maintenance must not purge it until the worker has completed.
	<-prover.started
	closed.Store(true)
	require.Eventually(t, func() bool {
		share, ok := store.loadShare(closedRound, 0, 1, 0)
		return ok && share.State == ShareStateWitnessed
	}, time.Second, 10*time.Millisecond)
	share, ok := store.loadShare(closedRound, 0, 1, 0)
	require.True(t, ok)
	assert.Equal(t, ShareStateWitnessed, share.State)
	assert.NotEmpty(t, share.Payload.PrimaryBlind)

	// Release the worker. Its duplicate result completes the share, after which
	// the next maintenance pass may safely purge the closed round.
	prover.release <- struct{}{}
	require.Eventually(t, func() bool {
		return store.Status()[closedRound].Total == 0
	}, time.Second, 10*time.Millisecond)
	assert.Equal(t, 1, store.Status()[pendingRound].Pending)
	queuedPending, pendingExists := store.loadShare(pendingRound, 0, 1, 0)
	require.True(t, pendingExists)
	assert.Equal(t, ShareStateReceived, queuedPending.State)

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
	err := proc.processShare(context.Background(), share)
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
	proc := NewProcessor(store, tree, prover, submitter, log.NewNopLogger(), 0, nil)
	assert.Equal(t, 1, proc.maxConcurrent)

	roundID := hex.EncodeToString(make([]byte, 32))
	p := testPayload(roundID, 0)
	p.TreePosition = 0
	enqueueAndRequireInserted(t, store, p)

	proc.processBatch(context.Background())

	status := store.Status()
	assert.Equal(t, 1, status[roundID].Pending)
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
	proc := NewProcessor(store, tree, prover, submitter, log.NewNopLogger(), 1, nil)

	roundID := hex.EncodeToString(make([]byte, 32))
	for i := 0; i < 4; i++ {
		p := testPayload(roundID, uint32(i))
		p.TreePosition = 0
		enqueueAndRequireInserted(t, store, p)
	}

	proc.processBatch(context.Background())

	maxSeen := prover.maxInFlight.Load()
	assert.Equal(t, int32(1), maxSeen)

	status := store.Status()
	assert.Equal(t, 4, status[roundID].Pending)
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
	proc := NewProcessor(store, tree, prover, submitter, log.NewNopLogger(), 2, nil)

	pA := testPayload(roundA, 0)
	pA.TreePosition = 0
	enqueueAndRequireInserted(t, store, pA)

	pB := testPayload(roundB, 0)
	pB.TreePosition = 1
	enqueueAndRequireInserted(t, store, pB)

	proc.processBatch(context.Background())

	assert.Equal(t, int32(2), prover.callCount.Load())
	assert.Equal(t, int32(2), tree.state.maxInFlight.Load())

	status := store.Status()
	assert.Equal(t, 1, status[roundA].Pending)
	assert.Equal(t, 1, status[roundB].Pending)
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
		EncShare:     testPayload(roundID, 0).EncShare,
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

func TestProcessorCleanupRequiresCommittedClosure(t *testing.T) {
	for _, tc := range []struct {
		name                          string
		ready, closed, missingChecker bool
		missingReadiness              bool
		statusErr                     error
		wantDeleted                   bool
	}{
		{name: "active", ready: true},
		{name: "closed", ready: true, closed: true, wantDeleted: true},
		{name: "node_paused", closed: true},
		{name: "status_unavailable", ready: true, statusErr: assert.AnError},
		{name: "round_unknown", ready: true, statusErr: ErrUnknownRound},
		{name: "restart_not_ready", ready: true, statusErr: ErrCheckTxNotReady},
		{name: "missing_checker", ready: true, missingChecker: true},
		{name: "missing_readiness", closed: true, missingReadiness: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			end := uint64(time.Now().Add(-time.Minute).Unix())
			store, err := NewShareStore(":memory:", func(string) (RoundInfo, error) { return RoundInfo{CreatedAtTime: end - 3600, VoteEndTime: end}, nil })
			require.NoError(t, err)
			defer store.Close()
			immediate := testPayload("aabbccdd", 0)
			scheduled := testPayload("aabbccdd", 1)
			scheduled.SubmitAt = end - 10
			enqueueAndRequireInserted(t, store, immediate)
			enqueueAndRequireInserted(t, store, scheduled)
			_, err = store.db.Exec("UPDATE shares SET received_at = ?", end-20)
			require.NoError(t, err)
			var logs bytes.Buffer
			checks := 0
			var checker RoundClosureChecker
			if !tc.missingChecker {
				checker = func(string) (bool, error) { checks++; return tc.closed, tc.statusErr }
			}
			var readiness func() bool
			if !tc.missingReadiness {
				readiness = func() bool { return tc.ready }
			}
			proc := NewProcessor(store, nil, nil, nil, log.NewLogger(&logs), 1, nil,
				WithProcessingReadinessCheck(readiness), WithRoundClosureCheck(checker))
			proc.cleanupClosedRounds()
			if tc.wantDeleted {
				require.Zero(t, store.Status()["aabbccdd"].Total)
				require.Empty(t, store.schedule)
				require.Empty(t, store.roundCache)
				require.Contains(t, logs.String(), "round closed with unsubmitted shares")
			} else {
				require.Equal(t, 2, store.Status()["aabbccdd"].Pending)
				require.Len(t, store.schedule, 2)
				require.Contains(t, store.roundCache, "aabbccdd")
				require.NotContains(t, logs.String(), "round closed with unsubmitted shares")
			}
			if !tc.ready || tc.missingChecker {
				require.Zero(t, checks)
			}
		})
	}
}

func TestProcessorRunRetainsExpiredQueueWhilePaused(t *testing.T) {
	end := uint64(time.Now().Add(-time.Minute).Unix())
	store, err := NewShareStore(":memory:", func(string) (RoundInfo, error) {
		return RoundInfo{CreatedAtTime: end - 3600, VoteEndTime: end}, nil
	})
	require.NoError(t, err)
	defer store.Close()
	enqueueAndRequireInserted(t, store, testPayload("aabbccdd", 0))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	proc := NewProcessor(store, nil, nil, nil, log.NewNopLogger(), 1, nil,
		WithProcessingReadinessCheck(func() bool { cancel(); return false }),
		WithRoundClosureCheck(func(string) (bool, error) {
			t.Fatal("paused nodes must not check round closure")
			return false, nil
		}))
	require.ErrorIs(t, proc.Run(ctx), context.Canceled)
	require.Equal(t, 1, store.Status()["aabbccdd"].Pending)
	require.Len(t, store.schedule, 1)
}

func TestProcessorCleanupAfterRestartAndClosure(t *testing.T) {
	end := uint64(time.Now().Add(-time.Minute).Unix())
	fetcher := func(string) (RoundInfo, error) { return RoundInfo{CreatedAtTime: end - 3600, VoteEndTime: end}, nil }
	path := filepath.Join(t.TempDir(), "helper.db")
	store, err := NewShareStore(path, fetcher)
	require.NoError(t, err)
	enqueueAndRequireInserted(t, store, testPayload("aabbccdd", 0))
	require.NoError(t, store.Close())
	store, err = NewShareStore(path, fetcher)
	require.NoError(t, err)
	defer store.Close()
	closed := false
	proc := NewProcessor(store, nil, nil, nil, log.NewNopLogger(), 1, nil,
		WithProcessingReadinessCheck(func() bool { return true }),
		WithRoundClosureCheck(func(string) (bool, error) { return closed, nil }))
	proc.cleanupClosedRounds()
	require.Equal(t, 1, store.Status()["aabbccdd"].Pending)
	require.Len(t, store.schedule, 1)
	closed = true
	proc.cleanupClosedRounds()
	require.Zero(t, store.Status()["aabbccdd"].Total, "late-received shares must also be cleaned once closure is confirmed")
	require.Empty(t, store.schedule)
}

func TestProcessor_RestartHonorsRelayHeightAndCutoff(t *testing.T) {
	store := newTestStore(t)
	prover := &mockProver{}
	tree := newMockTreeReader()
	tree.blockHeight.Store(100)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Write([]byte(`{"tx_hash":"OK","code":0}`))
	}))
	defer server.Close()
	roundID := hex.EncodeToString(make([]byte, 32))
	enqueueAndRequireInserted(t, store, testPayload(roundID, 0))
	ready := store.TakeReady()
	require.Len(t, ready, 1)
	start := time.Now()
	plan := newRelayPlan(start, ready[0].VoteEndTime)
	plan.Next, plan.LastStart, plan.LastHeight = 1, start, 100
	require.NoError(t, store.reserveRelaySlot(ready[0], plan))
	store.MarkRetry(roundID, 0, 1, 0)

	// A new processor has an empty height cache, but the reservation is durable.
	proc := NewProcessor(store, tree, prover, NewChainSubmitter(server.URL), log.NewNopLogger(), 1, nil)
	now := plan.Slots[1]
	proc.now = func() time.Time { return now }
	key := schedKey(roundID, 0, 1, 0)
	run := func() {
		store.mu.Lock()
		store.schedule[key] = time.Now().Add(-time.Second)
		store.mu.Unlock()
		proc.processBatch(context.Background())
	}
	run()
	assert.Zero(t, prover.callCount.Load(), "same-height reservation survives restart")
	tree.blockHeight.Store(101)
	run()
	assert.Equal(t, int32(1), prover.callCount.Load())
	assert.Equal(t, int32(1), calls.Load())

	// A late wakeup skips the remaining budget once the safety cutoff passed.
	now = plan.Cutoff.Add(time.Second)
	tree.blockHeight.Store(102)
	run()
	assert.Equal(t, int32(1), prover.callCount.Load())
	share, ok := store.loadShare(roundID, 0, 1, 0)
	require.True(t, ok)
	assert.Equal(t, ShareStateReceived, share.State)
	assert.NotEmpty(t, share.Payload.PrimaryBlind)
}
