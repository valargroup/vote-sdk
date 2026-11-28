package api

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	errorsmod "cosmossdk.io/errors"
	abci "github.com/cometbft/cometbft/abci/types"
	cmtbytes "github.com/cometbft/cometbft/libs/bytes"
	rpcclient "github.com/cometbft/cometbft/rpc/client"
	coretypes "github.com/cometbft/cometbft/rpc/core/types"
	"github.com/cosmos/cosmos-sdk/client"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	"github.com/gorilla/mux"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/valargroup/vote-sdk/testutil/proxytest"
	"github.com/valargroup/vote-sdk/x/vote/types"
)

// These tests cover the proxy delegation REST routes
// (docs/proxy-delegation/contracts-v1.md §8): 404 while dormant, and with the
// gate open the delegate-op endpoint and the registry queries.

// fakeQueryNode answers ABCI queries with handle. The client.CometRPC methods
// the query routes never call stay nil.
type fakeQueryNode struct {
	client.CometRPC
	handle func(path string, data []byte) (proto.Message, error)
	// height is the last committed height: ABCI Info reports it, and every
	// query reads at it, as BaseApp answers a latest-state query.
	height int64
}

// ABCIInfo reports height as the last committed height.
func (n *fakeQueryNode) ABCIInfo(context.Context) (*coretypes.ResultABCIInfo, error) {
	return &coretypes.ResultABCIInfo{Response: abci.ResponseInfo{LastBlockHeight: n.height}}, nil
}

// ABCIQueryWithOptions answers one query at height as BaseApp would: a gRPC
// status error becomes the SDK error BaseApp maps it to (NotFound to
// ErrKeyNotFound, InvalidArgument to ErrInvalidRequest), and a response is
// marshaled.
func (n *fakeQueryNode) ABCIQueryWithOptions(_ context.Context, path string, data cmtbytes.HexBytes, _ rpcclient.ABCIQueryOptions) (*coretypes.ResultABCIQuery, error) {
	resp, err := n.handle(path, data)
	if err != nil {
		var sdkErr error
		switch status.Code(err) {
		case codes.NotFound:
			sdkErr = errorsmod.Wrap(sdkerrors.ErrKeyNotFound, err.Error())
		case codes.InvalidArgument:
			sdkErr = errorsmod.Wrap(sdkerrors.ErrInvalidRequest, err.Error())
		default:
			sdkErr = errorsmod.Wrap(sdkerrors.ErrUnknownRequest, err.Error())
		}
		codespace, code, log := errorsmod.ABCIInfo(sdkErr, false)
		return &coretypes.ResultABCIQuery{Response: abci.ResponseQuery{Code: code, Codespace: codespace, Log: log, Height: n.height}}, nil
	}
	value, err := proto.Marshal(resp)
	if err != nil {
		return nil, err
	}
	return &coretypes.ResultABCIQuery{Response: abci.ResponseQuery{Value: value, Height: n.height}}, nil
}

// proxyRouter mounts a handler's tx and query routes, broadcasting to
// cometURL and querying through handle.
func proxyRouter(cometURL string, handle func(path string, data []byte) (proto.Message, error)) *mux.Router {
	handler := NewHandler(HandlerConfig{CometRPCEndpoint: cometURL})
	router := mux.NewRouter()
	handler.RegisterTxRoutes(router)
	handler.RegisterQueryRoutes(router, client.Context{}.WithClient(&fakeQueryNode{handle: handle}))
	return router
}

// cometExpectingTx stands in for CometBFT: it checks that each
// broadcast_tx_sync carries want under tag and answers with result, a
// ResultBroadcastTx JSON object. It reports mismatches with t.Errorf, since it
// runs on the server's goroutine.
func cometExpectingTx(t *testing.T, tag byte, want proto.Message, result string) *httptest.Server {
	t.Helper()
	comet := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string `json:"method"`
			Params struct {
				Tx []byte `json:"tx"`
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Method != "broadcast_tx_sync" {
			t.Errorf("unexpected Comet request %q: %v", req.Method, err)
		} else if gotTag, msg, err := DecodeVoteTx(req.Params.Tx); err != nil {
			t.Errorf("broadcast tx does not decode: %v", err)
		} else if gotTag != tag || !proto.Equal(want, msg.(proto.Message)) {
			t.Errorf("broadcast tag 0x%02x %v, want 0x%02x %v", gotTag, msg, tag, want)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":` + result + `}`))
	}))
	t.Cleanup(comet.Close)
	return comet
}

