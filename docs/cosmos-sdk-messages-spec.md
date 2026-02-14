# Cosmos SDK Messages Specification — ZCash Governance Voting Protocol

> Derived from **Gov Steps V1** (protocol spec) and the **Wallet SDK Operations → Cosmos SDK Messages** FigJam board.

---

## Table of Contents

1. [Overview](#1-overview)
2. [Type Definitions](#2-type-definitions)
3. [Chain State](#3-chain-state)
4. [Messages](#4-messages)
   - 4.1 [MsgCreateVotingSession](#41-msgcreatevotingsession) — Phase 0
   - 4.2 [MsgDelegateVote](#42-msgdelegatevote) — Phase 2
   - 4.3 [MsgCastVote](#43-msgcastvote) — Phase 3
   - 4.4 [MsgRevealShare](#44-msgrevealshare) — Phase 5
   - 4.5 [MsgSubmitTally](#45-msgsubmittally) — Tally
5. [Queries](#5-queries)
6. [State Transitions Summary](#6-state-transitions-summary)
7. [Error Codes](#7-error-codes)
8. [Open Questions](#8-open-questions)

---

## 1. Overview

The vote chain is a Cosmos SDK application chain that processes three core transaction types corresponding to the three ZKP circuits in the protocol:

| Message           | Phase | Submitter       | ZKP    | Purpose                                           |
| ----------------- | ----- | --------------- | ------ | ------------------------------------------------- |
| `MsgDelegateVote` | 2     | Wallet (client) | ZKP #1 | Delegate mainchain ZEC notes to a voting hotkey   |
| `MsgCastVote`     | 3     | Wallet (client) | ZKP #2 | Cast an encrypted vote on a proposal              |
| `MsgRevealShare`  | 5     | Helper server   | ZKP #3 | Reveal one encrypted share for tally accumulation |

Plus two administrative messages:

| Message                  | Phase | Submitter            | Purpose                                           |
| ------------------------ | ----- | -------------------- | ------------------------------------------------- |
| `MsgCreateVotingSession` | 0     | Governance authority | Initialize a voting round with parameters         |
| `MsgSubmitTally`         | Tally | Election authority   | Submit decrypted aggregate with correctness proof |

### Protocol Flow (from Figma)

```
Wallet SDK (Client)                    Helper Server              Vote Chain
───────────────────                    ─────────────              ──────────
Identify notes at snapshot
Sample voting hotkey
Construct dummy action
Keystone signs action
Gather witnesses (PIR)
Generate ZKP #1
        │
        ├──── MsgDelegateVote ──────────────────────────────────► verify ZKP #1 + sig
        │                                                         insert VAN into tree
        │                                                         record gov nullifiers
        │
User picks vote choices
Binary decompose weight
Encrypt shares (El Gamal)
Construct VAN nullifier + new VAN + vote commitment
Gather VAN Merkle path
Generate ZKP #2
        │
        ├──── MsgCastVote ──────────────────────────────────────► verify ZKP #2
        │                                                         insert VAN + VC into tree
        │                                                         record VAN nullifier
        │
Build delegated_voting_share_payload
        │
        ├──── send payload ──► Receive share payloads
                               Random delay (unlinkability)
                               Collect VC Merkle path
                               Derive share_nullifier
                               Generate ZKP #3
                                       │
                                       ├── MsgRevealShare ──────► verify ZKP #3
                                                                  record share nullifier
                                                                  accumulate enc tally
```

---

## 2. Type Definitions

All curve points are on the **Pallas** curve. All hashes use **Poseidon** unless otherwise noted. Serialization uses big-endian byte encoding for field elements and compressed point encoding for curve points.

```protobuf
// Pallas curve point (compressed, 32 bytes)
message PallasPoint {
  bytes data = 1; // 32 bytes, compressed encoding
}

// El Gamal ciphertext: (C1, C2) where C1 = r*G, C2 = v*G + r*pk
message ElGamalCiphertext {
  PallasPoint c1 = 1;
  PallasPoint c2 = 2;
}

// Halo2 proof blob (opaque to the chain; verified by the on-chain verifier)
message Halo2Proof {
  bytes data = 1;
}

// Field element (32 bytes, Pallas base field)
message FieldElement {
  bytes data = 1; // 32 bytes, big-endian
}
```

---

## 3. Chain State

### 3.1 VotingSession

Stored per `voting_round_id`. Created by `MsgCreateVotingSession`.

```protobuf
message VotingSession {
  bytes   voting_round_id       = 1;  // Blake2b(snapshot_height, snapshot_blockhash,
                                      //         proposals_hash, vote_end_time,
                                      //         nullifier_imt_root, nc_root)
  uint64  snapshot_height       = 2;
  bytes   snapshot_blockhash    = 3;  // 32 bytes
  bytes   proposals_hash        = 4;  // hash of proposals list
  int64   vote_end_time         = 5;  // unix timestamp

  // Pre-processing parameters (Phase 0.2)
  bytes   nullifier_imt_root    = 6;  // Poseidon IMT root of all Orchard nullifiers at snapshot
  bytes   nc_root               = 7;  // Note commitment tree root at snapshot (anchor)

  // Election authority (Phase 0.5)
  PallasPoint ea_pk              = 8;  // Election authority public key for El Gamal

  // Verification keys (Phase 0.4)
  bytes   vk_zkp1               = 9;  // Verification key for ZKP #1 (delegation)
  bytes   vk_zkp2               = 10; // Verification key for ZKP #2 (vote)
  bytes   vk_zkp3               = 11; // Verification key for ZKP #3 (share reveal)

  // Proposals
  repeated Proposal proposals   = 12; // Up to 16 proposals (0-indexed)

  // Status
  SessionStatus status          = 13;
  string  creator               = 14; // Cosmos SDK address of session creator
}

enum SessionStatus {
  SESSION_STATUS_UNSPECIFIED = 0;
  SESSION_STATUS_ACTIVE      = 1;  // Accepting votes
  SESSION_STATUS_TALLYING    = 2;  // Vote window closed, awaiting tally
  SESSION_STATUS_FINALIZED   = 3;  // Tally submitted and verified
}

message Proposal {
  uint32 id          = 1; // 0-indexed, max 15
  string title       = 2;
  string description = 3;
}
```

### 3.2 Nullifier Sets

Three independent nullifier sets per voting round, each stored as a key-value mapping `nullifier → bool` (existence check):

| Set                     | Key prefix                          | Populated by      | Prevents                           |
| ----------------------- | ----------------------------------- | ----------------- | ---------------------------------- |
| **Gov nullifier set**   | `gov_null/{round_id}/{nullifier}`   | `MsgDelegateVote` | Double-delegation of the same note |
| **VAN nullifier set**   | `van_null/{round_id}/{nullifier}`   | `MsgCastVote`     | Double-vote with the same VAN      |
| **Share nullifier set** | `share_null/{round_id}/{nullifier}` | `MsgRevealShare`  | Double-count of the same share     |

### 3.3 Vote Commitment Tree

An **append-only Poseidon Merkle tree** of fixed depth (e.g., 32). Shared by Vote Authority Notes (VANs) and Vote Commitments (VCs), with domain separation:

- `DOMAIN_VAN = 0` — prepended to VAN leaf preimage
- `DOMAIN_VC = 1` — prepended to VC leaf preimage

State stored:

```
vote_tree/{round_id}/root         → current tree root (bytes)
vote_tree/{round_id}/size         → number of leaves inserted (uint64)
vote_tree/{round_id}/leaf/{index} → leaf value (bytes)
vote_tree/{round_id}/node/{level}/{index} → intermediate hash (bytes)
```

Leaves are inserted in transaction order:

| Inserted by       | Leaf contents                                                       |
| ----------------- | ------------------------------------------------------------------- |
| `MsgDelegateVote` | `vote_authority_note` (the VAN commitment, §1.3.3)                  |
| `MsgCastVote`     | `vote_authority_note_new` AND `vote_commitment` (two leaves per tx) |

### 3.4 Encrypted Tally Accumulator

Per `(voting_round_id, proposal_id, vote_decision)`:

```protobuf
message TallyAccumulator {
  bytes         voting_round_id = 1;
  uint32        proposal_id     = 2;
  uint32        vote_decision   = 3;
  ElGamalCiphertext accumulated = 4; // sum(C1), sum(C2)
  uint64        share_count     = 5; // number of shares accumulated
}
```

Key: `tally/{round_id}/{proposal_id}/{decision}`

Updated by `MsgRevealShare` via component-wise point addition.

### 3.5 Finalized Tally Results

Stored after `MsgSubmitTally`:

```protobuf
message TallyResult {
  bytes   voting_round_id       = 1;
  uint32  proposal_id           = 2;
  uint32  vote_decision         = 3;
  uint64  total_value           = 4; // Decrypted aggregate (zatoshi)
  bytes   decryption_proof      = 5; // Chaum-Pedersen DLEQ proof
}
```

---

## 4. Messages

### 4.1 MsgCreateVotingSession

**Phase 0 — Setup.** Creates a new voting round with all governance parameters.

```protobuf
message MsgCreateVotingSession {
  string  creator               = 1; // Cosmos SDK address (governance authority)

  // Governance parameters (Phase 0.1)
  uint64  snapshot_height       = 2;
  bytes   snapshot_blockhash    = 3; // 32 bytes
  repeated Proposal proposals   = 4; // 1–16 proposals
  int64   vote_end_time         = 5; // Unix timestamp

  // Pre-processing roots (Phase 0.2)
  bytes   nullifier_imt_root    = 6; // Poseidon IMT root
  bytes   nc_root               = 7; // Note commitment tree anchor

  // Election authority key (Phase 0.5)
  PallasPoint ea_pk              = 8;

  // Verification keys (Phase 0.4)
  bytes   vk_zkp1               = 9;
  bytes   vk_zkp2               = 10;
  bytes   vk_zkp3               = 11;
}

message MsgCreateVotingSessionResponse {
  bytes voting_round_id = 1; // Blake2b hash of all parameters
}
```

#### Validation

1. `creator` must be a valid Cosmos SDK address with governance authority (module param or governance proposal).
2. `proposals` length must be in `[1, 16]`.
3. `vote_end_time` must be in the future.
4. `snapshot_height` must be a valid ZCash anchor height.
5. `ea_pk` must be a valid Pallas curve point (on-curve check).
6. `vk_zkp1`, `vk_zkp2`, `vk_zkp3` must be non-empty.
7. No existing session with the same `voting_round_id` may exist.

#### State Transitions

- Store `VotingSession` with status `SESSION_STATUS_ACTIVE`.
- Initialize empty Vote Commitment Tree for this round.
- Initialize empty nullifier sets (gov, van, share) for this round.
- Initialize zero-valued tally accumulators for each `(proposal_id, decision)` pair.
- Compute and store `voting_round_id = Blake2b(snapshot_height, snapshot_blockhash, proposals_hash, vote_end_time, nullifier_imt_root, nc_root)`.

---

### 4.2 MsgDelegateVote

**Phase 2 — Delegation.** Submitted by the wallet after keystone signing. Proves ownership of up to 4 ZCash Orchard notes and delegates their voting weight to a hotkey.

> Figma label: "MsgDelegateVote — rk, sig, signed_note_nullifier, cmx_new, gov_nullifiers, gov_comm, ZKP #1 proof"

```protobuf
message MsgDelegateVote {
  // Submitter (pays gas; does NOT need to be the note owner)
  string  submitter             = 1;

  // Voting round context
  bytes   voting_round_id       = 2;

  // Signed action data (standard ZCash-like, §2.4)
  PallasPoint rk                = 3;  // Randomized spend auth verification key
  bytes   sig                   = 4;  // SpendAuthSig over sighash (verified out-of-circuit)
  FieldElement signed_note_nullifier = 5; // Nullifier of the dummy signed note

  // Output note commitment (to the gov hotkey address)
  FieldElement cmx_new          = 6;

  // Governance nullifiers (exactly 4 — real + padded, §1.3.5)
  repeated FieldElement gov_nullifiers = 7; // [gov_null_1, gov_null_2, gov_null_3, gov_null_4]

  // Governance commitment (binds hotkey, weight, round, §1.3.3)
  FieldElement gov_comm         = 8;

  // ZKP #1 proof (Halo2 delegation proof, §Phase 2)
  Halo2Proof  proof             = 9;
}

message MsgDelegateVoteResponse {
  uint64 van_tree_index = 1; // Position of the VAN in the vote commitment tree
}
```

#### Sign-Action Sighash Contract (Implemented)

Current client implementation uses a custom domain-separated Blake2b-256 sighash for
`MsgDelegateVote` signing material (not Cosmos tx hash).

- Hash: Blake2b-256
- Personalization: `ZcVoteDelegation` (16 bytes)
- Input order:
  1. `nf_signed`
  2. `rk`
  3. `cmx_new`
  4. `gov_comm`
  5. `gov_null_1`
  6. `gov_null_2`
  7. `gov_null_3`
  8. `gov_null_4`
  9. `vote_round_id`

References:

- `librustvoting/src/governance.rs` (`compute_delegation_sighash`)
- `librustvoting/src/action.rs` (`encode_delegation_action_bytes`, `construct_delegation_action`)

#### Public Inputs to ZKP #1 Verifier

These are extracted from the message and passed to the on-chain Halo2 verifier:

| #   | Public Input            | Source                                             |
| --- | ----------------------- | -------------------------------------------------- |
| 1   | `signed_note_nullifier` | `msg.signed_note_nullifier`                        |
| 2   | `rk`                    | `msg.rk`                                           |
| 3   | `nc_root`               | `session.nc_root` (from chain state)               |
| 4   | `nullifier_imt_root`    | `session.nullifier_imt_root` (from chain state)    |
| 5   | `gov_comm`              | `msg.gov_comm`                                     |
| 6–9 | `gov_null_1..4`         | `msg.gov_nullifiers[0..3]`                         |
| 10  | `vote_round_id`         | `msg.voting_round_id` (cross-checked with session) |
| 11  | `cmx_new`               | `msg.cmx_new`                                      |

#### Validation (ordered)

1. **Session exists and is active:** `voting_round_id` maps to a `VotingSession` with status `ACTIVE`.
2. **Voting window open:** `block_time < session.vote_end_time`.
3. **Gov nullifiers count:** `len(gov_nullifiers) == 4`.
4. **Gov nullifiers unique within message:** No duplicates among the 4 gov nullifiers.
5. **Gov nullifiers fresh:** None of `gov_nullifiers[i]` exist in the gov nullifier set for this round. (Double-delegation check per §2.5.)
6. **ZKP #1 verification:** Verify `proof` against `session.vk_zkp1` with the public inputs above. This checks all 15 circuit conditions from the spec (§Phase 2: note commitment integrity, nullifier integrity, rho binding, spend authority, diversified address integrity, merkle path validity, IMT non-membership, gov nullifier integrity, padded note zero value, gov commitment integrity, minimum voting weight).
7. **Signature verification (out-of-circuit):** Verify `sig` is a valid `SpendAuthSig` under `rk` over the transaction's sighash. (Per §2.5, spec condition out-of-circuit check 1.)

#### State Transitions

On success:

1. **Record gov nullifiers:** For each `gov_nullifiers[i]`, insert into the gov nullifier set: `gov_null/{round_id}/{nullifier} → true`.
2. **Append VAN to vote commitment tree:** Insert `gov_comm` as a new leaf. Record the tree index. (Per §2.5: "The `vote_action_note` is added to the chain's `vote_commitment_tree`".) Note: The Figma board labels this as "insert VAN" under the state update.
3. **Update tree root.**
4. **Emit event:**

```protobuf
message EventDelegateVote {
  bytes   voting_round_id       = 1;
  bytes   gov_comm              = 2;
  uint64  van_tree_index        = 3;
  repeated bytes gov_nullifiers = 4;
}
```

---

### 4.3 MsgCastVote

**Phase 3 — Vote.** Submitted by the wallet after the user confirms their vote choice. Proves the voter holds a valid VAN and commits an encrypted vote.

> Figma label: "MsgCastVote — van_nullifier, vote_authority_note_new, vote_commitment, proposal_id, ZKP #2 proof"

```protobuf
message MsgCastVote {
  // Submitter (pays gas)
  string  submitter             = 1;

  // Voting round context
  bytes   voting_round_id       = 2;

  // VAN nullifier (prevents double-vote on same proposal, §3.6)
  FieldElement van_nullifier    = 3;

  // New VAN with decremented proposal authority (§3.5)
  FieldElement vote_authority_note_new = 4;

  // Vote commitment: H(DOMAIN_VC, shares_hash, proposal_id, vote_decision) (§3.4)
  FieldElement vote_commitment  = 5;

  // Proposal being voted on (0-indexed, §Proposal IDs)
  uint32  proposal_id           = 6;

  // Vote commitment tree anchor (which height the prover used)
  FieldElement vote_comm_tree_root = 7;

  // ZKP #2 proof (Halo2 vote proof, §3.8)
  Halo2Proof  proof             = 8;
}

message MsgCastVoteResponse {
  uint64 van_tree_index = 1; // Position of new VAN in vote commitment tree
  uint64 vc_tree_index  = 2; // Position of vote commitment in vote commitment tree
}
```

#### Public Inputs to ZKP #2 Verifier

| #   | Public Input              | Source                                                     |
| --- | ------------------------- | ---------------------------------------------------------- |
| 1   | `van_nullifier`           | `msg.van_nullifier`                                        |
| 2   | `vote_authority_note_new` | `msg.vote_authority_note_new`                              |
| 3   | `vote_commitment`         | `msg.vote_commitment`                                      |
| 4   | `vote_comm_tree_root`     | `msg.vote_comm_tree_root` (cross-checked with chain state) |
| 5   | `proposal_id`             | `msg.proposal_id`                                          |
| 6   | `voting_round_id`         | `msg.voting_round_id`                                      |

Note: No signature is needed — the ZKP itself proves knowledge of `vsk` (§3.8: "No external signature is needed — the ZKP proof itself serves as authorization").

#### Validation (ordered)

1. **Session exists and is active:** `voting_round_id` maps to a `VotingSession` with status `ACTIVE`.
2. **Voting window open:** `block_time < session.vote_end_time`.
3. **Proposal valid:** `proposal_id < len(session.proposals)`.
4. **VAN nullifier fresh:** `van_nullifier` does not exist in the VAN nullifier set for this round. (Double-vote check per §3.9.)
5. **Vote commitment tree root valid:** `vote_comm_tree_root` matches a known historical root for this round's vote commitment tree. (The chain should maintain a rolling window of recent roots, e.g., the last N roots, to account for concurrent submissions.)
6. **ZKP #2 verification:** Verify `proof` against `session.vk_zkp2` with the public inputs above. This checks all 11 circuit conditions from the spec (§3.8: VAN membership, VAN integrity, spend authority, VAN nullifier integrity, proposal authority decrement, new VAN integrity, shares sum correctness, shares range, shares hash integrity, encryption integrity, vote commitment integrity).

#### State Transitions

On success:

1. **Record VAN nullifier:** Insert `van_null/{round_id}/{van_nullifier} → true`.
2. **Append new VAN to vote commitment tree:** Insert `vote_authority_note_new` as a new leaf. (Per Figma: "insert VAN + VC".)
3. **Append vote commitment to vote commitment tree:** Insert `vote_commitment` as a new leaf.
4. **Update tree root.**
5. **Emit event:**

```protobuf
message EventCastVote {
  bytes   voting_round_id         = 1;
  bytes   van_nullifier           = 2;
  bytes   vote_commitment         = 3;
  uint32  proposal_id             = 4;
  uint64  van_tree_index          = 5;
  uint64  vc_tree_index           = 6;
}
```

---

### 4.4 MsgRevealShare

**Phase 5 — Share Reveal.** Submitted by the helper server after receiving a `delegated_voting_share_payload` from the client. Opens one encrypted share from a registered vote commitment for tally accumulation.

> Figma label: "MsgRevealShare — share_nullifier, enc_share (C1, C2), proposal_id, vote_decision, ZKP #3 proof"

```protobuf
message MsgRevealShare {
  // Submitter (helper server pays gas)
  string  submitter             = 1;

  // Voting round context
  bytes   voting_round_id       = 2;

  // Share nullifier (prevents double-count, §5.3)
  FieldElement share_nullifier  = 3;

  // The El Gamal ciphertext for this share (NOT the plaintext, §5.5)
  ElGamalCiphertext enc_share   = 4;

  // Vote metadata (revealed for tallying)
  uint32  proposal_id           = 5;
  uint32  vote_decision         = 6;

  // Vote commitment tree anchor
  FieldElement vote_comm_tree_root = 7;

  // ZKP #3 proof (Halo2 share reveal proof, §5.4)
  Halo2Proof  proof             = 8;
}

message MsgRevealShareResponse {
  // Updated accumulator state for this (proposal, decision) pair
  uint64 total_shares_accumulated = 1;
}
```

#### Public Inputs to ZKP #3 Verifier

| #   | Public Input          | Source                                                     |
| --- | --------------------- | ---------------------------------------------------------- |
| 1   | `share_nullifier`     | `msg.share_nullifier`                                      |
| 2   | `enc_share`           | `msg.enc_share`                                            |
| 3   | `proposal_id`         | `msg.proposal_id`                                          |
| 4   | `vote_decision`       | `msg.vote_decision`                                        |
| 5   | `vote_comm_tree_root` | `msg.vote_comm_tree_root` (cross-checked with chain state) |
| 6   | `voting_round_id`     | `msg.voting_round_id`                                      |

Note: No signature is needed — the ZKP proves the share originated from a valid, registered vote commitment (§5.5).

#### Validation (ordered)

1. **Session exists and is active or tallying:** `voting_round_id` maps to a `VotingSession` with status `ACTIVE` or `TALLYING`. (Share reveals may still arrive after the vote window closes but before tally finalization.)
2. **Voting window open for shares:** `block_time < session.vote_end_time + SHARE_GRACE_PERIOD`. (Helper servers stagger submissions; a grace period allows late-arriving shares.)
3. **Proposal valid:** `proposal_id < len(session.proposals)`.
4. **Share nullifier fresh:** `share_nullifier` does not exist in the share nullifier set for this round. (Double-count check per §5.6.)
5. **Vote commitment tree root valid:** `vote_comm_tree_root` matches a known historical root for this round's vote commitment tree.
6. **ZKP #3 verification:** Verify `proof` against `session.vk_zkp3` with the public inputs above. This checks all 5 circuit conditions from the spec (§5.4: VC membership, vote commitment integrity, shares hash integrity, share membership, share nullifier integrity).

#### State Transitions

On success:

1. **Record share nullifier:** Insert `share_null/{round_id}/{share_nullifier} → true`.
2. **Accumulate into encrypted tally:** For the key `(round_id, proposal_id, vote_decision)`:
   - `accumulator.c1 += enc_share.c1` (Pallas point addition)
   - `accumulator.c2 += enc_share.c2` (Pallas point addition)
   - `accumulator.share_count += 1`
3. **Emit event:**

```protobuf
message EventRevealShare {
  bytes   voting_round_id       = 1;
  bytes   share_nullifier       = 2;
  uint32  proposal_id           = 3;
  uint32  vote_decision         = 4;
  uint64  shares_accumulated    = 5;
}
```

---

### 4.5 MsgSubmitTally

**Appendix B — Tally.** Submitted by the election authority after the voting window closes. Provides the decrypted aggregate vote total with a proof of correct decryption.

```protobuf
message MsgSubmitTally {
  // Election authority address
  string  submitter             = 1;

  // Voting round context
  bytes   voting_round_id       = 2;

  // Results for each (proposal, decision) pair
  repeated TallyEntry entries   = 3;
}

message TallyEntry {
  uint32  proposal_id           = 1;
  uint32  vote_decision         = 2;
  uint64  total_value           = 3; // Decrypted aggregate (zatoshi)

  // Chaum-Pedersen discrete log equality proof (§Appendix B, Step 3)
  // Proves: the same ea_sk that generated ea_pk was used to decrypt
  bytes   decryption_proof      = 4;
}

message MsgSubmitTallyResponse {}
```

#### Validation (ordered)

1. **Session exists:** `voting_round_id` maps to a `VotingSession`.
2. **Session in tallying state:** Status must be `TALLYING` (set automatically when `block_time >= vote_end_time`).
3. **Authorized submitter:** `submitter` matches the session creator or is authorized by governance.
4. **All (proposal, decision) pairs covered:** Every non-zero accumulator must have a corresponding entry.
5. **For each entry:**
   a. **Accumulator exists:** A non-zero tally accumulator exists for `(round_id, proposal_id, vote_decision)`.
   b. **Decryption proof valid:** Verify the Chaum-Pedersen DLEQ proof:
      - Given `agg = (sum_C1, sum_C2)` from the accumulator and `ea_pk` from the session
      - Verify that `sum_C2 - ea_sk * sum_C1 = total_value * G` without learning `ea_sk`
      - The DLEQ proof demonstrates `log_G(ea_pk) == log_{sum_C1}(sum_C2 - total_value * G)`
   c. **Value consistency:** `total_value * G == sum_C2 - ea_sk * sum_C1` (implicit in proof verification).

#### State Transitions

On success:

1. **Store tally results:** For each entry, write `TallyResult` to state.
2. **Update session status:** Set `session.status = SESSION_STATUS_FINALIZED`.
3. **Emit event:**

```protobuf
message EventTallyFinalized {
  bytes   voting_round_id       = 1;
  repeated TallyEntry results   = 2;
}
```

---

## 5. Queries

```protobuf
service Query {
  // Get voting session by round ID
  rpc VotingSession(QueryVotingSessionRequest) returns (QueryVotingSessionResponse);

  // List all active voting sessions
  rpc ActiveSessions(QueryActiveSessionsRequest) returns (QueryActiveSessionsResponse);

  // Get current vote commitment tree root and size for a round
  rpc VoteCommitmentTreeInfo(QueryVoteCommitmentTreeInfoRequest)
    returns (QueryVoteCommitmentTreeInfoResponse);

  // Get a Merkle path for a leaf in the vote commitment tree
  // (Used by clients/servers to construct ZKP witnesses)
  rpc VoteCommitmentTreePath(QueryVoteCommitmentTreePathRequest)
    returns (QueryVoteCommitmentTreePathResponse);

  // Check if a nullifier has been used (gov, van, or share)
  rpc NullifierExists(QueryNullifierExistsRequest) returns (QueryNullifierExistsResponse);

  // Get accumulated encrypted tally for a (proposal, decision) pair
  rpc TallyAccumulator(QueryTallyAccumulatorRequest) returns (QueryTallyAccumulatorResponse);

  // Get finalized tally results
  rpc TallyResult(QueryTallyResultRequest) returns (QueryTallyResultResponse);

  // Get historical vote commitment tree roots (for anchor validation)
  rpc HistoricalTreeRoots(QueryHistoricalTreeRootsRequest)
    returns (QueryHistoricalTreeRootsResponse);
}
```

### Key Query Details

**VoteCommitmentTreePath** — Critical for both client (ZKP #2) and server (ZKP #3):
- Client needs VAN Merkle path after `MsgDelegateVote` succeeds
- Server needs VC Merkle path after `MsgCastVote` succeeds

```protobuf
message QueryVoteCommitmentTreePathRequest {
  bytes  voting_round_id = 1;
  uint64 leaf_index      = 2;
  uint64 anchor_height   = 3; // Optional: tree state at specific height
}

message QueryVoteCommitmentTreePathResponse {
  repeated bytes path = 1; // Sibling hashes from leaf to root
  uint64 position     = 2; // Leaf position in tree
  bytes  root         = 3; // Tree root at the requested anchor
}
```

**HistoricalTreeRoots** — The chain must maintain recent roots for concurrent proof generation:

```protobuf
message QueryHistoricalTreeRootsRequest {
  bytes  voting_round_id = 1;
  uint64 count           = 2; // How many recent roots to return
}

message QueryHistoricalTreeRootsResponse {
  repeated TreeRootEntry roots = 1;
}

message TreeRootEntry {
  bytes  root   = 1;
  uint64 height = 2; // Block height when this was the current root
  uint64 size   = 3; // Tree size at this root
}
```

---

## 6. State Transitions Summary

```
                    MsgCreateVotingSession
                           │
                           ▼
                  ┌─────────────────┐
                  │  ACTIVE session  │
                  └────────┬────────┘
                           │
              ┌────────────┼────────────┐
              │            │            │
              ▼            ▼            ▼
       MsgDelegateVote  MsgCastVote  MsgRevealShare
              │            │            │
              ▼            ▼            ▼
       ┌──────────┐  ┌──────────┐  ┌────────────────┐
       │ Gov null  │  │ VAN null │  │ Share null set  │
       │   set     │  │   set    │  │ Tally accum.    │
       │ VCT: +VAN │  │ VCT: +VAN│  └────────────────┘
       └──────────┘  │      +VC  │
                     └──────────┘
                           │
                    vote_end_time reached
                           │
                           ▼
                  ┌─────────────────┐
                  │ TALLYING session │◄── MsgRevealShare still accepted
                  └────────┬────────┘    (grace period)
                           │
                    MsgSubmitTally
                           │
                           ▼
                  ┌─────────────────┐
                  │ FINALIZED session│
                  └─────────────────┘
```

### Per-Message State Changes

| Message           | Gov Null Set | VAN Null Set | Share Null Set | Vote Commitment Tree     | Tally Accum           |
| ----------------- | ------------ | ------------ | -------------- | ------------------------ | --------------------- |
| `MsgDelegateVote` | +4 entries   | —            | —              | +1 leaf (VAN)            | —                     |
| `MsgCastVote`     | —            | +1 entry     | —              | +2 leaves (new VAN + VC) | —                     |
| `MsgRevealShare`  | —            | —            | +1 entry       | —                        | +(C1,C2) accumulation |
| `MsgSubmitTally`  | —            | —            | —              | —                        | Results stored        |

---

## 7. Error Codes

| Code | Name                             | Triggered by                                              |
| ---- | -------------------------------- | --------------------------------------------------------- |
| 1    | `ErrSessionNotFound`             | Round ID does not map to a session                        |
| 2    | `ErrSessionNotActive`            | Session status is not `ACTIVE` (or `TALLYING` for shares) |
| 3    | `ErrVotingWindowClosed`          | `block_time >= vote_end_time` (+ grace for shares)        |
| 4    | `ErrInvalidProposalId`           | `proposal_id >= len(proposals)`                           |
| 5    | `ErrDuplicateGovNullifier`       | Gov nullifier already in set                              |
| 6    | `ErrDuplicateVanNullifier`       | VAN nullifier already in set                              |
| 7    | `ErrDuplicateShareNullifier`     | Share nullifier already in set                            |
| 8    | `ErrZkpVerificationFailed`       | Halo2 proof did not verify                                |
| 9    | `ErrSignatureVerificationFailed` | SpendAuthSig invalid (MsgDelegateVote only)               |
| 10   | `ErrInvalidTreeRoot`             | Provided tree root not in historical window               |
| 11   | `ErrInvalidCurvePoint`           | Pallas point not on curve                                 |
| 12   | `ErrTallyDecryptionProofFailed`  | Chaum-Pedersen DLEQ proof invalid                         |
| 13   | `ErrSessionAlreadyExists`        | Duplicate `voting_round_id`                               |
| 14   | `ErrUnauthorized`                | Submitter not authorized for this action                  |
| 15   | `ErrInvalidGovNullifierCount`    | `gov_nullifiers` length != 4                              |

---

## 8. Open Questions

These are items marked as TODO in the protocol spec that affect message design:

| # | Question | Spec Reference | Impact |
|---|---|---|---|
| 1 | **Historical root window size:** How many recent tree roots should the chain maintain for concurrent proof generation? | §5.2 | Affects `vote_comm_tree_root` validation in MsgCastVote and MsgRevealShare |
| 2 | **Share grace period:** How long after `vote_end_time` should the chain accept `MsgRevealShare`? The helper server staggers submissions for temporal unlinkability. | §5.1 | Affects MsgRevealShare validation window |
| 3 | **`vote_decision` encoding:** Is this a simple integer (e.g., 0=no, 1=yes), or a more complex structure? | §3.2 | Affects `vote_decision` field type |
| 4 | **Sighash computation:** What exactly is the sighash for `MsgDelegateVote`? Standard Cosmos SDK tx hash, or a custom domain-separated hash? | §2.4 | Affects out-of-circuit signature verification |
| 5 | **Governance authority model:** Who can create voting sessions? A single admin, a multisig, or via Cosmos governance proposal? | §Phase 0 | Affects MsgCreateVotingSession authorization |
| 6 | **Differential privacy for share count:** The spec mentions replacing binary decomposition with a DP random distribution (§3.3). Changing from 4 shares would require updating ZKP #2 and #3 circuit parameters. | §3.3 | Affects shares_hash computation and ZKP constraints |
| 7 | **Delegation support (§6.0):** The spec mentions optional delegation — this would require additional messages (MsgDelegateToVoter, etc.). | §6.0 | Future message additions |
| 8 | **Tree depth:** Resolved — depth 24 (2^24 ≈ 16.7M leaves). Reduced from ZCash's depth 32 for governance voting scale. Saves 8 Poseidon hashes per ZKP proof. | §Vote Commitment Tree | Affects Merkle path sizes and circuit parameters |
| 9 | **Proposal authority decrement model:** The spec uses a single decrement per vote (§3.8 condition 5). Should this support weighted authority decrements for multi-proposal voting? | §3.5 | Affects ZKP #2 conditions |
| 10 | **VAN insertion for MsgDelegateVote:** The spec says `vote_action_note` (i.e., `gov_comm`) is inserted into the tree. Confirm this is `gov_comm` and not `cmx_new`. | §2.5 | Affects which value becomes the VAN leaf |
