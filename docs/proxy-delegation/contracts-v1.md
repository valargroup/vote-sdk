# Proxy delegation contract

This document is the one contract for proxy delegation: the formats that the chain (vote-sdk), the client library (zcash_voting and its `zcash_vote_delegate` crate), the circuits (voting-circuits), Vizor, the attester (delegate-verifier) and the config repo build against. Nothing has shipped, so it changes in place, with every repo and vector file updated together.

Every chain rule here is dormant until the v1.7.0 upgrade sets `ProxyDelegationEnabled` (§9).

Section numbers are stable, so code can cite them as `contracts-v1 §N`. A number that names no section here is not in use.

## 0. Conventions

**Digest encoding.** Every digest in §4 is unkeyed BLAKE2b-256 over a raw ASCII domain (no length prefix, no NUL), then `lp8(chain_id)`, then fields:
- `write32(b)`: exactly 32 bytes. Any other length is refused before hashing.
- `writeU32As32(v)`, `writeU64As32(v)`: the integer in little-endian in the first 4 or 8 bytes of a zeroed 32-byte slot.
- `u8(v)`: one raw byte.
- `lp8(s)`: one length byte, then the bytes; `len(s) ≤ 255`.

**Other encodings.**
- KV keys use big-endian integers.
- Times are unix seconds (`uint64`) taken from block time.
- Hex is lowercase, except `tx_hash`, which is uppercase. Base64 is standard with padding.
- Ed25519 signatures by delegate and verifier keys, and wallet list signatures, are verified with ZIP-215 rules: CometBFT `crypto/ed25519` in Go, `ed25519-zebra` or `ed25519-consensus` in Rust.
  - **Key rule.** A delegate or verifier key must decode to a curve point (`NotOnCurve`), re-encode to the same bytes (`NonCanonical`), not be of small order (`SmallOrder`) and be torsion-free, `[ℓ]A = O` (`NotTorsionFree`), checked in that order. On chain every key-rule failure is `ErrInvalidDelegateKey` (66).
  - **Signature failures**, in CometBFT's order: the public key does not decode (`PublicKeyNotOnCurve`), `S` is not canonical (`ScalarNotCanonical`), `R` does not decode (`CommitmentNotOnCurve`), the equation fails (`EquationMismatch`). On chain each is `ErrInvalidSignature` (6), wrapped with Ed25519 context.

**Errors surface their registered code.** `cosmossdk.io/errors` derives the ABCI code by following `Cause()`, not Go's `Unwrap()`, so an error wrapped with `fmt.Errorf("%w", ...)` reports code 1 in codespace `undefined`. So:
- every error reachable from 0x09, 0x0B and p=0 reveals returns its registered code, wrapped with `errorsmod.Wrap`/`Wrapf` or returned bare;
- from activation the vote ante reports the registered code of every vote-transaction failure, including 0x02 to 0x07 and `MsgSubmitTally`. For example a spent nullifier is code 2, and 0x04's missing-root error is code 5. A failed tx's code feeds CometBFT's `LastResultsHash`, so this is gated by `ProxyDelegationEnabled` and changes only at the v1.7.0 halt. While dormant those failures keep code 1;
- tests assert the numeric code and codespace of every error in §6, on each path that raises it.

**Codes over REST.** Clients key every rule on the pair (codespace, code) and treat any codespace other than `vote` as a terminal rejection. For example `sdk` code 2 is `ErrTxDecode`, not a spent nullifier. Deployed clients parse a submission's `BroadcastResult` strictly, so `codespace` is added compatibly:
- `POST /proxy-delegation-batch` and `POST /delegate-op` always include `codespace`;
- the other submission endpoints, and `/tx/{hash}`, include it only when the request carries the header `X-Svote-Codespace: 1`.

**No `oneof` in REST-borne messages.** The REST layer decodes request JSON with `encoding/json` into the generated structs, which cannot fill a `oneof` interface field. Variants are separate optional sub-messages with an "exactly one is set" rule.

**JSON.** REST bodies use snake_case keys, `[]byte` as base64, `uint64` as JSON numbers, and omit zero values. The submission endpoints of §8 decode canonically: unknown fields and trailing values are rejected.

**Off-chain documents** (§12) are UTF-8 JSON. Integers are JSON numbers. Base64 is canonical: standard alphabet, padded, with zero padding bits, so equal bytes have one accepted text. A repeated key in an object this contract defines makes the document `Malformed`: Go's `encoding/json` keeps the last value and other parsers may keep the first, so a reader must detect it.

**Error names** such as `InvalidKey` or `Malformed` are bare names. Each implementation maps them to its own error type, and the vectors (§13) use them.

## 1. Transaction tags

| Tag | Message | Notes |
|---|---|---|
| 0x09 | `MsgProxyDelegationBatch` | DC batches (§2.1) |
| 0x0B | `MsgDelegateOp` | Delegate register, retire and ballot (§2.2) |
| 0x04 | `MsgRevealShare` | Unchanged proto; gains the proposal-0 branch (§5.4) |
| 0x0C | none | Stays unassigned |

Both new tags are encoded and decoded as 0x06 and 0x07 are: deterministic marshal, recursive rejection of unknown fields, and byte equality with the deterministic re-marshal.

**Registration traps.**
- **Signers.** At startup the SDK validates that every input of every `cosmos.msg.v1.service` RPC has a signer, whatever the dormancy const says. Both new Msgs get no-op `CustomGetSigner` providers.
- **Gating.** `RegisterMsgServer` registers the whole generated service, so the new RPCs cannot be gated per RPC; their handlers return `ErrProxyDelegationDisabled` while dormant.
- **gogoproto bridges.** Nested types in Msgs and payloads are bridged with `gogoproto.RegisterType`: `ProxyDelegationAction`, `DelegateAttestation`, `DelegateRegister`, `DelegateRetire`, `DelegateBallot` and `ProxyVerifier`.

## 2. Messages (`proto/svote/v1/tx.proto`)

### 2.1 0x09 `MsgProxyDelegationBatch`

```proto
message ProxyDelegationAction {
  bytes van_nullifier = 1;               // 32
  bytes r_vpk = 2;                       // 32, compressed, non-identity
  bytes vote_authority_note_new = 3;     // 32, successor VAN (W - w, MAX authority)
  bytes proxy_delegation_commitment = 4; // 32, the DC
  bytes dc_slot_nullifier = 5;           // 32
  bytes proof = 6;                       // ZKP4, 1..MaxProofSize bytes
  bytes vote_auth_sig = 7;               // 64, RedPallas under r_vpk over D1
}

message MsgProxyDelegationBatch {
  bytes vote_round_id = 1;                 // 32
  uint64 vote_comm_tree_anchor_height = 2; // 0 if and only if registration is set
  MsgDelegateVote registration = 3;        // optional ZKP1, today's message unchanged
  repeated ProxyDelegationAction dcs = 4;  // 1..8
  uint64 expiry_height = 5;                // rejected at or after this height
}

message MsgProxyDelegationBatchResponse {
  bytes batch_digest = 1; // D1 with expiry_height = 0 (§4.1)
}
```

### 2.2 0x0B `MsgDelegateOp`

```proto
message DelegateAttestation {
  uint32 verifier_id = 1;
  uint64 not_before = 2;  // unix seconds
  uint64 expires_at = 3;  // unix seconds, at most 72 h after not_before
  bytes sig = 4;          // 64, Ed25519 over ATTEST by the verifier's params key
}

message DelegateRegister {
  bytes dk = 1;                // 32, the Ed25519 delegate key
  uint32 provider = 2;         // 1 = X, 2 = GitHub
  string handle = 3;           // canonical (§2.5)
  string proof_id = 4;         // canonical: the X post id or the gist id (§2.5)
  repeated DelegateAttestation attestations = 5; // 1..16
  bytes dk_sig = 6;            // 64, Ed25519 over REGISTER by dk
}

message DelegateRetire {
  uint32 delegate_index = 1;
  bytes dk_sig = 2;            // 64, Ed25519 over RETIRE by the entry's dk
}

message DelegateBallot {
  bytes vote_round_id = 1;     // 32
  uint32 delegate_index = 2;
  repeated uint32 options = 3; // options[i] is the choice on proposal i + 1
  bytes dk_sig = 4;            // 64, Ed25519 over BALLOT by the entry's dk
}

// Exactly one of register, retire and ballot is set.
message MsgDelegateOp {
  DelegateRegister register = 1;
  DelegateRetire retire = 2;
  DelegateBallot ballot = 3;
}

message MsgDelegateOpResponse {
  uint32 delegate_index = 1; // the entry the op touched; for a registration, its assigned index
}
```

### 2.3 Round creation

`MsgCreateVotingSession` gains `bool proxy_delegation = 12;`. It is already a coordinator payload; setting the flag raises its approval floor (§5.5).

### 2.4 Coordinator payloads

Each is a payload-only message (no Msg RPC), registered like the existing ones (§5.5).

```proto
message ProxyVerifier {
  uint32 verifier_id = 1;
  bytes pubkey = 2;            // 32, Ed25519
}

message MsgSetProxyDelegationParams {
  string creator = 1;
  repeated ProxyVerifier verifiers = 2; // 0..16; empty turns registration off
  uint32 verifier_threshold = 3;        // 1..len(verifiers); 0 only when verifiers is empty
  uint32 max_delegates = 4;             // 0..2000
}

message MsgSetProxyDelegationPause {
  string creator = 1;
  bool paused = 2;
}

message MsgSetDelegateStatus {
  string creator = 1;
  repeated uint32 delegate_indexes = 2; // 1..64, each >= 1, unique
  uint32 action = 3;                    // 1 suspend, 2 lift, 3 retire
  uint32 reason = 4;                    // 1 impersonation, 2 key lost or compromised, 3 legal, 4 verifier review, 99 other
}
```

