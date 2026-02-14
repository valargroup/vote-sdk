package api

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	abci "github.com/cometbft/cometbft/abci/types"
	"github.com/cosmos/cosmos-sdk/client"
	"github.com/gorilla/mux"
	"google.golang.org/protobuf/proto"

	"github.com/z-cale/zally/x/vote/types"
)

// RegisterQueryRoutes registers vote query REST endpoints on the router.
//
//	GET /zally/v1/commitment-tree/{height}
//	GET /zally/v1/commitment-tree/latest
//	GET /zally/v1/commitment-tree/leaves?from_height=X&to_height=Y
//	GET /zally/v1/round/{round_id}
//	GET /zally/v1/tally/{round_id}/{proposal_id}
//	GET /zally/v1/tally-results/{round_id}
func (h *Handler) RegisterQueryRoutes(router *mux.Router, clientCtx client.Context) {
	qh := &queryHandler{clientCtx: clientCtx}

	// Register "latest" and "leaves" before "{height}" to avoid gorilla/mux
	// treating them as a height param.
	router.HandleFunc("/zally/v1/commitment-tree/latest", qh.handleLatestCommitmentTree).Methods("GET")
	router.HandleFunc("/zally/v1/commitment-tree/leaves", qh.handleCommitmentLeaves).Methods("GET")
	router.HandleFunc("/zally/v1/commitment-tree/{height}", qh.handleCommitmentTreeAtHeight).Methods("GET")
	router.HandleFunc("/zally/v1/round/{round_id}", qh.handleVoteRound).Methods("GET")
	router.HandleFunc("/zally/v1/tally/{round_id}/{proposal_id}", qh.handleProposalTally).Methods("GET")
	router.HandleFunc("/zally/v1/tally-results/{round_id}", qh.handleTallyResults).Methods("GET")
}

// queryHandler handles query REST endpoints by delegating to the gRPC query
// server via BaseApp's ABCI query interface.
type queryHandler struct {
	clientCtx client.Context
}

func (qh *queryHandler) handleCommitmentTreeAtHeight(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	heightStr := vars["height"]
	height, err := strconv.ParseUint(heightStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid height: %v", err))
		return
	}

	req := &types.QueryCommitmentTreeRequest{Height: height}
	resp := &types.QueryCommitmentTreeResponse{}

	if err := qh.abciQuery("/zvote.v1.Query/CommitmentTreeAtHeight", req, resp); err != nil {
		writeQueryError(w, err)
		return
	}

	writeProtoJSON(w, resp)
}

func (qh *queryHandler) handleLatestCommitmentTree(w http.ResponseWriter, _ *http.Request) {
	req := &types.QueryLatestTreeRequest{}
	resp := &types.QueryLatestTreeResponse{}

	if err := qh.abciQuery("/zvote.v1.Query/LatestCommitmentTree", req, resp); err != nil {
		writeQueryError(w, err)
		return
	}

	writeProtoJSON(w, resp)
}

func (qh *queryHandler) handleVoteRound(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	roundIDHex := vars["round_id"]
	roundID, err := hex.DecodeString(roundIDHex)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid round_id (expected hex): %v", err))
		return
	}

	req := &types.QueryVoteRoundRequest{VoteRoundId: roundID}
	resp := &types.QueryVoteRoundResponse{}

	if err := qh.abciQuery("/zvote.v1.Query/VoteRound", req, resp); err != nil {
		writeQueryError(w, err)
		return
	}

	writeProtoJSON(w, resp)
}

func (qh *queryHandler) handleProposalTally(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)

	roundIDHex := vars["round_id"]
	roundID, err := hex.DecodeString(roundIDHex)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid round_id (expected hex): %v", err))
		return
	}

	proposalIDStr := vars["proposal_id"]
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

	if err := qh.abciQuery("/zvote.v1.Query/ProposalTally", req, resp); err != nil {
		writeQueryError(w, err)
		return
	}

	writeProtoJSON(w, resp)
}

func (qh *queryHandler) handleCommitmentLeaves(w http.ResponseWriter, r *http.Request) {
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
		FromHeight: fromHeight,
		ToHeight:   toHeight,
	}
	resp := &types.QueryCommitmentLeavesResponse{}

	if err := qh.abciQuery("/zvote.v1.Query/CommitmentLeaves", req, resp); err != nil {
		writeQueryError(w, err)
		return
	}

	writeProtoJSON(w, resp)
}

func (qh *queryHandler) handleTallyResults(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	roundIDHex := vars["round_id"]
	roundID, err := hex.DecodeString(roundIDHex)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid round_id (expected hex): %v", err))
		return
	}

	req := &types.QueryTallyResultsRequest{VoteRoundId: roundID}
	resp := &types.QueryTallyResultsResponse{}

	if err := qh.abciQuery("/zvote.v1.Query/TallyResults", req, resp); err != nil {
		writeQueryError(w, err)
		return
	}

	writeProtoJSON(w, resp)
}

// abciQuery performs an ABCI query through BaseApp's query routing.
// The path should be the fully qualified gRPC method name
// (e.g. "/zvote.v1.Query/VoteRound").
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
