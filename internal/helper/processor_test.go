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

// mockTreeReader implements TreeReader for tests.
type mockTreeReader struct {
	leafCount    uint64
	anchorHeight uint64
	leaves       map[uint64][]byte
	err          error
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
	proc := NewProcessor(store, tree, prover, submitter, log.NewNopLogger(), 2, nil)

	// Enqueue a share (zero delay in test store means immediately ready).
	roundID := hex.EncodeToString(make([]byte, 32))
	p := testPayload(roundID, 0)
	p.TreePosition = 0
	enqueueAndRequireInserted(t, store, p)

	// Process the batch — processBatch calls TakeReady internally.
	proc.processBatch(context.Background())

	// Verify the prover was called.
	assert.Equal(t, int32(1), prover.callCount.Load())

	// Verify share is marked submitted.
	status := store.Status()
	assert.Equal(t, 1, status[roundID].Submitted)
}

func TestProcessor_ProcessBatch_ProofFailure(t *testing.T) {
	store := newTestStore(t)
	prover := &mockProver{err: assert.AnError}
	tree := newMockTreeReader()

	chainServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("should not submit when proof fails")
	}))
	defer chainServer.Close()

	submitter := NewChainSubmitter(chainServer.URL)
	proc := NewProcessor(store, tree, prover, submitter, log.NewNopLogger(), 2, nil)

	// Enqueue (zero delay, immediately ready).
	roundID := hex.EncodeToString(make([]byte, 32))
	p := testPayload(roundID, 0)
	p.TreePosition = 0
	enqueueAndRequireInserted(t, store, p)

	proc.processBatch(context.Background())

	// Should have been retried (attempts=1), back to pending.
	status := store.Status()
	assert.Equal(t, 1, status[roundID].Pending)
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

	// Share should be marked as failed (retried).
	status := store.Status()
	assert.Equal(t, 1, status[roundID].Pending) // back to pending for retry
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
				require.Equal(t, uint32(1), proposalID)
				require.Equal(t, uint32(0), voteDecision)
				return expectedCommitment, nil
			},
			func(voteCommitment [32]byte, shareIndex uint32, primaryBlind [32]byte) ([32]byte, error) {
				require.Equal(t, expectedCommitment, voteCommitment)
				require.Equal(t, uint32(0), shareIndex)
				return expectedNullifier, nil
			},
			func(roundIDHex string, shareNullifier []byte) (bool, error) {
				checkerCalls.Add(1)
				require.Equal(t, expectedNullifier[:], shareNullifier)
				return true, nil
			},
		),
	)

	roundID := hex.EncodeToString(make([]byte, 32))
	p := testPayload(roundID, 0)
	p.TreePosition = 100
	enqueueAndRequireInserted(t, store, p)

	proc.processBatch(context.Background())

	assert.Equal(t, int32(1), checkerCalls.Load())
	assert.Equal(t, int32(0), prover.callCount.Load())
	status := store.Status()
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
	store, err := NewShareStore(filepath.Join(t.TempDir(), "helper.db"), func(roundID string) (uint64, error) {
		return now + testVoteEndOffset, nil
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
		return prover.callCount.Load() == 1 && status[roundID].Submitted == 1
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