`ProxyVerifier` is defined once, in `types.proto`, and imported by `tx.proto`.

### 2.5 ValidateBasic

**Canonical forms.** A handle and a proof id each have one accepted form on the wire and in state. Clients lowercase what the user types and what a provider returns before using it.

| Field | Rule |
|---|---|
| `provider` | 1 = X, 2 = GitHub |
| `handle`, X | 1 to 15 bytes of `[a-z0-9_]` |
| `handle`, GitHub | 1 to 39 bytes of `[a-z0-9-]`, not starting or ending with `-` |
| `proof_id`, X | The post id: 1 to 20 decimal digits, no leading zero |
| `proof_id`, GitHub | The gist id: 1 to 40 bytes of `[0-9a-f]` |

**Stateless checks.** Every failure is `ErrInvalidField` (9) unless §6 says otherwise.

| Message | Stateless checks |
|---|---|
| `MsgProxyDelegationBatch` | `vote_round_id` 32 bytes; `1 ≤ len(dcs) ≤ 8`; every `r_vpk` non-zero, as `MsgCastVote` requires; `registration` set if and only if `vote_comm_tree_anchor_height == 0`, and then `registration.ValidateBasic()` with the same round; `expiry_height > 0`; every 32-byte field exactly 32; `proof` 1..`MaxProofSize`; `vote_auth_sig` 64; within the message, `van_nullifier` values unique, `dc_slot_nullifier` values unique, and the set {every DC, the final `vote_authority_note_new`} unique |
| `MsgDelegateOp` | Exactly one variant set |
| `DelegateRegister` | `dk` 32 bytes; `provider ∈ {1, 2}`; `handle` and `proof_id` canonical for the provider; 1..16 attestations, each `sig` 64 and `not_before < expires_at ≤ not_before + 259200`; `dk_sig` 64 |
| `DelegateRetire` | `delegate_index ≥ 1`; `dk_sig` 64 |
| `DelegateBallot` | `vote_round_id` 32; `delegate_index ≥ 1`; `1 ≤ len(options) ≤ MaxProposals`; `dk_sig` 64 |
| `MsgSetProxyDelegationParams` | At most 16 verifiers with unique ids and unique pubkeys, each pubkey a valid key under the key rule; threshold as in the proto comment; `max_delegates ≤ 2000` |
| `MsgSetProxyDelegationPause` | A bech32 `creator` |
| `MsgSetDelegateStatus` | 1..64 `delegate_indexes`, each `≥ 1`, none repeated; `action ∈ {1, 2, 3}`; `reason ∈ {1, 2, 3, 4, 99}` |

`max_delegates` is capped at 2,000 because routing (§5.6) is measured up to that size (`routing-performance.md`).

## 3. State

### 3.1 Types (`proto/svote/v1/types.proto`)

```proto
message ProxyDelegationRoundState {
  bool enabled = 1;
  uint32 next_delegate_index_at_creation = 2;
  bool pools_routed = 3;
}
// VoteRound gains: ProxyDelegationRoundState proxy_delegation = 31;
// Written only for rounds created with the proxy flag. It is outside the round-id preimage.

message ProxyDelegateEntry {
  uint32 index = 1;
  bytes dk = 2;                // 32; one key per entry, forever
  uint32 provider = 3;         // 1 = X, 2 = GitHub
  string handle = 4;
  string proof_id = 5;
  uint32 status = 6;           // 0 ACTIVE, 1 SUSPENDED, 2 RETIRED
  uint32 status_reason = 7;    // coordinator reason code; 0 when retired by its own key or never set
  uint64 registered_height = 8;
}

message DelegateBallotRecord {
  bytes vote_round_id = 1;
  uint32 delegate_index = 2;
  repeated uint32 options = 3;    // one per proposal, in proposal order
  uint64 height = 4;
}

message ProxyDelegationParams {
  repeated ProxyVerifier verifiers = 1;
  uint32 verifier_threshold = 2;
  uint32 max_delegates = 3;
  bool proxy_dc_paused = 4;
}
```

### 3.2 KV layout

| Key | Value |
|---|---|
| `0x1A 0x01 u32be(d)` | `ProxyDelegateEntry`, deterministic protobuf |
| `0x1A 0x02 dk(32)` | `u32be(d)`: every delegate key ever registered. A key is never reused |
| `0x1A 0x03 u8(provider) handle` | `u32be(d)` of the live entry for that handle. Written at register, deleted at retire |
| `0x1A 0x04` | `next_index` as `u32be`; absent reads as 1 (§9); index 0 is never assigned |
| `0x1A 0x06` | `ProxyDelegationParams`; absent reads as the zero value (unpaused, no verifiers, `max_delegates = 0`) |
| `0x1B round(32) u32be(d)` | `DelegateBallotRecord`; at most one per `(round, d)`, never overwritten |
| `TallyKey(round, 0, d)` | `Pool[d]`, the existing 64-byte El Gamal ciphertext slot |
| `ShareCountKey(round, 0, d)` | Pool share count, existing `u64be` slot |
| `NullifierKey(0x03, round, nf)` | DC slot nullifiers: `NullifierTypeProxySlot = 0x03` |

Under 0x1A, sub-prefix 0x05 and those from 0x07 are unused, as are 0x1C to 0x1F.

### 3.3 Entry status and the one-key rule

- An entry has one key for life, and its index never changes. Entries are never deleted.
- A **live** entry is ACTIVE or SUSPENDED. RETIRED is final.
- A handle has at most one live entry. While it has one, it cannot be registered again, so a hijacked X or GitHub account cannot add a key.
- To change a key, or after a rename on X or GitHub, the delegate retires the entry with its key and registers again. The new entry has a new index.
- A delegate who lost the key with no copy asks the coordinators to retire the entry (`MsgSetDelegateStatus`, action 3, reason 2) after an out-of-band check, then registers again.
- Only an ACTIVE entry can ballot. A ballot already accepted stands whatever happens to the entry later. So pools delegated to an entry that retires during a round count only if its ballot was already accepted.

## 4. Digests

All signatures in this section are Ed25519 over the 32-byte digest, except D1, which is signed with RedPallas under each DC's `r_vpk`.

### 4.1 D1, the 0x09 batch sighash

```
D1 = BLAKE2b-256("SVOTE_PROXY_DELEGATION_BATCH_SIGHASH_V1" || lp8(chain_id)
       || write32(vote_round_id)
       || writeU64As32(vote_comm_tree_anchor_height)
       || writeU64As32(expiry_height)
       || u8(has_registration)                       // 1 if registration is set, else 0
       || [write32(registration.van_cmx)]            // only when has_registration = 1
       || writeU32As32(n)
       || for i in 0..n:
            writeU32As32(i) || write32(r_vpk_i) || write32(van_nullifier_i)
            || write32(vote_authority_note_new_i) || write32(dc_i) || write32(dc_slot_nullifier_i))
```

`batch_digest` is D1 computed with `expiry_height = 0`. It is what the REST response echoes, what the event carries, and what zcash_voting uses as the batch identity, so every re-signed attempt of one batch shares it. Proofs and signatures are excluded, as in the 0x06 and 0x07 digests.

### 4.2 Registry digests

```
REGISTER = BLAKE2b-256("SVOTE_PROXY_DELEGATE_REGISTER_V1" || lp8(chain_id)
             || write32(dk) || writeU32As32(provider) || lp8(handle) || lp8(proof_id))

ATTEST   = BLAKE2b-256("SVOTE_PROXY_DELEGATE_ATTEST_V1" || lp8(chain_id)
             || write32(dk) || writeU32As32(provider) || lp8(handle) || lp8(proof_id)
             || writeU64As32(not_before) || writeU64As32(expires_at))

RETIRE   = BLAKE2b-256("SVOTE_PROXY_DELEGATE_RETIRE_V1" || lp8(chain_id)
             || writeU32As32(delegate_index) || write32(dk))

BALLOT   = BLAKE2b-256("SVOTE_PROXY_DELEGATE_BALLOT_V1" || lp8(chain_id)
             || write32(vote_round_id) || writeU32As32(delegate_index)
             || writeU32As32(n) || for i in 0..n: writeU32As32(options[i]))
```

`handle` and `proof_id` are the canonical strings of §2.5 as ASCII bytes. Every digest binds `chain_id`, so a signature for stage never verifies on mainnet.

### 4.3 Domain separation

A unit test in each language asserts that every `SVOTE_` domain in use is prefix-free: the three existing ones (`SVOTE_CAST_VOTE_SIGHASH_V0`, `SVOTE_CAST_VOTE_BATCH_SIGHASH_V1`, `SVOTE_DELEGATE_AND_CAST_VOTE_BATCH_SIGHASH_V1`) and the five above.

## 5. Validation and execution

### 5.1 0x09 ante

