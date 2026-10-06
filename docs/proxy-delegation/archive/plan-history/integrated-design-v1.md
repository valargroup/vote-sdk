# Proxy delegation: integrated design v1 (for adversarial review)

This document reconciles six component specs into one design. Component specs (authoritative for detail unless this doc overrides them) live in `scratchpad/specs/{circuits,chain,client,vizor,identity,spec_rollout}.md`. Research dossiers live in `scratchpad/dossiers/`.

## 0. Owner decisions (fixed)

1. Delegate votes are public: public delegate pools.
2. Proxy delegation is final for the round. There is no override and no revocation. A proposal the delegate does not route abstains.
3. Delegate listing is open with proof (X post, with GitHub and domain fallbacks). A Valar verifier attests, the chain holds a registry of delegate keys, and there is a curated "featured" tier.
4. Per-delegate totals stay hidden: pools are never individually decrypted.

Scope is Vizor only. Zodl and old Vizor must keep voting unchanged in proxy-enabled rounds.

## 1. Terminology

| Term | Meaning |
|---|---|
| Registration (spec) / "voting authorization" (Vizor UI) | Today's ZKP1 / MsgDelegateVote (notes to a VAN). Vizor copy that currently says "voting delegation" is renamed. |
| Proxy delegation (spec) / "Delegate" (UI) | The new feature. |
| Delegator / delegate | The person giving weight / the registered person receiving it. |
| Delegation commitment (DC) | Prose term for the proxy commitment. Code identifiers use the `proxy_delegation_*` / `ProxyDelegation*` prefix, never bare `delegation`. |
| Pool | Per-(round, delegate) El Gamal accumulator, `TallyKey(round, 0, d)`. |
| Route | A delegate's public per-proposal choice. |
| DIK / DRK | Delegate identity key / delegate route key. |

## 2. End-to-end flow

### Delegator (any account type; Keystone and Ledger sign only ZKP1, as today)
1. In Vizor, the user chooses up to 10 delegates with percentages (or "delegate everything"), optionally keeping a remainder, and confirms finality.
2. The client plans DCs over the wallet's bundle VANs. Each VAN's DCs go before any cast on that VAN. The planner uses integer ballots (Hamilton largest remainder, at least 1 ballot each) and the fewest DCs.
3. Per bundle, it submits one `MsgVoteActionBatch`: an optional ZKP1, then 1..k ZKP4 proxy actions, then 0..50 ZKP2 casts for the remainder (split if over the byte budget). Each successive proof is chained by single-leaf roots.
4. DC shares (16 per DC, or 1 in last-moment single-share mode) go to the existing helpers with `proposal_id = 0` and `vote_decision = delegate_index`. Helpers reveal them with the **unchanged ZKP3**, and the chain adds them into `Pool[d]`.
5. Status screen: DC inclusion, then DC shares revealed k/16 (share nullifiers, IAVL-provable), then per proposal: Voted (choice) / Abstained / Not yet / Did not vote.

### Delegate
1. Onboarding:
   1. Create the delegate key phrase. DIK and DRK are derived from it.
   2. Post the marker line on X (or a GitHub gist, or DNS).
   3. The verifier resolves the numeric account id; the DIK signs the binding statement; the verifier issues a threshold attestation.
   4. `MsgDelegateRegistry{register}` on chain assigns `delegate_index` (append-only, starting at 1).
2. Per round: the delegate dashboard casts public routes, `MsgDelegateRoute` signed by the DRK. Routes are one-shot per proposal and need re-auth plus a confirm sheet. An explicit abstain route is allowed. The delegate can also vote their own weight privately as usual.

### Tally
At the ACTIVE→TALLYING EndBlock, before any partial decryption, the chain deterministically adds each `Pool[d]` into `agg[p][route(d,p)]`, plus a deterministic `Enc(0; rho)` re-randomizer per routed bucket. Pools are never decrypted. Threshold decryption then proceeds as today.

## 3. Circuits (voting-circuits 0.13.0): authoritative spec `specs/circuits.md` §2

**ZKP4 (proxy-delegation proof)** is a new additive circuit at K=11. The prototype measured **2,015/2,048 rows, an 11,008 B proof, and ZKP2-like prove time**. ZKP1, ZKP2 and ZKP3 VKs stay byte-identical.

