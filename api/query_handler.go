package api

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"

	abci "github.com/cometbft/cometbft/abci/types"
	"github.com/cosmos/cosmos-sdk/client"
	sentryhttp "github.com/getsentry/sentry-go/http"
	"github.com/gorilla/mux"
	"google.golang.org/protobuf/proto"

	"github.com/valargroup/vote-sdk/x/vote/types"
)

// RegisterQueryRoutes registers vote query REST endpoints on the router.
//
//	GET /shielded-vote/v1/commitment-tree/{round_id}/latest
//	GET /shielded-vote/v1/commitment-tree/{round_id}/leaves?from_height=X&to_height=Y
//	GET /shielded-vote/v1/commitment-tree/{round_id}/{height}
//	GET /shielded-vote/v1/round/{round_id}
//	GET /shielded-vote/v1/rounds
//	GET /shielded-vote/v1/rounds/active
//	GET /shielded-vote/v1/tally/{round_id}/{proposal_id}
//	GET /shielded-vote/v1/tally-results/{round_id}
//	GET /shielded-vote/v1/vote-summary/{round_id}
//	GET /shielded-vote/v1/ceremony
//	GET /shielded-vote/v1/pallas-keys
//	GET /shielded-vote/v1/vote-managers
//	GET /shielded-vote/v1/genesis
func (h *Handler) RegisterQueryRoutes(router *mux.Router, clientCtx client.Context) {
	qh := &queryHandler{clientCtx: clientCtx}
	trace := sentryhttp.New(sentryhttp.Options{Repanic: true}).Handle

	// Register "latest" and "leaves" before "{height}" to avoid gorilla/mux
	// treating them as a height param.
	router.Handle("/shielded-vote/v1/commitment-tree/{round_id}/latest", trace(http.HandlerFunc(qh.handleLatestCommitmentTree))).Methods("GET")
	router.Handle("/shielded-vote/v1/commitment-tree/{round_id}/leaves", trace(http.HandlerFunc(qh.handleCommitmentLeaves))).Methods("GET")
	router.Handle("/shielded-vote/v1/commitment-tree/{round_id}/{height}", trace(http.HandlerFunc(qh.handleCommitmentTreeAtHeight))).Methods("GET")
	router.Handle("/shielded-vote/v1/rounds/active", trace(http.HandlerFunc(qh.handleActiveRound))).Methods("GET")
	router.Handle("/shielded-vote/v1/rounds", trace(http.HandlerFunc(qh.handleListRounds))).Methods("GET")
	router.Handle("/shielded-vote/v1/round/{round_id}", trace(http.HandlerFunc(qh.handleVoteRound))).Methods("GET")
	router.Handle("/shielded-vote/v1/tally/{round_id}/{proposal_id}", trace(http.HandlerFunc(qh.handleProposalTally))).Methods("GET")
	router.Handle("/shielded-vote/v1/tally-results/{round_id}", trace(http.HandlerFunc(qh.handleTallyResults))).Methods("GET")
	router.Handle("/shielded-vote/v1/vote-summary/{round_id}", trace(http.HandlerFunc(qh.handleVoteSummary))).Methods("GET")
	router.Handle("/shielded-vote/v1/ceremony", trace(http.HandlerFunc(qh.handleCeremonyState))).Methods("GET")
	router.Handle("/shielded-vote/v1/pallas-keys", trace(http.HandlerFunc(qh.handlePallasKeys))).Methods("GET")
	router.Handle("/shielded-vote/v1/vote-managers", trace(http.HandlerFunc(qh.handleVoteManagers))).Methods("GET")
	router.Handle("/shielded-vote/v1/genesis", trace(http.HandlerFunc(qh.handleGenesis))).Methods("GET")
}

// queryHandler handles query REST endpoints by delegating to the gRPC query
// server via BaseApp's ABCI query interface.
type queryHandler struct {
	clientCtx client.Context
}

// parseRoundID extracts and hex-decodes the round_id path variable.
// Returns nil and writes an error response on failure.
func parseRoundID(w http.ResponseWriter, r *http.Request) []byte {
	roundIDHex := mux.Vars(r)["round_id"]
	roundID, err := hex.DecodeString(roundIDHex)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid round_id (expected hex): %v", err))
		return nil
	}
	if len(roundID) != types.RoundIDLen {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("round_id must be exactly %d bytes, got %d", types.RoundIDLen, len(roundID)))
		return nil
	}
	return roundID
}