Steps 1 to 5 also run on RecheckTx; signatures and proofs are skipped there, as for 0x06 and 0x07.
1. Dormant gate.
2. The round exists (`ErrRoundNotFound`), then `round.proxy_delegation.enabled` (`ErrProxyDelegationDisabled`).
3. Nullifiers, all before any expiry, round status or pause check: gov (type 0x00, only when a registration is present), VAN (0x01) and slot (0x03). A spent nullifier returns `ErrDuplicateNullifier` with code 2. So a re-signed retry of a batch that already landed is always answered code 2, even after the round closes. A byte-identical resend can instead meet CometBFT's mempool cache, and the API then answers with that tx's own outcome: code 0 with the original hash once it has committed, or code 0 while it is still pending. The client treats either as landed or accepted.
4. `h < expiry_height` (`ErrProxyBatchExpired`), then `ValidateRoundForVoting` (round ACTIVE, `blockTime < vote_end_time`; `ErrRoundNotActive`), then `!params.proxy_dc_paused` (`ErrProxyDelegationPaused`).
   - In FinalizeBlock, `h` is the block's height.
   - In CheckTx and RecheckTx, `h` is the store's last committed height plus 1, never the context header's height, which reads 0 just after a restart.
   - So an `ErrProxyBatchExpired` answer proves the batch never landed and never can: the node has applied every block it could have landed in, and its nullifiers are unspent.
   - Codes 2 and 52 come before the round status, so a resend of a batch that already landed gets code 2, and a resend of an expired attempt gets 52, even after the round closes (§11.3). An unspent batch that has not expired gets `ErrRoundNotActive` once the round closes, and spends nothing.
5. `EnsureCommitmentCapacity(1 + n)` (`ErrCommitmentTreeFull`). Every round's tree has depth 24, so from activation every append, not only 0x09's, stops at leaf index 2^24 − 1: the ante of 0x02, 0x03, 0x06 and 0x07 checks room for every leaf the tx appends (1, 2, and 1 + n for n casts) after its nullifier check, in RecheckTx too. Otherwise a valid cast after a 0x09 that filled the last slots would pass execution and halt the chain at EndBlock.
6. Every DC's `vote_auth_sig` over D1, all checked before any proof (`ErrInvalidSignature`).
7. The registration's own checks, as in 0x07 (its SpendAuth signature over the TX1 sighash and ZKP1).
8. ZKP4 proofs (`ErrInvalidProof`). `root0 = SingleLeafRoot(van_cmx)` if a registration is present, else `GetCommitmentRootAtHeight(anchor)`, which must be non-nil (`ErrInvalidAnchorHeight`). Each later root is `SingleLeafRoot(prev.vote_authority_note_new)`. Every DC's instance uses the batch's `vote_comm_tree_anchor_height` as its anchor-height input, so 0 when a registration is present, as 0x07's casts carry 0.

### 5.2 0x09 handler

Re-run every state check in §5.1's order, with all three nullifier sets checked before any write; signatures and proofs are not re-verified, because the FinalizeBlock ante has verified them. Then:
- set every nullifier;
- append the final `vote_authority_note_new`, then each DC in message order, never intermediate VANs or `van_cmx`;
- emit `proxy_delegation_batch` (§7).

### 5.3 0x0B ante and handler

The ante runs each op's state checks in CheckTx and RecheckTx, and CheckTx also verifies its Ed25519 signatures, so an invalid op never reaches a mempool. The handler re-runs every check. `MsgDelegateOp` carries no top-level round, so the generic round check does not apply to it; the ballot op checks its own round in the order below.

**register.**
1. The params verifier set is non-empty (`ErrRegistrationClosed`).
2. `dk` passes the key rule (`ErrInvalidDelegateKey`) and is absent from `0x1A 0x02` (`ErrDelegateKeyReused`).
3. No live entry exists for `(provider, handle)` (`ErrHandleTaken`).
4. `dk_sig` verifies over REGISTER under `dk` (`ErrInvalidSignature`).
5. Attestations: each names a `verifier_id` in the current params set and verifies over ATTEST under that verifier's key, with `not_before ≤ blockTime < expires_at` (`ErrInvalidAttestation`). CheckTx skips the time window when its header time is unset after a restart, as `ValidateRoundForVoting` does; FinalizeBlock always enforces it. The count of distinct verifiers, deduplicated by id and pubkey, is at least `verifier_threshold` (`ErrInsufficientAttestations`).
6. `next_index ≤ max_delegates` (`ErrRegistryFull`).
7. Write the entry `{index: next_index, dk, provider, handle, proof_id, status: ACTIVE, registered_height}`, `0x1A 0x02 dk → index` and `0x1A 0x03 provider handle → index`, increment `next_index`, and emit `delegate_registered`.

**retire.**
- Entry `d` exists (`ErrDelegateNotFound`).
- A RETIRED entry answers `ErrDelegateRetired`, which the client treats as success. A SUSPENDED entry answers `ErrDelegateSuspended`: a suspended entry cannot retire itself, so a suspension is not escaped by registering again.
- `dk_sig` verifies over RETIRE under the entry's `dk` (`ErrInvalidSignature`).
- Set RETIRED with reason 0, delete `0x1A 0x03`, and emit `delegate_retired`.

**ballot.**
1. The round exists (`ErrRoundNotFound`).
2. No ballot is stored for `(round, d)` (`ErrDelegateBallotExists`). Any second ballot, identical or not, is rejected. This check comes before anything that can change after a ballot is accepted, so a re-signed retry of a landed ballot is always answered 57, even after close or a later suspension or retirement.
3. The round is ACTIVE with `blockTime < vote_end_time` (`ErrRoundNotActive`), and `round.proxy_delegation.enabled` (`ErrProxyDelegationDisabled`). Like `ValidateRoundForVoting`, CheckTx and RecheckTx skip the `vote_end_time` comparison while the header time is unset after a restart; FinalizeBlock always enforces it.
4. Entry `d` exists (`ErrDelegateNotFound`) and is ACTIVE: SUSPENDED is `ErrDelegateSuspended`, RETIRED is `ErrDelegateRetired`.
5. `dk_sig` verifies over BALLOT under the entry's `dk` (`ErrInvalidSignature`). The round's creation time plays no part.
6. `len(options) = len(round.Proposals)` and every `options[i] < num_options(i + 1)` (`ErrInvalidBallot`).
7. Write `0x1B round d` and emit `delegate_ballot`.

`proxy_dc_paused` does not affect any 0x0B op.

### 5.4 0x04 reveal with `proposal_id = 0`

- `MsgRevealShare.ValidateBasic` rejects `proposal_id < 1` and `vote_decision ≥ 8` today, and the proposer's reveal filter and ProcessProposal reuse it, so one p=0 reveal would make a whole block invalid. Behind the dormancy const, ValidateBasic accepts `proposal_id = 0` with `vote_decision ≥ 1` and no option bound.
- Ante and handler: `round.proxy_delegation.enabled` (`ErrProxyDelegationDisabled`) and `1 ≤ d < next_index` at reveal time (`ErrDelegateNotFound`), where `d = vote_decision`. Delegate status and `proxy_dc_paused` are ignored. Every other check is today's reveal path, with ZKP3 unchanged.
- Effects: `AddToTally(round, 0, d)`, `IncrementShareCount(round, 0, d)` and a `reveal_pool_share` event, never `reveal_share`. A p=0 reveal counts toward the 256-per-block reveal cap.

### 5.5 Coordinator payloads and approvals

- **Registration.** Each of the three payload types is registered like the existing ones: a payload type name, cases in the payload validation and execution switches, a `ValidateBasic`, `RegisterImplementations` and the CLI.
- **Approval floor.** The three proxy payloads, and a `MsgCreateVotingSession` with `proxy_delegation = true`, execute only with at least `max(2, threshold)` distinct current vote-manager approvals. So a proxy payload never executes inside its proposing tx, even at threshold 1.
- **Effects.**
  - `MsgSetProxyDelegationParams` replaces the verifier set, threshold and `max_delegates` and never changes `proxy_dc_paused`.
  - `MsgSetProxyDelegationPause` sets only `proxy_dc_paused`.
  - `MsgSetDelegateStatus` applies its action to every listed entry, or to none if one fails:
    - **suspend**: ACTIVE or SUSPENDED becomes SUSPENDED with the payload's reason. RETIRED is `ErrDelegateRetired`.
    - **lift**: SUSPENDED becomes ACTIVE with reason 0. ACTIVE is a no-op. RETIRED is `ErrDelegateRetired`.
    - **retire**: ACTIVE or SUSPENDED becomes RETIRED with the payload's reason, and `0x1A 0x03` is deleted. RETIRED is a no-op.
    - An unknown index is `ErrDelegateNotFound`.
  - `MsgCreateVotingSession` with the flag writes `VoteRound.proxy_delegation = {enabled: true, next_delegate_index_at_creation: next_index, pools_routed: false}`.

### 5.6 Routing at the tally transition

Routing runs in EndBlock, in the iteration that sets ACTIVE to TALLYING, before `SetVoteRound`:

```
if !round.proxy_delegation.enabled or round.proxy_delegation.pools_routed: return
for each ballot b under 0x1B || round, in ascending d:
    pool := GetTally(round, 0, d); if pool is nil: continue
    // a decode failure, or a stored ballot without exactly one option per proposal, is state corruption: halt
    for p in 1..n: sums[(p, b.options[p - 1])] += pool
for (p, o) in sums, ascending by p then o:
    combined := GetTally(round, p, o) + sums[(p, o)]
    rho := ScalarPallas.Hash("svote-route-rerand-v1" || round(32) || u32be(p) || u32be(o) || MarshalCiphertext(combined))
    if rho == 0: halt                                // EncryptWithRandomness rejects 0
    final := combined + EncryptWithRandomness(ea_pk, 0, rho)
    if final.C1 or final.C2 is the identity: halt   // probability about 2^-250
    Set(TallyKey(round, p, o), final)
set pools_routed; emit proxy_pools_routed
```

