package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/mux"
	protov2 "google.golang.org/protobuf/proto"

	"github.com/z-cale/zally/x/vote/types"
)

// HandlerConfig configures the REST API handler.
type HandlerConfig struct {
	// CometRPCEndpoint is the URL of the local CometBFT RPC server.
	// Default: "http://localhost:26657"
	CometRPCEndpoint string

}

// Handler provides JSON REST endpoints for vote transaction submission
// and query access.
type Handler struct {
	cometRPC string
	client   *http.Client
}

// NewHandler creates a new REST API handler.
func NewHandler(cfg HandlerConfig) *Handler {
	endpoint := cfg.CometRPCEndpoint
	if endpoint == "" {
		endpoint = "http://localhost:26657"
	}
	// Allow long waits for broadcast_tx_sync when CheckTx is slow (e.g. ZKP ~30–60s).
	// CometBFT RPC server must also have a large enough WriteTimeout (see initCometBFTConfig).
	client := &http.Client{Timeout: 120 * time.Second}
	return &Handler{
		cometRPC: endpoint,
		client:   client,
	}
}

// RegisterTxRoutes registers vote transaction submission endpoints on the router.
//
//	POST /zally/v1/create-voting-session  → MsgCreateVotingSession
//	POST /zally/v1/delegate-vote          → MsgDelegateVote
//	POST /zally/v1/cast-vote              → MsgCastVote
//	POST /zally/v1/reveal-share           → MsgRevealShare
//	POST /zally/v1/submit-tally           → MsgSubmitTally
//	POST /zally/v1/register-pallas-key            → MsgRegisterPallasKey
//	POST /zally/v1/deal-ea-key                    → MsgDealExecutiveAuthorityKey
//	POST /zally/v1/create-validator-with-pallas   → MsgCreateValidatorWithPallasKey
//	POST /zally/v1/reinitialize-ea               → MsgReInitializeElectionAuthority
//
// Note: MsgAckExecutiveAuthorityKey has no REST endpoint — acks are injected
// in-protocol via PrepareProposal (auto-ack).
func (h *Handler) RegisterTxRoutes(router *mux.Router) {
	router.HandleFunc("/zally/v1/create-voting-session", h.handleCreateVotingSession).Methods("POST")
	router.HandleFunc("/zally/v1/delegate-vote", h.handleDelegateVote).Methods("POST")
	router.HandleFunc("/zally/v1/cast-vote", h.handleCastVote).Methods("POST")
	router.HandleFunc("/zally/v1/reveal-share", h.handleRevealShare).Methods("POST")
	router.HandleFunc("/zally/v1/submit-tally", h.handleSubmitTally).Methods("POST")
	router.HandleFunc("/zally/v1/register-pallas-key", h.handleRegisterPallasKey).Methods("POST")
	router.HandleFunc("/zally/v1/deal-ea-key", h.handleDealEAKey).Methods("POST")
	router.HandleFunc("/zally/v1/create-validator-with-pallas", h.handleCreateValidatorWithPallasKey).Methods("POST")
	router.HandleFunc("/zally/v1/reinitialize-ea", h.handleReInitializeElectionAuthority).Methods("POST")
}

// --- Tx submission handlers ---

func (h *Handler) handleCreateVotingSession(w http.ResponseWriter, r *http.Request) {
	msg := &types.MsgCreateVotingSession{}

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("read body: %v", err))
		return
	}
	if len(body) == 0 {
		writeError(w, http.StatusBadRequest, "empty request body")
		return
	}
	if err := json.Unmarshal(body, msg); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid JSON: %v", err))
		return
	}

	if err := msg.ValidateBasic(); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("validation failed: %v", err))
		return
	}

	h.broadcastVoteTx(w, msg)
}

func (h *Handler) handleDelegateVote(w http.ResponseWriter, r *http.Request) {
	msg := &types.MsgDelegateVote{}
	if !h.decodeAndValidate(w, r, msg) {
		return
	}
	h.broadcastVoteTx(w, msg)
}

