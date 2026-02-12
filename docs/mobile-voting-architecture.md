# Mobile Voting Architecture

Governance voting for Zashi iOS. The mobile client handles the full voter flow: delegation signing, ZKP generation, vote commitment, and share delegation to a helper server.

See [Gov Steps V1](https://github.com/z-cale/zally/blob/main/docs/gov-steps-v1.md) for the cryptographic protocol spec. See the [Figma board](https://www.figma.com/board/CCKJMV6iozvYV8mT6H050a/Wallet-SDK-V2) for UI design.

## System Context

```
                    Zcash Mainnet
                         |
                    lightwalletd
                         |
     +-------------------+-------------------+
     |                                       |
  zashi-ios                            vote chain (sdk/)
  (voter)                              (Cosmos, Go)
     |                                       ^
     |            helper server              |
     +--------> (share delegation) ----------+
```

The mobile client is one of several components in the zally repo. This doc covers the three that make up the mobile stack:

| Layer                | Path          | Language      | Role                                 |
| -------------------- | ------------- | ------------- | ------------------------------------ |
| `librustvoting/`     | Rust crate    | Rust          | Core voting crypto + SQLite storage  |
| `zcash-voting-ffi/`  | Swift package | Rust + UniFFI | Bridges Rust to iOS via xcframework  |
| `zashi-ios/modules/` | Swift modules | Swift         | UI, TCA reducers, dependency clients |

## Layer Diagram

```
+-----------------------------------------------------------+
|  VotingView / ProposalListView / ProposalDetailView       |  SwiftUI
+-----------------------------------------------------------+
|  VotingStore (TCA Reducer)                                |  State + Effects
+-----------------------------------------------------------+
|  VotingCryptoClient  |  VotingAPIClient                   |  TCA Dependencies
+-----------------------------------------------------------+
|  ZcashVotingFFI (UniFFI)                                  |  Generated Swift bindings
+-----------------------------------------------------------+
|  librustvoting                                            |  Rust
|    storage (SQLite)  |  crypto (stubs)                    |
+-----------------------------------------------------------+
```

`VotingCryptoClient` is the main integration surface. It wraps a `VotingDatabase` FFI object and exposes the full round lifecycle as async Swift functions. `VotingAPIClient` handles HTTP calls to the vote chain and helper server (currently mocked).

## Data Flow

SQLite is the single source of truth. Every mutating operation writes to the DB, then re-queries and publishes the new state:

```
Rust DB write
    -> publishState() queries rounds + votes tables
    -> CurrentValueSubject<VotingDbState> emits
    -> TCA subscribes via stateStream()
    -> votingDbStateChanged overwrites TCA state
    -> SwiftUI re-renders
```

The TCA reducer never holds authoritative state for rounds, proofs, or votes. `state.votes` is overwritten on every DB update. The only in-memory state that isn't DB-derived is `pendingVote` (uncommitted user choice) and `delegationProofStatus` (UI progress during active proof generation).

## Round Lifecycle

A voting round progresses through phases, tracked in the `rounds.phase` column:

```
Initialized
    -> generateHotkey()
HotkeyGenerated
    -> constructDelegationAction() + Keystone signing
DelegationConstructed
    -> buildDelegationWitness() (inclusion + exclusion proofs)
WitnessBuilt
    -> generateDelegationProof() (ZKP #1, long-running with progress)
DelegationProved
    -> buildVoteCommitment() per proposal (ZKP #2)
VoteReady
    -> per-proposal: encrypt shares, build commitment, build share payloads, submit
```

Phase transitions happen inside Rust — each operation validates the current phase, does its work, persists results, and advances the phase atomically.

## TCA Dependency Clients

Three dependency clients, each with live/test implementations:

**VotingCryptoClient** (`VotingCryptoClientInterface.swift`)

- Wraps `VotingDatabase` FFI object via a thread-safe `DatabaseActor`
- `stateStream()` — publishes `VotingDbState` (round info + votes) whenever DB changes
- All crypto operations: hotkey generation, delegation action, witness, proofs, vote commitment, share payloads
- `StreamProgressReporter` bridges UniFFI progress callbacks into `AsyncThrowingStream<ProofEvent>`

**VotingAPIClient** (`VotingAPIClientInterface.swift`)

- HTTP calls to vote chain and helper server
- `submitVoteCommitment()`, `delegateShares()`, `fetchSession()`
- Currently returns mocked responses

**VotingStorageClient** (`VotingStorageClientInterface.swift`)

- Legacy client from before SQLite integration; retained for any storage needs outside the Rust DB

## What's Real vs Stubbed

| Component                      | Status  | Notes                                   |
| ------------------------------ | ------- | --------------------------------------- |
| SQLite storage + phase machine | Real    | Full CRUD, WAL mode, migrations         |
| Round lifecycle orchestration  | Real    | Phase transitions enforced              |
| ElGamal share encryption       | Real    | Pallas curve, proper randomness         |
| Binary weight decomposition    | Real    | 4-share limit enforced                  |
| Hotkey generation              | Real    | Random Pallas keypair                   |
| Vote commitment construction   | Stubbed | Returns placeholder hashes              |
| ZKP #1 (delegation proof)      | Stubbed | Simulates progress, returns dummy proof |
| ZKP #2 (vote proof)            | Stubbed | Returns placeholder bundle              |
| Keystone signing               | Stubbed | Auto-approved in prototype              |
| Vote chain API                 | Mocked  | Returns success responses               |
| Helper server delegation       | Mocked  | `delegateShares()` is a no-op           |
| VAN witness / tree sync        | Stubbed | Hardcoded placeholder data              |

## Key Design Decisions

**SQLite over in-memory state.** The round lifecycle has many steps that can fail or be interrupted. Persisting to SQLite means the app can resume where it left off. This follows the same pattern as `SDKSynchronizer` in the Zcash wallet SDK.

**`VotingDatabase` as a stateful UniFFI object.** Rather than free functions, the FFI exposes a database handle that owns the connection. This keeps the Rust side simple (no global state) and lets Swift manage the lifecycle through `DatabaseActor`.

**Per-vote publish.** Each vote writes to DB and publishes state immediately, so the UI reflects confirmed votes without waiting for chain submission. The `submitted` flag tracks whether the vote has actually landed on-chain.

**4-share maximum.** The protocol spec limits vote weight decomposition to 4 shares per proposal (binary decomposition, largest 4 powers of 2). This keeps ZKP #2 cheap — just 4 hash preimage checks instead of a Merkle tree circuit.
