package admin

import (
	"encoding/base64"
	"encoding/json"
	"github.com/stretchr/testify/require"
	"github.com/valargroup/vote-sdk/internal/pirupdate"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPIRPRCommitsBothSignedFilesTogether(t *testing.T) {
	cfg := pirupdate.Config{SchemaVersion: 1, SnapshotHeight: 3484440, BinaryTag: "v1.2.3"}
	raw := "{\"schema_version\":1,\"snapshot_height\":3484440,\"binary_tag\":\"v1.2.3\"}\n"
	body := pirPRRequest{BaseSHA: "blob", Config: raw, Attestations: pirupdate.Attestations{SchemaVersion: 1}}
	trees, refs, prs := 0, 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/repos/owner/repo/git/ref/heads/main":
			_, _ = w.Write([]byte(`{"object":{"sha":"main"}}`))
		case "/repos/owner/repo/contents/prod/pir.json":
			require.Equal(t, "main", r.URL.Query().Get("ref"))
			_ = json.NewEncoder(w).Encode(map[string]string{"sha": "blob", "encoding": "base64", "content": base64.StdEncoding.EncodeToString([]byte(raw))})
		case "/repos/owner/repo/git/commits/main":
			_, _ = w.Write([]byte(`{"tree":{"sha":"old-tree"}}`))
		case "/repos/owner/repo/git/trees":
			trees++
			var b struct {
				Base string                                       `json:"base_tree"`
				Tree []struct{ Path, Content, Mode, Type string } `json:"tree"`
			}
			require.NoError(t, json.NewDecoder(r.Body).Decode(&b))
			require.Equal(t, "old-tree", b.Base)
			require.Len(t, b.Tree, 2)
			require.Equal(t, "prod/pir.json", b.Tree[0].Path)
			require.Equal(t, raw, b.Tree[0].Content)
			require.Equal(t, "prod/pir_attestations.json", b.Tree[1].Path)
			_, _ = w.Write([]byte(`{"sha":"new-tree"}`))
		case "/repos/owner/repo/git/commits":
			_, _ = w.Write([]byte(`{"sha":"new-commit"}`))
		case "/repos/owner/repo/git/refs":
			refs++
			_, _ = w.Write([]byte(`{}`))
		case "/repos/owner/repo/pulls":
			prs++
			_, _ = w.Write([]byte(`{"html_url":"https://github.com/owner/repo/pull/1"}`))
		default:
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"message":"not found"}`))
		}
	}))
	defer server.Close()
	a := &Admin{configPR: configPRAutomation{Owner: "owner", Repo: "repo", BaseBranch: "main", APIURL: server.URL, ConfigPath: "prod"}}
	result, err := a.createPIRUpdatePR(t.Context(), body, cfg)
	require.NoError(t, err)
	require.Equal(t, "new-commit", result.CommitSHA)
	require.Equal(t, 1, trees)
	require.Equal(t, 1, refs)
	require.Equal(t, 1, prs)
	body.BaseSHA = "outdated"
	_, err = a.createPIRUpdatePR(t.Context(), body, cfg)
	require.ErrorContains(t, err, "changed")
	require.Equal(t, 1, trees)
}
