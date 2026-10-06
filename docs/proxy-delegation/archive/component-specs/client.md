# Component spec: CLIENT LIBRARY (`zcash_voting`) for proxy delegation (B2, public delegate pools)

Status: design spec. Every repo was read-only. The one prototype is a throwaway planner at `scratchpad/proto-client/planner.py`.

Labels: **[code]** means verified in source, with file:line. **[doc]** means a document claims it. **[inference]** means my reasoning.

Path prefixes:

| Prefix | Location |
|---|---|
| `zv:` | `scratchpad/src/zcash_voting/zcash_voting/` (5.1.1-rc.3) |
| `zvroot:` | `scratchpad/src/zcash_voting/` |
| `vc:` | `scratchpad/src/voting-circuits/` (origin/main v0.12.2) |
| `vc-pv:` | branch `origin/roman/private-vote-implementation` in `/Users/czstudio/Documents/voting-circuits` |
| `sdk:` | the vote-sdk worktree |
| `zip32:` | `~/.cargo/registry/src/index.crates.io-*/zip32-0.2.1/` |
| `vizor:` | `scratchpad/src/vizor-wallet/` |

---

## 0. Terminology and naming

| Concept | In code | User-facing (proposal) |
|---|---|---|
| ZKP1 / `MsgDelegateVote`: moving note weight into a VAN bound to the round hotkey | keep the existing `delegation*` / `Delegation*` names (legacy) | "Authorize voting" (never "delegate") |
| New feature: giving weight to another person | `proxy` module; `ProxyAllocation`, `ProxySlot`, `ProxyBatch` | "Delegate your vote", "Your delegates" |
| The person who receives weight | `Delegate`, `DelegateKey` (DK), `delegate_index` | "Delegate" |
| Baseline "DC" commitment, `Poseidon(DOMAIN_VC, round, shares_hash, 0, d)` | `ProxyCommitment` | not shown |
| ZKP4 | proxy-commitment proof (`zkp4.rs`) | not shown |
| Chain accumulator `TallyKey(round, 0, d)` | delegate pool | never shown (totals stay hidden) |
| Delegate's signed per-proposal choice | `DelegateRoute` | "@alice voted Yes" |
| Helper share relay | "share delivery" in new code | not shown |

The existing code uses a third meaning of "delegation" for helper shares: `share_delegations` and `ShareDelegationRecord` (zv:src/storage/migrations/001_init.sql:138-155; zv:src/types.rs:954-989). New tables and types therefore say "delivery", never "delegation".

---

## 1. Baseline review from the client side

There is no fatal flaw for the client. B2 works with the existing hotkey model, the bundle/VAN model, chained batches and helper share delivery. These are the client-relevant gaps, with fixes:

1. **HIGH: no recovery path for (delegate, amount) after local DB loss.**
   - The DC hides `d` inside a Poseidon hash. The successor VAN hides `w`.
   - After a reinstall, a delegator cannot learn who it delegated to or how much. It cannot recompute DC share nullifiers (blinds are derived secrets). It cannot rebuild helper payloads, and it cannot resume self-voting with the remainder VAN, because `W - Σw` is unknown.
   - Brute force over `w` (≤ 2^30) × `d` is infeasible for large holders. [inference]
   - Fix (adopted here, §3.4 and §3.10): each proxy action carries a 32-byte, sighash-bound `recovery_hint`. It is an AEAD of `(version, layout, d, w)` under an **account-scoped proxy recovery key (ARK)** derived from the account's Orchard FVK.
   - All DC share secrets derive from the same ARK plus `(van_nullifier, d, w, layout)`.
   - A per-round proxy-action feed (chain REST or an untrusted indexer; hints are self-authenticating) is required.
2. **MEDIUM-HIGH: "unchanged ZKP3" is false once private-vote V2 lands.**
   - V2 rewrites ZKP3 in place to the vector form over `DOMAIN_VC_V2` (vc-pv:src/share_reveal/circuit.rs:1-30; `git diff --stat origin/main origin/roman/private-vote-implementation`: share_reveal/circuit.rs 540 lines).
   - DC reveals need the current scalar ZKP3 kept as a separately named circuit with its VK still verified on chain for `proposal_id == 0`. ZKP4 also keeps its own scalar El Gamal gadget, which V2 trims (vc-pv: gadgets/elgamal.rs −193 lines).
   - The client always emits DC share payloads in the V1 scalar shape (§3.8).
3. **MEDIUM: three validators reject proposal 0 today.**
   - The circuit is fine, but:
     - helper `validatePayload` requires `vote_decision < 8` and `proposal_id 1..50` (sdk:internal/helper/api.go:773-779);
     - chain `RevealShare` `ValidateBasic` has the same rule [doc: chain dossier];
     - the client's own vote-recovery path validates `proposal_id` 1..50 and the decision against `num_options` (zv:src/vote.rs:4498-4499; zv:src/share.rs:995).
   - The client therefore uses a separate proxy recovery format and share path (§3.7, §3.8). It does not reuse `VoteRecoveryBundle` with proposal 0.
4. **MEDIUM: transaction size.**
   - ZKP1 + 10 ZKP4 + 50 ZKP2 in one combined tx is about 0.94 MB of JSON. A 50-vote delegate-and-cast is already about 78% of the effective 1 MB REST/Comet cap [doc: chain dossier]. [inference: per-proof sizes from the circuits dossier]
   - The client packer caps one submission at 700,000 JSON bytes and defers overflow casts to a follow-up `MsgCastVoteBatch` (§3.3).
   - The chain must publish explicit caps (`max_proxy_actions_per_batch` = 10, max mixed actions, max bytes).
5. **MEDIUM: delegate validity cannot be checked at DC time.**
   - `d` is hidden until reveal. A DC to an unregistered or retired index burns the weight. Reveals are rejected, or routed to a pool nobody controls.
   - The client therefore makes two mandatory chain cross-checks of `(delegate_index, dk_pubkey, status)`: at commit and immediately before proving (§5.3).
   - The chain must accept reveals for any index that was registered before the round's `vote_end_time` (append-only, never reused).
6. **LOW-MEDIUM: per-delegate DC counts are public.**
   - Every standard DC yields exactly 16 reveals on `(0, d)`; the client posts all 16 shares (zv:src/share.rs:1002-1006). So `reveals(d)/16` approximates the number of delegations each delegate received.
   - The number of DC actions in each tx is also public.
   - Totals stay hidden, so decision 4 holds literally. This is a disclosed side channel (§7).
7. **LOW: zero-weight successor.**
   - When `w = W`, the successor VAN has weight 0 and is still appended (the chain appends the final VAN first: sdk:x/vote/keeper/msg_server.go:221-269).
   - The client must never cast with it, and must never count it as "remaining weight".
8. **Config (affects Zodl).**
   - Bumping `supported_versions.vote_protocol` makes every client without that version reject the whole dynamic config (zv:src/config/mod.rs:1004-1008).
   - Editing the sha256-pinned static config in place breaks every pinned client.
   - Proxy gating must be additive (§6).

---

## 2. Facts this spec builds on (client side)

- **Hotkey.**
  - The stored secret is 64 bytes and is used as a ZIP-32 seed: `UnifiedSpendingKey::from_seed(secret, account 0)`, address index 0 (zv:src/hotkey.rs:11-19, 41-105). [code]
  - It is random per (account, round) (zv:src/hotkey.rs:35-39). [code]
  - Vizor stores it per (account, round) (vizor:lib/src/core/storage/voting_hotkey_store.dart:1-80). [doc: dossier]
- **VAN blinding.** Deterministic, `BLAKE2b(key = hotkey secret, …)` per bundle (zv:src/van_blinding.rs:14-93). [code]
- **ZKP2 secrets.**
  - Share El Gamal randomness, blinds, denomination split and shuffle come from `BLAKE2b-512("ZcashVote_Expand", sk ‖ domain ‖ round ‖ proposal ‖ VAN ‖ share_index)` (vc:src/vote_proof/builder.rs:84, 508-528, 538-585; vc:src/domain_tags.rs:32-43). [code]
  - `alpha_v` is drawn from `OsRng` (zv:src/zkp2.rs:272). [code]
- **Share nullifier.** `Poseidon(share_spend_tag, vote_commitment, share_index, blind)` (vc:src/share_reveal/circuit.rs:170-197). It is computable only with the blind. [code]
- **Batches.**
  - The protocol maximum is 50 actions (zv:src/vote.rs:31-34). [code]
  - Each later proof anchors at `SingleLeafRoot(prev van_new)` (sdk:x/vote/ante/validate.go:380-449). [code]
  - The chain appends the final VAN, then each commitment in action order (sdk:x/vote/keeper/msg_server.go:221-269). [code]
  - Sighashes are BLAKE2b over 32-byte padded fields (zv:src/vote_commitment.rs:160-196; zv:src/delegate_and_vote_batch/authorization.rs:5-50). [code]
