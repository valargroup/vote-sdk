# Delegation circuit key fingerprint contract v1

A delegation proof only verifies against the delegation circuit key it was made
with. A client built against an older delegation circuit therefore gets every
delegation rejected with the generic "invalid zero-knowledge proof", and nothing
in the request says which key it used.

Clients can now tell the vote API which key made their proof. The API compares
it with the key its own verifier uses and, when they differ, answers "this app
is outdated" before anything is broadcast. Rejected delegations from clients
that say nothing are labelled as such for operators.

## The fingerprint

The fingerprint identifies a delegation circuit verifying key. It is
BLAKE2b-256 (32-byte output, no key, no personalisation) over the UTF-8 bytes of
Rust's `format!("{:?}", vk.pinned())` for that key. This is the definition the
`voting-circuits` crate pins in its `vk_fingerprint_unchanged` test, so a client
and a node built against the same circuit compute the same value.

The node computes its fingerprint from the key that verifies delegation proofs
(`sv_delegation_key_fingerprint` in `circuits/src/ffi.rs`), once per process.
For the pinned `voting-circuits =0.12.0` with the default `zakura` backend it is:

```text
e1773ecd2eb8531cc54c1df5b935a978788ef4eea4949cb45a00edf550ecee81
```

The fingerprint describes a public key. It is not secret and is safe to log.

## Request header

```text
X-Vote-Delegation-Key-V1: <64 lowercase hex characters>
```

The header is optional and is sent at most once. It is read only on
`POST /shielded-vote/v1/delegate-vote` and
`POST /shielded-vote/v1/delegate-and-cast-vote-batch`; every other route ignores
it. The constant is `api.DelegationKeyHeader`.

## Server answers

The header is examined after the request body has been decoded and validated,
and before anything is broadcast. Readiness, body, and validation failures are
answered exactly as before, whatever the header says.

| Header | Answer |
| --- | --- |
| Absent | The request proceeds exactly as it did before this contract existed. |
| Sent more than once, or not exactly 64 lowercase hex characters | HTTP 400 with the usual error body. Nothing is broadcast. |
| Well formed, differs from the node's fingerprint | HTTP 422 with the usual broadcast-result body and code 50. Nothing is broadcast. |
| Well formed, equals the node's fingerprint | The request proceeds as if the header were absent. |

A malformed header is answered with:

```json
{"error":"invalid X-Vote-Delegation-Key-V1 header: expected 64 lowercase hex characters"}
```

or, when the header is repeated,
`"invalid X-Vote-Delegation-Key-V1 header: sent more than once"`.

A mismatch on `delegate-vote` is answered with:

```json
{"tx_hash":"","code":50,"log":"this app's delegation circuit key does not match this chain, so its delegation proof cannot be verified; update the app to the latest version and try again (app key <client fingerprint>, chain key <node fingerprint>)"}
```

On `delegate-and-cast-vote-batch` the same body also carries `batch_digest`,
with the value a broadcast of that message would have returned:

```json
{"tx_hash":"","code":50,"log":"...","batch_digest":"<hex digest of the submitted batch>"}
```

Clients treat a batch 422 without the expected digest as "possibly sent", so
the digest is what lets them conclude that this request was definitely not
broadcast. No field is added to either body: clients reject fields they do not
know.

Code 50 is `ErrClientCircuitKeyMismatch` in the `vote` module
(`x/vote/types/errors.go`). The response has no codespace field, so a client
tells this answer apart from a chain rejection by the code together with the
empty `tx_hash`.

## The check is API-only

The comparison happens in the two HTTP handlers and nowhere else. The
fingerprint is not part of any message or transaction, and `CheckTx`, proposal
validation, and block execution never see it. A node with this check accepts and
rejects exactly the same transactions as a node without it, so the change is
state compatible and nodes can be replaced one at a time.
`ErrClientCircuitKeyMismatch` is returned only by the HTTP API and never by
consensus.

A client that bypasses the API, or sends a matching fingerprint with a proof
made by another key, gains nothing: the proof is still verified by consensus and
rejected there as before.

## Clients that send nothing

Clients released before this contract send no header. They are never rejected
for that; their requests take the same path as before. When their delegation is
then rejected for an invalid proof, the rejection is labelled `absent` (see
below), which is how operators can tell "old client, key unknown" from "current
client, genuinely bad proof".

## Builds without the real verifier

A binary built without the `halo2` build tag uses a mock verifier and has no
delegation circuit key, so it has no fingerprint. Such a node still answers a
malformed header with HTTP 400, but skips the comparison: any well-formed
fingerprint proceeds to broadcast. Production binaries are built with the tag.

## Labelling rejected delegations

The API writes one log line and sends one Sentry event when a request on either
delegation route is rejected:

- because of a key mismatch (stage `delegation_key_mismatch`, nothing
  broadcast); or
- by the chain with the vote module's invalid-proof error, codespace `vote`
  code 5 (stage `invalid_proof`).

```text
[shielded-vote-api] delegation rejected stage=<stage> msg_type=<Go type> tx_hash=<hash or empty> client_delegation_key=<label>: <error>
```

`client_delegation_key` is `absent` (no header), `match`, `mismatch`, or
`unchecked` (a well-formed fingerprint on a node that has none to compare
with). The Sentry event carries the same values as the tags `handler`, `stage`,
`msg_type`, `tx_hash` (when a transaction was broadcast), and
`client_delegation_key`. Request headers are scrubbed from Sentry events, which
is why the label is an explicit tag. No proof, signature, or other request
content is logged or tagged.

Rejections on other routes, other vote-module codes, and code 5 from another
module are not labelled.

## Tests that pin each rule

| Rule | Test |
| --- | --- |
| The Rust export returns the pinned fingerprint | `test_delegation_key_fingerprint_matches_pinned_value` (`circuits/src/ffi.rs`) |
| The Go binding returns the pinned fingerprint and caches it | `TestDelegationKeyFingerprintMatchesPinnedValue` (`ffi/zkp/halo2`, `halo2` tag) |
| Header absent proceeds to broadcast | `TestDelegationKeyAbsentProceedsToBroadcast` |
| Matching fingerprint proceeds to broadcast | `TestDelegationKeyMatchProceedsToBroadcast` |
| Mismatch returns 422, code 50, empty `tx_hash`, both fingerprints, the batch digest on the batch route, no extra fields, and no broadcast | `TestDelegationKeyMismatchIsRejectedWithoutBroadcast` |
| Malformed or repeated header returns 400 and no broadcast, with or without a node fingerprint | `TestDelegationKeyMalformedHeaderIsRejectedWithoutBroadcast` |
| No node fingerprint skips the comparison | `TestDelegationKeyComparisonSkippedWithoutNodeFingerprint` |
| The header is examined only after body validation | `TestDelegationKeyIsCheckedAfterBodyValidation` |
| Other routes ignore the header | `TestDelegationKeyIgnoredOnOtherRoutes` |
| Invalid-proof rejections are labelled `absent` or `match`, and the client response gains no field | `TestDelegationInvalidProofRejectionIsLabelledWithClientKey` |
| A mismatch is labelled `mismatch` | `TestDelegationKeyMismatchIsLabelled` |
| Other rejections are not labelled | `TestOtherRejectionsAreNotLabelledWithClientKey` |

The API tests are in `api/handler_delegation_key_test.go`.

## Rollout

Server support ships first; it changes nothing for existing clients. Clients
then start sending the header. When the delegation circuit changes, the pinned
fingerprint in the two pinned-value tests changes with it, and clients built
against the old circuit start receiving the code 50 answer.
