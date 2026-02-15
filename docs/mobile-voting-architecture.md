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
|    storage (SQLite)  |  wallet_notes  |  crypto (stubs)   |
+-----------------------------------------------------------+
                            |
                    Zcash wallet DB (read-only)
                    (managed by zcash_client_sqlite)
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
    -> buildDelegationSignAction() + build 1-zatoshi self-spend PCZT + Keystone signing
DelegationConstructed
    -> extract spendAuthSig from signed PCZT
    -> buildDelegationWitness() (spendAuthSig + inclusion + exclusion proofs)
WitnessBuilt
    -> generateDelegationProof() (ZKP #1, long-running with progress)
DelegationProved
    -> buildVoteCommitment() per proposal (ZKP #2)
VoteReady
    -> per-proposal: encrypt shares, build commitment, build share payloads, submit
```

Phase transitions happen inside Rust — each operation validates the current phase, does its work, persists results, and advances the phase atomically.

## Wallet Notes (Voting Power from Zcash Wallet)

The voting protocol requires knowing which Orchard notes a user owned and were unspent at a snapshot block height. This data feeds into `constructDelegationAction` (the sighash covers the note commitments) and determines the user's voting weight.

Rather than modifying the Zcash wallet SDK, librustvoting opens the wallet's SQLite DB **read-only** and queries it directly.

### Query

```
wallet_notes::get_wallet_notes_at_snapshot(wallet_db_path, snapshot_height, network_id)
    -> Vec<NoteInfo> { commitment (cmx), nullifier, value, position }
```

The SQL joins `orchard_received_notes` with `transactions` and `accounts`:

- **Received at or before snapshot**: `t_recv.mined_height <= snapshot_height`
- **Not spent at snapshot**: excludes notes with a spend tx mined at or before snapshot height
- **Valid scope only**: `recipient_key_scope IN (0, 1)` — External and Internal, skips Ephemeral/Foreign
- **Has required fields**: `nf IS NOT NULL`, `commitment_tree_position IS NOT NULL`, `ufvk IS NOT NULL`

### cmx Computation

The wallet DB stores note parts but not the note commitment. Each note's cmx is recomputed:

```
UFVK string (from accounts table)
    -> UnifiedFullViewingKey::decode(network)
    -> .orchard() -> FullViewingKey
    -> .address(diversifier, scope) -> Address

Note::from_parts(address, value, rho, rseed)
    -> .commitment() -> NoteCommitment
    -> ExtractedNoteCommitment -> .to_bytes() -> [u8; 32]  (cmx)
```

This uses `orchard = "0.11"` from crates.io (not the local v0.12 fork) because the types must match what `zcash_keys` uses. The local orchard v0.12 has halo2 breaking changes for the delegation circuit and is a separate concern.

### Data Flow

```
VotingView.onAppear
    -> .fetchVotingWeight
    -> VotingCryptoClient.getWalletNotes(walletDbPath, snapshotHeight, networkId)
    -> FFI: VotingDatabase.get_wallet_notes()
    -> Rust: wallet_notes::get_wallet_notes_at_snapshot()
    -> opens wallet DB read-only, runs query, computes cmx per note
    -> Vec<NoteInfo> returned to Swift
    -> sum(note.value) -> votingWeight (displayed on delegation signing screen)
    -> notes cached in State.walletNotes for use by startDelegationProof
```

The wallet DB path is resolved at runtime from `DatabaseFilesClient` and `ZcashSDKEnvironment` (TCA dependencies), not passed through State. This avoids coupling the parent coordinator to wallet DB naming conventions. The current prototype snapshot height is **3,235,467** (one block before [tx 0bac2a68ca...](https://mainnet.zcashexplorer.app/transactions/0bac2a68ca6cd7deca65a65322da5b678097e927b2325131d089d47b1d9cbc97) at block 3,235,468).

### Dependencies

librustvoting uses `[patch.crates-io]` to resolve `zcash_keys`, `zcash_protocol`, `zcash_address`, `zcash_encoding`, and `zcash_transparent` from the local librustzcash tree. `orchard` comes from crates.io at v0.11 to match the wallet stack.

## TCA Dependency Clients

Three dependency clients, each with live/test implementations:

**VotingCryptoClient** (`VotingCryptoClientInterface.swift`)

- Wraps `VotingDatabase` FFI object via a thread-safe `DatabaseActor`
- `stateStream()` — publishes `VotingDbState` (round info + votes) whenever DB changes
- `getWalletNotes()` — queries Zcash wallet DB for Orchard notes unspent at snapshot height
- `buildDelegationSignAction()` — high-level sign-action boundary used by `VotingStore`; wraps input derivation and delegation action construction
- All crypto operations: hotkey generation, delegation action, witness, proofs, vote commitment, share payloads
- `StreamProgressReporter` bridges UniFFI progress callbacks into `AsyncThrowingStream<ProofEvent>`

**VotingAPIClient** (`VotingAPIClientInterface.swift`)

- HTTP calls to vote chain and helper server
- `submitVoteCommitment()`, `delegateShares()`, `fetchSession()`
- Currently returns mocked responses

**VotingStorageClient** (`VotingStorageClientInterface.swift`)

- Legacy client from before SQLite integration; retained for any storage needs outside the Rust DB

## What's Real vs Stubbed

| Component                      | Status  | Notes                                        |
| ------------------------------ | ------- | -------------------------------------------- |
| Wallet notes at snapshot       | Real    | Read-only query of wallet DB, cmx recomputed |
| Voting weight from notes       | Real    | Sum of note values displayed in UI           |
| SQLite storage + phase machine | Real    | Full CRUD, WAL mode, migrations              |
| Round lifecycle orchestration  | Real    | Phase transitions enforced                   |
| ElGamal share encryption       | Real    | Pallas curve, proper randomness              |
| Binary weight decomposition    | Real    | 4-share limit enforced                       |
| Hotkey generation              | Real    | Random Pallas keypair                        |
| Vote commitment construction   | Stubbed | Returns placeholder hashes                   |
| ZKP #1 (delegation proof)      | Stubbed | Simulates progress, returns dummy proof      |
| ZKP #2 (vote proof)            | Stubbed | Returns placeholder bundle                   |
| Keystone signing               | Real    | QR request/scan roundtrip via signed PCZT    |
| Vote chain API                 | Mocked  | Returns success responses                    |
| Helper server delegation       | Mocked  | `delegateShares()` is a no-op                |
| VAN witness / tree sync        | Stubbed | Hardcoded placeholder data                   |

## Key Design Decisions

**SQLite over in-memory state.** The round lifecycle has many steps that can fail or be interrupted. Persisting to SQLite means the app can resume where it left off. This follows the same pattern as `SDKSynchronizer` in the Zcash wallet SDK.

**`VotingDatabase` as a stateful UniFFI object.** Rather than free functions, the FFI exposes a database handle that owns the connection. This keeps the Rust side simple (no global state) and lets Swift manage the lifecycle through `DatabaseActor`.

**Per-vote publish.** Each vote writes to DB and publishes state immediately, so the UI reflects confirmed votes without waiting for chain submission. The `submitted` flag tracks whether the vote has actually landed on-chain.

**4-share maximum.** The protocol spec limits vote weight decomposition to 4 shares per proposal (binary decomposition, largest 4 powers of 2). This keeps ZKP #2 cheap — just 4 hash preimage checks instead of a Merkle tree circuit.
