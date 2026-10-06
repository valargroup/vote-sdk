# Proxy delegation: final implementation plan

Status: final plan for implementation, 2026-10-05. It supersedes integrated-design-v1 and the six component specs wherever they disagree. It folds in seven adversarial review lenses (soundness, privacy, liveness, identity, chain ops, circuit feasibility, completeness) and their skeptic verdicts.

Evidence labels: [code] read in a repo, [doc] from a spec or doc, [measured] from a prototype run, [inference] reasoned. Repos: **vote-sdk** (chain, helper, FFI), **voting-circuits**, **zcash_voting** (client SDK), **Vizor**, **verifier service** (new repo), **token-holder-voting-config**, **vizor-deeplink-server**, **zips/book**.

Terms: *registration* is today's ZKP1 (notes to a VAN, shown in Vizor as "voting authorization"). *Proxy delegation* is the new feature. A *DC* (delegation commitment) is the leaf a delegator creates. *Pool[d]* is the El Gamal accumulator `TallyKey(round, 0, d)`. A *route* is a delegate's public choice on one proposal. *DIK* and *DRK* are the delegate's identity key and route key.

---

## 1. Summary

1. **What we are building.** In proxy-enabled rounds, a Vizor user can give some or all of their round voting weight to up to 10 registered delegates and keep the rest to vote privately. Delegates vote publicly, one route per proposal. Delegated weight is counted through encrypted per-delegate pools that the protocol never decrypts individually.
2. **Why.** Many holders do not want to read every proposal. Today their weight simply goes unused. Public delegates with verifiable records give that weight a voice without revealing who the delegators are or how much each one gave.
3. **Circuits.** One new additive circuit, ZKP4, spends a full-authority VAN. It outputs a DC that commits to a hidden delegate index `d` and an amount `w`, plus a successor VAN holding `W − w`. ZKP1, ZKP2 and ZKP3 VKs stay byte-identical, so Zodl and old Vizor keep working unchanged.
4. **Pools.** Each DC is split into 16 El Gamal shares. Existing helpers reveal them with the **unchanged ZKP3** as `(proposal 0, decision d)`, and the chain adds them into `Pool[d]`.
5. **Tally.** At the ACTIVE→TALLYING EndBlock, the chain adds each pool into the option bucket its delegate routed, plus a deterministic `Enc(0; ρ)` re-randomizer. Threshold decryption then proceeds exactly as today. Unrouted proposals abstain.
6. **Delegate identity.** A dedicated delegate key phrase derives two Ed25519 keys: a DIK (identity, can be kept cold) and a DRK (routes, hot). The delegate proves control of an X account (or a GitHub account or domain) with a marker post. A Valar verifier issues a threshold attestation. The chain registry stores keys, status and a salted commitment only. No X content goes on chain.
7. **Discovery.** Wallets download a signed directory: a registry doc, a profiles doc, and avatar packs re-encoded server-side. Search runs locally on the device. A curated Featured tier is signed by curator keys. Fingerprints and lookalike warnings appear on every delegate surface.
8. **Compatibility.** Gating is additive only: no `vote_protocol` or `auth_version` bump. The feature turns on through chain capabilities plus a VK fingerprint match, a signed per-round config extension, and a Vizor kill switch.
9. **Delivery.** voting-circuits 0.13.0, vote-sdk v1.7.0 (dormant merges, one coordinated activation), zcash_voting, a new verifier service, and Vizor, with two audit tranches and stage rounds first. The critical path is about 19 weeks.
10. **Honest limits.** The protocol never decrypts a pool, but pool totals are not secret from an EA-threshold coalition or from inference on published totals. Delegation counts are public, helpers learn each DC's delegate, and there is no receipt-freeness (§5).

**Answers to the owner's original questions.**

- **(a) Delegate some or all of my weight to up to ~10 others, and keep some or none?** Yes. You choose 1 to 10 delegates with percentages, and either keep a remainder to vote yourself or delegate everything. Amounts are whole ballots (0.125 ZEC), at least 1 ballot per delegate, assigned by Hamilton largest-remainder rounding and shown exactly before you confirm. Two rules apply. You must delegate before your first own vote in the round, because the circuit needs full authority. And delegation is final for the round.
- **(b) ICNS-style X linking with a profile picture to look up influencers?** Yes, without ICNS's flaws. The delegate posts a marker line containing their key; the verifier binds the **numeric** X account id (never the handle) via the official X API, and the delegate's key signs the binding back. Profile pictures are re-encoded server-side and decoded in Rust; wallets never contact X. You search handles locally and see a fingerprint plus lookalike and "new this round" warnings. GitHub and domain proofs ship as fallbacks.
- **(c) If I delegate to an influencer, can I find out whether they voted with it?** Yes, per proposal. Routes are public and IAVL-provable, and Vizor shows each proposal as Voted (their choice), Abstained, Not yet, or Didn't vote (abstain once the round ends). It also shows that your shares reached their pool (k/16, provable via share nullifiers). Their route applies to the whole pool, your weight included. You cannot see the pool's total, or their private vote with their own ZEC, which may differ from their route.

**Top changes from integrated-design-v1:** ARK no longer keyed by the viewing key; no unfreeze op, DIK-only recovery cancellation, recovery hardening; 30-bit `delegate_index_bound`; no single-share DCs or DC immediate shares, with delegation closing at the last-moment buffer; a normative contract artifact with `chain_id`-bound digests; an on-chain proxy-action record for recovery; no Abstain option as a privacy mitigation; curator-signed Featured; no "Delegate more" in v1, plus a release path for stuck bundles.

---

## 2. Decisions log

### 2.1 Owner decisions (fixed)

| # | Decision | How the plan honors it | Where it cannot be fully honored |
|---|---|---|---|
| D1 | Delegate votes are public, via pools | Routes are public, signed by the DRK, IAVL-provable, and applied to the whole pool | A delegate's own private vote can differ from its route |
| D2 | Proxy delegation is final; unrouted means abstain | No override or revocation message exists. A DC is final once included. | Undispatched slots can be released, because they never landed (LIV-3) |
| D3 | Open listing with proof; Valar verifier; chain registry; featured tier | X, GitHub and DNS providers; threshold attestations; registry 0x1A; curator-signed Featured | Launch is 1-of-1 Valar, so the verifier is a trust and censorship point until a second verifier exists |
| D4 | Per-delegate totals hidden; pools never individually decrypted | The protocol never decrypts proposal 0. Only routed per-(p, o) totals are decrypted. | Exposed to a ≥t EA coalition; exact when a pool is the sole contributor to a bucket; estimable by differencing; delegation counts public (§5) |

### 2.2 Open items from v1, resolved

| Item | Resolution | Evidence |
|---|---|---|
| O1 `delegate_index_bound` | **Adopt** as public input 11: in-circuit `1 ≤ d ≤ bound` via 30-bit range checks on `d−1` and `bound−d`; chain requires `bound < NextDelegateIndex`, registry ≤ 2^30; the builder sets `bound = NextDelegateIndex − 1` itself (§4.1) | [measured] Still 2,015/2,048 rows, 78 fixed columns, 11,008 B proof, prove time within noise; soundness suite passes; a real proof fails if PI[11] changes |
| O2 one tag vs two | **One tag 0x09** with optional registration; registry 0x0B, routes 0x0C; `anchor_height == 0` if and only if a registration is present | SND-7 front-running analysis |
| O3 chain DC cutoff | **No consensus deadline** (vote parity). Client rule: no proxy batch planned **or broadcast** once `now ≥ vote_end − max(last_moment_buffer, 30 min)`, with chain-derived time. Single-share DCs removed. Coordinator `proxy_dc_paused` brake. | PRV-4, LIV-4, CMP-11 |
| O4 delegate keys | **Dedicated delegate key phrase**, in a non-BIP-39 format; hardware-account users can be delegates | CMP-5, IDN-14 |
| O5 Abstain option | **Not a privacy mitigation.** Keep the sentinel route `0xFFFFFFFF` (recorded, not counted); an Abstain option is product question Q2 | [measured] 42-44% of pools exactly solvable with an Abstain option (15 proposals, 30 voters, 5 delegates) vs 0% with the sentinel |

### 2.3 Design decisions

| # | Decision | Rationale | Rejected |
|---|---|---|---|
| 1 | B2: DC shares through helpers | Hides delegate choice and amounts from public and EA | B1 in-tx slots: EA sees exact amounts; delegate set linked to tx |
| 2 | ZKP4 as a separate circuit at K=11 | No existing VK changes | A ZKP2 mode: new VK for all voters |
| 3 | DC reuses `DOMAIN_VC` with constant proposal 0 | ZKP3 unchanged; ZKP2's `p ≠ 0` gate keeps it sound | New `DOMAIN_DC` and reveal VK |
| 4 | Input and successor VAN authority MAX | No double counting of cast weight | Per-proposal pools |
| 5 | Slot nullifier: ≤16 DCs per registration per round | Bounds helper load | Minimum size only (count grows with W) |
| 6 | p=0 reveals accepted for any existing index | Status changes never strand weight | Status check at reveal |
| 7 | EndBlock routing with ρ re-randomization | Pools final; blocks identity-C1 round kill | Lazy routing in three code paths |
| 8 | Pool counts at `ShareCountKey(round,0,d)`; distinct event | Free genesis export; VoteSummary never sees p=0 | Separate key (OPS-2) |
| 9 | Digests: BLAKE2b, ASCII domain, `lp8(chain_id)`, 32-byte fields | One convention; no cross-chain replay | `net` string, LE fields |
| 10 | DIK and DRK from a delegate phrase | Hot-key rotation keeps the index; hardware users included | Single key; seed derivation |
| 11 | No unfreeze; DIK rotation exits FROZEN | Stolen DRK cannot undo a freeze; no replay | DIK-or-DRK unfreeze |
| 12 | DIK-only recovery cancel; recovered keys cannot route older rounds | Takeover cannot capture formed pools | v1 rules |
| 13 | Delay floors; verifier warm-up | One coordinator key cannot re-key quickly | Unconstrained params |
| 14 | ARK from spending key or hotkey, never OVK | Viewing keys don't reveal votes | OVK-keyed ARK |
| 15 | No immediate DC share | No public tx-to-delegate link | DC share 0 immediate |
| 16 | One locked allocation per round | Simple schema and recovery | Top-ups in v1 |
| 17 | On-chain `ProxyActionRecord` per DC | Complete, provable recovery | Event or tx-index feed |
| 18 | Whole-list sync; sticky proof set | No per-index interest leak | Per-index REST; fresh decoys |
| 19 | Featured needs curator signatures | Directory-key theft can't mint Featured | Plain flag |
| 20 | X proof: original post, exact template | No binding via replies or retweets | Any post with the line |
| 21 | Signer recomputes and enforces budgets | API compromise isn't an oracle | Opaque-digest signer |
| 22 | Proxy PRs `V:state/breaking`, v1.7.0 only | No GasUsed divergence | Rolling dormant releases |
| 23 | 0x09 caps at 10 DCs and 50 actions, plus an advertised byte budget | Same envelope as today's 50-cast composite | Larger batches (over 1 MB RPC limits) |
| 24 | No per-block proof-verification cap in v1 | 0x06/0x07 already set this load class; a cap under 51 strands Zodl batches | A proof-action cap (OPS-10) |
| 25 | Per-block dedupe: one registry op per delegate, one route tx per (round, d) | Stops duplicate-signature floods | A global cap only (IDN-9) |
| 26 | Route entries identical to stored routes are no-ops | Safe retries near the deadline | Strict all-or-nothing (LIV-11) |
| 27 | ZIP-215 Ed25519 everywhere; torsion-component keys rejected | Chain, verifiers and auditors agree | Mixed stdlib, dalek and CometBFT rules (IDN-15) |
| 28 | Deep-link payload only in the URL fragment, fingerprint mandatory | Host and CDN logs never see the index | Index in the path (IDN-16) |
| 29 | Pool reveals share the 256/block cap and helper FIFO with votes | No new scheduling class | A priority class |
| 30 | B2 ships before private-vote V2, with V2 acceptance criteria | B2 changes no existing VK | Bundling with Zodl-breaking V2 |

