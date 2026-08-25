package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	sentrysdk "github.com/getsentry/sentry-go"
	sentryhttp "github.com/getsentry/sentry-go/http"
	"github.com/gorilla/mux"
	protov2 "google.golang.org/protobuf/proto"

	"github.com/valargroup/vote-sdk/sentry"
	"github.com/valargroup/vote-sdk/x/vote/types"
)

const (
	// CryptoReadinessStatusNotStarted means the node has not launched verifier
	// cache warm-up yet. Submit endpoints fail closed in this state.
	CryptoReadinessStatusNotStarted = "not_started"

	// CryptoReadinessStatusWarming means the node is currently initializing the
	// expensive Halo2 verifier params and verifying keys.
	CryptoReadinessStatusWarming = "warming"

	// CryptoReadinessStatusReady means vote submit endpoints may broadcast txs
	// because the local process has already paid the verifier cold-cache cost.
	CryptoReadinessStatusReady = "ready"

	// CryptoReadinessStatusFailed means verifier cache warm-up failed. Submit
	// endpoints remain unavailable until the process is restarted and warms
	// successfully.
	CryptoReadinessStatusFailed = "failed"

	// cometBroadcastAttemptTimeout bounds each individual broadcast_tx_sync call.
	// Slow CheckTx should not hold the helper request open indefinitely.
	cometBroadcastAttemptTimeout = 10 * time.Second

	// cometBroadcastMaxAttempts is the total number of attempts for transport-level
	// failures where CometBFT may still have accepted the tx.
	cometBroadcastMaxAttempts = 3

	// cometStatusQueryTimeout bounds best-effort status lookups after ambiguous
	// broadcast outcomes. The public /tx/{hash} endpoint has its own timeout.
	cometStatusQueryTimeout = 5 * time.Second
)

// HandlerConfig configures the REST API handler.
type HandlerConfig struct {
	// CometRPCEndpoint is the URL of the local CometBFT RPC server.
	// Default: "http://localhost:26657"
	CometRPCEndpoint string

	// Snapshot configures external service URLs for fetching Zcash snapshot
	// data (Ironwood nc_root from lightwalletd, nullifier IMT root from PIR).
	Snapshot SnapshotConfig

	// CryptoReadiness returns the process-local Halo2 verifier warm-up status.
	// If nil, transaction submission is treated as ready for compatibility.
	CryptoReadiness func() CryptoReadinessStatus
}

// Handler provides JSON REST endpoints for vote transaction submission
// and query access.
type Handler struct {
	cometRPC        string
	client          *http.Client
	snapshot        SnapshotConfig
	cryptoReadiness func() CryptoReadinessStatus
}

// NewHandler creates a new REST API handler.
func NewHandler(cfg HandlerConfig) *Handler {
	endpoint := cfg.CometRPCEndpoint
	if endpoint == "" {
		endpoint = "http://localhost:26657"
	}
	// Individual CometBFT RPC calls use shorter per-request contexts; this
	// client timeout is a final guardrail for any caller that forgets one.
	client := &http.Client{Timeout: 30 * time.Second}
	return &Handler{
		cometRPC:        endpoint,
		client:          client,
		snapshot:        cfg.Snapshot,
		cryptoReadiness: cfg.CryptoReadiness,
	}
}

// CryptoReadinessStatus describes whether the process has initialized the
// expensive verifier caches needed for vote CheckTx.
type CryptoReadinessStatus struct {
	// Status is one of CryptoReadinessStatusNotStarted, Warming, Ready, or Failed.
	Status string `json:"status"`

	// StartedAt is set once this process starts warming verifier caches.
	StartedAt *time.Time `json:"started_at,omitempty"`

	// CompletedAt is set when warm-up reaches either ready or failed.
	CompletedAt *time.Time `json:"completed_at,omitempty"`

	// DurationMS is the elapsed warm-up duration. While warming, it is measured
	// from StartedAt to now; after completion, from StartedAt to CompletedAt.
	DurationMS int64 `json:"duration_ms,omitempty"`

	// Error contains the warm-up failure message when Status is failed.
	Error string `json:"error,omitempty"`
}

