---
name: CometBFT Vote Chain
overview: "Build a minimal Cosmos SDK v0.53.x chain in a new `zcash-vote-chain` repo that can accept ZKP-backed voting transactions (starting with ZKP #1 / delegation registration), using mock verification initially, with a custom AnteHandler replacing standard Cosmos SDK signature checks with RedPallas + ZKP verification, and a JSON REST API so the Tauri client never touches Cosmos protobuf encoding."
todos:
  - id: scaffold
    content: "Phase 1: Build from stripped-down simapp v0.53.5 (official Cosmos SDK reference), create new zcash-vote-chain repo, verify it boots and produces blocks"
    status: done
  - id: proto-types
    content: "Phase 2: Define protobuf types (tx.proto, query.proto, types.proto, module.proto) and generate Go code"
    status: done
  - id: vote-module
    content: "Phase 2: Implement x/vote module skeleton (module.go, keeper, msg_server, query_server, genesis) with KV store layout"
    status: done
  - id: validation-pipeline
    content: "Phase 3: Build validation pipeline (validate.go) + mock crypto interfaces (RedPallas, ZKP) + nullifier uniqueness check"
    status: done
  - id: msg-handlers
    content: "Phase 4: Implement keeper logic for all 4 message types (setup round, register delegation, vote commitment, reveal share)"
    status: pending
  - id: commitment-tree
    content: "Phase 4: Implement append-only commitment tree state + EndBlocker root computation + height-indexed root snapshots"
    status: pending
  - id: raw-abci-api
    content: "Phase 5: Build JSON REST endpoints + raw ABCI tx pipeline (custom CheckTx/FinalizeBlock decoding, bypass Cosmos Tx envelope)"
    status: pending
  - id: queries
    content: "Phase 5: Implement gRPC query handlers (commitment tree at height, latest tree, vote round, proposal tally)"
    status: pending
  - id: tests
    content: "Phase 6: Unit tests for keeper, integration test for JSON tx submission, recheck behavior test"
    status: pending
  - id: review-strip-staking
    content: "Review: Can we remove staking + distribution modules? Vote chain doesn't need validator economics. Would require rewriting init to set CometBFT genesis validators directly (no gentx), providing a staking shim for genutil or removing genutil, and accepting a static validator set. Revisit after Phase 5 when the custom ABCI tx pipeline is in place."
    status: pending
isProject: false
---

# Minimal CometBFT Tallying Chain for ZCash Voting

## Starting Point

