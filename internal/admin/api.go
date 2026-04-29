package admin

import (
	"encoding/json"
	"net/http"

	"cosmossdk.io/log"
	"github.com/gorilla/mux"
)

// RegisterRoutes registers admin HTTP routes on the given mux router.
func RegisterRoutes(
	router *mux.Router,
	getAdmin func() *Admin,
	logger log.Logger,
) {
	h := &apiHandler{getAdmin: getAdmin, logger: logger}
	router.HandleFunc("/api/voting-config", h.handleGetVotingConfig).Methods("GET", "OPTIONS")
	router.HandleFunc("/api/vote-server-health", h.handleGetVoteServerHealth).Methods("GET", "OPTIONS")
	router.HandleFunc("/api/register-validator", h.handleRegisterValidator).Methods("POST", "OPTIONS")
	router.HandleFunc("/api/pending-validators", h.handleGetPendingValidators).Methods("GET", "OPTIONS")
}

type apiHandler struct {
	getAdmin func() *Admin
	logger   log.Logger
}

func corsHeaders(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
}

func (h *apiHandler) handleGetVotingConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method == "OPTIONS" {
		corsHeaders(w)
		w.WriteHeader(204)
		return
	}
	a := h.getAdmin()
	if a == nil {
		corsHeaders(w)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(503)
		json.NewEncoder(w).Encode(map[string]string{"error": "admin server not initialized"})
		return
	}
	cfg, err := a.GetVotingConfig()
	if err != nil {
		corsHeaders(w)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(502)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		h.logger.Error("get voting config", "error", err)
		return
	}
	corsHeaders(w)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(cfg)
}

func (h *apiHandler) handleGetVoteServerHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method == "OPTIONS" {
		corsHeaders(w)
		w.WriteHeader(204)
		return
	}
	a := h.getAdmin()
	if a == nil {
		corsHeaders(w)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(503)
		json.NewEncoder(w).Encode(map[string]string{"error": "admin server not initialized"})
		return
	}
	health, err := a.GetVoteServerHealth()
	if err != nil {
		corsHeaders(w)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(502)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		h.logger.Error("get vote-server health", "error", err)
		return
	}
	corsHeaders(w)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(health)
}
