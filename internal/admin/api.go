package admin

import (
	"encoding/json"
	"net/http"
	"time"

	"cosmossdk.io/log"
	"github.com/gorilla/mux"
)

const (
	timestampWindowSecs = 300           // 5 minutes
	pendingExpirySecs   = 7 * 24 * 3600 // 7 days
)

// RegisterRoutes registers admin HTTP routes on the given mux router.
// Getters are used so routes can be mounted before the admin server is
// fully initialized (same pattern as the helper server).
func RegisterRoutes(
	router *mux.Router,
	getAdmin func() *Admin,
	logger log.Logger,
) {
	h := &apiHandler{getAdmin: getAdmin, logger: logger}

	router.HandleFunc("/api/voting-config", h.handleGetVotingConfig).Methods("GET", "OPTIONS")
	router.HandleFunc("/api/approved-servers", h.handleGetApprovedServers).Methods("GET", "OPTIONS")
	router.HandleFunc("/api/pending-registrations", h.handleGetPendingRegistrations).Methods("GET", "OPTIONS")
	router.HandleFunc("/api/server-pulses", h.handleGetServerPulses).Methods("GET", "OPTIONS")
	router.HandleFunc("/api/update-voting-config", h.handleUpdateVotingConfig).Methods("POST", "OPTIONS")
	router.HandleFunc("/api/register-validator", h.handleRegisterValidator).Methods("POST", "OPTIONS")
	router.HandleFunc("/api/server-heartbeat", h.handleServerHeartbeat).Methods("POST", "OPTIONS")
	router.HandleFunc("/api/approve-registration", h.handleApproveRegistration).Methods("POST", "OPTIONS")
	router.HandleFunc("/api/remove-approved-server", h.handleRemoveApprovedServer).Methods("POST", "OPTIONS")
}

type apiHandler struct {
	getAdmin func() *Admin
	logger   log.Logger
}

func corsHeaders(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
}

func jsonResponse(w http.ResponseWriter, body interface{}, status int) {
	corsHeaders(w)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(body)
}

func jsonError(w http.ResponseWriter, msg string, status int) {
	jsonResponse(w, map[string]string{"error": msg}, status)
}

func (h *apiHandler) admin() *Admin {
	return h.getAdmin()
}

// --- Read endpoints ---

func (h *apiHandler) handleGetVotingConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method == "OPTIONS" {
		corsHeaders(w)
		w.WriteHeader(204)
		return
	}
	a := h.admin()
	if a == nil {
		jsonError(w, "admin server not initialized", 503)
		return
	}
	cfg, err := a.Store.BuildVotingConfig(a.PIRServers)
	if err != nil {
		jsonError(w, "internal error", 500)
		h.logger.Error("build voting config", "error", err)
		return
	}
	jsonResponse(w, cfg, 200)
}

func (h *apiHandler) handleGetApprovedServers(w http.ResponseWriter, r *http.Request) {
	if r.Method == "OPTIONS" {
		corsHeaders(w)
		w.WriteHeader(204)
		return
	}
	a := h.admin()
	if a == nil {
		jsonError(w, "admin server not initialized", 503)
		return
	}
	servers, err := a.Store.ListApprovedServers()
	if err != nil {
		jsonError(w, "internal error", 500)
		h.logger.Error("list approved servers", "error", err)
		return
	}
	if servers == nil {
		servers = []ServiceEntry{}
	}
	jsonResponse(w, servers, 200)
}

func (h *apiHandler) handleGetPendingRegistrations(w http.ResponseWriter, r *http.Request) {
	if r.Method == "OPTIONS" {
		corsHeaders(w)
		w.WriteHeader(204)
		return
	}
	a := h.admin()
	if a == nil {
		jsonError(w, "admin server not initialized", 503)
		return
	}
	regs, err := a.Store.ListPendingRegistrations()
	if err != nil {
		jsonError(w, "internal error", 500)
		h.logger.Error("list pending registrations", "error", err)
		return
	}
	if regs == nil {
		regs = []PendingRegistration{}
	}
	jsonResponse(w, regs, 200)
}

