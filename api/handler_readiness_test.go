package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gorilla/mux"
)

func TestReadinessEndpointReportsWarming(t *testing.T) {
	handler := NewHandler(HandlerConfig{
		CryptoReadiness: func() CryptoReadinessStatus {
			return CryptoReadinessStatus{Status: CryptoReadinessStatusWarming}
		},
	})
	router := mux.NewRouter()
	handler.RegisterTxRoutes(router)

	req := httptest.NewRequest(http.MethodGet, "/shielded-vote/v1/readiness", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec.Code)
	}
	if got := rec.Header().Get("Retry-After"); got != "2" {
		t.Fatalf("expected Retry-After 2, got %q", got)
	}
}

func TestReadinessEndpointReportsReady(t *testing.T) {
	handler := NewHandler(HandlerConfig{
		CryptoReadiness: func() CryptoReadinessStatus {
			return CryptoReadinessStatus{Status: CryptoReadinessStatusReady}
		},
	})
	router := mux.NewRouter()
	handler.RegisterTxRoutes(router)

	req := httptest.NewRequest(http.MethodGet, "/shielded-vote/v1/readiness", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestSubmitRoutesFailClosedWhileCryptoWarms(t *testing.T) {
	handler := NewHandler(HandlerConfig{
		CryptoReadiness: func() CryptoReadinessStatus {
			return CryptoReadinessStatus{Status: CryptoReadinessStatusWarming}
		},
	})
	router := mux.NewRouter()
	handler.RegisterTxRoutes(router)

	req := httptest.NewRequest(http.MethodPost, "/shielded-vote/v1/delegate-vote", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec.Code)
	}
	if got := rec.Header().Get("Retry-After"); got != "2" {
		t.Fatalf("expected Retry-After 2, got %q", got)
	}
}

func TestSubmitRoutesProceedWhenCryptoReady(t *testing.T) {
	handler := NewHandler(HandlerConfig{
		CryptoReadiness: func() CryptoReadinessStatus {
			return CryptoReadinessStatus{Status: CryptoReadinessStatusReady}
		},
	})
	router := mux.NewRouter()
	handler.RegisterTxRoutes(router)

	req := httptest.NewRequest(http.MethodPost, "/shielded-vote/v1/delegate-vote", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code == http.StatusServiceUnavailable {
		t.Fatalf("expected request to pass readiness gate")
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected empty ready request to reach body validation and return 400, got %d", rec.Code)
	}
}
