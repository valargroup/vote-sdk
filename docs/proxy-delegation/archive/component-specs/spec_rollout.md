# Proxy delegation via public delegate pools (B2): specs, threat model, and cross-repo rollout plan

Component: SPECS, THREAT MODEL, AND CROSS-REPO ROLLOUT PLAN. Date: 2026-10-05.

**Legend.** [code] means I verified it in source at the cited file:line. [doc] means a spec, book page or repo doc says it. [inference] means it is my own reasoning.

**Path keys.**
- `V` = `/Users/czstudio/Documents/vote-sdk/.claude/worktrees/vote-delegation-planning-29b451` (vote-sdk main)
- `S` = `/private/tmp/claude-501/-Users-czstudio-Documents-vote-sdk--claude-worktrees-vote-delegation-planning-29b451/0cff7cc1-5f3f-4de1-be83-322a1fdf7727/scratchpad/src`
- `VC` = `$S/voting-circuits` (origin/main, v0.12.2)
- `VCB` = branch `origin/roman/private-vote-implementation` in `/Users/czstudio/Documents/voting-circuits` (read with `git show` only)
- `ZV` = `$S/zcash_voting/zcash_voting`; `VZ` = `$S/vizor-wallet`; `BOOK` = `$S/shielded-vote-book`
- `ZIP-A` = `$S/zips/draft-valargroup-shielded-voting__adam_voting-protocol-client-delay.md`; `ZIP-G` = `$S/zips/draft-valargroup-shielded-voting__greg_shielded-voting-fixes.md`
- `WAPI`, `SUB`, `SETUP`, `KS`, `CER` = the wallet-API, submission-server, setup, Keystone and EA-ceremony drafts in `$S/zips/`
- `ZI` = `/Users/czstudio/Documents/zodl-ios` (local checkout a893c64a, 2026-05-13; it may be older than production)
- `CFG` = `/Users/czstudio/Documents/token-holder-voting-config` (origin/main); `DLS` = `/Users/czstudio/Documents/vizor-deeplink-server`; `LAB` = `/Users/czstudio/Documents/voting-load-lab`

---

## 0. Verdict on the B2 baseline

**No fatal flaw.** The core safety argument checks out against code:

- **ZKP2 cannot produce a proposal-0 commitment.** The authority-decrement non-zero gate rejects `proposal_id = 0` [code] `VC/src/vote_proof/circuit.rs:131-150`; [doc] `ZIP-A:1482-1501`. The private-vote-choice (V2) compact ZKP2 keeps the authority decrement [doc] `VC/docs/design.md` "ZKP 2: compact cast proof", and V2 VCs use a different domain, `DOMAIN_VC_V2` (`design.md:300`).
- **ZKP3 copies `proposal_id` and `vote_decision` from the instance and only hashes them into the VC preimage** [code] `VC/src/share_reveal/circuit.rs:528-550, 787-808`. The chain's `MsgRevealShare.vote_decision` is a `uint32` [code] `V/proto/svote/v1/tx.proto:120-128`.
  - A DC with `(0, d)` therefore reveals through the unchanged ZKP3.
  - Poseidon preimage binding prevents a VC from being revealed as a DC, and a DC from being revealed as a VC.
- **Pools are already structurally excluded from decryption.**
  - Partial-decryption injection iterates `round.Proposals` [code] `V/app/prepare_proposal_partial_decrypt.go:153-154`.
  - Completeness also iterates `round.Proposals` [code] `V/x/vote/keeper/keeper_tally.go:307-341`.
  - `ValidateEntryBounds` rejects `proposal_id < 1` for both partial-decryption entries and `MsgSubmitTally` entries [code] `V/x/vote/keeper/keeper_voting.go:291-304`, `keeper_tally.go:408`, `msg_server_tally_decrypt.go` (the SubmitTally loop).
  - This must become a named, tested invariant, because one relaxed bound would publicly decrypt every pool.

**Required corrections (details in `baseline_challenges`):**

1. **HIGH, Zodl compatibility.** Never gate proxy delegation through `supported_versions.vote_protocol` or `tally`.
   - Zodl reads the same dynamic config and hard-fails on an unknown version [code] `ZI/docs/voting-service-discovery.md` ("Version handling"); [doc] `WAPI:719-722`.
   - Advertise the feature through a new optional `extensions` key, chain round parameters and `ProtocolCapabilities` instead.
2. **HIGH, coupling with private vote choice.** The V2 branch rewrites ZKP3 in place to `DOMAIN_VC_V2` vectors and deletes the scalar circuit [code] `VCB:src/share_reveal/circuit.rs:1-33`; `git diff --stat origin/main...origin/roman/private-vote-implementation` shows `src/shares_hash.rs` at -741 lines.
   - B2 needs the scalar ZKP3 and the V1 `shares_hash` forever.
   - V2 must keep them as a separately named circuit with a pinned verifying key (VK).
3. **HIGH, claim correctness.** Owner decision 4 ("per-delegate totals stay hidden") cannot be unconditional.
   - Exact per-option tallies plus public routes disclose `Pool[d]` exactly when `d` is the sole contributor to an option, and approximately by differencing.
   - A coalition of at least `t` EA key holders can decrypt pools.
   - Per-delegate delegation counts are public (16 tagged reveals per DC).
   - The specs and UI must say this precisely.
4. **MEDIUM-HIGH, DoS.** Allowing `1 <= w` lets one VAN mint up to `W` fee-less DCs, each costing 16 helper proofs and one tree leaf.
   - Fix: an in-circuit minimum `w >= min_dc_ballots`, taken from a round parameter.
   - Also add a per-block cap on proof-carrying actions and the tree-capacity guard (`AppendCommitment` has none [code] `V/x/vote/keeper/keeper_voting.go:99-117`).
5. **MEDIUM, transaction size.** ZKP1 plus 10 DCs plus 50 casts is about 61 proofs of about 11 KB each, roughly 680 KB of raw proof bytes alone. That exceeds the 1 MiB REST body limit once base64/JSON-encoded [code] `V/api/handler.go:780,814`; [inference].
   - Fix: a byte budget advertised by the chain, with the client splitting the batch.
6. **MEDIUM, observability hygiene.** Do not reuse the `reveal_share` event or `ShareCount` for proposal 0 (`V/x/vote/keeper/msg_server_tally_decrypt.go:54-64`). Use a distinct event type so naive consumers and old clients do not see a phantom "proposal 0".

---

## 1. Spec work

### 1.1 Which ZIP draft becomes the base

**Decision: ZIP-A (zcash/zips PR 1200) is the base protocol ZIP.**
- It matches code on y-coordinate share commitments, the vote and delegation sighash structure, and per-vote secret derivation, and it is MUST-heavy [doc] specs dossier §1.1.
- Port four ZIP-G-only items into it:
  - the "Voting Round Identifier" section (`ZIP-G:312-354`);
  - the "Election Authority Key" section (`ZIP-G:1195-1216`);
  - the third-party hotkey requirement (`ZIP-G:213-214`), reworded so it is satisfied by proxy delegation rather than by ZKP1-to-a-stranger;
  - the hardware-wallet future-work text (`ZIP-G:1313-1323, 1554-1565`).

**Proxy delegation does not go inside ZIP-A.** It becomes a companion ZIP, and identity becomes a third ZIP:
- **ZIP-PD:** "Shielded Voting: Proxy Delegation via Public Delegate Pools" (normative; optional extension).
- **ZIP-DR:** "Shielded Voting: Delegate Registry and Identity Proofs" (normative for chain registry messages; the identity-proof and snapshot formats are normative for wallets claiming the extension).
- **Rationale.** Wallets such as Zodl can claim base conformance without implementing the extension. ZIP-A review is not reopened for a feature with different trust assumptions. The extension is versioned on its own (`proxy_delegation_v1`).
- ZIP-A receives only small "hooks" (§1.3).

### 1.2 Drift to reconcile first ("ZIP-A errata" PR, before any new text)

