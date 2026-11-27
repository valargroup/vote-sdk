package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cosmos/cosmos-sdk/client"
	"github.com/gorilla/mux"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/valargroup/vote-sdk/x/vote/types"
)

// These tests pin, byte for byte in both gate states, the two chain heights a
// 0x09 client reads (docs/proxy-delegation/contracts-v1.md §8). While
// dormant, GET /height answers 404 as every proxy route does, and
// /commitment-tree/{round_id}/latest answers today's body. Once enabled,
// /height answers the node's last committed height, and /latest adds
// block_height, the height its state was read at.

// chainHeightTestHeight is the last committed height of heightTestRouter's
// node.
const chainHeightTestHeight = 57

// heightTestRouter mounts the query routes of handler over a node at
// chainHeightTestHeight whose latest tree for every round is tree.
func heightTestRouter(t *testing.T, handler *Handler, tree *types.CommitmentTreeState) *mux.Router {
	t.Helper()
	router := mux.NewRouter()
	handler.RegisterQueryRoutes(router, client.Context{}.WithClient(&fakeQueryNode{
		height: chainHeightTestHeight,
		handle: func(path string, _ []byte) (proto.Message, error) {
			require.Equal(t, "/svote.v1.Query/LatestCommitmentTree", path)
			return &types.QueryLatestTreeResponse{Tree: tree}, nil
		},
	}))
	return router
}

// serveGET answers one GET request for path on router.
func serveGET(router http.Handler, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

// TestLatestCommitmentTreeBodies pins /latest's exact bodies: today's while
// dormant, whatever height the query read, and the same bytes with
// block_height appended once enabled.
func TestLatestCommitmentTreeBodies(t *testing.T) {
	path := "/shielded-vote/v1/commitment-tree/" + strings.Repeat("ab", 32) + "/latest"
	for _, c := range []struct {
		name    string
		tree    *types.CommitmentTreeState
		dormant string
		enabled string
	}{
		{
			name:    "tree",
			tree:    &types.CommitmentTreeState{NextIndex: 5, Root: bytes.Repeat([]byte{0x01}, 32), Height: 42, NextIndexAtRoot: 5},
			dormant: `{"tree":{"next_index":5,"root":"AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE=","height":42,"next_index_at_root":5}}`,
			enabled: `{"tree":{"next_index":5,"root":"AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE=","height":42,"next_index_at_root":5},"block_height":"57"}`,
		},
		{
			name:    "empty tree",
			tree:    &types.CommitmentTreeState{},
			dormant: `{"tree":{}}`,
			enabled: `{"tree":{},"block_height":"57"}`,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			want := c.dormant
			if types.ProxyDelegationEnabled {
				want = c.enabled
			}
			rec := serveGET(heightTestRouter(t, NewHandler(HandlerConfig{}), c.tree), path)
			require.Equal(t, http.StatusOK, rec.Code)
			require.Equal(t, "application/json", rec.Header().Get("Content-Type"))
			require.Equal(t, want, rec.Body.String())
		})
	}
}

// TestHeightRoute pins GET /height's exact bodies: 404 while dormant, before
// the node is asked, and the node's last committed height as a decimal string
// once enabled.
func TestHeightRoute(t *testing.T) {
	path := "/shielded-vote/v1/height"
	offline := mux.NewRouter()
	NewHandler(HandlerConfig{}).RegisterQueryRoutes(offline, client.Context{})
	rec := serveGET(offline, path)
	if !types.ProxyDelegationEnabled {
		// Without a node client, answering 404 rather than failing shows the
		// gate comes first.
		require.Equal(t, http.StatusNotFound, rec.Code)
		require.Equal(t, `{"error":"proxy delegation is not enabled"}`+"\n", rec.Body.String())
		return
	}
	require.Equal(t, http.StatusInternalServerError, rec.Code, rec.Body.String())

	rec = serveGET(heightTestRouter(t, NewHandler(HandlerConfig{}), nil), path)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	require.Equal(t, `{"height":"57"}`+"\n", rec.Body.String())
}