- **Planner.**
  - Casts are planned only once ballot intents cover the roster (`roster_is_terminal`, zv:src/round_planning/classify.rs:211). [code]
  - A bundle needs ZKP1 only while it has a due cast (classify.rs:397, 513-555). [code]
  - A cast signs its bundle's delegation (combined tx) unless the delegation is confirmed or imported (classify.rs:593-608). [code]
- **Schema.**
  - v24 (zv:src/storage/migrations.rs:5).
  - `chain_submissions.kind` is enforced by a CHECK (001_init.sql:201-245). The table is fingerprinted and rebuilt on drift (migrations.rs:377-420). [code]
  - Every migration must preserve rows (migrations.rs:18-24). [code]
- **Config.**
  - Wire structs have no `deny_unknown_fields` (config/mod.rs:855-891). [code]
  - Round entries are 1-of-N Ed25519-signed over `RoundAuthPayloadV2` (round_auth.rs:6-53; config/mod.rs:1092-1143). [code]
  - `ProtocolChanged` means "stop using cached voting state" (config/mod.rs:375-382). [code]
- **Wire module.** It is struct-only so FRB can scan it from Vizor (zv:src/wire.rs:1-13). [code]

---

## 3. Delegator

### 3.1 Data model

- **Proxy allocation** is a durable record per `(round, wallet)`. It is independent of ballot intents. Fields:
  - an ordered list of up to 10 entries `(delegate_index, dk_pubkey, basis_points, display_handle_at_commit)`;
  - `remainder ∈ {KeepForSelf, DelegateEverything}`;
  - `plan_digest`;
  - state `committed → locked`.
- **Proxy slot** means one DC action. It is keyed by `(bundle_index, slot_index ∈ 0..=9)` and carries `delegate_index` and `ballots ≥ 1`. There is at most one slot per `(bundle, delegate)`.
- **Effective VAN weight.** For bundle `j`:
  - `remaining_ballots(j) = floor(total_note_value_j / 12_500_000) − Σ ballots(slots of j that are dispatched or confirmed)`.
  - The current VAN is `VAN(addr, remaining_ballots, round, authority, rand)`.
- **What must change for effective weight.** Every place that recomputes a VAN or builds ZKP2 from `bundles.total_note_value` must use the effective weight instead. Known sites: zkp2.rs, vote.rs, precompute/mod.rs, confirmation.rs, storage/operations.rs, storage/queries/mod.rs (`load_van_tree_entries` at :2335), round/mod.rs, chain_submission/generation.rs, delegation_capability.rs.
  - Pass `total_note_value = remaining_ballots × BALLOT_DIVISOR` to the existing builders. It floor-divides back exactly. [inference]
  - Ask circuits for ballot-denominated APIs anyway.
- **Bundle eligibility.** A bundle may host slots only while its VAN authority is `MAX_PROPOSAL_AUTHORITY`: no vote has been dispatched or confirmed and none is lifecycle-owned. ZKP4 requires this. Slots may be added to the same remainder VAN later, before any self-vote, because the successor still has MAX authority.

### 3.2 Allocation algorithm (`proxy::planner`, `PROXY_PLANNER_VERSION = 1`; frozen like `recoverable_bundle_policy_v1`)

**Inputs**

- Eligible bundles `(j, W_j)`. These are bundles with MAX authority and no proxy or vote work in flight. Bundles are already ordered by value descending (zv:src/note_bundling.rs:577-742).
- Entries `(d_i, bps_i)` in user order: `1 ≤ n ≤ 10`, distinct `d`, `1 ≤ bps ≤ 10_000`.
- The remainder policy.

**Validation**

- `Σbps ≤ 10_000` for `KeepForSelf`; `Σbps = 10_000` for `DelegateEverything`.
- `T = ΣW_j ≥ n`.

**Step 1: largest remainder (Hamilton), u128 arithmetic.**

1. Parties are the entries, plus `Keep` with `bps_keep = 10_000 − Σbps` when that is positive. `Keep` is always last in the order.
2. For each party, `q = T·bps`, `floor = q / 10_000`, `rem = q % 10_000`.
3. `R = T − Σfloor`, where `0 ≤ R < #parties`.
4. Sort parties by `(rem desc, bps desc, order asc)` and give +1 ballot to each of the first `R`.

**Step 2: minimum of 1 ballot per delegate.**

- In user order, each delegate with 0 ballots takes 1 ballot from a donor: `Keep` if `Keep ≥ 1`, else the delegate with the most ballots (> 1, ties to the lowest order).
- Mark the delegate, and any delegate donor, `adjusted`. The UI shows exact ballots before commit.
- If there is no donor, return `ProxyPlanError::InsufficientWeight{total_ballots, delegates}`.
- A round may raise the floor through `min_ballots_per_delegate` in the round's proxy config (§6).

**Step 3: assignment to bundles.** The objective, in lexicographic order: fewest DC actions, then fewest bundles touched (an untouched keep-only bundle needs no tx at all), then lowest bundle index.

- `B = 1`: one slot per delegate.
- `B ≤ 4`: exact depth-first search for a split-free packing.
  - Delegates are sorted by ballots descending.
  - Bundles with equal remaining capacity and equal touched-status are pruned as symmetric.
  - The node budget is 2^20.
  - The best packing minimizes bundles touched.
- `B > 4`, or the search fails or exhausts its budget: best-fit decreasing, choosing the bundle with minimal leftover (ties: already touched, then lowest index).
- If no split-free packing is found: first-fit decreasing **with splitting**. Each delegate in descending order repeatedly takes `min(remaining, cap_j)` from the bundle with the largest remaining capacity (ties: touched, then lowest index).
- Each split exhausts a bundle, so the total is `#slots ≤ n + B − 1`. With `n ≤ 10` there are at most 10 slots per bundle, which fits the per-tx cap of 10.
- `slot_index` is assigned per bundle in delegate order, sorted by ballots descending, then user order.
- `Keep` is whatever capacity is left.

**Output: `ProxyPlan`**

- Per-delegate ballots, the slots, per-bundle planned txs, excluded bundles with reasons, warnings, and `plan_digest`.
- `plan_digest = BLAKE2b-256(personal "ZVoteProxyPlan_1", canonical(round_id, planner_version, remainder, [(d, dk, bps)], [(j, slot, d, ballots)]))`.

**Prototype.** The prototype ran about 12k random cases with 1 to 12 bundles and 1 to 10 delegates. It confirmed:

- conservation (`Σ delegates + keep = T`);
- per-bundle capacity;
- `D ≤ #slots ≤ D + B − 1`;
- `B = 1 ⇒ #slots = D`;
- every non-adjusted delegate within 1 ballot of its quota.

**Item 5 worked answer: 2 VANs, 50% to A, 30% to B, keep the rest.**

| Case | Bundles (ballots) | Targets | Slots | DC actions | Txs |
|---|---|---|---|---|---|
| a | W0 = 7,960, W1 = 40 (T = 8,000) | A = 4,000, B = 2,400, keep = 1,600 | A→b0, B→b0 | 2 (the minimum, = D) | b0 only; b1 needs no tx unless the user self-votes |
| b | W0 = 5,000, W1 = 3,000 | same | A→b0, B→b1 | 2 | 2 (no single-bundle packing, since 6,400 > 5,000) |
| c | W0 = W1 = 4,000, "everything" 60/40 | A = 4,800, B = 3,200 | A: b0 4,000 + b1 800; B: b1 3,200 | 3 = D + B − 1 (worst case) | 2 |

- General minimum: `#DC = D` when a split-free packing exists; never more than `D + B − 1`.

### 3.3 Batch composition, ordering and packing

- **Per bundle, one atomic submission whose actions are ordered `[ZKP1?] → DC₀..DCₖ → casts`.** DCs come first because ZKP4 requires MAX authority. Casts spend the post-DC remainder VAN and are ordered by proposal ascending, as today.
  - The first action anchors at the real tree root. In a combined tx it anchors at synthetic height 0, `SingleLeafRoot(van_cmx)`.
  - Every later action anchors at `SingleLeafRoot(prev van_new)` with the same anchor height, mirroring the existing batches (sdk:x/vote/ante/validate.go:380-449).
- **Shapes** (chain contract K1):
  - `MsgProxyCastBatch`: DCs, then casts, spending a confirmed tree VAN.
  - `MsgDelegateProxyCastBatch`: ZKP1, then DCs, then casts.
  - When the bundle's ZKP1 is unconfirmed and not imported, the combined shape is the default. That avoids a separate timing event.