Conditions:

| Condition | Relation |
|---|---|
| C1 | VCT membership |
| C2 | Old VAN with authority = MAX as a **constant cell** |
| C3 | Address ownership |
| C4 | Spend authority `r_vpk` |
| C5 | VAN nullifier byte-identical to ZKP2 (same 0x01 set) |
| C6 | Successor VAN with the same address, rand, round and MAX, weight W−w |
| C7 | `W_new ∈ [0,2^30)` (anti-inflation) |
| C8 | `Σshares + W_new = W` |
| C9 | Shares < 2^30 |
| C10 | `shares_hash` |
| C11 | El Gamal under `ea_pk` |
| C12 | `w = Σshares ≠ 0` |
| C13 | `DC = Poseidon5(DOMAIN_VC, round, shares_hash, 0 /*constant*/, delegate_index /*private*/)` |
| C14 | `dc_slot ∈ [0,16)`, `dc_slot_nullifier = Poseidon4(nk, TAG("proxy delegation slot")+slot, round, rand)`: at most 16 DCs per registration chain per round (DoS bound) |

Public inputs (11):
`[van_nf, r_vpk_x, r_vpk_y, van_new, dc, root, anchor_height, round, ea_pk_x, ea_pk_y, dc_slot_nf]`

**OPEN, to be measured in review:** add `delegate_index_bound` (public) with `1 ≤ d ≤ bound` in circuit, so the chain can reject `bound ≥ NextDelegateIndex` at submission. Include it only if it still fits at K=11. Otherwise rely on client cross-checks plus reveal-time acceptance.

ZKP3 is reused unchanged. It has no range checks on `proposal_id`/`vote_decision`. A reveal for `(0, 1234)` and for `u32::MAX` was measured to pass.

ZKP2's `proposal_id ≠ 0` gate becomes **load-bearing**: without it, ZKP2 could emit `VC(0,d)` while keeping full weight. Re-document it and pin the test.

DC secrets come from an explicit `dc_seed`. The builder API takes the seed, and new PRF domains 0x10–0x14 are added. The builders are ballot-denominated, and new public helpers are exported (DC hash, slot nf, DC share nullifiers, pool share reveal builder, VK fingerprints).

Private-vote V2 (unmerged branch) MUST preserve:
- the v1 scalar ZKP3 byte-identical as `pool_share_reveal`;
- the v1 shares_hash and El Gamal gadgets;
- DOMAIN_VC as live;
- the compact ZKP2's `proposal_id≠0` gate;
- PRF domains 0x10–0x14 reserved;
- vector reveals on a new tag.

## 4. Chain (vote-sdk v1.7.0, dormant-flag merges, one coordinated activation between rounds)

### 4.1 Messages (custom wire, fee-less, canonical encoding enforced for all new tags)

| Tag | Message | Notes |
|---|---|---|
| 0x09 | `MsgVoteActionBatch` | See below. |
| 0x0B | `MsgDelegateRegistry` | oneof op (see below) |
| 0x0C | `MsgDelegateRoute` | Routes for one delegate in one round |
| 0x04 | `MsgRevealShare` | Gains a proposal-0 branch |

**`MsgVoteActionBatch` (0x09).**
- Fields: `{round, anchor_height, optional registration (MsgDelegateVote fields), actions[] oneof {proxy_delegation, cast}}`.
- Rules:
  - DCs strictly precede casts.
  - At most 10 DCs per message and at most 50 actions in total.
  - The chain advertises a byte budget.
- Anchoring: with a registration, synthetic anchor 0 = `SingleLeafRoot(van_cmx)`; otherwise a real anchor. Each later action is anchored at `SingleLeafRoot(prev.van_new)`.
- One `SVOTE_VOTE_ACTION_BATCH_SIGHASH_V1` digest covers everything, including each DC's `recovery_hint` and `dc_slot_nf`.
- `ProxyDelegationAction` = `{van_nf, van_new, proxy_delegation_commitment, dc_slot_nf, recovery_hint[32], proof, r_vpk, vote_auth_sig}`.
- Leaf append order: final VAN, then commitments in action order (as today).
- Nullifiers: VAN nfs go to set 0x01 and slot nfs to new set 0x03. `CheckNullifiersUnique` is fixed to reject intra-message duplicates for all sets.

