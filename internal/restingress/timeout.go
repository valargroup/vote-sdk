package restingress

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
)

// WriteTimeout emits attempt-bound non-dispatch evidence only for a synchronous
// original request-body read timeout. Callers must return without broadcasting,
// enqueueing, or scheduling work. It says nothing about earlier attempts.
// The token opts into v1 and binds the receipt to one HTTP attempt, not a vote.
// Unknown clients retain the existing generic error response.
func WriteTimeout(w http.ResponseWriter, r *http.Request, err error) bool {
	var timeout net.Error
	if !errors.As(err, &timeout) || !timeout.Timeout() {
		return false
	}
	tokens := r.Header.Values("X-Vote-Ingress-Attempt-V1")
	if len(tokens) != 1 || len(tokens[0]) != 64 {
		return false
	}
	token := tokens[0]
	decoded, decodeErr := hex.DecodeString(token)
	if decodeErr != nil || hex.EncodeToString(decoded) != token {
		return false
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusRequestTimeout)
	_ = json.NewEncoder(w).Encode(struct {
		Error ingressTimeoutReceipt `json:"error"`
	}{Error: ingressTimeoutReceipt{Version: 1, Code: "request_body_timeout", Dispatch: "not_started", Attempt: token}})
	return true
}

type ingressTimeoutReceipt struct {
	Version  int    `json:"version"`
	Code     string `json:"code"`
	Dispatch string `json:"dispatch"`
	Attempt  string `json:"attempt"`
}
