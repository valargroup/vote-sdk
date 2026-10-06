# vote-sdk chain deep read: what proxy delegation would touch

**Root:** `/Users/czstudio/Documents/vote-sdk/.claude/worktrees/vote-delegation-planning-29b451` (main @ `6e5f6928`). All paths below are relative to that root unless they are absolute.

**Labels used:**
- **[code]** is what the code does today.
- **[doc]** is what a document claims.
- **[inference]** is my own reasoning.

"Proxy delegation" means giving voting weight to another person. "Delegation", `MsgDelegateVote` and ZKP1 keep their codebase meaning (note → VAN).

---

## 1. Vote-round message inventory

### 1.1 Wire format, tags and decoding [code]
- **Custom wire format:** vote transactions skip the Cosmos Tx envelope. The format is `[1-byte tag][protobuf body]` (`api/codec.go:91-109`).
- **Tags** (`api/codec.go:27-46`):

  | Tag | Message |
  |---|---|
  | `0x02` | `MsgDelegateVote` |
  | `0x03` | `MsgCastVote` |
  | `0x04` | `MsgRevealShare` |
  | `0x05` | `MsgSubmitTally` (proposer-injected) |
  | `0x06` | `MsgCastVoteBatch` |
  | `0x07` | `MsgDelegateAndCastVoteBatch` |
  | `0x08` | Ack (ceremony) |
  | `0x0D` | Partial decryption (ceremony) |
  | `0x0E` | DKG contribution (ceremony) |

  `0x0A` is deliberately unused because it collides with the standard Cosmos `TxRaw` field-1 encoding (`api/codec.go:25-26`).
- **`IsVoteTag` is a contiguous range check** `0x02..0x07` (`api/codec.go:57-59`). Any new tag (`0x09`, `0x0B`, `0x0C`, `0x0F`, …) must be added explicitly.
- **Decoder:** `app/decode.go:23-48` routes a tx by its first byte when `IsCustomTag`. Otherwise it falls through to the standard Cosmos decoder.
- **Canonical encoding is enforced only for tags `0x06` and `0x07`** (`api/codec.go:143-154`): no unknown fields, and the bytes must re-encode deterministically.
  - Tags `0x02`, `0x03` and `0x04` accept unknown proto fields. **[inference]** Their tx bytes are therefore malleable: the same content can produce many tx hashes, which bypasses the mempool cache.
- **`app/ante.go`:**
  - Vote transactions get an infinite gas meter and are fee-less (`app/ante.go:128-129`).
  - Ceremony tags are checked against the proposer (`:134-155`).
  - Vote messages go to `voteante.ValidateVoteTx` (`:157-182`).
  - Standard Cosmos txs with more than one message are rejected (`:87-90`).
  - Vote-module messages inside standard txs are rejected by `isVoteModuleMsg` (`:240-248`) and by the whitelist (`app/ante_whitelist.go:46-69`).

### 1.2 ValidateBasic (stateless) [code] (`x/vote/types/msgs.go`)

**`MsgDelegateVote` (`:66-117`)**
- `rk` is 32 bytes and not the identity point. The signature is 64 bytes. `nf_signed`, `cmx_new` and `van_cmx` are 32 bytes each.
- There are 1–5 `gov_nullifiers`, each 32 bytes.
- Duplicate gov nullifiers are rejected (`:97-106`) **"since the circuit does not constrain the 5 governance nullifiers to be distinct"**.
- Proof is ≤ `MaxProofSize` (15 KiB, `keys.go:74`). The round ID is 32 bytes.
- `tx1_effects` must be 821 bytes and bound to `rk`, `nf` and `cmx_new` (`types/tx1.go`, `ffi/tx1/effects.go:10-24,39-63`).
  - **[inference]** From the field offsets, the 820-byte Ironwood action carries the usual Orchard `enc_ciphertext` (580 bytes) and `out_ciphertext` (80 bytes). These opaque ciphertexts live in tx bytes only. They are not written to state or events.

**`MsgCastVote` (`:128-160`)**
- `van_nf`, `van_new` and `vc` are 32 bytes each. `proposal_id` is in 1..50. Proof is ≤ 15 KiB. The round ID is 32 bytes.
- Anchor height is non-zero, unless the cast is nested in the composite, where a synthetic zero is allowed.
- The signature is 64 bytes. `r_vpk` is 32 bytes and not the identity point.

