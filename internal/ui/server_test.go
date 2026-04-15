package ui

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gorilla/mux"
)

func TestNotGRPCGateway(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{"/", true},
		{"/settings", true},
		{"/rounds/123", true},
		{"/assets/main.js", true},
		{"/cosmos/staking/v1beta1/validators", false},
		{"/cosmos/base/tendermint/v1beta1/blocks/latest", false},
		{"/cosmos/bank/v1beta1/balances/sv1abc", false},
		{"/ibc/core/channel/v1/channels", false},
	}
	for _, tt := range tests {
		r := httptest.NewRequest(http.MethodGet, tt.path, nil)
		got := notGRPCGateway(r, &mux.RouteMatch{})
		if got != tt.want {
			t.Errorf("notGRPCGateway(%q) = %v, want %v", tt.path, got, tt.want)
		}
	}
}