---

## 3. Architecture

### 3.1 Actors

- **Delegator wallet:** Vizor plus the zcash_voting SDK; plans, proves, submits, delivers shares, verifies. Software, Keystone and Ledger accounts; devices sign only ZKP1.
- **Delegate wallet:** Vizor delegate mode plus the `zcash_vote_delegate` crate; holds the phrase, DIK and DRK; onboards; signs routes and registry ops.
- **Vote chain:** vote-sdk validators; verify proofs, hold registry, routes and pools, route at the tally transition, and hold EA key shares.
- **Helpers:** the svoted helper on each vote server; receive DC shares (learning `d`, the DC leaf and the client IP unless Tor is on), prove ZKP3, reveal at randomized times.
- **Verifier service:** Valar-run verifier-api, attest-signer (KMS/HSM), publisher, refresher, chain-indexer and independent re-verifier.
- **Directory mirrors:** the valargroup origin, `functions.vizor.cash` and a third independent mirror, all untrusted for integrity.
- **Config repo:** a new static pin and a per-round signed dynamic extension.
- **Coordinators:** vote managers who set params and verifiers and may suspend a delegate.

### 3.2 Sequence

```
Delegator (Vizor+SDK)      Directory    Vote chain                  Helpers        Delegate (Vizor)
  |                            |            |  (earlier) phrase -> DIK/DRK; marker post; verifier attests;
  |                            |            |<--------- 0x0B register {DIK, DRK, subject_commit, attestation}
  |                            |            |  index d assigned (append-only, from 1)
  |--GET signed docs---------->|            |                            |              |
  |--page /delegates, IAVL-prove sticky 10-key set (commit, pre-prove)-->|              |
  | plan: Hamilton -> slots; dc_seed(ARK); hint; bound = Next-1           |              |
  | prove: [ZKP1] -> ZKP4 x k -> [ZKP2 x m] (phased)                     |              |
  |--0x09 {reg?, DC..., cast...} signed over D1------>|                  |              |
  |                            |            | verify; set nfs 0x00/0x01/0x03;            |
  |                            |            | append final VAN, DCs, VCs; record 0x1D    |
  |<--event: leaf indices------------------|                            |              |
  |--16 shares/DC (p=0, decision=d, submit_at) -------------------------->|              |
  |  "Handed off" = definite acceptance of every share                   |              |
  |                            |            |<--0x04 reveal(0,d)+ZKP3 at random times    |
  |                            |            | Pool[d] += share                          |
  |                            |            |<----------------------- 0x0C route {p: o} signed by DRK
  |--poll route list; helper share-status-->|                            |              |
  |                         ... vote_end_time ...                        |              |
  |                            |            | EndBlock ACTIVE->TALLYING:                 |
  |                            |            |  agg[p][route(d,p)] += Pool[d]; += Enc(0;rho)
  |                            |            |<-- partial decryptions (DLEQ), tally as today
  |--IAVL-prove route sets and share nfs (sticky set)-->|               |              |
```

### 3.3 Data and trust boundaries

- **Chain-authoritative:** index, DIK, DRK, `key_epoch`, status, suspension, routes, pools, nullifiers, `NextDelegateIndex`, the proxy-action records.
- **Display-only:** the directory (handles, names, avatars, statements). A directory lie cannot redirect a delegation to a different index or key. Featured badges and handle labels are protected by curator signatures and wallet pinning (§4.6).
- **Third-party consumers:** the registry doc is ID-level and may be consumed by other wallets. The profiles doc stays Vizor-only until counsel clears redistribution.

---

## 4. Component designs

### 4.1 Circuits (voting-circuits 0.13.0)

**ZKP4 conditions.** All are measured at K=11: 2,015/2,048 rows, 34 advice and 78 fixed columns, 4 lookups.

| Cond. | Relation |
|---|---|
| C1 | Old VAN is a member of the vote-commitment tree under `root` |
| C2 | Old VAN = Poseidon2 integrity hash with authority = MAX (2^51−1) as a **constant cell** |
| C3 | Address ownership (same `nk` cell as C5 and C14) |
| C4 | Spend authority `r_vpk`, as in ZKP2 condition 4 |
| C5 | `van_nf` = Poseidon4(nk, "vote authority spend", round, VAN), byte-identical to ZKP2 (set 0x01) |
| C6 | Successor VAN: same address, rand, round and MAX; weight `W_new` |
| C7 | `W_new ∈ [0, 2^30)` (3-word strict lookup; the only check that stops `w > W` wraparound) |
| C8 | `Σ shares + W_new = W` |
| C9 | Each of the 16 shares `< 2^30` |
| C10 | `shares_hash` (v1 two-level hash) |
| C11 | El Gamal encryption of each share under `ea_pk` (v1 gadget) |
| C12 | `w = Σ shares ≠ 0` (inverse gate) |
| C13 | `DC = Poseidon5(DOMAIN_VC, round, shares_hash, 0 /*constant cell*/, d /*private*/)` |
| C14 | `dc_slot ∈ [0,16)`; `dc_slot_nf = Poseidon4(nk, TAG("proxy delegation slot") + slot, round, rand)` |
| C15 | `1 ≤ d ≤ bound`: witness `e1 = d − 1` and `e2 = bound − d`, each a strict 3×10-bit lookup range check, in a one-row custom gate (bound copied from instance 11) |

C7 plus C9 plus C12, together with ZKP1's `W ≤ 2^30`, give `1 ≤ w ≤ W` over the integers.

**Public inputs (12, in this order).** `van_nf, r_vpk_x, r_vpk_y, van_new, dc, root, anchor_height, round, ea_pk_x, ea_pk_y, dc_slot_nf, delegate_index_bound`. The voting-circuits `Instance` constructor is the only source of this order. The chain FFI must build instances through it, and a golden vector pins the packing.

**Constants and registrations.** All of these are frozen with pinned-value tests before rc.1, because changing them later breaks either a VK or recovery:
- `POOL_PROPOSAL_ID = 0`;
- the slot tag text with offsets 0..15;
- PRF domains 0x10-0x14 (0x14, the single-share El Gamal domain, stays reserved but unused);
- `DOMAIN_VC` documented as live for proposal-0 pool commitments.

**Invariant (normative in ZIP-PD).** `rand` is constant along every transition that keeps MAX authority. Any future VAN-transforming circuit must either preserve `rand` or carry a slot counter; otherwise C14's cap silently resets (CF-3). A cross-circuit test pins it.

**Measured costs** ([measured], loaded M3 Ultra): ZKP4 proves in 530, 278, 161 and 96 ms at 1, 2, 4 and 8 threads (within 2% of ZKP2), verifies in about 1.7 ms, and is 11,008 B (cap 15,360 B). Keygen peak RSS is about 140 MiB; ZKP4, ZKP2 and ZKP3 resident use 259 MiB, plus about 182 MiB for ZKP1. At 4 threads, ZKP1 + 10 ZKP4 + 50 ZKP2 takes 10.2 s, and "delegate everything" 1.9 s. [inference] Mid-range Android at 2.5-4x slower still meets Vizor's ≤2 s per proof target.

**ZKP3 reuse.** ZKP3 is unchanged. Real proofs [measured] for `(0, d)` at a non-zero tree position verify for `d = 4242` and `d = u32::MAX`. They are rejected for `d + 1` and for proposal 1. The ZKP1, ZKP2 and ZKP3 `vk_fingerprint_unchanged` tests are a release gate.

**ZKP2 gate.** ZKP2's `proposal_id ≠ 0` inverse gate (`authority_decrement.rs:398-416` [code]) is now **load-bearing**: without it, ZKP2 could emit `VC(0, d)` while keeping full weight. Re-document it, make `proposal_id_zero_fails` a non-ignored proxy soundness test, and require it on every ZKP2 rewrite.

**Builders and exports.** All APIs are ballot-denominated:
- `build_proxy_delegation_proof(..., delegate_index, ballots, dc_slot, next_delegate_index, dc_seed)`. The builder computes `bound = next_delegate_index − 1` internally, from IAVL-verified state read just before proving, and there is no free bound parameter (CF-1). The public bound then reveals only the registry height at proving time; a caller-chosen `bound = d` would have revealed the delegate.
- `derive_proxy_delegation_transition` (native, proof-free).
- `derive_proxy_share_secrets(dc_seed, ballots)`.
- Also exported: `proxy_delegation_commitment_hash`, `dc_slot_nullifier`, `dc_share_nullifiers`, `share_reveal::build_pool_share_reveal`, ballot variants of the ZKP2 builders, and `vk_fingerprints()` for ZKP1-4.
- Frozen vectors for every formula.

**Zero-weight successor.** A successor with `W_new = 0` ("delegate everything") can still produce zero-weight ZKP2 casts [measured]. By convention it is **dead**: clients never cast from it, and ZIP-PD says so (CF-2). The behavior is pinned with a test.

**Tests.**
- MockProver negatives for C1-C15. For C15: `d = 0`, `d = bound + 1`, `bound < d`, `d − 1 = 2^30`, `bound − d = 2^30`, `d = p − 1`.
- Nullifier parity with ZKP2.
- Chains: ZKP4→ZKP4→ZKP2 passes; ZKP2→ZKP4 fails; a further ZKP4 after `W_new = 0` fails.
- Replay of every public input.
- A ZKP4 proof under the ZKP2 VK fails.
- Witness independence (`Circuit::default()` gives the same row count).
- Row budget: re-measure after every change, because the 33-row headroom is shared.

### 4.2 Chain (vote-sdk v1.7.0)

#### Messages and tags

New tags are 0x09, 0x0B and 0x0C, and 0x04 gains a branch. Every new tag needs:
- an explicit `IsVoteTag`/`IsCustomTag` branch (the existing check is a contiguous range, `api/codec.go:57-59` [code]);
- canonical protobuf encoding;
- strict JSON at REST;
- fee-less handling with an infinite gas meter.

**0x09 `MsgVoteActionBatch`.**