**`MsgCastVoteBatch` (`:167-208`)**
- 1..`MaxCastVoteBatchSize` (= `MaxProposals` = 50, `:14-15`) votes.
- All votes share one round and one anchor height.
- Proposal IDs and VAN nullifiers must be unique within the batch.

**`MsgDelegateAndCastVoteBatch` (`:213-235`)**
- Requires both a delegation and a batch, with the same round.
- Every nested cast must use anchor height `0`.

**`MsgRevealShare` (`:238-261`)**
- 32-byte share nullifier, 64-byte `enc_share`, a valid proposal ID, decision < 8, proof bound and a non-zero anchor.

**Phase rules:** `AcceptsTallyingRound` is false for every vote-round message, including `RevealShare` (`:540-560`).

### 1.3 Ante pipeline [code] (`x/vote/ante/validate.go:55-144`)
1. `ValidateBasic`.
2. Round status: `ValidateRoundForVoting` requires ACTIVE and `blockTime < vote_end_time` (`keeper_voting.go:313-341`). In CheckTx with an unset block time after a restart it returns nil (`:324-333`).
3. Proposal existence for every cast, before any signature or proof work (`validate.go:93-117`).
4. Nullifier uniqueness against the store. This runs even on Recheck. The composite message checks its gov and VAN families explicitly (`:119-135`).
5. Recheck stops here (`:137-140`).
6. `verifyProofs` dispatch (`:148-179`):
   - **Delegation** (`:248-318`): decode `rk`, compute the TX1 sighash, verify RedPallas, look up the round, run ZKP1 with `nc_root` and `nf_imt_root`.
   - **Cast** (`:323-378`): decode `r_vpk`, compute the cast sighash, verify the signature, look up the root at the anchor height, look up `ea_pk`, run ZKP2.
   - **Batch** (`:383-448`): check every signature first, then the root at the shared anchor. Proof `i+1` is anchored to `SingleLeafRoot(vote[i].van_new)`. Verification stops at the first failing proof.
   - **Composite** (`:185-243`): check all cast signatures over the composite digest, run the full delegation check, then anchor the first cast to `SingleLeafRoot(delegation.van_cmx)`.
   - **Reveal share** (`:471-504`): look up the root at the anchor height, then run ZKP3.

**Anchor freshness:** the chain accepts any historical stored root. There is no recency window. The ZKP2 anchor-height slot is not constrained in-circuit, so the height↔root binding is the chain lookup (voting-circuits `src/vote_proof/circuit.rs:169-177`).

### 1.4 FFI and circuits crate [code]
- **Go verifier interface:** `ffi/zkp/verify.go:12-65` defines three input structs and three methods. It also has a `MockVerifier`; the package comment still calls it "a mock … will later be replaced", which is stale.
- **cgo bindings:** `ffi/zkp/halo2/verify.go`. Delegation inputs are packed as 12×32 bytes (`:145-200`); vote and share inputs as 9×32 bytes.
- **Rust staticlib exports** (`circuits/src/ffi.rs`):
  - `sv_verify_delegation_proof` (`:426`)
  - `sv_verify_vote_proof` (`:666`, 11 field elements: van_nf, r_vpk x and y, van_new, vc, root, anchor height, proposal ID, round ID, ea_pk x and y)
  - `sv_warm_verifier_caches` (`:854`, one thread per circuit)
  - `sv_verify_share_reveal_proof` (`:937`)
  - Tree, round-ID and share helpers.
- **Verifying keys** are derived at runtime from circuit code through `*_cached_keys()`. Nothing about them is stored on chain.
- **Version pin:** `circuits/Cargo.toml` pins `voting-circuits =0.12.0`. Origin/main is 0.12.2.

