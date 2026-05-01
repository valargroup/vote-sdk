package admin

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"

	"github.com/valargroup/vote-sdk/internal/votingconfig"
)

type signConfigEntryRequest struct {
	RoundID     string `json:"round_id"`
	EaPK        string `json:"ea_pk"`
	AuthVersion int    `json:"auth_version"`
}

type signConfigEntryResponse struct {
	CanonicalPayloadB64 string `json:"canonical_payload_b64"`
	SignedPayloadHash   string `json:"signed_payload_hash"`
	AuthVersion         int    `json:"auth_version"`
}

func (h *apiHandler) handleSignConfigEntry(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		corsHeaders(w)
		w.WriteHeader(http.StatusNoContent)
		return
	}

	var body signConfigEntryRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if body.AuthVersion != votingconfig.AuthVersionV1 {
		jsonError(w, "unsupported auth_version", http.StatusBadRequest)
		return
	}
	if err := votingconfig.ValidateRoundID(body.RoundID); err != nil {
		jsonError(w, "round_id must be 64 lowercase hex characters", http.StatusBadRequest)
		return
	}

	eaPKBytes, err := votingconfig.DecodeBase64Fixed(body.EaPK, 32, "ea_pk")
	if err != nil {
		jsonError(w, "ea_pk must be base64-encoded 32 bytes", http.StatusBadRequest)
		return
	}
	var eaPK [32]byte
	copy(eaPK[:], eaPKBytes)

	payload := votingconfig.CanonicalPayloadV1(eaPK)
	hash := votingconfig.SignedPayloadHash(payload)
	jsonResponse(w, signConfigEntryResponse{
		CanonicalPayloadB64: base64.StdEncoding.EncodeToString(payload),
		SignedPayloadHash:   hex.EncodeToString(hash[:]),
		AuthVersion:         votingconfig.AuthVersionV1,
	}, http.StatusOK)
}