// ready reports whether submit endpoints may proceed to CometBFT.
func (s CryptoReadinessStatus) ready() bool {
	return s.Status == CryptoReadinessStatusReady
}

// RegisterTxRoutes registers vote transaction submission endpoints on the router.
//
//	POST /shielded-vote/v1/delegate-vote          → MsgDelegateVote
//	POST /shielded-vote/v1/cast-vote              → MsgCastVote
//	POST /shielded-vote/v1/cast-vote-batch        → MsgCastVoteBatch
//	POST /shielded-vote/v1/reveal-share           → MsgRevealShare
//
// MsgSubmitTally is proposer-only (auto-injected via PrepareProposal) and
// has no REST endpoint.
//
// Coordinator-owned actions such as MsgCreateVotingSession and
// MsgUpdateVoteManagers should be submitted inside MsgProposeCoordinatorAction
// via svoted tx sign/broadcast or /cosmos/tx/v1beta1/txs.
//
// Validator-owned messages such as MsgRegisterPallasKey and
// MsgCreateValidatorWithPallasKey are also standard Cosmos SDK transactions.
//
// MsgAckExecutiveAuthorityKey and MsgSubmitPartialDecryption have no REST
// endpoints — they are injected in-protocol via PrepareProposal.
func (h *Handler) RegisterTxRoutes(router *mux.Router) {
	trace := sentryhttp.New(sentryhttp.Options{Repanic: true}).Handle
	router.Handle("/shielded-vote/v1/delegate-vote", trace(http.HandlerFunc(h.handleDelegateVote))).Methods("POST")
	router.Handle("/shielded-vote/v1/cast-vote", trace(http.HandlerFunc(h.handleCastVote))).Methods("POST")
	router.Handle("/shielded-vote/v1/cast-vote-batch", trace(http.HandlerFunc(h.handleCastVoteBatch))).Methods("POST")
	router.Handle("/shielded-vote/v1/reveal-share", trace(http.HandlerFunc(h.handleRevealShare))).Methods("POST")

	router.Handle("/shielded-vote/v1/snapshot-data/{height}", trace(http.HandlerFunc(h.handleSnapshotData))).Methods("GET")

	router.Handle("/shielded-vote/v1/tx/{hash}", trace(http.HandlerFunc(h.handleTxStatus))).Methods("GET")
	router.Handle("/shielded-vote/v1/readiness", trace(http.HandlerFunc(h.handleReadiness))).Methods("GET")
}

// --- Tx submission handlers ---

func (h *Handler) handleDelegateVote(w http.ResponseWriter, r *http.Request) {
	if !h.ensureCryptoReady(w) {
		return
	}
	msg := &types.MsgDelegateVote{}
	if !h.decodeAndValidate(w, r, msg) {
		return
	}
	h.broadcastVoteTx(r.Context(), w, msg)
}

func (h *Handler) handleCastVote(w http.ResponseWriter, r *http.Request) {
	if !h.ensureCryptoReady(w) {
		return
	}
	msg := &types.MsgCastVote{}
	if !h.decodeAndValidate(w, r, msg) {
		return
	}
	h.broadcastVoteTx(r.Context(), w, msg)
}

func (h *Handler) handleCastVoteBatch(w http.ResponseWriter, r *http.Request) {
	if !h.ensureCryptoReady(w) {
		return
	}
	msg := &types.MsgCastVoteBatch{}
	if !h.decodeAndValidateCanonicalJSON(w, r, msg) {
		return
	}
	h.broadcastVoteTx(r.Context(), w, msg)
}

func (h *Handler) handleRevealShare(w http.ResponseWriter, r *http.Request) {
	if !h.ensureCryptoReady(w) {
		return
	}
	msg := &types.MsgRevealShare{}
	if !h.decodeAndValidate(w, r, msg) {
		return
	}
	h.broadcastVoteTx(r.Context(), w, msg)
}