### 1.5 Message handlers [code] (`x/vote/keeper/msg_server.go`)
- **`DelegateVote` (`:144-171`):** check-and-set each gov nullifier, append only `van_cmx` (`cmx_new` is never put in the tree, `:155-157`), emit `delegate_vote{vote_round_id, leaf_index, nullifier_count}`.
- **`CastVote` (`:176-216`):** recheck the proposal and that the anchor root exists, check-and-set the VAN nullifier, append `van_new` then `vc`, emit `cast_vote{vote_round_id, leaf_index:"van,vc"}`. The event does not include the nullifier.
- **`CastVoteBatch` (`:221-284`)**:
  1. Re-run `ValidateBasic`, then check that the anchor root exists, the proposals exist and the nullifiers are unique.
  2. Call `SetNullifier` for each nullifier.
  3. Append only the final VAN, then every VC in action order.
  4. Emit `cast_vote_batch` with `batch_digest`, `batch_size`, `final_van_leaf_index`, `vote_commitment_leaf_indices`, `proposal_ids` and `van_nullifiers`.
- **`DelegateAndCastVoteBatch` (`:290-354`):** runs every fallible check before any write (`:298-310`), then writes. The delegation VAN is never appended.
- **`RevealShare` (`msg_server_tally_decrypt.go:22-66`):**
  1. Check the round is still voting (`:26`), then validate the proposal and decision.
  2. Check-and-set the share nullifier (`:44`).
  3. `AddToTally` homomorphically adds the share (`:49`, `keeper_tally.go:35+`), and the share count is incremented (`:54`).

**Load-bearing invariant [code]:** `CheckNullifiersUnique` (`keeper_voting.go:54-66`) checks the store only. It does **not** detect duplicates inside the list. Batch handlers then call plain `SetNullifier`. Intra-message dedupe in `ValidateBasic` is therefore the only guard.

### 1.6 Nullifier keyspaces [code] (`types/keys.go:98-114,210-222`)
- Key layout: `0x01 || type || round_id || nf`.

| Type | Recorded by |
|---|---|
| `0x00` gov | ZKP1 |
| `0x01` VAN | ZKP2 |
| `0x02` share | ZKP3 |

- Genesis validation accepts types 0–2 only (`types/genesis.go:93-104`).
- No round state is ever deleted. The only `Delete` calls are for endorsers and Pallas keys. Genesis export includes all nullifiers, leaves and roots (`keeper/genesis.go:170-260`).

### 1.7 Vote commitment tree semantics [code]
- **Append:** `AppendCommitment` writes a leaf to `0x14||round||0x02||idx` and bumps `NextIndex` during DeliverTx (`keeper_voting.go:99-117`). It has **no capacity check**.
- **Root:** EndBlock, for ACTIVE rounds only, calls `ComputeTreeRoot` (`module.go:437-489`). That appends delta leaves from KV into the Rust ShardTree (`keeper.go:181-227`).
  - A root is stored at height H only when it changed. `BlockLeafIndex(H) = (start, count)` is stored, and a `commitment_tree_root` event is emitted.
  - Errors return from EndBlock, which halts the node by design (`module.go:441-460`).
- **Capacity:** depth 24 = 16,777,216 leaves per round (vote-commitment-tree 0.6.1 `src/hash.rs:13-25`).
  - `ErrCommitmentTreeFull` is registered (`types/errors.go:20`) but never used.
  - `MaxTreePosition` in `keys.go:76-79` says 2^32−1, which contradicts the 2^24 tree.
- **Anchors:** a tx can anchor to any height H at which a root was stored. Leaves appended in block H are first usable at anchor H, by txs in block H+1 or later.

### 1.8 How clients learn leaf positions [code]
- From tx events. `GET /shielded-vote/v1/tx/{hash}` proxies CometBFT `/tx` and returns the events (`api/handler.go`, `queryTxByHash`).
- zcash_voting parses `leaf_index`, `final_van_leaf_index` and `vote_commitment_leaf_indices` with strict shape rules. Batch positions must be contiguous (`final+1+i`), and any position ≥ 2^24 is rejected (zcash_voting `docs/chain_submission_invariants.md:996-1022`).
- Fallback: tree scan via `CommitmentLeaves`.
- The custody handoff ships the tx hash off-chain, and the voter polls for `leaf_index` (`exporting-to-external-software.md:140-160`).

### 1.9 REST and query endpoints [code]
**Transaction routes** (`api/handler.go:157-169`), all `POST /shielded-vote/v1/`:
- `delegate-vote`, `cast-vote`, `cast-vote-batch`, `delegate-and-cast-vote-batch`, `reveal-share`.

