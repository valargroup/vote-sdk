package helper

import (
	"bytes"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"cosmossdk.io/log"
	sentryhttp "github.com/getsentry/sentry-go/http"
	"github.com/gorilla/mux"

	"github.com/valargroup/vote-sdk/x/vote/types"
)

// RegisterRoutes registers helper server HTTP routes on the given mux router.
func RegisterRoutes(router *mux.Router, store *ShareStore, logger log.Logger) {
	RegisterRoutesWithGetters(
		router,
		func() *ShareStore { return store },
		func() string { return "" },
		func() bool { return false },
		nil,
		nil,
		nil,
		logger,
	)
}

// RegisterRoutesWithStoreGetter registers helper server HTTP routes on the given
// mux router, resolving the store at request time. This allows routes to be
// mounted before the helper is fully initialized.
func RegisterRoutesWithStoreGetter(router *mux.Router, getStore func() *ShareStore, logger log.Logger) {
	RegisterRoutesWithGetters(router, getStore, func() string { return "" }, func() bool { return false }, nil, nil, nil, logger)
}

// ErrInvalidCommitment is returned when the share's recomputed vote commitment
// hash does not match the on-chain leaf at the claimed tree position.
var ErrInvalidCommitment = fmt.Errorf("invalid vote commitment")

// RegisterRoutesWithGetters registers helper routes using runtime getters for
// store, API token, tree reader, VC hash function, and share-nullifier checker.
func RegisterRoutesWithGetters(
	router *mux.Router,
	getStore func() *ShareStore,
	getAPIToken func() string,
	getExposeQueueStatus func() bool,
	getTree func() TreeReader,
	getVCHash func() VCHashFunc,
	getShareNullifier ShareNullifierCheckerGetter,
	logger log.Logger,
) {
	h := &apiHandler{
		getStore:             getStore,
		getAPIToken:          getAPIToken,
		getExposeQueueStatus: getExposeQueueStatus,
		getTree:              getTree,
		getVCHash:            getVCHash,
		getShareNullifier:    getShareNullifier,
		logger:               logger,
	}
	recover := sentryhttp.New(sentryhttp.Options{Repanic: false}).Handle
	router.Handle("/shielded-vote/v1/shares", recover(http.HandlerFunc(h.handleSubmitShare))).Methods("POST")
	router.Handle("/shielded-vote/v1/share-status/{roundId}/{nullifier}", recover(http.HandlerFunc(h.handleShareStatus))).Methods("GET")
	router.Handle("/shielded-vote/v1/status", recover(http.HandlerFunc(h.handleStatus))).Methods("GET")
	router.Handle("/shielded-vote/v1/queue-status", recover(http.HandlerFunc(h.handleQueueStatus))).Methods("GET")
}

// ShareNullifierCheckerGetter resolves the checker at request time (nil when helper disabled).
type ShareNullifierCheckerGetter func() ShareNullifierChecker

type apiHandler struct {
	getStore             func() *ShareStore
	getAPIToken          func() string
	getExposeQueueStatus func() bool
	getTree              func() TreeReader
	getVCHash            func() VCHashFunc
	getShareNullifier    ShareNullifierCheckerGetter
	logger               log.Logger
}

type submitResponse struct {
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

// shareSubmissionStatusResponse is returned by GET /shielded-vote/v1/share-status/{roundId}/{nullifier}.
type shareSubmissionStatusResponse struct {
	Status string `json:"status"`
}

func jsonError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(submitResponse{Status: "error", Error: msg})
}