`ScalarPallas.Hash` is curvey's hash-to-scalar, as `crypto/elgamal` uses it. An absent `GetTally(round, p, o)` counts as the identity ciphertext. Routing derives one `rho` per bucket and halts on a degenerate one; there is no retry. Buckets are written only after all are computed, so a failed run writes nothing. Proposal 0 never enters partial decryption, completeness, `ValidateEntryBounds` or `MsgSubmitTally`.

## 6. Errors

New errors are registered in codespace `vote` from code 51. Code 50 is `ErrClientCircuitKeyMismatch`, which the vote API returns for a delegation proved with another circuit key. Codes 15, 20 and 44 were used before proxy delegation and are not reused; 56, 63 and 67 are unused. Existing codes keep their meaning.

| Code | Error | Raised by | Client handling |
|---|---|---|---|
| 2 | `ErrDuplicateNullifier` (existing) | 0x09 nullifier check | Landed, or the registration landed alone: run exact-tree recovery, which looks for the registration's standalone VAN leaf first (§11.3). If it finds neither, resend |
| 3 | `ErrRoundNotFound` (existing) | 0x09, ballot | 0x09: resend (§11.3). Ballot: stop |
| 4 | `ErrRoundNotActive` (existing) | 0x09, ballot | 0x09: resend (§11.3). Ballot: stop; at or after `vote_end_time` this is `VoteEnded` |
| 5 | `ErrInvalidProof` (existing) | 0x09 | Resend (§11.3); a client bug |
| 6 | `ErrInvalidSignature` (existing) | 0x09, 0x0B | 0x09: resend (§11.3). 0x0B: stop retries |
| 7 | `ErrInvalidAnchorHeight` (existing) | 0x09 | Resend (§11.3); may come from a node that is behind |
| 9 | `ErrInvalidField` (existing) | ValidateBasic | 0x09: resend (§11.3). Otherwise stop retries |
| 11 | `ErrCommitmentTreeFull` (existing) | 0x09 | Resend (§11.3) |
| 51 | `ErrProxyDelegationPaused` | 0x09 | Resend until the pause lifts, re-signing once the expiry has passed |
| 52 | `ErrProxyBatchExpired` | 0x09 | Proof that the batch never landed. Re-sign D1 and resend (§11.3) |
| 53 | `ErrDelegateNotFound` | 0x0B, p=0 reveal, status payload | Permanent |
| 54 | `ErrDelegateSuspended` | ballot, retire | Ballot: retry after coordinators lift the suspension (§5.5), until vote end. Retire: not possible while suspended |
| 55 | `ErrDelegateRetired` | ballot, retire, status payload | Ballot: permanent. Retire: success, because the entry is already retired |
| 57 | `ErrDelegateBallotExists` | ballot | Success if the stored ballot equals the one submitted |
| 58 | `ErrInvalidBallot` | ballot | Permanent (wrong option count or an option out of range) |
| 59 | `ErrRegistrationClosed` | register | Retry later |
| 60 | `ErrRegistryFull` | register | Retry after params change |
| 61 | `ErrDelegateKeyReused` | register | Query `ProxyDelegateByKey(dk)`: if that entry's `dk` equals it, the registration landed and this is success |
| 62 | `ErrHandleTaken` | register | Permanent until the handle's live entry is retired |
| 64 | `ErrInvalidAttestation` | register | Attest again |
| 65 | `ErrInsufficientAttestations` | register | Attest again |
| 66 | `ErrInvalidDelegateKey` | register, params | Permanent |
| 68 | `ErrInvalidProxyParams` | params payload | Permanent |
| 69 | `ErrProxyDelegationDisabled` | 0x09, p=0 reveal, ballot | 0x09: resend (§11.3). Otherwise stop; the round is not a proxy round |

No 0x09 answer stops the wallet: every rejection, in any codespace, leaves the batch pending, and the wallet resends it until it lands or voting ends (§11.3).

Stateless payload failures: in `MsgSetProxyDelegationParams`, a verifier key that fails the key rule is 66 and every other stateless failure is 68; every stateless failure of `MsgSetProxyDelegationPause` and `MsgSetDelegateStatus` is 9.

## 7. Events

Attribute values follow the existing conventions: lowercase hex for bytes, decimal for integers, comma-joined lists without spaces, one event per message. Existing attribute keys are reused where they exist.

| Event | Attributes |
|---|---|
| `proxy_delegation_batch` | `vote_round_id`, `batch_digest`, `has_registration` (`true`/`false`), `nullifier_count` (the gov-nullifier count, as 0x07 emits it), `final_van_leaf_index`, `dc_leaf_indices`, `van_nullifiers`, `dc_count` |
| `delegate_registered` | `delegate_index`, `provider`, `handle`, `proof_id` |
| `delegate_retired` | `delegate_index`. Emitted by the retire op only |
| `delegate_ballot` | `vote_round_id`, `delegate_index`, `options` |
| `reveal_pool_share` | `vote_round_id`, `delegate_index`, `share_nullifier` |
| `proxy_pools_routed` | `vote_round_id`, `ballots` (count), `routed_pools` (count) |
| `delegate_status` | One event per listed delegate, no-ops included: `delegate_index`, `action`, `reason` |
| `proxy_delegation_params` | `verifier_count`, `verifier_threshold`, `max_delegates` |
| `proxy_delegation_pause` | `paused` |

The coordinator flow's own events (`propose_`, `approve_` and `execute_coordinator_action`) wrap the last three, as for every payload. A watcher that tracks retirement reads both `delegate_retired` and `delegate_status` with action 3.

## 8. Queries and REST

**gRPC (`proto/svote/v1/query.proto`).**

```proto
message QueryProxyDelegatesRequest { uint32 start_after = 1; uint32 limit = 2; }
message QueryProxyDelegatesResponse { repeated ProxyDelegateEntry delegates = 1; uint32 next_start_after = 2; }
message QueryProxyDelegateRequest { uint32 delegate_index = 1; }
message QueryProxyDelegateResponse { ProxyDelegateEntry delegate = 1; }
message QueryProxyDelegateByKeyRequest { bytes dk = 1; }
message QueryProxyDelegateByKeyResponse { ProxyDelegateEntry delegate = 1; }
message QueryProxyDelegateByHandleRequest { uint32 provider = 1; string handle = 2; }
message QueryProxyDelegateByHandleResponse { ProxyDelegateEntry delegate = 1; }
message QueryDelegateBallotsRequest { bytes vote_round_id = 1; uint32 start_after = 2; uint32 limit = 3; }
message QueryDelegateBallotsResponse { repeated DelegateBallotRecord ballots = 1; uint32 next_start_after = 2; }
message QueryProxyDelegationParamsRequest {}
message QueryProxyDelegationParamsResponse { ProxyDelegationParams params = 1; uint32 next_delegate_index = 2; }
```

- **Paging.** Pages return entries or ballots with index above `start_after`, in ascending order; `next_start_after` is 0 when done. `limit` 0 means 100; a `limit` above 1000 is `InvalidArgument` (HTTP 400).
- **Not found.** A single-entry query for an unknown index or key is `NotFound` (HTTP 404). `ProxyDelegateByKey` names the entry a key was registered to, retired or not. `ProxyDelegateByHandle` reads `0x1A 0x03`, so it names only a live entry: a handle without one is `NotFound`, and a handle that is not canonical is `InvalidArgument`. `DelegateBallots` for an unknown round is `NotFound`; a known round with no ballots returns an empty list.
- **Proposal 0.** `ProposalTally` rejects `proposal_id = 0` and ids above `len(proposals)`, so no query exposes a pool.
- `QueryProtocolCapabilitiesResponse` appends:
  - `uint32 proxy_delegation_version = 4;` (0 = off, 1 = tags 0x09 and 0x0B with 8 DC slots per registration);
  - `repeated CircuitFingerprint circuit_fingerprints = 5;`, where `message CircuitFingerprint { string name = 1; bytes vk_blake2b = 2; }` names `zkp1` to `zkp4`.

**REST (`/shielded-vote/v1/...`).**

| Method and path | Body or result |
|---|---|
| `POST /proxy-delegation-batch` | `MsgProxyDelegationBatch` JSON, decoded canonically. Answers `BroadcastResult{tx_hash, code, codespace, log, batch_digest}`: 200 when code is 0, 422 when CheckTx rejects, 400 for bad JSON or ValidateBasic |
| `POST /delegate-op` | `MsgDelegateOp` JSON, decoded canonically; the same `BroadcastResult` without `batch_digest` |
| `GET /delegates?start_after=&limit=` | `ProxyDelegates` |
| `GET /delegates/{index}` | `ProxyDelegate` |
| `GET /delegates/by-key/{hex}` | `ProxyDelegateByKey` |
| `GET /delegates/by-handle/{provider}/{handle}` | `ProxyDelegateByHandle` |
| `GET /delegate-ballots/{round_id}?start_after=&limit=` | `DelegateBallots` |
| `GET /proxy-delegation-params` | `ProxyDelegationParams` |
| `GET /protocol-capabilities` | gains fields 4 and 5 |
| `GET /height` | `{"height": "<decimal>"}`, the node's latest committed height |
| `GET /commitment-tree/{round_id}/latest` | gains `block_height`, the committed height its snapshot was read at |