// handleReadiness exposes the process-local crypto warm-up state for operators
// and load balancers. It deliberately reflects only crypto readiness, not full
// chain health.
func (h *Handler) handleReadiness(w http.ResponseWriter, r *http.Request) {
	status := h.currentCryptoReadiness()
	if !status.ready() {
		w.Header().Set("Retry-After", "2")
		writeJSON(w, http.StatusServiceUnavailable, status)
		return
	}
	writeJSON(w, http.StatusOK, status)
}

// ensureCryptoReady gates write endpoints before they submit to CometBFT. This
// keeps the first live vote transaction from paying Halo2 verifier keygen during
// CheckTx and surfacing as a transport timeout.
func (h *Handler) ensureCryptoReady(w http.ResponseWriter) bool {
	status := h.currentCryptoReadiness()
	if status.ready() {
		return true
	}

	w.Header().Set("Retry-After", "2")
	writeJSON(w, http.StatusServiceUnavailable, status)
	return false
}

// currentCryptoReadiness returns the configured readiness snapshot. A nil getter
// preserves the historical behavior for tests or embedders that do not wire
// process-level crypto warm-up.
func (h *Handler) currentCryptoReadiness() CryptoReadinessStatus {
	if h.cryptoReadiness == nil {
		return CryptoReadinessStatus{Status: CryptoReadinessStatusReady}
	}
	status := h.cryptoReadiness()
	if status.Status == "" {
		status.Status = CryptoReadinessStatusNotStarted
	}
	return status
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
		log.Printf("[shielded-vote-api] snapshot-data error: %v", err)
		writeError(w, http.StatusBadGateway, fmt.Sprintf("fetch snapshot data: %v", err))
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		types.SessionKeyNcRoot:           hex.EncodeToString(data.NcRoot),
		types.SessionKeyNullifierImtRoot: hex.EncodeToString(data.NullifierIMTRoot),
		types.SessionKeyBlockhash:        hex.EncodeToString(data.SnapshotBlockhash),
	})
}

// --- TX status ---

// txStatusResult holds the confirmed status of a transaction in a block,
// including any ABCI events emitted during execution.
type txStatusResult struct {
	Height string
	Code   uint32
	Log    string
	Events []abciEvent
}

type abciEvent struct {
	Type       string          `json:"type"`
	Attributes []abciAttribute `json:"attributes"`
}

type abciAttribute struct {
	Key   string `json:"key"`
	Value string `json:"value"`
	Index bool   `json:"index,omitempty"`
}

// decodeBase64IfPlain tries to base64-decode s. CometBFT ≤0.37 encodes event
// attribute keys/values as base64; ≥0.38 sends plain strings. If decoding
// succeeds and the result is valid UTF-8, the decoded string is returned;
// otherwise the original value is returned unchanged.
func decodeBase64IfPlain(s string) string {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return s
	}
	decoded := string(b)
	for _, r := range decoded {
		if r == '\uFFFD' {
			return s
		}
	}
	return decoded
}

// errTxNotFound is returned by queryTxByHash when CometBFT has no record of the TX in any block.
var errTxNotFound = errors.New("tx not found in any block")

