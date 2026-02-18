package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/mux"
	"golang.org/x/crypto/blake2b"
	protov2 "google.golang.org/protobuf/proto"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/z-cale/zally/x/vote/types"
)

// HandlerConfig configures the REST API handler.
type HandlerConfig struct {
	// CometRPCEndpoint is the URL of the local CometBFT RPC server.
	// Default: "http://localhost:26657"
	CometRPCEndpoint string

	// IMTServiceURL is the URL of the nullifier IMT query service (e.g. "http://localhost:3000").
	// Used by submit-session to fetch the current nullifier IMT root.
	// Default: "http://localhost:3000"
	IMTServiceURL string

	// LightwalletdURL is the gRPC address of a lightwalletd server (e.g. "https://zec.rocks:443").
	// Used by submit-session to fetch block hashes and tree state.
	// Default: "https://zec.rocks:443"
	LightwalletdURL string
}

// Handler provides JSON REST endpoints for vote transaction submission
// and query access.
type Handler struct {
	cometRPC    string
	client      *http.Client
	snapshotCfg SnapshotConfig
}

// NewHandler creates a new REST API handler.
func NewHandler(cfg HandlerConfig) *Handler {
	endpoint := cfg.CometRPCEndpoint
	if endpoint == "" {
		endpoint = "http://localhost:26657"
	}
	imtURL := cfg.IMTServiceURL
	if imtURL == "" {
		imtURL = "http://localhost:3000"
	}
	lwdURL := cfg.LightwalletdURL
	if lwdURL == "" {
		lwdURL = "https://zec.rocks:443"
	}
	// Allow long waits for broadcast_tx_sync when CheckTx is slow (e.g. ZKP ~30–60s).
	// CometBFT RPC server must also have a large enough WriteTimeout (see initCometBFTConfig).
	client := &http.Client{Timeout: 120 * time.Second}
	return &Handler{
		cometRPC: endpoint,
		client:   client,
		snapshotCfg: SnapshotConfig{
			IMTServiceURL:   imtURL,
			LightwalletdURL: lwdURL,
		},
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
//	POST /zally/v1/set-vote-manager              → MsgSetVoteManager
//	POST /zally/v1/submit-session               → MsgCreateVotingSession (simplified)
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
	router.HandleFunc("/zally/v1/set-vote-manager", h.handleSetVoteManager).Methods("POST")
	router.HandleFunc("/zally/v1/submit-session", h.handleSubmitSession).Methods("POST")
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

func (h *Handler) handleSetVoteManager(w http.ResponseWriter, r *http.Request) {
	msg := &types.MsgSetVoteManager{}
	if !h.decodeCeremonyMsg(w, r, msg) {
		return
	}
	// Validate new_manager is a valid account address before broadcasting.
	if _, err := sdk.AccAddressFromBech32(msg.NewManager); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("new_manager is not a valid account address: %v", err))
		return
	}
	h.broadcastCeremonyTx(w, msg, TagSetVoteManager)
}

// handleSubmitSession accepts a simplified JSON payload from the UI, fetches
// snapshot data from the nullifier IMT service and lightwalletd, computes
// proposals_hash, and broadcasts a MsgCreateVotingSession.
func (h *Handler) handleSubmitSession(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("read body: %v", err))
		return
	}
	if len(body) == 0 {
		writeError(w, http.StatusBadRequest, "empty request body")
		return
	}

	var req struct {
		Creator        string `json:"creator"`
		SnapshotHeight uint64 `json:"snapshot_height"`
		VoteEndTime    uint64 `json:"vote_end_time"`
		Description    string `json:"description"`
		Proposals      []struct {
			ID          uint32 `json:"id"`
			Title       string `json:"title"`
			Description string `json:"description"`
			Options     []struct {
				Index uint32 `json:"index"`
				Label string `json:"label"`
			} `json:"options"`
		} `json:"proposals"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid JSON: %v", err))
		return
	}

	if req.Creator == "" {
		writeError(w, http.StatusBadRequest, "creator is required")
		return
	}
	if req.SnapshotHeight == 0 {
		writeError(w, http.StatusBadRequest, "snapshot_height is required")
		return
	}
	if len(req.Proposals) == 0 {
		writeError(w, http.StatusBadRequest, "at least one proposal is required")
		return
	}

	// Fetch snapshot data from IMT service + lightwalletd (parallel).
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	snapshot, err := fetchSnapshotData(ctx, h.snapshotCfg, req.SnapshotHeight)
	if err != nil {
		log.Printf("[zally-api] snapshot data fetch failed: %v", err)
		writeError(w, http.StatusBadGateway, fmt.Sprintf("fetch snapshot data: %v", err))
		return
	}

	// Build proto proposals and compute proposals_hash (Blake2b-256 of serialized proposals).
	protoProposals := make([]*types.Proposal, len(req.Proposals))
	proposalsHasher, _ := blake2b.New256(nil)
	for i, p := range req.Proposals {
		opts := make([]*types.VoteOption, len(p.Options))
		for j, o := range p.Options {
			opts[j] = &types.VoteOption{Index: o.Index, Label: o.Label}
		}
		prop := &types.Proposal{
			Id:          p.ID,
			Title:       p.Title,
			Description: p.Description,
			Options:     opts,
		}
		protoProposals[i] = prop

		propBytes, err := protov2.Marshal(prop)
		if err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("marshal proposal: %v", err))
			return
		}
		proposalsHasher.Write(propBytes)
	}
	proposalsHash := proposalsHasher.Sum(nil)

	// Build the full MsgCreateVotingSession.
	// - nullifier_imt_root: real value from IMT service
	// - snapshot_blockhash: real value from lightwalletd
	// - nc_root: SHA-256 placeholder of orchard tree frontier (see snapshot.go)
	// - vk_zkp1/2/3: dummy values — the chain stores but does not verify these
	//   during session creation; verification uses compiled circuit VKs via FFI.
	msg := &types.MsgCreateVotingSession{
		Creator:           req.Creator,
		SnapshotHeight:    req.SnapshotHeight,
		SnapshotBlockhash: snapshot.SnapshotBlockhash,
		ProposalsHash:     proposalsHash,
		VoteEndTime:       req.VoteEndTime,
		NullifierImtRoot:  snapshot.NullifierIMTRoot,
		NcRoot:            snapshot.NcRoot,
		VkZkp1:            bytes.Repeat([]byte{0x11}, 64),
		VkZkp2:            bytes.Repeat([]byte{0x22}, 64),
		VkZkp3:            bytes.Repeat([]byte{0x33}, 64),
		Proposals:         protoProposals,
		Description:       req.Description,
	}

	if err := msg.ValidateBasic(); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("validation failed: %v", err))
		return
	}

	h.broadcastVoteTx(w, msg)
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
