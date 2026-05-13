package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gorilla/mux"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/valargroup/vote-sdk/x/vote/types"
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

func TestWriteProtoJSONUsesSnakeCaseNumericUint64(t *testing.T) {
	rec := httptest.NewRecorder()
	root := bytes.Repeat([]byte{0xAB}, 32)

	writeProtoJSON(rec, &types.QueryCommitmentLeavesResponse{
		Blocks: []*types.BlockCommitments{{
			Height:     5,
			StartIndex: 0,
			Leaves:     [][]byte{bytes.Repeat([]byte{0x01}, 32)},
			Root:       root,
		}},
		NextFromHeight: 12,
	})

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var body map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if _, ok := body["nextFromHeight"]; ok {
		t.Fatal("unexpected camelCase nextFromHeight field")
	}
	if got := body["next_from_height"]; got != float64(12) {
		t.Fatalf("expected numeric next_from_height 12, got %#v", got)
	}
	blocks, ok := body["blocks"].([]interface{})
	if !ok || len(blocks) != 1 {
		t.Fatalf("expected one block, got %#v", body["blocks"])
	}
	block, ok := blocks[0].(map[string]interface{})
	if !ok {
		t.Fatalf("expected block object, got %#v", blocks[0])
	}
	if got, ok := block["root"].(string); !ok || got == "" {
		t.Fatalf("expected base64 root string, got %#v", block["root"])
	}
}

func TestCommitmentLeavesRejectsInvalidHeightParams(t *testing.T) {
	roundID := bytes.Repeat([]byte{0xAA}, types.RoundIDLen)
	tests := []struct {
		name string
		path string
	}{
		{
			name: "invalid from_height",
			path: "/shielded-vote/v1/commitment-tree/round/leaves?from_height=bad",
		},
		{
			name: "invalid to_height",
			path: "/shielded-vote/v1/commitment-tree/round/leaves?to_height=bad",
		},
		{
			name: "to before from",
			path: "/shielded-vote/v1/commitment-tree/round/leaves?from_height=10&to_height=5",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			req = mux.SetURLVars(req, map[string]string{"round_id": fmt.Sprintf("%x", roundID)})
			rec := httptest.NewRecorder()

			(&queryHandler{}).handleCommitmentLeaves(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d", rec.Code)
			}
		})
	}
}