| # | Drift | Spec text | Code truth | Fix |
|---|---|---|---|---|
| 1 | Authority width | 16 bits / 15 proposals (`ZIP-A:321-325, 359, 703-707, 794`; `BOOK/data-types.md:20`) | 51 bits, `MAX_PROPOSALS = 50`, sentinel bit 0 [code] `VC/src/params.rs:31-45`, `V/x/vote/types/keys.go:44-48` | Parameterize as `N_P = 50`, `MAX_PROPOSAL_AUTHORITY = 2^51 - 1`. Recompute "Why VCT Depth 24" (`ZIP-A:1557-1572`). |
| 2 | Delegation sighash | Client-provided (`ZIP-A:740-749`; `WAPI:538-547`) | Recomputed from on-chain `tx1_effects` [code] `V/x/vote/ante/validate.go:259-272`; `V/ffi/tx1/effects.go:38-62` | Specify the `tx1_effects` format (821 bytes, 1 action) and the on-chain recomputation. |
| 3 | Signed-note value | 0 (`ZIP-A:631, 664-665`; `KS:38-39`) | Exactly 1 zatoshi [code] `VC/src/delegation/README.md:18-19,177-178` | Change to 1 zatoshi. |
| 4 | Batch messages | Absent; `WAPI:191-193` says "cannot be voted in parallel" | `MsgCastVoteBatch`, `MsgDelegateAndCastVoteBatch`, single-leaf-root chaining [code] `V/proto/svote/v1/tx.proto:97-117`; `V/x/vote/types/sighash.go:12-20` | New ZIP-A section "Atomic Action Batches" covering synthetic anchors, ordering and digest domains. ZIP-PD generalizes it. |
| 5 | Config | Single-round `config_version:1` (`WAPI:209-293`; `CFG/README.md`) | Signed static (hash-pinned) plus dynamic config with per-round `RoundEntry{auth_version, ea_pk, signatures}` [code] `ZV/src/config/mod.rs:852-898`; `CFG:prod/v2-static-voting-config.json`, `prod/dynamic-voting-config.json` | Rewrite the WAPI config section. Define `extensions` (§1.4.10). |
| 6 | Share distribution | MUST (`ZIP-A:1193-1196`) vs MAY (`ZIP-G:1152-1155`); equal split (`SUB:534-541`) vs base-10 [code] `VC/src/vote_proof/builder.rs:39-112` | Base-10 greedy plus PRF remainder | Adopt the code. |
| 7 | `submit_at = 0` | Immediate (`SUB:264-265`) vs "last possible moment" (`WAPI:635`) | Client: buffer missing or `single_share` means immediate [code] `ZV/src/share_policy/submission_schedule.rs:8-34` | Adopt "immediate". |
| 8 | Book field names and `vsk.nk == nk` | `total_note_value`, `allowed_proposals`; "same field element" (`BOOK/data-types.md:36`) | `num_ballots`, `proposal_authority`; separate hotkeys exist | Fix the book (§1.6). |
| 9 | Tally proof | Single-EA Chaum-Pedersen (`BOOK/overview/design-principles.md:33`) | Threshold with DLEQ-checked partials [code] `msg_server_tally_decrypt.go` (SubmitPartialDecryption) | Fix the book. |
| 10 | Terminology | "VAN … consumed (to cast a vote or delegate)" (`ZIP-A:86-90, 329-330`); SETUP:141-142 | Today only ZKP2 consumes VANs | Rename (§1.3). |

### 1.3 Terminology (spec and user-facing)

| Spec term | Definition | Legacy / code name | User-facing (Vizor) |
|---|---|---|---|
| **Registration** | ZKP1 / `MsgDelegateVote`: binding note weight to a VAN under the holder's own voting hotkey | "delegation", "Delegation Phase", `MsgDelegateVote` (wire names unchanged) | "Voting authorization". Change today's copy: `VZ/lib/src/features/voting/screens/voting_status_screen.dart:1289,1345` ("authorizes this voting delegation") and `VZ/lib/src/providers/voting/voting_session_provider.dart:1303` ("Delegate this round before casting votes"). |
| **Proxy delegation** | Moving `w` ballots of a full-authority VAN into delegate `d`'s pool via a DC | new | "Delegate" (verb), "Delegate your vote" |
| **Delegator** | Holder who proxy-delegates | | "You" |
| **Delegate** | Party registered in the on-chain delegate registry with a delegate key | | "Delegate", shown with @handle and a key fingerprint |
| **Delegate key (DK)** | Long-lived signing key (Ed25519 recommended) bound to an off-chain identity proof | | "Delegate key" plus a 16-character fingerprint |
| **Delegate index** | `u32` assigned append-only by the chain | | never shown |
| **Delegation commitment (DC)** | VCT leaf `Poseidon(DOMAIN_VC, round, shares_hash, 0, d)` | | n/a |
| **Delegate pool** `Pool[d]` | Per-round encrypted accumulator `TallyKey(round, 0, d)` | | "Delegated voting power" (never shown as a number) |
| **Route** | A delegate's one-shot public decision on one proposal, applied to its pool at tally | `MsgDelegateRoute` | "@alice voted Yes" |
| **ZKP4 / Delegation Commitment Proof** | ZKP2-sibling circuit that spends a VAN and outputs a successor VAN plus a DC | | n/a |
| **Share submission** | A helper revealing shares | "Share Delegation" (`WAPI:603`), "server-delegated shares" (`BOOK/delegation/server-delegated-shares.md`) | n/a |

Required user-facing statements:
- **Finality:** "Delegation is final for this poll. You can't change it or take it back."
- **Abstention:** "If @alice doesn't vote on a proposal, the ZEC you delegated doesn't count on that proposal."
- **Publicity:** "Delegates' votes are public. Who delegated and how much stays private."

### 1.4 ZIP-PD normative content (outline with the exact rules)

#### 1.4.1 Hooks added to ZIP-A

- **Reserved proposal 0.** `proposal_id = 0` is reserved for delegation commitments defined in [ZIP-PD]. A Vote Proof MUST reject `proposal_id = 0` (existing gate). A vote chain MUST NOT accept a proposal with id 0.
- **VCT leaf kinds.** VAN (`DOMAIN_VAN`), VC (`DOMAIN_VC`, `proposal_id ∈ [1, N_P]`) and DC (`DOMAIN_VC`, `proposal_id = 0`). Update the insertion-order paragraph at `ZIP-A:444-445`.
- **VAN consumption.** Any proof that consumes a VAN MUST publish `Poseidon4(vsk.nk, "vote authority spend", voting_round_id, van)` into the VAN nullifier set. A VAN MAY be consumed by a Vote Proof or by a Delegation Commitment Proof (`ZIP-A:338-345` lifecycle MUSTs).
- Replace the "future partial delegation" paragraphs (`ZIP-A:342-345, 1613-1618`) with a pointer to ZIP-PD. Update the rationale "Why a Send-Based VAN Model" (`ZIP-A:1506-1543`): B2 does not transfer VANs.
- **Correct "Why Reusing VAN Address and Randomness"** (`ZIP-A:1470-1480`). The fields are observable to anyone holding a VAN opening (custody handoff today).
- **Requirements** (`ZIP-A:200-215`). "Only aggregate totals per (proposal, decision) are recoverable; aggregates include routed delegate pools."
- **Non-requirements** (`ZIP-A:217-231`). Add: "Receipt-freeness and coercion resistance are not provided."

#### 1.4.2 Round parameters (SETUP amendment)

`ProxyDelegationParams { bool enabled; uint32 min_dc_ballots; uint32 max_dc_actions_per_tx; }` is stored in `VoteRound` and set by `MsgCreateVotingSession` (coordinator action).
- These parameters MUST NOT enter the `voting_round_id` preimage. Changing the preimage would change round IDs for Zodl and every client [inference]; [doc] `SETUP:328-331`.
- Authentication comes from the chain (coordinator-created) plus the signed config extension (§1.4.10).
- Defaults: `enabled = false` for rounds created before activation. Recommended values `min_dc_ballots = 8` (1 ZEC; owner question) and `max_dc_actions_per_tx = 10`.

#### 1.4.3 Delegation Commitment (DC)

`dc = Poseidon5(DOMAIN_VC, voting_round_id, shares_hash, 0, delegate_index)`

- `shares_hash` is exactly the V1 construction: `Poseidon16(share_comm_0..15)`, with `share_comm_i = Poseidon5(blind_i, C1_i.x, C2_i.x, C1_i.y, C2_i.y)` [code] `VC/src/share_reveal/circuit.rs:11-24`.
- `delegate_index ∈ [0, 2^32)`.
- Rationale for reusing `DOMAIN_VC` with proposal 0 instead of a new tag: it keeps ZKP3 byte-identical. Separation comes from the proposal-0 reservation, and in V2 rounds additionally from `DOMAIN_VC_V2`.

#### 1.4.4 ZKP4: Delegation Commitment Proof

The circuits component owns the layout. This is the normative statement.

**Public inputs.** There are 11; the offsets mirror ZKP2 [code] `VC/src/vote_proof/circuit.rs:157-193`:

| Offset | Input |
|---|---|
| 0 | `van_nullifier` |
| 1 | `r_vpk_x` |
| 2 | `r_vpk_y` |
| 3 | `vote_authority_note_new` |
| 4 | `delegation_commitment` |
| 5 | `vote_comm_tree_root` |
| 6 | `vote_comm_tree_anchor_height` (transcript metadata, as in ZKP2 `circuit.rs:169-177`) |
| 7 | `min_dc_ballots` (in place of `proposal_id`) |
| 8 | `voting_round_id` |
| 9 | `ea_pk_x` |
| 10 | `ea_pk_y` |