// queryTxByHash queries CometBFT's /tx JSON-RPC endpoint for a transaction
// that has already been included in a block. It returns errTxNotFound when
// CometBFT has no committed record yet, which includes txs still waiting in
// the mempool.
func (h *Handler) queryTxByHash(ctx context.Context, txHash string) (*txStatusResult, error) {
	// CometBFT JSON-RPC unmarshals the hash param as []byte via Go's
	// json.Unmarshal, which expects base64 — not hex. Convert hex → bytes → base64.
	hashBytes, err := hex.DecodeString(txHash)
	if err != nil {
		return nil, fmt.Errorf("invalid tx hash hex: %w", err)
	}
	hashB64 := base64.StdEncoding.EncodeToString(hashBytes)

	reqBody := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tx",
		"params": map[string]interface{}{
			"hash":  hashB64,
			"prove": false,
		},
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
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
				Code   uint32      `json:"code"`
				Log    string      `json:"log"`
				Events []abciEvent `json:"events"`
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

	events := rpcResp.Result.TxResult.Events
	for i, ev := range events {
		for j, attr := range ev.Attributes {
			events[i].Attributes[j].Key = decodeBase64IfPlain(attr.Key)
			events[i].Attributes[j].Value = decodeBase64IfPlain(attr.Value)
		}
	}

	return &txStatusResult{
		Height: rpcResp.Result.Height,
		Code:   rpcResp.Result.TxResult.Code,
		Log:    rpcResp.Result.TxResult.Log,
		Events: events,
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

	result, err := h.queryTxByHash(r.Context(), txHash)
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
		"events": result.Events,
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
	TxHash      string `json:"tx_hash"`
	Code        uint32 `json:"code"`
	Log         string `json:"log,omitempty"`
	BatchDigest string `json:"batch_digest,omitempty"`
}

// broadcastVoteTx encodes a vote message, broadcasts it to the local CometBFT
// node, and translates the result into the REST response contract. Ambiguous
// transport failures are reconciled by cometBroadcastTxSyncWithRetry before an
// HTTP 502 is returned to the client.
func (h *Handler) broadcastVoteTx(ctx context.Context, w http.ResponseWriter, msg types.VoteMessage) {
	raw, err := EncodeVoteTx(msg)
	if err != nil {
		sentry.CaptureErr(err, map[string]string{
			"handler":  "broadcastVoteTx",
			"stage":    "encode",
			"msg_type": fmt.Sprintf("%T", msg),
		})
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("encode failed: %v", err))
		return
	}

	start := time.Now()
	result, err := h.cometBroadcastTxSyncWithRetry(ctx, raw, fmt.Sprintf("%T", msg))
	elapsed := time.Since(start)
	log.Printf("[shielded-vote-api] broadcast_tx_sync duration_ms=%d msg_type=%T", elapsed.Milliseconds(), msg)
	if err != nil {
		// Ambiguous transport failures are handled inside cometBroadcastTxSyncWithRetry.
		// Anything that reaches this point is a definite CometBFT/RPC rejection.
		log.Printf("[shielded-vote-api] broadcast_tx_sync failed: %v", err)
		sentry.CaptureErr(err, map[string]string{
			"handler":  "broadcastVoteTx",
			"stage":    "broadcast",
			"msg_type": fmt.Sprintf("%T", msg),
		})
		writeError(w, http.StatusBadGateway, fmt.Sprintf("broadcast failed: %v", err))
		return
	}
	if batch, ok := msg.(*types.MsgCastVoteBatch); ok {
		result.BatchDigest = hex.EncodeToString(types.ComputeCastVoteBatchSighash(batch))
	}

	if result.Code != 0 {
		log.Printf("[shielded-vote-api] CheckTx rejected (code %d): %s", result.Code, result.Log)
		writeJSON(w, http.StatusUnprocessableEntity, result)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// cometBroadcastTxSyncWithRetry broadcasts txBytes with bounded retries for
// transport errors whose outcome is unknown. EOFs and timeouts can happen after
// CometBFT accepts the tx but before the helper receives the RPC response, so
// each ambiguous failure is followed by a /tx lookup for the precomputed hash.
func (h *Handler) cometBroadcastTxSyncWithRetry(ctx context.Context, txBytes []byte, msgType string) (*BroadcastResult, error) {
	txHash := txHashHex(txBytes)
	var lastErr error

	for attempt := 1; attempt <= cometBroadcastMaxAttempts; attempt++ {
		result, err := h.cometBroadcastTxSyncAttempt(ctx, txBytes, txHash, msgType, attempt)
		if err == nil {
			return result, nil
		}

		lastErr = err
		if !isUnknownBroadcastError(err) {
			return nil, err
		}

		log.Printf("[shielded-vote-api] broadcast_tx_sync unknown outcome attempt=%d tx_hash=%s: %v", attempt, txHash, err)
		status, statusErr := h.queryTxByHashWithSpan(ctx, txHash, msgType, attempt)
		if statusErr == nil {
			return broadcastResultFromTxStatus(txHash, status), nil
		}
		captureStatusLookupError(statusErr, msgType, txHash)
	}

	return nil, fmt.Errorf("broadcast outcome unknown after retries; tx_hash=%s: %w", txHash, lastErr)
}

// cometBroadcastTxSyncAttempt wraps one broadcast_tx_sync call with its per-RPC
// timeout and Sentry span. Retry decisions are made by the caller.
func (h *Handler) cometBroadcastTxSyncAttempt(ctx context.Context, txBytes []byte, txHash, msgType string, attempt int) (*BroadcastResult, error) {
	span := h.startCometSpan(ctx, "broadcast_tx_sync", txHash, msgType, attempt)
	attemptCtx, cancel := context.WithTimeout(span.Context(), cometBroadcastAttemptTimeout)

	start := time.Now()
	result, err := h.cometBroadcastTxSync(attemptCtx, txBytes, txHash)
	elapsed := time.Since(start)
	cancel()

	h.finishCometSpan(span, err, elapsed)
	log.Printf("[shielded-vote-api] broadcast_tx_sync attempt=%d duration_ms=%d tx_hash=%s msg_type=%s", attempt, elapsed.Milliseconds(), txHash, msgType)
	return result, err
}

// cometBroadcastTxSync sends raw tx bytes to CometBFT's broadcast_tx_sync
// JSON-RPC endpoint. The tx bytes are automatically base64-encoded by
// encoding/json when marshaled.
func (h *Handler) cometBroadcastTxSync(ctx context.Context, txBytes []byte, txHash string) (*BroadcastResult, error) {
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

	req, err := http.NewRequestWithContext(ctx, "POST", h.cometRPC, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("create CometBFT request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := h.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("HTTP POST to CometBFT: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			return nil, fmt.Errorf("CometBFT returned status %d (body unreadable: %v)", resp.StatusCode, readErr)
		}
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
			log.Printf("[shielded-vote-api] tx already in mempool cache, querying real status hash=%s", txHash)

			status, err := h.queryTxByHash(ctx, txHash)
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
			return broadcastResultFromTxStatus(txHash, status), nil
		}

		return nil, fmt.Errorf("CometBFT RPC error %d: %s", rpcResp.Error.Code, detail)
	}

	return &BroadcastResult{
		TxHash: rpcResp.Result.Hash,
		Code:   rpcResp.Result.Code,
		Log:    rpcResp.Result.Log,
	}, nil
}

// queryTxByHashWithSpan records the status lookup used to reconcile an
// ambiguous broadcast. A not-found result is expected while a tx is still
// pending, so callers decide whether to surface or ignore that error.
func (h *Handler) queryTxByHashWithSpan(ctx context.Context, txHash, msgType string, attempt int) (*txStatusResult, error) {
	span := h.startCometSpan(ctx, "tx", txHash, msgType, attempt)
	statusCtx, cancel := context.WithTimeout(span.Context(), cometStatusQueryTimeout)

	start := time.Now()
	status, err := h.queryTxByHash(statusCtx, txHash)
	elapsed := time.Since(start)
	cancel()

	h.finishCometSpan(span, err, elapsed)
	return status, err
}

// startCometSpan creates optional Sentry instrumentation for a CometBFT RPC.
// sentry-go is safe when Sentry is not configured; in that case this remains
// local no-op tracing and does not affect request handling.
func (h *Handler) startCometSpan(ctx context.Context, method, txHash, msgType string, attempt int) *sentrysdk.Span {
	span := sentrysdk.StartSpan(ctx, "rpc.client", sentrysdk.WithDescription("CometBFT "+method))
	span.SetTag("rpc.system", "jsonrpc")
	span.SetTag("rpc.service", "cometbft")
	span.SetTag("rpc.method", method)
	span.SetTag("tx_hash", txHash)
	span.SetTag("msg_type", msgType)
	span.SetData("attempt", attempt)
	span.SetData("endpoint", h.cometRPC)
	return span
}

// finishCometSpan records the outcome and coarse error kind for a CometBFT RPC
// span before closing it.
func (h *Handler) finishCometSpan(span *sentrysdk.Span, err error, elapsed time.Duration) {
	span.SetData("duration_ms", elapsed.Milliseconds())
	if err == nil {
		span.SetTag("outcome", "ok")
		span.Status = sentrysdk.SpanStatusOK
		span.Finish()
		return
	}

	span.SetTag("outcome", "error")
	span.SetTag("error_kind", cometErrorKind(err))
	if errors.Is(err, errTxNotFound) {
		span.Status = sentrysdk.SpanStatusNotFound
	} else if isUnknownBroadcastError(err) {
		span.Status = sentrysdk.SpanStatusDeadlineExceeded
	} else {
		span.Status = sentrysdk.SpanStatusInternalError
	}
	span.Finish()
}

// isUnknownBroadcastError reports whether err means the helper lost the RPC
// response without knowing if CometBFT accepted the tx. These are safe to retry
// because the tx hash is deterministic and duplicate submissions are reconciled
// through CometBFT's cache/status query behavior.
func isUnknownBroadcastError(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}

	var netErr net.Error
	return errors.As(err, &netErr) && (netErr.Timeout() || netErr.Temporary())
}

