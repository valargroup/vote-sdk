package helper

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cosmossdk.io/log"
)

func newTestRouter(t *testing.T) (*mux.Router, *ShareStore) {
	t.Helper()
	store := newTestStore(t)
	router := mux.NewRouter()
	RegisterRoutes(router, store, log.NewNopLogger())
	return router, store
}

func newQueueStatusRouter(t *testing.T, token string) (*mux.Router, *ShareStore) {
	t.Helper()
	store := newTestStore(t)
	router := mux.NewRouter()
	RegisterRoutesWithGetters(
		router,
		func() *ShareStore { return store },
		func() string { return token },
		func() bool { return true },
		nil,
		nil,
		log.NewNopLogger(),
	)
	return router, store
}

func enqueueInserted(t *testing.T, s *ShareStore, p SharePayload) {
	t.Helper()
	result, err := s.Enqueue(p)
	require.NoError(t, err)
	require.Equal(t, EnqueueInserted, result)
}

func validPayloadJSON() string {
	p := testPayload("aabbccddaabbccddaabbccddaabbccddaabbccddaabbccddaabbccddaabbccdd", 0)
	b, _ := json.Marshal(p)
	return string(b)
}

func TestSubmitShare_Success(t *testing.T) {
	router, store := newTestRouter(t)

	req := httptest.NewRequest("POST", "/api/v1/shares", strings.NewReader(validPayloadJSON()))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	var resp submitResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "queued", resp.Status)

	// Verify share was actually enqueued.
	status := store.Status()
	assert.Equal(t, 1, status["aabbccddaabbccddaabbccddaabbccddaabbccddaabbccddaabbccddaabbccdd"].Total)
}

func TestSubmitShare_InvalidJSON(t *testing.T) {
	router, _ := newTestRouter(t)

	req := httptest.NewRequest("POST", "/api/v1/shares", strings.NewReader("not json"))
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestSubmitShare_ValidationErrors(t *testing.T) {
	tests := []struct {
		name    string
		modify  func(*SharePayload)
		errPart string
	}{
		{
			name:    "bad shares_hash base64",
			modify:  func(p *SharePayload) { p.SharesHash = "not-valid-base64!!!" },
			errPart: "shares_hash",
		},
		{
			name:    "share_index out of range",
			modify:  func(p *SharePayload) { p.EncShare.ShareIndex = 5 },
			errPart: "share_index",
		},
		{
			name: "share_index mismatch",
			modify: func(p *SharePayload) {
				p.ShareIndex = 1
				p.EncShare.ShareIndex = 0
			},
			errPart: "share_index must match",
		},
		{
			name:    "vote_decision out of range",
			modify:  func(p *SharePayload) { p.VoteDecision = 8 },
			errPart: "vote_decision",
		},
		{
			name:    "bad round_id hex",
			modify:  func(p *SharePayload) { p.VoteRoundID = "ZZ" },
			errPart: "vote_round_id",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			router, _ := newTestRouter(t)

			p := testPayload("aabbccddaabbccddaabbccddaabbccddaabbccddaabbccddaabbccddaabbccdd", 0)
			tc.modify(&p)
			body, _ := json.Marshal(p)

			req := httptest.NewRequest("POST", "/api/v1/shares", strings.NewReader(string(body)))
			w := httptest.NewRecorder()

			router.ServeHTTP(w, req)
			assert.Equal(t, http.StatusBadRequest, w.Code)
			assert.Contains(t, w.Body.String(), tc.errPart)
		})
	}
}

func TestStatus_Empty(t *testing.T) {
	router, _ := newTestRouter(t)

	req := httptest.NewRequest("GET", "/api/v1/status", nil)
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	var resp statusResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "ok", resp.Status)
}

func TestStatus_WithShares(t *testing.T) {
	router, store := newTestRouter(t)

	roundID := "aabbccddaabbccddaabbccddaabbccddaabbccddaabbccddaabbccddaabbccdd"
	enqueueInserted(t, store, testPayload(roundID, 0))
	enqueueInserted(t, store, testPayload(roundID, 1))

	req := httptest.NewRequest("GET", "/api/v1/status", nil)
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	var resp statusResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "ok", resp.Status)

	// Queue counts are intentionally omitted from the response to prevent
	// timing correlation by observers polling the status endpoint.
}

