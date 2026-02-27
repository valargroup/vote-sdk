package helper

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
	assert.Empty(t, resp.Queues)
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
	assert.Equal(t, 2, resp.Queues[roundID].Total)
	assert.Equal(t, 2, resp.Queues[roundID].Pending)
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
