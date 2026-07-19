package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"cosmossdk.io/log"
	"github.com/gorilla/mux"
	"github.com/stretchr/testify/require"
)

func TestUIConfigIncludesZcashNetwork(t *testing.T) {
	t.Setenv(uiModeEnv, "dev")
	t.Setenv(precomputedBaseURLEnv, "https://snapshots.example/")
	t.Setenv(zcashNetworkEnv, " TEST ")

	router := mux.NewRouter()
	RegisterUIConfigRoutes(router, log.NewNopLogger())

	req := httptest.NewRequest(http.MethodGet, "/api/ui-config", nil)
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	require.Equal(t, http.StatusOK, resp.Code)
	var config UIConfigResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&config))
	require.Equal(t, UIModeDev, config.Mode)
	require.True(t, config.DevPIRControls)
	require.Equal(t, "https://snapshots.example", config.PrecomputedBaseURL)
	require.Equal(t, "test", config.ZcashNetwork)
}

func TestUIConfigRejectsUnknownZcashNetwork(t *testing.T) {
	t.Setenv(zcashNetworkEnv, "regtest")

	router := mux.NewRouter()
	RegisterUIConfigRoutes(router, log.NewNopLogger())

	req := httptest.NewRequest(http.MethodGet, "/api/ui-config", nil)
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	require.Equal(t, http.StatusOK, resp.Code)
	var config UIConfigResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&config))
	require.Empty(t, config.ZcashNetwork)
}