func TestQueueStatus_DisabledByDefault(t *testing.T) {
	router, _ := newTestRouter(t)

	req := httptest.NewRequest("GET", "/api/v1/queue-status", nil)
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestQueueStatus_RequiresTokenWhenEnabled(t *testing.T) {
	router, store := newQueueStatusRouter(t, "secret-token")
	roundID := "aabbccddaabbccddaabbccddaabbccddaabbccddaabbccddaabbccddaabbccdd"
	enqueueInserted(t, store, testPayload(roundID, 0))

	t.Run("missing token rejected", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/v1/queue-status", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusUnauthorized, w.Code)
	})

	t.Run("wrong token rejected", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/v1/queue-status", nil)
		req.Header.Set("X-Helper-Token", "wrong")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusUnauthorized, w.Code)
	})

	t.Run("valid token returns status", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/v1/queue-status", nil)
		req.Header.Set("X-Helper-Token", "secret-token")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)

		var resp map[string]QueueStatus
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.Equal(t, 1, resp[roundID].Total)
		assert.Equal(t, 1, resp[roundID].Pending)
	})
}

func TestRoutes_HelperUnavailable(t *testing.T) {
	router := mux.NewRouter()
	RegisterRoutesWithStoreGetter(router, func() *ShareStore { return nil }, log.NewNopLogger())

	t.Run("shares returns 503", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/api/v1/shares", strings.NewReader(validPayloadJSON()))
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusServiceUnavailable, w.Code)
	})

	t.Run("status returns 503", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/v1/status", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusServiceUnavailable, w.Code)
	})
}

func TestRoutes_BecomeReadyAfterStoreSet(t *testing.T) {
	router := mux.NewRouter()
	var store *ShareStore
	RegisterRoutesWithStoreGetter(router, func() *ShareStore { return store }, log.NewNopLogger())

	req1 := httptest.NewRequest("GET", "/api/v1/status", nil)
	w1 := httptest.NewRecorder()
	router.ServeHTTP(w1, req1)
	assert.Equal(t, http.StatusServiceUnavailable, w1.Code)

	store = newTestStore(t)
	req2 := httptest.NewRequest("GET", "/api/v1/status", nil)
	w2 := httptest.NewRecorder()
	router.ServeHTTP(w2, req2)
	assert.Equal(t, http.StatusOK, w2.Code)
}

func TestSubmitShare_DuplicateIsIdempotent(t *testing.T) {
	router, _ := newTestRouter(t)
	body := validPayloadJSON()

	req1 := httptest.NewRequest("POST", "/api/v1/shares", strings.NewReader(body))
	w1 := httptest.NewRecorder()
	router.ServeHTTP(w1, req1)
	require.Equal(t, http.StatusOK, w1.Code)

	req2 := httptest.NewRequest("POST", "/api/v1/shares", strings.NewReader(body))
	w2 := httptest.NewRecorder()
	router.ServeHTTP(w2, req2)
	require.Equal(t, http.StatusOK, w2.Code)

	var resp submitResponse
	require.NoError(t, json.Unmarshal(w2.Body.Bytes(), &resp))
	assert.Equal(t, "duplicate", resp.Status)
}

func TestSubmitShare_ConflictingPayloadReturnsConflict(t *testing.T) {
	router, _ := newTestRouter(t)

	first := testPayload("aabbccddaabbccddaabbccddaabbccddaabbccddaabbccddaabbccddaabbccdd", 0)
	b1, _ := json.Marshal(first)
	req1 := httptest.NewRequest("POST", "/api/v1/shares", strings.NewReader(string(b1)))
	w1 := httptest.NewRecorder()
	router.ServeHTTP(w1, req1)
	require.Equal(t, http.StatusOK, w1.Code)

	conflicting := first
	conflicting.SharesHash = "AQAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
	b2, _ := json.Marshal(conflicting)
	req2 := httptest.NewRequest("POST", "/api/v1/shares", strings.NewReader(string(b2)))
	w2 := httptest.NewRecorder()
	router.ServeHTTP(w2, req2)
	require.Equal(t, http.StatusConflict, w2.Code)
}