// TestProxyRoutesDormant checks that every proxy delegation route answers 404
// while dormant, before readiness, decoding or any broadcast or query.
func TestProxyRoutesDormant(t *testing.T) {
	proxytest.SkipUnlessDormant(t)
	comet := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("a dormant route broadcast")
	}))
	defer comet.Close()

	handler := NewHandler(HandlerConfig{
		CometRPCEndpoint: comet.URL,
		CryptoReadiness:  func() CryptoReadinessStatus { return CryptoReadinessStatus{Status: CryptoReadinessStatusWarming} },
	})
	router := mux.NewRouter()
	handler.RegisterTxRoutes(router)
	handler.RegisterQueryRoutes(router, client.Context{}.WithClient(&fakeQueryNode{handle: func(path string, _ []byte) (proto.Message, error) {
		t.Errorf("a dormant route queried %s", path)
		return nil, status.Error(codes.Internal, "unexpected query")
	}}))

	for _, req := range []*http.Request{
		httptest.NewRequest(http.MethodPost, "/shielded-vote/v1/delegate-op", strings.NewReader(`{}`)),
		httptest.NewRequest(http.MethodGet, "/shielded-vote/v1/delegates?start_after=1&limit=2", nil),
		httptest.NewRequest(http.MethodGet, "/shielded-vote/v1/delegates/1", nil),
		httptest.NewRequest(http.MethodGet, "/shielded-vote/v1/delegates/by-key/"+strings.Repeat("ab", 32), nil),
		httptest.NewRequest(http.MethodGet, "/shielded-vote/v1/delegates/by-handle/1/alice", nil),
		httptest.NewRequest(http.MethodGet, "/shielded-vote/v1/proxy-delegation-params", nil),
	} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		require.Equal(t, http.StatusNotFound, rec.Code, "%s %s", req.Method, req.URL)
		require.JSONEq(t, `{"error":"proxy delegation is not enabled"}`, rec.Body.String())
	}
}

// TestProxySubmissionRoutesRejectNonCanonicalOrInvalidBodies checks that the
// delegate-op endpoint decodes canonically and runs ValidateBasic, answering
// 400 without a broadcast.
func TestProxySubmissionRoutesRejectNonCanonicalOrInvalidBodies(t *testing.T) {
	proxytest.SkipUnlessEnabled(t)
	comet := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("an invalid body was broadcast")
	}))
	defer comet.Close()
	router := proxyRouter(comet.URL, nil)

	retire, err := json.Marshal(delegateOpsForCodecTest()["retire"])
	require.NoError(t, err)
	for name, c := range map[string]struct {
		path, body, want string
	}{
		"op unknown field": {"/shielded-vote/v1/delegate-op", strings.Replace(string(retire), "{", `{"memo":"x",`, 1), "unknown field"},
		"op no variant":    {"/shielded-vote/v1/delegate-op", `{}`, "exactly one of register, retire and ballot"},
	} {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, c.path, strings.NewReader(c.body)))
			require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			require.Contains(t, rec.Body.String(), c.want)
		})
	}
}