func (h *Handler) handleCastVote(w http.ResponseWriter, r *http.Request) {
	msg := &types.MsgCastVote{}
	if !h.decodeAndValidate(w, r, msg) {
		return
	}
	h.broadcastVoteTx(w, msg)
}

func (h *Handler) handleRevealShare(w http.ResponseWriter, r *http.Request) {
	msg := &types.MsgRevealShare{}
	if !h.decodeAndValidate(w, r, msg) {
		return
	}
	h.broadcastVoteTx(w, msg)
}

func (h *Handler) handleSubmitTally(w http.ResponseWriter, r *http.Request) {
	msg := &types.MsgSubmitTally{}
	if !h.decodeAndValidate(w, r, msg) {
		return
	}
	h.broadcastVoteTx(w, msg)
}

// --- Ceremony tx handlers ---

func (h *Handler) handleRegisterPallasKey(w http.ResponseWriter, r *http.Request) {
	msg := &types.MsgRegisterPallasKey{}
	if !h.decodeCeremonyMsg(w, r, msg) {
		return
	}
	h.broadcastCeremonyTx(w, msg, TagRegisterPallasKey)
}

func (h *Handler) handleDealEAKey(w http.ResponseWriter, r *http.Request) {
	msg := &types.MsgDealExecutiveAuthorityKey{}
	if !h.decodeCeremonyMsg(w, r, msg) {
		return
	}
	h.broadcastCeremonyTx(w, msg, TagDealExecutiveAuthorityKey)
}

func (h *Handler) handleCreateValidatorWithPallasKey(w http.ResponseWriter, r *http.Request) {
	msg := &types.MsgCreateValidatorWithPallasKey{}
	if !h.decodeCeremonyMsg(w, r, msg) {
		return
	}
	h.broadcastCeremonyTx(w, msg, TagCreateValidatorWithPallasKey)
}

func (h *Handler) handleReInitializeElectionAuthority(w http.ResponseWriter, r *http.Request) {
	msg := &types.MsgReInitializeElectionAuthority{}
	if !h.decodeCeremonyMsg(w, r, msg) {
		return
	}
	h.broadcastCeremonyTx(w, msg, TagReInitializeElectionAuthority)
}

// decodeCeremonyMsg reads the JSON request body and unmarshals it into the
// protobuf message. Unlike decodeAndValidate, ceremony messages don't
// implement VoteMessage so we skip ValidateBasic here — the keeper performs
// all ceremony validation.
func (h *Handler) decodeCeremonyMsg(w http.ResponseWriter, r *http.Request, msg protov2.Message) bool {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("read body: %v", err))
		return false
	}
	if len(body) == 0 {
		writeError(w, http.StatusBadRequest, "empty request body")
		return false
	}
	if err := json.Unmarshal(body, msg); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid JSON: %v", err))
		return false
	}
	return true
}

// broadcastCeremonyTx encodes a ceremony message to wire format and broadcasts
// it to the local CometBFT node via broadcast_tx_sync.
func (h *Handler) broadcastCeremonyTx(w http.ResponseWriter, msg protov2.Message, tag byte) {
	raw, err := EncodeCeremonyTx(msg, tag)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("encode failed: %v", err))
		return
	}

	start := time.Now()
	result, err := h.cometBroadcastTxSync(raw)
	elapsed := time.Since(start)
	log.Printf("[zally-api] broadcast_tx_sync ceremony duration_ms=%d tag=0x%02x", elapsed.Milliseconds(), tag)
	if err != nil {
		log.Printf("[zally-api] broadcast_tx_sync ceremony failed: %v", err)
		writeError(w, http.StatusBadGateway, fmt.Sprintf("broadcast failed: %v", err))
		return
	}

	writeJSON(w, http.StatusOK, result)
}

// --- Broadcast ---

// BroadcastResult is the JSON response returned to clients after tx submission.
type BroadcastResult struct {
	TxHash string `json:"tx_hash"`
	Code   uint32 `json:"code"`
	Log    string `json:"log,omitempty"`
}