func (qh *queryHandler) handleCommitmentTreeAtHeight(w http.ResponseWriter, r *http.Request) {
	roundID := parseRoundID(w, r)
	if roundID == nil {
		return
	}
	vars := mux.Vars(r)
	heightStr := vars["height"]
	height, err := strconv.ParseUint(heightStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid height: %v", err))
		return
	}

	req := &types.QueryCommitmentTreeRequest{Height: height, VoteRoundId: roundID}
	resp := &types.QueryCommitmentTreeResponse{}

	if err := qh.abciQuery("/svote.v1.Query/CommitmentTreeAtHeight", req, resp); err != nil {
		writeQueryError(w, err)
		return
	}

	writeProtoJSON(w, resp)
}

func (qh *queryHandler) handleLatestCommitmentTree(w http.ResponseWriter, r *http.Request) {
	roundID := parseRoundID(w, r)
	if roundID == nil {
		return
	}

	req := &types.QueryLatestTreeRequest{VoteRoundId: roundID}
	resp := &types.QueryLatestTreeResponse{}

	if err := qh.abciQuery("/svote.v1.Query/LatestCommitmentTree", req, resp); err != nil {
		writeQueryError(w, err)
		return
	}

	writeProtoJSON(w, resp)
}

func (qh *queryHandler) handleActiveRound(w http.ResponseWriter, _ *http.Request) {
	req := &types.QueryActiveRoundRequest{}
	resp := &types.QueryActiveRoundResponse{}

	if err := qh.abciQuery("/svote.v1.Query/ActiveRound", req, resp); err != nil {
		writeQueryError(w, err)
		return
	}

	writeProtoJSON(w, resp)
}

func (qh *queryHandler) handleVoteRound(w http.ResponseWriter, r *http.Request) {
	roundID := parseRoundID(w, r)
	if roundID == nil {
		return
	}

	req := &types.QueryVoteRoundRequest{VoteRoundId: roundID}
	resp := &types.QueryVoteRoundResponse{}

	if err := qh.abciQuery("/svote.v1.Query/VoteRound", req, resp); err != nil {
		writeQueryError(w, err)
		return
	}

	writeProtoJSON(w, resp)
}

func (qh *queryHandler) handleProposalTally(w http.ResponseWriter, r *http.Request) {
	roundID := parseRoundID(w, r)
	if roundID == nil {
		return
	}

	proposalIDStr := mux.Vars(r)["proposal_id"]
	proposalID, err := strconv.ParseUint(proposalIDStr, 10, 32)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid proposal_id: %v", err))
		return
	}

	req := &types.QueryProposalTallyRequest{
		VoteRoundId: roundID,
		ProposalId:  uint32(proposalID),
	}
	resp := &types.QueryProposalTallyResponse{}

	if err := qh.abciQuery("/svote.v1.Query/ProposalTally", req, resp); err != nil {
		writeQueryError(w, err)
		return
	}

	writeProtoJSON(w, resp)
}

func (qh *queryHandler) handleCommitmentLeaves(w http.ResponseWriter, r *http.Request) {
	roundID := parseRoundID(w, r)
	if roundID == nil {
		return
	}

	fromStr := r.URL.Query().Get("from_height")
	toStr := r.URL.Query().Get("to_height")

	if fromStr == "" || toStr == "" {
		writeError(w, http.StatusBadRequest, "from_height and to_height query params are required")
		return
	}

	fromHeight, err := strconv.ParseUint(fromStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid from_height: %v", err))
		return
	}
	toHeight, err := strconv.ParseUint(toStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid to_height: %v", err))
		return
	}

	req := &types.QueryCommitmentLeavesRequest{
		FromHeight:  fromHeight,
		ToHeight:    toHeight,
		VoteRoundId: roundID,
	}
	resp := &types.QueryCommitmentLeavesResponse{}

	if err := qh.abciQuery("/svote.v1.Query/CommitmentLeaves", req, resp); err != nil {
		writeQueryError(w, err)
		return
	}

	writeProtoJSON(w, resp)
}