**Other transaction-side routes:**
- `GET snapshot-data/{height}`, `GET tx/{hash}`, `GET readiness`.

**Query routes** (`api/query_handler.go:46-71`):
- `commitment-tree/{round}/latest`, `commitment-tree/{round}/leaves?from_height&to_height`, `commitment-tree/{round}/{height}`
- `rounds`, `rounds/active`, `rounds/overview`, `round/{id}`
- `tally`, `tally-results`, `partial-decryptions`, `vote-summary`
- `ceremony`, `pallas-keys`, `vote-managers`, `coordinator-actions`
- `endorsers`, `endorsed-rounds/{id}`, `genesis`, **`protocol-capabilities`**

**Tree sync:**
- `CommitmentLeaves` is paginated by height, up to 5000 leaves per page, but a block is never split (`keeper_voting.go:150-231`, `keys.go:83`).
- `LatestCommitmentTree` and `CommitmentTreeAtHeight` are the other two.
- **There is no query for whether a nullifier is spent.**

**What is publicly reachable [code]:** Caddy exposes only the REST port, not the CometBFT RPC (`deploy/Caddyfile`). **[inference]** Wallets therefore cannot reach `block_results` or `tx_search`. Standard SDK gRPC-gateway routes are probably registered through `app.App.RegisterAPIRoutes` (`app/app.go:361-362`), but I did not verify that they are reachable.

---

## 2. Atomic batches [code]

**`MsgCastVoteBatch`:**
- **Size and anchoring:** up to 50 actions, sharing one round and one real anchor.
- **Digest:** `ComputeCastVoteBatchSighash` (`sighash.go:62-102`, domain `SVOTE_CAST_VOTE_BATCH_SIGHASH_V1`) is Blake2b over the round, anchor, count, and for each action its index, `r_vpk`, `van_nf`, `van_new`, `vc` and `proposal_id`. Every `vote_auth_sig` signs this single digest. That prevents truncation, reordering and grafting.
- **Chaining:** proofs are chained through single-leaf roots.
- **What is written:** only the final VAN is appended; every VC is kept. Intermediate VANs never exist on chain, but their nullifiers are recorded.
- **Encoding:** canonical protobuf on the wire (`api/codec.go:143-154`) and strict JSON at REST (unknown fields and trailing values rejected, `api/handler.go:~810-846`).
- **Response:** the REST response echoes the digest (`api/handler.go:483-488`).

