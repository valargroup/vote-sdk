package admin

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cosmossdk.io/log"
	"github.com/gorilla/mux"
)

func TestHandleSignConfigEntry(t *testing.T) {
	t.Parallel()

	eaPK := make([]byte, 32)
	for i := range eaPK {
		eaPK[i] = byte(i)
	}

	r := mux.NewRouter()
	RegisterRoutes(r, func() *Admin { return nil }, log.NewNopLogger())

	body := `{"round_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","ea_pk":"` +
		base64.StdEncoding.EncodeToString(eaPK) +
		`","auth_version":2,"pir_layout":{"pir_depth":19,"tier0_layers":12,"tier1_layers":7,"poly_len":4096}}`
	req := httptest.NewRequest(http.MethodPost, "/api/sign-config-entry", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Fatalf("missing CORS header: %q", got)
	}

	var resp signConfigEntryResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.AuthVersion != 2 {
		t.Fatalf("auth_version mismatch: got %d", resp.AuthVersion)
	}
	// Golden vector for the v2 preimage:
	// "zcash-shielded-vote:round-auth:v2" || 0xaa*32 || 0x00..0x1f
	// || u32le(19) || u32le(12) || u32le(7) || u32le(4096).
	// Shared with wallet-side (librustvoting) verification.
	const wantPayload = "emNhc2gtc2hpZWxkZWQtdm90ZTpyb3VuZC1hdXRoOnYyqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqoAAQIDBAUGBwgJCgsMDQ4PEBESExQVFhcYGRobHB0eHxMAAAAMAAAABwAAAAAQAAA="
	if resp.CanonicalPayloadB64 != wantPayload {
		t.Fatalf("canonical payload mismatch: got %s", resp.CanonicalPayloadB64)
	}
	const wantHash = "7f5f1cc0eee15cb12b729ef23c00423a08f76780a406b9b582196de0a83d8ade"
	if resp.SignedPayloadHash != wantHash {
		t.Fatalf("hash mismatch: got %s want %s", resp.SignedPayloadHash, wantHash)
	}
}

func TestHandleSignConfigEntryOptions(t *testing.T) {
	t.Parallel()

	r := mux.NewRouter()
	RegisterRoutes(r, func() *Admin { return nil }, log.NewNopLogger())

	req := httptest.NewRequest(http.MethodOptions, "/api/sign-config-entry", nil)
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("want 204, got %d", w.Code)
	}
	if got := w.Header().Get("Access-Control-Allow-Methods"); !strings.Contains(got, "POST") {
		t.Fatalf("allow methods missing POST: %q", got)
	}
}

func TestHandleSignConfigEntryRejectsBadInputs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
	}{
		{name: "bad json", body: "not-json"},
		{name: "legacy v1 version", body: `{"round_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","ea_pk":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=","auth_version":1}`},
		{name: "unknown version", body: `{"round_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","ea_pk":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=","auth_version":3}`},
		{name: "uppercase round id", body: `{"round_id":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA","ea_pk":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=","auth_version":2}`},
		{name: "short round id", body: `{"round_id":"aa","ea_pk":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=","auth_version":2}`},
		{name: "bad ea pk base64", body: `{"round_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","ea_pk":"not-base64","auth_version":2}`},
		{name: "short ea pk", body: `{"round_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","ea_pk":"AQID","auth_version":2}`},
		{name: "missing pir layout", body: `{"round_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","ea_pk":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=","auth_version":2}`},
		{name: "inconsistent pir layout", body: `{"round_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","ea_pk":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=","auth_version":2,"pir_layout":{"pir_depth":19,"tier0_layers":12,"tier1_layers":8,"poly_len":4096}}`},
		{name: "missing poly_len", body: `{"round_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","ea_pk":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=","auth_version":2,"pir_layout":{"pir_depth":19,"tier0_layers":12,"tier1_layers":7}}`},
		{name: "invalid poly_len", body: `{"round_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","ea_pk":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=","auth_version":2,"pir_layout":{"pir_depth":19,"tier0_layers":12,"tier1_layers":7,"poly_len":1024}}`},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := mux.NewRouter()
			RegisterRoutes(r, func() *Admin { return nil }, log.NewNopLogger())

			req := httptest.NewRequest(http.MethodPost, "/api/sign-config-entry", bytes.NewBufferString(tt.body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()

			r.ServeHTTP(w, req)

			if w.Code != http.StatusBadRequest {
				t.Fatalf("want 400, got %d body=%s", w.Code, w.Body.String())
			}
		})
	}
}
