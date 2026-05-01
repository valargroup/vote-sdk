package admin

import (
	"crypto/ed25519"
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

type verifyConfigEntryRequest struct {
	RoundID     string                    `json:"round_id"`
	EaPK        string                    `json:"ea_pk"`
	Signature   string                    `json:"signature"`
	AuthVersion int                       `json:"auth_version"`
	TrustedKeys []votingconfig.TrustedKey `json:"trusted_keys,omitempty"`
}

type verifyConfigEntryResponse struct {
	OK                bool   `json:"ok"`
	KeyID             string `json:"key_id,omitempty"`
	SignedPayloadHash string `json:"signed_payload_hash"`
	AuthVersion       int    `json:"auth_version"`
	Error             string `json:"error,omitempty"`
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

func (h *apiHandler) handleVerifyConfigEntry(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		corsHeaders(w)
		w.WriteHeader(http.StatusNoContent)
		return
	}

	var body verifyConfigEntryRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if body.AuthVersion == 0 {
		body.AuthVersion = votingconfig.AuthVersionV1
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

	sig, err := votingconfig.DecodeBase64Fixed(body.Signature, ed25519.SignatureSize, "signature")
	if err != nil {
		jsonError(w, "signature must be base64-encoded 64 bytes", http.StatusBadRequest)
		return
	}

	trustedKeys := body.TrustedKeys
	if len(trustedKeys) == 0 {
		a := h.getAdmin()
		if a == nil {
			jsonError(w, "admin server not initialized", http.StatusServiceUnavailable)
			return
		}
		trustedKeys, err = a.GetTrustedKeys()
		if err != nil {
			jsonError(w, err.Error(), http.StatusBadGateway)
			h.logger.Error("get trusted keys", "error", err)
			return
		}
	}

	hash := votingconfig.SignedPayloadHash(votingconfig.CanonicalPayloadV1(eaPK))
	resp := verifyConfigEntryResponse{
		OK:                false,
		SignedPayloadHash: hex.EncodeToString(hash[:]),
		AuthVersion:       votingconfig.AuthVersionV1,
	}
	for _, key := range trustedKeys {
		if key.Alg != votingconfig.AlgEd25519 {
			continue
		}
		pub, err := votingconfig.DecodeBase64Fixed(key.Pubkey, ed25519.PublicKeySize, "trusted_keys.pubkey")
		if err != nil {
			continue
		}
		if votingconfig.VerifyV1(ed25519.PublicKey(pub), eaPK, sig) {
			resp.OK = true
			resp.KeyID = key.KeyID
			break
		}
	}
	if !resp.OK {
		resp.Error = "signature did not verify against trusted keys"
	}
	jsonResponse(w, resp, http.StatusOK)
}