- **Heights.** `/height` is ABCI Info's `last_block_height`, the version a query for the latest state reads. `block_height` on `/latest` is a top-level field beside `tree`; it is at or above `tree.height`, and a snapshot holds every leaf appended at or below it. Both are decimal strings, as the Cosmos SDK's `block_height` and `/tx/{hash}`'s `height` are; message fields keep §0's JSON numbers.
- **Dormancy.** The proxy routes answer 404 while dormant, `/height` included, and `/latest` answers today's exact bytes, without `block_height`.
- **A byte-identical resend** is answered from the node's transaction cache before any check runs, with HTTP 200, code 0 and the original tx hash. Clients treat that answer as "already submitted" and settle by the original hash or by proof, never as a new acceptance. The "always answered 2" and "always answered 57" rules of §5 hold for a re-signed or different transaction.

These serve the wallet's registry cache (§11.10), the attester's audit (§12.4), delegate tooling and explorers.

## 9. Genesis and the upgrade

**GenesisState** (`types.proto`) appends fields 19 to 21. The gaps at 2, 3 and 10 are not reused.

```proto
repeated ProxyDelegateEntry proxy_delegates = 19;
repeated DelegateBallotRecord proxy_delegate_ballots = 20;
ProxyDelegationParams proxy_params = 21;
```

Nullifier type 3 is accepted. The key records (`0x1A 0x02`), the handle records of live entries (`0x1A 0x03`) and `next_index` are not exported: import rebuilds them from the entries, `next_index` as one more than the highest entry index, or 1.

**Import validation:**
- entry indices run 1..N with no gaps, because the p=0 reveal check `d < next_index` assumes that, and N is at most 2,000;
- every entry's key passes the key rule, its handle and proof id are canonical, and its status is 0, 1 or 2;
- no two entries share a key, and no two live entries share a handle, so the rebuilt records agree with the entries;
- the params pass their stateless checks;
- every ballot names a flagged round and an existing entry, once per `(round, d)`, with one in-range option per proposal;
- a flagged round numbers its proposals from 1 and its options from 0, and its `pools_routed` is set exactly when it is TALLYING or FINALIZED.

**Upgrade.** v1.7.0 registers a no-op handler at a halt between rounds. Absent values read as fixed defaults: `next_index` as 1, params as unpaused with no verifiers and `max_delegates = 0`, so registration stays off until coordinators set params.

**Dormancy.** One const, `ProxyDelegationEnabled = false`, gates every new path until the activation commit sets it. The test-only `proxyenabled` build tag opens it, and CI runs the Go suites both ways. Gated:
- tag decode, and the new Msg handlers (§1 Registration traps);
- the p=0 branch, including ValidateBasic;
- routing;
- the new payloads and the round flag;
- capability fields 4 and 5;
- the new REST routes and fields (§8);
- any change to existing validation, including the registered result codes of §0 and the tree bound of §5.1.

Standard-transaction decoding is not gated: registering the new Msgs and adding `MsgCreateVotingSession` field 12 lets a dormant binary decode transactions that an older binary rejects, so their result codes and gas differ. That is safe only because dormant code never runs beside an older binary on a live chain: v1.7.0 activates at a halt between rounds. While dormant, the new handlers reject and the keeper refuses the round flag.

## 10. ZKP4 interface

- **Public inputs, 11, in this order:** `van_nullifier, r_vpk_x, r_vpk_y, vote_authority_note_new, delegation_commitment, vote_comm_tree_root, vote_comm_tree_anchor_height, voting_round_id, ea_pk_x, ea_pk_y, dc_slot_nullifier`.
  - The `Instance` constructor in voting-circuits is the only source of this order; the chain FFI builds instances through it.
  - A golden vector pins the packing.
- **DC hash:** `DC = Poseidon5(DOMAIN_VC, voting_round_id, shares_hash, 0, d)`, with proposal 0 as a constant cell.
- **Slot nullifier:** `dc_slot_nullifier = Poseidon4(nk, TAG("proxy delegation slot") + dc_slot, voting_round_id, van_comm_rand)`, with `dc_slot ∈ [0, 8)` (`DC_SLOT_BITS = 3`).
- **Constants pinned by tests:**
  - `POOL_PROPOSAL_ID = 0`;
  - `DC_SLOT_BITS = 3` and the slot tag text;
  - PRF domains keyed by `dc_seed`: 0x10 El Gamal, 0x11 blind, 0x12 shuffle, 0x13 remainder, and 0x14 El Gamal for the single-share layout;
  - `DOMAIN_VC`.
- **Share secrets:**
  - `PRF(domain, i) = BLAKE2b-512(personal = "ZcashVoteProxyEx", dc_seed || u8(domain) || u8(i))`.
  - Domain 0x10 gives each share's El Gamal randomness, 0x11 its blind, 0x12 (index 0) the shuffle seed, and 0x13 the remainder weight of each absolute slot; 0x14 gives the El Gamal randomness of the single-share layout.
  - Layout 0 splits `w` with ZKP2's denomination split, weighting the remainder by 0x13, then shuffles with ZKP2's Fisher-Yates shuffle seeded by 0x12.
  - Layout 1 uses `shares = [w, 0 × 15]`, unshuffled.
  - As in ZKP2, an El Gamal randomness that reduces to zero becomes one.
  - All 16 ciphertexts and commitments are built in both layouts, because `shares_hash` covers 16.
- **Size and fingerprint.** A ZKP4 proof is at most `MaxProofSize` (15,360 bytes); about 11,008 bytes was measured. A test pins ZKP4's fingerprint, published as `zkp4` in capability field 5. Every `vk_blake2b` in field 5 is BLAKE2b-256 of `format!("{:?}", vk.pinned())`.

## 11. Client formats

### 11.1 DC secrets

```
hrk     = BLAKE2b-256(key = hotkey_secret, personal = "ZVoteProxyHRK_v1", net_u8 || round_id)
dc_seed = BLAKE2b-256(key = hrk, personal = "ZVoteProxySeed01", van_nf(32) || d_le32 || w_le64 || layout_u8)  // 45-byte input
```

- `hotkey_secret` is the 32-byte Orchard spending key of the round hotkey that owns the VAN, as bytes.
- `net_u8` is zcash_voting's network rank: 0 for mainnet, 1 for testnet and 2 for regtest.
- `layout` is 0 (standard, 16 shares) or 1 (single share).

### 11.2 Delegate key

- **The key.** The delegate key is 32 random bytes from the OS RNG, the RFC 8032 secret seed. `dk` is its Ed25519 public key. The app generates it and keeps it in its existing secure storage, the way it keeps vote hotkeys. A device holds one delegate key and one entry.
- **Export and import.** The seed exports and imports as one string: bech32m with HRP `zvdsk` over the 32 bytes, so it reads `zvdsk1…`. Decoding accepts only the canonical lowercase form, with zero padding bits. The app also shows it as a QR code whose content is exactly that string. Import is how a key moves to another device or is recovered.
- **Signing.** The app signs with the key after its own re-authentication gate.
- **Display.** `dk` is bech32m with HRP `zvdk`, so it reads `zvdk1…`. Decoding accepts only the canonical lowercase form, with zero padding bits. The fingerprint is the first 16 data characters after `zvdk1` (80 bits), grouped as `xxxx-xxxx-xxxx-xxxx`. The canonical display is `#17 · qqqs-yqcy-q5rq-wzqf`.

### 11.3 0x09 client lifecycle

- **Batch identity.** zcash_voting tracks a batch by `batch_digest` (§4.1), which excludes `expiry_height` and the D1 signatures. So re-signing with a new expiry is a retry of the same batch.
- **Expiry.** The wallet sets `expiry_height = h_sign + PROXY_BATCH_EXPIRY_BLOCKS`, where `PROXY_BATCH_EXPIRY_BLOCKS = 500` (about 10 minutes at about 1.2 s blocks). The constant is fixed so the value never identifies a wallet. The wallet re-signs only after the previous attempt's expiry has passed, so one signed attempt is live at a time, and it persists each new expiry and its signatures before the POST that carries them.
- **Heights.** A wallet reads the latest height from `GET /shielded-vote/v1/height` and, on a node whose answer gives no height (404, missing or malformed field), from the Cosmos SDK validator-set route's `block_height`, taking the highest any endpoint reports.
- **Finality.** A committed delegation is final, like a sent vote. Nothing returns its weight.
  - The wallet sends each bundle's batch, and resends the same batch, re-signed after each expiry, until it lands or voting ends. A pause, any other rejection and a submission without a hash all keep it pending. A committed batch that was never sent is sent when the job next runs.
  - A resend of a batch that already landed is answered code 2, and the exact-tree recovery pass confirms it. So a batch that landed despite a rejection is still found.
  - If a bundle's batch hasn't landed when voting ends, that bundle isn't counted this round, its kept ballots included, since they are cast from the batch's successor VAN. The wallet reports it as not counted.
- **Split-off.** After a relayer lifts the registration out of a batch, a batch retry gets code 2. Recovery first looks for the registration's standalone VAN leaf, appended only by a standalone 0x02; finding it means the registration landed alone.

### 11.4 Availability

`proxy::availability` returns the first result that applies, in this order:

| Result | When |
|---|---|
| `Unsupported` | `WalletCapabilities.proxy_delegation` lacks `"v1"`, chain capability field 4 is not 1, field 5 lacks any of `zkp1` to `zkp4` or one differs from the compiled fingerprint, or the round's field 31 `enabled` is not set |
| `Paused` | `proxy_dc_paused` |
| `AlreadyVoted { used_elsewhere }` | A bundle of the wallet has a cast in the round that is on the wire, submitted without a hash or confirmed, or a vote whose helper share plan is saved (`used_elsewhere = false`); or the account's gov nullifiers are spent with no local state (`used_elsewhere = true`) |
| `TimingUnavailable` | The host has no `ceremony_phase_start` and `vote_end_time` for the round |
| `VoteEnded` | `now ≥ vote_end_time` |
| `Available` | None of the above |