func (h *apiHandler) handleGetServerPulses(w http.ResponseWriter, r *http.Request) {
	if r.Method == "OPTIONS" {
		corsHeaders(w)
		w.WriteHeader(204)
		return
	}
	a := h.admin()
	if a == nil {
		jsonError(w, "admin server not initialized", 503)
		return
	}
	pulses, err := a.Store.GetPulses()
	if err != nil {
		jsonError(w, "internal error", 500)
		h.logger.Error("get pulses", "error", err)
		return
	}
	jsonResponse(w, pulses, 200)
}

// --- Write endpoints ---

type signedPayloadRequest struct {
	Payload       json.RawMessage `json:"payload"`
	Signature     string          `json:"signature"`
	PubKey        string          `json:"pubKey"`
	SignerAddress string          `json:"signerAddress"`
}

func (h *apiHandler) handleUpdateVotingConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method == "OPTIONS" {
		corsHeaders(w)
		w.WriteHeader(204)
		return
	}
	a := h.admin()
	if a == nil {
		jsonError(w, "admin server not initialized", 503)
		return
	}

	var req signedPayloadRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "invalid JSON body", 400)
		return
	}
	if req.SignerAddress == "" || req.Signature == "" || req.PubKey == "" || req.Payload == nil {
		jsonError(w, "missing required fields: payload, signature, pubKey, signerAddress", 400)
		return
	}

	payloadStr := string(req.Payload)
	if err := VerifyArbitrarySignature(req.SignerAddress, payloadStr, req.Signature, req.PubKey); err != nil {
		jsonError(w, err.Error(), 401)
		return
	}

	vm := a.GetVoteManager()
	if vm == "" || vm != req.SignerAddress {
		jsonError(w, "signer is not the vote-manager", 403)
		return
	}

	// Decode the payload as a VotingConfig and apply it by upserting servers.
	var newConfig VotingConfig
	if err := json.Unmarshal(req.Payload, &newConfig); err != nil {
		jsonError(w, "invalid voting-config payload", 400)
		return
	}

	// Replace approved servers with the new list.
	// This is a destructive operation gated by vote-manager auth.
	for _, entry := range newConfig.VoteServers {
		if err := a.Store.UpsertApprovedServer(entry); err != nil {
			jsonError(w, "internal error", 500)
			h.logger.Error("upsert approved server", "error", err)
			return
		}
	}

	jsonResponse(w, map[string]string{"status": "ok"}, 200)
}

type registerBody struct {
	OperatorAddress string `json:"operator_address"`
	URL             string `json:"url"`
	Moniker         string `json:"moniker"`
	Timestamp       int64  `json:"timestamp"`
	Signature       string `json:"signature"`
	PubKey          string `json:"pub_key"`
}

func (h *apiHandler) handleRegisterValidator(w http.ResponseWriter, r *http.Request) {
	if r.Method == "OPTIONS" {
		corsHeaders(w)
		w.WriteHeader(204)
		return
	}
	a := h.admin()
	if a == nil {
		jsonError(w, "admin server not initialized", 503)
		return
	}

	var body registerBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, "invalid JSON body", 400)
		return
	}
	if body.OperatorAddress == "" || body.URL == "" || body.Moniker == "" || body.Timestamp == 0 || body.Signature == "" || body.PubKey == "" {
		jsonError(w, "missing required fields", 400)
		return
	}

	now := time.Now().Unix()
	if abs(now-body.Timestamp) > timestampWindowSecs {
		jsonError(w, "timestamp too far from server time (>5min)", 400)
		return
	}

	payloadStr, _ := json.Marshal(map[string]interface{}{
		"operator_address": body.OperatorAddress,
		"url":              body.URL,
		"moniker":          body.Moniker,
		"timestamp":        body.Timestamp,
	})
	if err := VerifyArbitrarySignature(body.OperatorAddress, string(payloadStr), body.Signature, body.PubKey); err != nil {
		jsonError(w, err.Error(), 401)
		return
	}

	// Already approved? Short-circuit.
	approved, err := a.Store.IsApproved(body.OperatorAddress)
	if err != nil {
		jsonError(w, "internal error", 500)
		return
	}
	if approved {
		jsonResponse(w, map[string]string{"status": "registered", "phase": "already-approved"}, 200)
		return
	}

	// Check on-chain bonding status.
	valoper, err := AddressToValoper(body.OperatorAddress)
	if err != nil {
		jsonError(w, "invalid operator address", 400)
		return
	}
	isBonded := a.CheckBonding(valoper)

	if isBonded {
		entry := ServiceEntry{URL: body.URL, Label: body.Moniker, OperatorAddress: body.OperatorAddress}
		if err := a.Store.UpsertApprovedServer(entry); err != nil {
			jsonError(w, "internal error", 500)
			return
		}
		_ = a.Store.CleanPendingByOperator(body.OperatorAddress, body.URL)
		_ = a.Store.UpsertPulse(body.URL, now)
		jsonResponse(w, map[string]string{"status": "registered", "phase": "bonded"}, 200)
		return
	}

	// Not bonded — add to pending queue.
	reg := PendingRegistration{
		OperatorAddress: body.OperatorAddress,
		URL:             body.URL,
		Moniker:         body.Moniker,
		Timestamp:       body.Timestamp,
		Signature:       body.Signature,
		PubKey:          body.PubKey,
		ExpiresAt:       now + pendingExpirySecs,
	}
	if err := a.Store.UpsertPendingRegistration(reg); err != nil {
		jsonError(w, "internal error", 500)
		return
	}
	jsonResponse(w, map[string]interface{}{"status": "pending", "phase": "unbonded", "expires_at": reg.ExpiresAt}, 200)
}