func (qh *queryHandler) handleTallyResults(w http.ResponseWriter, r *http.Request) {
	roundID := parseRoundID(w, r)
	if roundID == nil {
		return
	}

	req := &types.QueryTallyResultsRequest{VoteRoundId: roundID}
	resp := &types.QueryTallyResultsResponse{}

	if err := qh.abciQuery("/svote.v1.Query/TallyResults", req, resp); err != nil {
		writeQueryError(w, err)
		return
	}

	writeProtoJSON(w, resp)
}

func (qh *queryHandler) handleVoteSummary(w http.ResponseWriter, r *http.Request) {
	roundID := parseRoundID(w, r)
	if roundID == nil {
		return
	}

	req := &types.QueryVoteSummaryRequest{VoteRoundId: roundID}
	resp := &types.QueryVoteSummaryResponse{}

	if err := qh.abciQuery("/svote.v1.Query/VoteSummary", req, resp); err != nil {
		writeQueryError(w, err)
		return
	}

	writeProtoJSON(w, resp)
}

func (qh *queryHandler) handleCeremonyState(w http.ResponseWriter, _ *http.Request) {
	req := &types.QueryCeremonyStateRequest{}
	resp := &types.QueryCeremonyStateResponse{}

	if err := qh.abciQuery("/svote.v1.Query/CeremonyState", req, resp); err != nil {
		writeQueryError(w, err)
		return
	}

	writeProtoJSON(w, resp)
}

func (qh *queryHandler) handlePallasKeys(w http.ResponseWriter, _ *http.Request) {
	req := &types.QueryPallasKeysRequest{}
	resp := &types.QueryPallasKeysResponse{}

	if err := qh.abciQuery("/svote.v1.Query/PallasKeys", req, resp); err != nil {
		writeQueryError(w, err)
		return
	}

	writeProtoJSON(w, resp)
}

func (qh *queryHandler) handleListRounds(w http.ResponseWriter, _ *http.Request) {
	req := &types.QueryListRoundsRequest{}
	resp := &types.QueryListRoundsResponse{}

	if err := qh.abciQuery("/svote.v1.Query/ListRounds", req, resp); err != nil {
		writeQueryError(w, err)
		return
	}

	writeProtoJSON(w, resp)
}

// handleGenesis serves the node's genesis.json directly from the home directory.
// This allows joining validators to fetch genesis from any existing node.
func (qh *queryHandler) handleGenesis(w http.ResponseWriter, _ *http.Request) {
	genesisPath := filepath.Join(qh.clientCtx.HomeDir, "config", "genesis.json")
	data, err := os.ReadFile(genesisPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("read genesis.json: %v", err))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(data) //nolint:errcheck
}

func (qh *queryHandler) handleVoteManagers(w http.ResponseWriter, _ *http.Request) {
	req := &types.QueryVoteManagersRequest{}
	resp := &types.QueryVoteManagersResponse{}

	if err := qh.abciQuery("/svote.v1.Query/VoteManagers", req, resp); err != nil {
		writeQueryError(w, err)
		return
	}

	writeProtoJSON(w, resp)
}

// abciQuery performs an ABCI query through BaseApp's query routing.
// The path should be the fully qualified gRPC method name
// (e.g. "/svote.v1.Query/VoteRound").
func (qh *queryHandler) abciQuery(path string, req proto.Message, resp proto.Message) error {
	bz, err := proto.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal query request: %w", err)
	}

	abciResp, err := qh.clientCtx.QueryABCI(abci.RequestQuery{
		Path: path,
		Data: bz,
	})
	if err != nil {
		return err
	}

	if abciResp.Code != 0 {
		return fmt.Errorf("query failed (code %d): %s", abciResp.Code, abciResp.Log)
	}

	if err := proto.Unmarshal(abciResp.Value, resp); err != nil {
		return fmt.Errorf("unmarshal query response: %w", err)
	}

	return nil
}

// writeProtoJSON marshals a protobuf message to JSON and writes it to the response.
// Uses encoding/json which works with our protoc-gen-go generated types since
// they have exported fields with json struct tags.
func writeProtoJSON(w http.ResponseWriter, msg proto.Message) {
	bz, err := json.Marshal(msg)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("marshal response: %v", err))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(bz) //nolint:errcheck
}

// writeQueryError writes an appropriate HTTP error response for a query failure.
func writeQueryError(w http.ResponseWriter, err error) {
	writeError(w, http.StatusInternalServerError, err.Error())
}