- The proxy flag and the round's timing come from the sources ordinary votes already use. The wallet proves no round record: the chain rejects a batch for a round without the flag before it spends the VAN (§5.1 step 2).
- The last-moment window that picks the single-share layout (§15.2) is computed from the same `ceremony_phase_start` and `vote_end_time`.
- An undispatched vote is not `AlreadyVoted` until its helper share plan is saved: committing an allocation holds the bundle from casts, and the vote flow later plans the user's answers against the remainder. A saved plan has already fixed which share the round sends at once. A delegation that then took another bundle's whole weight could move that share onto the saved vote, whose plan lacks it, and the round's vote shares would no longer deliver. So preview and commit refuse with `AlreadyVoted` in the same case.

### 11.5 Marker text

```
zcash-vote-delegate v1 <zvdk1…>
```

- **Matching.** The post's whole text, with leading and trailing whitespace trimmed and each run of whitespace collapsed to one space, must equal this string exactly, with the key in place of the placeholder. The comparison is case-sensitive. "Whitespace" is Unicode White_Space, which Rust's `char::is_whitespace` and Go's `unicode.IsSpace` share; a zero-width space is not whitespace.
- **Parse errors**, checked in this order:
  1. the normalized text does not start with the word `zcash-vote-delegate` (`NotAMarker`);
  2. the second word is not `v1` (`UnsupportedVersion`);
  3. what remains is not exactly one word (`Malformed`);
  4. that word is not the canonical `zvdk1` string of a key that passes the key rule (`InvalidKey`).
- **GitHub.** A gist's single file carries the same text.

### 11.6 Proven keys in Vizor's participation reader

The reader proves a key in the `vote` store with an IAVL existence or non-existence proof under a verified header and returns the value bytes. It has two key classes:

| Class | Key | A present value must be | Used by |
|---|---|---|---|
| Governance nullifier | `[0x01, 0x00] || round(32) || nf(32)` | exactly `[1]` | Availability |
| Registry entry | `0x1A 0x01 || u32be(d)`, `d ≥ 1` | 1 to 65,536 bytes | The pre-proving check (§11.11) |

- **Absence.** An entry proven absent is `NotFound`.
- **Decoding.** zcash_voting decodes an entry as `ProxyDelegateEntry`. It accepts only the deterministic encoding and refuses unknown fields, so adding a field to the chain record is a contract change.
- **Batching.** One evidence answer may prove up to 8 entries.

### 11.7 Lookalikes

`fold(h)`:
1. lowercase ASCII letters only, because Go and Rust lowercase other characters differently;
2. strip one leading `@`;
3. delete `_` and `-`;
4. map `0 → o`, `1 → l` and `i → l`;
5. replace `rn → m`, `vv → w` and `cl → d` in a single left-to-right pass without overlap.

Two handles are lookalikes when their folds are within optimal-string-alignment (restricted Damerau-Levenshtein) distance 1. Vectors pin the order of the steps.

**Lookalike set.** A wallet warns with the verified wallet list's handles (§12.3) plus its `reserved_names`, and nothing else.
- A search result, a pasted or scanned delegate, or a chosen delegate whose handle is a lookalike of a set member shows "Looks like @member".
- A wallet list row is not warned about its own handle: no warning when the delegate is that row, with the same index, provider and handle.

### 11.8 Helper API for pool shares

DC shares use the existing share-submission endpoint and `VoteShareWire` with `proposal_id = 0` and `vote_decision = d`. The helper answers:

| Case | HTTP status | Body |
|---|---|---|
| Accepted | as today | as today |
| `proposal_id = 0` in a round without the proxy flag | 422 | `{"status":"error","error":"proxy_delegation_disabled"}` |
| The leaf at `tree_position` is absent | 503 with `Retry-After` | `{"status":"error","error":"leaf_not_found"}` |
| `d = 0` or `d ≥ next_index` | 422 | `{"status":"error","error":"delegate_not_found"}` |

- For `proposal_id = 0`, the helper checks `vote_decision ≥ 1` and skips today's `vote_decision < 8` bound. It verifies the commitment first, then checks the index.
- **Client handling.** A wallet reads these answers as it reads any helper answer: a 422 is that helper's definite failure, and a 503 `leaf_not_found` is an unknown outcome that a later re-post reconciles. An unknown outcome counts only at a helper still in the configured fleet.
- **Handed off.** A DC share is handed off once `min(target_count, current fleet size)` configured helpers hold it, and at least one does.
- **After hand-off.** The wallet tracks a handed-off DC share as it tracks a vote share a helper accepted. It polls the share's status and confirms its reveal through the configured helper quorum. Before the resubmit cutoff it sends a share no helper revealed in time to another helper with `submit_at = 0`, derived again from the round hotkey, since a DC's share secrets are never stored. Without the hotkey it only polls.

### 11.10 Finding a delegate

**Registry cache.** The wallet downloads the whole registry from an RPC node through `GET /shielded-vote/v1/delegates`, 1,000 entries a page (§8), and caches it per chain. Downloading all of it means the server never learns which delegate the user looks at. The cache is not proven: it drives display and lookup, and the commit proves what it relies on (§11.11). The wallet chooses when to refresh it.

**Browse.** Browse shows the rows of the wallet's verified list (§12.3), in list order, each with its reviewed status (§11.11) and the row's name. Without a verified list, browse is empty.

**Search.** Search is exact handle search over the cached registry. The query is trimmed, loses one leading `@` and is ASCII-lowercased, then compared with the canonical handle of every live entry; each match shows, labelled X or GitHub. A result shows the handle, provider, the fingerprint of the entry's key and an identicon. If the wallet list has a row for the same index, the result also shows that row's name and reviewed status; otherwise it shows "Not reviewed" with no name. Lookalike warnings apply (§11.7).

**Paste or scan.** After trimming whitespace, the wallet accepts:
1. a marker post's text (§11.5), whose key is looked up;
2. a `zvdk1…` key, typed, pasted or scanned from a QR code whose content is exactly that string;
3. an index, `17` or `#17`, in canonical decimal of at least 1;
4. a fingerprint: 16 bech32 characters in either case, grouped by `-` or not.

A key or fingerprint matches the cached entry with that key, and an index matches its entry. The wallet never fetches a post.

There is no handle index and no server-side search: the handle a wallet shows at confirm comes from the proven entry.

### 11.11 Confirm and the pre-proving check

A choice carries what the user picked: `{delegate_index, percent_bps, provider, handle, dk}` (§15.1). At confirm, immediately before commit, the wallet proves the entry at each chosen index (§11.6) and checks it against the choice. Each delegate gets the first verdict that applies:

| Verdict | When |
|---|---|
| `CannotVerify` | No proof could be obtained: none was served, it does not verify, or the proven value does not decode |
| `NotFound` | The entry is proven absent |
| `Mismatch` | The proven entry's `dk`, `provider` or `handle` differs from the choice's |
| `Suspended` | The proven entry is SUSPENDED |
| `Retired` | The proven entry is RETIRED |
| `Ok` | The proven entry is ACTIVE with exactly the choice's `dk`, `provider` and `handle` |

Commit refuses unless every verdict is `Ok`.

**Confirm screen.** Each delegate shows its index, provider and handle and its key as `#17 · xxxx-xxxx-xxxx-xxxx`, all from the proven entry. Reviewed status and lookalike warnings (§11.7) are recomputed from the proven values.

**New this round.** An entry with `index ≥ next_delegate_index_at_creation` registered after the round was created. That is a display label only.

**Reviewed.** A delegate shows as reviewed only while the wallet's verified list has a row for its index and the entry at that index is ACTIVE with the row's `dk`, `provider` and `handle`. Otherwise it shows as not reviewed. Browse and search compute this from the registry cache; confirm computes it from the proven entry. Reviewed status is a label. On its own it never blocks or allows a commit.

### 11.12 Delegate app

How the delegate app reads chain answers:
- A `/delegate-op` answer of code 0 means only that CheckTx accepted the op. A registration, including one answered code 61, settles when `GET /delegates/by-key/{hex}` names an entry whose proven value holds `dk`.
- A retirement counts on code 55 or when the re-proven entry is RETIRED.
- Code 57 counts as success only when the round's ballot list shows the stored ballot.
- Only an HTTP 404 whose JSON error names the key as never registered means "unregistered"; a dormant node's 404 or any other answer is "unavailable".
- An attestation is accepted while unexpired, with `not_before` at most 300 s after the device clock.
- A new ballot is signed only before `vote_end_time`; a stored ballot is resent byte for byte at any time.

## 12. Off-chain formats

### 12.1 Dynamic config extension

```json
"extensions": {
  "proxy_delegation_v1": {
    "verifier_api_urls": ["https://<attester>"],
    "proof_rpc_urls": ["https://<rpc-a>", "https://<rpc-b>"]
  }
}
```

- **Unsigned.** The URL lists are unsigned wrapper fields. Each list holds distinct https URLs: at least one `verifier_api_urls` and at least two `proof_rpc_urls`. Config CI checks the shape.
- **Other members.** Wallets ignore members of the extension they do not know; config CI refuses them.
- **Name.** The key's `v1` is the chain's proxy delegation version (capability field 4).
- **Wallet list URL.** It is not here. It comes from the wallet build (§12.3).