- **When casts ride along.**
  - If the remainder is `KeepForSelf` and the ballot is terminal (`roster_is_terminal`), the bundle's due casts join the same batch.
  - Otherwise the DCs go alone and casts follow later, as an ordinary `MsgCastVoteBatch` on the appended remainder VAN.
  - `DelegateEverything` never plans casts.
- **Packer.**
  - Compute exact JSON bytes before reserving. If the total exceeds `max_batch_json_bytes` (default 700,000, or lower from `ProtocolCapabilities`), keep ZKP1 and all DCs and drop casts from the tail into a follow-up batch.
  - Never split DCs of one bundle across submissions. That would be legal, but it adds a timing event.
  - If ZKP1 + DCs alone exceed the budget, which is impossible at ≤ 10 DCs with today's proof sizes, return `InvariantViolation`.
- **Proving.**
  - Plan the whole VAN chain natively first, with a native ZKP4 transition helper (contract C2).
  - Then prove in parallel with `DEFAULT_BATCH_PROOF_CONCURRENCY` (vote.rs:29). This is the same pattern as `plan_vote_authority_transition` (zv:src/zkp2.rs:35-71).
- **Last-moment window.** If `RoundHostContext::is_last_moment()` is true (zv:src/vote_work/mod.rs:107-121), DCs use the single-share layout, as votes do.
- **After vote end.** No new proxy batch is planned or dispatched after `vote_end_time` (`RoundStepFailureKind::VoteEnded`).

### 3.4 ZKP4 inputs, deterministic secrets, recovery hint and sighash

**Account proxy recovery key (ARK), one per (account, network, round).**

- Primary form:
  `ARK = BLAKE2b-256(key = ovk_orchard_external(account FVK), personal = "ZVoteProxyARK_v1", data = net_u8 ‖ round_id[32])`
  - `net`: mainnet 0, testnet 1, regtest 2.
  - Vizor has a UFVK for software, Keystone and Ledger accounts.
- Fallback when no account key is available (custody or imported capability):
  `ARK_hk = BLAKE2b-256(key = hotkey_stored_secret[64], personal = "ZVoteProxyHRK_v1", data = net_u8 ‖ round_id)`.

**Per-action derivations.** `van_nf` is the input VAN's nullifier for that action, which is unique per action.

```
hint_key = BLAKE2b-256(key = ARK, personal = "ZVoteProxyHint01", data = van_nf)
dc_seed  = BLAKE2b-256(key = ARK, personal = "ZVoteProxySeed01",
                       data = van_nf ‖ d_le32 ‖ w_le64 ‖ layout_u8)   // layout: 0 standard, 1 single-share
```

- Binding `(d, w, layout)` means a rebuilt DC with a different plan never reuses El Gamal nonces. This mirrors the existing standard and single-share domain split (vc:src/vote_proof/builder.rs:538-566).

**Share secrets.** These are prover policy and are not circuit-constrained. They are implemented in voting-circuits (contract C1), so third parties can reproduce them.

- The shares come from the existing denomination split and shuffle algorithm with a new PRF: `BLAKE2b-512(personal "ZcashVoteProxyEx", dc_seed ‖ domain ‖ share_index)`.
- The domains are El Gamal, blind, shuffle, remainder and single-share El Gamal.
- Single-share layout puts `w` in share 0.

**Recovery hint.** It is a fixed 32 bytes.

- Plaintext (16 bytes): `0x01 ‖ layout_u8 ‖ 0x0000 ‖ d_le32 ‖ w_le64`.
- `recovery_hint = ChaCha20Poly1305(hint_key, nonce = 0^12, aad = "ZVoteProxyHint01" ‖ round_id, plaintext)`.
- A zero nonce is safe because the key is unique per `van_nf`.
- The chain treats the hint as opaque, enforces exactly 32 bytes, and binds it through the sighash (K1).

**ZKP4 builder call** (wraps contract C1):

```rust
pub(crate) fn build_proxy_commitment(
  hotkey_seed: &[u8], network: Network, address_index: u32,
  input_ballots: u64, van_comm_rand: &[u8; 32], voting_round_id: &[u8; 32], ea_pk: &[u8; 32],
  delegate_index: u32, ballots: u64, layout: ShareLayout,
  van_auth_path: &[[u8; 32]; 24], van_position: u32, anchor_height: u32,
  proxy_recovery_key: &[u8; 32], progress: &dyn ProgressReporter, obs: &ObservationScope,
) -> Result<ProxyCommitmentBundle, VotingError>
```

- Preconditions: `1 ≤ ballots ≤ input_ballots < 2^30`; input authority = MAX.
- `ProxyCommitmentBundle` fields:
  - `van_nullifier`, `vote_authority_note_new` (VAN with weight `input − ballots`, MAX authority, same `rand`), `proxy_commitment`, `proof`;
  - `enc_shares[16]`, `shares_hash`, `share_blinds[16]`, `share_comms[16]`;
  - `r_vpk`, `alpha_v` (`OsRng`);
  - `recovery_hint`, `delegate_index`, `ballots`, `layout`.

**Sighash and wire.** The chain owns these (K1). The client mirrors them and freezes cross-language vectors.

```
SVOTE_PROXY_CAST_BATCH_SIGHASH_V1 (or SVOTE_DELEGATE_AND_PROXY_CAST_BATCH_SIGHASH_V1 with initial_van = delegation.van_cmx, anchor 0):
 DOMAIN ‖ pad32(round_id) ‖ u64pad32(anchor_height) ‖ pad32(initial_van | 0^32) ‖ u32pad32(n_proxy) ‖ u32pad32(n_vote)
 ‖ for i in proxies: u32pad32(i) ‖ r_vpk ‖ van_nf ‖ van_new ‖ proxy_commitment ‖ recovery_hint
 ‖ for j in votes:  u32pad32(n_proxy + j) ‖ r_vpk ‖ van_nf ‖ van_new ‖ vote_commitment ‖ u32pad32(proposal_id)
```

- `delegate_index` never appears in the tx.

```rust
#[serde(deny_unknown_fields)] pub struct ProxyCommitmentWire {
  van_nullifier: String, vote_authority_note_new: String, proxy_commitment: String,
  recovery_hint: String, proof: String, vote_round_id: String,
  #[serde(rename="vote_comm_tree_anchor_height")] anchor_height: u32, r_vpk: String, vote_auth_sig: String }
#[serde(deny_unknown_fields)] pub struct ProxyCastBatchWire { proxies: Vec<ProxyCommitmentWire>, votes: Vec<VoteCommitmentWire> }
#[serde(deny_unknown_fields)] pub struct DelegateAndProxyCastBatchWire { delegation: DelegationSubmissionWire, batch: ProxyCastBatchWire }
```

- Encoding is canonical base64, as in zv:src/delegate_and_vote_batch/wire.rs.

### 3.5 Planner, `RoundPlan` and `NextStep` changes

**New obligations in `round_planning::classify`.**

- `ProxyDelegate { bundle_index, slots, drafts: Vec<CastDraft>, signs_delegation }`. It is planned for each bundle with committed, undispatched slots.
  - `drafts` holds the remainder casts that are due, and only when the remainder is non-zero and the roster is terminal.
  - `signs_delegation` follows the existing rule (classify.rs:593-608).
- A bundle with slots joins `bundles_needing_delegation` even when it has zero due casts. That is the change to classify.rs:397 and 513-555.
- `AdvanceProxyBatch { bundle_index, anchor_slot }` covers in-flight proxy submissions.
- `DeliverProxy` / `ConfirmProxy` cover DC shares and mirror `Deliver` / `Confirm`.
- New blocks:
  - `ProxyBlocked{bundle, reason: AlreadyVoted | VoteInFlight | DelegationTerminal | DelegateChanged}`;
  - in a round with an imported capability, proxy batches wait for the import barrier (classify.rs:216).
- **Mutual exclusion.** A bundle with an undispatched or in-flight proxy batch is "held". No standalone cast is planned on it, and the reverse also holds. This extends `held_bundles`.

**`NextStep` additions.**

```rust
ProxyDelegate { bundle_index: u32 },
AdvanceProxyBatch { bundle_index: u32, slot_index: u32 },
SubmitProxyShares { bundle_index: u32, slot_index: u32, share_index: u32 },
ConfirmProxyShare { bundle_index: u32, slot_index: u32, share_index: u32 },
```

- `wire::NextStepKind` gets the same four values. `NextStepView` gains `#[serde(default)] slot_index: u32`.
- `kind_view` and every derived predicate stay exhaustive matches (session.rs:553-566). `round_drive/selection.rs` and the `RoundExecutor` dispatch gain the new arms.
- **`RoundPlan` additions:**
  - `proxy: Option<ProxyRoundStatus>`;
  - `needs_proxy_signing` (hotkey needed, plus the ZKP1 signer when combined);
  - `has_in_flight_proxy`;
  - `proxy_bundles_needing_work: Vec<u32>`.