```proto
message ProxyDelegationAction {
  bytes  van_nullifier = 1;               // 32
  bytes  r_vpk = 2;                       // 32, compressed, non-identity
  bytes  vote_authority_note_new = 3;     // 32, successor VAN (W-w, MAX)
  bytes  proxy_delegation_commitment = 4; // 32
  bytes  dc_slot_nullifier = 5;           // 32
  uint32 delegate_index_bound = 6;        // ZKP4 public input 11
  bytes  recovery_hint = 7;               // exactly 32, opaque
  bytes  proof = 8;                       // ZKP4, <= MaxProofSize
  bytes  vote_auth_sig = 9;               // 64, RedPallas under r_vpk over D1
}
message VoteAction { oneof action { ProxyDelegationAction proxy_delegation = 1; MsgCastVote cast = 2; } } // 3..9 reserved
message MsgVoteActionBatch {
  bytes vote_round_id = 1;
  uint64 vote_comm_tree_anchor_height = 2;   // 0 iff registration present
  MsgDelegateVote registration = 3;          // optional ZKP1
  repeated VoteAction actions = 4;
}
```

**ValidateBasic.**
- `1 ≤ len(actions) ≤ 50`; `1 ≤ #DC ≤ 10`; DCs strictly precede casts. Pure-cast lists keep using 0x06/0x07.
- A registration is present **if and only if** `anchor_height == 0` (SND-7). The registration and nested casts carry the batch round; casts also carry the batch anchor.
- `registration.ValidateBasic()`.
- Intra-message uniqueness:
  - `van_nullifier` across all actions;
  - `dc_slot_nullifier` across DCs;
  - cast `proposal_id`;
  - the set {DCs, VCs, final `van_new`}.

  These follow `msgs.go:180-205` [code], because `CheckNullifiersUnique` is store-only (`keeper_voting.go:54-66` [code]).
- `recovery_hint` is exactly 32 B, `bound ≥ 1`, and field sizes are checked.

**Ante.** Every step also runs on RecheckTx.
1. Dormant gate.
2. `ValidateRoundForVoting`, then `round.proxy_delegation.enabled`, then `!params.proxy_dc_paused`.
3. For each DC, `bound < NextDelegateIndex`. For each cast, `ValidateProposalId`. Then `EnsureCommitmentCapacity(1 + n)`.
4. Nullifiers: an explicit 0x09 case next to the 0x07 special case (`validate.go:123-128` [code]) that checks gov (0x00, only when a registration is present), VAN (0x01) and slot (0x03).
5. Every action signature over D1, all checked before any proof.
6. The registration's own verifier.
7. Proofs. `root0 = SingleLeafRoot(van_cmx)` if a registration is present, else `GetCommitmentRootAtHeight(anchor)`, which must be non-nil. Each later root is `SingleLeafRoot(prev.van_new)`.

**Handler.** Re-run every check (all three nullifier sets before any write); set nullifiers; append the final VAN, then each commitment in action order (never intermediate VANs or `van_cmx`); write one `ProxyActionRecord` per DC; emit `vote_action_batch{round, batch_digest, has_registration, final_van_leaf_index, action_kinds, commitment_leaf_indices, proposal_ids (0 for DCs), van_nullifiers, dc_count}`.