### 12.3 Wallet list (`<env>/wallets/<wallet_id>/list.json`)

A wallet list is one long-lived signed file per wallet, per chain, holding that wallet's curated delegates. A row carries only the curated name, which the wallet's signer chooses, and the entry's identifiers, so a wallet shows nothing a delegate wrote. The wallet re-signs the list only when its picks, a name or a delegate's entry change.

**Hosting.** By default the list lives in the config repo at `<env>/wallets/<wallet_id>/list.json` (`env` is `prod` or `stage`) and is served by the existing gateway and mirrors, for example `https://voting.valargroup.dev/prod/wallets/vizor/list.json`. The URL comes from the wallet build, so a wallet can host its list elsewhere without a client change.

```json
{
  "format": "zcash-vote-wallet-list/v1",
  "wallet_id": "vizor",
  "chain_id": "zvote-1",
  "version": 3,
  "issued_at": 1790000000,
  "reserved_names": ["zcash", "electriccoin", "zodl"],
  "delegates": [
    {
      "index": 17,
      "provider": 1,
      "handle": "alice",
      "dk": "zvdk1…",
      "name": "Alice Example"
    }
  ],
  "signatures": [{ "key_id": "valargroup", "alg": "ed25519", "sig": "<base64>" }]
}
```

**Fields.** Every member is required. Each rule's error name follows it.

| Field | Rule | Error |
|---|---|---|
| `format` | Exactly `zcash-vote-wallet-list/v1` | `UnsupportedFormat` |
| `wallet_id` | 1 to 32 bytes: a lowercase ASCII letter, then lowercase letters, digits or `-`. It names the wallet, and the hosting path | `InvalidWalletId` |
| `chain_id` | 1 to 64 printable ASCII characters other than space: the chain whose indexes the rows name | `InvalidChainId` |
| `version` | A `u64` of at least 1, strictly increasing across a wallet's lists on one chain | `InvalidVersion` |
| `issued_at` | Unix seconds when the list was signed, at least 1. Display only; there is no expiry | `InvalidIssuedAt` |
| `reserved_names` | 0 to 1,000 names, each 1 to 64 lowercase ASCII letters, digits, `_` or `-`, none repeated. They feed lookalike warnings (§11.7) | `TooManyReservedNames`, `InvalidReservedName`, `RepeatedReservedName` |
| `delegates` | 0 to 1,000 rows | `TooManyDelegates` |
| `index` | A `u32` of at least 1. Rows are strictly ascending by index | `InvalidIndex`, `IndexNotAscending` |
| `provider` | 1 for X, 2 for GitHub | `InvalidProvider` |
| `handle` | The entry's handle, canonical for the provider (§2.5) | `InvalidHandle` |
| `dk` | The reviewed key: the entry's key as its canonical `zvdk1…` string (§11.2) | `InvalidKey` |
| `name` | 1 to 50 Unicode scalar values in NFC, with no control character (Unicode category Cc) and no bidirectional formatting character (U+061C, U+200E, U+200F, U+202A to U+202E, U+2066 to U+2069) | `InvalidName` |
| `signatures` | 1 to 8 objects in the dynamic config's signature shape: `key_id` of 1 to 64 bytes, `alg` exactly `ed25519`, and `sig` canonical base64 of 64 bytes. Not signed | `Malformed` |

The `dk` rule is the string's form only. A key that fails the key rule can never be registered, so its row is never reviewed.

**Signed payload.** Every signature covers:

```
WalletListPayloadV1 = "zcash-shielded-vote:wallet-list:v1"      // 34 ASCII bytes
                      || lp8(wallet_id) || lp8(chain_id)
                      || u64le(version) || u64le(issued_at)
                      || u16le(r) || r × lp8(reserved_name)
                      || u16le(n) || n × row
row                 = u32le(index) || u32le(provider)
                      || lp8(handle) || dk(32)                    // the key's 32 bytes
                      || lp8(utf8(name))
```

- `lp8(s)` is one length byte, then the bytes. The field rules keep every length in range: a name is at most 200 bytes.
- Lists and rows are encoded in the JSON's order, so the order of `reserved_names` is signed as written.
- **Why this encoding.** Raw fields and little-endian integers follow the round-auth v2 payload. The JSON stays readable in a pull request diff, any JSON formatting verifies, and no JSON canonicalization is needed. The admin UI signs the payload bytes with the Keplr-derived Ed25519 flow it uses for round entries, then writes `{ key_id, alg: "ed25519", sig }`.
- **Domain separation.** The domain is prefix-free with the config family's other signing domain, `zcash-shielded-vote:round-auth:v2`. Round-auth v1 signs exactly the 32-byte `ea_pk`, and every list payload is longer than 34 bytes. So a list signature can never pass as a round attestation, and the reverse.

**Signer pinning.** Each wallet build pins, per chain, exactly which keys may sign its list, as pairs of `key_id` and 32-byte Ed25519 public key compiled into the app. A list verifies only through a signature object that carries a pinned `key_id` and verifies over the payload under that pinned public key (ZIP-215, §0). Signatures by any other key are ignored, even keys the static config trusts for round entries, so a static config change can never widen who signs a wallet's list.

Vizor pins one key per chain and accepts no other:
- mainnet (`zvote-1`): `valargroup`, `8oQiUWq6QDGnAgRw6U3YhnXb6JLFSXauYnWHIFmJRcw=`, the vote-manager Keplr-derived key in the prod static config;
- stage (`svote-1`): `valar-test-keplr-derived`.

**Verification.** A wallet reports the first problem, in this order:
1. longer than 1 MiB: `TooLarge`. Written with a 2-space indent and only the escapes JSON requires, the largest list the field rules allow is 492,742 bytes, so re-indenting a valid list keeps it well under the limit;
2. not JSON, a top level that is not an object, a repeated top-level key, or a `format` that is absent or not a string: `Malformed`;
3. a `format` other than `zcash-vote-wallet-list/v1`: `UnsupportedFormat`;
4. at any level: an unknown member, a repeated key, a required member absent, `null` anywhere, a wrong type, a signature that is not canonical base64, or a `signatures` member that breaks its rule: `Malformed`. Unknown members are refused because everything but `signatures` is signed, and a reader must never take unsigned content for part of the list;
5. the field rules, in the table's order, row by row: the rule's error;
6. a `wallet_id` other than the one the wallet pins: `WrongWallet`;
7. a `chain_id` other than the wallet's chain: `WrongChain`;
8. no signature from a pinned key verifies: `NoPinnedSignature`.

**Versions and storage.**
- The wallet stores, per `wallet_id` and `chain_id`, the newest verified list and its version.
- It replaces the stored list only with a verified list of a higher version. A verified list with the stored version and a different payload is ignored and reported, and a lower version is ignored.
- Browse shows the stored list while a fetch fails or does not verify. Without one, browse is empty.
- There is no expiry and no freshness gate: the list is long-lived, and every safety fact comes from the chain. A mirror can serve a fresh install an older signed list, which can only show a delegate the wallet has since removed, still subject to every chain check at confirm.

**Config repo operations.** These are not wire formats, but the format relies on them:
- **Signing.** The signer signs the list on the admin UI's Sign Wallet List page, or with `voting-config wallet-list sign`, and opens the pull request by hand.
- **CI** checks each `<env>/wallets/<wallet_id>/list.json` with `voting-config wallet-list verify`: the format and field rules, a valid signature by the key the wallet pins for that environment, a `wallet_id` equal to its directory name, a `chain_id` equal to the environment's chain, and a `version` that rose whenever the payload changed. CI refuses a change that removes a list existing at the base revision; to withdraw a list, publish an empty one with a higher `version`. The Pages build runs the same verification before it publishes. A bad merge can only make the list fail verification, which leaves browse on the stored list or empty; it can never list someone unsigned.
- **Re-review alert.** A scheduled check runs `wallet-list verify --chain-check` and alerts the wallet's team when a row is no longer reviewed (§11.11): its entry is missing, suspended or retired, or has another provider, handle or key.

### 12.4 Attester API

The attester (delegate-verifier) is one small stateless service. It checks that a public post carries the marker for a key and signs ATTEST. The delegate app speaks this API. Every answer is JSON.

**`POST /v1/attest`.** Request:

```json
{ "provider": 1, "proof_url": "https://x.com/<handle>/status/<id>", "dk": "zvdk1…" }
```

For GitHub, `provider` is 2 and `proof_url` is the gist URL.

- **X.** The attester reads the post through the free oEmbed endpoint. The post's whole text must be the marker for `dk` (§11.5). `handle` is the lowercased author handle oEmbed returns. `proof_id` is the post id.
- **GitHub.** The attester reads the gist through the public API. The gist must be public: a secret gist answers `post_not_found`, as a protected X post does. Its single file must be the marker for `dk`. `handle` is the lowercased owner login. `proof_id` is the gist id.
- **Signature.** It signs ATTEST (§4.2) with `not_before = now − 300` and `expires_at = now + ttl`. `ttl` is configured, 3,600 s by default, and must be at least 600 s and at most 258,900 s, so that `expires_at − not_before` stays within the chain's 72 h (§2.2).

Response 200:

```json
{
  "provider": 1,
  "handle": "alice",
  "proof_id": "1790000000000000000",
  "attestations": [ { "verifier_id": 1, "not_before": 1790000000, "expires_at": 1790003900, "sig": "<base64>" } ]
}
```

The app then builds `DelegateRegister` from these fields and its `dk`, signs REGISTER with the delegate key and submits 0x0B.

