package api_test

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	"github.com/gorilla/mux"
	"github.com/stretchr/testify/require"

	"github.com/valargroup/vote-sdk/api"
	svtest "github.com/valargroup/vote-sdk/testutil"
	"github.com/valargroup/vote-sdk/x/vote/types"
)

const (
	delegateVoteRoute        = "/shielded-vote/v1/delegate-vote"
	delegateAndCastVoteRoute = "/shielded-vote/v1/delegate-and-cast-vote-batch"
)

// nodeDelegationKey and outdatedDelegationKey are arbitrary distinct
// fingerprints: the node's own, and one a client built against another
// delegation circuit would send.
var (
	nodeDelegationKey     = strings.Repeat("ab", api.DelegationKeyFingerprintLen)
	outdatedDelegationKey = strings.Repeat("cd", api.DelegationKeyFingerprintLen)
)

// delegationKeyNode is a vote API wired to a fake CometBFT node that counts
// broadcasts and answers each one with a fixed JSON-RPC result.
type delegationKeyNode struct {
	router     *mux.Router
	broadcasts atomic.Int64
}

// newDelegationKeyNode starts the fake node. fingerprintAvailable selects
// between a node that knows its delegation key fingerprint (nodeDelegationKey)
// and a mock-verifier build that has none.
func newDelegationKeyNode(t *testing.T, fingerprintAvailable bool, cometResult string) *delegationKeyNode {
	t.Helper()
	node := &delegationKeyNode{router: mux.NewRouter()}
	comet := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		node.broadcasts.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":` + cometResult + `}`))
	}))
	t.Cleanup(comet.Close)

	handler := api.NewHandler(api.HandlerConfig{
		CometRPCEndpoint: comet.URL,
		DelegationKeyFingerprint: func() (fingerprint [api.DelegationKeyFingerprintLen]byte, available bool) {
			if !fingerprintAvailable {
				return fingerprint, false
			}
			decoded, err := hex.DecodeString(nodeDelegationKey)
			require.NoError(t, err)
			copy(fingerprint[:], decoded)
			return fingerprint, true
		},
	})
	handler.RegisterTxRoutes(node.router)
	return node
}

// post submits msg to route, sending each of delegationKeys as one
// X-Vote-Delegation-Key-V1 header.
func (node *delegationKeyNode) post(t *testing.T, route string, msg any, delegationKeys ...string) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(msg)
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, route, bytes.NewReader(body))
	for _, key := range delegationKeys {
		req.Header.Add(api.DelegationKeyHeader, key)
	}
	rec := httptest.NewRecorder()
	node.router.ServeHTTP(rec, req)
	return rec
}

func validDelegation() *types.MsgDelegateVote {
	return svtest.ValidDelegation(bytes.Repeat([]byte{0x51}, types.RoundIDLen), 0x31)
}

func validDelegateAndCast() *types.MsgDelegateAndCastVoteBatch {
	roundID := bytes.Repeat([]byte{0x51}, types.RoundIDLen)
	votes := svtest.ValidCastVoteN(roundID, 0, 2, 90)
	votes[0].ProposalId = 1
	votes[1].ProposalId = 2
	return &types.MsgDelegateAndCastVoteBatch{
		Delegation: svtest.ValidDelegation(roundID, 0x31),
		Batch:      &types.MsgCastVoteBatch{Votes: votes},
	}
}

// delegationRoutes lists both routes that read the delegation key header, each
// with a valid request and the batch digest its responses must carry.
func delegationRoutes() []struct {
	name        string
	route       string
	msg         any
	batchDigest string
} {
	composite := validDelegateAndCast()
	return []struct {
		name        string
		route       string
		msg         any
		batchDigest string
	}{
		{"delegate-vote", delegateVoteRoute, validDelegation(), ""},
		{
			"delegate-and-cast-vote-batch", delegateAndCastVoteRoute, composite,
			hex.EncodeToString(types.ComputeDelegateAndCastVoteBatchSighash(composite)),
		},
	}
}

const acceptedByComet = `{"code":0,"hash":"ABC123","log":""}`