**`MsgDelegateRegistry` (0x0B) ops:**
- `register` (threshold Ed25519 verifier attestation + DIK proof-of-possession + DRK)
- `rotate_route_key` (DIK-signed, immediate)
- `change_identity_key` (co-signed, 72 h delay)
- `recover_identity` (attested, 7 d delay)
- `cancel_pending`
- `freeze` / `unfreeze` (DIK or DRK)
- `revoke` (DIK, terminal)

Pending changes take effect lazily (an effective view is computed at read time). At most 64 lifetime key changes per delegate, and a per-block cap on registry ops.

**`MsgDelegateRoute` (0x0C).**
- Fields: `{round, delegate_index, key_epoch, entries[(proposal_id, decision)], DRK sig}`.
- Validation: all-or-nothing; one-shot per (round, d, p); accepted until `vote_end_time`.
- `decision < num_options(p)`, or `0xFFFFFFFF` for an explicit abstain that is recorded and not counted.
- The delegate must be effectively ACTIVE: not frozen, not suspended, not revoked.
- Routes already made survive a later freeze, suspension or revocation.

**`MsgRevealShare` (0x04) proposal-0 branch.**
- Accepted iff `round.proxy_delegation.enabled && 1 ≤ d < NextDelegateIndex`. Delegate status is ignored, so pools of inactive delegates simply abstain.
- Effects: `AddToTally(round,0,d)`, a separate `PoolShareCount`, and a distinct `reveal_pool_share` event. It does **not** touch `ShareCount` or VoteSummary, so old clients and explorers never see a phantom proposal 0. It counts toward the 256/block reveal cap.

**Coordinator payloads.**
- `MsgSetProxyDelegationParams`: verifier set, register and recover thresholds, `enable_for_new_rounds`, caps.
- `MsgSetDelegateSuspension`: freeze-only, threshold, public reason code. It cannot create entries, change keys, or void routes.

### 4.2 State and queries

- **Registry family 0x1A** (records, key index, used attestation ids, next index, params). Records hold the index, DIK, DRK, key_epoch, status, suspension, provider code, a salted `subject_commit`, any pending change, and heights. **No X content on chain.**
- Other prefixes: routes `0x1B||round||u32(d)`; routing base and audit `0x1C`; proxy params `0x1D`.
- Pools reuse `TallyKey(round,0,d)`.
- `VoteRound.proxy_delegation = {enabled, ...}` is a snapshot taken at round creation from `enable_for_new_rounds`. It is outside the round-id preimage.
- Tree-capacity guard: `ErrCommitmentTreeFull` returned in DeliverTx, and `MaxTreePosition` fixed to 2^24−1.
- REST/gRPC:
  - `delegates` (paged, `updated_since_height`), `delegates/{index}`, `delegates/by-key/{hex}`;
  - `delegate-routes/{round}` (whole-round, paged, incremental);
  - `delegate-pools/{round}` (share counts only);
  - `nullifier/{round}/{type}/{nf}`;
  - `proxy-actions/{round}?from_height` (recovery feed: per DC action `{height, tx_hash, van_nf, dc, leaf_index, recovery_hint, final_van_leaf_index}`).
- Documented IAVL keys for light-client proofs.
- `ProtocolCapabilities` adds `proxy_delegation`, wire tags, `max_dc_actions_per_tx`, `max_vote_actions_per_tx`, `max_vote_tx_bytes`, and **VK fingerprints for ZKP1–4**.

### 4.3 Tally routing (EndBlock at ACTIVE→TALLYING)

1. Iterate delegates in sorted order, then routed proposals in sorted order.
2. `agg[p][o] = direct[p][o] + Σ_d Pool[d] + Enc(0; rho)`, where `rho = HashToScalar("svote-route-rerand-v1"‖round‖p‖o‖combined)`. This fixes the prototyped identity-C1 griefing attack, which would otherwise make every DLEQ fail and time the round out with EMPTY results.
3. Store the base accumulators for audit and emit a `proxy_pools_routed` event.
4. Proposal 0 stays excluded from partial decryption, completeness checks and `ValidateEntryBounds` (already true in code; lock it in with tests).
5. Measured cost: about 1.4 s for 10k delegates × 50 proposals on an M3 Ultra.

### 4.4 Timing