// TestDelegateOpRoute checks POST /delegate-op with the gate open for each
// variant: a 0x0B broadcast and a body with codespace and no batch_digest.
func TestDelegateOpRoute(t *testing.T) {
	proxytest.SkipUnlessEnabled(t)
	for name, op := range delegateOpsForCodecTest() {
		t.Run(name, func(t *testing.T) {
			comet := cometExpectingTx(t, TagDelegateOp, op, `{"code":57,"data":"","log":"delegate ballot exists","codespace":"vote","hash":"ABC123"}`)
			rec := postJSON(t, proxyRouter(comet.URL, nil), "/shielded-vote/v1/delegate-op", op, false)
			require.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
			require.Equal(t, `{"tx_hash":"ABC123","code":57,"codespace":"vote","log":"delegate ballot exists"}`+"\n", rec.Body.String())
		})
	}

	comet := cometBroadcastStub(t, `{"code":0,"data":"","log":"","codespace":"","hash":"ABC123"}`)
	rec := postJSON(t, proxyRouter(comet.URL, nil), "/shielded-vote/v1/delegate-op", delegateOpsForCodecTest()["retire"], false)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, `{"tx_hash":"ABC123","code":0,"codespace":""}`+"\n", rec.Body.String())
}

// TestProxyQueryRoutes checks the query routes with the gate open: each
// maps its path and parameters onto its gRPC request, writes the response as
// snake_case JSON, and maps NotFound to 404 and InvalidArgument to 400.
func TestProxyQueryRoutes(t *testing.T) {
	proxytest.SkipUnlessEnabled(t)
	dk := bytes.Repeat([]byte{0xab}, 32)
	entry := &types.ProxyDelegateEntry{
		Index:            7,
		Dk:               dk,
		Provider:         types.DelegateProviderX,
		Handle:           "alice",
		ProofId:          "1790000000000000000",
		RegisteredHeight: 12,
	}
	params := &types.ProxyDelegationParams{
		Verifiers:         []*types.ProxyVerifier{{VerifierId: 1, Pubkey: bytes.Repeat([]byte{0x09}, 32)}},
		VerifierThreshold: 1,
		MaxDelegates:      2000,
	}

	for _, c := range []struct {
		name   string
		path   string
		method string
		want   proto.Message
		resp   proto.Message
		err    error
		status int
	}{
		{"page", "/shielded-vote/v1/delegates?start_after=3&limit=2", "/svote.v1.Query/ProxyDelegates",
			&types.QueryProxyDelegatesRequest{StartAfter: 3, Limit: 2},
			&types.QueryProxyDelegatesResponse{Delegates: []*types.ProxyDelegateEntry{entry}, NextStartAfter: 7}, nil, http.StatusOK},
		{"default page", "/shielded-vote/v1/delegates", "/svote.v1.Query/ProxyDelegates",
			&types.QueryProxyDelegatesRequest{}, &types.QueryProxyDelegatesResponse{}, nil, http.StatusOK},
		{"limit above 1000", "/shielded-vote/v1/delegates?limit=1001", "/svote.v1.Query/ProxyDelegates",
			&types.QueryProxyDelegatesRequest{Limit: 1001}, nil, status.Error(codes.InvalidArgument, "limit must be at most 1000, got 1001"), http.StatusBadRequest},
		{"entry", "/shielded-vote/v1/delegates/7", "/svote.v1.Query/ProxyDelegate",
			&types.QueryProxyDelegateRequest{DelegateIndex: 7}, &types.QueryProxyDelegateResponse{Delegate: entry}, nil, http.StatusOK},
		{"unknown entry", "/shielded-vote/v1/delegates/8", "/svote.v1.Query/ProxyDelegate",
			&types.QueryProxyDelegateRequest{DelegateIndex: 8}, nil, status.Error(codes.NotFound, "delegate 8 not found"), http.StatusNotFound},
		{"by key", "/shielded-vote/v1/delegates/by-key/" + hex.EncodeToString(dk), "/svote.v1.Query/ProxyDelegateByKey",
			&types.QueryProxyDelegateByKeyRequest{Dk: dk}, &types.QueryProxyDelegateByKeyResponse{Delegate: entry}, nil, http.StatusOK},
		{"short key", "/shielded-vote/v1/delegates/by-key/abcd", "/svote.v1.Query/ProxyDelegateByKey",
			&types.QueryProxyDelegateByKeyRequest{Dk: []byte{0xab, 0xcd}}, nil, status.Error(codes.InvalidArgument, "dk must be exactly 32 bytes, got 2"), http.StatusBadRequest},
		{"by handle", "/shielded-vote/v1/delegates/by-handle/1/alice", "/svote.v1.Query/ProxyDelegateByHandle",
			&types.QueryProxyDelegateByHandleRequest{Provider: 1, Handle: "alice"}, &types.QueryProxyDelegateByHandleResponse{Delegate: entry}, nil, http.StatusOK},
		{"handle without a live entry", "/shielded-vote/v1/delegates/by-handle/2/bob", "/svote.v1.Query/ProxyDelegateByHandle",
			&types.QueryProxyDelegateByHandleRequest{Provider: 2, Handle: "bob"}, nil, status.Error(codes.NotFound, "no live delegate entry"), http.StatusNotFound},
		{"params", "/shielded-vote/v1/proxy-delegation-params", "/svote.v1.Query/ProxyDelegationParams",
			&types.QueryProxyDelegationParamsRequest{},
			&types.QueryProxyDelegationParamsResponse{Params: params, NextDelegateIndex: 8}, nil, http.StatusOK},
	} {
		t.Run(c.name, func(t *testing.T) {
			queried := false
			router := proxyRouter("", func(path string, data []byte) (proto.Message, error) {
				queried = true
				require.Equal(t, c.method, path)
				got := c.want.ProtoReflect().New().Interface()
				require.NoError(t, proto.Unmarshal(data, got))
				require.True(t, proto.Equal(c.want, got), "request %v, want %v", got, c.want)
				return c.resp, c.err
			})
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, c.path, nil))
			require.True(t, queried)
			require.Equal(t, c.status, rec.Code, rec.Body.String())
			if c.err != nil {
				require.Contains(t, rec.Body.String(), status.Convert(c.err).Message())
				return
			}
			want, err := json.Marshal(c.resp)
			require.NoError(t, err)
			require.JSONEq(t, string(want), rec.Body.String())
		})
	}
}

