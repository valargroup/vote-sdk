package api

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/require"
	protov2 "google.golang.org/protobuf/proto"

	"github.com/valargroup/vote-sdk/x/vote/types"
)

// These tests pin the bodies deployed clients decode with deny_unknown_fields:
// without X-Svote-Codespace: 1 the existing endpoints answer exactly as
// before, and with it they add codespace (docs/proxy-delegation/contracts-v1.md
// §0).

// handlerTestDelegation returns a MsgDelegateVote that passes ValidateBasic,
// bound to the TX1 effects fixture. It reads the fixture by path because
// testutil imports this package through app.
func handlerTestDelegation(t *testing.T, roundID []byte) *types.MsgDelegateVote {
	t.Helper()
	raw, err := os.ReadFile("../testutil/testdata/delegation_tx1_effects_v1.json")
	require.NoError(t, err)
	var fixture struct {
		Rk                  []byte `json:"rk"`
		SignedNoteNullifier []byte `json:"signed_note_nullifier"`
		CmxNew              []byte `json:"cmx_new"`
		TX1Effects          []byte `json:"tx1_effects"`
	}
	require.NoError(t, json.Unmarshal(raw, &fixture))
	return &types.MsgDelegateVote{
		Rk:                  fixture.Rk,
		SpendAuthSig:        bytes.Repeat([]byte{0x02}, 64),
		SignedNoteNullifier: fixture.SignedNoteNullifier,
		CmxNew:              fixture.CmxNew,
		VanCmx:              bytes.Repeat([]byte{0x04}, 32),
		GovNullifiers:       [][]byte{bytes.Repeat([]byte{0x05}, 32)},
		Proof:               []byte{0x06},
		VoteRoundId:         roundID,
		Tx1Effects:          fixture.TX1Effects,
	}
}

// submissionEndpoint is one existing submission route with a body that passes
// its ValidateBasic, and the batch_digest its answers carry, if any.
type submissionEndpoint struct {
	path   string
	msg    protov2.Message
	digest string
}

// existingSubmissionEndpoints returns every submission endpoint that predates
// proxy delegation.
func existingSubmissionEndpoints(t *testing.T) []submissionEndpoint {
	t.Helper()
	batch := validHandlerBatch()
	roundID := batch.GetVoteRoundId()
	composite := &types.MsgDelegateAndCastVoteBatch{
		Delegation: handlerTestDelegation(t, roundID),
		Batch:      validHandlerBatch(),
	}
	for _, vote := range composite.Batch.Votes {
		vote.VoteCommTreeAnchorHeight = 0
	}
	reveal := &types.MsgRevealShare{
		ShareNullifier:           bytes.Repeat([]byte{0x07}, 32),
		EncShare:                 bytes.Repeat([]byte{0x08}, 64),
		ProposalId:               1,
		VoteDecision:             1,
		Proof:                    []byte{0x09},
		VoteRoundId:              roundID,
		VoteCommTreeAnchorHeight: 88,
	}
	return []submissionEndpoint{
		{path: "/shielded-vote/v1/delegate-vote", msg: handlerTestDelegation(t, roundID)},
		{path: "/shielded-vote/v1/cast-vote", msg: validHandlerBatch().Votes[0]},
		{path: "/shielded-vote/v1/cast-vote-batch", msg: batch, digest: hex.EncodeToString(types.ComputeCastVoteBatchSighash(batch))},
		{path: "/shielded-vote/v1/delegate-and-cast-vote-batch", msg: composite, digest: hex.EncodeToString(types.ComputeDelegateAndCastVoteBatchSighash(composite))},
		{path: "/shielded-vote/v1/reveal-share", msg: reveal},
	}
}

// cometBroadcastStub answers every broadcast_tx_sync with result, a CometBFT
// ResultBroadcastTx JSON object.
func cometBroadcastStub(t *testing.T, result string) *httptest.Server {
	t.Helper()
	comet := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":` + result + `}`))
	}))
	t.Cleanup(comet.Close)
	return comet
}

