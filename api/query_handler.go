package api

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	abci "github.com/cometbft/cometbft/abci/types"
	"github.com/cosmos/cosmos-sdk/client"
	sentryhttp "github.com/getsentry/sentry-go/http"
	"github.com/gorilla/mux"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
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
//	GET /shielded-vote/v1/rounds/overview
//	GET /shielded-vote/v1/rounds/active
//	GET /shielded-vote/v1/tally/{round_id}/{proposal_id}
//	GET /shielded-vote/v1/tally-results/{round_id}
//	GET /shielded-vote/v1/partial-decryptions/{round_id}
//	GET /shielded-vote/v1/vote-summary/{round_id}
//	GET /shielded-vote/v1/ceremony
//	GET /shielded-vote/v1/pallas-keys
//	GET /shielded-vote/v1/vote-managers
//	GET /shielded-vote/v1/coordinator-actions
//	GET /shielded-vote/v1/coordinator-actions/{id}
//	GET /shielded-vote/v1/endorsers
//	GET /shielded-vote/v1/endorsed-rounds/{id}
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
	router.Handle("/shielded-vote/v1/rounds/overview", trace(http.HandlerFunc(qh.handleRoundOverview))).Methods("GET")
	router.Handle("/shielded-vote/v1/rounds", trace(http.HandlerFunc(qh.handleListRounds))).Methods("GET")
	router.Handle("/shielded-vote/v1/round/{round_id}", trace(http.HandlerFunc(qh.handleVoteRound))).Methods("GET")
	router.Handle("/shielded-vote/v1/tally/{round_id}/{proposal_id}", trace(http.HandlerFunc(qh.handleProposalTally))).Methods("GET")
	router.Handle("/shielded-vote/v1/tally-results/{round_id}", trace(http.HandlerFunc(qh.handleTallyResults))).Methods("GET")
	router.Handle("/shielded-vote/v1/partial-decryptions/{round_id}", trace(http.HandlerFunc(qh.handlePartialDecryptions))).Methods("GET")
	router.Handle("/shielded-vote/v1/vote-summary/{round_id}", trace(http.HandlerFunc(qh.handleVoteSummary))).Methods("GET")
	router.Handle("/shielded-vote/v1/ceremony", trace(http.HandlerFunc(qh.handleCeremonyState))).Methods("GET")
	router.Handle("/shielded-vote/v1/pallas-keys", trace(http.HandlerFunc(qh.handlePallasKeys))).Methods("GET")
	router.Handle("/shielded-vote/v1/vote-managers", trace(http.HandlerFunc(qh.handleVoteManagers))).Methods("GET")
	router.Handle("/shielded-vote/v1/coordinator-actions", trace(http.HandlerFunc(qh.handlePendingCoordinatorActions))).Methods("GET")
	router.Handle("/shielded-vote/v1/coordinator-actions/{id}", trace(http.HandlerFunc(qh.handleCoordinatorAction))).Methods("GET")
	router.Handle("/shielded-vote/v1/endorsers", trace(http.HandlerFunc(qh.handleEndorsers))).Methods("GET")
	router.Handle("/shielded-vote/v1/endorsed-rounds/{id}", trace(http.HandlerFunc(qh.handleEndorsedRounds))).Methods("GET")
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

func parseOptionalUint64QueryParam(w http.ResponseWriter, r *http.Request, name string) (uint64, bool) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return 0, true
	}

	value, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid %s: %v", name, err))
		return 0, false
	}
	return value, true
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
		if grpcstatus.Code(err) == codes.NotFound {
			writeJSON(w, http.StatusOK, map[string]*types.VoteRound{"round": nil})
			return
		}
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

	fromHeight, ok := parseOptionalUint64QueryParam(w, r, "from_height")
	if !ok {
		return
	}
	toHeight, ok := parseOptionalUint64QueryParam(w, r, "to_height")
	if !ok {
		return
	}
	if toHeight != 0 && toHeight < fromHeight {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("to_height (%d) must be >= from_height (%d)", toHeight, fromHeight))
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