func (h *apiHandler) handleServerHeartbeat(w http.ResponseWriter, r *http.Request) {
	if r.Method == "OPTIONS" {
		corsHeaders(w)
		w.WriteHeader(204)
		return
	}
	a := h.admin()
	if a == nil {
		jsonError(w, "admin server not initialized", 503)
		return
	}

	var body registerBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, "invalid JSON body", 400)
		return
	}
	if body.OperatorAddress == "" || body.URL == "" || body.Moniker == "" || body.Timestamp == 0 || body.Signature == "" || body.PubKey == "" {
		jsonError(w, "missing required fields", 400)
		return
	}

	now := time.Now().Unix()
	if abs(now-body.Timestamp) > timestampWindowSecs {
		jsonError(w, "timestamp too far from server time (>5min)", 400)
		return
	}

	payloadStr, _ := json.Marshal(map[string]interface{}{
		"operator_address": body.OperatorAddress,
		"url":              body.URL,
		"moniker":          body.Moniker,
		"timestamp":        body.Timestamp,
	})
	if err := VerifyArbitrarySignature(body.OperatorAddress, string(payloadStr), body.Signature, body.PubKey); err != nil {
		jsonError(w, err.Error(), 401)
		return
	}

	approved, err := a.Store.IsApproved(body.OperatorAddress)
	if err != nil {
		jsonError(w, "internal error", 500)
		return
	}

	if !approved {
		// Not approved — add to pending queue (same behavior as register-validator for unknown servers).
		reg := PendingRegistration{
			OperatorAddress: body.OperatorAddress,
			URL:             body.URL,
			Moniker:         body.Moniker,
			Timestamp:       body.Timestamp,
			Signature:       body.Signature,
			PubKey:          body.PubKey,
			ExpiresAt:       now + pendingExpirySecs,
		}
		_ = a.Store.UpsertPendingRegistration(reg)
		jsonResponse(w, map[string]interface{}{"status": "pending", "expires_at": reg.ExpiresAt}, 200)
		return
	}

	// Approved — record pulse and update server entry.
	_ = a.Store.UpsertPulse(body.URL, now)
	_ = a.Store.UpsertApprovedServer(ServiceEntry{URL: body.URL, Label: body.Moniker, OperatorAddress: body.OperatorAddress})

	// Piggyback stale eviction.
	stale, _ := a.Store.EvictStalePulses(a.StaleThreshold)

	resp := map[string]interface{}{"status": "active"}
	if len(stale) > 0 {
		resp["evicted"] = stale
	}
	jsonResponse(w, resp, 200)
}

type approvePayload struct {
	Action          string `json:"action"`
	OperatorAddress string `json:"operator_address"`
}