- **`RoundPlanAction::ProxyDelegate`** is a new primary-action value, used while proxy work remains before votes.
- **`completed_for_display`** becomes true for proxy-only rounds once no blocking work remains. `CompletedVoteDisplay` gains `proxies: Vec<(delegate_index, ballots)>`.
- **`needs_draft_setup`** is false when every eligible bundle's remaining weight is 0. With zero self-votes, open proposals no longer block anything.
- **`hotkey_bound`** becomes true once any proxy batch is reserved.

### 3.6 Ballot intents

- Proxy works with **zero intents**: allocation, then commit, then `run_round`.
- Intents apply only to remainder weight. The existing roster-terminal rule still gates casts.
- If the remainder is 0, any recorded intents are inert. The plan reports `proxy.intents_without_weight = true` so the host can clear them.
- `clear_ballot_intent` semantics are unchanged.
- `immediate_share_key`:
  - With at least one voted proposal, it keeps the existing rule (zv:src/share_policy/initial_placement.rs:27-38).
  - In a proxy-only round it becomes `(highest bundle index with a slot, slot 0, share 0)`, pending owner question 2.

### 3.7 Persistence: schema v25 (migration `008_proxy_delegation.sql`, ladder entry `(24, …)`)

New tables. All are created in `001_init.sql` too, and added to `RESET_SQL` (migrations.rs:157).

```sql
CREATE TABLE proxy_allocations (
  round_id TEXT NOT NULL, wallet_id TEXT NOT NULL DEFAULT '',
  remainder TEXT NOT NULL CHECK (remainder IN ('keep','none')),
  planner_version INTEGER NOT NULL CHECK (planner_version = 1),
  plan_digest BLOB NOT NULL CHECK (length(plan_digest) = 32),
  state TEXT NOT NULL CHECK (state IN ('committed','locked')),
  committed_at INTEGER NOT NULL, locked_at INTEGER,
  PRIMARY KEY (round_id, wallet_id),
  FOREIGN KEY (round_id, wallet_id) REFERENCES rounds(round_id, wallet_id) ON DELETE CASCADE);
CREATE TABLE proxy_allocation_entries (
  round_id TEXT NOT NULL, wallet_id TEXT NOT NULL DEFAULT '',
  entry_index INTEGER NOT NULL CHECK (entry_index BETWEEN 0 AND 9),
  delegate_index INTEGER NOT NULL CHECK (delegate_index BETWEEN 0 AND 4294967295),
  dk_pubkey BLOB NOT NULL CHECK (length(dk_pubkey) = 32),
  basis_points INTEGER NOT NULL CHECK (basis_points BETWEEN 1 AND 10000),
  target_ballots INTEGER NOT NULL CHECK (target_ballots >= 1),
  display_handle TEXT,  -- what the user saw at commit; display-only, never an identity key
  PRIMARY KEY (round_id, wallet_id, entry_index), UNIQUE (round_id, wallet_id, delegate_index),
  FOREIGN KEY (round_id, wallet_id) REFERENCES proxy_allocations(round_id, wallet_id) ON DELETE CASCADE);
CREATE TABLE proxy_slots (
  round_id TEXT NOT NULL, wallet_id TEXT NOT NULL DEFAULT '', bundle_index INTEGER NOT NULL,
  slot_index INTEGER NOT NULL CHECK (slot_index BETWEEN 0 AND 9),
  delegate_index INTEGER NOT NULL, ballots INTEGER NOT NULL CHECK (ballots >= 1),
  input_ballots INTEGER NOT NULL CHECK (input_ballots >= ballots),
  layout INTEGER NOT NULL CHECK (layout IN (0,1)),
  van_nullifier BLOB, vote_authority_note_new BLOB, proxy_commitment BLOB, shares_hash BLOB,
  recovery_hint BLOB CHECK (recovery_hint IS NULL OR length(recovery_hint) = 32),
  commitment_bundle_json TEXT,   -- format "zcash_voting_proxy_recovery_v1" (secret: shares, blinds, alpha, sig)
  batch_digest BLOB, batch_index INTEGER, commitment_tree_position INTEGER,
  recovered INTEGER NOT NULL DEFAULT 0, created_at INTEGER NOT NULL,
  PRIMARY KEY (round_id, wallet_id, bundle_index, slot_index),
  UNIQUE (round_id, wallet_id, bundle_index, delegate_index),
  FOREIGN KEY (round_id, wallet_id, bundle_index) REFERENCES bundles(round_id, wallet_id, bundle_index) ON DELETE CASCADE);
CREATE TABLE proxy_helper_share_plans (  -- mirrors helper_share_plans (001_init.sql:96-110)
  round_id, wallet_id, bundle_index, slot_index, commitment_bundle_json, configured_server_urls_json,
  share_plans_json, format_version CHECK (format_version = 1), placement_guarantee, created_at,
  PRIMARY KEY (round_id, wallet_id, bundle_index, slot_index), FK -> proxy_slots ON DELETE CASCADE);
CREATE TABLE proxy_share_deliveries (    -- mirrors share_delegations (001_init.sql:138-155)
  round_id, wallet_id, bundle_index, slot_index, share_index, sent_to_urls, nullifier, confirmed,
  submit_at, created_at, ambiguous_urls, attempting_urls, target_count,
  PRIMARY KEY (round_id, wallet_id, bundle_index, slot_index, share_index), FK -> proxy_slots ON DELETE CASCADE);
CREATE TABLE proxy_recovery_cursor (round_id, wallet_id, scanned_through_height INTEGER, feed_url TEXT, updated_at,
  PRIMARY KEY (round_id, wallet_id));
-- delegate side (wallet-scoped; no FK to rounds: a delegate may hold no notes)
CREATE TABLE delegate_keys (wallet_id, network, key_index INTEGER, origin TEXT CHECK (origin IN ('zip32_registered_v1','random_v1')),
  dk_pubkey BLOB UNIQUE CHECK (length(dk_pubkey)=32), delegate_index INTEGER,
  status TEXT CHECK (status IN ('generated','registration_pending','registered','rotated_out','retired')),
  created_at, updated_at, PRIMARY KEY (wallet_id, network, key_index));  -- secrets never stored in SQLite
CREATE TABLE delegate_registrations (wallet_id, dk_pubkey, statement_json, attestations_json, tx_hash, state, diagnostic, created_at, updated_at,
  PRIMARY KEY (wallet_id, dk_pubkey));
CREATE TABLE delegate_routes (wallet_id, round_id, delegate_index, proposal_id INTEGER CHECK (proposal_id BETWEEN 1 AND 50),
  decision INTEGER NOT NULL,  -- 0..7, or 4294967295 = explicit abstain
  batch_digest BLOB, signed_tx_json TEXT, tx_hash BLOB, state TEXT CHECK (state IN ('signed','submitted','confirmed','rejected','conflict')),
  confirmed_height INTEGER, diagnostic TEXT, created_at, updated_at,
  PRIMARY KEY (wallet_id, round_id, delegate_index, proposal_id));
CREATE TABLE delegate_directory_cache (network, vote_chain_id, sequence INTEGER, sha256 BLOB, issued_at, expires_at, json TEXT, fetched_at,
  PRIMARY KEY (network, vote_chain_id));
CREATE TABLE delegate_pfp_cache (sha256 BLOB PRIMARY KEY, bytes BLOB, mime TEXT, fetched_at INTEGER);
```

**`chain_submissions` rebuild.**

- Update `002_chain_submissions.sql` and `001_init.sql`.
- Use the `006_delegate_cast.sql` rename-copy-drop pattern.
- Add the new kinds:

```sql
kind TEXT NOT NULL CHECK (kind IN ('delegation','vote','vote_batch','delegate_and_cast_vote_batch','proxy_cast_batch','delegate_proxy_cast_batch')),
CHECK ((kind = 'delegation' AND proposal_id IS NULL AND ordered_batch_digest IS NULL)
    OR (kind = 'vote' AND proposal_id BETWEEN 1 AND 50 AND ordered_batch_digest IS NULL)
    OR (kind IN ('vote_batch','delegate_and_cast_vote_batch','proxy_cast_batch','delegate_proxy_cast_batch')
        AND proposal_id IS NULL AND length(ordered_batch_digest) = 32))
```

- `ordered_batch_digest` is the proxy sighash.
- The unique identity index is unchanged.
- The `vote_commitment_positions` blob stores commitment leaf positions for all actions in order (DCs, then VCs).
- The version bump makes an older SDK refuse the DB (migrations.rs:174-185), so it cannot mishandle proxy rows.

