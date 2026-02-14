# Mobile Voting API Contract

API contract for `VotingCryptoClient` and `VotingAPIClient` — the two TCA dependency clients that Zashi iOS uses to execute the voting protocol.

This document covers **what to call, with what, and what comes back**. For system architecture and data flow, see [mobile-voting-architecture.md](mobile-voting-architecture.md). For on-chain message definitions, see [cosmos-sdk-messages-spec.md](cosmos-sdk-messages-spec.md).

---

## Implementation Status

Grouped by round lifecycle phase. Status key: **Real** = production implementation, **Stubbed** = returns placeholder data, **Mocked** = returns hardcoded success.

### VotingCryptoClient

| Phase      | Method                                                                                   | Status  | Notes                                                    |
| ---------- | ---------------------------------------------------------------------------------------- | ------- | -------------------------------------------------------- |
| —          | `stateStream()`                                                                          | Real    | Publishes DB state via `CurrentValueSubject`             |
| —          | `openDatabase(path:)`                                                                    | Real    | Opens/creates SQLite via UniFFI                          |
| Init       | `initRound(params:sessionJson:)`                                                         | Real    | Creates round row, sets phase to `initialized`           |
| Init       | `getRoundState(roundId:)`                                                                | Real    | Queries `rounds` table                                   |
| Init       | `getVotes(roundId:)`                                                                     | Real    | Queries `votes` table                                    |
| Init       | `listRounds()`                                                                           | Real    | Lists all round summaries                                |
| Init       | `clearRound(roundId:)`                                                                   | Real    | Deletes round + associated votes                         |
| Init       | `getWalletNotes(walletDbPath:snapshotHeight:networkId:)`                                 | Real    | Read-only query of Zcash wallet DB, cmx recomputed       |
| Delegation | `generateHotkey(roundId:seed:)`                                                          | Real    | Random Pallas keypair from seed                          |
| Delegation | `buildDelegationSignAction(roundId:notes:senderSeed:hotkeySeed:networkId:accountIndex:)` | Real    | FVK derivation, receiver construction, action + sighash  |
| Delegation | `storeTreeState(roundId:treeState:)`                                                     | Stubbed | Stores bytes but tree data is placeholder                |
| Delegation | `buildDelegationWitness(roundId:action:inclusionProofs:exclusionProofs:)`                | Stubbed | Accepts placeholder proofs, returns dummy witness        |
| Delegation | `generateDelegationProof(roundId:)`                                                      | Stubbed | Simulates progress over ~2s, returns 32-byte dummy proof |
| Voting     | `decomposeWeight(weight:)`                                                               | Real    | Binary decomposition, max 4 shares                       |
| Voting     | `encryptShares(roundId:shares:)`                                                         | Real    | ElGamal encryption on Pallas curve                       |
| Voting     | `buildVoteCommitment(roundId:proposalId:choice:encShares:vanWitness:)`                   | Stubbed | Returns placeholder hashes for bundle fields             |
| Voting     | `buildSharePayloads(encShares:commitment:)`                                              | Real    | Constructs helper server payloads from encrypted shares  |
| Voting     | `markVoteSubmitted(roundId:proposalId:)`                                                 | Real    | Sets `submitted = true` in votes table                   |

### VotingAPIClient

All methods are **Mocked** — they return hardcoded success responses after a short delay. These will be replaced with HTTP calls to the vote chain REST API and helper server.