There is **no new chain deadline**, for parity with votes. DCs, pool reveals and routes are all accepted until `vote_end_time`. The client applies the same last-moment logic as for votes and blocks new delegation inside the helper last-moment buffer, with a hard cutoff 30 min before end. Late delegation to a delegate who has already routed is "informed delegation" and is allowed.

### 4.5 Helper

The helper gets a proposal-0 branch in `validatePayload` and `ValidateShareChoice`: a registered index in an enabled round. It returns a permanent, distinct error for unknown indices. Scheduling is unchanged.

## 5. Client (zcash_voting): authoritative spec `specs/client.md`

- **Allocation.** A durable proxy allocation (preview → commit with `plan_digest` → locked) is a first-class intent, so ZKP1 is planned even with zero ballot intents. The cap is 10 delegates per account per round (UX cap). The planner and packer are described in client §3.2–3.3.
- **ARK.** The account proxy recovery key is `BLAKE2b(key=Orchard OVK, "ZVoteProxyARK_v1", net‖round)`, with a hotkey fallback.
  - `hint_key = H(ARK, van_nf)`
  - `dc_seed = H(ARK, van_nf‖d‖w‖layout)`
  - `recovery_hint` (32 B) = `ChaCha20Poly1305(hint_key, 0-nonce, aad=tag‖round, 0x01‖layout‖0x0000‖d‖w)`
- **Recovery tiers.** R1 (DB lost, hotkey kept) recovers slots and the remainder VAN. R2 (DB and hotkey lost, UFVK kept) recovers delegations and verification, but not remainder voting (same as today). Consequence: **UFVK holders can see the account's proxy delegations.**
- **Schema v25** adds proxy tables, two new `chain_submissions` kinds, `ShareKey CommitmentRef::{Vote, Proxy}`, and new NextStepKinds (`ProxyDelegate`, `AdvanceProxyBatch`, `SubmitProxyShares`, `ConfirmProxyShare`).
- **Verification view model.**
  - Per delegate: ballots, inclusion, shares k/16.
  - Per proposal: `Voted | Abstained | NotYetVoted | DidNotVote`.
  - Routes and share nullifiers are IAVL-verified, with whole-round route lists and decoys.
- **Registry client.** The chain is authoritative for index, DIK, DRK and status. The directory snapshot is display-only and signed by a purpose-separated directory key (new static-config section, threshold-capable, monotonic seq, equivocation checks). Selected delegates are cross-checked against IAVL-proven chain records at preview, at commit, and immediately before proving. The client refuses non-ACTIVE, frozen, suspended or pending-change delegates.
- **Gating.** Never bump `vote_protocol` or the round `auth_version`. Use:
  - `WalletCapabilities.proxy_delegation`;
  - dynamic-config `extensions.proxy_delegation_v1` per-round entries signed by `trusted_keys` under the new domain `zcash-shielded-vote:round-proxy:v1`;
  - chain `ProtocolCapabilities` plus a VK fingerprint match.
- **Delegate APIs.** Delegate phrase → DIK/DRK derivation (keyed BLAKE2b generations); marker, statement and digest encoders (shared `zcash_vote_delegate` crate with cross-language vectors); route signing and submission; rotation, freeze, revoke and recovery.

## 6. Identity, verifier, directory: authoritative spec `specs/identity.md`

- **Keys.** Two Ed25519 keys per delegate. The DIK (identity, can be cold) and DRK (route, hot) are derived from a **dedicated 24-word delegate key phrase**, not the wallet seed. This works for Keystone and Ledger users and needs no ZIP number.
- **Marker.** The post line is `zcash-vote-delegate v1 zvdk1…` (brand-neutral, no URL or dots). The verifier resolves the numeric account id and the DIK signs the binding (two-way binding).
- **Verifier.** It uses the official X API (pay per use) as primary, and oEmbed only to re-verify existing entries. It never uses the syndication endpoint. GitHub gist and DNS providers ship at launch. Attestations are threshold Ed25519, with single-use ids, expiry ≤ 72 h, dedupe by verifier id, and no admin bypass. Launch is 1-of-1 Valar with t-of-n machinery in place.
- **On chain.** Only a salted `subject_commit`. The salt stays in a mutable store and is shredded on deletion (X 24 h duty).
- **Directory.** A registry doc (ID-level), a profiles doc (X-hydrated, 30 h expiry), and avatar packs (fetched server-side, pure-Rust sandboxed decode, re-encoded WebP 64/256, content-addressed). All are hash-chained and served from two mirrors (valargroup infra plus `functions.vizor.cash`). Search is local; nothing is server-side.
- **Curation.**
  - Listing thresholds for browse (account ≥ 180 d, ≥ 100 followers, registered ≥ 24 h), while exact lookup always works.
  - Featured: at most 30, with 2-of-3 curator approval and a public log.
  - Lookalike detection via UTS#39 skeletons, plus fingerprints and identicons everywhere.
  - A report form, and coordinator freeze for confirmed impersonation.