**`MsgDelegateAndCastVoteBatch` (#452):**
- **Digest:** domain `SVOTE_DELEGATE_AND_CAST_VOTE_BATCH_SIGHASH_V1` binds the round, `delegation.van_cmx`, count and the ordered actions (`sighash.go:104-132`). The delegation's own signature covers `tx1_effects`, and ZKP1 binds `van_cmx`.
- **Anchoring:** the synthetic anchor `0` means `SingleLeafRoot(van_cmx)` (`validate.go:218-241`, `ffi/votetree/verify.go:13-35`).
- **Atomicity:** every check runs before any write (`msg_server.go:298-321`), so the transaction is all-or-nothing.

**Test and size notes:**
- Tests: `types/delegate_and_cast_vote_batch_test.go` and `cast_vote_batch_test.go`.
- PR #452 (`git show --stat 462fc28b`) touched 44 files, listed in §3.
- **[inference] Size of a 50-vote composite:** about 11.3 KB per cast (11,040-byte ZKP2 proof plus fields) gives about 577 KB raw and about 780 KB as JSON. That is roughly 78% of the effective REST cap (§3.3).

---

## 3. What `MsgProxyDelegate`, `MsgMergeVans` and their composites would touch

### 3.1 Checklist (modelled on PR #452, `462fc28b`)
1. **Protobuf:** messages and `rpc` entries in `proto/svote/v1/tx.proto:14-32`, then regenerate `tx.pb.go` and `tx_grpc.pb.go` (protoc-gen-go v2).
2. **Codec registration** in `x/vote/types/codec.go`:
   - Add `RegisterImplementations` entries (`:34-58`).
   - Add a gogoproto `RegisterType` for every nested message type (`:17-25`). Nested output structs need this, or SDK unknown-field rejection fails.
3. **No-op signer provider** for the custom tx path: `ProvideXSigner` in `x/vote/module.go:49-67,138-166`.
4. **Validation and helpers:**
   - `msgs.go`: `ValidateBasic` and the `VoteMessage` methods. `AcceptsTallyingRound` must be false.
   - Dedupe every nullifier list across all components of a composite (see 1.5).
   - New sighash domain(s) in `sighash.go`.
5. **Wire codec** (`api/codec.go`): new tag, `TagForMessage`, `DecodeVoteTx`, an explicit `IsVoteTag` branch, and inclusion in the canonical-encoding set.
6. **Ante:**
   - `app/ante.go:240-248` `isVoteModuleMsg`.
   - `x/vote/ante/validate.go`: proposal step if casts are nested, the nullifier step (composite pattern at `:123-128`), and a `verifyProofs` case.
   - Bounded metrics label in `x/vote/ante/metrics.go:72-76,133-147`.
7. **ZKP plumbing:**
   - `ffi/zkp/verify.go`: inputs struct, interface method and mock.
   - `ffi/zkp/halo2/{verify.go,verifier.go,verifier_default.go}`.
   - `circuits/src/ffi.rs`: new `sv_verify_*` function and an added thread in `sv_warm_verifier_caches`.
   - `circuits/include/shielded_vote_circuits.h`, `circuits/src/lib.rs` re-export, and a `circuits/Cargo.toml` version bump.
8. **Keeper and state:**
   - Handler in `msg_server.go`: re-run `ValidateBasic`, all checks before writes, `SetNullifier`, `AppendCommitment` for each output, event.
   - Event types and attribute keys in `types/events.go`, with contiguous output leaf indices.
   - If ciphertexts are stored: a new keyspace in `types/keys.go`. Free prefixes are `0x09`, `0x0D`, `0x1A`+, or a per-round inner prefix under `0x14||round`.
   - Genesis export, import and validation (`keeper/genesis.go`, `types/genesis.go`, `GenesisState`).
   - Query and REST endpoint for payload scanning.
9. **REST:** route in `api/handler.go:157-169`, strict JSON decode, digest echo, and coverage in `TestIngressTimeoutNeverBroadcasts` (`docs/rest-ingress-timeout.md:41-45`).
10. **Capabilities:** new `ProtocolCapabilities` fields and wire tag (`query.proto:67-77`, `query_server.go:27-36`). Clients use these to avoid sending a new tx to old nodes.
11. **Upgrade:** `app/upgrades.go` plus `vX_Y_0_upgrade.go` (no-op handler), `app/upgrade_test.go`, a runbook section, CHANGELOG, and `docs/session-status-lifecycle.md`.
12. **Tests and fixtures:** `e2e-tests` (`api.rs`, `payloads.rs`, `sighash.rs` must mirror the new domain) and `testutil` fixtures.
13. **Optional block cap:** a per-block cap like `MaxVoteShareSubmissionsPerBlock`, enforced in both `PrepareProposal` and `ProcessProposal` (`app/vote_share_submission_proposal.go:13-89`, `app/process_proposal.go:35-38`).

### 3.2 Tag allocation [code + inference]
- `0x09` is the natural next tag; `0x0B`, `0x0C` and `0x0F` are also free.
- Never use `0x0A`.
- **[inference]** Also avoid `0x12` and `0x1A`. They are other `TxRaw` field tags; the SDK's ADR-027 check should already reject those encodings, but avoiding them is safer.

### 3.3 Size limits [code]
| Limit | Value | Where |
|---|---|---|
| REST listener body cap | 1,000,000 bytes (`RPCMaxBodyBytes` default) | valar cosmos-sdk `server/config/config.go:267`, `server/api/server.go:123` |
| REST handler cap | `io.LimitReader(1<<20)` | `api/handler.go:780,814` |
| REST → CometBFT `broadcast_tx_sync` | JSON with base64 tx, Comet RPC `max_body_bytes` 1,000,000 (not overridden) | cometbft `config/config.go:448`; `cmd/svoted/cmd/commands.go:44-53` |
| Mempool | `max_tx_bytes` 1 MiB, `size` 5000, cache 10000, recheck on (defaults) | cometbft `config/config.go:787-801` |
| Block | 5 MiB | `app/consensus_limits.go:13` |
| Proof | ≤ 15 KiB | `keys.go:74` |
| Batch | ≤ 50 casts | `msgs.go:14-15` |

- **[inference]** The effective maximum raw vote tx over REST is about 750 KB, because of base64 inflation in the broadcast JSON.
- A 10-output split with around 150–250 byte ciphertexts is about 15–20 KB, which is easy. Composites of delegation + split + 50 casts still fit. Large merge + cast composites need a size budget.

### 3.4 CheckTx cost [code + inference]
- Every vote tx is fully verified in CheckTx and again in FinalizeBlock. Recheck skips signatures and proofs.
- There is no per-block cap on proof-carrying txs except reveal shares (256 per block). Block size alone bounds about 450 ZKP2 proofs per block.
- I found no latency numbers for proof verification in the repo; Prometheus `svote_vote_tx_verification_*` metrics exist. **[unverified]**
- A garbage tx costs at most the signature checks plus one failing proof, because verification exits early. Anyone can produce a valid RedPallas signature with a random key, so signatures are not a spam gate.

---

## 4. Round lifecycle and timing [code]
- **PENDING:**
  - DKG `REGISTERING` and `DEALT` timeouts are 600 s each (`keys.go:20-26`, `module.go:514-639`).
  - Rounds become ACTIVE on ceremony confirmation. `vote_end_time` is absolute and fixed at creation, so ceremony delay shortens the window.
  - Several ACTIVE rounds can coexist; only one PENDING round is allowed (`msg_server.go:60-67`).
- **ACTIVE → TALLYING:** EndBlock computes roots first and then transitions when `blockTime >= vote_end_time` (`module.go:491-512`).
- **TALLYING → FINALIZED:** by proposer-injected `MsgSubmitTally` (`app/prepare_proposal.go:126-254`) or by a 6 h timeout (`keys.go:31`, `module.go:641-662`).
- **Shares must land before the end:** shares are not accepted in TALLYING (`msgs.go:554-557`; `docs/session-status-lifecycle.md:44,95`). That doc's diagram note at `:17-18` ("TALLYING: Only RevealShare accepted") contradicts the code and is doc drift.
- **Helper timing:**
  - `submit_at` must be before `vote_end_time` (`docs/helper_submission_invariants.md:10-21`).
  - The "last-minute window" is the last 40% of the round, capped at 6 h (`internal/helper/store.go:1606-1619`).
  - Retries stop 5 min before the end (30 s minimum). Late shares get only an immediate attempt (`internal/helper/retry_schedule.go:10-16,68-82`).
  - Measured proving rate is about 0.58 shares/s per worker, with 2 workers by default (`docs/runbooks/software-upgrades.md:446-457`).
- **Reveal load:** each vote commitment produces 16 reveal transactions (`keys.go:50-52`). Reveals are capped at 256 per block, and blocks take about 1.2 s (`docs/blocktimes.md`).
- **No delegation cutoff exists** anywhere (grep for `cutoff`, `delegation_end` and start-time fields returns nothing).

**[inference] If proxy delegation is open until `vote_end_time`, four things go wrong:**
1. The delegate has to scan, merge, cast up to 50 proposals, and get 16 × P reveals proved and included before the end. Anything that misses is lost silently.
2. A vote commitment whose shares only partly land is **partially counted**, because each share carries part of the weight.
3. End-of-round surges saturate helpers and the 256-per-block cap.
4. The delegator cannot reclaim the weight.

**[inference] Implementing a cutoff:**
- It needs a new check, for example `ValidateRoundForProxyDelegation`, that copies the CheckTx unset-time behavior.
- **Round ID binding problem:** the round ID binds only creation height, snapshot hash, proposals hash, `vote_end_time`, the IMT root and the NC root (`ffi/roundid/roundid.go`, `circuits/src/ffi.rs:2080-2140`). Signed voting config covers `round_id || ea_pk || pir layout` (`internal/votingconfig/votingconfig.go:25-26,120-160`). A new stored round field would therefore not be authenticated by the config signatures. A cutoff derived deterministically from `created_at_time` and `vote_end_time` avoids this.

---

## 5. Upgrade and activation mechanics [code + doc]
- **Every protocol change uses a coordinated halt.** Upgrade handlers are registered in `app/upgrades.go:14-27`. v1.5.0 and v1.6.0 are no-op handlers (`app/v1_5_0_upgrade.go`, `app/v1_6_0_upgrade.go`).
  - The runbook says a new wire tag or transaction set "must not be installed as a rolling" update (`docs/runbooks/software-upgrades.md:135-159`).
  - The coordinator schedules the halt with `MsgScheduleUpgrade`. There is no lead-time guard (`:249-252`).
  - **Do not halt during an active round or ceremony, and helper queues must be empty** (`:476-495`).
- **Why mixed versions fail:**
  - An old binary routes an unknown tag to the standard decoder and returns code 2. A new binary executes the tx, so app hashes diverge.
  - Registering a type in the InterfaceRegistry also changes how standard txs decode nested `Any` payloads ("unable to resolve type URL" in the removed dormancy tests, `git show 771fbdef`).
- **Dormant-feature pattern** (`7cd8b521`, then the flip in `771fbdef`):
  - A `types.AtomicVoteBatchesEnabled=false` constant gated the tag and registration so the code could merge as `V:state/compatible`.
  - It was removed in the activation commit, which also added the upgrade handler.
  - Policy is in `docs/release-branches.md:12-36`: "A dormant feature is compatible only when mixed-version validators retain the same accepted inputs".
- **Circuits are not versioned.** There is no per-round circuit version and no verifying-key hash on chain or in `ProtocolCapabilities`. Changing ZKP1, ZKP2 or ZKP3 breaks state and requires a matching wallet prover. **[inference]** Add circuit or VK identifiers to `ProtocolCapabilities`.

---

## 6. Spam and DoS today [code]
**What protects the chain today:**
- Fee-less, infinite gas (`app/ante.go:128-129`).
- Full cryptographic verification in CheckTx. Txs that fail CheckTx are not gossiped.
- Nullifier uniqueness. Mempool defaults (5000 txs, 1 MiB per tx, cache 10000, recheck).
- Reveal cap of 256 per block, with dedupe by round and nullifier (`app/vote_share_submission_proposal.go`).
- 5 MiB blocks.
- REST: crypto-readiness gate, 1 MB body, 15 s read timeout.
- Helper: optional `X-Helper-Token` (`internal/helper/api.go:225-226,610-617`).

**There is no rate limiting anywhere in the repo or in the Caddyfile.** Every validator REST endpoint is public.

**[inference] How a multi-output proxy message changes this:**
- Valid spam is bounded by "1 VAN = at most 50 successor spends", because each ZKP2 clears one `proposal_authority` bit.
- A split does not consume an authority bit. **Without in-circuit limits, one VAN can be split forever** (each step spends one nullifier and creates N new leaves). That allows unbounded leaf growth and ciphertext state, and filling the 2^24 tree.
- A full tree fails the ShardTree append in EndBlock, and **that error halts the chain**. Neither DeliverTx nor `AppendCommitment` has a capacity check.
- Ciphertexts add N × ~150–600 bytes per tx of permanent state if they are stored.

---

## 7. Opaque payloads, state growth and scanning [code + inference]
**Existing payload-like data:**
- `MsgDelegateVote.tx1_effects` holds Orchard-format ciphertexts in tx bytes only. These are never parsed or stored.
- State stores bounded, permissioned blobs:
  - DKG `DealerPayload` ciphertexts inside `VoteRound` (`types.proto:281-295`).
  - Coordinator action `Any` payloads.
  - Round, proposal and description strings.
  - The ceremony log.
- **No permissionless opaque payload is stored in state today.**

**Lifetime of state and blocks:**
- State is never pruned per round (§1.6). Validators prune old versions (`PruningOptionEverything`, `cmd/svoted/cmd/commands.go:126-129`) but keep the latest state forever.
- Genesis export includes every leaf and root.
- Blocks are retained (`min-retain-blocks` 0).
- The snapshot node uses `indexer=null` and `discard_abci_responses=true` (`scripts/reset-snapshot.sh:76-84`), but its API is disabled.
- Chains have historically been reset from genesis (`docs/production-setup.md:101-110`).

**Scanning options for a delegate:**
1. **Events.** These need CometBFT `block_results` or `tx_search`, which are not public; large attributes would also bloat the kv tx index.
2. **State.** A per-round leaf-indexed ciphertext store, served by a paginated height-range query (extend `BlockCommitments` or add a sibling query). This is robust and already shaped like `CommitmentLeaves`.
3. **Raw blocks** through the SDK block endpoint, if exposed. This is heavy for the client.

**Recommendation:** option 2. Bound the ciphertext size, and consider deterministic deletion after FINALIZED plus a delay.

---

## 8. Coordinator, endorser and registry precedents [code + doc]
- **Coordinator actions** (`msg_server_coordinator_actions.go:16-32,226-333`) are a closed set of payload types:
  - create session, update managers, schedule or cancel upgrade, `MsgSetEndorser`, `MsgAuthorizedSend`.
  - Proposals expire after 7 days. The threshold is recounted against current managers. The payload creator must equal the proposer.
  - **[inference]** A coordinator-curated `MsgSetDelegateProfile` would be a small addition.
- **Endorsers:**
  - `MsgSetEndorser` (coordinator payload) maps an `endorser_id` (`[a-z0-9-]{1,64}`) to a bech32 address and creates the auth account (`msg_server_endorsements.go:15-57`, `accounts.go:14-22`).
  - `MsgEndorseRound` and `MsgClearRoundEndorsement` are standard Cosmos txs authorized by that address (`:60-139`), stored at `0x16`/`0x17` (`keys.go:193-199`) and whitelisted (`ante_whitelist.go:55-56`).
  - **[inference]** This is the closest precedent for endorser-signed identity attestations, for example a `MsgAttestDelegate` keyed by endorser.
- **Self-registration by influencers through standard txs is blocked:**
  - Accounts must exist, and only coordinators create them (`MsgAuthorizedSend`, `ensureAccountExists`). Bank sends are disabled.
  - The message whitelist must be extended.
  - Anonymous custom-wire registration would be free spam, because there are no fees and no account.
- **Off-chain signed attestation precedents:**
  - IMT verification acknowledgments are ADR-036 signed intents recorded in config PRs, and are explicitly not consensus (`docs/imt-verification.md:44-75`).
  - Ed25519 coordinator-signed PIR update attestations with embedded trusted keys (`internal/pirupdate/attestation.go:20-67`, `keys.json`).
  - Signed voting-config entries.
- **The chain cannot check X/Twitter.** Any binding from an X handle to an address has to come from trusted verifiers (ICNS-style), stored on chain or off chain.

---

## 9. Tally path [code]
- The tally only sees `MsgRevealShare`. ZKP3 proves VC membership in a stored root and a share nullifier. `AddToTally` homomorphically adds the share into the `(proposal, decision)` accumulator.
- `SubmitTally` checks Lagrange-combined partial decryptions, and the per-accumulator total must be below `TallyBSGSBound` = 2^28 (`keys.go:35`, `msg_server_tally_decrypt.go`, `app/prepare_proposal.go:126-341`).
- None of this references VANs or delegation.
- **Conclusion:** if proxy-delegated outputs are ordinary VANs (same preimage and domain) that an unchanged ZKP2 can spend, **no tally, ZKP3, helper or EndBlock changes are needed**.
- If the VAN preimage gains fields (hop counter, split authority, receipt key), ZKP2 changes. That means a coordinated upgrade, a new wallet prover and a new VK. The tally is still unchanged.
- **Per-VAN weight limits:** ZKP1 keeps `num_ballots ≤ 2^30` (voting-circuits `delegation/README.md:353`), and shares are 30-bit (`params.rs:20`). A merge circuit must keep the merged VAN within these bounds.

---

## 10. "Can I tell whether my delegate voted?" [code + inference]
**No, by default.**
- The VAN nullifier is `Poseidon(vsk.nk, …, van)` and uses the delegate's `nk`. The delegator knows the VAN opening, so they can find its leaf, but they cannot compute its nullifier.
- Vote choices are El Gamal shares; only aggregates are decrypted.
- The chain has no nullifier query, and `cast_vote` events omit nullifiers (batch events include them).
- After a merge, any link is lost.

**Options, each trading away delegate privacy:**
- A delegator-known receipt tag in the VAN that ZKP2 or the merge publishes.
- Per-input "claimed" tags published by the merge.
- Voluntary off-chain disclosure by the delegate.

Chain support would be a nullifier or tag existence query plus event attributes.