**Conditions (all MUST):**
- **D1 Membership.** `van_old` is a leaf under `vote_comm_tree_root` (ZKP2 condition 1).
- **D2 VAN integrity.** `van_old = Poseidon2(Poseidon6(DOMAIN_VAN, g_d_x, pk_d_x, W, round, authority), rand)`.
- **D3 Address ownership.** Same as ZKP2 condition 3 (CommitIvk; `pk_d = [ivk]g_d`).
- **D4 Spend authority.** `r_vpk = ak + [alpha]G`. Out of circuit, a RedPallas signature over the batch sighash.
- **D5 VAN nullifier.** `van_nullifier = Poseidon4(nk, "vote authority spend", round, van_old)`, with the identical tag and arity to ZKP2 condition 5, recorded in nullifier type 0x01 [code] `V/x/vote/types/keys.go:98-114`.
- **D6 Full authority.** `authority == 2^51 - 1`.
- **D7 Bounds.** `w - min_dc_ballots ∈ [0, 2^30)` and `W - w ∈ [0, 2^30)`. The chain enforces `min_dc_ballots >= 1`.
  - `w >= 1` is safety-critical: `w = 0` would make the successor VAN equal to the input VAN, so its nullifier would collide and the remaining weight would be stranded.
- **D8 Successor.** `van_new = Poseidon2(Poseidon6(DOMAIN_VAN, g_d_x, pk_d_x, W - w, round, 2^51 - 1), rand)`, with the same address and the same `rand`.
- **D9 Shares.** `Σ_{i<16} v_i = w`, with each `v_i ∈ [0, 2^30)`.
- **D10 El Gamal (V1).** `C1_i = [r_i]G` and `C2_i = [v_i]G + [r_i]ea_pk`, with `r_i ≠ 0` and the same generator as ZKP2 condition 11.
- **D11 Commitments.** The share commitments and `shares_hash` as in §1.4.3.
- **D12 DC.** As in §1.4.3, with `proposal_id = 0` as a circuit constant and `delegate_index` range-checked to 32 bits.

**Deterministic secrets (SHOULD).** DC share split, `r_i` and `blind_i` derive from the hotkey PRF. Use domain `"zvote-dc-v1"` and context `(round, van_old, dc_slot)`, kept distinct from vote secrets, so a wallet can recompute share nullifiers after a crash.

#### 1.4.5 The proxy delegation transaction (Vote Action Batch)

Tag 0x09 (proposed; the chain component owns the assignment). The free bytes are 0x09, 0x0B, 0x0C and 0x0F; 0x0A is forbidden [code] `V/api/codec.go:25-45`.

```
MsgVoteActionBatch {
  bytes  vote_round_id;
  MsgDelegateVote registration;          // optional ZKP1; when set, synthetic anchor height 0
  uint64 vote_comm_tree_anchor_height;   // required iff registration unset
  repeated VoteAction actions;           // DCs first (1..max_dc_actions_per_tx), then casts (0..50)
}
VoteAction = oneof { DelegationCommitmentAction dc; CastVoteAction cast }   // cast = MsgCastVote fields
DelegationCommitmentAction { van_nullifier[32]; r_vpk[32]; vote_authority_note_new[32];
                             delegation_commitment[32]; proof[<=15 KiB]; spend_auth_sig[64] }
```

**Rules:**
- All DC actions precede all casts.
- Every action nullifier is distinct within the message, checked in `ValidateBasic`. `CheckNullifiersUnique` only checks the store [code] `V/x/vote/keeper/keeper_voting.go:54-66`.
- Casts have unique proposals.
- Action `i > 0` is anchored at `SingleLeafRoot(van_new[i-1])`. Action 0 is anchored at a stored root, or at `SingleLeafRoot(registration.van_cmx)`.
- Each action's signature covers `SVOTE_VOTE_ACTION_BATCH_SIGHASH_V1`. That digest binds round, registration presence and `van_cmx`, anchor height, action count, and for each action its type byte and every public field in order.
- The chain supplies `min_dc_ballots`, `ea_pk` and `round` from round state.
- Canonical protobuf encoding with unknown fields rejected, as for 0x06 and 0x07 [code] `V/api/codec.go:143-154`.
- Encoded size must be at most `ProtocolCapabilities.max_vote_tx_bytes`.

**Appended leaves.** DC leaves in action order, then VC leaves in action order, then the final VAN. Intermediate VANs are not appended, as today.

**Event.** `vote_action_batch{round_id, dc_leaf_indices, vc_leaf_indices, final_van_leaf_index, dc_count, cast_proposal_ids}`.

#### 1.4.6 DC share submission and reveal

- **Helper payload.** The existing `SharePayload` schema is unchanged, with `proposal_id = 0` and `vote_decision = delegate_index`.
  - The helper validation branch replaces the `[1, 50]` and `< 8` checks for this case [code] `V/internal/helper/api.go:773-777`.
  - The choice validator (`api.go:735-742`) checks that `d` is a registered index and that the round has proxy delegation enabled.
  - The helper reads the leaf at `tree_position` (`api.go:663-670`), so it necessarily learns the DC leaf.
- **Chain.** `MsgRevealShare` (0x04) with `proposal_id = 0` is valid iff:
  - `round.proxy_delegation.enabled`;
  - `vote_decision < NextDelegateIndex`, including revoked delegates (accepting them keeps accounting conserved);
  - ZKP3 verifies with public `(0, d)`;
  - the share nullifier is fresh (type 0x02).
- **Effect.**
  - `AddToTally(round, 0, d)`.
  - **MUST NOT** call `IncrementShareCount(round, 0, d)`.
  - Emit `reveal_pool_share{round_id, delegate_index, share_nullifier}` (not `reveal_share`).
  - Counts toward `MaxVoteShareSubmissionsPerBlock = 256` [code] `V/app/vote_share_submission_proposal.go:13`.
- **Why 0x04 is kept and not a new tag:** 0x04 is the permanent scalar-reveal path. In V2 rounds it is used only for proposal 0, and V2 vector reveals take a new tag.

#### 1.4.7 Delegate pools and tally routing

In the `EndBlock` that moves a round from ACTIVE to TALLYING [code] `V/x/vote/module.go:491-512`, and immediately after the status write:
- For each stored route `(d, p, o)` in ascending `(d, p)` KV order: if `Pool[d]` exists, set `agg[p][o] = agg[p][o] ⊕ Pool[d]`, creating it if absent.
- Pools are never modified or decrypted.
- Unrouted `(d, p)` pairs contribute nothing (abstain).
- `ShareCount` is untouched.
- Routing MUST complete before any partial decryption, which is injected in later blocks.
- `ValidateEntryBounds` MUST keep rejecting proposal 0.
- An implementation MAY accumulate in memory (decompress each pool once), provided the results are identical.

#### 1.4.8 Delegate route messages (ZIP-DR defines DK; ZIP-PD defines routes)

```
MsgDelegateRoute {            // tag 0x0C proposed
  bytes  vote_round_id;
  uint32 delegate_index;
  repeated RouteEntry routes; // 1..50, unique proposal_id
  bytes  signature;           // DK over route sighash
}
RouteEntry { uint32 proposal_id; uint32 vote_decision; }
```

- **Route sighash:** `BLAKE2b-256("SVOTE_DELEGATE_ROUTE_V1" || chain_id || round_id || u32 d || dk_pubkey || entries sorted by proposal_id)`. The chain id prevents testnet/mainnet replay.
- **Valid iff:**
  - the round is ACTIVE and `blockTime < vote_end_time`, and `enabled`;
  - `d` exists with status ACTIVE;
  - the signature verifies under `d`'s current DK;
  - each `(p, o)` passes `ValidateVoteChoice`;
  - no `Route(round, d, p)` exists for any entry (all-or-nothing).
- **Effect:** store `Route(round, d, p) = {o, height}` and emit `delegate_route{round_id, delegate_index, proposal_ids, decisions}`.
- Routes are public, one-shot and final.

#### 1.4.9 Delegator verification (normative for wallets claiming the extension)

For each DC `j` (delegate `d_j`, weight `w_j`):
- **(a) Inclusion.** The DC leaf at `dc_leaf_indices[j]` equals `dc_j` (via `CommitmentLeaves` or the tx event).
- **(b) Reveal completeness.** Recompute `nf_i = Poseidon4(DOMAIN_SHARE_SPEND, dc_j, i, blind_i)` [code] `VC/src/share_reveal/circuit.rs:173-185` and check each exists in nullifier type 0x02. Use the IAVL-provable key `0x01||0x02||round||nf` [code] `V/x/vote/types/keys.go:98-114`; Vizor's participation reader already verifies IAVL proofs for type 0x00 [doc] `VZ/docs/voting-participation.md:9-31`.
  - **By ZKP3 soundness, existence implies the share was added to `Pool[d_j]`.** No delegator-specific tag is needed.
