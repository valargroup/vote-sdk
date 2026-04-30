package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"time"
)

const timestampWindowSecs = 300 // 5 minutes

type registerBody struct {
	OperatorAddress string `json:"operator_address"`
	URL             string `json:"url"`
	Moniker         string `json:"moniker"`
	Timestamp       int64  `json:"timestamp"`
	Signature       string `json:"signature"`
	PubKey          string `json:"pub_key"`
}

type registerPayloadWire struct {
	OperatorAddress string `json:"operator_address"`
	URL             string `json:"url"`
	Moniker         string `json:"moniker"`
	Timestamp       int64  `json:"timestamp"`
}

func marshalRegisterPayloadWire(payload registerPayloadWire) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(payload); err != nil {
		return nil, err
	}
	return bytes.TrimSpace(buf.Bytes()), nil
}

// PendingValidatorPublic is returned by GET /api/pending-validators (no secrets).
type PendingValidatorPublic struct {
	OperatorAddress string `json:"operator_address"`
	URL             string `json:"url"`
	Moniker         string `json:"moniker"`
	RequestedAt     int64  `json:"requested_at"`
	ExpiresAt       int64  `json:"expires_at"`
}

func abs64(n int64) int64 {
	if n < 0 {
		return -n
	}
	return n
}

func (h *apiHandler) handleRegisterValidator(w http.ResponseWriter, r *http.Request) {
	if r.Method == "OPTIONS" {
		corsHeaders(w)
		w.WriteHeader(204)
		return
	}
	a := h.getAdmin()
	if a == nil || a.Store() == nil {
		jsonError(w, "admin server not initialized", 503)
		return
	}

	var body registerBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, "invalid JSON body", 400)
		return
	}
	if body.OperatorAddress == "" || body.Moniker == "" || body.Timestamp == 0 || body.Signature == "" || body.PubKey == "" {
		jsonError(w, "missing required fields", 400)
		return
	}

	now := time.Now().Unix()
	if abs64(now-body.Timestamp) > timestampWindowSecs {
		jsonError(w, "timestamp too far from server time (>5min)", 400)
		return
	}

	payloadBytes, err := marshalRegisterPayloadWire(registerPayloadWire{
		OperatorAddress: body.OperatorAddress,
		URL:             body.URL,
		Moniker:         body.Moniker,
		Timestamp:       body.Timestamp,
	})
	if err != nil {
		jsonError(w, "internal error", 500)
		return
	}
	if err := VerifyArbitrarySignature(body.OperatorAddress, string(payloadBytes), body.Signature, body.PubKey); err != nil {
		jsonError(w, err.Error(), 401)
		return
	}

	if a.ValidatorExists(body.OperatorAddress) {
		_, _ = a.Store().RemovePendingRegistration(body.OperatorAddress)
		jsonResponse(w, map[string]string{"status": "registered"}, 200)
		return
	}

	expiresAt := now + int64(PendingRegistrationTTL.Seconds())
	reg := PendingRegistration{
		OperatorAddress: body.OperatorAddress,
		URL:             body.URL,
		Moniker:         body.Moniker,
		RequestedAt:     now,
		ExpiresAt:       expiresAt,
	}
	if err := a.Store().UpsertPendingRegistration(reg); err != nil {
		jsonError(w, "internal error", 500)
		h.logger.Error("upsert pending registration", "error", err)
		return
	}

	jsonResponse(w, map[string]interface{}{
		"status":     "pending",
		"expires_at": expiresAt,
	}, 200)
}

func (h *apiHandler) handleGetPendingValidators(w http.ResponseWriter, r *http.Request) {
	if r.Method == "OPTIONS" {
		corsHeaders(w)
		w.WriteHeader(204)
		return
	}
	a := h.getAdmin()
	if a == nil || a.Store() == nil {
		jsonError(w, "admin server not initialized", 503)
		return
	}

	regs, err := a.Store().ListPendingRegistrations()
	if err != nil {
		jsonError(w, "internal error", 500)
		h.logger.Error("list pending registrations", "error", err)
		return
	}

	out := make([]PendingValidatorPublic, 0, len(regs))
	for _, r := range regs {
		if a.ValidatorExists(r.OperatorAddress) {
			if _, err := a.Store().RemovePendingRegistration(r.OperatorAddress); err != nil {
				h.logger.Error("remove registered pending registration", "operator", r.OperatorAddress, "error", err)
			}
			continue
		}
		out = append(out, PendingValidatorPublic{
			OperatorAddress: r.OperatorAddress,
			URL:             r.URL,
			Moniker:         r.Moniker,
			RequestedAt:     r.RequestedAt,
			ExpiresAt:       r.ExpiresAt,
		})
	}
	jsonResponse(w, out, 200)
}

func jsonError(w http.ResponseWriter, msg string, status int) {
	jsonResponse(w, map[string]string{"error": msg}, status)
}

func jsonResponse(w http.ResponseWriter, body interface{}, status int) {
	corsHeaders(w)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
