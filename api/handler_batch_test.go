package api

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/require"

	"github.com/valargroup/vote-sdk/x/vote/types"
)

func validHandlerBatch() *types.MsgCastVoteBatch {
	roundID := bytes.Repeat([]byte{0x91}, types.RoundIDLen)
	votes := make([]*types.MsgCastVote, 2)
	for i := range votes {
		fill := byte(i + 1)
		votes[i] = &types.MsgCastVote{
			VanNullifier:             bytes.Repeat([]byte{fill}, 32),
			VoteAuthorityNoteNew:     bytes.Repeat([]byte{fill + 2}, 32),
			VoteCommitment:           bytes.Repeat([]byte{fill + 4}, 32),
			ProposalId:               uint32(i + 1),
			Proof:                    []byte{fill},
			VoteRoundId:              append([]byte(nil), roundID...),
			VoteCommTreeAnchorHeight: 88,
			VoteAuthSig:              bytes.Repeat([]byte{fill + 6}, 64),
			RVpk:                     bytes.Repeat([]byte{fill + 8}, 32),
		}
	}
	return &types.MsgCastVoteBatch{Votes: votes}
}

func TestDecodeAndValidateCanonicalJSONRejectsUnknownAndTrailingData(t *testing.T) {
	handler := NewHandler(HandlerConfig{})

	unknownReq := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(`{"votes":[],"unsigned_note":"ignored?"}`))
	unknownRec := httptest.NewRecorder()
	require.False(t, handler.decodeAndValidateCanonicalJSON(unknownRec, unknownReq, &types.MsgCastVoteBatch{}))
	require.Equal(t, http.StatusBadRequest, unknownRec.Code)
	require.Contains(t, unknownRec.Body.String(), "unknown field")

	validJSON, err := json.Marshal(validHandlerBatch())
	require.NoError(t, err)
	trailingReq := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(append(validJSON, []byte(` {}`)...)))
	trailingRec := httptest.NewRecorder()
	require.False(t, handler.decodeAndValidateCanonicalJSON(trailingRec, trailingReq, &types.MsgCastVoteBatch{}))
	require.Equal(t, http.StatusBadRequest, trailingRec.Code)
	require.Contains(t, trailingRec.Body.String(), "trailing value")
}

func TestCastVoteBatchRouteRegistrationMatchesFeatureFlag(t *testing.T) {
	handler := NewHandler(HandlerConfig{})
	router := mux.NewRouter()
	handler.RegisterTxRoutes(router)

	req := httptest.NewRequest(http.MethodPost, "/shielded-vote/v1/cast-vote-batch", nil)
	require.Equal(t, types.AtomicVoteBatchesEnabled, router.Match(req, &mux.RouteMatch{}))
}

func TestCastVoteBatchHandlerReturnsStableDigest(t *testing.T) {
	comet := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"code":0,"hash":"ABC123","log":""}}`))
	}))
	defer comet.Close()

	handler := NewHandler(HandlerConfig{CometRPCEndpoint: comet.URL})
	batch := validHandlerBatch()
	body, err := json.Marshal(batch)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/shielded-vote/v1/cast-vote-batch", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	handler.handleCastVoteBatch(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var response BroadcastResult
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))
	require.Equal(t, "ABC123", response.TxHash)
	decodedDigest, err := hex.DecodeString(response.BatchDigest)
	require.NoError(t, err)
	require.Equal(t, types.ComputeCastVoteBatchSighash(batch), decodedDigest)
}