- **(c) Routes.** Fetch `d_j`'s routes. Per proposal, show the routed option, or "did not vote (abstained)" once `vote_end_time` has passed.
- **(d) Final.** Any full node can recompute `agg` from pools and routes. Decrypted totals are DLEQ-checked.

Display states:
- `pending` before `vote_end_time`;
- `counted k/16`, with a warning if `k < 16` at the end (partial weight);
- `final`.

**Required queries** (chain component):
- `GET /shielded-vote/v1/delegates?after=&limit=`
- `GET /shielded-vote/v1/delegates/{index}`
- `GET /shielded-vote/v1/rounds/{round_id}/routes?delegate_index=`
- `GET /shielded-vote/v1/rounds/{round_id}/nullifiers/{type}/{nf}`, plus the ABCI store path for proofs

#### 1.4.10 Capability advertisement (WAPI amendment)

- **Chain.** `QueryProtocolCapabilitiesResponse` gains (existing fields 1-3 unchanged [code] `V/proto/svote/v1/query.proto:72-76`):
  - `proxy_delegation` (bool);
  - `vote_action_batch_wire_tag`, `delegate_route_wire_tag`, `delegate_register_wire_tag`, `delegate_update_wire_tag`;
  - `max_dc_actions_per_tx`, `max_vote_tx_bytes`;
  - `repeated CircuitFingerprint {name, vk_sha256}` for all circuits. This closes the "no circuit version" gap.
- **Dynamic config.** A new optional top-level `extensions.proxy_delegation_v1.rounds[round_id] = {registry_snapshot_sha256, min_dc_ballots, signatures[]}`.
  - Signed by the round `trusted_keys` over `"zvote-proxy-round-v1" || round_id || sha256(canonical JSON without signatures)`.
  - Round entries keep `auth_version: 2`. `supported_versions` is unchanged.
  - Zodl ignores unknown keys: its Swift `Codable` structs list only known fields [code] `ZI/secant/Sources/Dependencies/VotingModels/VotingServiceConfig.swift:6-58`. Rust uses no `deny_unknown_fields` [code] `ZV/src/config/mod.rs:852-898`.
- **Static config (new pin).** Optional `delegate_directory {snapshot_urls[], trusted_keys[], threshold}`. These are purpose-separated keys that sign registry snapshots.
- **WAPI version handling** (`WAPI:712-741`). Additive extensions MUST NOT bump `vote_protocol` or `tally`. Wallets MUST ignore unknown extensions. A wallet MAY implement an extension only if the chain capabilities, the round parameters and the signed extension entry all agree.

#### 1.4.11 Timing

- Proxy transactions, pool reveals and routes are all accepted only while ACTIVE and `blockTime < vote_end_time`. **No new chain cutoff.** Keeping the window unchanged keeps Zodl's behavior identical.
- Wallet SHOULD finish proxy delegation before `vote_end_time - last_moment_buffer` (`SUB:294-301`).
- Wallet MUST NOT start one after `vote_end_time - 30 min`.
- In the last-moment window, DC shares follow `SUB:303-312`. The wallet MUST disclose that the exact DC amount becomes decryptable by an EA-threshold coalition.
- **Registry operations** are accepted at any time. Route validity uses the DK status at route inclusion.

### 1.5 ZIP-DR (delegate registry and identity proofs): outline

1. **DK.** Ed25519 (RFC 8032), 32 bytes.
   - bech32m HRP `zvdk` (mainnet) or `zvdktest`.
   - Fingerprint: `BLAKE2b-256("zvote-dk-fp-v1" || dk)[0..10]`, rendered in groups of 4 plus an identicon.
2. **Proof post.**
   - X: ASCII, at most 280 weighted characters, no URLs, containing the line `zvote-delegate:v1:<bech32m dk>`.
   - GitHub gist `zvote-delegate.txt`.
   - Domain: `_zvote-delegate.<domain>` TXT, or `/.well-known/zvote-delegate.txt`.
3. **Two-way binding.** The DK signs `"zvote-binding-v1" || network || provider || provider_user_id || proof_locator || created_at`.
   - The X numeric user id is the key, never the handle [doc] identity dossier, design implications.
4. **Verifier attestation.** Ed25519 by a registered verifier key over `"SVOTE_DELEGATE_ATTEST_V1" || chain_id || dk || binding_digest || issued_at || expires_at`.
   - Deduplicate by signer key. No admin bypass (ICNS bugs).
   - Failure classes: 404 means proof removed after a 72 h grace; 5xx and 429 mean no state change.
5. **Chain messages** (custom wire, fee-less):
   - `MsgRegisterDelegate{dk_pubkey, attestations[], pop_signature}`, where the PoP covers `"SVOTE_DELEGATE_POP_V1" || chain_id || dk || attestation_digest`.
   - `MsgUpdateDelegate{delegate_index, op = rotate{new_dk, new_attestations, new_pop} | revoke, signature_by_current_dk}`.
   - A coordinator-action payload `MsgSetDelegateVerifiers{keys[], threshold}`.
   - The chain stores only `dk`, `attestation_digest`, status, height and key history. **No X content on chain.**
6. **Rotation and revocation.**
   - Rotation co-signed by the old DK is immediate.
   - Recovery without the old DK needs a fresh attestation plus a cooling-off (default 7 days, or the next round).
   - Revocation by the DK, or by a verifier-keyset coordinator action for impersonation or takedown.
   - Effects: no new routes; existing routes stand; reveals to the index are still accepted.
7. **Snapshot.**
   - (a) **Binding list:** append-only, sequence-numbered, signed by `delegate_directory.trusted_keys`. It contains index, dk, provider, numeric id, proof locator, attestation, status and key history. Its hash is pinned per round in the config extension.
   - (b) **Profile overlay:** mutable, refreshed at least daily, short expiry. It contains handle, display name, pfp sha256, bio, featured flag and confusable skeleton. It lives in a mutable store, never in git (X 24 h deletion duty).
   - (c) **Thumbnail bundle:** re-encoded and content-addressed.
8. **Wallet rules.**
   - MUST bind a DC to `(index, dk)` verified against the chain.
   - MUST NOT contact X or `pbs.twimg.com`. MUST NOT use `Image.network`.
   - MUST download the whole snapshot (no per-delegate queries).
   - MUST warn on lookalikes of featured delegates.
   - Deeplinks carry only the DK fingerprint.

### 1.6 Book pages to rewrite (claims that become false)

| Page:line | Current claim | Action |
|---|---|---|
| `BOOK/README.md:14` | "Delegation (TODO) lets you assign voting rights (fully or partially) to third parties" | Point to proxy delegation. |
| `BOOK/delegation/delegation-setup.md:3-34` | VAN split into two VANs; "can … further delegate"; "delegate's identity is hidden" (:30); "§6.0 Gov Steps V1" (:34) | Rewrite as "Proxy delegation overview" (DC, pools, routes). Delete the transitivity and hidden-delegate claims. |
| `BOOK/delegation/partial-delegation.md:7-38` | Successive VAN splits; "Nobody can observe the delegation amounts" (:34); a ZEC example (:15-21) | Rewrite: DC splits in ballots, up to 10 per transaction, full-authority requirement, precise amount-privacy statement (§2 T3). |
| `BOOK/delegation/server-delegated-shares.md` | "delegates share submission" (a third meaning of "delegation") | Rename to "Server-assisted share submission". Add DC payloads. |
| `BOOK/userflow/delegating-your-vote.md:1-28` | VAN split; "Further delegate" (:24); "Nobody can see the delegation amount" (:18) | Rewrite for delegators: choose, split, final, verify. Add `userflow/acting-as-a-delegate.md`. |
| `BOOK/data-types.md:20, 36, 38-42, 104` | u16 bitmask; `vsk.nk` is the same as `nk`; "When delegating: amount is split, two new VANs"; "Delegated in Phase 3" | Fix all four. Add DC, Pool, Route and RegistryEntry types. |
| `BOOK/overview/design-principles.md:20, 25, 33` | "No one but the delegation recipient can see the delegation amount"; ambiguous "Servers who receive delegations…"; Chaum-Pedersen | In B2 even the delegate cannot see amounts; fix the ambiguous sentence and the tally text. |
| `BOOK/overview/privacy-guarantees.md:7-26` | No proxy section; post-quantum paragraph premise (:26) | Add what is public (routes, counts) and what is hidden. Add trust assumptions for helpers (they see the delegate choice) and the EA threshold (pools). |
| `BOOK/circuits/van-nullifier.md`, `governance-nullifier.md:28-30`, `proposal-authority-decrement.md:99-101` | `vsk.nk == nk`; 16-bit | Fix. |
| New: `zkps/zkp4-delegation-commitment-proof.md`, `circuits/delegation-commitment.md`, `chain/delegate-registry.md`, `overview/threat-model.md` | none | Add. |
| `appendices/tally.md`, `appendices/share-splits.md`, `chain/chain-api.md`, `chain/roles-and-admin.md`, `wallet-integration/guide.md` (§12 custody contrast) | none | Add the routing step, DC shares, new endpoints, verifier keyset, and proxy APIs. |

