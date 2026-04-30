package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestWriteQueryErrorMapsNotFoundToHTTP404(t *testing.T) {
	rec := httptest.NewRecorder()

	writeQueryError(rec, status.Error(codes.NotFound, "no active voting round"))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestABCIQueryErrorPreservesGRPCNotFound(t *testing.T) {
	rec := httptest.NewRecorder()
	err := abciQueryError(5, "rpc error: code = NotFound desc = no active voting round")

	writeQueryError(rec, err)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestActiveRoundNotFoundResponseIsSuccessfulNullRound(t *testing.T) {
	rec := httptest.NewRecorder()

	writeJSON(rec, http.StatusOK, map[string]interface{}{"round": nil})

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var body map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if _, ok := body["round"]; !ok {
		t.Fatal("expected round key in response")
	}
	if body["round"] != nil {
		t.Fatalf("expected null round, got %#v", body["round"])
	}
}

func TestWriteQueryErrorMapsInvalidArgumentToHTTP400(t *testing.T) {
	rec := httptest.NewRecorder()

	writeQueryError(rec, status.Error(codes.InvalidArgument, "bad query"))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestWriteQueryErrorKeepsUnhandledErrorsAsHTTP500(t *testing.T) {
	rec := httptest.NewRecorder()

	writeQueryError(rec, status.Error(codes.Internal, "store failed"))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rec.Code)
	}
}
