package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
)

func TestResolvePIRServiceURLPrefersVotingConfigResolver(t *testing.T) {
	cfg := SnapshotConfig{
		PIRServiceURLResolver: func(context.Context) (string, error) {
			return " https://stage.pir.valargroup.org ", nil
		},
	}

	got, err := resolvePIRServiceURL(context.Background(), cfg)
	if err != nil {
		t.Fatalf("resolvePIRServiceURL returned error: %v", err)
	}
	if got != "https://stage.pir.valargroup.org" {
		t.Fatalf("resolved PIR URL = %q, want stage endpoint", got)
	}
}

func TestResolvePIRServiceURLRejectsEmptyResolverResult(t *testing.T) {
	cfg := SnapshotConfig{
		PIRServiceURLResolver: func(context.Context) (string, error) {
			return " ", nil
		},
	}

	_, err := resolvePIRServiceURL(context.Background(), cfg)
	if err == nil {
		t.Fatalf("resolvePIRServiceURL returned nil error for empty resolver result")
	}
}

func TestResolvePIRServiceURLReturnsResolverError(t *testing.T) {
	want := errors.New("config unavailable")
	cfg := SnapshotConfig{
		PIRServiceURLResolver: func(context.Context) (string, error) {
			return "", want
		},
	}

	_, err := resolvePIRServiceURL(context.Background(), cfg)
	if !errors.Is(err, want) {
		t.Fatalf("resolvePIRServiceURL error = %v, want %v", err, want)
	}
}

func TestResolvePIRServiceURLRequiresResolver(t *testing.T) {
	_, err := resolvePIRServiceURL(context.Background(), SnapshotConfig{})
	if err == nil {
		t.Fatalf("resolvePIRServiceURL returned nil error without resolver")
	}
}

func TestDecodeLwdTreeStateSelectsIronwoodTree(t *testing.T) {
	var encoded []byte
	encoded = protowire.AppendTag(encoded, 1, protowire.BytesType)
	encoded = protowire.AppendString(encoded, "main")
	encoded = protowire.AppendTag(encoded, 2, protowire.VarintType)
	encoded = protowire.AppendVarint(encoded, 3_500_000)
	encoded = protowire.AppendTag(encoded, 6, protowire.BytesType)
	encoded = protowire.AppendString(encoded, "orchard-frontier")
	encoded = protowire.AppendTag(encoded, 7, protowire.BytesType)
	encoded = protowire.AppendString(encoded, "ironwood-frontier")

	ts, err := decodeLwdTreeState(encoded)
	if err != nil {
		t.Fatalf("decodeLwdTreeState returned error: %v", err)
	}
	if ts.IronwoodTree != "ironwood-frontier" {
		t.Fatalf("IronwoodTree = %q, want ironwood-frontier", ts.IronwoodTree)
	}
	if err := validateLwdTreeState(ts, 3_500_000, "main"); err != nil {
		t.Fatalf("validateLwdTreeState returned error: %v", err)
	}
}

func TestDecodeLwdTreeStateRejectsMissingIronwoodTree(t *testing.T) {
	var encoded []byte
	encoded = protowire.AppendTag(encoded, 2, protowire.VarintType)
	encoded = protowire.AppendVarint(encoded, 3_500_000)
	encoded = protowire.AppendTag(encoded, 6, protowire.BytesType)
	encoded = protowire.AppendString(encoded, "orchard-frontier")

	_, err := decodeLwdTreeState(encoded)
	if err == nil || !strings.Contains(err.Error(), "no ironwoodTree field") {
		t.Fatalf("decodeLwdTreeState error = %v, want missing ironwoodTree error", err)
	}
}

func TestValidateLwdTreeStateRejectsMismatch(t *testing.T) {
	ts := &lwdTreeState{
		Network:      "main",
		Height:       3_500_000,
		IronwoodTree: "ironwood-frontier",
	}
	tests := []struct {
		name            string
		expectedHeight  uint64
		expectedNetwork string
		wantErr         string
	}{
		{
			name:            "height",
			expectedHeight:  3_500_001,
			expectedNetwork: "main",
			wantErr:         "does not match requested height",
		},
		{
			name:            "network",
			expectedHeight:  3_500_000,
			expectedNetwork: "test",
			wantErr:         "does not match expected network",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateLwdTreeState(ts, test.expectedHeight, test.expectedNetwork)
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("validateLwdTreeState error = %v, want %q", err, test.wantErr)
			}
		})
	}
}

func TestFetchNullifierRootRequiresIronwoodPool(t *testing.T) {
	const height = uint64(3_500_000)
	tests := []struct {
		name     string
		pool     string
		omitPool bool
		wantErr  string
	}{
		{name: "ironwood", pool: "ironwood"},
		{name: "orchard", pool: "orchard", wantErr: `nullifier pool "orchard" is not Ironwood`},
		{name: "missing", omitPool: true, wantErr: `nullifier pool "" is not Ironwood`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				response := map[string]any{
					"root29": strings.Repeat("00", 32),
					"height": height,
				}
				if !test.omitPool {
					response["nullifier_pool"] = test.pool
				}
				if err := json.NewEncoder(w).Encode(response); err != nil {
					t.Errorf("encode response: %v", err)
				}
			}))
			defer server.Close()

			root, err := fetchNullifierRoot(context.Background(), server.URL, height)
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("fetchNullifierRoot error = %v, want %q", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("fetchNullifierRoot returned error: %v", err)
			}
			if len(root) != 32 {
				t.Fatalf("root length = %d, want 32", len(root))
			}
		})
	}
}