**Recovery formats.**

- `zcash_voting_proxy_recovery_v1` per slot holds: round, bundle, slot, `d`, `ballots`, `input_ballots`, `layout`, `anchor_height`, `van_nullifier`, `van_new`, `proxy_commitment`, `proof`, `shares_hash`, `r_vpk`, `alpha_v`, `vote_auth_sig`, `encrypted_shares`, `share_blinds`, `share_comms`, `recovery_hint`, `commitment_tree_position`, `batch{kind, digest, index, size, initial_van?}`.
- Cast members of a mixed batch use a new `zcash_voting_proxy_cast_batch_recovery_v1`, whose `batch.index` is offset by the number of DCs.
- A `combined_cast_rejections`-style streak ledger is keyed by delegation generation, so a repeatedly rejected combined proxy batch stops re-POSTing.

**Cleanup.** Account deletion must delete the wallet-scoped delegate tables explicitly. Round tables cascade.

### 3.8 Helper payloads for DC shares

- Use the existing `VoteShareWire` (zv:src/wire.rs:132-152), with no new fields:

| Field | DC value |
|---|---|
| `proposal_id` | `0` |
| `vote_decision` | `delegate_index` |
| `shares_hash` | the DC's shares hash |
| `tree_position` | the DC leaf |
| `share_comms` | all 16 |
| `primary_blind` | that share's blind |
| `submit_at` | from `plan_share_submission`, with the same schedule and last-moment rules as votes |

- All 16 shares are delivered in the standard layout; one in single-share mode (zv:src/share.rs:1002-1006).
- Payloads are built from `proxy_slots.commitment_bundle_json` through a new `proxy::recover_payloads`. It validates `proposal_id == 0` and `d` against the slot. It never goes through the vote path.
- Share tracking:
  - `ShareKey` gains `commitment: CommitmentRef::{Vote{proposal_id}, Proxy{slot_index}}`.
  - The delivery, confirmation and tracking drivers select the table by variant.
  - Helper status queries use the DC share nullifier `Poseidon(tag, DC, i, blind_i)`.
- Under V2, DC payloads keep this scalar shape. The helper routes `proposal_id == 0` to the scalar ZKP3 (H1).

### 3.9 Tracking and verification ("did my delegate vote")

- **Inclusion.**
  - The `ChainSubmissionClient` lifecycle for the new kinds confirms by hash using the event attributes (K1).
  - If there is no hash, it uses exact-tree recovery with the layout `[final_van, c₁…cₙ]`, extending zv:src/chain_submission/recovery.rs.
  - It stores `commitment_tree_position` per slot.
- **Shares.** Per slot it reports `{total, delivered, confirmed}` from helper share-status by nullifier. Optionally it adds chain share-nullifier existence, which Vizor can IAVL-verify (K5).
- **Routes.** `GET /shielded-vote/v1/delegate-routes/{round}/{d}` (K5). REST is unauthenticated. Vizor may verify route keys with IAVL proofs, as it already does for governance nullifiers.
- **View model.**
  - `proxy::outcomes(db, round, transports) -> ProxyOutcome`.
  - Per delegate: summed ballots over slots, inclusion, shares, and per proposal `Voted(decision) | Abstained (explicit) | NotYetVoted (now < end) | DidNotVote (now ≥ end, unrouted means abstain)`.
  - The view never shows pool totals.
- **Display identity.** The delegate's identity comes from the current verified directory entry when one exists. Otherwise the view shows `display_handle` from commit time, the fingerprint and the index.

### 3.10 Recovery after reinstall

**Tiers.**

- **R0: DB present.** The existing resume path applies.
- **R1: DB lost, hotkey present.**
  1. Rebuild bundles with `recoverable_bundle_policy_v1` (zv:src/note_bundling.rs:332-343) and deterministic VANs (van_blinding.rs).
  2. Compute each bundle's VAN nullifier.
  3. Locate spends with `GET /van-nullifier/{round}/{nf}` (K8, optional), else by scanning the proxy feed.
  4. Decrypt each hint, then follow the VAN chain action by action. This recovers all slots and the remainder VAN (final VAN position from the feed), so self-voting can resume.
- **R2: DB and hotkey lost, account UFVK present.**
  1. Derive ARK.
  2. Scan the per-round proxy feed (K8). For each action: `hint_key(ARK, van_nf)`, then try AEAD open, which costs about a microsecond.
  3. On a hit, derive `dc_seed`, rebuild the shares, and **require `proxy_commitment` to equal the on-chain value**. That makes the hint self-authenticating.
  4. Rebuild the helper payloads and share nullifiers, then redeliver.
  5. Remainder voting is lost. The governance nullifiers are spent and the VAN's `nk` is gone. That is the same as today.
  - Optional chain shortcut: `GET /nullifier/{round}/gov/{nf} → {tx_hash, height}`. Vizor already derives governance nullifiers from the FVK (vizor:docs/voting-participation.md:9-12). This finds the combined tx directly without a full scan.

**What each key re-derives.**

| Material | Hotkey + chain | UFVK (ARK) + feed |
|---|---|---|
| Bundle plan and VANs | yes (v1 policy and hotkey blinding) | no |
| VAN nullifiers and remainder VAN | yes | no |
| `(d, w, layout)` per DC | yes (hint) | yes (hint) |
| DC shares, blinds, randomness, DC, share nullifiers, helper payloads | yes | yes |
| ZKP4 proof, `alpha_v`, signatures | persisted only; regenerated only for never-landed batches | n/a |
| Ballot intents and allocation intent | lost (re-entered); landed slots recovered | landed slots recovered |

- `proxy::recover(db, round, keys: ProxyRecoveryKeys{ark, ark_hk?, hotkey?}, feed) -> ProxyRecoveryReport`.
- Recovery is idempotent and inserts rows with `recovered = 1` and `allocation.state = locked`.
- `ARK_hk` is also tried when the hotkey exists.

### 3.11 Failure handling

| Situation | Behavior |
|---|---|
| The registry entry for `d` changed between preview and commit (key, status) | `commit` fails `ProxyDelegateChanged{d, reason}`; re-preview |
| The entry changed between commit and proving | Block the bundle (`ProxyBlocked::DelegateChanged`). Proceed only on a continuity-signed rotation of the same index (chain-reported). Otherwise surface it; the allocation can be cleared while nothing is dispatched |
| Terminal chain rejection | Retire the batch (members re-proved with the same `dc_seed`, so nonce-safe). Streak ledger; after 3 rejections, `ChainTerminal` with diagnostic |
| Hashless dispatch | `SubmittedWithoutHash`; exact-tree recovery |
| `now ≥ vote_end_time` | `VoteEnded` for new proxy batches; in-flight work still advances |
| ARK missing when a `ProxyDelegate` step runs | `InvalidInput("proxy recovery key required")` before any proving |
| Helper permanently rejects a DC share (invalid delegate) | `HelperDeliveryIncomplete` with a permanent flag. Weight may be stranded, which is why there are two pre-checks |
| All bundles already voted | `ProxyAvailability::Unavailable(WalletAlreadyVoted)` |
| `clear_proxy_allocation` | Refused once any proxy POST is reserved (same rule as `clear_ballot_intent` for lifecycle-owned votes) |

---

## 4. Delegate

### 4.1 Delegate key (DK)

- **Algorithm.** Ed25519 (RFC 8032), using `ed25519-dalek` 2.2, already a dependency (zvroot:Cargo.lock:766). Verification is strict.
- **Software accounts: ZIP-32 *registered* derivation**, not ad-hoc.
  - Ad-hoc derivation is "deprecated … NOT RECOMMENDED for new protocols" (zip32:src/arbitrary.rs:1-13). [code]
  - Registered derivation is designed for this use: keys are tied to a ZIP and support tagged path elements (zip32:src/registered.rs:1-37, 225-262). [code]

  ```
  cv = zip32::registered::cryptovalue_from_subpath(
         context = b"Zcash shielded voting delegate key", seed, zip_number = ZIP_N,
         [ (coin_type' , ""), (account', ""), (key_index', b"zvote-delegate-key-ed25519-v1") ])
  dk_secret = cv[0..32]
  ```

  - `ZIP_N` must be assigned before launch (owner question 6). If it is not, ship random+backup for all accounts. Never ship ad-hoc derivation that would later need a key migration.
  - Rotation is `key_index + 1`.
- **Hardware-only accounts (Keystone/Ledger, no seed in Vizor).**
  - `generate_random_delegate_key` uses OsRng 32 bytes.
  - The backup is mandatory: `encode_delegate_key_backup` produces bech32m with HRP `zvdksec` (testnet `zvdksectest`, regtest `zvdksecregtest`) over `0x01 ‖ secret[32]`.
  - The backup is distinct from wallet mnemonics, so nobody imports it as a wallet.