| Method                                       | Mock behavior                                                      |
| -------------------------------------------- | ------------------------------------------------------------------ |
| `fetchActiveVotingSession()`                 | Returns session with `snapshotHeight: 2_800_000`, placeholder keys |
| `fetchVotingWeight(snapshotHeight:)`         | Returns `14_250_000_000` (142.50 ZEC)                              |
| `fetchNoteInclusionProofs(commitments:)`     | Returns one 32-byte placeholder per commitment                     |
| `fetchNullifierExclusionProofs(nullifiers:)` | Returns one 32-byte placeholder per nullifier                      |
| `fetchCommitmentTreeState(height:)`          | Returns `CommitmentTreeState(nextIndex: 1024, ...)`                |
| `fetchLatestCommitmentTree()`                | Returns `CommitmentTreeState(nextIndex: 2048, ...)`                |
| `submitDelegation(registration:)`            | Returns `TxResult(code: 0)` — maps to `MsgDelegateVote`            |
| `submitVoteCommitment(bundle:)`              | Returns `TxResult(code: 0)` — maps to `MsgCastVote`                |
| `delegateShares(payloads:)`                  | No-op — sends `SharePayload`s to helper server                     |
| `fetchProposalTally(roundId:proposalId:)`    | Returns mock tally with 3 entries                                  |

---

## Round Lifecycle

A voting round progresses through phases tracked in the `rounds.phase` SQLite column. Phase transitions happen atomically inside Rust — each operation validates the current phase, does its work, persists results, and advances the phase.

```
initialized ──► hotkeyGenerated ──► delegationConstructed ──► witnessBuilt ──► delegationProved ──► voteReady
```

| Phase                   | Entered by                  | Valid next calls                                                                  |
| ----------------------- | --------------------------- | --------------------------------------------------------------------------------- |
| `initialized`           | `initRound`                 | `generateHotkey`                                                                  |
| `hotkeyGenerated`       | `generateHotkey`            | `buildDelegationSignAction`                                                       |
| `delegationConstructed` | `buildDelegationSignAction` | `storeTreeState`, `buildDelegationWitness`                                        |
| `witnessBuilt`          | `buildDelegationWitness`    | `generateDelegationProof`                                                         |
| `delegationProved`      | `generateDelegationProof`   | `buildVoteCommitment` (first call)                                                |
| `voteReady`             | `buildVoteCommitment`       | `buildVoteCommitment`, `encryptShares`, `buildSharePayloads`, `markVoteSubmitted` |

Phase-independent methods (callable at any phase): `stateStream`, `openDatabase`, `getRoundState`, `getVotes`, `listRounds`, `clearRound`, `getWalletNotes`, `decomposeWeight`.

---

## VotingCryptoClient API Reference

### stateStream

```swift
var stateStream: @Sendable () -> AnyPublisher<VotingDbState, Never>
```

Publishes combined round state and vote records whenever the DB changes. The publisher drops the initial empty value (`.dropFirst()`). Follows the same pattern as `SDKSynchronizer` in the Zcash wallet SDK.

**Output:** `VotingDbState` — contains `roundState: RoundStateInfo` and `votes: [VoteRecord]`.

### openDatabase

```swift
var openDatabase: @Sendable (_ path: String) async throws -> Void
```

Opens or creates the voting SQLite database at the given path. Must be called before any other database operation. The database handle is held by a thread-safe `DatabaseActor`.

| Param  | Type     | Description                          |
| ------ | -------- | ------------------------------------ |
| `path` | `String` | Filesystem path for `voting.sqlite3` |

### initRound

```swift
var initRound: @Sendable (_ params: VotingRoundParams, _ sessionJson: String?) async throws -> Void
```

Creates a new voting round row in the database. Sets phase to `initialized`. Publishes state.

| Param         | Type                | Description                                          |
| ------------- | ------------------- | ---------------------------------------------------- |
| `params`      | `VotingRoundParams` | Round ID, snapshot height, EA public key, tree roots |
| `sessionJson` | `String?`           | Optional raw session JSON for debugging/replay       |

### getRoundState

```swift
var getRoundState: @Sendable (_ roundId: String) async throws -> RoundStateInfo
```

Returns the current state of a round from the `rounds` table.

| Param     | Type     | Description                       |
| --------- | -------- | --------------------------------- |
| `roundId` | `String` | Hex-encoded 32-byte `voteRoundId` |

**Output:** `RoundStateInfo` — `roundId`, `phase`, `snapshotHeight`, `hotkeyAddress?`, `delegatedWeight?`, `proofGenerated`.

### getVotes

```swift
var getVotes: @Sendable (_ roundId: String) async throws -> [VoteRecord]
```