// broadcastVoteTx encodes a vote message to wire format and broadcasts it
// to the local CometBFT node via broadcast_tx_sync.
func (h *Handler) broadcastVoteTx(w http.ResponseWriter, msg types.VoteMessage) {
	raw, err := EncodeVoteTx(msg)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("encode failed: %v", err))
		return
	}

	start := time.Now()
	result, err := h.cometBroadcastTxSync(raw)
	elapsed := time.Since(start)
	log.Printf("[zally-api] broadcast_tx_sync duration_ms=%d msg_type=%T", elapsed.Milliseconds(), msg)
	if err != nil {
		// 502 = CometBFT rejected the broadcast (RPC error). The error string now includes
		// CometBFT's error.data when present (e.g. "tx already in cache", "context canceled").
		log.Printf("[zally-api] broadcast_tx_sync failed: %v", err)
		writeError(w, http.StatusBadGateway, fmt.Sprintf("broadcast failed: %v", err))
		return
	}

	writeJSON(w, http.StatusOK, result)
}

// cometBroadcastTxSync sends raw tx bytes to CometBFT's broadcast_tx_sync
// JSON-RPC endpoint. The tx bytes are automatically base64-encoded by
// encoding/json when marshaled.
func (h *Handler) cometBroadcastTxSync(txBytes []byte) (*BroadcastResult, error) {
	reqBody := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "broadcast_tx_sync",
		"params": map[string]interface{}{
			"tx": txBytes, // encoding/json base64-encodes []byte
		},
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	resp, err := h.client.Post(h.cometRPC, "application/json", bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("HTTP POST to CometBFT: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("CometBFT returned status %d: %s", resp.StatusCode, string(respBody))
	}

	var rpcResp struct {
		Result struct {
			Code uint32 `json:"code"`
			Hash string `json:"hash"`
			Log  string `json:"log"`
		} `json:"result"`
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
			Data    string `json:"data"`
		} `json:"error"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
		return nil, fmt.Errorf("decode CometBFT response: %w", err)
	}

	if rpcResp.Error != nil {
		// -32603 "Internal error" is used by CometBFT when BroadcastTxSync returns a Go error.
		// The real cause is in error.data, e.g. "tx already exists in cache" (duplicate),
		// "broadcast confirmation not received: context canceled" (RPC timeout), or app
		// connection errors.
		detail := rpcResp.Error.Data
		if detail == "" {
			detail = rpcResp.Error.Message
		}

		// "tx already exists in cache" means the tx was previously accepted into the
		// mempool. Treat this as success so callers don't retry indefinitely.
		if strings.Contains(detail, "already exists in cache") {
			log.Printf("[zally-api] tx already in mempool cache, treating as success")
			return &BroadcastResult{
				Code: 0,
				Log:  "tx already exists in mempool cache",
			}, nil
		}

		return nil, fmt.Errorf("CometBFT RPC error %d: %s", rpcResp.Error.Code, detail)
	}

	return &BroadcastResult{
		TxHash: rpcResp.Result.Hash,
		Code:   rpcResp.Result.Code,
		Log:    rpcResp.Result.Log,
	}, nil
}

// --- Helpers ---

// voteProtoMessage is the intersection of VoteMessage and protov2.Message
// that all vote message types satisfy.
type voteProtoMessage interface {
	types.VoteMessage
	protov2.Message
}

// decodeAndValidate reads the JSON request body, unmarshals it into the
// protobuf message using standard JSON encoding, validates basic fields,
// and returns true on success. On failure, writes an error response and
// returns false.
func (h *Handler) decodeAndValidate(w http.ResponseWriter, r *http.Request, msg voteProtoMessage) bool {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)) // 1 MB limit
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("read body: %v", err))
		return false
	}
	if len(body) == 0 {
		writeError(w, http.StatusBadRequest, "empty request body")
		return false
	}

	// Use standard encoding/json for simplicity. Bytes fields should be
	// sent as base64-encoded strings (Go's default JSON encoding for []byte).
	if err := json.Unmarshal(body, msg); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid JSON: %v", err))
		return false
	}

	if err := msg.ValidateBasic(); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("validation failed: %v", err))
		return false
	}

	return true
}

func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data) //nolint:errcheck
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
