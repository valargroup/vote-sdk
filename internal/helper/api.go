package helper

import (
	"bytes"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"cosmossdk.io/log"
	"github.com/cosmos/cosmos-sdk/telemetry"
	sentryhttp "github.com/getsentry/sentry-go/http"
	"github.com/gorilla/mux"
	"github.com/mikelodder7/curvey/native/pasta/fp"

	"github.com/valargroup/vote-sdk/crypto/elgamal"
	"github.com/valargroup/vote-sdk/x/vote/types"
)

// RegisterRoutes registers helper routes without production validation hooks.
// It is retained for package tests; the application uses
// RegisterRoutesWithValidationGetters.
func RegisterRoutes(router *mux.Router, store *ShareStore, logger log.Logger) {
	RegisterRoutesWithGetters(
		router,
		func() *ShareStore { return store },
		func() string { return "" },
		func() bool { return false },
		func() bool { return true },
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
	RegisterRoutesWithGetters(router, getStore, func() string { return "" }, func() bool { return false }, func() bool { return true }, nil, nil, nil, logger)
}

// ErrInvalidCommitment is returned when the share's recomputed vote commitment
// hash does not match the on-chain leaf at the claimed tree position.
var ErrInvalidCommitment = errors.New("invalid vote commitment")

// ErrInvalidSharePayload is returned when individually valid share fields do
// not form the commitments claimed by the submitted payload.
var ErrInvalidSharePayload = errors.New("invalid share payload")

// ErrInvalidRoundChoice is returned when a proposal or vote decision is not
// part of the submitted share's authenticated voting round.
var ErrInvalidRoundChoice = errors.New("invalid voting round choice")

// ErrShareValidationUnavailable means a configured ingress validator cannot
// currently run. Production registration treats this as service unavailability
// rather than accepting an unchecked share.
var ErrShareValidationUnavailable = errors.New("share validation unavailable")

// RegisterRoutesWithGetters is the compatibility registration for routes that
// do not need the production round and payload validators.
func RegisterRoutesWithGetters(
	router *mux.Router,
	getStore func() *ShareStore,
	getAPIToken func() string,
	getExposeQueueStatus func() bool,
	getIngressAllowed func() bool,
	getTree func() TreeReader,
	getVCHash func() VCHashFunc,
	getShareNullifier ShareNullifierCheckerGetter,
	logger log.Logger,
) {
	RegisterRoutesWithQueueSummaryGetters(
		router,
		getStore,
		getAPIToken,
		getExposeQueueStatus,
		func() bool { return true },
		getIngressAllowed,
		getTree,
		getVCHash,
		getShareNullifier,
		logger,
	)
}

// RegisterRoutesWithQueueSummaryGetters registers helper routes including the
// public queue summary controls.
func RegisterRoutesWithQueueSummaryGetters(
	router *mux.Router,
	getStore func() *ShareStore,
	getAPIToken func() string,
	getExposeQueueStatus func() bool,
	getExposeQueueSummary func() bool,
	getIngressAllowed func() bool,
	getTree func() TreeReader,
	getVCHash func() VCHashFunc,
	getShareNullifier ShareNullifierCheckerGetter,
	logger log.Logger,
) {
	RegisterRoutesWithValidationGetters(
		router,
		getStore,
		getAPIToken,
		getExposeQueueStatus,
		getExposeQueueSummary,
		getIngressAllowed,
		getTree,
		getVCHash,
		getShareNullifier,
		nil,
		nil,
		nil,
		logger,
	)
}

// RegisterRoutesWithValidationGetters registers helper routes with production
// share consistency, exact round choice, commitment, and round status checks.
// A configured getter that returns nil makes share submission unavailable.
func RegisterRoutesWithValidationGetters(
	router *mux.Router,
	getStore func() *ShareStore,
	getAPIToken func() string,
	getExposeQueueStatus func() bool,
	getExposeQueueSummary func() bool,
	getIngressAllowed func() bool,
	getTree func() TreeReader,
	getVCHash func() VCHashFunc,
	getShareNullifier ShareNullifierCheckerGetter,
	getRoundStatus func() RoundStatusChecker,
	getPayloadValidator func() SharePayloadValidator,
	getChoiceValidator func() ShareChoiceValidator,
	logger log.Logger,
) {
	h := &apiHandler{
		getStore:              getStore,
		getAPIToken:           getAPIToken,
		getExposeQueueStatus:  getExposeQueueStatus,
		getExposeQueueSummary: getExposeQueueSummary,
		getIngressAllowed:     getIngressAllowed,
		getTree:               getTree,
		getVCHash:             getVCHash,
		getShareNullifier:     getShareNullifier,
		getRoundStatus:        getRoundStatus,
		getPayloadValidator:   getPayloadValidator,
		getChoiceValidator:    getChoiceValidator,
		logger:                logger,
	}
	recover := sentryhttp.New(sentryhttp.Options{Repanic: false}).Handle
	router.Handle("/shielded-vote/v1/shares", recover(http.HandlerFunc(h.handleSubmitShare))).Methods("POST")
	router.Handle("/shielded-vote/v1/share-status/{roundId}/{nullifier}", recover(http.HandlerFunc(h.handleShareStatus))).Methods("GET")
	router.Handle("/shielded-vote/v1/status", recover(http.HandlerFunc(h.handleStatus))).Methods("GET")
	router.Handle("/shielded-vote/v1/queue-summary/{roundId}", recover(http.HandlerFunc(h.handleQueueSummary))).Methods("GET")
	router.Handle("/shielded-vote/v1/queue-status", recover(http.HandlerFunc(h.handleQueueStatus))).Methods("GET")
}

// ShareNullifierCheckerGetter resolves the checker at request time (nil when helper disabled).
type ShareNullifierCheckerGetter func() ShareNullifierChecker

type apiHandler struct {
	getStore              func() *ShareStore
	getAPIToken           func() string
	getExposeQueueStatus  func() bool
	getExposeQueueSummary func() bool
	getIngressAllowed     func() bool
	getTree               func() TreeReader
	getVCHash             func() VCHashFunc
	getShareNullifier     ShareNullifierCheckerGetter
	getRoundStatus        func() RoundStatusChecker
	getPayloadValidator   func() SharePayloadValidator
	getChoiceValidator    func() ShareChoiceValidator
	logger                log.Logger
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
	if !h.ensureIngressAllowed(w) {
		recordShareSubmissionOutcome("unavailable", "ingress_disabled")
		return
	}
	store := h.getStore()
	if store == nil {
		recordShareSubmissionOutcome("unavailable", "store")
		jsonError(w, "helper unavailable", http.StatusServiceUnavailable)
		return
	}
	if !h.authorizeSubmit(r) {
		recordShareSubmissionOutcome("rejected", "unauthorized")
		jsonError(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	// Limit request body to 1MB to prevent memory exhaustion.
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)

	var payload SharePayload
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(&payload); err != nil {
		recordShareSubmissionOutcome("rejected", "invalid_json")
		jsonError(w, fmt.Sprintf("invalid JSON: %v", err), http.StatusBadRequest)
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		recordShareSubmissionOutcome("rejected", "invalid_json")
		jsonError(w, "invalid JSON: expected one object", http.StatusBadRequest)
		return
	}

	if err := validatePayload(&payload); err != nil {
		recordShareSubmissionOutcome("rejected", "invalid_fields")
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}

	if err := h.validatePayloadConsistency(&payload); err != nil {
		if errors.Is(err, ErrInvalidSharePayload) {
			recordShareSubmissionOutcome("rejected", "inconsistent_payload")
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		if errors.Is(err, ErrShareValidationUnavailable) {
			h.logger.Error("share payload validator unavailable", "error", err)
			CaptureErr(err, map[string]string{
				"stage": "payload_consistency_check",
			})
			recordShareSubmissionOutcome("unavailable", "payload_validator")
			jsonError(w, "helper unavailable", http.StatusServiceUnavailable)
			return
		}
		h.logger.Error("share payload validation failed", "error", err)
		CaptureErr(err, map[string]string{
			"stage": "payload_consistency_check",
		})
		recordShareSubmissionOutcome("failed", "payload_validator")
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}

	if h.getRoundStatus != nil {
		checker := h.getRoundStatus()
		if checker == nil {
			err := fmt.Errorf("%w: round status checker", ErrShareValidationUnavailable)
			h.logger.Error("round status checker unavailable", "error", err)
			CaptureErr(err, map[string]string{
				"stage": "round_status_check_ingress",
			})
			recordShareSubmissionOutcome("unavailable", "round_status_checker")
			jsonError(w, "helper unavailable", http.StatusServiceUnavailable)
			return
		}
		active, err := checker(payload.VoteRoundID)
		if err != nil {
			if errors.Is(err, ErrUnknownRound) || errors.Is(err, types.ErrRoundNotFound) {
				recordShareSubmissionOutcome("rejected", "unknown_round")
				jsonError(w, ErrUnknownRound.Error(), http.StatusBadRequest)
				return
			}
			if errors.Is(err, ErrCheckTxNotReady) {
				recordShareSubmissionOutcome("unavailable", "check_tx_not_ready")
				jsonError(w, "helper unavailable", http.StatusServiceUnavailable)
				return
			}
			h.logger.Error("round status check failed", "error", err)
			CaptureErr(err, map[string]string{
				"stage": "round_status_check_ingress",
			})
			recordShareSubmissionOutcome("failed", "round_status_check")
			jsonError(w, "internal error", http.StatusInternalServerError)
			return
		}
		if !active {
			recordShareSubmissionOutcome("rejected", "round_inactive")
			jsonError(w, "voting round is not active", http.StatusConflict)
			return
		}
	}

	if err := h.validateShareChoice(&payload); err != nil {
		if errors.Is(err, ErrInvalidRoundChoice) || errors.Is(err, ErrUnknownRound) || errors.Is(err, types.ErrRoundNotFound) {
			recordShareSubmissionOutcome("rejected", "invalid_round_choice")
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		if errors.Is(err, ErrShareValidationUnavailable) {
			h.logger.Error("share choice validator unavailable", "error", err)
			CaptureErr(err, map[string]string{
				"stage": "round_choice_check_ingress",
			})
			recordShareSubmissionOutcome("unavailable", "choice_validator")
			jsonError(w, "helper unavailable", http.StatusServiceUnavailable)
			return
		}
		h.logger.Error("share choice validation failed", "error", err)
		CaptureErr(err, map[string]string{
			"stage": "round_choice_check_ingress",
		})
		recordShareSubmissionOutcome("failed", "choice_validator")
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Vote commitment cross-check: recompute the Poseidon VC hash from the
	// payload and compare against the on-chain leaf at tree_position. This
	// rejects fabricated shares before they enter the queue (microsecond cost).
	if err := h.verifyCommitment(&payload); err != nil {
		if errors.Is(err, ErrInvalidCommitment) {
			recordShareSubmissionOutcome("rejected", "invalid_commitment")
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		if errors.Is(err, ErrShareValidationUnavailable) {
			h.logger.Error("vote commitment validator unavailable", "error", err)
			CaptureErr(err, map[string]string{
				"stage": "commitment_check",
			})
			recordShareSubmissionOutcome("unavailable", "commitment_validator")
			jsonError(w, "helper unavailable", http.StatusServiceUnavailable)
			return
		}
		h.logger.Error("vote commitment verification failed", "error", err)
		CaptureErr(err, map[string]string{
			"stage": "commitment_check",
		})
		recordShareSubmissionOutcome("failed", "commitment_check")
		jsonError(w, "internal error", http.StatusInternalServerError)
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
			recordShareSubmissionOutcome("rejected", "unknown_round")
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		if errors.Is(err, ErrInvalidSubmitAt) {
			recordShareSubmissionOutcome("rejected", "invalid_schedule")
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		h.logger.Error("failed to enqueue share", "error", err)
		CaptureErr(err, map[string]string{
			"round_id": payload.VoteRoundID,
			"stage":    "enqueue",
		})
		recordShareSubmissionOutcome("failed", "enqueue")
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}
	if result == EnqueueConflict {
		recordShareSubmissionOutcome("rejected", "conflict")
		jsonError(w, "conflicting share payload for round_id/share_index", http.StatusConflict)
		return
	}
	if result == EnqueueInserted {
		_, span := StartTrace(r.Context(), "helper.enqueue", "helper.share_enqueued", map[string]string{
			"round_id": payload.VoteRoundID,
		}, map[string]interface{}{
			"share_index":   payload.EncShare.ShareIndex,
			"proposal_id":   payload.ProposalID,
			"tree_position": payload.TreePosition,
			"submit_at":     payload.SubmitAt,
		})
		span.Finish(nil)
	}

	w.Header().Set("Content-Type", "application/json")
	status := "queued"
	if result == EnqueueDuplicate {
		status = "duplicate"
		recordShareSubmissionOutcome("duplicate", "exact")
	} else {
		recordShareSubmissionOutcome("accepted", "queued")
	}
	json.NewEncoder(w).Encode(submitResponse{Status: status})
}

func (h *apiHandler) handleShareStatus(w http.ResponseWriter, r *http.Request) {
	if !h.ensureIngressAllowed(w) {
		return
	}
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
	if !h.ensureIngressAllowed(w) {
		return
	}
	store := h.getStore()
	if store == nil {
		jsonError(w, "helper unavailable", http.StatusServiceUnavailable)
		return
	}

	resp := statusResponse{
		Status: "ok",
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (h *apiHandler) handleQueueStatus(w http.ResponseWriter, r *http.Request) {
	if !h.ensureIngressAllowed(w) {
		return
	}
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

// handleQueueSummary writes the public coarse queue histogram for a single
// round when the helper is configured to expose it.
func (h *apiHandler) handleQueueSummary(w http.ResponseWriter, r *http.Request) {
	if h.getExposeQueueSummary == nil || !h.getExposeQueueSummary() {
		jsonError(w, "not found", http.StatusNotFound)
		return
	}
	store := h.getStore()
	if store == nil {
		jsonError(w, "helper unavailable", http.StatusServiceUnavailable)
		return
	}

	roundID := strings.ToLower(strings.TrimSpace(mux.Vars(r)["roundId"]))
	const roundIDHexLen = 64
	if len(roundID) != roundIDHexLen {
		jsonError(w, "roundId must be 64 hex characters", http.StatusBadRequest)
		return
	}
	if _, err := hex.DecodeString(roundID); err != nil {
		jsonError(w, "invalid roundId hex", http.StatusBadRequest)
		return
	}

	summary, err := store.QueueSummary(roundID, time.Now())
	if err != nil {
		if errors.Is(err, ErrUnknownRound) {
			jsonError(w, "round not found", http.StatusNotFound)
			return
		}
		if errors.Is(err, ErrInvalidRoundInfo) {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		h.logger.Error("queue summary failed", "round_id", roundID, "error", err)
		CaptureErr(err, map[string]string{
			"round_id": roundID,
			"stage":    "queue_summary",
		})
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(summary)
}

func (h *apiHandler) authorizeSubmit(r *http.Request) bool {
	token := h.getAPIToken()
	if token == "" {
		return true
	}
	provided := r.Header.Get("X-Helper-Token")
	return subtle.ConstantTimeCompare([]byte(provided), []byte(token)) == 1
}

func (h *apiHandler) ensureIngressAllowed(w http.ResponseWriter) bool {
	if h.getIngressAllowed == nil || h.getIngressAllowed() {
		return true
	}
	jsonError(w, "helper ingress disabled: local node is catching up or stale", http.StatusServiceUnavailable)
	return false
}

// verifyCommitment recomputes the vote commitment Poseidon hash from the
// payload fields and compares it against the on-chain leaf at tree_position.
// Legacy route registrations that provide neither dependency skip this check.
// A configured getter returning nil is treated as service unavailability.
func (h *apiHandler) verifyCommitment(p *SharePayload) error {
	if h.getVCHash == nil && h.getTree == nil {
		return nil
	}
	if h.getVCHash == nil || h.getTree == nil {
		return fmt.Errorf("%w: incomplete commitment validator", ErrShareValidationUnavailable)
	}
	vcHash := h.getVCHash()
	if vcHash == nil {
		return fmt.Errorf("%w: vote commitment hash", ErrShareValidationUnavailable)
	}
	tree := h.getTree()
	if tree == nil {
		return fmt.Errorf("%w: commitment tree", ErrShareValidationUnavailable)
	}

	var roundID [32]byte
	roundBytes, err := hex.DecodeString(p.VoteRoundID)
	if err != nil {
		return fmt.Errorf("vote_round_id: %w", err)
	}
	copy(roundID[:], roundBytes)

	tree = tree.ForRound(roundBytes)

	var sharesHash [32]byte
	shBytes, err := base64.StdEncoding.DecodeString(p.SharesHash)
	if err != nil {
		return fmt.Errorf("shares_hash: %w", err)
	}
	copy(sharesHash[:], shBytes)

	computed, err := vcHash(roundID, sharesHash, p.ProposalID, p.VoteDecision)
	if err != nil {
		return fmt.Errorf("compute vote commitment: %w", err)
	}

	onChain, err := tree.LeafAt(p.TreePosition)
	if err != nil {
		return fmt.Errorf("read commitment tree leaf: %w", err)
	}
	if onChain == nil {
		return fmt.Errorf("%w: no leaf at position %d", ErrInvalidCommitment, p.TreePosition)
	}

	if !bytes.Equal(computed[:], onChain) {
		return fmt.Errorf("%w: hash mismatch at position %d", ErrInvalidCommitment, p.TreePosition)
	}
	return nil
}

func (h *apiHandler) validatePayloadConsistency(p *SharePayload) error {
	if h.getPayloadValidator == nil {
		return nil
	}
	validator := h.getPayloadValidator()
	if validator == nil {
		return fmt.Errorf("%w: share payload validator", ErrShareValidationUnavailable)
	}
	return checkPayloadConsistency(p, validator)
}

// checkPayloadConsistency validates the commitment relationships in a
// structurally decoded payload. A false validator result is caller-owned;
// validator execution failures remain helper-owned.
func checkPayloadConsistency(p *SharePayload, validator SharePayloadValidator) error {
	if validator == nil {
		return fmt.Errorf("%w: share payload validator", ErrShareValidationUnavailable)
	}
	sharesHash, err := decodeBase64Array32(p.SharesHash, "shares_hash")
	if err != nil {
		return fmt.Errorf("decode validated shares_hash: %w", err)
	}
	primaryBlind, err := decodeBase64Array32(p.PrimaryBlind, "primary_blind")
	if err != nil {
		return fmt.Errorf("decode validated primary_blind: %w", err)
	}
	encC1, err := decodeBase64Array32(p.EncShare.C1, "enc_share.c1")
	if err != nil {
		return fmt.Errorf("decode validated enc_share.c1: %w", err)
	}
	encC2, err := decodeBase64Array32(p.EncShare.C2, "enc_share.c2")
	if err != nil {
		return fmt.Errorf("decode validated enc_share.c2: %w", err)
	}
	var shareComms [16][32]byte
	for i, encoded := range p.ShareComms {
		shareComms[i], err = decodeBase64Array32(encoded, fmt.Sprintf("share_comms[%d]", i))
		if err != nil {
			return fmt.Errorf("decode validated share_comms[%d]: %w", i, err)
		}
	}

	valid, err := validator(sharesHash, shareComms, primaryBlind, encC1, encC2, p.EncShare.ShareIndex)
	if err != nil {
		return fmt.Errorf("validate share payload consistency: %w", err)
	}
	if !valid {
		return ErrInvalidSharePayload
	}
	return nil
}

func (h *apiHandler) validateShareChoice(p *SharePayload) error {
	if h.getChoiceValidator == nil {
		return nil
	}
	validator := h.getChoiceValidator()
	if validator == nil {
		return fmt.Errorf("%w: share choice validator", ErrShareValidationUnavailable)
	}
	return validator(p.VoteRoundID, p.ProposalID, p.VoteDecision)
}

// validatePayload checks required fields and canonicalizes vote_round_id.
func validatePayload(p *SharePayload) error {
	if err := validateB64Field(p.SharesHash, 32, "shares_hash"); err != nil {
		return err
	}
	if err := validateCanonicalB64Field(p.SharesHash, "shares_hash"); err != nil {
		return err
	}
	if err := validateB64Field(p.EncShare.C1, 32, "enc_share.c1"); err != nil {
		return err
	}
	if err := validateB64Field(p.EncShare.C2, 32, "enc_share.c2"); err != nil {
		return err
	}
	c1, _ := base64.StdEncoding.DecodeString(p.EncShare.C1)
	c2, _ := base64.StdEncoding.DecodeString(p.EncShare.C2)
	ciphertext, err := elgamal.UnmarshalCiphertext(append(append(make([]byte, 0, 64), c1...), c2...))
	if err != nil {
		return fmt.Errorf("enc_share: invalid Pallas point encoding")
	}
	if ciphertext.C1.IsIdentity() || ciphertext.C2.IsIdentity() {
		return fmt.Errorf("enc_share: identity points are not valid reveal inputs")
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
	var roundID [32]byte
	copy(roundID[:], roundBytes)
	if _, err := fp.PastaFpNew().SetBytes(&roundID); err != nil {
		return fmt.Errorf("vote_round_id: non-canonical Pallas field element")
	}
	p.VoteRoundID = hex.EncodeToString(roundBytes)

	// share_comms: exactly 16 entries, each base64-decodable to 32 bytes.
	if len(p.ShareComms) != 16 {
		return fmt.Errorf("share_comms: expected 16 entries, got %d", len(p.ShareComms))
	}
	for i, c := range p.ShareComms {
		if err := validateB64Field(c, 32, fmt.Sprintf("share_comms[%d]", i)); err != nil {
			return err
		}
		if err := validateCanonicalB64Field(c, fmt.Sprintf("share_comms[%d]", i)); err != nil {
			return err
		}
	}

	// primary_blind: base64-decodable to 32 bytes.
	if err := validateB64Field(p.PrimaryBlind, 32, "primary_blind"); err != nil {
		return err
	}
	if err := validateCanonicalB64Field(p.PrimaryBlind, "primary_blind"); err != nil {
		return err
	}

	return nil
}

func validateCanonicalB64Field(value, fieldName string) error {
	decoded, err := decodeBase64Array32(value, fieldName)
	if err != nil {
		return err
	}
	if _, err := fp.PastaFpNew().SetBytes(&decoded); err != nil {
		return fmt.Errorf("%s: non-canonical Pallas field element", fieldName)
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

// recordShareSubmissionOutcome emits a bounded counter with no request or
// share identifiers.
func recordShareSubmissionOutcome(outcome, reason string) {
	telemetry.IncrCounter(1, "helper", "share_submission", outcome, reason)
}