func TestDelegationKeyAbsentProceedsToBroadcast(t *testing.T) {
	for _, tc := range delegationRoutes() {
		t.Run(tc.name, func(t *testing.T) {
			node := newDelegationKeyNode(t, true, acceptedByComet)
			rec := node.post(t, tc.route, tc.msg)

			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			require.EqualValues(t, 1, node.broadcasts.Load())
		})
	}
}

func TestDelegationKeyMatchProceedsToBroadcast(t *testing.T) {
	for _, tc := range delegationRoutes() {
		t.Run(tc.name, func(t *testing.T) {
			node := newDelegationKeyNode(t, true, acceptedByComet)
			rec := node.post(t, tc.route, tc.msg, nodeDelegationKey)

			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			require.EqualValues(t, 1, node.broadcasts.Load())
			var response api.BroadcastResult
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))
			require.Equal(t, "ABC123", response.TxHash)
			require.Equal(t, tc.batchDigest, response.BatchDigest)
		})
	}
}

func TestDelegationKeyMismatchIsRejectedWithoutBroadcast(t *testing.T) {
	for _, tc := range delegationRoutes() {
		t.Run(tc.name, func(t *testing.T) {
			node := newDelegationKeyNode(t, true, acceptedByComet)
			rec := node.post(t, tc.route, tc.msg, outdatedDelegationKey)

			require.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
			require.Zero(t, node.broadcasts.Load())

			// Decode into a plain map so the exact set of JSON fields is
			// checked: clients reject fields they do not know.
			var fields map[string]any
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &fields))
			require.Equal(t, "", fields["tx_hash"])
			require.EqualValues(t, 50, fields["code"])
			require.EqualValues(t, types.ErrClientCircuitKeyMismatch.ABCICode(), fields["code"])
			responseLog, ok := fields["log"].(string)
			require.True(t, ok)
			require.Contains(t, responseLog, outdatedDelegationKey)
			require.Contains(t, responseLog, nodeDelegationKey)
			require.Contains(t, responseLog, "update the app")

			expectedFields := []string{"tx_hash", "code", "log"}
			if tc.batchDigest != "" {
				expectedFields = append(expectedFields, "batch_digest")
				require.Equal(t, tc.batchDigest, fields["batch_digest"])
			}
			require.Len(t, fields, len(expectedFields))
		})
	}
}

// TestDelegationKeyMismatchCarriesCodespaceWhenAskedFor checks that a client
// that opts into codespace gets it on the mismatch answer too, in codespace
// vote, as on every other answer from these routes.
func TestDelegationKeyMismatchCarriesCodespaceWhenAskedFor(t *testing.T) {
	for _, tc := range delegationRoutes() {
		t.Run(tc.name, func(t *testing.T) {
			node := newDelegationKeyNode(t, true, acceptedByComet)
			body, err := json.Marshal(tc.msg)
			require.NoError(t, err)
			req := httptest.NewRequest(http.MethodPost, tc.route, bytes.NewReader(body))
			req.Header.Set(api.DelegationKeyHeader, outdatedDelegationKey)
			req.Header.Set("X-Svote-Codespace", "1")
			rec := httptest.NewRecorder()
			node.router.ServeHTTP(rec, req)

			require.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
			require.Zero(t, node.broadcasts.Load())
			var response api.BroadcastResult
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))
			require.Equal(t, types.ErrClientCircuitKeyMismatch.ABCICode(), response.Code)
			require.Equal(t, types.ModuleName, response.Codespace)
			require.Equal(t, tc.batchDigest, response.BatchDigest)
		})
	}
}

