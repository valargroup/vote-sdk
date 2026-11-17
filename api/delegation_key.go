package api

import (
	"encoding/hex"
	"fmt"
	"log"
	"net/http"

	"github.com/valargroup/vote-sdk/sentry"
	"github.com/valargroup/vote-sdk/x/vote/types"
)

// DelegationKeyHeader is the optional request header in which a client reports
// the fingerprint of the delegation circuit key that made its delegation proof.
// It is read only on the delegate-vote and delegate-and-cast-vote-batch routes.
// See docs/delegation-key-fingerprint.md for the contract.
const DelegationKeyHeader = "X-Vote-Delegation-Key-V1"

// DelegationKeyFingerprintLen is the length in bytes of a delegation circuit
// key fingerprint. The header carries it as twice as many lowercase hex
// characters.
const DelegationKeyFingerprintLen = 32

// DelegationKeyFingerprintProvider returns the fingerprint of the delegation
// circuit key this node verifies delegation proofs with. available is false
// when the node has no such key (a mock-verifier build), in which case a
// client's fingerprint is not compared.
type DelegationKeyFingerprintProvider func() (fingerprint [DelegationKeyFingerprintLen]byte, available bool)

// clientDelegationKey says how the delegation circuit key a client reported
// relates to the key this node verifies with. Its values are the
// client_delegation_key label in logs and Sentry events.
type clientDelegationKey string

const (
	// clientDelegationKeyNotApplicable marks a request on a route that carries
	// no delegation proof. Such requests are never labelled.
	clientDelegationKeyNotApplicable clientDelegationKey = ""

	// clientDelegationKeyAbsent means the client sent no fingerprint. Clients
	// released before the header existed are in this group.
	clientDelegationKeyAbsent clientDelegationKey = "absent"

	// clientDelegationKeyMatch means the client's key is the node's key.
	clientDelegationKeyMatch clientDelegationKey = "match"

	// clientDelegationKeyMismatch means the client's key differs from the
	// node's key, so its delegation proof cannot verify.
	clientDelegationKeyMismatch clientDelegationKey = "mismatch"

	// clientDelegationKeyUnchecked means the client sent a well-formed
	// fingerprint but this node has no key to compare it with.
	clientDelegationKeyUnchecked clientDelegationKey = "unchecked"
)

// checkClientDelegationKey applies the delegation key fingerprint contract to
// a decoded and validated delegation request, before anything is broadcast.
//
// It returns proceed=true when the request may be broadcast: the client sent
// no fingerprint, sent the node's fingerprint, or the node has no fingerprint
// to compare with. Otherwise it has already written the response and the
// caller must return without broadcasting:
//
//   - HTTP 400 when the header is repeated or is not exactly 64 lowercase hex
//     characters;
//   - HTTP 422 with ErrClientCircuitKeyMismatch's code when the fingerprint is
//     well formed but differs from the node's. On the batch route the response
//     carries the batch digest a broadcast of msg would have returned. The
//     mismatch is also logged and reported to Sentry.
//
// The returned label describes the client's key for later failure reports.
func (h *Handler) checkClientDelegationKey(w http.ResponseWriter, r *http.Request, msg types.VoteMessage) (label clientDelegationKey, proceed bool) {
	headerValues := r.Header.Values(DelegationKeyHeader)
	if len(headerValues) == 0 {
		return clientDelegationKeyAbsent, true
	}
	if len(headerValues) > 1 {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid %s header: sent more than once", DelegationKeyHeader))
		return clientDelegationKeyNotApplicable, false
	}
	clientFingerprint := headerValues[0]
	if !isDelegationKeyFingerprintHex(clientFingerprint) {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid %s header: expected %d lowercase hex characters", DelegationKeyHeader, 2*DelegationKeyFingerprintLen))
		return clientDelegationKeyNotApplicable, false
	}

	if h.delegationKeyFingerprint == nil {
		return clientDelegationKeyUnchecked, true
	}
	nodeFingerprintBytes, available := h.delegationKeyFingerprint()
	if !available {
		return clientDelegationKeyUnchecked, true
	}
	nodeFingerprint := hex.EncodeToString(nodeFingerprintBytes[:])
	if clientFingerprint == nodeFingerprint {
		return clientDelegationKeyMatch, true
	}

	mismatch := types.ErrClientCircuitKeyMismatch.Wrapf("client key %s, chain key %s", clientFingerprint, nodeFingerprint)
	reportDelegationFailure(delegationFailure{
		handler:   "checkClientDelegationKey",
		stage:     "delegation_key_mismatch",
		msgType:   fmt.Sprintf("%T", msg),
		clientKey: clientDelegationKeyMismatch,
		cause:     mismatch,
	})
	writeJSON(w, http.StatusUnprocessableEntity, &BroadcastResult{
		Code: types.ErrClientCircuitKeyMismatch.ABCICode(),
		Log: fmt.Sprintf(
			"this app's delegation circuit key does not match this chain, so its delegation proof cannot be verified; update the app to the latest version and try again (app key %s, chain key %s)",
			clientFingerprint, nodeFingerprint,
		),
		BatchDigest: batchDigestHex(msg),
	})
	return clientDelegationKeyMismatch, false
}

