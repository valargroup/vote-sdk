package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/mux"
)

func TestTxStatusPreservesCometEventAttributes(t *testing.T) {
	// Both values are valid Base64 whose decoded bytes are valid UTF-8.
	roundID := strings.Repeat("0400", 16)
	comet := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      1,
			"result": map[string]interface{}{
				"height": "42",
				"tx_result": map[string]interface{}{
					"code": 0,
					"log":  "",
					"events": []abciEvent{{
						Type: "delegate_vote",
						Attributes: []abciAttribute{
							{Key: "vote_round_id", Value: roundID, Index: true},
							{Key: "leaf_index", Value: "3753", Index: true},
						},
					}},
				},
			},
		})
	}))
	defer comet.Close()

	handler := NewHandler(HandlerConfig{CometRPCEndpoint: comet.URL})
	router := mux.NewRouter()
	handler.RegisterTxRoutes(router)

	txHash := strings.Repeat("00", 32)
	req := httptest.NewRequest(http.MethodGet, "/shielded-vote/v1/tx/"+txHash, nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var response struct {
		Events []abciEvent `json:"events"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(response.Events) != 1 || len(response.Events[0].Attributes) != 2 {
		t.Fatalf("unexpected events: %+v", response.Events)
	}
	if got := response.Events[0].Attributes[0].Value; got != roundID {
		t.Fatalf("expected round ID %q, got %q", roundID, got)
	}
	if got := response.Events[0].Attributes[1].Value; got != "3753" {
		t.Fatalf("expected leaf index %q, got %q", "3753", got)
	}
}
