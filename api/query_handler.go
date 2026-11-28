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
	"github.com/gorilla/mux"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/valargroup/vote-sdk/sentry"
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
//	GET /shielded-vote/v1/protocol-capabilities
//	GET /shielded-vote/v1/delegates?start_after=X&limit=Y
//	GET /shielded-vote/v1/delegates/{index}
//	GET /shielded-vote/v1/delegates/by-key/{hex}
//	GET /shielded-vote/v1/delegates/by-handle/{provider}/{handle}
//	GET /shielded-vote/v1/delegate-ballots/{round_id}?start_after=X&limit=Y
//	GET /shielded-vote/v1/proxy-delegation-params
//	GET /shielded-vote/v1/height
//
// The last seven serve proxy delegation (contracts-v1 §8) and answer 404 while
// it is dormant. Once it is enabled, "latest" also reports the height its
// state was read at.
func (h *Handler) RegisterQueryRoutes(router *mux.Router, clientCtx client.Context) {
	qh := &queryHandler{clientCtx: clientCtx}
	trace := sentry.RouteTracing(true)

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
	router.Handle("/shielded-vote/v1/protocol-capabilities", trace(http.HandlerFunc(qh.handleProtocolCapabilities))).Methods("GET")
	router.Handle("/shielded-vote/v1/delegates", trace(requireProxyEnabled(qh.handleProxyDelegates))).Methods("GET")
	router.Handle("/shielded-vote/v1/delegates/by-key/{hex}", trace(requireProxyEnabled(qh.handleProxyDelegateByKey))).Methods("GET")
	router.Handle("/shielded-vote/v1/delegates/by-handle/{provider}/{handle}", trace(requireProxyEnabled(qh.handleProxyDelegateByHandle))).Methods("GET")
	router.Handle("/shielded-vote/v1/delegates/{index}", trace(requireProxyEnabled(qh.handleProxyDelegate))).Methods("GET")
	router.Handle("/shielded-vote/v1/delegate-ballots/{round_id}", trace(requireProxyEnabled(qh.handleDelegateBallots))).Methods("GET")
	router.Handle("/shielded-vote/v1/proxy-delegation-params", trace(requireProxyEnabled(qh.handleProxyDelegationParams))).Methods("GET")
	router.Handle("/shielded-vote/v1/height", trace(requireProxyEnabled(qh.handleHeight))).Methods("GET")
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

// parseOptionalUint32QueryParam is parseOptionalUint64QueryParam for a uint32
// parameter: absent reads as 0, and a value that does not fit is a 400.
func parseOptionalUint32QueryParam(w http.ResponseWriter, r *http.Request, name string) (uint32, bool) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return 0, true
	}

	value, err := strconv.ParseUint(raw, 10, 32)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid %s: %v", name, err))
		return 0, false
	}
	return uint32(value), true
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

// handleLatestCommitmentTree serves a round's latest tree state, with
// block_height once proxy delegation is enabled (latestTreeWithHeight).
func (qh *queryHandler) handleLatestCommitmentTree(w http.ResponseWriter, r *http.Request) {
	roundID := parseRoundID(w, r)
	if roundID == nil {
		return
	}

	req := &types.QueryLatestTreeRequest{VoteRoundId: roundID}
	resp := &types.QueryLatestTreeResponse{}

	height, err := qh.abciQueryWithHeight("/svote.v1.Query/LatestCommitmentTree", req, resp)
	if err != nil {
		writeQueryError(w, err)
		return
	}

	if !types.ProxyDelegationEnabled {
		writeProtoJSON(w, resp)
		return
	}
	writeProtoJSON(w, latestTreeWithHeight{QueryLatestTreeResponse: resp, BlockHeight: strconv.FormatInt(height, 10)})
}

// latestTreeWithHeight is the latest-tree body once proxy delegation is
// enabled: today's body plus block_height, the committed height the state
// was read at (contracts-v1 §8).
type latestTreeWithHeight struct {
	*types.QueryLatestTreeResponse
	BlockHeight string `json:"block_height"`
}

// handleHeight serves the node's latest committed height as
// {"height":"<decimal>"}: ABCI Info's last_block_height, the version a
// latest-state query reads (contracts-v1 §8).
func (qh *queryHandler) handleHeight(w http.ResponseWriter, r *http.Request) {
	node, err := qh.clientCtx.GetNode()
	if err != nil {
		writeQueryError(w, err)
		return
	}
	info, err := node.ABCIInfo(r.Context())
	if err != nil {
		writeQueryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"height": strconv.FormatInt(info.Response.LastBlockHeight, 10)})
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

func (qh *queryHandler) handleProtocolCapabilities(w http.ResponseWriter, _ *http.Request) {
	req := &types.QueryProtocolCapabilitiesRequest{}
	resp := &types.QueryProtocolCapabilitiesResponse{}
	if err := qh.abciQuery("/svote.v1.Query/ProtocolCapabilities", req, resp); err != nil {
		writeQueryError(w, err)
		return
	}
	writeProtoJSON(w, resp)
}

