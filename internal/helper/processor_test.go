package helper

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
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

func TestProcessor_ProcessBatch_BroadcastAcceptedRetriesUntilCommit(t *testing.T) {
	store := newTestStore(t)
	prover := &mockProver{}
	tree := newMockTreeReader()
	var submitCalls atomic.Int32
	var committed atomic.Bool
	var submittedBodiesMu sync.Mutex
	var submittedBodies [][]byte

	// Fake chain server that accepts submissions.
	chainServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		submitCalls.Add(1)
		body, err := io.ReadAll(r.Body)
		if err == nil {
			submittedBodiesMu.Lock()
			submittedBodies = append(submittedBodies, body)
			submittedBodiesMu.Unlock()
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"tx_hash":"AABB","code":0,"log":""}`))
	}))
	defer chainServer.Close()

	submitter := NewChainSubmitter(chainServer.URL)
	expectedNullifier := [32]byte{0xBB}
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
				return [32]byte{}, nil
			},
			func(voteCommitment [32]byte, shareIndex uint32, primaryBlind [32]byte) ([32]byte, error) {
				return expectedNullifier, nil
			},
			func(roundIDHex string, shareNullifier []byte) (bool, error) {
				require.Equal(t, expectedNullifier[:], shareNullifier)
				return committed.Load(), nil
			},
		),
	)

	// Enqueue a share (zero delay in test store means immediately ready).
	roundID := hex.EncodeToString(make([]byte, 32))
	p := testPayload(roundID, 0)
	p.TreePosition = 0
	enqueueAndRequireInserted(t, store, p)

	// Process the batch — processBatch calls TakeReady internally.
	proc.processBatch(context.Background())

	// CheckTx acceptance is not commitment. The witness remains available for
	// retry until the pre-proof nullifier check observes committed state.
	status := store.Status()
	assert.Equal(t, 0, status[roundID].Submitted)
	assert.Equal(t, 1, status[roundID].Pending)
	share, ok := store.loadShare(roundID, 0, 1, 0)
	require.True(t, ok)
	assert.Equal(t, 0, share.Attempts)
	assert.NotEmpty(t, share.Payload.EncShare.C1)
	assert.NotEmpty(t, share.Payload.EncShare.C2)
	assert.NotEmpty(t, share.Payload.ShareComms)
	assert.NotEmpty(t, share.Payload.PrimaryBlind)
	require.NotNil(t, share.pendingBroadcast)
	assert.Equal(t, "AABB", share.pendingBroadcast.TxHash)
	assert.Equal(t, uint64(1), share.pendingBroadcast.SinceHeight)
	assert.NotEmpty(t, share.pendingBroadcast.Reveal.Proof)
	assert.Equal(t, int32(1), prover.callCount.Load())
	assert.Equal(t, int32(1), submitCalls.Load())

	key := schedKey(roundID, 0, 1, 0)
	for retry, expected := range []time.Duration{
		shareSystemRetryBackoff,
		20 * time.Second,
		40 * time.Second,
		80 * time.Second,
		shareStalledRetryMaxBackoff,
		shareStalledRetryMaxBackoff,
	} {
		store.mu.Lock()
		store.schedule[key] = time.Now().Add(-time.Second)
		store.mu.Unlock()
		proc.processBatch(context.Background())
		share, ok = store.loadShare(roundID, 0, 1, 0)
		require.True(t, ok)
		assert.Equal(t, 0, share.Attempts)
		store.mu.Lock()
		next, scheduled := store.schedule[key]
		store.mu.Unlock()
		require.True(t, scheduled, "retry %d", retry+1)
		assert.WithinDuration(t, time.Now().Add(expected), next, 300*time.Millisecond)
	}
	assert.Equal(t, int32(1), prover.callCount.Load())
	assert.Equal(t, int32(1), submitCalls.Load())

	// New blocks poll committed state but do not reprove or rebroadcast while the
	// accepted transaction has had fewer than 20 committed heights to land.
	for height := uint64(2); height <= pendingBroadcastRetryBlocks; height++ {
		tree.blockHeight.Store(height)
		store.mu.Lock()
		store.schedule[key] = time.Now().Add(-time.Second)
		store.mu.Unlock()
		proc.processBatch(context.Background())
		share, ok = store.loadShare(roundID, 0, 1, 0)
		require.True(t, ok)
		assert.Equal(t, 0, share.Attempts)
	}

	assert.Equal(t, int32(1), prover.callCount.Load())
	assert.Equal(t, int32(1), submitCalls.Load())

	// Once the committed-height timeout elapses, rebroadcast the exact persisted
	// message without generating a new randomized proof.
	tree.blockHeight.Store(1 + pendingBroadcastRetryBlocks)
	store.mu.Lock()
	store.schedule[key] = time.Now().Add(-time.Second)
	store.mu.Unlock()
	proc.processBatch(context.Background())

	assert.Equal(t, int32(1), prover.callCount.Load())
	assert.Equal(t, int32(2), submitCalls.Load())
	share, ok = store.loadShare(roundID, 0, 1, 0)
	require.True(t, ok)
	require.NotNil(t, share.pendingBroadcast)
	assert.Equal(t, uint64(1+pendingBroadcastRetryBlocks), share.pendingBroadcast.SinceHeight)
	submittedBodiesMu.Lock()
	require.Len(t, submittedBodies, 2)
	assert.Equal(t, submittedBodies[0], submittedBodies[1])
	submittedBodiesMu.Unlock()

	// A later committed-nullifier observation is the durable success signal. It
	// marks the row submitted and scrubs both witness and pending message data.
	committed.Store(true)
	tree.blockHeight.Store(2 + pendingBroadcastRetryBlocks)
	store.mu.Lock()
	store.schedule[key] = time.Now().Add(-time.Second)
	store.mu.Unlock()
	proc.processBatch(context.Background())

	status = store.Status()
	assert.Equal(t, 0, status[roundID].Pending)
	assert.Equal(t, 0, status[roundID].Failed)
	assert.Equal(t, 1, status[roundID].Submitted)
	share, ok = store.loadShare(roundID, 0, 1, 0)
	require.True(t, ok)
	assert.Equal(t, ShareStateSubmitted, share.State)
	assert.Nil(t, share.pendingBroadcast)
	assert.Empty(t, share.Payload.EncShare.C1)
	assert.Empty(t, share.Payload.EncShare.C2)
	assert.Empty(t, share.Payload.ShareComms)
	assert.Empty(t, share.Payload.PrimaryBlind)
}

func TestProcessor_ProcessBatch_CommittedPendingRevealWinsRoundTransition(t *testing.T) {
	store := newTestStore(t)
	prover := &mockProver{}
	expectedNullifier := bytes.Repeat([]byte{0x55}, 32)
	var roundStatusCalls atomic.Int32
	var nullifierCalls atomic.Int32

	chainServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("committed pending reveal must not be rebroadcast")
	}))
	defer chainServer.Close()

	proc := NewProcessor(
		store,
		&mockTreeReader{err: assert.AnError},
		prover,
		NewChainSubmitter(chainServer.URL),
		log.NewNopLogger(),
		1,
		func(roundID string) (bool, error) {
			roundStatusCalls.Add(1)
			return false, nil
		},
		WithPreProofShareDeduper(
			func(roundID, sharesHash [32]byte, proposalID, voteDecision uint32) ([32]byte, error) {
				return [32]byte{}, nil
			},
			func(voteCommitment [32]byte, shareIndex uint32, primaryBlind [32]byte) ([32]byte, error) {
				return [32]byte{}, nil
			},
			func(roundIDHex string, shareNullifier []byte) (bool, error) {
				nullifierCalls.Add(1)
				assert.Equal(t, expectedNullifier, shareNullifier)
				return true, nil
			},
		),
	)

	roundID := hex.EncodeToString(make([]byte, 32))
	payload := testPayload(roundID, 0)
	enqueueAndRequireInserted(t, store, payload)
	require.Len(t, store.TakeReady(), 1)
	require.NoError(t, store.markAwaitingCommit(roundID, 0, 1, 0, pendingRevealBroadcast{
		Reveal: MsgRevealShareJSON{
			ShareNullifier: base64.StdEncoding.EncodeToString(expectedNullifier),
		},
		TxHash:      "AABB",
		SinceHeight: 1,
	}))
	store.mu.Lock()
	store.schedule[schedKey(roundID, 0, 1, 0)] = time.Now().Add(-time.Second)
	store.mu.Unlock()

	proc.processBatch(context.Background())

	assert.Equal(t, int32(1), nullifierCalls.Load())
	assert.Equal(t, int32(0), roundStatusCalls.Load(), "commitment must be checked before inactive round status")
	assert.Equal(t, int32(0), prover.callCount.Load())
	status := store.Status()[roundID]
	assert.Equal(t, 1, status.Submitted)
	assert.Equal(t, 0, status.Pending)
	assert.Equal(t, 0, status.Failed)
	share, ok := store.loadShare(roundID, 0, 1, 0)
	require.True(t, ok)
	assert.Nil(t, share.pendingBroadcast)
	assert.Empty(t, share.Payload.PrimaryBlind)
}

func TestProcessor_ProcessBatch_PendingLookupErrorDefersRoundTransition(t *testing.T) {
	store := newTestStore(t)
	expectedNullifier := bytes.Repeat([]byte{0x66}, 32)
	var roundStatusCalls atomic.Int32

	chainServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("uncertain pending reveal must not be rebroadcast or failed")
	}))
	defer chainServer.Close()

	proc := NewProcessor(
		store,
		&mockTreeReader{err: assert.AnError},
		&mockProver{},
		NewChainSubmitter(chainServer.URL),
		log.NewNopLogger(),
		1,
		func(roundID string) (bool, error) {
			roundStatusCalls.Add(1)
			return false, nil
		},
		WithPreProofShareDeduper(
			func(roundID, sharesHash [32]byte, proposalID, voteDecision uint32) ([32]byte, error) {
				return [32]byte{}, nil
			},
			func(voteCommitment [32]byte, shareIndex uint32, primaryBlind [32]byte) ([32]byte, error) {
				return [32]byte{}, nil
			},
			func(roundIDHex string, shareNullifier []byte) (bool, error) {
				assert.Equal(t, expectedNullifier, shareNullifier)
				return false, assert.AnError
			},
		),
	)

	roundID := hex.EncodeToString(make([]byte, 32))
	enqueueAndRequireInserted(t, store, testPayload(roundID, 0))
	require.Len(t, store.TakeReady(), 1)
	pending := pendingRevealBroadcast{
		Reveal: MsgRevealShareJSON{
			ShareNullifier: base64.StdEncoding.EncodeToString(expectedNullifier),
		},
		TxHash:      "CCDD",
		SinceHeight: 1,
	}
	require.NoError(t, store.markAwaitingCommit(roundID, 0, 1, 0, pending))
	store.mu.Lock()
	store.schedule[schedKey(roundID, 0, 1, 0)] = time.Now().Add(-time.Second)
	store.mu.Unlock()

	proc.processBatch(context.Background())

	assert.Equal(t, int32(0), roundStatusCalls.Load(), "lookup uncertainty must be resolved before inactive status")
	status := store.Status()[roundID]
	assert.Equal(t, 1, status.Pending)
	assert.Equal(t, 0, status.Submitted)
	assert.Equal(t, 0, status.Failed)
	share, ok := store.loadShare(roundID, 0, 1, 0)
	require.True(t, ok)
	assert.Equal(t, ShareStateReceived, share.State)
	assert.Equal(t, 0, share.Attempts)
	require.NotNil(t, share.pendingBroadcast)
	assert.Equal(t, pending, *share.pendingBroadcast)
}

func TestProcessor_ProcessBatch_UnknownDeliveryReusesProofAcrossRestart(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "helper.db")
	now := uint64(time.Now().Unix())
	findRound := func(roundID string) (RoundInfo, error) {
		return RoundInfo{CreatedAtTime: now, VoteEndTime: now + testVoteEndOffset}, nil
	}
	store, err := NewShareStore(dbPath, findRound)
	require.NoError(t, err)
	defer func() {
		if store != nil {
			store.Close()
		}
	}()

	prover := &mockProver{}
	tree := newMockTreeReader()
	var submitCalls atomic.Int32
	var submittedBodiesMu sync.Mutex
	var submittedBodies [][]byte
	chainServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		submitCalls.Add(1)
		body, readErr := io.ReadAll(r.Body)
		if readErr == nil {
			submittedBodiesMu.Lock()
			submittedBodies = append(submittedBodies, body)
			submittedBodiesMu.Unlock()
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		w.Write([]byte(`{"error":"broadcast outcome unknown"}`))
	}))
	defer chainServer.Close()

	roundID := hex.EncodeToString(make([]byte, 32))
	enqueueAndRequireInserted(t, store, testPayload(roundID, 0))
	proc := NewProcessor(store, tree, prover, NewChainSubmitter(chainServer.URL), log.NewNopLogger(), 1, nil)
	proc.processBatch(context.Background())

	assert.Equal(t, int32(1), prover.callCount.Load())
	assert.Equal(t, int32(1), submitCalls.Load())
	share, ok := store.loadShare(roundID, 0, 1, 0)
	require.True(t, ok)
	require.NotNil(t, share.pendingBroadcast)
	assert.Empty(t, share.pendingBroadcast.TxHash)
	assert.Zero(t, share.pendingBroadcast.SinceHeight)
	require.NoError(t, store.Close())
	store = nil

	store, err = NewShareStore(dbPath, findRound)
	require.NoError(t, err)
	tree.blockHeight.Store(2)
	proc = NewProcessor(store, tree, prover, NewChainSubmitter(chainServer.URL), log.NewNopLogger(), 1, nil)
	proc.processBatch(context.Background())

	assert.Equal(t, int32(1), prover.callCount.Load(), "restart retry must reuse the staged proof")
	assert.Equal(t, int32(2), submitCalls.Load())
	submittedBodiesMu.Lock()
	require.Len(t, submittedBodies, 2)
	assert.Equal(t, submittedBodies[0], submittedBodies[1])
	submittedBodiesMu.Unlock()
}

func TestProcessor_ProcessBatch_BroadcastAcceptedUnknownDeadlineUsesFailureBudget(t *testing.T) {
	store := newTestStore(t)
	prover := &mockProver{}
	tree := newMockTreeReader()

	chainServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"tx_hash":"AABB","code":0,"log":""}`))
	}))
	defer chainServer.Close()

	proc := NewProcessor(store, tree, prover, NewChainSubmitter(chainServer.URL), log.NewNopLogger(), 2, nil)
	roundID := hex.EncodeToString(make([]byte, 32))
	p := testPayload(roundID, 0)
	p.TreePosition = 0
	enqueueAndRequireInserted(t, store, p)
	_, err := store.db.Exec("UPDATE shares SET vote_end_time = 0 WHERE round_id = ?", roundID)
	require.NoError(t, err)

	key := schedKey(roundID, 0, 1, 0)
	for height := uint64(1); height <= 5; height++ {
		tree.blockHeight.Store(height)
		if height > 1 {
			store.mu.Lock()
			store.schedule[key] = time.Now().Add(-time.Second)
			store.mu.Unlock()
		}
		proc.processBatch(context.Background())
	}

	status := store.Status()
	assert.Equal(t, 0, status[roundID].Pending)
	assert.Equal(t, 1, status[roundID].Failed)
	share, ok := store.loadShare(roundID, 0, 1, 0)
	require.True(t, ok)
	assert.Equal(t, ShareStateFailed, share.State)
	assert.Empty(t, share.Payload.EncShare.C1)
	assert.Empty(t, share.Payload.EncShare.C2)
	assert.Empty(t, share.Payload.ShareComms)
	assert.Empty(t, share.Payload.PrimaryBlind)
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

	err := proc.processShare(context.Background(), ready[0], false)
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
	err := proc.processShare(context.Background(), share, false)
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
