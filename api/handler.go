package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
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

	// Snapshot configures external service URLs for fetching Zcash snapshot
	// data (nc_root from lightwalletd, nullifier IMT root from IMT service).
	Snapshot SnapshotConfig
}

// Handler provides JSON REST endpoints for vote transaction submission
// and query access.
type Handler struct {
	cometRPC string
	client   *http.Client
	snapshot SnapshotConfig
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
		snapshot: cfg.Snapshot,
	}
}

// RegisterTxRoutes registers vote transaction submission endpoints on the router.
//
//	POST /zally/v1/delegate-vote          → MsgDelegateVote
//	POST /zally/v1/cast-vote              → MsgCastVote
//	POST /zally/v1/reveal-share           → MsgRevealShare
//
// MsgSubmitTally is proposer-only (auto-injected via PrepareProposal) and
// has no REST endpoint.
//
// MsgCreateVotingSession is a standard Cosmos SDK transaction (signed by
// the vote manager) and should be submitted via zallyd tx sign/broadcast
// or /cosmos/tx/v1beta1/txs.
//
// Ceremony messages (MsgRegisterPallasKey, MsgDealExecutiveAuthorityKey,
// MsgCreateValidatorWithPallasKey, MsgSetVoteManager) are also standard
// Cosmos SDK transactions.
//
// MsgAckExecutiveAuthorityKey and MsgSubmitPartialDecryption have no REST
// endpoints — they are injected in-protocol via PrepareProposal.
func (h *Handler) RegisterTxRoutes(router *mux.Router) {
	router.HandleFunc("/zally/v1/delegate-vote", h.handleDelegateVote).Methods("POST")
	router.HandleFunc("/zally/v1/cast-vote", h.handleCastVote).Methods("POST")
	router.HandleFunc("/zally/v1/reveal-share", h.handleRevealShare).Methods("POST")

	// Snapshot data endpoint: fetches real nc_root and nullifier_imt_root
	// for session creation. Used by the admin UI to replace stub values.
	router.HandleFunc("/zally/v1/snapshot-data/{height}", h.handleSnapshotData).Methods("GET")

	// TX confirmation endpoint: checks whether a TX has been included in a block.
	// Used by iOS app to verify vote commitment TXs landed after tree growth timeout.
	router.HandleFunc("/zally/v1/tx/{hash}", h.handleTxStatus).Methods("GET")
}

// --- Tx submission handlers ---

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

// --- Snapshot data ---

func (h *Handler) handleSnapshotData(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	heightStr := vars["height"]
	height, err := strconv.ParseUint(heightStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid height: %v", err))
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	data, err := fetchSnapshotData(ctx, h.snapshot, height)
	if err != nil {
		log.Printf("[zally-api] snapshot-data error: %v", err)
		writeError(w, http.StatusBadGateway, fmt.Sprintf("fetch snapshot data: %v", err))
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"nc_root":            hex.EncodeToString(data.NcRoot),
		"nullifier_imt_root": hex.EncodeToString(data.NullifierIMTRoot),
		"snapshot_blockhash": hex.EncodeToString(data.SnapshotBlockhash),
	})
}

// --- TX status ---

// txStatusResult holds the confirmed status of a transaction in a block.
type txStatusResult struct {
	Height string
	Code   uint32
	Log    string
}

// errTxNotFound is returned by queryTxByHash when CometBFT has no record of the TX in any block.
var errTxNotFound = errors.New("tx not found in any block")

// queryTxByHash queries CometBFT's /tx JSON-RPC endpoint for a confirmed
// transaction. Returns errTxNotFound if the TX is not yet in a block.
func (h *Handler) queryTxByHash(txHash string) (*txStatusResult, error) {
	reqBody := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tx",
		"params": map[string]interface{}{
			"hash":  txHash,
			"prove": false,
		},
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "POST", h.cometRPC, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := h.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("CometBFT request failed: %w", err)
	}
	defer resp.Body.Close()

	var rpcResp struct {
		Result *struct {
			Height   string `json:"height"`
			TxResult struct {
				Code uint32 `json:"code"`
				Log  string `json:"log"`
			} `json:"tx_result"`
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
		return nil, errTxNotFound
	}

	if rpcResp.Result == nil {
		return nil, fmt.Errorf("unexpected empty result from CometBFT")
	}

	return &txStatusResult{
		Height: rpcResp.Result.Height,
		Code:   rpcResp.Result.TxResult.Code,
		Log:    rpcResp.Result.TxResult.Log,
	}, nil
}

// handleTxStatus queries CometBFT for a confirmed transaction by hash.
// Returns { "height": "...", "code": 0, "log": "" } if the TX is in a block,
// or 404 if not yet included. Returns HTTP 422 if the TX was included but
// failed during execution (code != 0).
func (h *Handler) handleTxStatus(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	txHash := vars["hash"]
	if txHash == "" {
		writeError(w, http.StatusBadRequest, "missing tx hash")
		return
	}

	result, err := h.queryTxByHash(txHash)
	if errors.Is(err, errTxNotFound) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{"error": "tx not found"}) //nolint:errcheck
		return
	}
	if err != nil {
		writeError(w, http.StatusBadGateway, fmt.Sprintf("CometBFT query failed: %v", err))
		return
	}

	resp := map[string]interface{}{
		"height": result.Height,
		"code":   result.Code,
		"log":    result.Log,
	}
	if result.Code != 0 {
		writeJSON(w, http.StatusUnprocessableEntity, resp)
		return
	}
	writeJSON(w, http.StatusOK, resp)
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

	if result.Code != 0 {
		log.Printf("[zally-api] CheckTx rejected (code %d): %s", result.Code, result.Log)
		writeJSON(w, http.StatusUnprocessableEntity, result)
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

		// "tx already exists in cache" means the tx bytes were seen before by CometBFT's
		// mempool. This includes txs that passed CheckTx AND txs that were rejected — the
		// cache tracks hashes, not outcomes. Query CometBFT /tx to find the real status.
		if strings.Contains(detail, "already exists in cache") {
			txHash := fmt.Sprintf("%X", sha256.Sum256(txBytes))
			log.Printf("[zally-api] tx already in mempool cache, querying real status hash=%s", txHash)

			status, err := h.queryTxByHash(txHash)
			if errors.Is(err, errTxNotFound) {
				// TX is pending in the mempool (not yet committed). Return the hash
				// so the client can poll /tx/{hash} for confirmation.
				return &BroadcastResult{
					TxHash: txHash,
					Code:   0,
					Log:    "tx pending in mempool (duplicate submission)",
				}, nil
			}
			if err != nil {
				return nil, fmt.Errorf("tx in cache but status query failed: %w", err)
			}

			// TX was committed — return the real outcome (may be code 0 or non-zero).
			return &BroadcastResult{
				TxHash: txHash,
				Code:   status.Code,
				Log:    status.Log,
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