// postJSON posts msg as JSON to path on router, with X-Svote-Codespace: 1 when
// withHeader is set.
func postJSON(t *testing.T, router http.Handler, path string, msg any, withHeader bool) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(msg)
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	if withHeader {
		req.Header.Set(codespaceHeader, "1")
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

// bodyKeys returns the sorted top-level keys of a JSON object.
func bodyKeys(t *testing.T, body []byte) []string {
	t.Helper()
	var object map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(body, &object), string(body))
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// TestExistingSubmissionBodiesWithoutCodespaceHeader pins today's exact bodies
// on every existing submission endpoint, accepted and rejected.
func TestExistingSubmissionBodiesWithoutCodespaceHeader(t *testing.T) {
	for _, c := range []struct {
		name   string
		result string
		status int
		body   string
	}{
		{"accepted", `{"code":0,"data":"","log":"","codespace":"","hash":"ABC123"}`, http.StatusOK, `{"tx_hash":"ABC123","code":0`},
		{"rejected", `{"code":2,"data":"","log":"nullifier already spent","codespace":"vote","hash":"ABC123"}`, http.StatusUnprocessableEntity, `{"tx_hash":"ABC123","code":2,"log":"nullifier already spent"`},
	} {
		comet := cometBroadcastStub(t, c.result)
		router := mux.NewRouter()
		NewHandler(HandlerConfig{CometRPCEndpoint: comet.URL}).RegisterTxRoutes(router)
		for _, endpoint := range existingSubmissionEndpoints(t) {
			t.Run(c.name+endpoint.path, func(t *testing.T) {
				for _, value := range []string{"", "0", "true"} {
					body, err := json.Marshal(endpoint.msg)
					require.NoError(t, err)
					req := httptest.NewRequest(http.MethodPost, endpoint.path, bytes.NewReader(body))
					if value != "" {
						req.Header.Set(codespaceHeader, value)
					}
					rec := httptest.NewRecorder()
					router.ServeHTTP(rec, req)
					require.Equal(t, c.status, rec.Code, rec.Body.String())

					want := c.body
					if endpoint.digest != "" {
						want += `,"batch_digest":"` + endpoint.digest + `"`
					}
					require.Equal(t, want+"}\n", rec.Body.String(), "header %q", value)
				}
			})
		}
	}
}

// TestExistingSubmissionBodiesWithCodespaceHeader checks that opting in adds
// codespace, and only codespace, to the same bodies.
func TestExistingSubmissionBodiesWithCodespaceHeader(t *testing.T) {
	for _, c := range []struct {
		name      string
		result    string
		status    int
		keys      []string
		codespace string
	}{
		{"accepted", `{"code":0,"data":"","log":"","codespace":"","hash":"ABC123"}`, http.StatusOK, []string{"code", "codespace", "tx_hash"}, ""},
		{"rejected", `{"code":2,"data":"","log":"nullifier already spent","codespace":"vote","hash":"ABC123"}`, http.StatusUnprocessableEntity, []string{"code", "codespace", "log", "tx_hash"}, types.ModuleName},
	} {
		comet := cometBroadcastStub(t, c.result)
		router := mux.NewRouter()
		NewHandler(HandlerConfig{CometRPCEndpoint: comet.URL}).RegisterTxRoutes(router)
		for _, endpoint := range existingSubmissionEndpoints(t) {
			t.Run(c.name+endpoint.path, func(t *testing.T) {
				rec := postJSON(t, router, endpoint.path, endpoint.msg, true)
				require.Equal(t, c.status, rec.Code, rec.Body.String())

				want := append([]string(nil), c.keys...)
				if endpoint.digest != "" {
					want = append(want, "batch_digest")
					sort.Strings(want)
				}
				require.Equal(t, want, bodyKeys(t, rec.Body.Bytes()))
				var result BroadcastResult
				require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &result))
				require.Equal(t, c.codespace, result.Codespace)
				require.Equal(t, endpoint.digest, result.BatchDigest)
			})
		}
	}
}

// TestTxStatusCodespaceHeader pins /tx/{hash}'s keys without the header and
// checks that the header adds the result's codespace.
func TestTxStatusCodespaceHeader(t *testing.T) {
	comet := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"height":"42","tx_result":{"code":2,"codespace":"vote","log":"nullifier already spent","events":[]}}}`))
	}))
	defer comet.Close()
	router := mux.NewRouter()
	NewHandler(HandlerConfig{CometRPCEndpoint: comet.URL}).RegisterTxRoutes(router)

	path := "/shielded-vote/v1/tx/" + strings.Repeat("AB", 32)
	legacy := httptest.NewRecorder()
	router.ServeHTTP(legacy, httptest.NewRequest(http.MethodGet, path, nil))
	require.Equal(t, http.StatusUnprocessableEntity, legacy.Code)
	require.Equal(t, `{"code":2,"events":[],"height":"42","log":"nullifier already spent"}`+"\n", legacy.Body.String())

	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set(codespaceHeader, "1")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	require.Equal(t, `{"code":2,"codespace":"vote","events":[],"height":"42","log":"nullifier already spent"}`+"\n", rec.Body.String())
}

// TestBroadcastResultFromTxStatusKeepsCodespace checks that a result rebuilt
// from /tx after an ambiguous or duplicate broadcast keeps its codespace.
func TestBroadcastResultFromTxStatusKeepsCodespace(t *testing.T) {
	result := broadcastResultFromTxStatus("ABC", &txStatusResult{Code: 2, Codespace: types.ModuleName, Log: "spent"})
	require.Equal(t, &BroadcastResult{TxHash: "ABC", Code: 2, Codespace: types.ModuleName, Log: "spent"}, result)
}