- **Storage.** The host stores the secret in secure storage, for example Vizor `zcash_account_delegate_key_{uuid}_{key_index}`. SQLite holds only the public key and status.

### 4.2 Encodings

- Public key: bech32m HRP `zvdk` (`zvdktest`, `zvdkregtest`). The `bech32` 0.11 crate is already in the lockfile.
- Fingerprint: `BLAKE2b-256(personal "ZVoteDKFingerpr1", dk)[0..10]` in lowercase base32, shown as `abcd-efgh-ijkl-mnop`. The same digest seeds the identicon.
- Proof post: ASCII, under 280 characters, no URLs or dotted tokens:

  ```
  I accept Zcash shielded-vote delegations.
  zvote-delegate:v1:zvdk1…
  ```

  `DelegateKey::proof_post() -> {text, machine_line}`.

### 4.3 Registration

1. **Statement.** `DelegateRegistrationStatementV1`, canonical JSON with sorted keys:
   - `domain = "zcash-shielded-vote/delegate-registration/v1"`;
   - `network`, `vote_chain_id`, `dk_pubkey` (bech32m);
   - `provider ∈ {x, github, domain}`, `provider_user_id` (X numeric id as a string);
   - `provider_handle` (display only), `proof_locator` (post id, gist id or URL);
   - optional `profile{display_name, bio}`;
   - `issued_at`, `nonce` (16 random bytes).
   - Signature: Ed25519 over `"zcash-shielded-vote:delegate-registration:v1\0" ‖ canonical_json`.
2. **Verifier.** POST the signed statement to the verifier (I1). It returns one or more `DelegateAttestationV1 {verifier_key_id, dk_pubkey, provider, provider_user_id, provider_handle, proof_locator, observed_at, expires_at, statement_sha256, sig}`.
   - The client verifies each attestation against `verifier_keys` from the static config before submitting.
3. **Chain message.** `MsgRegisterDelegate {dk_pubkey, attestations, pop_sig}`.
   - `pop_sig = Ed25519(dk, BLAKE2b-256("SVOTE_DELEGATE_REGISTER_V1" ‖ len‖vote_chain_id ‖ dk ‖ sha256(canonical attestations)))`.
   - POST `/shielded-vote/v1/delegate-register` (K4).
4. **Status.** Poll `GET /delegates/by-key/{dk_hex}` until `delegate_index` is assigned. Store it in `delegate_keys`.
   - States: `Draft → AwaitingVerifier → Attested → Submitted → Registered | Rejected`.

### 4.4 Routes

- **Message.** `MsgDelegateRouteBatch {vote_round_id, delegate_index, routes: [{proposal_id, decision}], sig}`.
  - Proposals are unique and ascending, 1 to 50 of them.
  - `decision < num_options`, or `ROUTE_ABSTAIN = 0xFFFF_FFFF` (owner question 3).
- **Digest.**
  `BLAKE2b-256("SVOTE_DELEGATE_ROUTE_BATCH_SIGHASH_V1" ‖ pad32(round) ‖ u32pad32(d) ‖ pad32(dk) ‖ u32pad32(n) ‖ Σ (u32pad32(p) ‖ u32pad32(decision)))`, signed with Ed25519.
- **Idempotency.** Signing is deterministic, so a resubmission produces identical bytes. The chain must include the tag in its canonical-encoding set.
- **Preflight** (`plan_routes`):
  - the round is ACTIVE and `now < vote_end_time` (authenticated timing);
  - each proposal is in the authenticated roster;
  - the chain's current DK for `d` equals the key, with status Active;
  - fetch existing routes: an equal decision becomes `Confirmed`, a different one becomes `Conflict` (routes are one-shot).
- **Submission.** POST `/shielded-vote/v1/delegate-route-batch`, then poll `/tx/{hash}`. Statuses come from the chain routes query.
- No helpers and no proofs are involved. Mobile foreground time is seconds.
- **Informed delegation.** Routes are public, so the delegator UI may show a delegate's existing routes before the user delegates.

### 4.5 Rotation, retirement and recovery

- **Rotation.** `MsgRotateDelegateKey {d, new_dk, attestations(new), old_sig?, new_pop_sig}`.
  - Continuity: the old key co-signs `"SVOTE_DELEGATE_ROTATE_V1" ‖ d ‖ old ‖ new`.
  - Recovery: no old key; the chain applies the cooling-off rule.
  - The pool index never changes.
- **Retirement.** `MsgRetireDelegate {d, sig}`. It affects future rounds only.
- **Recovery.**
  - Software: derive `key_index` 0 to 19 and query `/delegates/by-key` for each. The newest Active match is current.
  - Hardware: decode the backup. If the key is lost, use recovery rotation.
  - Then import on-chain routes for active rounds into `delegate_routes`.
- **Own voting.** A delegate votes its own notes with a normal per-round hotkey. Delegate tables are separate from round tables, so no `wallet_id` mixing occurs. Self-delegation is allowed; the UI warns.

---

## 5. Registry client (`proxy::registry`)

### 5.1 Sources and trust

1. **Chain registry** (K4) is authoritative for `delegate_index ↔ dk_pubkey ↔ status ↔ key history`. It carries attestations.
2. **Directory snapshot** (I2) is display data only: handle, display name, bio, `pfp_sha256`, featured tier and rank, flags, age.
3. **Verifier attestations.** The client checks them against pinned `verifier_keys`. This binds DK to `provider_user_id` independently of the directory.

### 5.2 Snapshot

- Format: `DelegateDirectorySnapshotV1 {format_version, network, vote_chain_id, sequence, issued_at, expires_at (≤ issued_at + 24h), registry_height, entries[…], signatures[{key_id, alg, sig}]}`.
- Signature: over `"zcash-shielded-vote:delegate-directory:v1\0" ‖ sha256(canonical JSON without signatures)`.
- Verify against **`directory_keys`**, `directory_threshold`-of-N, from the new static config. These keys are purpose-separated from `trusted_keys`, which can sign rounds (config/mod.rs:1092-1143).
- Reject:
  - a wrong network or chain;
  - an expired snapshot (X-derived fields are not displayed);
  - `sequence` lower than the cached one (rollback);
  - threshold failure.
- Download the whole snapshot (no per-query server search) through the host transport (Tor-aware).

### 5.3 Cross-check and mapping

- `resolve(d)` and `verify_selected(entries)` fetch `/delegates/{d}` and require snapshot DK = chain DK and status Active.
- The check runs **before preview, at commit, and immediately before proving** (§1.5).
- A mismatch quarantines the entry (`verified_against_chain = false`, hidden from search) and raises `ProxyDelegateChanged`.
- Allocations and favorites pin `(d, dk_pubkey)`, never a handle.

### 5.4 Cache and X compliance

- `delegate_directory_cache` holds only the latest valid snapshot.
- Every successful refresh replaces it and purges every `delegate_pfp_cache` row the new snapshot does not reference.
- Expired snapshots keep index and fingerprint data but drop X-derived fields from views.
- `display_handle` in `proxy_allocation_entries` is the user's own record of intent and is retained.

### 5.5 Profile pictures

- `fetch_pfp(sha256)` uses the host transport against the directory host.
- Size cap 256 KiB. The bytes must hash to `pfp_sha256`, and the MIME type must be PNG or WebP with dimensions ≤ 512.
- The bytes are returned for `Image.memory`. The library never uses a URL loader.

### 5.6 Lookalike helpers

These live in Rust so all five platforms share tested logic.

- **`handle_skeleton`.** X handles are ASCII `[A-Za-z0-9_]`. The skeleton:
  - lowercases;
  - maps `0→o`, `1|i→l`, `5→s`, `8→b`;
  - maps `rn→m`, `vv→w`, `cl→d`;
  - strips `_`.
- **`display_name_skeleton`.** The UTS #39 skeleton. This adds a new dependency (`unicode-security`) or a vendored confusables table.
- **`lookalike_warnings(candidate, snapshot)`.** Flags entries whose skeleton equals the candidate's, or whose display name or pfp hash matches, when they are featured or older.

### 5.7 Search and order

- `search(query, seed)` ranks:
  1. exact handle;
  2. handle prefix;
  3. display name;
  4. fingerprint;
  5. index.
- Each result carries its lookalike warnings.
- Default browse order: the featured tier by rank, then the rest weighted-random with a per-session seed.

---

## 6. Config and capability gating