**Errors.** An error answer names its code in `error`.

| Status | `error` | Meaning |
|---|---|---|
| 400 | `bad_request` | Malformed request or unsupported provider |
| 404 | `post_not_found` | The provider has no such post or public gist, or the post is protected |
| 409 | `marker_mismatch` | The post's text is not the marker for this `dk` |
| 429 | `rate_limited` | The per-IP bucket or the global daily cap is exhausted; carries `retry_after`, the unix seconds from which to try again |
| 503 | `provider_unavailable` | X or GitHub failed, the provider's answer was over its size cap, or the attester is busy (64 requests in flight); retry later |

**What it keeps.** Nothing durable: no database, no numeric account id, no paid X API, no chain client in the request path. The signing key is a file key. Production sets a GitHub token for the gist API. Its limits are in memory:
- a per-IP token bucket, checked before the request body is read;
- a global daily attestation cap (configured, 200 by default), where each `(provider, proof_id, dk)` counts at most once per UTC day;
- a repeated request for the same `(provider, proof_id, dk)` is answered with the attestations already signed for it while at least 300 s of their validity remain;
- at most 64 requests in flight;
- 5 s deadlines for reading the request's headers and body, and a size cap on every provider answer.

**One entry per handle is the chain's rule.** Two attestations for one handle are harmless: the second registration fails with `ErrHandleTaken`.

**Audit.** The attester's `audit` subcommand reads the registry from a chain REST URL and re-runs the check for every live entry's `(provider, proof_id)`. It prints findings (`post_missing`, `handle_changed`, `marker_mismatch`) and keeps no state. It waits out provider rate limits within a configured budget. Exit codes:
- 0: clean, every live entry checked with no finding.
- 1: at least one finding. Findings win over an incomplete audit.
- 2: incomplete. An entry went unchecked because its proof couldn't be read or was over the size cap, or the audit stopped on a chain failure or an exhausted rate-limit budget.

## 13. Golden vectors

The vector set is three files. Each has one home repo and is copied wherever another repo checks it. Any change to a file is made together with every copy and every test that pins its hash.

| File | Home | SHA-256 |
|---|---|---|
| `proxy_delegation_v1.json`, the client file | zcash_voting, `zcash_vote_delegate/test-vectors/` | `e50fb0e6978b82a114c8c26ff84648cf35e77400505bb2a3584e39b37262a918` |
| `proxy_delegation_chain_v1.json`, the chain file | vote-sdk, `testutil/testdata/` | `fdfe5131df8bb8a37b9025567e3b6d8cd41e04a106f6d2f3102952efa5f3f244` |
| `proxy_delegation_circuit_v1.json`, the circuit file | voting-circuits, `test-vectors/` | `6293658ba866a7738f5d34ae5364559938b8f3b3b8175bbd77ac6b3431e5b883` |

**The client file.** Its inputs were written once and are edited by hand. `make delegate-vectors` in zcash_voting recomputes every expected output from its inputs through the crate's public API. Its sections:
- `d1`: with and without a registration, 1 and 8 DCs; each with `d1` and `batch_digest` (§4.1).
- `handle`, `proof_id`: canonical and refused forms for each provider (§2.5).
- `register`, `attest`, `retire`, `ballot`: each digest with a signature over it (§4.2).
- `domains`: the prefix-free `SVOTE_` set (§4.3).
- `kv_keys`: entry, used-key, handle, next-index, params, ballot and pool share count keys (§3.2).
- `entry`: `ProxyDelegateEntry` values with the entry the strict client decoder reads, or its refusal (§11.6).
- `ed25519_keys`: keys that pass and fail the key rule, and ZIP-215 signature cases (§0).
- `delegate_key`: the `zvdk1…` and `zvdsk1…` strings, the fingerprint and the display form of a key, and which strings decode and import (§11.2).
- `dc_secrets`: `hrk` and `dc_seed` for both layouts (§11.1).
- `marker`: valid and refused marker texts (§11.5).
- `lookalike`: folds and pairs with their verdicts (§11.7).
- `config_domains`: the config family's signing domains (§12.3).
- `wallet_list_payload`: list fields and a signer seed, giving the payload, the signature and the published document, or the first rule a list breaks (§12.3).
- `wallet_list_document`: a fetched document with the pinned wallet, chain and keys, giving the verified payload and signer or the error, including a signature that only ZIP-215 accepts (§12.3).
- `errors`: name, code and codespace (§6).

**The chain file** is committed, and vote-sdk's tests recompute every expected value from its inputs, so changing its format means editing the file. `entry_value` holds `ProxyDelegateEntry` values as the chain stores them. `routing` holds ρ derivations and the identity-C1 case they block (§5.6).

**The circuit file** holds `zkp4_instance`, the public-input packing with and without a registration (anchor 0), and `share_secrets`: for given `dc_seed`, `w` and layout, the PRF outputs, the 16 shares, their El Gamal randomness and blinds (§10).

**Conventions.** Most sections are lists of `{name, inputs, expected}` cases. Bytes are lowercase hex. A nested error keeps only its outer name (for example `InvalidKey` for a marker whose key fails the key rule); each implementation still checks the specific cause in its own tests.

**vote-sdk's copies** live in `testutil/testdata/`. Of the client and circuit files vote-sdk vendors only the sections Go and the admin UI implement, each byte for byte as the canonical file holds it: every client section but `dc_secrets`, `marker` and `lookalike`, and the circuit file's `zkp4_instance`. `x/vote/types/proxy_vectors_test.go` pins each copy's own SHA-256.

## 14. Shared crate

`zcash_vote_delegate` is a backend-neutral member of the zcash_voting workspace, published on crates.io for Vizor. It holds the client formats of this contract in one place: the registry digests and D1, the marker parser, the delegate key encodings and the fingerprint (§11.2), the lookalike fold, the wallet list payload and its verification (§12.3), the `ProxyDelegateEntry` decoder (§11.6) and the client vector file (§13).

## 15. Client API and vocabulary

### 15.1 Client API

zcash_voting's `wire` module defines the client types, and Vizor's bridge exposes them to Dart as they are. This contract fixes what they mean across repos:

- **Availability** is one of the results of §11.4.
- **A choice** is `{delegate_index, percent_bps, provider, handle, dk}`, filled from what the user picked: the delegate's index, its share in basis points, and the provider, handle and key the wallet showed for it. A wallet picks 1 to 8 delegates. The choices' shares total at most 10,000, and the rest is kept for the user's own vote.
- **The pre-proving verdicts** are `Ok`, `Suspended`, `Retired`, `NotFound` and `Mismatch`, plus `CannotVerify` when no proof could be obtained (§11.11). Only `Ok` lets a commit through.
- **Delegation is final and once per round.** A second allocation in a round is refused.
- **Stages.** A delegation is `Preparing`, `Submitting`, `Included`, `HandedOff` or `NotCounted`.
  - A DC is `HandedOff` once every share is definitely accepted by its helpers (§11.8), even while an immediate share 0 awaits its reveal.
  - After close, only a handed-off DC counts, and its stage stays `HandedOff`; any other is `NotCounted`. A pending share 0 reveal after close doesn't change the stage; it only gates the job's completion.
  - A delegation spanning bundles is `NotCounted` if any of its DCs is, otherwise its least-advanced DC's stage.
- **The delegate's ballot** shows as `NoBallotYet`, `Submitted`, `Counted` or `NotCounted`.
  - `Counted` describes the delegate's ballot: it means that ballot was accepted. Whether this delegation's shares reached helpers is its own stage, so a delegation can show ballot `Counted` with stage `NotCounted`, for example when its batch never landed.
  - Neither proves that every delayed share was revealed before close. The chain adds each reveal on its own, so if no helper reveals a share in time, that part of the weight never reaches the pool, and the wallet still shows `HandedOff` and `Counted`. The wallet tracks reveals after hand-off and resends a share no helper revealed (§11.8), but the status stays this lean by choice: it shows no reveal count.
- **Document sizes.** A wallet reads a wallet list of at most 1 MiB (§12.3) and a proof answer of at most 8 MiB.

### 15.2 Vocabulary

| Term | Meaning |
|---|---|
| Registration | Today's ZKP1: notes into a VAN. Not the delegate's on-chain registration, which is "delegate registration" |
| Proxy delegation | The feature: weight handed to public delegates through DCs |
| DC | Delegation commitment, the leaf a delegator creates |
| Pool[d] | The El Gamal accumulator `TallyKey(round, 0, d)` |
| Delegate ballot | One delegate's one public, complete vote for a round (0x0B ballot). New code always says "delegate ballot" |
| Ballots (weight) | The weight unit: one ballot is 12,500,000 zatoshi (0.125 ZEC). `W` and `w` count these. Distinct from the existing `OptionSummary.ballot_count`, which counts revealed shares |
| Routing | Adding each counted pool into its delegate ballot's options at the tally transition (§5.6) |
| Live entry | An entry that is ACTIVE or SUSPENDED (§3.3) |
| Handed off | Every share of a DC definitely accepted by its helpers (§11.8) |
| Last-moment window | The final `min(40% of the round, 6 h)` before `vote_end_time`; layout 1 is used inside it |
| Wallet list | One wallet's signed list of curated delegates (§12.3) |
| Registry cache | The wallet's unproven copy of the whole on-chain registry (§11.10) |
| Reviewed | A delegate whose live entry matches its row in the wallet's list (§11.11) |
| Attester | The service that checks a marker post and signs ATTEST (§12.4) |
