package helper

import (
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"

	"cosmossdk.io/log"
	"github.com/gorilla/mux"
)

// RegisterRoutes registers helper server HTTP routes on the given mux router.
func RegisterRoutes(router *mux.Router, store *ShareStore, logger log.Logger) {
	RegisterRoutesWithGetters(
		router,
		func() *ShareStore { return store },
		func() string { return "" },
		nil,
		logger,
	)
}

// RegisterRoutesWithStoreGetter registers helper server HTTP routes on the given
// mux router, resolving the store at request time. This allows routes to be
// mounted before the helper is fully initialized.
func RegisterRoutesWithStoreGetter(router *mux.Router, getStore func() *ShareStore, logger log.Logger) {
	RegisterRoutesWithGetters(router, getStore, func() string { return "" }, nil, logger)
}

// RegisterRoutesWithGetters registers helper routes using runtime getters for
// store, API token, and tree reader.
func RegisterRoutesWithGetters(
	router *mux.Router,
	getStore func() *ShareStore,
	getAPIToken func() string,
	getTree func() TreeReader,
	logger log.Logger,
) {
	h := &apiHandler{getStore: getStore, getAPIToken: getAPIToken, getTree: getTree, logger: logger}
	router.HandleFunc("/api/v1/shares", h.handleSubmitShare).Methods("POST")
	router.HandleFunc("/api/v1/status", h.handleStatus).Methods("GET")
}

type apiHandler struct {
	getStore    func() *ShareStore
	getAPIToken func() string
	getTree     func() TreeReader
	logger      log.Logger
}

type submitResponse struct {
	Status string `json:"status"`
}

func (h *apiHandler) handleSubmitShare(w http.ResponseWriter, r *http.Request) {
	store := h.getStore()
	if store == nil {
		http.Error(w, "helper unavailable", http.StatusServiceUnavailable)
		return
	}
	if !h.authorizeSubmit(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	// Limit request body to 1MB to prevent memory exhaustion.
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)

	var payload SharePayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, fmt.Sprintf("invalid JSON: %v", err), http.StatusBadRequest)
		return
	}

	if err := validatePayload(&payload); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	h.logger.Info("share received",
		"round_id", payload.VoteRoundID,
		"share_index", payload.EncShare.ShareIndex,
		"proposal_id", payload.ProposalID,
		"tree_position", payload.TreePosition,
	)

	result, err := store.Enqueue(payload)
	if err != nil {
		h.logger.Error("failed to enqueue share", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if result == EnqueueConflict {
		http.Error(w, "conflicting share payload for round_id/share_index", http.StatusConflict)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	status := "queued"
	if result == EnqueueDuplicate {
		status = "duplicate"
	}
	json.NewEncoder(w).Encode(submitResponse{Status: status})
}

type statusResponse struct {
	Status string                 `json:"status"`
	Queues map[string]QueueStatus `json:"queues"`
	Tree   *TreeStatus            `json:"tree,omitempty"`
}

func (h *apiHandler) handleStatus(w http.ResponseWriter, r *http.Request) {
	store := h.getStore()
	if store == nil {
		http.Error(w, "helper unavailable", http.StatusServiceUnavailable)
		return
	}

	resp := statusResponse{
		Status: "ok",
		Queues: store.Status(),
	}

	if h.getTree != nil {
		if tree := h.getTree(); tree != nil {
			if ts, err := tree.GetTreeStatus(); err == nil {
				resp.Tree = &ts
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (h *apiHandler) authorizeSubmit(r *http.Request) bool {
	token := h.getAPIToken()
	if token == "" {
		return true
	}
	provided := r.Header.Get("X-Helper-Token")
	return subtle.ConstantTimeCompare([]byte(provided), []byte(token)) == 1
}

// validatePayload checks required fields of a share submission.
func validatePayload(p *SharePayload) error {
	if err := validateB64Field(p.SharesHash, 32, "shares_hash"); err != nil {
		return err
	}
	if err := validateB64Field(p.EncShare.C1, 32, "enc_share.c1"); err != nil {
		return err
	}
	if err := validateB64Field(p.EncShare.C2, 32, "enc_share.c2"); err != nil {
		return err
	}
	if p.EncShare.ShareIndex > 15 {
		return fmt.Errorf("enc_share.share_index must be 0..15")
	}
	if p.ShareIndex != p.EncShare.ShareIndex {
		return fmt.Errorf("share_index must match enc_share.share_index")
	}
	// Protocol allows up to 8 options per proposal (indices 0-7).
	// The chain keeper validates the exact range per-proposal.
	if p.VoteDecision >= 8 {
		return fmt.Errorf("vote_decision must be 0..7")
	}

	// vote_round_id: hex, 32 bytes.
	roundBytes, err := hex.DecodeString(p.VoteRoundID)
	if err != nil {
		return fmt.Errorf("vote_round_id: %v", err)
	}
	if len(roundBytes) != 32 {
		return fmt.Errorf("vote_round_id: expected 32 bytes, got %d", len(roundBytes))
	}

	// all_enc_shares: exactly 16 entries.
	if len(p.AllEncShares) != 16 {
		return fmt.Errorf("all_enc_shares: expected 16 entries, got %d", len(p.AllEncShares))
	}
	for i, es := range p.AllEncShares {
		if err := validateB64Field(es.C1, 32, fmt.Sprintf("all_enc_shares[%d].c1", i)); err != nil {
			return err
		}
		if err := validateB64Field(es.C2, 32, fmt.Sprintf("all_enc_shares[%d].c2", i)); err != nil {
			return err
		}
		if es.ShareIndex != uint32(i) {
			return fmt.Errorf("all_enc_shares[%d].share_index: expected %d, got %d", i, i, es.ShareIndex)
		}
	}

	// enc_share must match all_enc_shares[share_index].
	idx := p.EncShare.ShareIndex
	expected := p.AllEncShares[idx]
	if p.EncShare.C1 != expected.C1 || p.EncShare.C2 != expected.C2 {
		return fmt.Errorf("enc_share c1/c2 must match all_enc_shares[%d]", idx)
	}

	// share_comms: exactly 16 entries, each base64-decodable to 32 bytes.
	if len(p.ShareComms) != 16 {
		return fmt.Errorf("share_comms: expected 16 entries, got %d", len(p.ShareComms))
	}
	for i, c := range p.ShareComms {
		if err := validateB64Field(c, 32, fmt.Sprintf("share_comms[%d]", i)); err != nil {
			return err
		}
	}

	// primary_blind: base64-decodable to 32 bytes.
	if err := validateB64Field(p.PrimaryBlind, 32, "primary_blind"); err != nil {
		return err
	}

	return nil
}

func validateB64Field(value string, expectedLen int, fieldName string) error {
	bytes, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return fmt.Errorf("%s: %v", fieldName, err)
	}
	if len(bytes) != expectedLen {
		return fmt.Errorf("%s: expected %d bytes, got %d", fieldName, expectedLen, len(bytes))
	}
	return nil
}