- **`WalletCapabilities`** gains `#[serde(default)] proxy_delegation: Vec<String>`, defaulting to `["v1"]`. `vote_protocol` stays `[v0, v1]` (config/mod.rs:88-106).
- **Dynamic config.** These are optional fields; old parsers ignore them because there is no `deny_unknown_fields`.
  - **Top-level `proxy_delegation`.**
    - Shape: `{ "version": "v1", "directory_urls": [..], "feed_urls": [..], "verifier_urls": [..] }`.
    - An unsupported version disables the feature, emitting `ConfigCondition{kind: ProxyDelegationSupported, status: false}`. It **never** rejects the config.
  - **Per-round `rounds[id].proxy`.**
    - Shape: `{auth_version: 1, enabled, max_delegates, min_ballots_per_delegate, signatures}`.
    - The payload is signed by `trusted_keys` under a new domain: `RoundProxyAuthPayloadV1 = "zcash-shielded-vote:round-proxy:v1" ‖ round_id ‖ ea_pk ‖ enabled_u8 ‖ max_delegates_le32 ‖ min_ballots_le64 ‖ proxy_protocol_le32`.
    - The new domain means a round-auth v2 signature can never validate as a proxy payload, and the reverse.
- **Resolved config and switch decision.**
  - `ResolvedVotingConfig` gains `proxy_delegation: Option<ResolvedProxyDelegationConfig>`.
  - `ResolvedVotingConfigSummary` gains `proxy_fingerprint`. A change maps to `SameChainServiceUpdate`, **not** `ProtocolChanged`, which would discard cached voting state (config/mod.rs:375-382).
  - The proxy version is deliberately kept out of `SupportedVersions`, because that struct feeds `ProtocolChanged`.
- **Static config.** Publish a v2 file **at a new URL** with `directory_keys`, `directory_threshold` and `verifier_keys`, and give new Vizor a new sha256 pin. Never edit a pinned file in place, because Zodl and old Vizor pin the old hash.
- **Chain checks.**
  - Fetch `GET /protocol-capabilities` once per session. New fields are parsed with `serde(default)` (K6).
  - Require `proxy_delegation && proxy_protocol_version ∈ {1} && zkp4_vk_fingerprint == local VK && wire tags present`.
  - The round query must report `proxy_delegation_enabled` (K7).
- **`proxy::availability(config, round, caps) -> ProxyAvailability`.**
  - `Available{max_delegates, min_ballots_per_delegate, max_proxy_actions_per_batch, max_batch_json_bytes}`, or
  - `Unavailable{reason}`, where reason is one of: `ConfigDoesNotAdvertise`, `ConfigVersionUnsupported`, `RoundNotEnabled`, `RoundExtensionUnauthenticated`, `ChainLacksCapability`, `ChainVkMismatch`, `DirectoryUnavailable`, `WalletAlreadyVoted`.
- **Dormant flag.** The SDK ships with `PROXY_DELEGATION_ENABLED: bool = false`, the same precedent as `ATOMIC_VOTE_BATCHES_ENABLED` (zv:src/lib.rs). It is flipped after the chain upgrade.
- **Zodl and old Vizor.** No existing message, endpoint, config field or schema they use changes. Rounds with proxy enabled are normal rounds for them. Tallies include pools on the chain side.

---

## 7. Privacy notes

- **Timing.**
  - DCs ride in the bundle's combined ZKP1 tx by default, so proxy delegation adds no tx and no timing event.
  - Non-combined proxy batches are one tx per bundle, as votes are today. Cross-bundle timing correlation is unchanged from today's multi-bundle votes.
  - A late DC uses single-share layout, which exposes its whole amount to a full EA-key holder. This matches votes (vc:src/vote_proof/builder.rs:812-818).
- **Chain observers learn**:
  - the number of DC actions per tx;
  - the remainder-VAN leaf, and the cast proposal ids when present;
  - reveal events `(proposal 0, d, ciphertext)` at randomized times. These are unlinkable to the tx except through the immediate share (owner question 2).
  - They never learn `d` from the tx, or `w`.
- **Each helper learns**, from the payload: the DC leaf position (hence the tx and bundle), `d`, the share ciphertext, `share_comms`, one blind and `submit_at`. It can recompute the DC and link tx to `d`.
  - It cannot learn `w` without the EA key. A helper colluding with an EA threshold learns per-share amounts, and so per-delegation amounts and per-pool totals.
  - This is the same class of leak as votes today, where helpers learn `(proposal, decision)` per tx.
- **Per-delegate counts.** About 16 reveals per standard DC per pool, so the number of delegations per delegate is approximately public. Totals stay hidden. Vizor should not display counts.
- **Recovery hint.** It is AEAD under a per-action key derived from an account-only key, has constant size, and is indistinguishable from random. Viewing-key (UFVK) holders can see the account's delegations and amounts, which is consistent with viewing-key semantics.
- **Directory.** Download the whole snapshot, never per-query search. Fetch pfps by content hash through the host's Tor-aware transport. The client never contacts X.
- **Identity is never pinned to handles.** Allocations pin `(d, dk)`.

---

## 8. FRB / flat DTO surface (`zcash_voting::wire`, struct-only)

All DTOs use u32/u64, String, `Vec<u8>`, `Option`, and fieldless enums.

**Delegator**

| DTO | Fields |
|---|---|
| `ProxyRemainderView` | `{KeepForSelf, DelegateEverything}` |
| `ProxyAllocationEntryInput` | `{delegate_index: u32, dk_pubkey: Vec<u8>, basis_points: u32}` |
| `ProxyAllocationRequestView` | `{round_id, entries, remainder}` |
| `ProxyPlanView` | `{round_id, planner_version, total_ballots: u64, keep_ballots: u64, delegates: Vec<ProxyPlannedDelegateView>, slots: Vec<ProxyPlannedSlotView>, transactions: Vec<ProxyPlannedTxView>, excluded_bundles: Vec<ProxyExcludedBundleView>, warnings: Vec<ProxyPlanWarningView>, plan_digest: Vec<u8>}` |
| `ProxyPlannedDelegateView` | `{delegate_index, dk_fingerprint, display_handle: Option<String>, basis_points, ballots: u64, zatoshi: u64, adjusted: bool}` |
| `ProxyPlannedSlotView` | `{bundle_index, slot_index, delegate_index, ballots}` |
| `ProxyPlannedTxView` | `{bundle_index, combined_with_authorization: bool, proxy_actions: u32, cast_actions: u32, deferred_cast_actions: u32}` |
| `ProxyExcludedBundleView` | `{bundle_index, ballots, reason: ProxyBundleExclusionView{AlreadyVoted, VoteInFlight, DelegationTerminal}}` |
| `ProxyPlanWarningView` | `{kind: {MinimumApplied, DelegateKeyRecentlyChanged, LookalikeHandle, LastMomentLayout, IntentsWithoutWeight}, delegate_index: Option<u32>, message}` |
| `ProxyRoundStatusView` | `{allocation_state: {None, Committed, Locked}, remainder: Option<ProxyRemainderView>, total_ballots, delegated_ballots, keep_ballots, remaining_self_ballots, intents_without_weight: bool, slots: Vec<ProxySlotStatusView>, blocked: Vec<ProxyBlockView>}` |
| `ProxySlotStatusView` | `{bundle_index, slot_index, delegate_index, ballots, phase: {Planned, Proved, Submitted, Confirmed, SharesDelivered, SharesConfirmed, Rejected}, tx_hash: Option<String>, commitment_tree_position: Option<u64>, shares_total, shares_delivered, shares_confirmed, submission_diagnostic: Option<SubmissionDiagnosticView>}` |
| `ProxyOutcomeView` | `{round_id, vote_end_time: Option<u64>, refreshed_at: u64, delegates: Vec<DelegateOutcomeView>}` |
| `DelegateOutcomeView` | `{delegate_index, dk_fingerprint, handle_at_delegation: Option<String>, ballots: u64, inclusion: {NotSubmitted, Pending, Included, Failed}, tx_hash: Option<String>, included_height: Option<u64>, shares_total, shares_revealed, proposals: Vec<DelegateProposalOutcomeView>}` |
| `DelegateProposalOutcomeView` | `{proposal_id, state: {Voted, Abstained, NotYetVoted, DidNotVote}, decision: Option<u32>, routed_height: Option<u64>}` |
| `ProxyRecoveryReportView` | `{round_id, scanned_actions: u64, recovered_slots: u32, key_used: {Account, Hotkey}, remainder_ballots: Option<u64>}` |
| `ProxyAvailabilityView` | `{available: bool, reason: Option<ProxyUnavailableReasonView>, max_delegates, min_ballots_per_delegate: u64}` |

**Directory**