---

## 2. Threat model after the change

**Adversaries.**
- `A_pub`: chain observer.
- `A_del`: one or more delegates.
- `A_help`: helpers / vote servers. They see payloads and client IP unless Tor; Tor is opt-in and off by default [doc] vizor dossier.
- `A_EA`: a coalition of at least `t` validators, equivalent to full `ea_sk` [doc] `ZIP-A:180-185`.
- `A_ver`: verifier or directory operator.
- `A_buy`: vote buyer or coercer.

Composite collusions are noted where they matter.

**T1 Delegator identity anonymity.**
- **Claim.** A proxy delegation is unlinkable to the delegator's Zcash identity by all of `A_pub, A_del, A_help, A_EA, A_ver`. Timing and IP linkage are the same as for voting today.
- **Rationale.**
  - The proxy transaction is authorized by the hotkey: a RedPallas signature under `r_vpk` plus ZKP4. Its only links are the VAN lineage and, when batched with ZKP1, governance nullifiers that are unlinkable without `nk` [doc] `ZIP-A:153-160`.
  - The delegator never contacts the delegate. The wallet downloads the whole snapshot.
  - `A_help` sees IP plus DC leaf (`V/internal/helper/api.go:663-670`), exactly as for votes.

**T2 Delegate-choice privacy (which delegates a delegator picked).**
- **Hidden from `A_pub`, `A_del`, `A_EA`, `A_ver`.** `delegate_index` is private in ZKP4, and ZKP3 hides which DC a reveal opens.
- **Not hidden from `A_help`.** Each helper receiving a DC share learns `(DC leaf, d)` and, through events, the proxy transaction.
- **Leaked to everyone:** the number of DCs per proxy transaction (`dc_count`), which is the number of distinct delegates chosen from that VAN.
- **Rationale.** This matches today's exposure of vote decisions to helpers [doc] `SUB:145-160`, `ZIP-A:185-190`. In V2 rounds, direct votes become helper-private but DC choices do not (T13).

**T3 Per-delegation amount privacy.**
- **Hidden from `A_pub`, `A_help`, `A_ver`, and the delegate** (strictly better than VAN transfer, where the delegate learns `num_ballots` [doc] specs dossier §0).
- **`A_EA` learns individual share plaintexts.** Grouping the 16 shares of one DC requires `A_help` collusion or timing correlation, so per-DC amounts get the same protection as vote amounts today (base-10 plus PRF remainder [code] `VC/src/vote_proof/builder.rs:39-112`).
- **Last-moment window:** shares are submitted together or as a single share, so `A_EA` learns the exact DC amount.
- **Rationale.** B2 splits DCs into 16 shares exactly like votes. B1 would publish one unsplit `Enc(w_j)` per delegation in the transaction, letting `A_EA` decrypt exact amounts and link the delegate set to one VAN (rejected).

**T4 Per-delegate pool totals.**
- **Claim.** `Pool[d]` is computationally hidden from any party without at least `t` EA shares, **except** for what follows from the published exact per-(p, o) totals and the public route table. Specifically:
  - (i) `Pool[d]` is disclosed exactly when `d` is the sole contributor to some published `(p, o)` total: no direct votes on `o` and no other delegate routed `o`.
  - (ii) It is estimable by differencing per-proposal turnout when `d` routes a strict subset of proposals and direct turnout is similar across proposals.
  - (iii) `A_EA` can decrypt any pool off-chain at any time.
  - The protocol itself never decrypts a pool: `ValidateEntryBounds` [code] `V/x/vote/keeper/keeper_voting.go:291-304`.
- **Rationale.** Totals are exact integers (the NU7 poll published 0.125-ZEC precision [doc] delegation dossier) and routes are public.
- **Mitigations (policy):**
  - delegates are nudged to route every proposal;
  - an explicit Abstain option on every proposal in proxy-enabled rounds (owner question);
  - no leaderboards.
- Rounding only in the UI is pointless, because chain state is public.

**T5 Per-delegate delegation counts.**
- **Claim.** The number of DCs to each delegate is public and live: `#reveal_pool_share(d) / 16`, exact once all shares land.
- Timing of reveals shows momentum during the round.
- **Rationale.** Every pool reveal carries a public `d`. Hiding it would need dummy DCs (costly) or one-hot anonymity sets (future work, V2-style).
- Do not feed `ShareCount`. Disclose the count in docs.

**T6 Integrity (no double counting, conservation).**
- **Claim.** Every ballot of a registered VAN counts at most once per proposal, either directly or through exactly one pool and one route, and weight is conserved.
- **Rationale.**
  - ZKP2 and ZKP4 share one nullifier derivation and set (D5), so a VAN is either cast or delegated.
  - D6 ensures no proposal was voted from this lineage before delegation. D8 keeps MAX on the successor.
  - D7 and D9 bound weights (no field wrap).
  - Routes are one-shot per `(d, p)`. A pool lands in exactly one `(p, o)` per proposal.
  - Intra-batch duplicate nullifiers are rejected in `ValidateBasic`.
  - Bounded totals fit `TallyBSGSBound = 2^28` [code] `V/x/vote/types/keys.go:31-33`.

**T7 Delegate accountability and delegator verifiability.**
- **Claim.**
  - Each delegate's decision per proposal is public, attributable to a DK, immutable, and applied deterministically to the whole pool.
  - A delegate cannot vote pooled weight differently from its route, cannot drop specific delegators, and cannot re-delegate.
  - A delegator can verify end to end that its DC landed in `Pool[d]` (§1.4.9 b) and how `d` routed.
- **Not covered:**
  - (i) The delegate's own private direct vote may contradict its route; this is undetectable and acceptable.
  - (ii) Pool size is not verifiable by anyone.
  - (iii) X-to-DK binding is trusted to the verifier (T11).
  - (iv) Verification depends on the hotkey secret. Vizor hotkeys are unrecoverable [doc] `VZ/rust/src/wallet/voting/README.md:42-50`, so device loss loses verification, not weight.

**T8 Finality and abstention.**
- **Claim.** A DC is irrevocable. An unrouted `(d, p)` abstains.
- A compromised or absent DK means the pool's influence is lost or captured for unrouted proposals.
- **Rationale.** Owner decision 2. No override path exists.

**T9 No receipt-freeness, no coercion resistance (explicit).**
- **Claim.** A delegator can prove to anyone that it delegated exactly `w` to `d` by revealing the DC opening (shares, randomness, blinds). The verifier then recomputes the DC, finds the leaf and checks the share nullifiers.
- Delegates' routes are public, so a buyer who is (or pays) a delegate obtains verifiable, fungible voting power.
- **Rationale.** The system is already not receipt-free: voters choose El Gamal randomness and secrets are deterministic from the hotkey [doc] `ZIP-A:520-558`, and the custody handoff transfers verifiable full VANs today [code] `ZV/src/delegation_capability.rs:46-52`.
- B2 lowers the buyer's cost (one route per proposal for many sellers).

**T10 Vote markets and concentration.**
- **Claim.** Pools are natural aggregation points for vote markets (cf. LobbyFi on Arbitrum [doc] delegation dossier) and for influencer concentration.
- Caps cannot be enforced because totals are encrypted, and "up to 10 delegates" is a per-transaction UX limit, not an invariant across transactions.
- **Mitigations:** weighted-random directory ordering, a featured tier with published criteria, no count-sorted lists, and per-round opt-in.

**T11 Identity binding.**
- **Claim.**
  - A route is attributable to the X/GitHub/domain identity only to the extent the verifier attested truthfully.
  - Handle recycling and lookalikes are mitigated by numeric-id keying, fingerprints and warnings.
  - An X account takeover can rebind only after the cooling-off, unless the attacker also holds the DK.
- **`A_ver` can lie about bindings** (impersonation) or equivocate snapshots. Mitigations:
  - snapshot hash pinned in signed round config;
  - raw evidence included so anyone can re-check;
  - a t-of-n schema;
  - two mirrors.