Returns all vote records for a round from the `votes` table.

**Output:** `[VoteRecord]` — each has `proposalId: UInt32`, `choice: VoteChoice`, `submitted: Bool`.

### listRounds

```swift
var listRounds: @Sendable () async throws -> [RoundSummaryInfo]
```

Returns summaries of all rounds in the database.

**Output:** `[RoundSummaryInfo]` — `roundId`, `phase`, `snapshotHeight`, `createdAt`.

### clearRound

```swift
var clearRound: @Sendable (_ roundId: String) async throws -> Void
```

Deletes a round and all associated votes from the database.

### getWalletNotes

```swift
var getWalletNotes: @Sendable (
    _ walletDbPath: String,
    _ snapshotHeight: UInt64,
    _ networkId: UInt32
) async throws -> [NoteInfo]
```

Queries the Zcash wallet database (read-only) for Orchard notes unspent at the snapshot height. Each note's cmx is recomputed from its parts using `orchard 0.11`.

| Param            | Type     | Description                                            |
| ---------------- | -------- | ------------------------------------------------------ |
| `walletDbPath`   | `String` | Path to `data.sqlite` managed by `zcash_client_sqlite` |
| `snapshotHeight` | `UInt64` | Block height for the note snapshot                     |
| `networkId`      | `UInt32` | `0` = mainnet, `1` = testnet                           |

**Output:** `[NoteInfo]` — each has `commitment` (32-byte cmx), `nullifier` (32 bytes), `value` (zatoshis), `position` (commitment tree position).

### generateHotkey

```swift
var generateHotkey: @Sendable (_ roundId: String, _ seed: [UInt8]) async throws -> VotingHotkey
```

Generates a random Pallas keypair from the given seed. Stores the hotkey in the round row and advances phase to `hotkeyGenerated`.

| Param     | Type      | Description                           |
| --------- | --------- | ------------------------------------- |
| `roundId` | `String`  | Hex-encoded round ID                  |
| `seed`    | `[UInt8]` | 64 bytes from BIP-39 `mnemonicToSeed` |

**Output:** `VotingHotkey` — `secretKey` (32 bytes), `publicKey` (32 bytes), `address` (string).

### buildDelegationSignAction

```swift
var buildDelegationSignAction: @Sendable (
    _ roundId: String,
    _ notes: [NoteInfo],
    _ senderSeed: [UInt8],
    _ hotkeySeed: [UInt8],
    _ networkId: UInt32,
    _ accountIndex: UInt32
) async throws -> DelegationAction
```

High-level boundary method for delegation action construction. Derives the FVK from `senderSeed`, generates delegation inputs (receiver addresses, diversified keys), validates hotkey consistency, and calls `constructDelegationAction` in Rust. Advances phase to `delegationConstructed`.

| Param          | Type         | Description                                       |
| -------------- | ------------ | ------------------------------------------------- |
| `roundId`      | `String`     | Hex-encoded round ID                              |
| `notes`        | `[NoteInfo]` | Wallet notes from `getWalletNotes` (up to 4 used) |
| `senderSeed`   | `[UInt8]`    | 64-byte sender wallet seed                        |
| `hotkeySeed`   | `[UInt8]`    | 64-byte hotkey mnemonic seed                      |
| `networkId`    | `UInt32`     | `0` = mainnet, `1` = testnet                      |
| `accountIndex` | `UInt32`     | Account index in the wallet (typically `0`)       |