// handleProxyDelegates serves a page of the delegate registry
// (ProxyDelegates, contracts-v1 §8).
func (qh *queryHandler) handleProxyDelegates(w http.ResponseWriter, r *http.Request) {
	startAfter, ok := parseOptionalUint32QueryParam(w, r, "start_after")
	if !ok {
		return
	}
	limit, ok := parseOptionalUint32QueryParam(w, r, "limit")
	if !ok {
		return
	}
	qh.serveQuery(w, "/svote.v1.Query/ProxyDelegates", &types.QueryProxyDelegatesRequest{StartAfter: startAfter, Limit: limit}, &types.QueryProxyDelegatesResponse{})
}

// handleProxyDelegate serves one registry entry by index (ProxyDelegate,
// contracts-v1 §8).
func (qh *queryHandler) handleProxyDelegate(w http.ResponseWriter, r *http.Request) {
	index, err := strconv.ParseUint(mux.Vars(r)["index"], 10, 32)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid index: %v", err))
		return
	}
	qh.serveQuery(w, "/svote.v1.Query/ProxyDelegate", &types.QueryProxyDelegateRequest{DelegateIndex: uint32(index)}, &types.QueryProxyDelegateResponse{})
}

// handleProxyDelegateByKey serves the registry entry a hex delegate key was
// registered to (ProxyDelegateByKey, contracts-v1 §8).
func (qh *queryHandler) handleProxyDelegateByKey(w http.ResponseWriter, r *http.Request) {
	dk, err := hex.DecodeString(mux.Vars(r)["hex"])
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid delegate key (expected hex): %v", err))
		return
	}
	qh.serveQuery(w, "/svote.v1.Query/ProxyDelegateByKey", &types.QueryProxyDelegateByKeyRequest{Dk: dk}, &types.QueryProxyDelegateByKeyResponse{})
}

// handleProxyDelegateByHandle serves the live registry entry of a canonical
// handle at a provider (ProxyDelegateByHandle, contracts-v1 §8).
func (qh *queryHandler) handleProxyDelegateByHandle(w http.ResponseWriter, r *http.Request) {
	provider, err := strconv.ParseUint(mux.Vars(r)["provider"], 10, 32)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid provider: %v", err))
		return
	}
	req := &types.QueryProxyDelegateByHandleRequest{Provider: uint32(provider), Handle: mux.Vars(r)["handle"]}
	qh.serveQuery(w, "/svote.v1.Query/ProxyDelegateByHandle", req, &types.QueryProxyDelegateByHandleResponse{})
}

// handleDelegateBallots serves a page of a round's delegate ballots
// (DelegateBallots, contracts-v1 §8).
func (qh *queryHandler) handleDelegateBallots(w http.ResponseWriter, r *http.Request) {
	roundID := parseRoundID(w, r)
	if roundID == nil {
		return
	}
	startAfter, ok := parseOptionalUint32QueryParam(w, r, "start_after")
	if !ok {
		return
	}
	limit, ok := parseOptionalUint32QueryParam(w, r, "limit")
	if !ok {
		return
	}
	req := &types.QueryDelegateBallotsRequest{VoteRoundId: roundID, StartAfter: startAfter, Limit: limit}
	qh.serveQuery(w, "/svote.v1.Query/DelegateBallots", req, &types.QueryDelegateBallotsResponse{})
}

// handleProxyDelegationParams serves the registry params and the next
// delegate index (ProxyDelegationParams, contracts-v1 §8).
func (qh *queryHandler) handleProxyDelegationParams(w http.ResponseWriter, _ *http.Request) {
	qh.serveQuery(w, "/svote.v1.Query/ProxyDelegationParams", &types.QueryProxyDelegationParamsRequest{}, &types.QueryProxyDelegationParamsResponse{})
}

// serveQuery answers with resp once the ABCI query at path fills it from
// req, or with the query's error (writeQueryError).
func (qh *queryHandler) serveQuery(w http.ResponseWriter, path string, req, resp proto.Message) {
	if err := qh.abciQuery(path, req, resp); err != nil {
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
	_, err := qh.abciQueryWithHeight(path, req, resp)
	return err
}

// abciQueryWithHeight is abciQuery, also returning the committed height the
// query read: BaseApp serves a latest-state query at its last committed height
// and reports that height in the response.
func (qh *queryHandler) abciQueryWithHeight(path string, req proto.Message, resp proto.Message) (int64, error) {
	bz, err := proto.Marshal(req)
	if err != nil {
		return 0, fmt.Errorf("marshal query request: %w", err)
	}

	abciResp, err := qh.clientCtx.QueryABCI(abci.RequestQuery{
		Path: path,
		Data: bz,
	})
	if err != nil {
		return 0, err
	}

	if abciResp.Code != 0 {
		return 0, abciQueryError(abciResp.Code, abciResp.Log)
	}

	if err := proto.Unmarshal(abciResp.Value, resp); err != nil {
		return 0, fmt.Errorf("unmarshal query response: %w", err)
	}

	return abciResp.Height, nil
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
func writeProtoJSON(w http.ResponseWriter, msg any) {
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