**T12 Liveness and censorship.**
- **Claim.** `A_help` or validators can censor all reveals tagged `d` or all routes by `d`, because both are public and attributable. Pool weight then silently vanishes or abstains.
- Mitigation: share redundancy across helpers (`SUB:332-342`) and multiple vote servers. This is a new targeted-censorship surface compared with anonymous votes.
- Tally failure leads to timeout with empty results, as today [code] `V/x/vote/types/keys.go:25-29`.

**T13 Private-vote-choice (V2) interplay.**
- **Claim.** In V2 rounds:
  - direct votes hide choices from helpers and the public;
  - DC delegate choices remain visible to helpers, and per-share `d` is public;
  - routes are public by design.
- Pools route into bucket accumulators unchanged. Decision 4's properties are unchanged.
- V2's warning that a holder of the full key can decrypt individual ciphertexts [doc] `VC/docs/design.md:441-460` applies equally to pools.

**T14 Tree and resource exhaustion.**
- **Claim.** With `min_dc_ballots = m`, a VAN of weight `W` can create at most `floor(W/m)` DCs, bounding leaves and helper load per unit of registered weight.
- Without `m`, `W` up to 2^30 per VAN is exploitable fee-lessly.
- **Rationale:** D7, plus the chain capacity guard.

---

## 3. Cross-repo sequencing

### 3.1 Repository work and version pins

| Repo | Deliverable | State-breaking? | Version |
|---|---|---|---|
| zips + book | ZIP-A errata PR; ZIP-PD; ZIP-DR; WAPI/SUB/SETUP/CER amendments; book rewrite | n/a | drafts |
| voting-circuits | `zkp4` module (circuit, builder, prove, verify); exports `delegation_commitment()`, `dc_share_nullifier()`, `vk_fingerprints()`; ballot-denominated builder APIs; ZKP4 VK pin; **ZKP1/2/3 `vk_fingerprint_unchanged` tests as a release gate** [code] `VC/src/*/prove.rs` (vk pins) | new VK only | 0.13.0-rc.N, then 0.13.0 |
| vote-sdk (chain + helper + FFI) | `circuits/` crate pin `=0.13.0` (today `=0.12.0` [code] `V/circuits/Cargo.toml:28`); ZKP4 verify FFI; tags 0x09/0x0B/0x0C/0x0F; registry state and coordinator action; routes; proposal-0 reveal branch; routing hook; queries and REST; events; capabilities; tree-capacity guard; per-block proof-action cap; helper DC payloads; genesis import/export of the registry | yes | v1.7.0 (dormant merges, then an activation commit) |
| zcash_voting | Proxy planner (ballots, at most 10, `min_dc_ballots`); DC builder and prover; batch builder with byte-budget splitting; DC share payloads; VotingDb v25 (rebuild of `chain_submissions.kind` CHECK [code] `ZV/src/storage/migrations/001_init.sql:207`, `migrations.rs:5`; new outbound-delegation and delegate-identity tables, preserving rows per `migrations.rs:7-15`); verification API; snapshot/registry verification; config extension; delegate mode (DK, route signing) | client | 5.2.0 (or 6.0.0 if APIs break), pinning voting-circuits 0.13.0 |
| verifier service (**new repo**, e.g. `valargroup/delegate-registry`) | X API plus GitHub/domain proofs, attestation signing, binding list, profile overlay, pfp re-encode pipeline, takedown/featured admin, monitoring, 24 h deletion jobs | n/a | v1 |
| token-holder-voting-config | `extensions.proxy_delegation_v1` schema and CI verification; new static pin with `delegate_directory`; old pins stay immutable (`CFG:scripts/tests/immutable-pins.sh`) | n/a | config |
| Vizor | Directory, profile, lookalike and featured UI; split editor; proxy job; verification screen; delegate mode (register, route); copy rename; account-deletion cleanup; route-table audit; figma scenarios | n/a | release N |
| vizor-deeplink-server | `/delegate` route with fragment `#v1/<dk-fingerprint>`; static page; generic OG image; no DB (charter `DLS/README.md`) [code] `DLS/src/routes.ts:10-19` | n/a | minor |

**Pin rule.** The chain and every Vizor-bound client pin the **same** voting-circuits minor.
- Today the chain is on 0.12.0 and clients on 0.12.2, with identical VKs [doc] circuits dossier.
- An e2e check compares the chain's `ProtocolCapabilities.circuits` fingerprints with the client's compiled ones, and Vizor fails closed with "update required".
- Zodl and custody-voter stay on old pins; they stay valid because the ZKP1/2/3 VKs are unchanged.

### 3.2 Dormant-flag merges and activation (vote-sdk)

- Follow the atomic-batch precedent: a const flag merged dormant (commit `7cd8b521`) and flipped with the upgrade handler (commit `771fbdef`).
- Add `types.ProxyDelegationEnabled = false` gating:
  - (a) tag decode in `IsCustomTag` and `IsVoteTag`, as explicit branches because `IsVoteTag` is a contiguous range [code] `V/api/codec.go:57-59`;
  - (b) InterfaceRegistry registration of the new messages (registration is itself state-breaking [doc] chain dossier);
  - (c) the `MsgRevealShare` proposal-0 branch;
  - (d) the EndBlock routing hook;
  - (e) the new coordinator-action payload;
  - (f) capability fields, which report false while dormant.
- Dormant PRs are labeled `V:state/compatible` only if mixed versions accept identical inputs [doc] `V/docs/release-branches.md:13-36`.
- The activation PR flips the flag and registers `registerV170Upgrade` (no-op handler; no new store) [code] `V/app/upgrades.go:13-24`.
- The tree-capacity guard and per-block proof-action cap ship in the same activation. They are state-breaking and independently valuable.

### 3.3 Order across repos

1. Specs freeze ZKP4 statement, messages, config extension and ZIP-DR formats (week 0 to 3).
2. voting-circuits 0.13.0-rc.1, which unblocks chain FFI and client prover work.
3. vote-sdk dormant merges, in parallel. The registry and routes need no circuits.
4. zcash_voting APIs against the rc.
5. Verifier service and config schema, in parallel from week 3.
6. Vizor: UI against mocks from week 4, integration once zcash_voting rc lands.
7. Deeplink: any time after the link format freezes.
8. External audit of ZKP4 plus chain handlers.
9. Final tags: voting-circuits 0.13.0, then vote-sdk v1.7.0, then zcash_voting, then Vizor.

### 3.4 Testnet, stage and mainnet plan

**Stage chain:** a coordinated v1.7.0 upgrade with the flag flipped, run with the same runbook as mainnet. Then three stage rounds, created through the stage dynamic config:
- **T1 (internal):** proxy enabled. Internal Vizor builds plus **a current Zodl build and an old Vizor build voting in the same round**. A scripted tally check confirms `totals == direct + routed pools`.
- **T2 (delegate beta):** 10 to 20 real influencers register through the stage verifier on stage, and the full X proof flow is exercised.
- **T3 (adversarial and load):** `LAB` ten-validator topology [doc] `LAB/README.md`. Run the adversarial list (§5.7) and the load matrix (§5.6).

**Mainnet activation:**
- Only between rounds: all rounds FINALIZED or ceremony-failed, and every helper queue empty [doc] `V/docs/runbooks/software-upgrades.md:476-495`.
- After activation:
  - a coordinator action sets the verifier keyset;
  - featured delegates register;
  - the binding list is published to two mirrors.

**[TEST] rounds on mainnet:** Vizor hides `[TEST]` rounds behind a toggle [code] `VZ/lib/src/providers/voting/voting_round_visibility_provider.dart:12-17`, but the local Zodl checkout has no `[TEST]` filtering [code] `ZI/secant/Sources` (no match). So:
- either do not sign mainnet [TEST] rounds into the prod dynamic config (internal testers use a custom static config source [doc] vizor dossier), or
- first confirm that current production Zodl hides them.

**First public proxy round:**
- `ProxyDelegationParams.enabled = true` in round creation;
- the signed config extension;
- the Vizor remote kill switch ON (version-keyed JSON, precedent `swap-enabled.json`).

### 3.5 Coordination with private vote choice (V2)

**Recommendation: two separate activations, B2 first.**
- B2 is **additive**: it adds a ZKP4 VK and changes no existing VK. Zodl is unaffected.
- V2 is **breaking** for every client:
  - new ZKP1.5;
  - changed ZKP2 and ZKP3 VKs and `DOMAIN_VC_V2`;
  - a ZKP1.5 proof of 57.7 KiB against a 15 KiB `MaxProofSize` [doc] `VC/docs/design.md:472`; [code] `V/x/vote/types/keys.go:65-74`;
  - a `vote_protocol` bump that forces a Zodl update.
- Bundling them would make B2 hostage to V2's review, its proof-cap change and Zodl's release.