// cometErrorKind normalizes transport/status failures into low-cardinality
// Sentry tag values.
func cometErrorKind(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline_exceeded"
	case errors.Is(err, io.EOF):
		return "eof"
	case errors.Is(err, io.ErrUnexpectedEOF):
		return "unexpected_eof"
	case errors.Is(err, errTxNotFound):
		return "tx_not_found"
	default:
		var netErr net.Error
		if errors.As(err, &netErr) {
			if netErr.Timeout() {
				return "net_timeout"
			}
			if netErr.Temporary() {
				return "net_temporary"
			}
			return "net_error"
		}
		return "other"
	}
}

func txHashHex(txBytes []byte) string {
	return fmt.Sprintf("%X", sha256.Sum256(txBytes))
}

func broadcastResultFromTxStatus(txHash string, status *txStatusResult) *BroadcastResult {
	return &BroadcastResult{
		TxHash: txHash,
		Code:   status.Code,
		Log:    status.Log,
	}
}

func captureStatusLookupError(err error, msgType, txHash string) {
	if errors.Is(err, errTxNotFound) {
		return
	}

	sentry.CaptureErr(err, map[string]string{
		"handler":  "broadcastVoteTx",
		"stage":    "status_after_unknown_broadcast",
		"msg_type": msgType,
		"tx_hash":  txHash,
	})
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

// decodeAndValidateCanonicalJSON applies strict JSON decoding for atomic vote
// batches. JSON object key order is not significant; after decoding, the wire
// transaction is always produced with deterministic protobuf serialization.
// Rejecting unknown fields and trailing values keeps retries reproducible and
// prevents clients from believing unsigned data was accepted.
func (h *Handler) decodeAndValidateCanonicalJSON(w http.ResponseWriter, r *http.Request, msg voteProtoMessage) bool {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("read body: %v", err))
		return false
	}
	if len(body) == 0 {
		writeError(w, http.StatusBadRequest, "empty request body")
		return false
	}

	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(msg); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid JSON: %v", err))
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			writeError(w, http.StatusBadRequest, "invalid JSON: trailing value")
		} else {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid JSON: %v", err))
		}
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