func TestSubmitShare_APITokenAuth(t *testing.T) {
	store := newTestStore(t)
	router := mux.NewRouter()
	RegisterRoutesWithGetters(
		router,
		func() *ShareStore { return store },
		func() string { return "secret-token" },
		func() bool { return false },
		nil,
		nil,
		log.NewNopLogger(),
	)

	t.Run("missing token rejected", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/api/v1/shares", strings.NewReader(validPayloadJSON()))
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusUnauthorized, w.Code)
	})

	t.Run("wrong token rejected", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/api/v1/shares", strings.NewReader(validPayloadJSON()))
		req.Header.Set("X-Helper-Token", "wrong")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusUnauthorized, w.Code)
	})

	t.Run("valid token accepted", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/api/v1/shares", strings.NewReader(validPayloadJSON()))
		req.Header.Set("X-Helper-Token", "secret-token")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
	})
}

// vcTestRouter creates a router with VC cross-check enabled.
// The mock VCHash returns a fixed 32-byte commitment; the mock TreeReader
// returns that same commitment at position 0 so the check passes.
func vcTestRouter(t *testing.T) (*mux.Router, *ShareStore, *vcMockTree) {
	t.Helper()
	fetcher := func(roundID string) (uint64, error) {
		return uint64(time.Now().Add(time.Hour).Unix()), nil
	}
	store, err := NewShareStore(":memory:", 0, fetcher)
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	tree := &vcMockTree{leaves: make(map[uint64][]byte)}
	var commitment [32]byte
	commitment[0] = 0xAB
	tree.leaves[0] = commitment[:]

	vcHash := func(roundID, sharesHash [32]byte, proposalID, voteDecision uint32) ([32]byte, error) {
		return commitment, nil
	}

	router := mux.NewRouter()
	RegisterRoutesWithGetters(
		router,
		func() *ShareStore { return store },
		func() string { return "" },
		func() bool { return false },
		func() TreeReader { return tree },
		func() VCHashFunc { return vcHash },
		log.NewNopLogger(),
	)
	return router, store, tree
}

// vcMockTree implements TreeReader for VC cross-check tests.
type vcMockTree struct {
	leaves map[uint64][]byte
}

func (m *vcMockTree) GetTreeStatus() (TreeStatus, error) {
	return TreeStatus{LeafCount: 1, AnchorHeight: 1}, nil
}

func (m *vcMockTree) MerklePath(_ uint64, _ uint32) ([]byte, error) {
	return make([]byte, 772), nil
}

func (m *vcMockTree) LeafAt(position uint64) ([]byte, error) {
	return m.leaves[position], nil
}

func TestSubmitShare_VCCrossCheck_Match(t *testing.T) {
	router, store, _ := vcTestRouter(t)

	req := httptest.NewRequest("POST", "/api/v1/shares", strings.NewReader(validPayloadJSON()))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp submitResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "queued", resp.Status)

	roundID := "aabbccddaabbccddaabbccddaabbccddaabbccddaabbccddaabbccddaabbccdd"
	status := store.Status()
	assert.Equal(t, 1, status[roundID].Total)
}

func TestSubmitShare_VCCrossCheck_Mismatch(t *testing.T) {
	router, _, tree := vcTestRouter(t)

	// Change the on-chain leaf to something different from what vcHash returns.
	badLeaf := make([]byte, 32)
	badLeaf[0] = 0xFF
	tree.leaves[0] = badLeaf

	req := httptest.NewRequest("POST", "/api/v1/shares", strings.NewReader(validPayloadJSON()))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "invalid vote commitment")
}

func TestSubmitShare_VCCrossCheck_NoLeaf(t *testing.T) {
	router, _, tree := vcTestRouter(t)

	// Remove the leaf at position 0.
	delete(tree.leaves, 0)

	req := httptest.NewRequest("POST", "/api/v1/shares", strings.NewReader(validPayloadJSON()))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "no leaf at position")
}

func TestSubmitShare_UnknownRound(t *testing.T) {
	// Build a store whose round fetcher rejects unknown rounds.
	fetcher := func(roundID string) (uint64, error) {
		return 0, fmt.Errorf("%w: %s", ErrUnknownRound, roundID)
	}
	store, err := NewShareStore(":memory:", 0, fetcher)
	require.NoError(t, err)
	defer store.Close()

	router := mux.NewRouter()
	RegisterRoutes(router, store, log.NewNopLogger())

	// The VC check is not wired (nil getVCHash), so we get past that.
	// The round rejection happens in Enqueue → getVoteEndTime.
	req := httptest.NewRequest("POST", "/api/v1/shares", strings.NewReader(validPayloadJSON()))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "unknown voting round")
}