**There is no "combined VK bump" to gain.** B2's only new VK is ZKP4, which V2 does not touch, provided V2:
- (a) preserves the V1 scalar ZKP3 byte-for-byte as module `pool_share_reveal`, with its VK pinned to today's ZKP3 fingerprint;
- (b) keeps the V1 `shares_hash` and El Gamal gadgets (or ZKP4 vendors them);
- (c) gives V2 vector reveals a new wire tag, leaving 0x04 as the scalar path for proposal 0;
- (d) routes pools into bucket accumulators by bucket index equal to the option index.

**If V2 lands first:** B2 still works under (a) to (d). Merge (a) and (b) into V2's PR as acceptance criteria now.

---

## 4. Zodl and old-client compatibility

### 4.1 Invariants that MUST hold (each gets a regression test)

- **Z1.** ZKP1, ZKP2 and ZKP3 circuits and VKs are unchanged (fingerprint tests).
- **Z2.** Tags 0x02 to 0x07: decoding, validation, sighash domains (`V/x/vote/types/sighash.go:12-20`) and REST paths are unchanged. New messages use new tags only.
- **Z3.** `MsgRevealShare` with `proposal_id ∈ [1, 50]` behaves identically, including `IncrementShareCount` and the `reveal_share` event.
- **Z4.** The `VoteRound` JSON gains only optional fields.
  - `proposals` never contains id 0.
  - The `voting_round_id` derivation and `proposals_hash` are unchanged.
  - Swift `Codable` ignores extra keys, but a renamed or removed key would break Zodl.
- **Z5.** `TallyResults` and `VoteSummary` never contain proposal 0. `VoteSummary` iterates `round.Proposals` [code] `V/x/vote/keeper/keeper_tally.go:175-205`.
- **Z6.** Dynamic config:
  - `supported_versions` is unchanged (`vote_protocol: "v0"` today [code] `CFG:prod/dynamic-voting-config.json`);
  - proxy data lives only under `extensions`;
  - proxy rounds keep their current `auth_version`, because Zodl marks unknown versions `unknownAuthVersion` per round [code] `ZI/secant/Sources/Dependencies/VotingAPIClient/RoundAuthenticator.swift:36-38`;
  - the static pins Zodl uses are unchanged.
- **Z7.** The helper payload schema and validation for proposals 1 to 50 are unchanged.
- **Z8.** DC leaves are ordinary VCT leaves. `CommitmentLeaves` and root computation are unchanged. Zodl's anchors remain valid.
- **Z9.** Existing event types keep their exact attribute sets. New behavior uses new event types.
- **Z10.** Existing `ProtocolCapabilities` fields 1 to 3 are unchanged. New fields are appended.
- **Z11.** No new deadline earlier than `vote_end_time` applies to existing messages.
- **Z12.** The per-block reveal cap is shared. The load test proves Zodl reveals are not starved (§5.6).

### 4.2 What old clients will misreport

- **M1.** Per-option totals include routed pool weight. Zodl shows them as plain totals. The numbers are correct, but there is no "via delegates" attribution (new clients cannot attribute it either).
- **M2.** `VoteSummary` share counts exclude pooled weight. Any share-count-based "participation" figure undercounts.
- **M3.** A seed that proxy-delegated in Vizor shows as "already used for this round" in Zodl or old Vizor (governance nullifiers spent). They cannot show the delegation or verify it.
- **M4.** Old Vizor's participation detector classifies "delegated" as "voted".
- **M5.** Third-party explorers that parse reveal events see new event types and no proposal-0 `reveal_share`.
- **M6.** A Vizor build downgraded mid-round cannot read DB v25. Vizor must refuse a downgrade rather than reset.

---

## 5. Test strategy

### 5.1 voting-circuits (MockProver negatives)

Each case must fail unless marked "pass".
- Input authority with any single bit cleared (D6).
- `w = 0`; `w = min_dc_ballots - 1`; `w = W + 1`; `w > W` with field wrap.
- `W - w` forged to `p - k` (wrap).
- `Σ v_i ≠ w`; `v_i = 2^30`.
- Nullifier using the wrong tag, wrong `nk` or wrong round.
- **Cross-test:** ZKP2 and ZKP4 nullifiers are equal for the same VAN (pass).
- Successor with a different `rand`, `g_d`, `pk_d`, round or authority.
- DC with `proposal_id ≠ 0`; `delegate_index = 2^32`.
- `r_i = 0`.
- `ea_pk` mismatch.
- Wrong root; VAN from another round.
- `DOMAIN_VC` replaced by `DOMAIN_VAN`.
- **Cross-circuit:**
  - ZKP3 accepts a DC opening with `(0, d)` (pass);
  - ZKP3 rejects `(0, d')` and `(p ≥ 1, d)`;
  - ZKP2 rejects `proposal_id = 0` (existing).
- **Release gates:**
  - ZKP1/2/3 VK fingerprints are unchanged against 0.12.x;
  - ZKP4 fingerprint is pinned;
  - proof at most 15,360 B;
  - record K and rows;
  - desktop and mobile prove-time benchmarks.

### 5.2 Chain keeper, ante and ABCI (Go unit and integration)

**Batch:**
- intra-list duplicate nullifier rejected;
- a DC after a cast rejected;
- 0 or 11 DCs rejected;
- synthetic-anchor chaining (first and later actions);
- sighash binds order: swapping two DCs invalidates the signatures;
- non-canonical encoding rejected;
- `enabled = false` rejects;
- post-`vote_end_time` rejected;
- a VAN spent by ZKP2 cannot be DC'd, and the reverse;
- oversized transaction rejected.

**Pool reveal:**
- proposal 0 accepted only when enabled;
- unregistered `d` rejected;
- revoked `d` accepted;
- duplicate nullifier rejected;
- counts toward the 256 cap;
- emits `reveal_pool_share`;
- `ShareCount(0, d)` stays absent.

**Route:**
- one-shot (two routes in the same block: the first wins deterministically);
- bad signature, revoked DK, unknown proposal, option at or above the option count, duplicate proposal in one message, after end time, replay across chain and round.

**Registry:**
- non-verifier attestation;
- duplicate signer counted twice toward the threshold;
- bad proof of possession;
- duplicate DK;
- rotation with and without the old signature (cooling-off);
- revoke;
- per-block registration cap;
- invalid messages rejected before signature verification cost.

**Tally determinism:**
- multi-validator app-hash equality with routes inserted in different transaction orders;
- route to an option with no direct votes (sole-router case);
- unrouted pools never decrypted;
- `ValidateEntryBounds` rejects proposal-0 partial-decryption and SubmitTally entries;
- BSGS bound;
- timeout path;
- genesis export and import round trip with registry, routes and pools;
- tree-capacity guard returns `ErrCommitmentTreeFull` in DeliverTx, not EndBlock.

### 5.3 e2e-tests (`V/e2e-tests/tests/`)

- **New `proxy_delegation_flow.rs`:**
  - register two delegates;
  - ZKP1 plus 2 DCs plus 3 casts in one batch;
  - a second wallet does a full delegation without casts;
  - helpers reveal;
  - delegates route (one leaves P3 unrouted);
  - tally;
  - assert `totals == direct + routed pools` (the test knows the plaintexts);
  - assert the delegator verification API returns `counted 16/16` and `abstained on P3`.
- **New `proxy_mixed_clients.rs`:** existing `voting_flow_zcash_voting.rs` and `atomic_delegate_cast.rs` flows pinned to zcash_voting 5.1.x run unchanged in a proxy-enabled round.
- **Capability test:** fingerprints from `ProtocolCapabilities` equal the client's.

### 5.4 zcash_voting (property and integration)

**Planner properties:**
- for any `W`, allocations and `m`, outputs satisfy `Σw_j <= W`, each `w_j >= m`, at most 10 per VAN;
- deterministic largest-remainder rounding from percentages;
- whole-VAN assignment is preferred across bundles to minimize DC count.

**Determinism:** DC secrets are reproducible from the hotkey secret, and share nullifiers match the chain.

**Crash and resume:** idempotent at every submission step; never re-spend a VAN.

**Other:**
- migration v24 to v25 round trip with live-round rows;
- config extension parsing (absent, malformed, bad signature, unknown extension ignored);
- verification state machine;
- byte-budget splitting.

### 5.5 Vizor

**Widget tests (mobile-tagged and desktop) for each screen state:**
- directory: empty, offline, snapshot invalid, featured, search exact versus lookalike warning;
- profile: verified, proof removed, key changed, revoked;
- split editor: below minimum, more than 10, rounding preview, keep-some;
- review: finality and publicity copy;
- multi-step progress, backgrounding and resume;
- success;
- verification: pending, counted k/16, Yes/No/abstained, final;
- delegate mode: register, route editor, one-shot confirmation.