| DTO | Fields |
|---|---|
| `DelegateDirectoryEntryView` | `{delegate_index, dk_pubkey: Vec<u8>, dk_bech32, dk_fingerprint, provider: {X, Github, Domain}, provider_user_id, handle, display_name, bio, pfp_sha256: Option<Vec<u8>>, featured: bool, featured_rank: Option<u32>, status: {Active, ProofRemoved, Suspended, KeyChangePending, Retired}, registered_height: u64, key_changed_height: Option<u64>, registration_age_days: u32, verified_against_chain: bool}` |
| `DelegateSearchResultView` | `{entry, match_kind: {ExactHandle, HandlePrefix, DisplayName, Fingerprint, Index}, lookalike_warnings: Vec<LookalikeWarningView>}` |
| `LookalikeWarningView` | `{other_delegate_index, other_handle, reason: {HandleSkeleton, DisplayNameSkeleton, SameDisplayName, SamePfp}}` |
| `DelegateDirectoryStatusView` | `{sequence, issued_at, expires_at, entry_count, stale: bool}` |

**Delegate**

| DTO | Fields |
|---|---|
| `DelegateKeyInfoView` | `{network, origin: {Zip32Registered, Random}, key_index: Option<u32>, dk_pubkey, dk_bech32, dk_fingerprint, delegate_index: Option<u32>, status}` |
| `DelegateProofPostView` | `{text, machine_line}` |
| `DelegateRegistrationStatusView` | `{state: {Draft, AwaitingVerifier, Attested, Submitted, Registered, Rejected}, delegate_index: Option<u32>, diagnostic: Option<String>}` |
| `DelegateRouteInput` | `{proposal_id, decision: Option<u32>}` (None means explicit abstain) |
| `DelegateRouteView` | `{round_id, delegate_index, proposal_id, decision: Option<u32>, state: {Signed, Submitted, Confirmed, Rejected, Conflict}, tx_hash: Option<String>, height: Option<u64>}` |

**Extended**

- `NextStepKind` (+4 values).
- `NextStepView.slot_index`.
- `RoundPlanActionKind::ProxyDelegate`.
- `RoundPlanView`: `proxy`, `needs_proxy_signing`, `has_in_flight_proxy`, `proxy_bundles_needing_work`, all `serde(default)`.
- `WalletCapabilities.proxy_delegation`.
- `ConfigConditionKind::ProxyDelegationSupported`.
- `ResolvedProxyDelegationConfig {version, directory_urls, feed_urls, verifier_urls, rounds: Vec<AuthenticatedRoundProxy{round_id, enabled, max_delegates, min_ballots_per_delegate}>}`.

**Rust entry points Vizor's session wraps**

- `RoundBinding.proxy_recovery_key: Option<Zeroizing<[u8; 32]>>`.
- `proxy::derive_proxy_recovery_key(ufvk, network, round_id)` and `..._from_hotkey`.
- `VotingDb::{preview_proxy_allocation, commit_proxy_allocation(plan), clear_proxy_allocation, proxy_round_status}`.
- `proxy::{availability, outcomes, recover}`.
- `proxy::registry::{DirectoryClient::refresh, search, resolve, verify_selected, fetch_pfp, lookalike_warnings, handle_skeleton}`.
- `proxy::delegate::{derive_delegate_key, generate_random_delegate_key, encode/decode_delegate_key_backup, DelegateKey::{public, proof_post, sign_registration_statement, build_chain_registration, sign_route_batch, sign_rotation, sign_retirement}, DelegateClient::{submit_registration, registration_status, plan_routes, submit_routes, route_status, recover}}`.

---

## 9. Test plan

1. **Planner property tests** (proptest).
   - Inputs: 1 to 12 bundles with `W` in `1..2^30`; 1 to 10 delegates; random bps; both remainder policies.
   - Properties:
     - conservation;
     - per-bundle capacity;
     - every slot ≥ 1;
     - every delegate ≥ 1;
     - `D ≤ #slots ≤ D + B − 1`;
     - `B = 1 ⇒ #slots = D`;
     - at most 1 slot per (bundle, delegate);
     - at most 10 slots per bundle;
     - non-adjusted delegates within 1 ballot of their quota (u128 reference Hamilton);
     - determinism across runs and HashMap seeds;
     - a split-free packing is found whenever brute force (small B) finds one;
     - `DelegateEverything ⇒ keep = 0` and no casts planned;
     - the `plan_digest` changes on any slot change.
   - Plus golden vectors for §3.2's worked examples.
2. **Deterministic recovery.**
   - Build a batch, persist it, delete the DB, then run R1 (hotkey + ARK + mock feed). Require byte-identical slots, DC, `shares_hash`, share nullifiers, helper payload JSON and remainder VAN.
   - Run R2 (ARK only): payloads and nullifiers must be identical.
   - Tampered hint: AEAD fails and the action is ignored.
   - Valid hint with a lying feed DC: rejected.
   - Re-running recovery is idempotent.
   - `ARK_hk` fallback works.
3. **Nonce hygiene.**
   - Changing `d`, `w` or `layout` for the same VAN changes every `r_i` and blind.
   - DC PRF domains are disjoint from vote PRF domains.
   - A retired-batch rebuild reproduces the identical DC.
4. **Frozen cross-language vectors** shared with Go (chain) and the verifier:
   - proxy batch sighash (both variants);
   - route-batch digest;
   - register PoP digest;
   - rotate and retire digests;
   - `RoundProxyAuthPayloadV1` bytes;
   - snapshot signing bytes.
   - Client-only vectors: ARK, `hint_key`, `dc_seed`, hint ciphertext, DK registered-derivation vectors (pending `ZIP_N`), fingerprint, bech32m.
5. **Planner and classify tests.**
   - A proxy-only round with zero intents gives `ProxyDelegate`, no `CastVote`, and no open-ballot block.
   - A mixed round orders DCs before casts.
   - A non-terminal ballot gives DCs now and casts later.
   - An already-voted bundle is excluded.
   - The combined ZKP1 path.
   - The import barrier.
   - The lock after reservation; clear refused.
   - Intents without weight are flagged.
   - The immediate-share designation in proxy-only rounds.
   - Exhaustive `NextStep` predicate tests.
6. **Packer.**
   - A 700 KB budget defers tail casts into a follow-up batch.
   - DCs are never split across submissions.
   - Exact JSON-length measurement.
7. **Migrations.**
   - v24 → v25 preserves every row.
   - Fresh `001_init` and the migrated schema fingerprint identically.
   - `chain_submissions` rebuild with the new kinds.
   - `RESET_SQL` includes the new tables.
   - A v25 DB is refused by v24 code (downgrade).
   - Account deletion removes the delegate tables.
8. **Chain lifecycle.** For both new kinds: submit, track, event parse, exact-tree recovery (`[final_van, DCs…, VCs…]` layout), terminal rejection and retirement streak, hashless dispatch.
9. **Share delivery.** DC payload has `proposal_id 0` and `decision = d`. 16 shares, or 1 in single-share mode. Nullifiers match ZKP3. Redelivery uses the `ShareKey::Proxy` variant.
10. **Config.**
    - An old-capability `WalletCapabilities` parses configs with proxy fields unchanged (Zodl simulation).
    - An unsupported proxy version disables the feature without rejecting the config.
    - A proxy-fingerprint change gives `SameChainServiceUpdate`.
    - Round proxy signature domain separation holds.
    - A VK fingerprint mismatch gives Unavailable.
11. **Registry.**
    - Snapshot threshold, rollback, expiry and network checks.
    - A chain cross-check mismatch quarantines the entry.
    - Attestation verification.
    - pfp hash, size and MIME checks.
    - Purge on refresh.
    - Skeleton tables: `O/0`, `l/I/1`, `rn/m`, `vv/w`, `_`, Unicode confusables.
    - Search ranking and seeded order.
12. **Delegate.**
    - Backup round-trip with checksum failure cases.
    - Route preflight: Confirmed and Conflict detection, vote end, roster and decision bounds, abstain sentinel.
    - Deterministic resubmission.
    - Recovery scan over `key_index`.
13. **Integration** on a regtest chain with helpers (`zvroot:stage-bench`):
    - DC, then reveals to `(0, d)`, then route, then a tally that includes the pool;
    - an unrouted pool abstains;
    - a delegator outcome view end to end;
    - reinstall R1 and R2 against the live feed;
    - Zodl-pinned SDK (`=5.1.1-rc.3`) voting in the same round, unaffected.
14. **Benchmarks.** ZKP4 prove time on desktop and on reference iOS/Android devices. Foreground time for ZKP1 + 10 DCs + N casts versus the 15-minute auto-lock.

---

## 10. Rollout

- Merge everything behind `PROXY_DELEGATION_ENABLED = false`, with the schema at v25 regardless.
- After the chain upgrade, which happens between rounds, and once the VK fingerprint matches, flip the const in a release.
- Enable per round via the signed round proxy extension.
- Pilot with `[TEST]` rounds in Vizor.