## 7. Vizor: authoritative spec `specs/vizor.md`

- Extend `VotingSubmissionJobNotifier` with a proxy mode instead of a second job.
- Delegation is available until the user's first own cast in the round.
- A combined review submits delegations plus own votes atomically per VAN.
- A finality checkbox, and a CTA that names the amount.
- Status screen with per-delegate, per-proposal states.
- Delegate onboarding and dashboard. A route needs re-auth plus a confirm sheet that names the choice.
- Deep link `link.vizor.cash/d/<index>#k=<fingerprint>`, with a desktop paste fallback.
- Images decoded in Rust and rendered from pixels; never `Image.network`. Directory and verifier hosts go into the Tor-aware route table.
- Gating fails closed: build define + remote flag + capabilities + VK fingerprint + valid registry. Beta runs on [TEST] rounds.
- ZKP1 copy is renamed to "voting authorization".
- The delegation count is shown only on the delegate's own dashboard, marked approximate.

## 8. Privacy and threat model (as designed)

**Hidden:**
- Delegator identity (VAN anonymity).
- Delegator amounts, from the public, the delegate and helpers.
- Which delegate a DC targets, at DC tx time. It is revealed only per share at helper-randomized times, unlinkable to the DC.

**Public:**
- Delegate routes.
- That an anonymous batch contains k DCs.
- Per-delegate approximate delegation count (pool reveals / 16).

**Leaks against decision 4 (owner must be told):**
- (a) A pool total is exact when the delegate is the sole contributor to an option bucket.
- (b) A pool total is estimable by turnout differencing when the delegate leaves proposals unrouted.
- (c) A ≥t EA coalition can decrypt pools off-chain. This is the same trust as for shares today.
- Mitigation: an explicit "Abstain" **option** on every proposal in proxy rounds, plus route-all nudges, plus never sorting by or showing counts publicly.

**Not provided:** receipt-freeness (already absent today). Pools are vote-market aggregation points (as LobbyFi was on Arbitrum).

**Recovery hint.** It lets UFVK holders see delegations.

**Helpers.** Helpers learn the DC→delegate link and tx position. Delegates whose private vote and public route come from the same IP can be linked.

## 9. Rollout (authoritative spec `specs/spec_rollout.md`)

1. Specs: ZIP-A errata, ZIP-PD (proxy delegation), ZIP-DR (registry).
2. Circuits: voting-circuits 0.13.0, with the ZKP1/2/3 fingerprint non-regression as a release gate.
3. Chain: vote-sdk v1.7.0 with dormant merges and a coordinated activation between rounds.
4. Helper.
5. zcash_voting.
6. Verifier service (new repo).
7. Config repo: new static pin plus dynamic extension.
8. Vizor.
9. Deeplink server.
10. Stage [TEST] rounds, Zodl mixed-client test, external audit, mainnet.

Optional follow-mode v0. B2 ships before private-vote V2, with V2 acceptance criteria as in §3.

## 10. Known open items for review

- O1: `delegate_index_bound` fit at K=11 (§3).
- O2: one tag with optional registration (0x09) versus two tags (0x09 batch + 0x0B composite, chain spec). This doc uses one tag; the registry moves to 0x0B.
- O3: no chain DC cutoff (this doc) versus a stored cutoff `vote_end − clamp(10%, 60 s, 1 h)` (chain spec).
- O4: delegate key phrase (identity spec) versus seed-derived software-only (vizor spec) versus ZIP-32 registered (client spec). This doc picks the phrase.
- O5: Abstain option per proposal: round-content policy.