func TestDelegationKeyMalformedHeaderIsRejectedWithoutBroadcast(t *testing.T) {
	malformed := []struct {
		name string
		keys []string
	}{
		{"empty", []string{""}},
		{"short", []string{nodeDelegationKey[:62]}},
		{"long", []string{nodeDelegationKey + "ab"}},
		{"uppercase", []string{strings.ToUpper(nodeDelegationKey)}},
		{"not_hex", []string{strings.Repeat("zz", api.DelegationKeyFingerprintLen)}},
		{"sent_twice", []string{nodeDelegationKey, nodeDelegationKey}},
	}
	for _, tc := range delegationRoutes() {
		for _, header := range malformed {
			for _, fingerprintAvailable := range []bool{true, false} {
				name := tc.name + "/" + header.name
				if !fingerprintAvailable {
					name += "/mock_verifier"
				}
				t.Run(name, func(t *testing.T) {
					node := newDelegationKeyNode(t, fingerprintAvailable, acceptedByComet)
					rec := node.post(t, tc.route, tc.msg, header.keys...)

					require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
					require.Zero(t, node.broadcasts.Load())
					var response map[string]string
					require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))
					require.Len(t, response, 1)
					require.Contains(t, response["error"], api.DelegationKeyHeader)
				})
			}
		}
	}
}

func TestDelegationKeyComparisonSkippedWithoutNodeFingerprint(t *testing.T) {
	for _, tc := range delegationRoutes() {
		t.Run(tc.name, func(t *testing.T) {
			node := newDelegationKeyNode(t, false, acceptedByComet)
			rec := node.post(t, tc.route, tc.msg, outdatedDelegationKey)

			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			require.EqualValues(t, 1, node.broadcasts.Load())
		})
	}
}

// A request with a body that fails validation is answered as before, even
// when its delegation key is outdated: the key is checked only after the body
// is decoded and validated.
func TestDelegationKeyIsCheckedAfterBodyValidation(t *testing.T) {
	node := newDelegationKeyNode(t, true, acceptedByComet)
	rec := node.post(t, delegateVoteRoute, &types.MsgDelegateVote{}, outdatedDelegationKey)

	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "validation failed")
	require.Zero(t, node.broadcasts.Load())
}

// The header is read only on the two delegation routes.
func TestDelegationKeyIgnoredOnOtherRoutes(t *testing.T) {
	node := newDelegationKeyNode(t, true, acceptedByComet)
	roundID := bytes.Repeat([]byte{0x51}, types.RoundIDLen)
	rec := node.post(t, "/shielded-vote/v1/cast-vote", svtest.ValidCastVoteN(roundID, 7, 1, 90)[0], "not-a-fingerprint")

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.EqualValues(t, 1, node.broadcasts.Load())
}

// captureAPILog redirects the standard logger, which the vote API writes its
// [shielded-vote-api] lines to, for the duration of the test.
func captureAPILog(t *testing.T) *bytes.Buffer {
	t.Helper()
	captured := &bytes.Buffer{}
	previous := log.Writer()
	log.SetOutput(captured)
	t.Cleanup(func() { log.SetOutput(previous) })
	return captured
}

func TestDelegationInvalidProofRejectionIsLabelledWithClientKey(t *testing.T) {
	// Use the same encoder as CheckTx, including its handling of Go's %w
	// wrappers. A hand-written vote/5 reply misses the undefined/1 result
	// produced by the chain's wrapped verifier failures.
	delegationError := fmt.Errorf("%w: delegation: bad proof", types.ErrInvalidProof)
	rejections := []struct {
		name string
		err  error
	}{
		{"registered_error", types.ErrInvalidProof},
		{"delegation_verifier", delegationError},
		{"combined_delegation_verifier", fmt.Errorf("delegation: %w", delegationError)},
		{"combined_vote_verifier", fmt.Errorf("batch votes[0]: %w", fmt.Errorf("%w: vote commitment: bad proof", types.ErrInvalidProof))},
	}
	cases := []struct {
		name  string
		keys  []string
		label string
	}{
		{"absent", nil, "client_delegation_key=absent"},
		{"match", []string{nodeDelegationKey}, "client_delegation_key=match"},
	}
	for _, tc := range delegationRoutes() {
		for _, key := range cases {
			for _, rejection := range rejections {
				t.Run(tc.name+"/"+key.name+"/"+rejection.name, func(t *testing.T) {
					apiLog := captureAPILog(t)
					encoded := sdkerrors.ResponseCheckTxWithEvents(rejection.err, 0, 0, nil, false)
					node := newDelegationKeyNode(t, true, cometRejection(t, rejection.err))
					rec := node.post(t, tc.route, tc.msg, key.keys...)

					require.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
					require.Contains(t, apiLog.String(), "[shielded-vote-api] delegation rejected stage=invalid_proof")
					require.Contains(t, apiLog.String(), "tx_hash=ABC123")
					require.Contains(t, apiLog.String(), key.label)

					// The client still receives the chain's own rejection, with
					// no field added for the codespace.
					var fields map[string]any
					require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &fields))
					require.EqualValues(t, encoded.Code, fields["code"])
					require.Equal(t, encoded.Log, fields["log"])
					require.Equal(t, "ABC123", fields["tx_hash"])
					require.NotContains(t, fields, "codespace")
				})
			}
		}
	}
}

