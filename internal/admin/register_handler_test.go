package admin

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"cosmossdk.io/log"
	"github.com/gorilla/mux"
)

func TestHandleRegisterValidatorBadJSON(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	st, err := NewStore(filepath.Join(dir, "h.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	a := &Admin{
		configURL:   "http://invalid.local",
		logger:      log.NewNopLogger(),
		store:       st,
		checkBonded: func(string) bool { return false },
	}

	r := mux.NewRouter()
	RegisterRoutes(r, func() *Admin { return a }, log.NewNopLogger())

	req := httptest.NewRequest(http.MethodPost, "/api/register-validator", bytes.NewBufferString("not-json"))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestHandleRegisterValidatorAdminNil(t *testing.T) {
	t.Parallel()
	r := mux.NewRouter()
	RegisterRoutes(r, func() *Admin { return nil }, log.NewNopLogger())

	req := httptest.NewRequest(http.MethodPost, "/api/register-validator", bytes.NewBufferString(`{}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503, got %d", w.Code)
	}
}