Build from a **stripped-down copy of simapp** from [Cosmos SDK v0.53.5](https://github.com/cosmos/cosmos-sdk/tree/v0.53.5/simapp). This is the official, maintained reference application. We remove modules we don't need (nft, feegrant, group, circuit, epochs, protocolpool, vesting, authz, slashing, evidence, upgrade, gov, mint) and keep only the minimal set needed for a chain that produces blocks: **auth, bank, staking, distribution, consensus, genutil, tx**.

Key version facts from simapp v0.53.5 `go.mod`:

- `github.com/cosmos/cosmos-sdk v0.53.5` (replaced from `v0.53.0`)
- `github.com/cometbft/cometbft v0.38.20` (NOT v1.0.x)
- Module config uses Go-based `app_config.go` with depinject (not YAML)
- The tx config supports `SkipAnteHandler: true` for full custom ante chain replacement

## Repository Structure

New repo: `zcash-vote-chain`

```
zcash-vote-chain/
├── app/
│   ├── app.go                    # ZVoteApp wiring (stripped simapp + vote module)
│   ├── app_config.go             # Go-based depinject module config (NOT yaml)
│   └── abci.go                   # Custom CheckTx/FinalizeBlock with raw tx decoding
├── cmd/zvoted/
│   ├── main.go
│   └── cmd/
│       └── commands.go           # CLI root command (from simapp/simd pattern)
├── proto/zvote/v1/
│   ├── tx.proto                  # MsgRegisterDelegation, MsgVoteCommitment, MsgRevealShare, MsgSetupRound
│   ├── query.proto               # QueryCommitmentTree, QueryLatestTree, QueryVoteRound
│   ├── types.proto               # VoteRound, CommitmentTreeState
│   └── module/module.proto       # Module config for depinject
├── api/                          # JSON REST gateway (no protobuf client needed)
│   └── handler.go                # Accepts JSON, wraps into Cosmos tx, broadcasts
├── x/vote/
│   ├── module.go                 # AppModule, RegisterServices, depinject provider
│   ├── keeper/
│   │   ├── keeper.go             # KVStore access, nullifier set, commitment tree state
│   │   ├── msg_server.go         # Handle MsgRegisterDelegation, MsgVoteCommitment, etc.
│   │   ├── query_server.go       # Commitment tree queries, vote round queries
│   │   └── genesis.go
│   ├── types/
│   │   ├── keys.go               # Store key prefixes (nullifiers, commitments, rounds)
│   │   ├── msgs.go               # Message constructors + ValidateBasic
│   │   ├── errors.go
│   │   └── expected_keepers.go
│   └── validate/
│       └── validate.go           # Validation pipeline: basic checks, nullifier, sig, ZKP
├── crypto/
│   ├── redpallas/
│   │   └── verify.go             # RedPallas stub (interface + mock impl)
│   └── zkp/
│       └── verify.go             # Halo2 verifier stub (interface + mock impl)
├── scripts/
│   └── init.sh                   # Chain init: genesis, validator, config
├── Makefile
├── go.mod
└── go.sum
```

## Phase 1: Scaffold and Boot the Chain

**Goal:** A running single-validator CometBFT chain that produces blocks.

Based on the [simapp v0.53.5 source](https://github.com/cosmos/cosmos-sdk/tree/v0.53.5/simapp):

- Create new repo `zcash-vote-chain`
- Copy and strip down simapp: take `app.go`, `app_config.go`, `ante.go`, `genesis.go`, `export.go`, `simd/` (rename to `zvoted`)
- **Strip `app_config.go**`: Remove all modules except auth, bank, staking, distribution, consensus, genutil, tx. This means removing ~15 module imports and their config entries from the `ModuleConfig` slice
- Set `SkipAnteHandler: true` in the tx module config (already shown in simapp as a comment) -- we will wire our own in `ante.go`
- Update `app.go`: rename `SimApp` to `ZVoteApp`, change `bech32_prefix` to `zvote`, set `DefaultNodeHome` to `~/.zvoted`
- Create standalone `go.mod` pointing to `github.com/cosmos/cosmos-sdk v0.53.5` (with same replace directives as simapp minus the `../.` local replace)
- Write `scripts/init.sh`: `zvoted init`, create genesis account, add gentx
- Write `Makefile` with `install`, `init`, `start` targets
- Verify: `make install && make init && zvoted start` produces blocks

## Phase 2: Define Protobuf Types and `x/vote` Module Skeleton

**Goal:** Register a custom `x/vote` module with message types the chain can route.

### 2.1 Proto Definitions (`proto/zvote/v1/`)

**tx.proto** - Four message types:

```protobuf
// Admin: create a voting session
message MsgSetupVoteRound {
  string creator = 1;
  uint64 snapshot_height = 2;
  bytes  snapshot_blockhash = 3;
  bytes  proposals_hash = 4;
  uint64 vote_end_time = 5;
  bytes  nullifier_imt_root = 6;
  bytes  nc_root = 7;
  // vote_round_id is computed on-chain from these fields
}

// ZKP #1: Register delegation (from keystone-signed action)
message MsgRegisterDelegation {
  bytes rk = 1;                    // Randomized spend auth key (32 bytes)
  bytes spend_auth_sig = 2;        // RedPallas signature
  bytes signed_note_nullifier = 3; // Nullifier of the dummy signed note
  bytes cmx_new = 4;               // Output note commitment
  bytes enc_memo = 5;              // Encrypted memo
  bytes gov_comm = 6;              // Governance commitment
  repeated bytes gov_nullifiers = 7; // Up to 4 governance nullifiers
  bytes proof = 8;                 // Halo2 ZKP #1
  bytes vote_round_id = 9;
}

// ZKP #2: Create vote commitment
message MsgCreateVoteCommitment {
  bytes van_nullifier = 1;
  bytes vote_authority_note_new = 2;
  bytes vote_commitment = 3;
  uint32 proposal_id = 4;
  bytes proof = 5;
  bytes vote_round_id = 6;
  uint64 vote_comm_tree_anchor_height = 7;
}

// ZKP #3: Reveal vote share (server submits)
message MsgRevealVoteShare {
  bytes  share_nullifier = 1;
  uint64 vote_amount = 2;
  uint32 proposal_id = 3;
  uint32 vote_decision = 4;
  bytes  proof = 5;
  bytes  vote_round_id = 6;
  uint64 vote_comm_tree_anchor_height = 7;
}
```

**query.proto**:

```protobuf
// Get commitment tree at a specific anchor height
rpc CommitmentTreeAtHeight(QueryCommitmentTreeRequest) returns (QueryCommitmentTreeResponse);
// Get latest commitment tree
rpc LatestCommitmentTree(QueryLatestTreeRequest) returns (QueryLatestTreeResponse);
// Get vote round info
rpc VoteRound(QueryVoteRoundRequest) returns (QueryVoteRoundResponse);
// Get tally for a proposal
rpc ProposalTally(QueryProposalTallyRequest) returns (QueryProposalTallyResponse);
```

### 2.2 Module Skeleton

- `x/vote/module.go`: Register as an `AppModule` via depinject, register `MsgServer` and `QueryServer`
- `x/vote/keeper/keeper.go`: Accept `storeService` via depinject, define KV prefixes for:
  - **Nullifier set** (`0x01 || nullifier_bytes -> []byte{1}`) -- for gov nullifiers, VAN nullifiers, and share nullifiers
  - **Commitment tree entries** (`0x02 || index -> commitment_bytes`) -- append-only list
  - **Commitment tree roots by height** (`0x03 || height -> root_bytes`)
  - **Vote rounds** (`0x04 || round_id -> VoteRound`)
  - **Tally accumulator** (`0x05 || round_id || proposal_id || decision -> amount`)
- Add `x/vote` to the `ModuleConfig` slice in `app_config.go`, add to `EndBlockers` list for tree root computation

## Phase 3: Validation Pipeline + Crypto Interfaces

**Goal:** Build the validation pipeline that runs inside our custom ABCI handlers (not the SDK's AnteDecorator chain). Since we bypass the Cosmos SDK Tx envelope (see Phase 5), validation is a simple function call chain in `app/abci.go` rather than composable decorators.

### 3.1 Validation Function

```go
// In x/vote/ante/validate.go
func ValidateVoteTx(ctx sdk.Context, msg VoteMessage, keeper Keeper, opts ValidateOpts) error {
    // 1. Basic field validation
    if err := msg.ValidateBasic(); err != nil { return err }
    // 2. Vote round exists and is active
    if err := keeper.ValidateRoundActive(ctx, msg.GetRoundID()); err != nil { return err }
    // 3. Nullifier uniqueness (ALWAYS runs, even on RecheckTx)
    if err := keeper.CheckNullifiersUnique(ctx, msg.GetNullifiers()); err != nil { return err }
    // 4. Skip expensive checks on RecheckTx
    if opts.IsRecheck { return nil }
    // 5. RedPallas signature (mock for now)
    if err := opts.SigVerifier.Verify(msg.GetRk(), msg.GetSighash(), msg.GetSig()); err != nil { return err }
    // 6. ZKP verification (mock for now)
    if err := opts.ZKPVerifier.Verify(msg); err != nil { return err }
    return nil
}
```

### 3.2 RedPallas Verify Interface (MOCK)

- `crypto/redpallas/verify.go`
- Interface: `type Verifier interface { Verify(rk, sighash, sig []byte) error }`
- Mock: always returns nil
- Later: CGo to Rust `reddsa` crate, or pure Go Pallas curve arithmetic

### 3.3 ZKP Verify Interface (MOCK)

- `crypto/zkp/verify.go`
- Interface: `type Verifier interface { VerifyDelegation(proof []byte, pubInputs DelegationInputs) error; VerifyVoteCommitment(...) error; VerifyVoteShare(...) error }`
- Mock: always returns nil
- Later: CGo to Halo2 Rust verifier

### 3.4 Nullifier Check (Real from day 1)

- Queries the KVStore to ensure no nullifier has been seen before
- On `CheckTx`: read-only check
- On `FinalizeBlock`: the keeper writes nullifiers after successful execution
- Runs on every check including `RecheckTx` (nullifiers may have been consumed by the newly committed block)

## Phase 4: Message Handlers (Keeper Logic)

### 4.1 `MsgSetupVoteRound`

- Compute `vote_round_id = Blake2b(snapshot_height, snapshot_blockhash, proposals_hash, vote_end_time, nullifier_imt_root, nc_root)`
- Store the `VoteRound` struct under `vote_round_id`
- Emit event with `vote_round_id`

### 4.2 `MsgRegisterDelegation` (ZKP #1 handler)

- Verify `vote_round_id` matches an active round (checked in AnteHandler or here)
- Record each `gov_nullifier` in the nullifier set (fail if any already exists)
- Append `gov_comm` to the vote commitment tree (new leaf)
- Append `cmx_new` to the vote commitment tree (new leaf)
- Snapshot: after each block, compute and store the tree root for that height
- Emit event with delegation details

### 4.3 `MsgCreateVoteCommitment` (ZKP #2 handler)

- Verify `vote_comm_tree_anchor_height` refers to a valid stored root
- Record `van_nullifier` in the nullifier set
- Append `vote_authority_note_new` to the commitment tree
- Record `vote_commitment` for later share opening
- Emit event

### 4.4 `MsgRevealVoteShare` (ZKP #3 handler)

- Record `share_nullifier` in the nullifier set
- Accumulate `vote_amount` into the tally for `(proposal_id, vote_decision)`
- Emit event

### 4.5 EndBlocker

- At each block commit, compute the commitment tree root and store it keyed by block height
- This provides the anchor heights that ZKP #2 and #3 reference

## Phase 5: JSON REST API + Raw ABCI Tx Pipeline

**Goal:** The Tauri/Zashi client sends a plain JSON POST; no Cosmos SDK protobuf or tx encoding on the client side. We bypass the Cosmos SDK `Tx` envelope entirely.

### Why bypass Cosmos SDK Tx encoding

Vote transactions don't use Cosmos accounts, signers, or fees. The authorization model is entirely custom (RedPallas + ZKP). Wrapping in a Cosmos `Tx` would force a "dummy signer" hack and pull in encoding libraries we don't need. Instead:

- Cosmos SDK module system is still used for **state** (KVStore), **genesis**, **gRPC queries**, and **EndBlocker**
- We only bypass the **Tx encoding/signing/ante** layer for transaction submission

### 5.1 Custom HTTP Endpoints (`api/handler.go`)

Register on the Cosmos SDK API server (in `app.go`'s `RegisterAPIRoutes`):

```
POST /zvote/v1/submit-delegation     -> accepts JSON MsgRegisterDelegation
POST /zvote/v1/submit-vote           -> accepts JSON MsgCreateVoteCommitment
POST /zvote/v1/submit-share          -> accepts JSON MsgRevealVoteShare
POST /zvote/v1/setup-round           -> accepts JSON MsgSetupVoteRound
```

Each handler:

1. Parses JSON body into the Go message struct
2. Serializes to a simple binary format: `[1-byte msg_type_tag || protobuf-encoded message body]` -- no Cosmos Tx envelope, no signer metadata, no fee
3. Broadcasts raw bytes via local CometBFT RPC (`broadcast_tx_sync`)
4. Returns `{ "tx_hash": "...", "error": "..." }` as JSON

### 5.2 Custom ABCI Decoding (`app/abci.go`)

Override `CheckTx` and the `ProcessProposal`/`FinalizeBlock` pipeline in the `BaseApp`:

- **Decode**: Read `msg_type_tag` byte, protobuf-decode the remaining bytes into the correct message struct
- **Validate**: Run our custom validation pipeline in order:
  1. Basic field validation (non-empty fields, valid round ID)
  2. Nullifier uniqueness check (read from KVStore)
  3. RedPallas signature verification (mock for now)
  4. ZKP verification (mock for now)
- **On `CheckTx**`: Validate only. On `RecheckTx`: skip ZKP + sig, only re-check nullifiers
- **On `FinalizeBlock**`: Validate + execute via keeper (write nullifiers, append to tree, accumulate tally)
- **Events**: Emit standard Cosmos SDK events from the keeper so CometBFT indexes them

This replaces both the AnteHandler chain AND the standard Cosmos SDK message routing. The keeper logic (Phase 4) stays the same -- we just call it directly from the ABCI layer instead of through the SDK's `MsgServiceRouter`.

### 5.3 Queries (standard gRPC-gateway, JSON-compatible)

Cosmos SDK still exposes gRPC-gateway REST endpoints for queries (these go through the normal module system, unaffected by our tx bypass):

```
GET /zvote/v1/commitment-tree/{height}
GET /zvote/v1/commitment-tree/latest
GET /zvote/v1/round/{round_id}
GET /zvote/v1/tally/{round_id}/{proposal_id}
```

These return JSON via gRPC-gateway out of the box.

## Phase 6: Testing and Verification

- **Unit tests:** Keeper tests for each message handler (nullifier checks, tree appends, tally accumulation)
- **Integration test:** Submit a mock ZKP #1 tx via the JSON endpoint to a running local chain, verify it lands in a block, verify nullifiers are recorded, verify commitment tree updated
- **Recheck test:** Submit a tx, commit a block, verify RecheckTx only checks nullifiers

## Key Design Decisions

- **Raw ABCI tx pipeline (no Cosmos Tx envelope):** Vote txs bypass the entire Cosmos SDK Tx encoding, signing, and AnteHandler system. Client sends JSON, server encodes to `[tag || protobuf_msg]`, broadcasts raw bytes to CometBFT. ABCI `CheckTx`/`FinalizeBlock` decode and validate directly. The SDK module system is still used for state, genesis, queries, and EndBlocker -- we only bypass the tx submission path.
- **Mock verifiers first:** RedPallas and Halo2 verification are interfaces with mock implementations. Swap in real CGo implementations later without changing any chain logic.
- **Single nullifier namespace:** All nullifier types (gov, VAN, share) share one KV prefix. They are already globally unique by construction (different personalization strings in Poseidon).
- **Append-only commitment tree:** Stored as indexed entries in KV store. Root computed at end of each block via EndBlocker. This mirrors the existing zcash-vote-server approach but using Cosmos SDK state instead of SQLite.
- **Recheck optimization:** On `RecheckTx` (mempool re-validation after a new block), skip expensive ZKP and signature verification. Only re-check nullifier uniqueness, since nullifiers may have been consumed by the newly committed block.

## Dependency Summary

- `github.com/cosmos/cosmos-sdk` **v0.53.5** -- chain framework
- `github.com/cometbft/cometbft` **v0.38.20** -- BFT consensus (this is what SDK v0.53.5 uses, NOT v1.0.x)
- `cosmossdk.io/x/{auth,bank,staking,distribution,consensus}` -- minimal module set for a working chain
- `cosmossdk.io/core` **v0.11.3**, `cosmossdk.io/store` **v1.1.2** -- core and store APIs
- `golang.org/x/crypto` -- Blake2b for `vote_round_id` derivation
- No RedPallas or Halo2 Go deps yet (mock implementations only)

