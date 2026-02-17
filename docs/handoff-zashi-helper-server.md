# Handoff: Wire Zashi to Helper Server

## Goal

Get Zashi (iOS app) submitting shares through the helper server instead of directly to the chain, completing the production voting flow end-to-end.

## Current state

### What works (validated)

The full voting pipeline is validated by an integration test (`voting_flow_librustvoting_path`, CI green on PR #80):

```
Zashi / E2E Test              Chain (1318)           Helper Server (9091)
────────────────              ────────────           ──────────────────
discover round ──────────────► GET /rounds/active
delegate-vote (ZKP#1) ──────► POST /delegate-vote
                               ◄── tree updated (VAN leaf)
sync tree ───────────────────► GET /commitment-tree/leaves
build VAN witness
build vote commitment (ZKP#2)
sign cast-vote
cast-vote ───────────────────► POST /cast-vote
                               ◄── tree updated (+2 leaves)
build share payloads
POST 4 shares ─────────────────────────────────────► POST /api/v1/shares
                                                     │ sync tree from chain
                                                     │ generate VC witness
                                                     │ derive share nullifier
                                                     │ generate ZKP #3 (30-60s)
                               ◄── POST /reveal-share┘
                               (×4 shares, with delay)
auto-tally via PrepareProposal
```

**Rust layer**: Complete. `librustvoting` handles delegation, ZKP #2, cast-vote signing, and share payload construction. The FFI (`zcash-voting-ffi`) exposes all of this to Swift.

**Zashi iOS**: Steps 1 through cast-vote work. Share submission is the gap — it currently posts directly to the chain with a mock proof instead of going through the helper server.

**Helper server**: Complete. Accepts shares, generates real ZKP #3, handles temporal delay for unlinkability, submits `MsgRevealShare` to chain.

### What's broken in Zashi

1. **Shares go to chain instead of helper server** — `VotingAPIClientLiveKey.swift:254-284` posts to `/zally/v1/reveal-share` with `"mock-reveal-share-proof"`
2. **`allEncShares` dropped in Swift model** — FFI returns it (`zcash_voting_ffi.swift:2233`) but `VotingModels.swift:SharePayload` doesn't have the field, so it's lost before reaching the API client
3. **Non-Keystone delegation not wired** — `VotingStore.swift` ~lines 466, 527 skip the delegation pipeline for software wallets

## What needs to change

### Task 1: Add `allEncShares` to Swift SharePayload

The helper server needs all 4 encrypted shares to generate ZKP #3 (they're circuit witnesses for `shares_hash` verification).

**VotingModels.swift (~line 437)** — add the field:
```swift
public struct SharePayload: Equatable, Sendable {
    public let sharesHash: Data
    public let proposalId: UInt32
    public let voteDecision: UInt32
    public let encShare: EncryptedShare
    public let treePosition: UInt64
    public let allEncShares: [EncryptedShare]  // ADD THIS
    // update init accordingly
}
```

**VotingCryptoClientLiveKey.swift (~line 430)** — map it through:
```swift
return ffiPayloads.map {
    SharePayload(
        sharesHash: $0.sharesHash,
        proposalId: $0.proposalId,
        voteDecision: $0.voteDecision,
        encShare: EncryptedShare(...),
        treePosition: $0.treePosition,
        allEncShares: $0.allEncShares.map { EncryptedShare(...) }  // ADD THIS
    )
}
```

### Task 2: Add helper server URL config

The app needs a separate URL for the helper server (different from chain URL).

**Suggested approach**: Add to `ZallyAPIConfig` or similar:
```swift
struct ZallyAPIConfig {
    static var baseURL = "http://localhost:1318"        // chain
    static var helperServerURL = "http://localhost:9091" // helper server
}
```

This should eventually come from server config / round discovery, but hardcoded is fine for now.

### Task 3: Rewrite `delegateShares` to POST to helper server

**VotingAPIClientLiveKey.swift:254-284** — replace the current implementation.

Current (posts to chain with mock proof):
```swift
let body: [String: Any] = [
    "share_nullifier": nullifier.base64EncodedString(),
    "enc_share": encShareBytes.base64EncodedString(),
    "proposal_id": payload.proposalId,
    "vote_decision": payload.voteDecision,
    "proof": Data("mock-reveal-share-proof".utf8).base64EncodedString(),
    "vote_round_id": roundIdBytes.base64EncodedString(),
    "vote_comm_tree_anchor_height": anchorHeight
]
let json = try await postJSON("/zally/v1/reveal-share", body: body)
```

Required (posts to helper server):
```swift
let allEncSharesJSON = payload.allEncShares.map { share -> [String: Any] in
    ["c1": share.c1.base64EncodedString(),
     "c2": share.c2.base64EncodedString(),
     "share_index": share.shareIndex]
}
let body: [String: Any] = [
    "shares_hash": payload.sharesHash.base64EncodedString(),
    "proposal_id": payload.proposalId,
    "vote_decision": payload.voteDecision,
    "enc_share": [
        "c1": payload.encShare.c1.base64EncodedString(),
        "c2": payload.encShare.c2.base64EncodedString(),
        "share_index": payload.encShare.shareIndex
    ],
    "tree_position": payload.treePosition,
    "vote_round_id": roundIdHex,  // hex string, NOT base64
    "all_enc_shares": allEncSharesJSON
]
// POST to helper server, not chain
let json = try await postJSON(ZallyAPIConfig.helperServerURL + "/api/v1/shares", body: body)
```

Key differences from current code:
- **Endpoint**: helper server `/api/v1/shares` instead of chain `/zally/v1/reveal-share`
- **vote_round_id**: hex string (not base64 bytes)
- **No proof**: helper server generates ZKP #3
- **No share_nullifier**: helper server derives it
- **No anchor_height**: helper server determines its own
- **all_enc_shares**: array of 4 shares with c1/c2/share_index
- **tree_position**: included (helper server needs this for witness generation)

The `delegateShares` method signature may also need to change — it currently takes `roundIdHex` and `anchorHeight`, but with the helper server, `anchorHeight` is no longer needed from the caller.

### Task 4: Wire non-Keystone delegation (lower priority)

`VotingStore.swift` ~lines 466 and 527: `witnessVerificationCompleted` and `delegationApproved` set `delegationProofStatus = .complete` without running the delegation pipeline for `!isKeystoneUser`. Need to call `startDelegationProof` so the non-Keystone path runs `buildAndProveDelegation`, submits the delegation TX, stores VAN position, then proceeds to proposals.

The Rust layer already handles software-wallet signing — this is purely a Swift wiring gap.

## Reference: helper server wire format

The helper server expects this JSON at `POST /api/v1/shares`:

```json
{
    "shares_hash": "<base64, 32 bytes>",
    "proposal_id": 1,
    "vote_decision": 1,
    "enc_share": {
        "c1": "<base64, 32 bytes>",
        "c2": "<base64, 32 bytes>",
        "share_index": 0
    },
    "tree_position": 2,
    "vote_round_id": "<hex, 64 chars>",
    "all_enc_shares": [
        {"c1": "<base64>", "c2": "<base64>", "share_index": 0},
        {"c1": "<base64>", "c2": "<base64>", "share_index": 1},
        {"c1": "<base64>", "c2": "<base64>", "share_index": 2},
        {"c1": "<base64>", "c2": "<base64>", "share_index": 3}
    ]
}
```

Response: `{"status": "queued"}` (200 OK)

The reference implementation is in `e2e-tests/src/payloads.rs:helper_share_payload()`.

## Key files

| File | Role |
|------|------|
| **Zashi iOS** | |
| `VotingStore.swift` | Orchestrates full flow: init → delegation → vote → shares |
| `VotingAPIClientLiveKey.swift` | REST calls to chain + helper server — **main file to change** |
| `VotingAPIClientInterface.swift` | API client protocol |
| `VotingCryptoClientLiveKey.swift` | FFI calls to librustvoting — needs `allEncShares` mapping |
| `VotingModels.swift` | Swift types — needs `allEncShares` field on `SharePayload` |
| **Rust / FFI** | |
| `zcash-voting-ffi/rust/src/lib.rs` | FFI boundary (SharePayload already has `all_enc_shares`) |
| `librustvoting/src/storage/operations.rs` | Core Rust logic for share payloads |
| **Reference** | |
| `e2e-tests/tests/voting_flow_librustvoting.rs` | Canonical e2e test (steps 9-10 show share flow) |
| `e2e-tests/src/payloads.rs` | `helper_share_payload()` — exact wire format |
| `helper-server/src/types.rs` | `SharePayload` struct the server deserializes |

## Running locally

```bash
# Terminal 1: Chain
cd sdk && make init && make start

# Terminal 2: Helper server
cd helper-server && cargo run --release --bin helper-server -- \
  --tree-node http://127.0.0.1:1318 \
  --chain-submit http://127.0.0.1:1318 \
  --min-delay 1 --max-delay 3 \
  --db-path :memory:

# Terminal 3: E2E test (validates the full flow)
cargo test --release --manifest-path e2e-tests/Cargo.toml \
  voting_flow_librustvoting_path -- --nocapture --ignored
```

## Remaining items (not in scope for this task)

| Item | Priority | Details |
|------|----------|---------|
| Keystone spendAuthSig not persisted | Medium | `VotingStore.swift:693` — sig extracted but not stored in DB |
| IMT server URL hardcoded | Medium | `VotingStore.swift:570,713` — `http://46.101.255.48:3000` |
| ZKP #2 Condition 5 disabled | Low | Range-check layout conflict in `orchard/src/vote_proof/` |
| VC tree not persisted to SQLite | Low | In-memory only; app restart re-downloads all leaves |