// cometRejection returns the RPC result fields from the same error encoder
// CheckTx uses, rather than assuming a wrapped error keeps its module code.
func cometRejection(t *testing.T, err error) string {
	t.Helper()
	result := sdkerrors.ResponseCheckTxWithEvents(err, 0, 0, nil, false)
	encoded, encodeErr := json.Marshal(map[string]any{
		"code": result.Code, "codespace": result.Codespace,
		"hash": "ABC123", "log": result.Log,
	})
	require.NoError(t, encodeErr)
	return string(encoded)
}

func TestDelegationKeyMismatchIsLabelled(t *testing.T) {
	apiLog := captureAPILog(t)
	node := newDelegationKeyNode(t, true, acceptedByComet)
	node.post(t, delegateVoteRoute, validDelegation(), outdatedDelegationKey)

	require.Contains(t, apiLog.String(), "[shielded-vote-api] delegation rejected stage=delegation_key_mismatch")
	require.Contains(t, apiLog.String(), "client_delegation_key=mismatch")
}

// Only the vote module's invalid-proof rejection of a delegation is labelled:
// another module's code 5, another vote-module code, and a non-delegation
// route are left alone.
func TestOtherRejectionsAreNotLabelledWithClientKey(t *testing.T) {
	roundID := bytes.Repeat([]byte{0x51}, types.RoundIDLen)
	cases := []struct {
		name        string
		route       string
		msg         any
		cometResult string
	}{
		{"other_codespace", delegateVoteRoute, validDelegation(), `{"code":5,"codespace":"sdk","hash":"ABC123","log":"insufficient funds"}`},
		{"other_vote_code", delegateVoteRoute, validDelegation(), `{"code":2,"codespace":"vote","hash":"ABC123","log":"nullifier already spent"}`},
		{"cast_vote_route", "/shielded-vote/v1/cast-vote", svtest.ValidCastVoteN(roundID, 7, 1, 90)[0], `{"code":5,"codespace":"vote","hash":"ABC123","log":"invalid zero-knowledge proof"}`},
		{"wrapped_signature_error", delegateVoteRoute, validDelegation(), cometRejection(t, fmt.Errorf("%w: bad signature", types.ErrInvalidSignature))},
		{"unclassified_error", delegateVoteRoute, validDelegation(), cometRejection(t, fmt.Errorf("unrelated validation failure"))},
		{"similar_error_text", delegateVoteRoute, validDelegation(), cometRejection(t, fmt.Errorf("unsupported invalid zero-knowledge proof format"))},
		{"other_module_same_message", delegateVoteRoute, validDelegation(), cometRejection(t, sdkerrors.ErrInsufficientFunds.Wrap(types.ErrInvalidProof.Error()))},
		{"unclassified_on_cast_route", "/shielded-vote/v1/cast-vote", svtest.ValidCastVoteN(roundID, 7, 1, 90)[0], cometRejection(t, fmt.Errorf("%w: vote commitment: bad proof", types.ErrInvalidProof))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			apiLog := captureAPILog(t)
			node := newDelegationKeyNode(t, true, tc.cometResult)
			rec := node.post(t, tc.route, tc.msg)

			require.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
			require.NotContains(t, apiLog.String(), "client_delegation_key")
		})
	}
}
