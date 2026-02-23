package helper

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"cosmossdk.io/log"
	"github.com/stretchr/testify/assert"
)

// mockProver returns a fixed proof and nullifier.
type mockProver struct {
	callCount atomic.Int32
	err       error
}

func (m *mockProver) GenerateShareRevealProof(
	merklePath []byte,
	allEncShares [10][32]byte,
	shareIndex uint32,
	proposalID, voteDecision uint32,
	roundID [32]byte,
	sharesHash [32]byte,
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
	allEncShares [10][32]byte,
	shareIndex uint32,
	proposalID, voteDecision uint32,
	roundID [32]byte,
	sharesHash [32]byte,
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

// mockMerklePath returns a fixed 772-byte path (matching votetree.MerklePathBytes).
func mockMerklePath(leaves [][]byte, position uint64) ([]byte, error) {
	return make([]byte, 772), nil
}

// mockTreeReader returns a fixed set of leaves.
type mockTreeReader struct {
	leaves       [][]byte
	anchorHeight uint64
	err          error
}

func (m *mockTreeReader) GetAllLeaves() ([][]byte, uint64, error) {
	if m.err != nil {
		return nil, 0, m.err
	}
	return m.leaves, m.anchorHeight, nil
}

func (m *mockTreeReader) GetTreeStatus() (TreeStatus, error) {
	if m.err != nil {
		return TreeStatus{}, m.err
	}
	return TreeStatus{
		LeafCount:    uint64(len(m.leaves)),
		AnchorHeight: m.anchorHeight,
	}, nil
}

func newMockTreeReader() *mockTreeReader {
	// Create leaves for a tree with at least 1 leaf.
	// Each leaf is 32 bytes (a vote commitment hash).
	leaf := make([]byte, 32)
	return &mockTreeReader{
		leaves:       [][]byte{leaf},
		anchorHeight: 1,
	}
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
	proc := NewProcessor(store, tree, mockMerklePath, prover, submitter, log.NewNopLogger(), time.Second, 2)

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
	proc := NewProcessor(store, tree, mockMerklePath, prover, submitter, log.NewNopLogger(), time.Second, 2)

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

	// Chain returns non-zero code.
	chainServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"tx_hash":"","code":5,"log":"duplicate nullifier"}`))
	}))
	defer chainServer.Close()

	submitter := NewChainSubmitter(chainServer.URL)
	proc := NewProcessor(store, tree, mockMerklePath, prover, submitter, log.NewNopLogger(), time.Second, 2)

	roundID := hex.EncodeToString(make([]byte, 32))
	p := testPayload(roundID, 0)
	p.TreePosition = 0
	enqueueAndRequireInserted(t, store, p)

	proc.processBatch(context.Background())

	// Share should be marked as failed (retried).
	status := store.Status()
	assert.Equal(t, 1, status[roundID].Pending) // back to pending for retry
}

func TestProcessor_Run_CancelContext(t *testing.T) {
	store := newTestStore(t)
	prover := &mockProver{}
	tree := newMockTreeReader()
	submitter := NewChainSubmitter("http://localhost:0")
	proc := NewProcessor(store, tree, mockMerklePath, prover, submitter, log.NewNopLogger(), 50*time.Millisecond, 2)

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- proc.Run(ctx)
	}()

	// Let it run for a bit.
	time.Sleep(100 * time.Millisecond)
	cancel()

	err := <-done
	assert.ErrorIs(t, err, context.Canceled)
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
	proc := NewProcessor(store, tree, mockMerklePath, prover, submitter, log.NewNopLogger(), time.Second, 2)

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
	proc := NewProcessor(store, tree, mockMerklePath, prover, submitter, log.NewNopLogger(), time.Second, 0)
	assert.Equal(t, 1, proc.maxConcurrent)

	roundID := hex.EncodeToString(make([]byte, 32))
	p := testPayload(roundID, 0)
	p.TreePosition = 0
	enqueueAndRequireInserted(t, store, p)

	proc.processBatch(context.Background())

	status := store.Status()
	assert.Equal(t, 1, status[roundID].Submitted)
}

func TestProcessor_ProcessBatch_ConcurrencyLimit(t *testing.T) {
	store := newTestStore(t)
	prover := &trackingProver{sleep: 60 * time.Millisecond}
	tree := newMockTreeReader()

	chainServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"tx_hash":"OK","code":0,"log":""}`))
	}))
	defer chainServer.Close()

	submitter := NewChainSubmitter(chainServer.URL)
	proc := NewProcessor(store, tree, mockMerklePath, prover, submitter, log.NewNopLogger(), time.Second, 2)

	roundID := hex.EncodeToString(make([]byte, 32))
	for i := 0; i < 4; i++ {
		p := testPayload(roundID, uint32(i))
		p.TreePosition = 0
		enqueueAndRequireInserted(t, store, p)
	}

	proc.processBatch(context.Background())

	maxSeen := prover.maxInFlight.Load()
	assert.LessOrEqual(t, maxSeen, int32(2))
	assert.GreaterOrEqual(t, maxSeen, int32(2))

	status := store.Status()
	assert.Equal(t, 4, status[roundID].Submitted)
}

func TestValidatePayload(t *testing.T) {
	// Build a valid 64-character hex round ID (32 bytes).
	roundID := hex.EncodeToString(make([]byte, 32))
	b64_32 := base64.StdEncoding.EncodeToString(make([]byte, 32))

	valid := SharePayload{
		SharesHash:   b64_32,
		ProposalID:   1,
		VoteDecision: 0,
		EncShare:     EncryptedShareWire{C1: b64_32, C2: b64_32, ShareIndex: 0},
		ShareIndex:   0,
		TreePosition: 0,
		VoteRoundID:  roundID,
		AllEncShares: []EncryptedShareWire{
			{C1: b64_32, C2: b64_32, ShareIndex: 0},
			{C1: b64_32, C2: b64_32, ShareIndex: 1},
			{C1: b64_32, C2: b64_32, ShareIndex: 2},
			{C1: b64_32, C2: b64_32, ShareIndex: 3},
			{C1: b64_32, C2: b64_32, ShareIndex: 4},
		},
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

	t.Run("all_enc_shares wrong order", func(t *testing.T) {
		p := valid
		p.AllEncShares[2].ShareIndex = 99
		err := validatePayload(&p)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "all_enc_shares[2]")
	})
}