**D1 digest** (signed by each action's `r_vpk`):

```
BLAKE2b-256("SVOTE_VOTE_ACTION_BATCH_SIGHASH_V1" || lp8(chain_id) || write32(round)
  || writeU64As32(anchor_height) || u8 has_registration || [write32(van_cmx)]
  || writeU32As32(n) || for i: writeU32As32(i) || u8 kind
     kind 1 (DC):   write32(r_vpk) write32(van_nf) write32(van_new) write32(dc)
                    write32(dc_slot_nf) writeU32As32(bound) write32(recovery_hint)
     kind 2 (cast): write32(r_vpk) write32(van_nf) write32(van_new) write32(vc) writeU32As32(proposal_id))
```

`recovery_hint` must be bound here because no proof covers it. The registration keeps its own self-contained signature, as in today's 0x07. A relayer can therefore lift it out and submit it alone; the batch then fails on spent gov nullifiers, and the client re-plans against the real anchor (CF-5).

**0x0B `MsgDelegateRegistry`.** The oneof ops:

| Op | Signed by | Effect | Notes |
|---|---|---|---|
| `register` | DIK + DRK over `reg`; verifier threshold over `attest` | New ACTIVE entry, `key_epoch = 0` | `next_index ≤ min(max_delegates, 2^30)`; keys never reused |
| `rotate_route_key` | Current DIK + new DRK | Immediate; `key_epoch++`; FROZEN→ACTIVE | The only exit from FROZEN |
| `change_identity` | Current DIK + new DIK + new DRK | Pending, 72 h (consensus floor 72 h) | Cancellable by the DIK or the DRK |
| `recover_identity` | New DIK + new DRK + `recover_threshold` attestation | Pending, 7 d (consensus floor 7 d); stores the attesting `verifier_ids` | Cancellable **by the DIK only** |
| `cancel_pending` | DIK (any pending change) or DRK (co-signed changes only) | Pending cleared; its keys stay burned | |
| `freeze` | DIK or DRK | ACTIVE→FROZEN; routes rejected | **No unfreeze op** |
| `revoke` | DIK | REVOKED, terminal | |

**Effective view** (lazy, computed on read, no EndBlock work):
- A pending change takes effect at `effective_time`.
- A pending RECOVERY is **void** unless at least `recover_threshold` of its stored `verifier_ids` are still in the current verifier set (IDN-1).
- Applying a recovery sets `recovered_at_time`.
- `max_key_changes = 64` counts only changes that took effect (IDN-6).

**Validation and preimages.** As in the identity spec §3.3, re-expressed in the common digest convention:

```
BLAKE2b-256(domain || lp8(chain_id) || fields as write32 / writeU32As32 / writeU64As32)
```

with `key_epoch` as a u64. The domains are `SVOTE_PROXY_DELEGATE_{SUBJECT, REGISTER, ATTEST, ROTATE_ROUTE_KEY, CHANGE_IDENTITY, RECOVER_IDENTITY, PENDING_ID, CANCEL_PENDING, FREEZE, REVOKE, ROUTE}_V1`. A unit test asserts that every `SVOTE_` domain is prefix-free (24 domains were measured prefix-free in review [measured]).

**Ed25519 rule.** ZIP-215 verification everywhere: CometBFT `crypto/ed25519` in Go, and `ed25519-zebra` or `ed25519-consensus` in Rust. Keys must be canonical, not small-order, and free of torsion (`[ℓ]A = O`).

[measured] A key with an order-2 component passed the old key check. CometBFT accepted 2,000 of 2,000 of its signatures, but Go stdlib accepted only 1,029 (IDN-15).

**0x0C `MsgDelegateRoute`.** Fields: `{round, delegate_index, key_epoch, entries[(proposal_id, decision)] sorted and unique, drk_sig}`. The digest:

```
BLAKE2b-256("SVOTE_PROXY_DELEGATE_ROUTE_V1" || lp8(chain_id) || write32(round)
  || writeU32As32(d) || writeU64As32(key_epoch) || writeU32As32(n)
  || n × (writeU32As32(p) || writeU32As32(decision)))
```

Accepted when: the round is ACTIVE, enabled, and `blockTime < vote_end_time`; the entry is effectively ACTIVE and not suspended; `key_epoch` matches; `round.created_at_time ≥ recovered_at_time` if that is set (IDN-1); every `p` is in `round.Proposals`; and every decision is `< num_options(p)` or `0xFFFFFFFF` (explicit abstain, recorded, not counted). An entry identical to a stored route is a no-op; a conflicting one rejects the tx (`ErrDelegateRouteExists`). Routes already made survive a later freeze, suspension or revocation.

**0x04 `MsgRevealShare` with `proposal_id = 0`.** Accepted if and only if `round.proxy_delegation.enabled` and `1 ≤ d < NextDelegateIndex`; delegate status is ignored. Effects: `AddToTally(round, 0, d)`, `IncrementShareCount(round, 0, d)`, and a `reveal_pool_share{round, delegate_index, share_nf}` event (never `reveal_share`). It counts toward the 256-per-block cap. Errors `ErrProxyDelegationDisabled` and `ErrDelegateNotFound` are permanent.

**Coordinator payloads.**
- `MsgSetProxyDelegationParams`: `enable_for_new_rounds`, `proxy_dc_paused`, `registration_enabled`, change delay (≥72 h) and recovery delay (≥7 d) as consensus floors, `max_attestation_validity` (≤72 h), `max_key_changes`, `max_delegates` (≤2^30; launch 20,000).
- `MsgSetProxyDelegateVerifiers`: 1..16 verifiers, unique ids and pubkeys, two thresholds, and a stored `added_at_time`. A verifier may attest only after `added_at_time + recovery_delay`, except in the activation set (IDN-3).
- `MsgSetDelegateSuspension`: immediate, freeze-only, public reason code (1 impersonation, 2 key compromise, 3 legal, 4 verifier review, 99 other). It cannot create entries, change keys or void routes.

#### State (normative key table)

| Key | Value |
|---|---|
| `0x1A 01 u32be(d)` | `ProxyDelegateEntry` {index, DIK, DRK, key_epoch u64, status ACTIVE/FROZEN/REVOKED, suspended + reason, provider, subject_commit, pending {kind, keys, effective_time, verifier_ids}, recovered_at_time, key_change_count, heights} |
| `0x1A 02 pk[32]` | `u32be(d) u8 role`: every DIK or DRK ever submitted, including burned keys |
| `0x1A 03 attestation_id[32]` | `u32be(d)` (single use) |
| `0x1A 04` | `next_index` (genesis 1; index 0 never assigned) |
| `0x1A 05` | verifier set with `added_at_time` |
| `0x1A 06` | `ProxyDelegationParams` (the only params key) |
| `0x1A 07 u64be(seq)` | reserved: Phase 2 directory anchor |
| `0x1A 08 u64be(height) u32be(d)` | registry change index (derived) |
| `0x1A 09 u32be(d)` | entries with a pending change (derived) |
| `0x1B round u32be(d)` | `DelegateRouteSet` |
| `0x1C round u32be(p) u32be(o)` | routing base (pre-routing accumulator, for audit) |
| `0x1D round van_nf[32]` | `ProxyActionRecord` {dc, dc_leaf_index, dc_slot_nf, recovery_hint, final_van_leaf_index, height} (about 150 B) |
| `0x1E round u64be(height) u32be(d)` | route change index (derived) |
| `TallyKey(round,0,d)` | `Pool[d]` |
| `ShareCountKey(round,0,d)` | pool share count |
| `NullifierKey(0x03, round, nf)` | DC slot nullifiers (`NullifierTypeProxySlot = 0x03`) |
| `VoteRound` field 31 | `proxy_delegation {enabled, next_delegate_index_at_creation, pools_routed, routed_pool_count, applied_route_count}` (outside the round-id preimage) |

**Route absence.** A route is absent when `p` is missing from an IAVL-proven `0x1B` set, or when the whole key is proven a non-member, at a height at or after the tally transition. It is never proven through a per-proposal key (OPS-8). A golden key-derivation vector is shared by `keys.go` tests and Vizor's reader.

**Genesis.** One normative GenesisState section covers:
- nullifier types 0-3: `ValidateGenesisState` must accept type 3, because `genesis.go:94-97` rejects anything above 2 today [code];
- every 0x1A key;
- routes, routing bases, proxy-action records and params.

Derived indexes are rebuilt on import. `TestExportImportGenesis` is extended with one finished and one active proxy round.

**Reset rule.** Any chain reset that does not export and import the registry **must change `chain_id`**. That kills replay of old registry and route signatures.

#### Queries, REST and capabilities

- `delegates?start_after=&limit≤1000&updated_since_height=` (backed by 0x1A08, always including 0x1A09 pending entries). `delegates/{index}` and `delegates/by-key/{hex}` serve delegate tooling and explorers, **not** the delegator path.
- `delegate-routes/{round}` (key-paged, `updated_since_height` via 0x1E); `delegate-pools/{round}` (share counts only); `nullifier/{round}/{type 0..3}/{nf}`; `proxy-actions/{round}` and `/{van_nf}` (IAVL-provable; `tx_hash` optional); params; verifiers.
- `ProposalTally` rejects `proposal_id = 0` and ids above `len(proposals)` (OPS-12).
- `ProtocolCapabilities` appends fields once:

  | Field | Value |
  |---|---|
  | 4 | `proxy_delegation` (bool) |
  | 5-7 | wire tags 0x09, 0x0B, 0x0C |
  | 8 | `max_dc_actions_per_tx = 10` |
  | 9 | `max_vote_actions_per_tx = 50` |
  | 10 | `max_vote_tx_bytes` |
  | 11 | `proxy_delegation_protocol_version = 1` |
  | 12 | `repeated CircuitFingerprint{name, vk_blake2b}` for ZKP1-4 |
  | 13 | `max_dc_slots = 16` |

  The identity work claims no field numbers of its own (CMP-12).

#### Routing algorithm (EndBlock, in the iteration that sets ACTIVE→TALLYING, before `SetVoteRound`)

```
if !enabled or pools_routed: return
for each route set under 0x1B||round in key order (d ascending):
    pool := GetTally(round,0,d); if nil: continue        // no reveals; a decode failure is state corruption -> halt
    for (p, o) in entries ascending: if o == ABSTAIN: continue; sums[(p,o)] ⊕= pool
for (p,o) in sums ascending:
    base := GetTally(round,p,o); combined := base ⊕ sums[(p,o)]; if base != nil: Set(0x1C.., base)
    rho := HashToScalarPallas("svote-route-rerand-v1" || round || u32be(p) || u32be(o) || Marshal(combined))
    final := combined ⊕ (rho·G, rho·ea_pk)   // retry with rho := H(rho) while C1 or C2 is identity (≤8 tries)
    Set(TallyKey(round,p,o), final)
set pools_routed, counts; emit proxy_pools_routed
```

**Why this is safe.** Pools are final: the transition block admits no reveals or routes (`keeper_voting.go:336-338`, `msgs.go:554-557` [code]). ρ (deterministic; curvey `expand_message_xmd` with BLAKE2b) blocks a forced identity C1 that would fail every DLEQ and time out the round [measured in prototype]. Buckets stay under 1.68·10^8 ballots, below `TallyBSGSBound = 2^28`. Proposal 0 is already excluded from partial decryption, completeness and `ValidateEntryBounds` [code]; tests lock it in.

**Cost.** [measured] 10k delegates × 50 routes took 1.39 s in memory on an M3 Ultra. Benchmark the real IAVL-backed hook on the 2-4 vCPU validator droplets in CI. Launch `max_delegates` is 20,000, raised only with benchmark evidence. Fallback: spread routing over the first K TALLYING blocks, gating partial-decrypt injection on `pools_routed` (OPS-13).

#### Limits, spam and per-block rules

- Slot cap: 16 DCs per registration per round. Per attacker proof this gives 16 reveals, the same as a cast.
- 0x09 caps at 10 DCs and 50 actions. [measured] ZKP1 + 50 casts is 576,769 B raw and 769,100 B as RPC JSON; ZKP1 + 10 DCs + 40 casts is about 577.5 KB raw and 770 KB as RPC JSON. That is about 23% headroom under the 1 MiB REST and 1,000,000 B Comet limits.
- Prepare/ProcessProposal dedupe: at most one registry op per `delegate_index` per block (one per new DIK for `register`), and one route tx per `(round, d)` per block. The global registry-op cap (64) stays as a backstop.
- No per-block proof-verification cap in v1 (decision 24). If one is ever added, it is weight-based, at least 51 proofs per tx, and FIFO across tags.
- The tree-capacity guard returns `ErrCommitmentTreeFull` inside `AppendCommitment`, and `MaxTreePosition` is fixed to 2^24−1.

#### Dormant merges and activation

A `ProxyDelegationEnabled = false` const gates tag decode, interface **and Msg-service** registration, the p=0 branch, routing, new payloads, capability fields and any change to existing validation. It is checked **before any new KV read**, so existing paths keep identical GasUsed (OPS-6). Proxy PRs are labeled `V:state/breaking`, never backported to v1.6.x, and ship only in v1.7.0. The activation commit flips the const and registers a no-op `v1_7_0` handler.

### 4.3 Helper (vote-sdk `internal/helper`)

**Proposal-0 branch.** `validatePayload` accepts `proposal_id = 0` with a u32 `vote_decision ≥ 1` and skips the `< 8` option check. For p=0, `verifyCommitment` runs **first** (the leaf at `tree_position` must equal the recomputed DC hash; an absent leaf returns a retryable 503). Only then is `1 ≤ d < NextDelegateIndex` checked, with a permanent distinct error, which `bound` makes unreachable once the leaf exists (LIV-9, OPS-11). Scheduling, store, retries and prover are unchanged; under V2, p=0 routes to the scalar ZKP3 prover.

**Telemetry hygiene** (PRV-6; also fixes votes today). Remove `proposal_id`, `tree_position` and `submit_at` from span data and never add `vote_decision` or `d`; name HTTP transactions by `mux` route template, not raw path; add a test that fails if span data contains these keys.

**Capacity** (LIV-1, medium). Each DC adds 16 helper proofs to the FIFO shared with votes, and delegators who would not otherwise vote are pure added load. [measured] A queue model (10 operators × 2 workers × 0.58 shares/s = 11.6/s) loses about 20% of both DC and Zodl direct-vote weight at 5k delegators × 3 with deadline-heavy arrivals and 2× duplicate proving. 4 workers per operator remove the loss even at 50k × 3 (dup = 1). The same model also predicts loss for direct votes alone at 5k × 37 proposals, so its inputs may be pessimistic; capacity must be measured, not assumed. Required:
1. Replace the "reduces load" claim with a budget: added reveals = 16 × DCs from delegators who would not otherwise vote.
2. Metrics: proofs per unique reveal; queue depth by class (bounded labels).
3. Raise workers to the measured headroom before the first public proxy round.
4. Release gate: in the mixed load scenario, Zodl direct-vote reveal loss and latency must not regress against the direct-only baseline.

A deterministic primary helper per share is an optional, separately tracked improvement.

### 4.4 Client library (zcash_voting)

**Modules.** `proxy::{planner, batch, secrets, recovery, registry, verify, gating, delegate}`, plus the shared `zcash_vote_delegate` crate (digests, marker parser, phrase, fingerprint, skeletons; cross-language vectors). A dormant `PROXY_DELEGATION_ENABLED` const.

**Allocation and planner** (`PROXY_PLANNER_VERSION = 1`).
- **Lifecycle.** One durable allocation per `(round, wallet)`: preview → commit (with `plan_digest`) → locked. Up to 10 entries `(d, dk, bps, display_handle)`. Remainder is `KeepForSelf` or `DelegateEverything`.
- **Rounding.** Hamilton largest remainder in u128. Minimum 1 ballot per delegate, funded by Keep or the largest delegate. A round may raise the floor through `min_ballots_per_delegate` in the signed round extension.
- **Packing.** An exact split-free search when B ≤ 4, then best-fit decreasing, then greedy splitting. The objective is fewest DCs, then fewest bundles touched. The bound is `D ≤ #DC ≤ D + B − 1` [measured over about 12k cases].
- **Slots.** A slot is `(bundle, slot_index 0..9)` and also carries `dc_slot` (0..15) for C14. `dc_slot` equals the DC's position in that bundle's action list, and positions follow a **CSPRNG shuffle** taken at commit so action order does not reveal size ranking (PRV-9). Slot positions and nullifiers are persisted before proving and reused on retry.
- **No top-ups in v1.** Delegation is available only until commit. The allocation then locks, which closes "Delegate more" (LIV-5).

**Batch composition.**
- Per bundle: `[ZKP1?] → DC… → casts`. Casts ride along only when the remainder is kept and the roster is terminal; otherwise they follow later as an ordinary 0x06 on the remainder VAN.
- The packer uses `max_vote_tx_bytes` from capabilities (default budget 700 KB) and the 50-action total. It never splits a bundle's DCs.
- A `W_new = 0` successor never gets casts (CF-2).
- Proving runs in phases: ZKP1, then all ZKP4s, then ZKP2s. Each proving key is loaded for its phase and evicted before the next. ZKP4 is never pre-warmed at app start, and a peak-RSS budget is part of the device benchmark gate (CF-4).
- **Cutoff.** No new proxy batch is planned or broadcast once `now ≥ vote_end − max(last_moment_buffer, 30 min)`. The buffer is `min(40% of round, 6 h)` (`share_policy/timing.rs:20-45` [code]). "Now" is derived from the latest chain block time, with a warning on more than 60 s of device skew. Only the standard 16-share layout exists.

**Secrets, ARK and recovery hint** (PRV-1).
- **Software accounts:**

  ```
  ARK = BLAKE2b-256(key = PRF^expand(sk_orchard, [t_ARK]), personal "ZVoteProxyARK_v2", net_u8 ‖ round_id)
  ```

  `t_ARK` is a new domain byte registered in the voting ZIP; crypto review confirms it collides with no Orchard or ZIP-32 use.
- **Hardware accounts and imported capabilities:** `ARK = BLAKE2b-256(key = hotkey_secret, personal "ZVoteProxyHRK_v1", net_u8 ‖ round_id)`.
- The ARK is **never derived from the OVK or FVK**, so UFVK holders cannot read delegations. Vizor derives it at commit and stores it in app secure storage keyed by `(account, round)` next to the hotkey.
- Per-action secrets, where `layout` is fixed to 0 and `slot` is `dc_slot`:

  ```
  hint_key = BLAKE2b-256(key=ARK, personal "ZVoteProxyHint01", van_nf)
  dc_seed  = BLAKE2b-256(key=ARK, personal "ZVoteProxySeed01", van_nf ‖ d_le32 ‖ w_le64 ‖ 0x00)
  recovery_hint = ChaCha20Poly1305(hint_key, nonce 0^12, aad "ZVoteProxyHint01" ‖ round,
                                   0x01 ‖ 0x00 ‖ slot_u8 ‖ 0x00 ‖ d_le32 ‖ w_le64)
  ```
- **Invariant:** `(d, w, slot)` is frozen per `van_nf` once a tx carrying the hint has been POSTed. A zero nonce is then safe, because only one plaintext ever exists per key (SND-8).

**Persistence (schema v25, one-way).** New tables for allocations and entries, slots (with `dc_slot` and `dc_slot_nf`), DC share plans and deliveries, the recovery cursor, the sticky proof-key set, and delegate identity metadata; two new `chain_submissions` kinds; `ShareKey` gains `CommitmentRef::{Vote, Proxy}`. Effective VAN weight, `floor(total_note_value / 12,500,000) − Σ dispatched or confirmed slot ballots`, replaces `total_note_value` at every VAN-recompute site (`zkp2.rs`, `vote.rs`, `load_van_tree_entries`, tree sync and others). Account deletion removes all proxy tables.

**NextStep and planner obligations.**
- New kinds `ProxyDelegate`, `AdvanceProxyBatch`, `SubmitProxyShares`, `ConfirmProxyShare`, with exhaustive matches. Proxy-only rounds plan ZKP1 with zero ballot intents. A bundle with a proxy batch in flight is held from casts, and vice versa.
- **Release path (LIV-3).** `release_proxy_bundle(bundle)` is allowed when the bundle is `ProxyBlocked{DelegateChanged}` without continuity, or `ChainTerminal`, and none of its batches has a reserved POST or landed tx. It drops those slots, turns their weight into Keep, lifts the hold and records a new `plan_digest` generation; landed slots stay final. To narrow the window, prove every bundle's batch before broadcasting any.
- **Split-off.** "Registration landed, batch rejected" is a normal transition: re-plan the same DCs and casts against the real anchor (CF-5).

**DC shares.** The existing `VoteShareWire` carries `proposal_id = 0`, `vote_decision = d`, all 16 shares and the standard `submit_at` schedule. **No DC share is ever the immediate share**, so proxy-only rounds have none (PRV-3). "Handed off" means definite acceptance of every DC share by its target helpers, reported separately from reveal progress (LIV-2).

**Verification ("did my delegate vote").** Per delegate: ballots, inclusion, shares k/16. Per proposal: `Voted(o) | Abstained | NotYet | DidNotVote`. Before TALLYING, share progress comes from helper share-status, and routes from the whole-round paged list (no per-delegate query). **Sticky proof set (PRV-5, CMP-19):** at commit the client fixes and persists a 10-key set (chosen delegates plus random decoys; share-nullifier decoys come from other pools' public reveal nullifiers) and proves exactly that set at commit, pre-prove, status and final verification, so intersecting checks gains nothing. Share-nullifier IAVL proofs run once after TALLYING. Docs say plainly that helpers and the RPC operator learn the chosen delegates unless Tor is on.

**Recovery.**
- **R0:** the DB is present.
- **R1** (DB lost, hotkey kept; the hotkey supplies the VAN `nk`): derive `van_nf(VAN_0)`, look it up in `0x1D` (round-prefix paging by default, point lookups over Tor), decrypt the hint with the ARK, and follow the VAN chain. This recovers every slot and the remainder VAN, so remainder voting resumes.
- **R2** (software accounts after seed restore): the seed-derived ARK trial-decrypts the paged `0x1D` feed (about 1 µs per open). On a hit, rebuild the DC from `dc_seed`, **require it to equal the on-chain DC**, then rebuild payloads and redeliver missing shares. Remainder voting stays lost, as today.

Hardware accounts get R1 only (Q4). Recovery is idempotent; recovered rows are locked.

**Registry client.** Parse the static `proxy_delegation` section; verify the directory index (online/offline key certificate, `seq` monotonicity, expiry, cross-mirror equivocation); mirror `/delegates` (paged, `updated_since_height`). Overlay checks: chain DIK and status match; `subject_commit = H(provider, id, salt)`; Featured needs a valid curator-threshold signature; handles are pinned per favourite and past delegation, with a warning on change. Refuse delegates that are not ACTIVE, are suspended, have a pending **recovery**, have `accepting = false`, or have a removed proof; allow a co-signed pending change with a banner (LIV-10). `proxyAvailability` also requires proven non-membership of the account's gov nullifiers; if they are spent and no local state exists, report "used from another device" and offer R2 (LIV-8).

**Gating.** No `vote_protocol` or `auth_version` bump. Requires `WalletCapabilities.proxy_delegation = ["v1"]`, a dynamic `extensions.proxy_delegation_v1.rounds[id]` entry signed by `trusted_keys` under `zcash-shielded-vote:round-proxy:v1` over binary `RoundProxyAuthPayloadV1`, and chain capabilities whose four circuit fingerprints match the compiled ones. Gates control only preview and commit of **new** allocations; committed work, share delivery and recovery always run (LIV-12). Vizor calls `proxy::availability` and never parses these fields.

**Delegate APIs.** Phrase create and restore (§4.6); DIK/DRK derivation and restore by scanning `i < 8`, `j < 64` against `by-key`; marker text, statement and resolve/attest client; encoders for every digest; route preflight (re-fetch the route set, submit only the unrouted delta), signing and submission; rotate, freeze, revoke, cancel, change identity; the off-chain DIK-signed statement `{accepting, text ≤ 280 chars, no links, consent line}`.

### 4.5 Vizor

**Screens.** Keep the PD-1..PD-13 (delegator) and DG-1..DG-11 (delegate) inventory, with changes: PD-1 drops "Delegate more"; PD-4 shows the cutoff time; PD-5/PD-10 have the finality checkbox, an amount-naming CTA, the list of covered proposals and an optional small-pool notice (delegate has ≤16 pool reveals); PD-6 completes at "Handed off"; PD-7 adds a pending-recovery banner and an "on track" state for slow shares; DG-2 becomes phrase create/confirm; DG-12 (software account required) is removed; DG-9 drops the side-by-side private-vote column; DG-11 gains "Stop accepting" (a statement) and "Emergency pause" (freeze).

**Job.**
- `VotingSubmissionJobNotifier` gains `kind {ballot, proxy, proxyAndBallot}`, `ProxyJobStage` and `terminalReason`.
- The proxy allocation is recorded before the hardware/software branch, so Keystone and Ledger users who only delegate are still asked to sign ZKP1.
- Cancel is allowed only before the first broadcast.
- COMPLETE requires definite helper acceptance of every DC share.
- Hold a wakelock from proving through hand-off (optional polish).
- On startup or poll open, run R2 for software accounts whose gov nullifiers are spent in a proxy-enabled ACTIVE round with no local state, then redeliver.

**Delegate mode.**
- The phrase root sits in app-level secure storage behind re-auth.
- The DRK is stored with **per-use user presence** (Keychain/Keystore access control), not session unlock (IDN-7).
- Routes need re-auth plus a confirm sheet naming the choice.
- Tor is **default-on** in delegate mode for route submission and for the delegate account's own vote and share traffic. If Tor is off, a blocking prompt appears before the first route or private cast (PRV-7).
- The dashboard shows a blocking banner when any recovery is pending on the user's entry.
- The deadline comes from chain time with a 5-minute margin.
- **Isolation warning** (optional, PRV-2): if public `BallotCount` and route lists show that option `o` has no direct shares and no other router so far, warn "No one has voted this option directly yet. If that stays true, your pool total will be public."

**Images and networking.**
- WebP only, at exactly 64 or 256 px.
- The sha256 is checked against the profiles doc, the image is decoded in Rust with `image-webp`, and drawn with `decodeImageFromPixels`.
- `Image.network`, `Image.memory` with encoded bytes, and `instantiateImageCodec` on network bytes are banned by a grep test (CMP-14).
- Directory, verifier and mirror hosts go in the Tor-aware route table.
- Avatars always come from packs: listed 64-px packs are sharded by index range, and 256-px images only through prefix-shard packs.

**Deep links.**
- Format: `https://link.vizor.cash/d#v1.<index>.<fp>`. The fingerprint is mandatory.
- On mismatch, Delegate is blocked until the user reviews both keys.
- There is a desktop paste fallback, and the server has one exact `/d` route.

**Directory freshness.**
- Fail closed for directory-only blocking flags (removed proof, impersonation, lookalike, "under review").
- When a refresh fails, accept a verified cached snapshot up to 1 h old, with an "out of date" banner, but only for listed, unflagged entries registered before the round (LIV-6).

**Copy rules** (sentence case, no em dashes). These replace the v1 strings:

| Key | New string |
|---|---|
| C4 | "Your delegate can't see who you are. Vizor never shows how much a delegate holds, and the voting chain never decrypts it. If very few people delegate to the same person, the total, and so your amount, can sometimes be worked out from the final results." |
| C7 | "Helpers add your delegation to the count at random times before voting ends. Vote servers can see which delegate you chose, but not who you are." |
| C8 | "If you reinstall Vizor before your delegation finishes sending, open Vizor again before voting ends so it can finish." Add for software accounts: "After restoring from your recovery phrase, Vizor can find your delegations." |
| C9 | "Your delegate votes for you on every proposal in this round. Delegate some or all of your voting power to people you trust." |
| Cutoff | "Delegation closes {time}, before voting ends, so helpers can add it to the count privately." |
| DG-1 additions | "Your public votes stay linked to your X account permanently, even if you delete your post or account." / "We don't publish how much was delegated to you, but it can sometimes be worked out from public results." / "Your private votes are hidden from the public. Vote servers can link them to you unless Tor is on." |
| Stop accepting | "Vizor will stop offering you as a delegate. People who already delegated stay with you, and you can still vote publicly." |
| Emergency pause | "Pausing blocks your public votes until you set a new route key with your identity key. Proposals you haven't voted on will count as abstain for everyone who delegated to you." |
| Kept ZEC | "Vote with the ZEC you kept from this device before voting ends." |

Other copy fixes:
- The ZKP1 copy becomes "voting authorization" everywhere (including the Ledger strings).
- Delegation counts appear only on the delegate's own dashboard, marked approximate.

**UGC (IDN-13, CMP-22).**
- An in-app report sheet POSTs through `NetworkHttpClient` to the verifier's `/v1/reports`, with no account id. A local "Hide delegate" option.
- Display name and statement are shown only when the profiles doc marks `text_state = approved`.
- Delegate onboarding requires accepting the content terms.
- App Privacy labels; reviewer notes with a stage build, a demo delegate and an open test round.

### 4.6 Identity, verifier and directory

**Delegate key phrase (O4, IDN-14).**
- Format: 24 words from the BIP-39 English list, encoding 256-bit entropy `E` plus a check byte `c = BLAKE2b-256(personal "ZcashVoteDlgPh01", E)[0]`.
- Generation retries until the standard BIP-39 checksum differs from `c`, so **no delegate phrase is a valid BIP-39 mnemonic**.
- Restore rejects any valid BIP-39 mnemonic with "This looks like a wallet recovery phrase. Never enter it here."
- Derivation:

  ```
  seed      = BLAKE2b-512(personal "ZcashVoteDlgSd01", E)
  dik_sk(i) = BLAKE2b-256(key = seed, personal "ZcashVoteDIK_v1_", u32le(i))
  drk_sk(i,j) = BLAKE2b-256(key = seed, personal "ZcashVoteDRK_v1_", u32le(i) ‖ u32le(j))
  ```
- The format goes into audit tranche 2.
- A CLI with bring-your-own-key comes in Phase 2.

**Keys and display.**
- Public keys are bech32m `zvdk1…` (DIK) and `zvdr1…` (DRK).
- **Fingerprint** = the first 16 data characters of the DIK's `zvdk1` string (80 bits), grouped `xxxx-xxxx-xxxx-xxxx`. It can be compared by eye with the X post.
- Canonical display: `#17 · qqqs-yqcy-q5rq-wzqf`.
- The identicon is decoration, not verification.

**Proofs.**
- The marker line is `zcash-vote-delegate v1 zvdk1…`.
- **X:** fetched with the official API. Request `referenced_tweets`, and require an original post (no reply, quote or repost) whose whole text equals the template modulo whitespace (IDN-5). Show the full post text on the resolve screen. Bind the numeric user id. oEmbed is used only to re-verify existing entries whose handle matches the last API-confirmed handle.
- **GitHub:** a gist file; bind `owner.id`.
- **DNS:** TXT or `.well-known`, with two DoH resolvers. Uniqueness and rate limits key on **eTLD+1** (IDN-10).
- The subject is committed on chain only as a salted `subject_commit`.

**Attestation.**
- Client-random single-use `attestation_id`; `not_before` and `expires_at` (≤72 h).
- A threshold of distinct current verifiers, deduplicated by `verifier_id` and pubkey. No admin bypass.
- Launch: register threshold 1 (Valar online key), recover threshold 2 (Valar online key plus a second Valar key behind two-person manual approval).

**Attest-signer hardening (IDN-2).**
- The signer receives structured fields and **recomputes** `attest` itself.
- For RECOVER, and for REGISTER of subjects above a follower threshold, it re-fetches the proof with **its own** X credentials before signing.
- It enforces its own per-kind budgets (RECOVER far below REGISTER), a per-index cooldown and a featured-entry refusal, using its own chain view.
- An **independent re-verifier** (separate credentials, read-only DB role) re-checks every on-chain registration and recovery within 24 h, and pages on any failure or on a RECOVER spike.
- The publisher reads a read-only replica.
- Signer-log reconciliation remains, for key theft only.

**Verifier policy.**
- After an owner cancels a recovery, the verifier refuses RECOVER for that subject for 30 d.
- After manual review, re-registration is allowed for a subject whose entry is suspended for key compromise (IDN-6).
- Per-provider quotas, so DNS and GitHub cannot starve X.
- Unused issuances count against the IP and its /24.

**Directory documents.**
- **index.json:** seq, hash chain, document hashes, and an online-key signature with an offline-key certificate.
- **Registry doc** (ID-level, open to other wallets): index, DIK, provider, subject id, salt, flags, listing state, and a curator-signed Featured list and log.
- **Profiles doc** (Vizor-only until counsel clears redistribution): handle, name, statement, `text_state`, avatar hashes. `expires_at ≤ issued + 24 h − purge SLA` (PRV-8).
- **Avatar packs:** content-addressed and sharded by index range.
- Served from three mirrors, with wallet equivocation checks.
- No per-round pinning; the chain overlay plus the hash chain is the consistency anchor (CMP-13).

**Retention and deletion** (PRV-8, IDN-12).
- On a purge, delete or rewrite every historical registry, profiles and pack object containing the subject. Keep only the current documents plus a short overlap, and purge the CDN and every mirror.
- Keep a keyed-hash tombstone of `(provider, subject_id)` for uniqueness checks.
- The delegate's wallet keeps its own salt, so a later recovery can still prove the subject.
- The "crypto-shredding makes the commit unlinkable" claim is withdrawn: published salts mean attribution is permanent, and DG-1 says so.

**Curation.**
- **Browse listing** requires an account at least 180 d old with at least 100 followers, registered at least 24 h. Exact lookup always works.
- **Featured:** at most 30 entries, curator 2-of-3 (Valar and Vizor keys pinned in the static config), and a public log. Ecosystem-role entries may skip the 30-day wait after out-of-band fingerprint confirmation (IDN-11).
- **Lookalike detection** uses UTS #39 skeletons:
  - precedence goes to whichever `(skeleton, subject)` pair the directory saw first, not to registration order;
  - the check is re-run on every handle change, flagging the entry that renamed;
  - skeletons are reserved for 12 months, shown as "handle previously used by #N";
  - a seeded list of reserved ecosystem names.
- **Successor links** ("re-registered as #N") come only from a DIK-signed statement or after curator review.
- **Reports:** Valar support, with a 24 h target for impersonation.

**Ops.** valargroup DigitalOcean hosting; signer keys in KMS or an HSM on a dedicated host (no YubiHSM on DO); monitoring of X API errors and spend, purge SLA, mirror divergence and the re-verifier; playbooks for verifier-key compromise (removing the key now voids its pending recoveries), X outage (GitHub and DNS continue), and directory-key compromise (offline-key revocation).

**Legal (CMP-21).** Before the X provider reaches production, counsel signs off on the X Developer Agreement in force at launch (attestation use, profile redistribution, political-data rule), privacy notices and retention, the CSAM reporting duty, the Featured disclaimer and vote-market policy.

### 4.7 Config repo and deeplink server

**Config repo.**
- A **new static pin** at a new URL with a `proxy_delegation` section: `{network, chain_id, directory_urls[3], directory_keys {online[], offline[]}, curator_keys[3] + threshold 2, verifier_api_urls}`. Old pins stay immutable (Zodl).
- **Dynamic config:** `extensions.proxy_delegation_v1 = {version, rounds: {round_id: {enabled, min_ballots_per_delegate, signatures}}}`, signed by `trusted_keys` under `zcash-shielded-vote:round-proxy:v1`.
- `supported_versions` is never touched, and there is no `registry_snapshot_sha256`.
- CI checks signature and shape.

**Deeplink server.** One exact `/d` route with a generic page, a static OG image, the payload only in the fragment, and no server-side directory data. A privacy review is required by the server's charter.

---

## 5. Privacy and threat model

**Hidden** (with these exact claims):

| Property | From | Caveats |
|---|---|---|
| Delegator identity | Everyone | VAN anonymity, as for votes. Timing and IP linkage are the same as voting unless Tor is on. |
| Which delegates a delegator chose | The public, the EA and the delegate, at DC time | **Not hidden from helpers**: each share fans out to `ceil(N/2)` vote servers, about every server, which learn `d`, the DC leaf and IP. The DC count per tx is public. For delegates with very few delegators, the 16 reveal times can narrow which DC tx it was: [measured] median candidate sets of 4, 8, 29 and 112 DC txs at 0.1, 0.25, 1 and 4 DC txs per hour, unique in 17%, 5%, 1% and 0% of trials. |
| Amount per delegation | The public, helpers and the delegate | A ≥t EA coalition decrypts share plaintexts. For a delegate with a single delegator, summing that pool's 16 shares gives that delegation's exact amount. |
| Delegator's choices from viewing-key holders | UFVK holders | The ARK is never derived from the FVK. |

**Public:** routes; that an anonymous batch contains k DCs; per-delegate delegation counts (pool reveals ÷ 16, live during the round); delegate registry entries; per-option direct share counts (VoteSummary `BallotCount`).

**Decision 4, normative wording for ZIP-PD, the book and the FAQ:**

> The protocol never decrypts an individual pool; only per-(proposal, option) totals are decrypted. Pool totals are therefore not published, but they are not guaranteed secret.
> (i) Any coalition holding at least t election-authority key shares can decrypt any pool and every pool share at any time. Because every share carries the public delegate index, this reveals individual delegation amounts for delegates with one delegator and narrow ranges for delegates with few.
> (ii) Per-option totals, routes and per-option direct share counts are public and exact. Anyone can compute a pool total whenever the published totals determine it, for example when a delegate is the only router of an option that received no direct shares. A delegate can cause this deliberately.
> (iii) Pool totals can be estimated by comparing turnout across proposals when a delegate leaves proposals unrouted or abstains.
> (iv) The number of delegations each delegate receives is public. If a pool total becomes known and the pool has one delegator, that delegator's amount is known.
> Delegations do not have the same privacy as direct votes.

**Quantified** [measured]: with sentinel abstain and Yes/No proposals, 0% of pools are exactly solvable. An explicit Abstain option makes 42-44% solvable at 15 proposals, 30 direct voters and 5 delegates, and 1-3% at 200 voters. A 1%-direct minor option gives 15% at 30 voters and about 0 at 200 or more. The risk concentrates in small and beta rounds.

**Integrity.** A VAN is either cast from or delegated from, never both (one nullifier set). No `VC(0, d)` can exist (the ZKP2 gate, and the chain rejecting casts with p < 1). Weight is conserved over the integers, each share is added once, and each pool lands in at most one option per proposal. **Accountability:** routes are signed, immutable and applied to the whole pool; a delegate cannot drop individual delegators. Not covered: a delegate's private vote may contradict its route, pool size is unverifiable, and the X binding is trusted to the verifier.

**Identity attacks and defenses.**

| Attack | Defense |
|---|---|
| X takeover recovery | 7 d delay; DIK-only cancel; recovered keys cannot route rounds that already existed; pending recovery blocks new delegations; banners; a removed verifier voids its pending recoveries |
| Verifier API compromise | The signer re-verifies; independent re-verifier pages |
| Directory key theft | Featured needs curator signatures; wallets pin handles; equivocation checks; offline revocation |
| DRK theft | Per-use presence; DIK rotation; can route this round's unrouted proposals once (accepted) |
| Coordinator key | Consensus delay floors; verifier warm-up; suspension is public. Coordinators remain fully trusted, as they already are via x/upgrade. |

**Not provided:** receipt-freeness (a delegator can prove its DC opening; pools are vote-market aggregation points, cf. LobbyFi), and per-delegate censorship resistance (reveals and routes tagged `d` are attributable; helper redundancy and multiple vote servers mitigate).

**Delegates' own privacy.** Vote servers can link a delegate's private votes to their public routes unless Tor is on, so delegate mode defaults Tor on. After private-vote V2 the residual is participation linkage only; V2 makes direct votes helper-private, but DC choices stay visible to helpers.

---

## 6. Hole register

Severity is after skeptic review. "Unverified" means not re-checked by a skeptic; these are adopted where cheap. Grouped rows share one fix.

| ID(s) | Sev. | Issue | Resolution |
|---|---|---|---|
| IDN-1 | high | Takeover or rogue-verifier recovery captures pools mid-round; removing a verifier doesn't stop it | `recovered_at_time` bars older rounds; pending recovery voided below threshold; DIK-only cancel; banners |
| IDN-2 | high | Signer signs opaque digests; alarm reads API-written table | Signer recomputes and re-fetches; own budgets; independent re-verifier |
| IDN-4 | high | One online directory key controls handle, avatar, Featured | Curator-signed Featured; handle pinning; independent auditor preferred |
| CMP-1, OPS-1, IDN-6(b), LIV-10(b) | high | DRK unfreeze defeats freeze; freezes replayable | No unfreeze; exit FROZEN only by DIK rotation; per-block dedupe |
| IDN-3 | medium | One coordinator key can install verifiers and zero delays | Consensus delay floors; verifier warm-up; trust model stated (Q5) |
| IDN-5 | medium | Any post or retweet with the marker binds an account | Original post, whole-template match |
| IDN-6(a,c) | medium | DRK thief blocks recovery; cancels burn change cap | DIK-only RECOVER cancel; cap counts effective changes; 30 d cooldown |
| IDN-11 | medium | Lookalike precedence by registration order; empty Featured; inherited successors | First-seen precedence; rename re-check; reservations; OOB Featured; signed successor links |
| IDN-13, CMP-22 | medium | Conflicting app-store UGC controls | In-app report, `text_state`, one text limit, terms |
| PRV-1 | medium | OVK-keyed ARK exposes delegations to UFVK holders | Spending-key or hotkey ARK |
| PRV-2, CMP-17 | medium | Abstain option creates solvable buckets; claims overstated | No Abstain mitigation; normative §5 wording; new C4 |
| PRV-4, LIV-4, CMP-11, SND-8 | medium | Five cutoff rules; single-share DCs leak; hint key reuse | One chain-time cutoff; single-share removed; `(d, w, slot)` frozen |
| PRV-7 | medium | Vote servers link delegate's private votes to identity | Tor default-on in delegate mode; honest copy |
| LIV-1 | medium | Helper fleet caps scale; Zodl votes also lost | Budget, metrics, more workers, Zodl non-regression gate |
| LIV-3 | medium | Locked allocation strands unexecuted bundles | `release_proxy_bundle` |
| LIV-5, CMP-10 | medium | "Delegate more" vs locked allocation; no `dc_slot` | No top-ups in v1; `dc_slot` persisted |
| OPS-3, CMP-7 | medium | Recovery feed had no backing store | `0x1D` records; height indexes |
| OPS-6 | medium | Dormant tests ignore GasUsed; Msg-service ungated | `V:state/breaking`; const before KV reads |
| OPS-8, CMP-7 | medium | Conflicting KV layouts; false "did not vote" | One key table; set-based absence |
| CMP-2 | medium | "Stop accepting" had no valid backing op | DIK-signed `accepting:false` statement |
| CMP-3 | medium | Registration shapes put X ids in tx bytes | Identity `register` op only |
| CMP-4 | medium | Wire contracts diverge across specs | `contracts-v1` with golden vectors |
| CMP-5 | medium | Phrase model not propagated; hardware delegates blocked | Phrase everywhere; DG-12 removed |
| CMP-6, SND-2, SND-3, IDN-8 | medium | Four route digests; no chain binding | One convention with `lp8(chain_id)`; reset changes `chain_id` |
| CMP-8 | medium | PI order, packing, PRF disagree | 12-PI `Instance` order; `dc_seed` PRF |
| CMP-12 | medium | Capability and config names disagree | One field list; `extensions` only |
| CMP-13 | medium | Four static-config shapes; per-round pin conflict | One `proxy_delegation` section; no pin |
| CMP-14 | medium | WebP vs PNG; platform codecs | WebP plus Rust decode; ban test |
| CMP-16, PRV-3 | medium | Immediate DC share links tx to delegate | No DC immediate share |
| CMP-18, LIV-2 | medium | Recovery unwired; wrong "still counts" copy | R2 in Vizor; COMPLETE on hand-off; new C8 |
| CMP-19, PRV-5 | medium | Per-index lookups leak choices | Whole-list sync; sticky proof set |
| CMP-23 | medium | Audit scope too narrow | Two tranches |
| CMP-25 | medium | Unlisted owner questions | §7 |
| SND-1, CF-3 | low | Slot nf missing from chain and genesis paths; rand invariant | Full wiring checklist (§4.2); invariant in ZIP-PD |
| SND-6, CF-1 | low | Bound open; width and value rule | Adopted at 30 bits; builder takes `NextDelegateIndex` |
| SND-7 (unv.) | low | Mixed anchor burns a registration | Registration ⇔ anchor 0 |
| SND-4 (unv.) | low | Stolen DRK routes irreversibly | Per-use DRK presence; DIK override deferred |
| SND-5 (unv.) | low | Suspension forces abstention | Kept; owner accepts (Q5) |
| IDN-7 | low | Hot DRK theft; misleading claim | Per-use presence; reworded |
| IDN-9 | low | Floods via fresh signatures | Per-delegate block dedupe |
| IDN-10 | low | Subdomain sybils | eTLD+1; per-provider quotas |
| IDN-12, PRV-8 | low | Crypto-shredding claim false; stale objects | Claim withdrawn; full purge; ≤24 h expiry |
| IDN-14 | low | Phrase looks like a wallet seed | Non-BIP-39 format |
| IDN-15 (unv.) | low | Ed25519 rule mismatch | ZIP-215; torsion-free keys |
| IDN-16, CMP-15, PRV-9 | low | Index in link path; fingerprint drift; size-ordered DCs | Fragment link; 80-bit fingerprint; shuffled order; packs only |
| PRV-6 | low | Sentry spans link reveals to leaves | Span hygiene |
| LIV-6 | low | Mirror outage blocks delegation | 1 h cached fallback for clean entries; third mirror |
| LIV-7, LIV-8 (unv.) | low | C9 misleads; cross-install surprises | C9 reworded; gov-nullifier gate |
| LIV-9, OPS-11 (unv.) | low | Helper index check before leaf | Leaf first; 503 |
| LIV-11, LIV-12 (unv.) | low | Route retries; gates halting work | Idempotent entries; gates only for new allocations |
| OPS-2 | low | Genesis rejects type 3 | Genesis section; `ShareCountKey` reuse |
| OPS-4, CMP-20 | low | Beta and Zodl; enable race | Stage beta; Zodl limits; runbook rule; sign-off |
| OPS-5 | low | Zakura 2.0 swap unchecked | Corpus replay gate |
| OPS-7 | low | No mid-round brake | `proxy_dc_paused` |
| OPS-9, OPS-10 (unv.) | low | V2 size coupling; proof cap risk | V2 criteria; no cap in v1 |
| OPS-12, OPS-13 (unv.) | low | `ProposalTally(0)`; routing cost | Reject 0; droplet benchmark; `max_delegates` 20,000 |
| CF-2, CF-4, CF-5 (unv.) | low | W=0 casts; key RAM; digest kind tag and split-off | Dead successor; phased proving; kind byte; re-plan |
| CMP-9 | low | ZIP-PD leaf order reversed | Corrected |
| CMP-21, CMP-24 | low | No legal item; missing ops tooling | Legal gate; V20 |
| CMP-26..29 (unv.) | low | Vocabulary, analytics, perf, support gaps | contracts-v1 vocabulary; public aggregate dashboard; device and droplet benchmarks; support runbook and delegate reminders |

**Partly refuted:** PRV-2's 81% assumed unrouted pools map to Abstain (real figure 42-44%); OPS-4's Zodl visibility claim fails for Zodl ≥3.9.5, which shows only endorsed rounds (only ≤3.13.x throws on unparseable rounds); IDN-8's replay-after-reset (the verifier refuses reused DIKs); LIV-2's "COMPLETE before delivery"; IDN-6(c)'s 64-cycle exhaustion; IDN-3's "new trust" (coordinators can already push binaries); OPS-7's reveal pause (unimplementable); CMP-3's on-chain X ids (no proto field exists).

---

## 7. Open questions for the owner

| # | Question | Recommended default |
|---|---|---|
| Q1 | Delegation closes at `vote_end − max(last-moment buffer, 30 min)`, up to 6 h early in long rounds, to keep delegations private and countable. Accept? | Yes. Delegates can still route until the end. |
| Q2 | Add an explicit "Abstain" option to proposals in proxy rounds, for product reasons only? It makes pool totals exactly computable in small rounds. | No in v1. Keep the recorded-not-counted sentinel. |
| Q3 | Minimum per delegate | 1 ballot (0.125 ZEC) protocol floor and round default. Raise `min_ballots_per_delegate` per round only if load tests show pressure. |
| Q4 | Offer Keystone/Ledger delegators opt-in recovery keyed to the viewing key? Viewing-key holders would see their delegations. | Not in v1. Hardware delegators keep R1 only, the same as votes today. |
| Q5 | Coordinator suspension stays immediate mid-round, so a quorum can force a pool to abstain. Accept, and require ≥2 vote managers with threshold ≥2 for proxy payloads? | Accept immediate suspension with a public reason. Reach 2-of-3 vote managers before featured delegates hold large pools, within the first two proxy rounds. |
| Q6 | Verifier trust at launch | 1-of-1 Valar for registration; recover threshold 2 (two Valar keys, two-person approval); an independent second verifier within 6 months |
| Q7 | Beta venue | Stage chain. Mainnet test rounds only within old-Zodl limits (≤15 proposals, 2-8 contiguous options), never Zodl-endorsed. |
| Q8 | Show approximate delegation counts? | Only on the delegate's own dashboard. "Voted on N of M" on profiles, not list rows. Never sort by popularity. |
| Q9 | Featured and avatar policy | ≤30 Featured, 2-of-3 curators (Valar, Vizor), public log, OOB confirmation for launch entries. Human review for featured avatars, automated checks for others. |
| Q10 | May other wallets consume the X-hydrated profiles doc? | Registry doc open now; profiles doc Vizor-only until counsel clears it. |
| Q11 | Who handles reports, and how fast? | Valar support; 24 h for impersonation and offensive content; same-day delisting when confirmed. |
| Q12 | Per-round enablement | Opt-in per round; enable for all public rounds after one successful mainnet proxy round. |
| Q13 | Ship follow-mode v0 (directory plus signed voting guides) a round early? | Only if the verifier MVP is early and it does not touch the circuit path. Do not plan on it. |
| Q14 | X API budget: de-hydrate dormant delegates (no route in 3 rounds, not featured)? | Yes (about $1k a month at 10k delegates instead of about $3.4k). |
| Q15 | Top-ups ("Delegate more") | Not in v1; append-only allocation generations in v1.1. |

---

## 8. Rollout and milestones

### 8.1 Work packages

Sizes: S is 1-3 days, M is 1-2 eng-weeks, L is 2-4 eng-weeks. Per-repo totals are rough.

- **Specs (chain lead, spec lead):** SP1 `contracts-v1` (M, weeks 0-3): proto, JSON names, tags, REST, digests, KV table, capabilities, errors and vocabulary, FRB DTOs, Go and Rust golden vectors in CI; **blocks every implementation PR**. SP2: ZIP-A errata (S), ZIP-PD (M; ZKP4 statement frozen by week 3), ZIP-DR (M), WAPI/SUB/SETUP amendments (S), book rewrite (M, to week 15).
- **voting-circuits (about 8 eng-weeks):** constants (S); ZKP4 with C14 and C15 (M); MockProver suite (M); builders with the `dc_seed` PRF (M); prove, verify and fingerprints (S); exports and vectors (S); cross-circuit tests (S); benchmarks with mobile RSS (S). rc.1 by week 9.
- **vote-sdk (about 20 eng-weeks):** proto and dormant gating (S); types and digests (M); capacity guard (S); registry (L); routes (M); ZKP4 FFI (M); 0x09 (L); p=0 reveal (S); round snapshot, params, pause (S); routing hook (M); queries, indexes, feed, capabilities (M); genesis (M); helper branch, telemetry, metrics (M); upgrade and runbooks (S); e2e (L); determinism and droplet benchmark (S); corpus replay gate (S); activation (S). **V20 ops (M):** CLI and coordinator-UI support for new payloads; Prometheus metrics with an alert on permanent p=0 rejections; a scripted canary delegate and delegator per proxy round asserting `totals == direct + routed`; explorer event docs.
- **zcash_voting (about 18 eng-weeks):** planner (M); secrets, phrase, digests (M); circuits integration and phased proving (M); schema v25 (L); effective-weight refactor (L); batch builder and packer (L); submission lifecycle and split-off (L); planner obligations and release path (L); DC share delivery (M); verification and sticky proofs (M); recovery (M); gating (M); directory client (M); delegate APIs and crate (M); integration, vectors, mobile benchmarks (M).
- **Verifier service (about 10 eng-weeks):** API and providers (L); hardened signer and key ceremony (M); indexer, re-verifier, refresher, retention (M); avatar pipeline (M); publisher, mirrors, curator signing (M); curation, reports, moderation (M); ops (M). Legal review runs externally in weeks 4-10 and gates the X provider.
- **Config repo** (S) and **deeplink server** (S).
- **Vizor (about 14 eng-weeks):** UI on mocks from week 4, integration from week 11.
- **Stage, load lab and QA (about 5 eng-weeks); specs and book (about 6).**

### 8.2 Timeline and critical path

| Milestone | Weeks | Notes |
|---|---|---|
| M0 contracts-v1, ZKP4 statement, ZIP-DR digests frozen | 0-3 | Critical |
| M1 voting-circuits 0.13.0-rc.1 | 2-9 | Critical |
| M2a Audit tranche 1: ZKP4, 0x09, p=0 reveal, routing and ρ, registry handlers | 9-13 | Critical; book auditors now |
| M2b Audit tranche 2: client secrets and recovery, phrase format, registry digests and attestation counting, signer custody, directory signing, Rust image decode, IAVL verification | 12-15 | Launch gate |
| M3 vote-sdk dormant merges plus helper | 3-12 | Registry and routes need no circuits |
| M4 Verifier MVP (X, GitHub, DNS), then hardening (IDN-2) | 3-10, 10-12 | |
| M5 zcash_voting | 4-13 | Needs M1 rc |
| M6 Vizor | 4-16 | |
| M7 Config, deeplink, legal sign-off | 6-10 | |
| M8 Stage activation; T1, T2, T3; load lab; capacity gate | 13-17 | Critical |
| M9 Mainnet activation between rounds | 17-18 | Critical |
| M10 First public proxy round | 18-19 | |

- **Critical path:** M0 → M1 → M2a → final tags (0.13.0, v1.7.0, zcash_voting, Vizor) → M8 → M9 → M10, about 19 weeks.
- M2b and the legal sign-off run on parallel paths that must close before M8 ends.
- **Total:** about 80-85 eng-weeks plus two audits. The earlier 60 eng-week estimate predates the scope the review added (verifier hardening, recovery records, ops tooling, the second audit tranche).
- **Staffing:** circuits 1-2, chain 2-3, client 2, Vizor 2, identity 2, specs/QA 1.

### 8.3 Activation

1. Merge dormant PRs to main under `V:state/breaking`.
2. Run the corpus replay gate: the candidate binary's FFI verifiers check every vote tx from recent mainnet rounds (ZKP1/2/3 proofs, RedPallas, TX1 sighash) with byte-identical accept/reject results against v1.6.x. This covers the Zakura 2.0 dependency swap (OPS-5).
3. Run the coordinated v1.7.0 halt **between rounds**: all rounds FINALIZED, helper queues empty.
4. Post-checks: capabilities on, the four fingerprints match, the registry is empty.
5. Coordinator actions: set the verifier set and params (`enable_for_new_rounds` false).
6. Register the canary and featured delegates. Publish the directory to three mirrors.
7. Per round: change `enable_for_new_rounds` only when no create-session action is pending, verify the created round's snapshot, then sign the config extension.

**Rollback.**
- No binary rollback after the first proxy tx.
- Incident levers: `proxy_dc_paused` (stops new DCs; existing DCs, reveals and routes stand), disable for new rounds, and the Vizor kill switch.

### 8.4 Stage and beta

- **T1 (internal):** Vizor (software, Keystone, Ledger) plus a current Zodl build and an old Vizor build in one proxy round. Includes a Keystone-signed registration. A scripted tally check confirms `totals == direct + routed`.
- **T2 (delegate beta):** 10-20 real influencers through the stage verifier.
- **T3:** the adversarial list plus the load matrix on the ten-validator lab.
- Then one mainnet proxy round per Q7 and Q12.

### 8.5 Zodl coordination

Send a written notice listing what Zodl will misreport:
- **M1:** totals include delegated weight, without attribution.
- **M2:** `VoteSummary` share counts exclude pools.
- **M3:** a seed that delegated in Vizor shows "already used" in Zodl.
- **M4:** old Vizor classifies "delegated" as "voted".
- **M5:** explorers see new event types.
- **M6:** DB v25 cannot be downgraded.

Zodl maintainers then confirm on a current production build that extra round fields and `extensions` are ignored, and sign off before the first proxy round. Zodl builds 3.13.x and earlier throw on rounds they cannot parse, so mainnet test rounds must stay within that parser's limits until those builds age out. Optional Zodl copy: "Totals include votes cast by public delegates."

### 8.6 Private-vote V2 coordination

Acceptance criteria on the V2 PR, added now: keep the v1 scalar ZKP3 byte-identical as `pool_share_reveal` (VK pinned); keep v1 `shares_hash`, El Gamal gadgets and `DOMAIN_VC` live; keep the compact ZKP2's `p ≠ 0` gate, VAN format, nullifier tag and `rand` invariant; reserve PRF domains 0x10-0x14; put vector reveals on a new tag and restrict 0x04 to p=0 in V2 rounds; use per-circuit proof caps (15 KiB for ZKP1-4); pack V2 casts by byte budget (about 11 per tx [measured]); count 0x04 and the V2 reveal tag in one per-block cap; extend the name-keyed fingerprint list; pin ZKP4 and `pool_share_reveal` fingerprints in V2 CI.

---

## 9. Test strategy and launch checklist

### 9.1 Tests by layer

- **Circuits:** the §4.1 suite; ZKP1-3 `vk_fingerprint_unchanged` (release gate); real-proof size ≤15 KiB; CI benchmarks.
- **Chain:** slot nf duplicates (intra-message, across txs in CheckTx and RecheckTx, genesis type 3); registration ⇔ anchor 0 in both directions; D1, route and registry digest golden vectors (Go = Rust = e2e `sighash.rs`); prefix-free `SVOTE_` domains; ZIP-215 torsion vectors; `bound ≥ NextDelegateIndex` rejected and registry capped at 2^30; p=0 reveals accepted for frozen, suspended and revoked delegates and rejected for `d ≥ Next`; proposal 0 never in VoteSummary, TallyResults, partials, completeness or `SubmitTally`, and `ProposalTally(0)` rejected; routing conservation against a plaintext model, byte-identical output under reversed order, the identity-C1 vector, `pools_routed` once; registry rules (no unfreeze, freeze replay after rotation fails, DRK cannot cancel RECOVER, removed verifier voids recovery, recovered entry cannot route older rounds, verifier warm-up, delay floors, per-block dedupe, idempotent route entries); dormancy (const before KV reads); two-node determinism across the transition block; genesis round trip with a finished and an active proxy round; droplet routing benchmark.
- **Helper:** leaf-first ordering with 503 on an absent leaf; span-data key test; capacity metrics.
- **e2e:** register; create round; 0x09 with and without registration; helper reveals; routes including abstain and a missing route; tally equals direct plus routed; split-off recovery; R1 and R2 against `0x1D`; a Zodl-style flow in the same round.
- **zcash_voting:** planner proptests (conservation, `D ≤ #DC ≤ D + B − 1`, within-1 quota); hint, ARK and `dc_seed` vectors; delegate phrases never validate as BIP-39 and restore rejects BIP-39; release-path state machine; no immediate DC share; cutoff from chain time; sticky proof set identical across checks; no per-index REST on delegator paths; gates never stop committed work; v24→v25 migration preserves rows; mobile benchmarks with peak RSS.
- **Vizor:** widget tests for every PD and DG state; `Image.network`/codec ban; route-table audit; kill switch both ways, including an in-flight DC; deletion cleanup; deep-link fragment parsing and mismatch blocking; accessibility.
- **Verifier:** reply, quote, retweet and template-mismatch proofs rejected; eTLD+1 uniqueness; signer refuses mismatched fields; re-verifier pages on a forged REGISTER; purge clears historical objects on every mirror.
- **Adversarial (stage T3):** ZKP4 after a partial vote; ZKP2 and ZKP4 on one VAN; one VAN twice in a batch; field-wrap inflation; a VC revealed as proposal 0 and a DC as proposal p; proposal-0 partials or `SubmitTally` entries; route replay, race, and routes from revoked or rotated keys; registry spam, expired-attestation reuse, double-counted signers; reveals to unregistered indices; targeted helper censorship of one index; non-canonical encodings; tree fill; oversized batch; snapshot equivocation; malicious avatar; deep link carrying an address; lookalike of a Featured handle; X takeover recovery; mixed-anchor front-running; freeze replay; DRK cancel of recovery; directory-key relabelling; retweet binding; subdomain sybils; fresh-signature route floods; Zodl and old Vizor in the same round.

**Load matrix:**

| Scenario | Mix |
|---|---|
| L1 | 5k direct voters × 10 proposals |
| L1b | 2k direct voters × 37 proposals, deadline-heavy |
| L2 | L1 plus 10k delegators × 3, uniform arrivals |
| L3 | L2 with deadline-heavy arrivals |
| L4 | 50k delegators × 3, deadline-heavy |
| L5 | Routing with 20k delegates × 50 proposals on validator hardware |
| L6 | 100 batches × 10 DCs in one block |
| L7 | Directory CDN at 50k wallets |
| L8 | Verifier spike at the daily cap |

Pass criteria:
- every honest share is revealed before `vote_end_time` with at least 30% headroom;
- Zodl direct-vote loss and latency do not regress against L1/L1b;
- block time p99 is at most 3 s;
- routing EndBlock is within the budget set from the droplet benchmark;
- no app-hash divergence;
- proofs per unique reveal are measured and reported.

### 9.2 Launch checklist

- [ ] contracts-v1 frozen. Golden vectors pass in Go, Rust and e2e. ZIP-PD and ZIP-DR published. Book privacy and threat pages carry the §5 wording.
- [ ] Both audit tranches closed. voting-circuits 0.13.0, vote-sdk v1.7.0, zcash_voting and the Vizor store release are final. Fingerprints for all four circuits published.
- [ ] Corpus replay gate passed. Stage T1, T2 and T3 passed, including Zodl and old Vizor and the tally equality check.
- [ ] Load gate passed. Helper workers set to the measured headroom.
- [ ] Mainnet upgrade applied between rounds. Capabilities verified. Verifier set (1 register, 2 recover) and params set. Delay floors confirmed. `max_delegates` 20,000.
- [ ] Verifier hardening live: signer re-verification, independent re-verifier, read-only publisher replica. Legal sign-off recorded. Purge SLA tested end to end, including all three mirrors.
- [ ] Featured delegates OOB-confirmed and curator-signed. Reserved-names list seeded. Directory on three mirrors.
- [ ] Static pin and per-round extension signed and CI-verified. Old pins untouched. `supported_versions` unchanged.
- [ ] Zodl written notice sent and sign-off received.
- [ ] Kill switch and `proxy_dc_paused` tested.
- [ ] Dashboards live: pool reveals, routes, helper queue by class, permanent p=0 rejections, verifier and re-verifier health, X API errors.
- [ ] Canary delegate and delegator scheduled for the first round.
- [ ] Public docs published: delegator FAQ (finality, abstain, cutoff, what is public, device loss), delegate guide (phrase safety, Tor, public and permanent attribution, counts public, isolation risk), no-receipt-freeness disclosure, support runbook with escalation to coordinators.
- [ ] App-store UGC review passed (report, hide, moderation, contact, terms).
- [ ] On-call owners named for chain, helper, verifier and Vizor.