// TestProxyQueryRoutesRejectBadParameters checks that malformed path and query
// parameters are a 400 before any query.
func TestProxyQueryRoutesRejectBadParameters(t *testing.T) {
	proxytest.SkipUnlessEnabled(t)
	router := proxyRouter("", func(path string, _ []byte) (proto.Message, error) {
		t.Errorf("a malformed request queried %s", path)
		return nil, status.Error(codes.Internal, "unexpected query")
	})
	for _, path := range []string{
		"/shielded-vote/v1/delegates?start_after=x",
		"/shielded-vote/v1/delegates?limit=4294967296",
		"/shielded-vote/v1/delegates/-1",
		"/shielded-vote/v1/delegates/4294967296",
		"/shielded-vote/v1/delegates/by-key/zz",
		"/shielded-vote/v1/delegates/by-handle/x/alice",
	} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		require.Equal(t, http.StatusBadRequest, rec.Code, "%s: %s", path, rec.Body.String())
	}
}

// TestProxyQueryJSONUsesSnakeCaseAndBase64 pins an entry's REST shape: snake
// case keys, base64 bytes, numbers, and zero values omitted (contracts-v1 §0).
func TestProxyQueryJSONUsesSnakeCaseAndBase64(t *testing.T) {
	proxytest.SkipUnlessEnabled(t)
	rec := httptest.NewRecorder()
	writeProtoJSON(rec, &types.QueryProxyDelegateResponse{Delegate: &types.ProxyDelegateEntry{
		Index:            7,
		Dk:               []byte{0x01, 0x02},
		Provider:         types.DelegateProviderGitHub,
		Handle:           "alice-example",
		ProofId:          "0123abcd",
		Status:           types.DelegateStatusRetired,
		StatusReason:     2,
		RegisteredHeight: 12,
	}})
	require.Equal(t, http.StatusOK, rec.Code)
	require.JSONEq(t, `{"delegate":{"index":7,"dk":"AQI=","provider":2,"handle":"alice-example","proof_id":"0123abcd","status":2,"status_reason":2,"registered_height":12}}`, rec.Body.String())
}