func TestSubmitShare_VCCrossCheck_GracefulDegradation(t *testing.T) {
	// When VCHash is nil (not configured), shares should still be accepted.
	store := newTestStore(t)
	router := mux.NewRouter()
	RegisterRoutesWithGetters(
		router,
		func() *ShareStore { return store },
		func() string { return "" },
		func() bool { return false },
		nil,
		nil,
		log.NewNopLogger(),
	)

	req := httptest.NewRequest("POST", "/api/v1/shares", strings.NewReader(validPayloadJSON()))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
}

func TestEnqueue_UnknownRoundRejected(t *testing.T) {
	fetcher := func(roundID string) (uint64, error) {
		return 0, fmt.Errorf("%w: %s", ErrUnknownRound, roundID)
	}
	s, err := NewShareStore(":memory:", 0, fetcher)
	require.NoError(t, err)
	defer s.Close()

	p := testPayload("aabbccddaabbccddaabbccddaabbccddaabbccddaabbccddaabbccddaabbccdd", 0)
	_, err = s.Enqueue(p)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUnknownRound)
}

func TestVerifyCommitment_HashMismatch(t *testing.T) {
	tree := &vcMockTree{leaves: make(map[uint64][]byte)}
	wrongLeaf := make([]byte, 32)
	wrongLeaf[0] = 0x01
	tree.leaves[0] = wrongLeaf

	var fixedCommitment [32]byte
	fixedCommitment[0] = 0x99
	vcHash := func(roundID, sharesHash [32]byte, proposalID, voteDecision uint32) ([32]byte, error) {
		return fixedCommitment, nil
	}

	handler := &apiHandler{
		getTree:   func() TreeReader { return tree },
		getVCHash: func() VCHashFunc { return vcHash },
		logger:    log.NewNopLogger(),
	}

	roundHex := strings.Repeat("aa", 32)
	sharesB64 := base64.StdEncoding.EncodeToString(make([]byte, 32))
	p := &SharePayload{
		VoteRoundID:  roundHex,
		SharesHash:   sharesB64,
		ProposalID:   1,
		VoteDecision: 0,
		TreePosition: 0,
	}
	err := handler.verifyCommitment(p)
	assert.ErrorIs(t, err, ErrInvalidCommitment)
	assert.Contains(t, err.Error(), "hash mismatch")
}

func TestVerifyCommitment_Match(t *testing.T) {
	var commitment [32]byte
	commitment[0] = 0xBB
	tree := &vcMockTree{leaves: map[uint64][]byte{0: commitment[:]}}
	vcHash := func(roundID, sharesHash [32]byte, proposalID, voteDecision uint32) ([32]byte, error) {
		return commitment, nil
	}

	handler := &apiHandler{
		getTree:   func() TreeReader { return tree },
		getVCHash: func() VCHashFunc { return vcHash },
		logger:    log.NewNopLogger(),
	}

	roundHex := strings.Repeat("aa", 32)
	sharesB64 := base64.StdEncoding.EncodeToString(make([]byte, 32))
	p := &SharePayload{
		VoteRoundID:  roundHex,
		SharesHash:   sharesB64,
		ProposalID:   1,
		VoteDecision: 0,
		TreePosition: 0,
	}
	err := handler.verifyCommitment(p)
	assert.NoError(t, err)
}

// Verify that varying tree_position bypasses PK dedup but is caught by VC check.
func TestSubmitShare_VCCrossCheck_BlocksPositionVariation(t *testing.T) {
	router, _, _ := vcTestRouter(t)

	// First request at position 0 succeeds (leaf matches).
	req1 := httptest.NewRequest("POST", "/api/v1/shares", strings.NewReader(validPayloadJSON()))
	w1 := httptest.NewRecorder()
	router.ServeHTTP(w1, req1)
	assert.Equal(t, http.StatusOK, w1.Code)

	// Attacker tries same payload but with tree_position=999 — no leaf there.
	p := testPayload("aabbccddaabbccddaabbccddaabbccddaabbccddaabbccddaabbccddaabbccdd", 0)
	p.TreePosition = 999
	body, _ := json.Marshal(p)

	req2 := httptest.NewRequest("POST", "/api/v1/shares", strings.NewReader(string(body)))
	w2 := httptest.NewRecorder()
	router.ServeHTTP(w2, req2)
	assert.Equal(t, http.StatusBadRequest, w2.Code)
	assert.Contains(t, w2.Body.String(), "no leaf at position")
}