func (h *apiHandler) handleApproveRegistration(w http.ResponseWriter, r *http.Request) {
	if r.Method == "OPTIONS" {
		corsHeaders(w)
		w.WriteHeader(204)
		return
	}
	a := h.admin()
	if a == nil {
		jsonError(w, "admin server not initialized", 503)
		return
	}

	var req struct {
		Payload       approvePayload `json:"payload"`
		Signature     string         `json:"signature"`
		PubKey        string         `json:"pubKey"`
		SignerAddress string         `json:"signerAddress"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "invalid JSON body", 400)
		return
	}
	if req.SignerAddress == "" || req.Signature == "" || req.PubKey == "" {
		jsonError(w, "missing required fields", 400)
		return
	}
	if (req.Payload.Action != "approve" && req.Payload.Action != "reject") || req.Payload.OperatorAddress == "" {
		jsonError(w, "invalid payload: expected { action: \"approve\"|\"reject\", operator_address }", 400)
		return
	}

	payloadStr, _ := json.Marshal(req.Payload)
	if err := VerifyArbitrarySignature(req.SignerAddress, string(payloadStr), req.Signature, req.PubKey); err != nil {
		jsonError(w, err.Error(), 401)
		return
	}

	if a.AdminAddress == "" || req.SignerAddress != a.AdminAddress {
		jsonError(w, "signer is not the admin", 403)
		return
	}

	entry, err := a.Store.RemovePendingRegistration(req.Payload.OperatorAddress)
	if err != nil {
		jsonError(w, "internal error", 500)
		return
	}
	if entry == nil {
		jsonError(w, "no pending registration found for "+req.Payload.OperatorAddress, 404)
		return
	}

	if req.Payload.Action == "reject" {
		jsonResponse(w, map[string]string{"status": "rejected", "operator_address": entry.OperatorAddress}, 200)
		return
	}

	// Approve: move to approved servers.
	svc := ServiceEntry{URL: entry.URL, Label: entry.Moniker, OperatorAddress: entry.OperatorAddress}
	if err := a.Store.UpsertApprovedServer(svc); err != nil {
		jsonError(w, "internal error", 500)
		return
	}

	jsonResponse(w, map[string]string{"status": "approved", "operator_address": entry.OperatorAddress, "url": entry.URL}, 200)
}

type removePayload struct {
	Action          string `json:"action"`
	OperatorAddress string `json:"operator_address"`
}

func (h *apiHandler) handleRemoveApprovedServer(w http.ResponseWriter, r *http.Request) {
	if r.Method == "OPTIONS" {
		corsHeaders(w)
		w.WriteHeader(204)
		return
	}
	a := h.admin()
	if a == nil {
		jsonError(w, "admin server not initialized", 503)
		return
	}

	var req struct {
		Payload       removePayload `json:"payload"`
		Signature     string        `json:"signature"`
		PubKey        string        `json:"pubKey"`
		SignerAddress string        `json:"signerAddress"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "invalid JSON body", 400)
		return
	}
	if req.SignerAddress == "" || req.Signature == "" || req.PubKey == "" {
		jsonError(w, "missing required fields", 400)
		return
	}
	if req.Payload.Action != "remove-approved" || req.Payload.OperatorAddress == "" {
		jsonError(w, "invalid payload", 400)
		return
	}

	payloadStr, _ := json.Marshal(req.Payload)
	if err := VerifyArbitrarySignature(req.SignerAddress, string(payloadStr), req.Signature, req.PubKey); err != nil {
		jsonError(w, err.Error(), 401)
		return
	}

	vm := a.GetVoteManager()
	if vm == "" || vm != req.SignerAddress {
		jsonError(w, "signer is not the vote-manager", 403)
		return
	}

	removedURL, err := a.Store.RemoveApprovedServer(req.Payload.OperatorAddress)
	if err != nil {
		jsonError(w, "internal error", 500)
		return
	}
	if removedURL == "" {
		jsonError(w, "no approved server found for "+req.Payload.OperatorAddress, 404)
		return
	}

	_ = a.Store.RemovePulse(removedURL)

	jsonResponse(w, map[string]string{"status": "removed", "operator_address": req.Payload.OperatorAddress, "url": removedURL}, 200)
}

func abs(x int64) int64 {
	if x < 0 {
		return -x
	}
	return x
}