**Output:** `DelegationAction` — 14 fields, see [Data Types](#data-types).

### storeTreeState

```swift
var storeTreeState: @Sendable (_ roundId: String, _ treeState: Data) async throws -> Void
```

Stores serialized commitment tree state in the round row. Currently accepts placeholder data.

### buildDelegationWitness

```swift
var buildDelegationWitness: @Sendable (
    _ roundId: String,
    _ action: DelegationAction,
    _ inclusionProofs: [Data],
    _ exclusionProofs: [Data]
) async throws -> Data
```

Builds the ZKP #1 circuit witness from the delegation action, note inclusion proofs, and nullifier exclusion proofs. Advances phase to `witnessBuilt`. Currently stubbed — accepts placeholder proof data.

| Param             | Type               | Description                                       |
| ----------------- | ------------------ | ------------------------------------------------- |
| `roundId`         | `String`           | Hex-encoded round ID                              |
| `action`          | `DelegationAction` | From `buildDelegationSignAction`                  |
| `inclusionProofs` | `[Data]`           | Merkle paths proving note membership in `nc_root` |
| `exclusionProofs` | `[Data]`           | IMT non-membership proofs for nullifiers          |

**Output:** `Data` — serialized witness bytes.

### generateDelegationProof

```swift
var generateDelegationProof: @Sendable (_ roundId: String) -> AsyncThrowingStream<ProofEvent, Error>
```

Long-running ZKP #1 proof generation. Runs on a detached task and streams progress updates via a UniFFI callback bridge. Advances phase to `delegationProved` on completion. Currently stubbed — simulates progress and returns a dummy 32-byte proof.

**Output stream:** `.progress(Double)` (0.0–1.0) then `.completed(Data)`.

### decomposeWeight

```swift
var decomposeWeight: @Sendable (_ weight: UInt64) -> [UInt64]
```

Decomposes a voting weight into its binary representation, returning the largest powers of 2. Maximum 4 shares enforced by the protocol.

**Example:** `decomposeWeight(13)` → `[8, 4, 1]`

### encryptShares

```swift
var encryptShares: @Sendable (
    _ roundId: String,
    _ shares: [UInt64]
) async throws -> [EncryptedShare]
```

Encrypts each share value under the election authority's public key (`eaPK` from the round params) using ElGamal on the Pallas curve.

**Output:** `[EncryptedShare]` — each has `c1` (32 bytes), `c2` (32 bytes), `shareIndex: UInt32`, `plaintextValue: UInt64`.

### buildVoteCommitment

```swift
var buildVoteCommitment: @Sendable (
    _ roundId: String,
    _ proposalId: UInt32,
    _ choice: VoteChoice,
    _ encShares: [EncryptedShare],
    _ vanWitness: Data
) -> AsyncThrowingStream<VoteCommitmentBuildEvent, Error>
```

Builds a vote commitment bundle with ZKP #2. On the first call, advances phase from `delegationProved` to `voteReady`. Subsequent calls stay in `voteReady`. Stores the vote record in the DB. Currently stubbed — returns placeholder hashes.

| Param        | Type               | Description                                   |
| ------------ | ------------------ | --------------------------------------------- |
| `roundId`    | `String`           | Hex-encoded round ID                          |
| `proposalId` | `UInt32`           | 0-indexed proposal identifier                 |
| `choice`     | `VoteChoice`       | `.support` (0), `.oppose` (1), or `.skip` (2) |
| `encShares`  | `[EncryptedShare]` | From `encryptShares`                          |
| `vanWitness` | `Data`             | VAN Merkle path from vote commitment tree     |

**Output stream:** `.progress(Double)` then `.completed(VoteCommitmentBundle)`.

### buildSharePayloads

```swift
var buildSharePayloads: @Sendable (
    _ encShares: [EncryptedShare],
    _ commitment: VoteCommitmentBundle
) async throws -> [SharePayload]
```

Constructs the payloads sent to the helper server for share delegation. One payload per encrypted share.

**Output:** `[SharePayload]` — each has `sharesHash` (32 bytes), `proposalId`, `voteDecision`, `encShare`, `shareIndex`, `treePosition`.

### markVoteSubmitted

```swift
var markVoteSubmitted: @Sendable (_ roundId: String, _ proposalId: UInt32) async throws -> Void
```

Marks a vote as submitted in the `votes` table (`submitted = true`) and publishes updated state.

---

## VotingAPIClient API Reference

All methods are currently mocked. See [cosmos-sdk-messages-spec.md](cosmos-sdk-messages-spec.md) for the on-chain message definitions these will map to.

### fetchActiveVotingSession

```swift
var fetchActiveVotingSession: @Sendable () async throws -> VotingSession
```

Queries the vote chain for the current active voting session. Returns the full `VotingSession` with all governance parameters, verification keys, and proposals.

### fetchVotingWeight

```swift
var fetchVotingWeight: @Sendable (_ snapshotHeight: UInt64) async throws -> UInt64
```

Queries voting weight at the given snapshot height. In practice, the client computes weight locally from `getWalletNotes` — this method exists for cross-checking or when the wallet DB isn't available.

### fetchNoteInclusionProofs

```swift
var fetchNoteInclusionProofs: @Sendable (_ commitments: [Data]) async throws -> [Data]
```

Fetches Merkle inclusion proofs for note commitments against `nc_root`. One proof per commitment.

### fetchNullifierExclusionProofs

```swift
var fetchNullifierExclusionProofs: @Sendable (_ nullifiers: [Data]) async throws -> [Data]
```

Fetches IMT non-membership proofs for nullifiers against `nullifierIMTRoot`. One proof per nullifier.

### fetchCommitmentTreeState

```swift
var fetchCommitmentTreeState: @Sendable (_ height: UInt64) async throws -> CommitmentTreeState
```

Returns the vote commitment tree state at a specific block height. Used to get tree anchors for ZKP #2.

### fetchLatestCommitmentTree

```swift
var fetchLatestCommitmentTree: @Sendable () async throws -> CommitmentTreeState
```

Returns the latest vote commitment tree state.

### submitDelegation

```swift
var submitDelegation: @Sendable (_ registration: DelegationRegistration) async throws -> TxResult
```

Submits a delegation transaction to the vote chain. Maps to `MsgDelegateVote`.

### submitVoteCommitment

```swift
var submitVoteCommitment: @Sendable (_ bundle: VoteCommitmentBundle) async throws -> TxResult
```

Submits a vote commitment transaction to the vote chain. Maps to `MsgCastVote`.

### delegateShares

```swift
var delegateShares: @Sendable (_ payloads: [SharePayload]) async throws -> Void
```

Sends encrypted share payloads to the helper server. The helper server adds a random delay for temporal unlinkability, gathers a VC Merkle path, and submits `MsgRevealShare` to the chain.

### fetchProposalTally

```swift
var fetchProposalTally: @Sendable (_ roundId: Data, _ proposalId: UInt32) async throws -> TallyResult
```

Queries the finalized tally for a proposal. Returns per-decision vote totals.

---

## Data Types

### NoteInfo

Orchard note from the Zcash wallet, unspent at the snapshot height.

| Field        | Type     | Size     | Description                                                 |
| ------------ | -------- | -------- | ----------------------------------------------------------- |
| `commitment` | `Data`   | 32 bytes | Extracted note commitment (cmx), recomputed from note parts |
| `nullifier`  | `Data`   | 32 bytes | Note nullifier                                              |
| `value`      | `UInt64` | 8 bytes  | Note value in zatoshis                                      |
| `position`   | `UInt64` | 8 bytes  | Position in the Orchard commitment tree                     |

### DelegationAction

Intermediate type produced by `buildDelegationSignAction`. Contains everything needed for Keystone signing and downstream proof generation.

| Field             | Type     | Size         | Description                                              |
| ----------------- | -------- | ------------ | -------------------------------------------------------- |
| `actionBytes`     | `Data`   | variable     | Serialized delegation action for signing                 |
| `rk`              | `Data`   | 32 bytes     | Randomized spend auth verification key                   |
| `sighash`         | `Data`   | 32 bytes     | Blake2b-256 hash with `ZcVoteDelegation` personalization |
| `govNullifiers`   | `[Data]` | 4 × 32 bytes | Governance nullifiers, always padded to 4                |
| `van`             | `Data`   | 32 bytes     | Vote Authority Note (governance commitment)              |
| `govCommRand`     | `Data`   | 32 bytes     | Blinding factor for VAN                                  |
| `dummyNullifiers` | `[Data]` | n × 32 bytes | Nullifiers for padded dummy notes (circuit witness)      |
| `rhoSigned`       | `Data`   | 32 bytes     | Constrained rho for the signed note                      |
| `paddedCmx`       | `[Data]` | n × 32 bytes | Extracted note commitments for padded dummy notes        |
| `nfSigned`        | `Data`   | 32 bytes     | Signed note nullifier (ZKP #1 public input)              |
| `cmxNew`          | `Data`   | 32 bytes     | Output note commitment (ZKP #1 public input)             |
| `alpha`           | `Data`   | 32 bytes     | Spend auth randomizer scalar (for Keystone signing)      |
| `rseedSigned`     | `Data`   | 32 bytes     | Signed note rseed (witness reconstruction)               |
| `rseedOutput`     | `Data`   | 32 bytes     | Output note rseed (witness reconstruction)               |

### DelegationRegistration

Maps to `MsgDelegateVote` (see [cosmos-sdk-messages-spec.md §4.2](cosmos-sdk-messages-spec.md#42-msgdelegatevote)). Assembled from `DelegationAction` + the Keystone signature + the ZKP #1 proof.

| Field                 | Type     | Size         | Description                               |
| --------------------- | -------- | ------------ | ----------------------------------------- |
| `rk`                  | `Data`   | 32 bytes     | Randomized spend auth verification key    |
| `spendAuthSig`        | `Data`   | 64 bytes     | SpendAuthSig over sighash (from Keystone) |
| `signedNoteNullifier` | `Data`   | 32 bytes     | `nfSigned` from `DelegationAction`        |
| `cmxNew`              | `Data`   | 32 bytes     | Output note commitment                    |
| `encMemo`             | `Data`   | variable     | Encrypted memo field                      |
| `govComm`             | `Data`   | 32 bytes     | VAN (governance commitment)               |
| `govNullifiers`       | `[Data]` | 4 × 32 bytes | Governance nullifiers                     |
| `proof`               | `Data`   | variable     | Halo2 ZKP #1 proof                        |
| `voteRoundId`         | `Data`   | 32 bytes     | Voting round identifier                   |
| `sighash`             | `Data`   | 32 bytes     | Delegation sighash                        |

### VoteCommitmentBundle

Maps to `MsgCastVote` (see [cosmos-sdk-messages-spec.md §4.3](cosmos-sdk-messages-spec.md#43-msgcastvote)).

| Field                      | Type     | Size     | Description                                          |
| -------------------------- | -------- | -------- | ---------------------------------------------------- |
| `vanNullifier`             | `Data`   | 32 bytes | Nullifier of the VAN being consumed                  |
| `voteAuthorityNoteNew`     | `Data`   | 32 bytes | New VAN with decremented proposal authority          |
| `voteCommitment`           | `Data`   | 32 bytes | `H(DOMAIN_VC, sharesHash, proposalId, voteDecision)` |
| `proposalId`               | `UInt32` | 4 bytes  | 0-indexed proposal identifier                        |
| `proof`                    | `Data`   | variable | Halo2 ZKP #2 proof                                   |
| `voteRoundId`              | `Data`   | 32 bytes | Voting round identifier                              |
| `voteCommTreeAnchorHeight` | `UInt64` | 8 bytes  | Vote commitment tree anchor height used by prover    |

### EncryptedShare

ElGamal ciphertext of a single voting share.

| Field            | Type     | Size     | Description                                            |
| ---------------- | -------- | -------- | ------------------------------------------------------ |
| `c1`             | `Data`   | 32 bytes | `r * G` (Pallas generator)                             |
| `c2`             | `Data`   | 32 bytes | `v * G + r * eaPK`                                     |
| `shareIndex`     | `UInt32` | 4 bytes  | Index of this share in the decomposition               |
| `plaintextValue` | `UInt64` | 8 bytes  | Power-of-2 share value (kept for payload construction) |

### SharePayload

Sent to the helper server for share delegation (not directly to chain).

| Field          | Type             | Size     | Description                                 |
| -------------- | ---------------- | -------- | ------------------------------------------- |
| `sharesHash`   | `Data`           | 32 bytes | Poseidon hash of all encrypted shares       |
| `proposalId`   | `UInt32`         | 4 bytes  | Proposal this share belongs to              |
| `voteDecision` | `UInt32`         | 4 bytes  | Vote decision (0=support, 1=oppose, 2=skip) |
| `encShare`     | `EncryptedShare` | —        | The encrypted share ciphertext              |
| `shareIndex`   | `UInt32`         | 4 bytes  | Index within the decomposition              |
| `treePosition` | `UInt64`         | 8 bytes  | Position in vote commitment tree            |

### VotingRoundParams

Lightweight subset of `VotingSession` passed to crypto operations.

| Field              | Type     | Size     | Description                                             |
| ------------------ | -------- | -------- | ------------------------------------------------------- |
| `voteRoundId`      | `Data`   | 32 bytes | Blake2b hash of session setup fields                    |
| `snapshotHeight`   | `UInt64` | 8 bytes  | Zcash block height for note snapshot                    |
| `eaPK`             | `Data`   | 32 bytes | Election authority public key (Pallas point)            |
| `ncRoot`           | `Data`   | 32 bytes | Note commitment tree root at snapshot                   |
| `nullifierIMTRoot` | `Data`   | 32 bytes | Poseidon IMT root of all Orchard nullifiers at snapshot |

### Encoding Conventions

- All `Data` fields containing curve points: 32-byte compressed Pallas encoding.
- All `Data` fields containing hashes: 32-byte Blake2b-256 or Poseidon (context-dependent).
- `roundId` in Swift is a hex-encoded string of the 32-byte `voteRoundId`: `data.map { String(format: "%02x", $0) }.joined()`.
- Seeds are raw `[UInt8]`, 64 bytes from BIP-39 `mnemonicToSeed`.
- `VoteChoice` maps to `UInt32` on the wire: `support = 0`, `oppose = 1`, `skip = 2`.

---

## Integration Sequence

The canonical call order for a complete voting round, as implemented in `VotingStore`. See the `.initialize`, `.startDelegationProof`, and `.confirmVote` actions.

### 1. Initialization

```
openDatabase(path)
fetchActiveVotingSession()                          → VotingSession
initRound(params, sessionJson)
getWalletNotes(walletDbPath, snapshotHeight, networkId) → [NoteInfo]
  sum(note.value) → votingWeight
generateHotkey(roundId, hotkeySeed)                 → VotingHotkey
```

The app subscribes to `stateStream()` after initialization to drive all UI updates.

### 2. Delegation

```
clearRound(roundId)                                 // reset if re-entering
initRound(params, nil)
buildDelegationSignAction(roundId, notes, senderSeed, hotkeySeed, networkId, accountIndex)
                                                    → DelegationAction
  // Keystone signs action.sighash with action.alpha → spendAuthSig
  // (currently auto-approved in prototype)
storeTreeState(roundId, treeState)
buildDelegationWitness(roundId, action, inclusionProofs, exclusionProofs)
                                                    → Data (witness)
generateDelegationProof(roundId)                    → stream ProofEvent
  // UI shows progress bar during .progress events
  // .completed triggers transition to proposal list
```

### 3. Per-Proposal Voting

Repeated for each proposal the user votes on:

```
decomposeWeight(votingWeight)                       → [UInt64] shares
encryptShares(roundId, shares)                      → [EncryptedShare]
buildVoteCommitment(roundId, proposalId, choice, encShares, vanWitness)
                                                    → stream VoteCommitmentBuildEvent
  // .completed yields VoteCommitmentBundle
submitVoteCommitment(bundle)                        → TxResult (chain tx)
buildSharePayloads(encShares, bundle)               → [SharePayload]
delegateShares(payloads)                            // sends to helper server
markVoteSubmitted(roundId, proposalId)
```

After all proposals are voted on, the UI transitions to the completion screen.