func (h *apiHandler) handleSubmitShare(w http.ResponseWriter, r *http.Request) {
	store := h.getStore()
	if store == nil {
		jsonError(w, "helper unavailable", http.StatusServiceUnavailable)
		return
	}
	if !h.authorizeSubmit(r) {
		jsonError(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	// Limit request body to 1MB to prevent memory exhaustion.
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)

	var payload SharePayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		jsonError(w, fmt.Sprintf("invalid JSON: %v", err), http.StatusBadRequest)
		return
	}

	if err := validatePayload(&payload); err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Vote commitment cross-check: recompute the Poseidon VC hash from the
	// payload and compare against the on-chain leaf at tree_position. This
	// rejects fabricated shares before they enter the queue (microsecond cost).
	if err := h.verifyCommitment(&payload); err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
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
		if errors.Is(err, ErrUnknownRound) {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		if errors.Is(err, ErrInvalidSubmitAt) {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		h.logger.Error("failed to enqueue share", "error", err)
		CaptureErr(err, map[string]string{
			"round_id": payload.VoteRoundID,
			"stage":    "enqueue",
		})
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}
	if result == EnqueueConflict {
		jsonError(w, "conflicting share payload for round_id/share_index", http.StatusConflict)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	status := "queued"
	if result == EnqueueDuplicate {
		status = "duplicate"
	}
	json.NewEncoder(w).Encode(submitResponse{Status: status})
}

func (h *apiHandler) handleShareStatus(w http.ResponseWriter, r *http.Request) {
	if h.getStore() == nil {
		jsonError(w, "helper unavailable", http.StatusServiceUnavailable)
		return
	}
	if !h.authorizeSubmit(r) {
		jsonError(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	roundID := strings.ToLower(strings.TrimSpace(mux.Vars(r)["roundId"]))
	nullifierHex := strings.ToLower(strings.TrimSpace(mux.Vars(r)["nullifier"]))
	const idHexLen = 64 // 32-byte field elements / nullifiers
	if len(roundID) != idHexLen {
		jsonError(w, "roundId must be 64 hex characters", http.StatusBadRequest)
		return
	}
	if len(nullifierHex) != idHexLen {
		jsonError(w, "nullifier must be 64 hex characters", http.StatusBadRequest)
		return
	}
	if _, err := hex.DecodeString(roundID); err != nil {
		jsonError(w, "invalid roundId hex", http.StatusBadRequest)
		return
	}
	nf, err := hex.DecodeString(nullifierHex)
	if err != nil {
		jsonError(w, "invalid nullifier hex", http.StatusBadRequest)
		return
	}
	if len(nf) != 32 {
		jsonError(w, "nullifier must decode to 32 bytes", http.StatusBadRequest)
		return
	}

	var checker ShareNullifierChecker
	if h.getShareNullifier != nil {
		checker = h.getShareNullifier()
	}
	if checker == nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(shareSubmissionStatusResponse{Status: "pending"})
		return
	}

	onChain, err := checker(roundID, nf)
	if err != nil {
		h.logger.Error("share nullifier check failed", "error", err)
		CaptureErr(err, map[string]string{
			"round_id": roundID,
			"stage":    "nullifier_check",
		})
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}
	out := shareSubmissionStatusResponse{Status: "pending"}
	if onChain {
		out.Status = "confirmed"
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}

type statusResponse struct {
	Status string      `json:"status"`
	Tree   *TreeStatus `json:"tree,omitempty"`
}

func (h *apiHandler) handleStatus(w http.ResponseWriter, r *http.Request) {
	store := h.getStore()
	if store == nil {
		jsonError(w, "helper unavailable", http.StatusServiceUnavailable)
		return
	}

	resp := statusResponse{
		Status: "ok",
	}

	if h.getTree != nil {
		if tree := h.getTree(); tree != nil {
			if ts, err := tree.GetTreeStatus(); err == nil {
				resp.Tree = &ts
			} else {
				h.logger.Error("tree status read failed", "error", err)
				CaptureErr(err, map[string]string{"stage": "tree_status"})
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (h *apiHandler) handleQueueStatus(w http.ResponseWriter, r *http.Request) {
	if h.getExposeQueueStatus == nil || !h.getExposeQueueStatus() {
		jsonError(w, "not found", http.StatusNotFound)
		return
	}
	store := h.getStore()
	if store == nil {
		jsonError(w, "helper unavailable", http.StatusServiceUnavailable)
		return
	}
	if !h.authorizeSubmit(r) {
		jsonError(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(store.Status())
}

func (h *apiHandler) authorizeSubmit(r *http.Request) bool {
	token := h.getAPIToken()
	if token == "" {
		return true
	}
	provided := r.Header.Get("X-Helper-Token")
	return subtle.ConstantTimeCompare([]byte(provided), []byte(token)) == 1
}

// verifyCommitment recomputes the vote commitment Poseidon hash from the
// payload fields and compares it against the on-chain leaf at tree_position.
// Returns nil when the VC hash function or tree reader is unavailable
// (graceful degradation during startup).
func (h *apiHandler) verifyCommitment(p *SharePayload) error {
	var vcHash VCHashFunc
	if h.getVCHash != nil {
		vcHash = h.getVCHash()
	}
	if vcHash == nil {
		return nil
	}
	var tree TreeReader
	if h.getTree != nil {
		tree = h.getTree()
	}
	if tree == nil {
		return nil
	}

	var roundID [32]byte
	roundBytes, err := hex.DecodeString(p.VoteRoundID)
	if err != nil {
		return fmt.Errorf("vote_round_id: %w", err)
	}
	copy(roundID[:], roundBytes)

	tree.SetRoundID(roundBytes)

	var sharesHash [32]byte
	shBytes, err := base64.StdEncoding.DecodeString(p.SharesHash)
	if err != nil {
		return fmt.Errorf("shares_hash: %w", err)
	}
	copy(sharesHash[:], shBytes)

	computed, err := vcHash(roundID, sharesHash, p.ProposalID, p.VoteDecision)
	if err != nil {
		h.logger.Error("vc hash computation failed", "error", err)
		CaptureErr(err, map[string]string{
			"round_id":      p.VoteRoundID,
			"proposal_id":   fmt.Sprintf("%d", p.ProposalID),
			"tree_position": fmt.Sprintf("%d", p.TreePosition),
			"stage":         "vc_hash",
		})
		return ErrInvalidCommitment
	}

	onChain, err := tree.LeafAt(p.TreePosition)
	if err != nil {
		h.logger.Error("leaf read failed", "tree_position", p.TreePosition, "error", err)
		CaptureErr(err, map[string]string{
			"round_id":      p.VoteRoundID,
			"proposal_id":   fmt.Sprintf("%d", p.ProposalID),
			"tree_position": fmt.Sprintf("%d", p.TreePosition),
			"stage":         "leaf_read",
		})
		return fmt.Errorf("%w: tree read error", ErrInvalidCommitment)
	}
	if onChain == nil {
		return fmt.Errorf("%w: no leaf at position %d", ErrInvalidCommitment, p.TreePosition)
	}

	if !bytes.Equal(computed[:], onChain) {
		return fmt.Errorf("%w: hash mismatch at position %d", ErrInvalidCommitment, p.TreePosition)
	}
	return nil
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
	if p.EncShare.ShareIndex > types.MaxProposals {
		return fmt.Errorf("enc_share.share_index must be 0..%d", types.MaxProposals)
	}
	// Protocol allows up to 8 options per proposal (indices 0-7).
	// The chain keeper validates the exact range per-proposal.
	if p.VoteDecision >= types.MaxVoteOptions {
		return fmt.Errorf("vote_decision must be 0..%d", types.MaxVoteOptions-1)
	}
	if p.ProposalID < types.MinProposalID || p.ProposalID > types.MaxProposals {
		return fmt.Errorf("proposal_id must be %d..%d, got %d", types.MinProposalID, types.MaxProposals, p.ProposalID)
	}
	if p.TreePosition > types.MaxTreePosition {
		return fmt.Errorf("tree_position %d exceeds maximum tree capacity", p.TreePosition)
	}

	// vote_round_id: hex, 32 bytes.
	roundBytes, err := hex.DecodeString(p.VoteRoundID)
	if err != nil {
		return fmt.Errorf("vote_round_id: %v", err)
	}
	if len(roundBytes) != 32 {
		return fmt.Errorf("vote_round_id: expected 32 bytes, got %d", len(roundBytes))
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