func (qh *queryHandler) handlePartialDecryptions(w http.ResponseWriter, r *http.Request) {
	roundID := parseRoundID(w, r)
	if roundID == nil {
		return
	}

	req := &types.QueryPartialDecryptionsRequest{VoteRoundId: roundID}
	resp := &types.QueryPartialDecryptionsResponse{}

	if err := qh.abciQuery("/svote.v1.Query/PartialDecryptions", req, resp); err != nil {
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

func (qh *queryHandler) handleRoundOverview(w http.ResponseWriter, _ *http.Request) {
	req := &types.QueryRoundOverviewRequest{}
	resp := &types.QueryRoundOverviewResponse{}

	if err := qh.abciQuery("/svote.v1.Query/RoundOverview", req, resp); err != nil {
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

func (qh *queryHandler) handlePendingCoordinatorActions(w http.ResponseWriter, _ *http.Request) {
	req := &types.QueryPendingCoordinatorActionsRequest{}
	resp := &types.QueryPendingCoordinatorActionsResponse{}

	if err := qh.abciQuery("/svote.v1.Query/PendingCoordinatorActions", req, resp); err != nil {
		writeQueryError(w, err)
		return
	}

	writeProtoJSON(w, resp)
}

func (qh *queryHandler) handleCoordinatorAction(w http.ResponseWriter, r *http.Request) {
	idRaw := mux.Vars(r)["id"]
	actionID, err := strconv.ParseUint(idRaw, 10, 64)
	if err != nil || actionID == 0 {
		writeError(w, http.StatusBadRequest, "action id must be a positive integer")
		return
	}

	req := &types.QueryCoordinatorActionRequest{ActionId: actionID}
	resp := &types.QueryCoordinatorActionResponse{}

	if err := qh.abciQuery("/svote.v1.Query/CoordinatorAction", req, resp); err != nil {
		writeQueryError(w, err)
		return
	}

	writeProtoJSON(w, resp)
}

func (qh *queryHandler) handleEndorsers(w http.ResponseWriter, _ *http.Request) {
	req := &types.QueryEndorsersRequest{}
	resp := &types.QueryEndorsersResponse{}

	if err := qh.abciQuery("/svote.v1.Query/Endorsers", req, resp); err != nil {
		writeQueryError(w, err)
		return
	}

	writeProtoJSON(w, resp)
}

func (qh *queryHandler) handleEndorsedRounds(w http.ResponseWriter, r *http.Request) {
	endorserID := mux.Vars(r)["id"]
	if err := types.ValidateEndorserID(endorserID); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid endorser_id: %v", err))
		return
	}

	req := &types.QueryEndorsedRoundsRequest{EndorserId: endorserID}
	resp := &types.QueryEndorsedRoundsResponse{}

	if err := qh.abciQuery("/svote.v1.Query/EndorsedRounds", req, resp); err != nil {
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
		return abciQueryError(abciResp.Code, abciResp.Log)
	}

	if err := proto.Unmarshal(abciResp.Value, resp); err != nil {
		return fmt.Errorf("unmarshal query response: %w", err)
	}

	return nil
}

func abciQueryError(code uint32, log string) error {
	switch {
	case strings.Contains(log, "code = NotFound"):
		return grpcstatus.Error(codes.NotFound, strings.TrimSpace(log))
	case strings.Contains(log, "code = InvalidArgument"):
		return grpcstatus.Error(codes.InvalidArgument, strings.TrimSpace(log))
	default:
		return fmt.Errorf("query failed (code %d): %s", code, log)
	}
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
	if grpcErr, ok := grpcstatus.FromError(err); ok {
		switch grpcErr.Code() {
		case codes.NotFound:
			writeError(w, http.StatusNotFound, grpcErr.Message())
			return
		case codes.InvalidArgument:
			writeError(w, http.StatusBadRequest, grpcErr.Message())
			return
		}
	}

	writeError(w, http.StatusInternalServerError, err.Error())
}
