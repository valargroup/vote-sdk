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
		base64.StdEncoding.EncodeToString(eaPK) + `","auth_version":1}`
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
	if resp.AuthVersion != 1 {
		t.Fatalf("auth_version mismatch: got %d", resp.AuthVersion)
	}
	if resp.CanonicalPayloadB64 != base64.StdEncoding.EncodeToString(eaPK) {
		t.Fatalf("canonical payload mismatch: got %s", resp.CanonicalPayloadB64)
	}
	const wantHash = "630dcd2966c4336691125448bbb25b4ff412a49c732db2c8abc1b8581bd710dd"
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
		{name: "unknown version", body: `{"round_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","ea_pk":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=","auth_version":2}`},
		{name: "uppercase round id", body: `{"round_id":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA","ea_pk":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=","auth_version":1}`},
		{name: "short round id", body: `{"round_id":"aa","ea_pk":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=","auth_version":1}`},
		{name: "bad ea pk base64", body: `{"round_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","ea_pk":"not-base64","auth_version":1}`},
		{name: "short ea pk", body: `{"round_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","ea_pk":"AQID","auth_version":1}`},
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