**Figma-compare scenarios** in `VZ/lib/figma_compare/figma_compare_scenarios.dart` [doc] `VZ/AGENTS.md:187-213`.

**Additional tests:**
- route-table audit test for new hosts under the Tor policy;
- a lint or test forbidding `Image.network`;
- account-deletion cleanup of new artifacts;
- kill-switch off hides all entry points.

### 5.6 Load test (`LAB`, ten validators)

Constants:
- chain ceiling: 256 reveals per block at about 1.2 s, about 213 reveals/s [code] `V/app/vote_share_submission_proposal.go:13`;
- helper throughput: 0.58 shares/s per worker, 2 workers per validator by default [doc] `V/docs/runbooks/software-upgrades.md:446-457`;
- a DC costs 16 reveals; a full delegator with `k` DCs replaces `16·P` reveals per VAN with `16·k`, so proxy delegation **reduces** helper load when `k < P` [inference].

**Load matrix:**

| Scenario | Mix |
|---|---|
| L1 | 5k direct voters × 10 proposals (baseline) |
| L2 | L1 plus 5k delegators × 2.5 DCs, spread uniformly |
| L3 | L2 with 30% of DCs inside the last 40% / 6 h helper window [code] `V/internal/helper/store.go:1606-1619` |
| L4 | Dust attack: one VAN of 10k ballots with `m = 1` versus `m = 8`, measuring queue displacement of honest shares |
| L5 | EndBlock routing with 1,000 delegates × 50 proposals |
| L6 | 100 batches × 10 DCs in one block (verify time; validates the per-block proof-action cap) |

**Pass criteria:**
- every honest share is revealed before `vote_end_time` with at least 30% headroom;
- block time p99 is at most 3 s;
- routing EndBlock takes at most 500 ms;
- no app-hash divergence.

### 5.7 Adversarial test list

1. ZKP4 on a VAN after a partial vote.
2. ZKP2 and ZKP4 on the same VAN, in the same block and across blocks.
3. The same VAN twice in one batch.
4. Inflation through field wrap.
5. Revealing a VC as proposal 0, and a DC as proposal `p`.
6. Proposer-injected partial decryption for proposal 0.
7. SubmitTally entries for proposal 0.
8. Route replay across chain and round.
9. A route race in one block.
10. Route from a revoked or rotated DK.
11. Registry spam with bogus attestations.
12. Attestation reuse after expiry.
13. Threshold counting the same signer twice.
14. Reveals to an unregistered index.
15. Targeted helper censorship of index `d` (measure redundancy).
16. Non-canonical encodings of new tags (mempool cache bypass).
17. Tree fill via DCs with the capacity guard active.
18. Oversized batch.
19. Snapshot equivocation: one mirror serves a different binding list, and the wallet must refuse.
20. Malicious pfp payload (pipeline re-encode).
21. Deeplink carrying an address next to a handle (must be ignored).
22. Lookalike handle of a featured delegate.
23. X account takeover rotation without the old DK, checked against cooling-off.
24. Zodl and old Vizor in the same round, end to end.

---

## 6. Milestones, effort, critical path

| Milestone | Weeks | Effort (eng-weeks) | Notes |
|---|---|---|---|
| M0 Spec freeze (ZIP-A errata, ZIP-PD statement, messages, config extension, ZIP-DR formats) | 0-3 | 5 (spread) | Book rewrite continues to week 14. |
| M1 voting-circuits 0.13.0-rc.1 (ZKP4 plus exports plus negatives plus benches) | 2-9 | 7-8 | **Critical.** |
| M2 External audit (ZKP4 plus chain handlers), fixes, final 0.13.0 | 9-13 | external plus 2 | **Critical.** |
| M3 vote-sdk dormant merges (registry, routes, reveal branch, routing, batch, FFI, capabilities, guards) plus helper | 3-12 | 9 plus 2 | Registry and routes are independent of circuits. |
| M4 Verifier service MVP (X plus GitHub, attestations, snapshot, overlay, pfp) | 3-10 | 6 | Parallel; X budget about $30-$320/month [doc] identity dossier. |
| M5 zcash_voting (planner, prover, batch, DB v25, verification, delegate mode) | 4-12 | 9 | Needs M1 rc. |
| M6 Vizor (UI on mocks from week 4; integration from week 10) | 4-15 | 12 | |
| M7 Config schema plus CI; deeplink route | 6-9 | 2.5 | |
| M8 Stage activation, T1/T2/T3 rounds, load lab | 12-16 | 4 | **Critical.** |
| M9 Mainnet activation between rounds; first proxy round | 16-18 | 1 | **Critical.** |

- **Total:** about 60 eng-weeks plus the audit.
- **Critical path:** M0 ZKP4 freeze → M1 → M2 → final tags → M8 → M9, about 16 to 18 weeks.
- **Parallel work:** M3 registry and routes, M4, M6 UI, M7, the book, and follow mode.
- **Staffing:** circuits 1-2, chain 2, client 1-2, Vizor 2, identity 1, specs and QA 1.

**Follow mode (no-protocol v0).**
- **Recommendation:** ship a *thin* v0 only as a by-product of the B2 identity stack, one round before B2 if M4 is ready. It must never gate B2.
- **Scope:**
  - the delegate directory (same DK, proof post, verifier and snapshot as B2);
  - delegate-published, DK-signed "voting guides" served from the overlay;
  - a "copy to my ballot" prefill. The user still casts private votes.
- **Value:**
  - it de-risks the most externally fragile part (X API, ToS, moderation, pfp pipeline, lookalikes) in production;
  - it seeds delegate supply;
  - it remains useful after B2 as the privacy-maximal, override-capable option, since B2 is final.
- **Incremental cost:** about 2-3 eng-weeks beyond M4.
- **Not worth it** if it requires anything B2 will not reuse, or if it delays M1.

---

## 7. Documentation, runbooks and launch checklist

### 7.1 Documentation and runbooks

- **Verifier service runbook (new repo):**
  - verifier Ed25519 keys held in Infisical or an HSM, with rotation through `MsgSetDelegateVerifiers`;
  - X developer account ownership and spend alerts;
  - failure classes (404, 5xx/429) and re-verification cadence (user daily, proof weekly and before each round);
  - the 24 h deletion SLA and delisting;
  - featured-tier curation (two-person review, published criteria);
  - impersonation and takedown handling;
  - incidents: verifier key compromise (coordinator action to remove it, then re-attest); X API outage (pause new registrations; GitHub/domain fallback); snapshot equivocation report.
- **vote-sdk `docs/runbooks/software-upgrades.md`, new section "v1.7.0 proxy delegation activation":**
  - preconditions (round state and helper DB queries at `software-upgrades.md:476-495`);
  - pre-stage;
  - post-checks: `ProtocolCapabilities.proxy_delegation = true`; ZKP4 fingerprint equals the published one; registry empty;
  - set the verifier keyset; register a canary delegate on stage;
  - rollback policy: no binary rollback after the first proxy transaction; disable for new rounds through round parameters, plus the Vizor kill switch.
- **Round creation runbook:** set `ProxyDelegationParams`, publish and sign `extensions.proxy_delegation_v1`, pin the binding-list hash, and include an Abstain option if adopted.
- **Helper runbook additions** (`docs/runbooks/helper-queue-rescue.md`): pool-share metrics and capacity guidance.
- **Genesis and chain-reset runbook:** carry the registry forward, since indices must stay stable.
- **Public docs:**
  - delegator FAQ (finality, abstain, what is public, verification, device loss);
  - delegate guide (key safety and backup, proof post, routing is one-shot and public, counts are public, sole-router disclosure);
  - updated privacy page and threat model.

### 7.2 Launch checklist

- ZIP-PD and ZIP-DR drafts published. Book threat model and delegation pages updated.
- ZKP4 audit closed. VK fingerprints for all four circuits published.
- voting-circuits 0.13.0, vote-sdk v1.7.0, zcash_voting and the Vizor store release are final. App-store review covers the UGC directory (report and block).
- Stage: T1, T2 and T3 passed, including Zodl and old Vizor in a proxy round and the scripted tally equality check. Load-lab pass criteria met.
- Mainnet upgrade applied between rounds, capabilities verified, verifier keyset set.
- Featured delegates registered and re-verified. Binding list on two mirrors.
- Config extension signed and CI-verified. Old static pins untouched.
- Kill switch tested both ways. Dashboards live: pool reveals, routes, helper queue, verifier health, X API errors.
- Comms published, including explicit no-receipt-freeness and public-count disclosures.
- On-call owners named for chain, helper, verifier and Vizor.

---

## 8. Contracts this component assumes from others (integrator checklist)

See `cross_component_contracts`. Each one above is cited where it is defined: §1.4 for chain and circuits, §1.5 for identity, §1.4.10 for config, §3 for releases.
