package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCometBroadcastTxSyncWithRetryReturnsErrorWhenOutcomeRemainsUnknown(t *testing.T) {
	comet := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("{"))
	}))
	defer comet.Close()

	handler := NewHandler(HandlerConfig{CometRPCEndpoint: comet.URL})
	txBytes := []byte("ambiguous transaction")

	result, err := handler.cometBroadcastTxSyncWithRetry(context.Background(), txBytes, "test")
	if err == nil {
		t.Fatal("expected unknown broadcast outcome to return an error")
	}
	if result != nil {
		t.Fatalf("expected no successful result, got %+v", result)
	}
	if !strings.Contains(err.Error(), "broadcast outcome unknown after retries") {
		t.Fatalf("expected unknown-outcome error, got %v", err)
	}
	if !strings.Contains(err.Error(), txHashHex(txBytes)) {
		t.Fatalf("expected error to contain transaction hash, got %v", err)
	}
}