// isDelegationKeyFingerprintHex reports whether value is a fingerprint in the
// only accepted encoding: exactly 64 lowercase hex characters.
func isDelegationKeyFingerprintHex(value string) bool {
	if len(value) != 2*DelegationKeyFingerprintLen {
		return false
	}
	for i := 0; i < len(value); i++ {
		c := value[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// isInvalidProofRejection reports whether a broadcast outcome is the vote
// module's invalid zero-knowledge proof rejection. The codespace is compared
// as well as the code, because other modules have their own code 5.
func isInvalidProofRejection(result *BroadcastResult) bool {
	return result.Code == types.ErrInvalidProof.ABCICode() &&
		result.codespace == types.ErrInvalidProof.Codespace()
}

// delegationFailure describes one rejected delegation request for operators.
// It deliberately holds no request content: no proof, signature, or message
// field.
type delegationFailure struct {
	// handler and stage name where the rejection was decided.
	handler string
	stage   string
	// msgType is the Go type of the rejected vote message.
	msgType string
	// txHash is the transaction hash, empty when nothing was broadcast.
	txHash string
	// clientKey labels the delegation circuit key the client reported.
	clientKey clientDelegationKey
	// cause is the error reported to Sentry.
	cause error
}

// reportDelegationFailure writes one log line and sends one Sentry event for a
// rejected delegation request, labelled with how the client's delegation
// circuit key relates to the node's. Sentry scrubs request headers, so the
// label travels as an explicit tag.
func reportDelegationFailure(failure delegationFailure) {
	log.Printf(
		"[shielded-vote-api] delegation rejected stage=%s msg_type=%s tx_hash=%s client_delegation_key=%s: %v",
		failure.stage, failure.msgType, failure.txHash, failure.clientKey, failure.cause,
	)
	tags := map[string]string{
		"handler":               failure.handler,
		"stage":                 failure.stage,
		"msg_type":              failure.msgType,
		"client_delegation_key": string(failure.clientKey),
	}
	if failure.txHash != "" {
		tags["tx_hash"] = failure.txHash
	}
	sentry.CaptureErr(failure.cause, tags)
}

// batchDigestHex returns the hex batch digest that batch routes report to the
// client so it can match a response to the batch it signed. It is empty for
// messages that are not batches.
func batchDigestHex(msg types.VoteMessage) string {
	switch batch := msg.(type) {
	case *types.MsgCastVoteBatch:
		return hex.EncodeToString(types.ComputeCastVoteBatchSighash(batch))
	case *types.MsgDelegateAndCastVoteBatch:
		return hex.EncodeToString(types.ComputeDelegateAndCastVoteBatchSighash(batch))
	default:
		return ""
	}
}
