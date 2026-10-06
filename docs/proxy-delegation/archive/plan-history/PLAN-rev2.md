# Proxy delegation: final implementation plan

Status: final plan for implementation, revision 2, 2026-10-05. It supersedes integrated-design-v1 and the six component specs wherever they disagree. It folds in seven adversarial review lenses (soundness, privacy, liveness, identity, chain ops, circuit feasibility, completeness) and their skeptic verdicts, then the owner's revision-2 feedback and its two skeptic reviews.

Evidence labels: [code] read in a repo, [doc] from a spec or doc, [measured] from a prototype run, [inference] reasoned. Repos: **vote-sdk** (chain, helper, FFI), **voting-circuits**, **zcash_voting** (client SDK), **Vizor**, **verifier service** (new repo), **token-holder-voting-config**, **vizor-deeplink-server**, **zips/book**.

Terms: *registration* is today's ZKP1 (notes to a VAN, shown in Vizor as "voting authorization"). *Proxy delegation* is the new feature. A *DC* (delegation commitment) is the leaf a delegator creates. *Pool[d]* is the El Gamal accumulator `TallyKey(round, 0, d)`. A *route* is a delegate's public choice on one proposal. *DIK* and *DRK* are the delegate's identity key and route key. The *last-moment window* is the final `min(40% of the round, 6 h)` before `vote_end_time` (`share_policy/timing.rs:19-24, 61-68` [code]). *Layout 0* is the standard 16-share DC; *layout 1* is the last-moment single-share DC.

### Revision 2 (owner feedback)

1. **Per-delegate totals are published (D4 revised).** No pool is decrypted during the round. After the per-option results are FINALIZED, a separate post-finalization phase threshold-decrypts every pool that received shares, in chunks (injected tag 0x0F), and the chain publishes each delegate's total and revealed-share count (§4.2 Pool disclosure). Per-option results keep today's path and never wait on pools. Leaderboards and track records are allowed (PD-14). The isolation warning, the "totals are never shown" copy, the privacy case against an Abstain option and the "pool size is unverifiable" caveat are gone; a lone delegator's amount is now public, and the §5 wording says so.
2. **Last-moment delegation parity (O3 and Q1 decided).** The early close at `vote_end − max(buffer, 30 min)` is removed. Delegation stays open until `vote_end_time`, exactly like voting. A batch planned in the last-moment window uses the vote path's single-share layout for its DCs and casts (layout 1, PRF domain 0x14 now live) and is delivered at once; pools are routed at close with every reveal that landed before `vote_end_time`. No chain, helper or VK change. The layout is frozen per batch before the first proof.
3. **Other owner confirmations.** A separate delegate key phrase (unchanged). Coordinator suspension stays immediate with safeguards: freeze-only, public reason, at least 2 approvals for every proxy payload, and 2-of-3 vote managers within the first two proxy rounds (Q5 decided).
4. **Fixes from the revision-2 skeptics:** disclosure start can never fail `MsgSubmitTally`; fail-safe share retention; no decryption of routing bases; chunk size capped at 500; curated leaderboards; approximate delegation counts; batch-level layout freeze covering casts; in-window DC completion waits for the reveal; equal-count sticky proofs; explicit-layout share planner. New owner rows D5-D7, decisions 31-37, questions Q16-Q18 (§7 now splits decided from open), and hole-register rows PUB-1..9, LMD-1..9 and CRD-1. Estimate: about 88-93 eng-weeks (was 80-85).

---

## 1. Summary

1. **What we are building.** In proxy-enabled rounds, a Vizor user can give some or all of their round voting weight to up to 10 registered delegates and keep the rest to vote privately. Delegates vote publicly, one route per proposal. Delegated weight is counted through encrypted per-delegate pools. No pool is decrypted during the round; after per-option results are final, each pool's total is decrypted and published.
2. **Why.** Many holders do not want to read every proposal. Today their weight simply goes unused. Public delegates with verifiable records give that weight a voice without revealing who the delegators are. Individual amounts stay hidden, except that a delegate's only delegator has their amount revealed by the published pool total.
3. **Circuits.** One new additive circuit, ZKP4, spends a full-authority VAN. It outputs a DC that commits to a hidden delegate index `d` and an amount `w`, plus a successor VAN holding `W − w`. ZKP1, ZKP2 and ZKP3 VKs stay byte-identical, so Zodl and old Vizor keep working unchanged.
4. **Pools.** Each DC is split into 16 El Gamal shares or, when its batch is planned in the last-moment window, one share holding the whole amount, exactly as votes are. Existing helpers reveal them with the **unchanged ZKP3** as `(proposal 0, decision d)`, and the chain adds them into `Pool[d]`. After the round, every pool that received shares is threshold-decrypted and its total published (§4.2 Pool disclosure).
5. **Tally.** At the ACTIVE→TALLYING EndBlock, the chain adds each pool into the option bucket its delegate routed, plus a deterministic `Enc(0; ρ)` re-randomizer. Threshold decryption of per-option results then proceeds exactly as today, so results never wait on pools. Once the round is FINALIZED, a separate pool-disclosure phase decrypts every pool that received shares, in chunks, and publishes per-delegate totals. Direct and delegated splits are derived as total minus routed pool totals. Unrouted proposals abstain.
6. **Delegate identity.** A dedicated delegate key phrase derives two Ed25519 keys: a DIK (identity, can be kept cold) and a DRK (routes, hot). The delegate proves control of an X account (or a GitHub account or domain) with a marker post. A Valar verifier issues a threshold attestation. The chain registry stores keys, status and a salted commitment only. No X content goes on chain.
7. **Discovery.** Wallets download a signed directory: a registry doc, a profiles doc, and avatar packs re-encoded server-side. Search runs locally on the device. A curated Featured tier is signed by curator keys. After each round, a Top delegates leaderboard and per-profile track records show published totals; the default directory order stays shuffled. Fingerprints and lookalike warnings appear on every delegate surface, leaderboards included.
8. **Compatibility.** Gating is additive only: no `vote_protocol` or `auth_version` bump. The feature turns on through chain capabilities plus a VK fingerprint match, a signed per-round config extension, and a Vizor kill switch.
9. **Delivery.** voting-circuits 0.13.0, vote-sdk v1.7.0 (dormant merges, one coordinated activation), zcash_voting, a new verifier service, and Vizor, with two audit tranches and stage rounds first. The critical path is about 19 weeks.
10. **Honest limits.** Per-delegate pool totals and revealed-share counts are public after each round. A delegate's only delegator therefore has their amount published (their whole round weight if they delegated everything), though not their identity, and direct per-option totals become exact. Approximate delegation counts (pool reveal counts) are public live. Helpers learn each DC's delegate; a DC made in the last-moment window is linked to its tx by reveal timing, as a late vote is; reveal timing can narrow small pools to a few transactions; a coalition holding at least t EA key shares can decrypt individual shares; and there is no receipt-freeness (§5).

**Answers to the owner's original questions.**

- **(a) Delegate some or all of my weight to up to ~10 others, and keep some or none?** Yes. You choose 1 to 10 delegates with percentages, and either keep a remainder to vote yourself or delegate everything. Amounts are whole ballots (0.125 ZEC), at least 1 ballot per delegate, assigned by Hamilton largest-remainder rounding and shown exactly before you confirm. Two rules apply. You must delegate before your first own vote in the round, because the circuit needs full authority. And delegation is final for the round. You can delegate until voting ends, just as you can vote. In the final hours your delegation is sent to helpers at once, as a late vote is, and as with a late vote, one sent in the final minute may not be counted.
- **(b) ICNS-style X linking with a profile picture to look up influencers?** Yes, without ICNS's flaws. The delegate posts a marker line containing their key; the verifier binds the **numeric** X account id (never the handle) via the official X API, and the delegate's key signs the binding back. Profile pictures are re-encoded server-side and decoded in Rust; wallets never contact X. You search handles locally and see a fingerprint plus lookalike and "new this round" warnings. GitHub and domain proofs ship as fallbacks.
- **(c) If I delegate to an influencer, can I find out whether they voted with it?** Yes, per proposal. Routes are public and IAVL-provable, and Vizor shows each proposal as Voted (their choice), Abstained, Not yet, or Didn't vote (abstain once the round ends). It also shows that your shares reached their pool (k/n, where n = 16, or 1 for a last-moment DC; provable via share nullifiers). Their route applies to the whole pool, your weight included. After voting ends you can also see their pool total (all delegations combined) and roughly how many delegations made it up. You cannot see their private vote with their own ZEC, which may differ from their route.
- **(d) Can someone delegate at the last moment, just as they can vote at the last moment, with pools tallied at close?** Yes (decided, Q1). The client allows a new delegation while `now < vote_end_time`, the same check casts use (`vote_work/cast_vote.rs:39-52` [code]). In the last-moment window a DC uses the vote path's single-share layout: one share carrying all of `w`, sent at once to `ceil(N/2)` helpers with `submit_at = 0` (`submission_schedule.rs:113-134` [code]). The chain accepts the DC and its reveal while `blockTime < vote_end_time` (`keeper_voting.go:313-341` [code]), and the closing EndBlock routes each pool with every reveal that landed before close. Genuine differences from a late vote: one reveal carries the DC's weight on every proposal, so a missed reveal loses the whole DC; delegation needs a few extra seconds before proving (directory, sticky proofs, `NextDelegateIndex`); and under published totals a late lone DC's amount is public and tied by timing to its anonymous tx. Shared with votes: the reveal must land before close (about 5-10 s at best, minutes under helper backlog [inference]), and a tx in the final block is never counted.

**Top changes from integrated-design-v1:** ARK no longer keyed by the viewing key; no unfreeze op, DIK-only recovery cancellation, recovery hardening; 30-bit `delegate_index_bound`; last-moment parity: a batch planned in the last-moment window uses the vote path's single-share layout for its DCs and casts, and delegation stays open until `vote_end_time`; no DC share is the designated immediate share; the layout is frozen per batch, and `(d, w, slot, layout)` per `van_nf`, before the first proof; a normative contract artifact with `chain_id`-bound digests; an on-chain proxy-action record for recovery; no Abstain option as a privacy mitigation; curator-signed Featured; no "Delegate more" in v1, plus a release path for stuck bundles; per-delegate pool totals published after each round through a post-finalization disclosure phase (owner revised D4).

---

## 2. Decisions log

### 2.1 Owner decisions (fixed)

| # | Decision | How the plan honors it | Where it cannot be fully honored |
|---|---|---|---|
| D1 | Delegate votes are public, via pools | Routes are public, signed by the DRK, IAVL-provable, and applied to the whole pool | A delegate's own private vote can differ from its route |
| D2 | Proxy delegation is final; unrouted means abstain | No override or revocation message exists. A DC is final once included. | Undispatched slots can be released, because they never landed (LIV-3) |
| D3 | Open listing with proof; Valar verifier; chain registry; featured tier | X, GitHub and DNS providers; threshold attestations; registry 0x1A; curator-signed Featured | Launch is 1-of-1 Valar, so the verifier is a trust and censorship point until a second verifier exists |
| D4 (revised in rev 2) | Per-delegate totals are public after the round; leaderboards allowed | No pool is decrypted during the round. Every pool that received shares is threshold-decrypted after per-option results are FINALIZED, in a separate chunked phase (§4.2 Pool disclosure). Totals and revealed-share counts are on chain; the direct/delegated split is derived. | Disclosure can EXPIRE or FAIL, leaving some totals unpublished (results unaffected). A lone delegator's amount becomes public. Delegation counts are approximate: a reveal count R means between `ceil(R/16)` and R DCs. A late lone DC's amount is tied by timing to its tx (§5). |
| D5 (rev 2) | Last-moment delegation parity: delegate until voting closes, as for votes | Delegation stays open while `now < vote_end_time`; a batch planned in the last-moment window uses the single-share layout and immediate delivery; pools are routed at close with every reveal that landed before `vote_end_time` (§4.4, O3) | As for a late vote, the reveal must land before close. One reveal carries a late DC's weight on every proposal. Reveal timing links a late DC's tx to `d`, as it links a late vote's tx to its option |
| D6 (confirmed) | Delegate keys come from a separate delegate key phrase | Non-BIP-39 24-word phrase derives DIK and DRK (§4.6, O4) | A lost phrase falls back to verifier-attested recovery with a 7 d delay |
| D7 (rev 2) | Coordinator suspension with safeguards | `MsgSetDelegateSuspension` is immediate and freeze-only, with a public reason code; every proxy payload needs at least 2 coordinator approvals; 2-of-3 vote managers within the first two proxy rounds (§4.2, Q5) | Coordinators remain fully trusted (they can already push binaries via x/upgrade). A quorum can still make an unrouted pool abstain mid-round. Incident levers need two coordinators on call |

### 2.2 Open items from v1, resolved

| Item | Resolution | Evidence |
|---|---|---|
| O1 `delegate_index_bound` | **Adopt** as public input 11: in-circuit `1 ≤ d ≤ bound` via 30-bit range checks on `d−1` and `bound−d`; chain requires `bound < NextDelegateIndex`, registry ≤ 2^30; the builder sets `bound = NextDelegateIndex − 1` itself (§4.1) | [measured] Still 2,015/2,048 rows, 78 fixed columns, 11,008 B proof, prove time within noise; soundness suite passes; a real proof fails if PI[11] changes |
| O2 one tag vs two | **One tag 0x09** with optional registration; registry 0x0B, routes 0x0C; `anchor_height == 0` if and only if a registration is present | SND-7 front-running analysis |
| O3 chain DC cutoff | **Decided (rev 2, D5): no consensus deadline and no early client close (vote parity).** The SDK uses the cast path's predicates on the same `RoundHostContext`: no new allocation or proxy batch once `now ≥ vote_end_time` (`VoteEnded`); in-flight work advances and the chain rejects late txs without spending the VAN; new allocations require authenticated round timing; layout = single share if and only if `is_last_moment()` at batch planning, persisted once per 0x09 batch for its DCs and casts before the first proof, with `(d, w, slot, layout)` per `van_nf`. Coordinator `proxy_dc_paused` brake. The rev-1 rule (close at `vote_end − max(last_moment_buffer, 30 min)`, no single-share DCs) is withdrawn. | PRV-4, LIV-4, CMP-11, SND-8; `cast_vote.rs:35-53`, `vote.rs:4447-4452`, `submission_schedule.rs:113-134` (zcash_voting), `keeper_voting.go:313-341`, `module.go:491-497` (vote-sdk) [code]; single-share ZKP4 MockProver positives and ZKP3 reveal (`rev-lastmoment/proto/single_share_test.log`) [measured] |
| O4 delegate keys | **Dedicated delegate key phrase**, in a non-BIP-39 format; hardware-account users can be delegates. Owner confirmed (D6). | CMP-5, IDN-14 |
| O5 Abstain option | Privacy is no longer a factor, because pool totals are public (D4 revised). Keep the sentinel `0xFFFFFFFF` (recorded, not counted) for Zodl compatibility; an Abstain option is product question Q2. | Historical: [measured] 42-44% of pools were exactly solvable with an Abstain option (15 proposals, 30 voters, 5 delegates) vs 0% with the sentinel; moot now that totals are published |

### 2.3 Design decisions

| # | Decision | Rationale | Rejected |
|---|---|---|---|
| 1 | B2: DC shares through helpers | Hides delegate choice from the public and the EA, and hides per-DC amounts (only pool sums are published). With public totals, the unlinkable DC also keeps a lone delegator's amount from being tied to a tx, except for last-moment DCs, as for late votes | B1 in-tx slots: EA sees exact amounts; delegate set linked to tx, so a lone delegator's public amount would attach to one tx |
| 2 | ZKP4 as a separate circuit at K=11 | No existing VK changes | A ZKP2 mode: new VK for all voters |
| 3 | DC reuses `DOMAIN_VC` with constant proposal 0 | ZKP3 unchanged; ZKP2's `p ≠ 0` gate keeps it sound | New `DOMAIN_DC` and reveal VK |
| 4 | Input and successor VAN authority MAX | No double counting of cast weight | Per-proposal pools |
| 5 | Slot nullifier: ≤16 DCs per registration per round | Bounds helper load | Minimum size only (count grows with W) |
| 6 | p=0 reveals accepted for any existing index | Status changes never strand weight | Status check at reveal |
| 7 | EndBlock routing with ρ re-randomization | Pools final; blocks identity-C1 round kill on the combined buckets; per-option results never depend on pool decryption | Lazy routing in three code paths |
| 8 | Pool counts at `ShareCountKey(round,0,d)`; distinct event | Free genesis export; VoteSummary never sees p=0 | Separate key (OPS-2) |
| 9 | Digests: BLAKE2b, ASCII domain, `lp8(chain_id)`, 32-byte fields | One convention; no cross-chain replay | `net` string, LE fields |
| 10 | DIK and DRK from a delegate phrase | Hot-key rotation keeps the index; hardware users included | Single key; seed derivation |
| 11 | No unfreeze; DIK rotation exits FROZEN | Stolen DRK cannot undo a freeze; no replay | DIK-or-DRK unfreeze |
| 12 | DIK-only recovery cancel; recovered keys cannot route older rounds | Takeover cannot capture formed pools | v1 rules |
| 13 | Delay floors; verifier warm-up | One coordinator key cannot re-key quickly | Unconstrained params |
| 14 | ARK from spending key or hotkey, never OVK | Viewing keys don't reveal votes | OVK-keyed ARK |
| 15 | No designated immediate DC share | No public tx-to-delegate link for early DCs; inside the last-moment window every share, vote or DC, is immediate anyway | DC share 0 immediate in every window |
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
| 31 | Pools disclosed in a separate post-FINALIZED phase (option A) | Main results keep today's ≤400-entry path; a disclosure stall cannot wipe results | B: decrypt direct and pools separately and sum in `MsgSubmitTally` (puts up to 20k decryptions before results; a stall finalizes with empty results, `module.go:641-662` [code]) |
| 32 | Disclose every pool that received shares; routing bases are not decrypted | Accountability for unrouted and abstaining pools. Base plaintexts are implied by the final totals, pool totals and the public homomorphic routing identity | Routed pools only; also decrypting 0x1C bases (redundant, and base BSGS cost scales with proposals × direct turnout inside FinalizeBlock) |
| 33 | Chain combines and runs BSGS on the t-th chunk submission; no pool tally message | No proposer trust; BSGS cheaper than a `v·G` check for typical pools; total giant steps ≤ P + supply/16,384 (≤ P + 10,254) | Proposer-claimed `MsgSubmitPoolTally` |
| 34 | Chunked pool partials, tag 0x0F, at most one per block, exactly t submitters per chunk; chunk start index stored; partial blobs pruned after combine | Bounds FinalizeBlock work and permanent state | One 2.1 MB message per validator |
| 35 | Disclosure is unconditional in v1 proxy rounds | Delegators know the rule before delegating | Coordinator toggle |
| 36 | Disclosure start can never fail `MsgSubmitTally` | Runs in a cache context after FINALIZED; any error records FAILED and alerts | Start logic on the tally tx's error path |
| 37 | Last-moment parity: single-share DCs in the window, open until `vote_end_time`, layout frozen per batch | Delegating behaves like voting; 1 reveal per late DC; one clock and one predicate for DCs and casts | Early close at `vote_end − max(buffer, 30 min)`; chain `cutoff_time`; a DC-only chain-time clock (breaks batches that mix DCs and casts) |

---

## 3. Architecture

### 3.1 Actors

- **Delegator wallet:** Vizor plus the zcash_voting SDK; plans, proves, submits, delivers shares, verifies. Software, Keystone and Ledger accounts; devices sign only ZKP1.
- **Delegate wallet:** Vizor delegate mode plus the `zcash_vote_delegate` crate; holds the phrase, DIK and DRK; onboards; signs routes and registry ops.
- **Vote chain:** vote-sdk validators; verify proofs, hold registry, routes and pools, route at the tally transition, and hold EA key shares; after FINALIZED, decrypt and publish pool totals (validators keep the round's Shamir share until disclosure ends).
- **Helpers:** the svoted helper on each vote server; receive DC shares (learning `d`, the DC leaf and the client IP unless Tor is on), prove ZKP3, reveal at randomized times.
- **Verifier service:** Valar-run verifier-api, attest-signer (KMS/HSM), publisher, refresher, chain-indexer and independent re-verifier.
- **Directory mirrors:** the valargroup origin, `functions.vizor.cash` and a third independent mirror, all untrusted for integrity.
- **Config repo:** a new static pin and a per-round signed dynamic extension.
- **Coordinators:** vote managers who set params and verifiers and may suspend a delegate. Every proxy payload needs at least 2 approvals (D7).

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
  |--16 shares/DC, or 1 if planned in the last-moment window ------------>|              |
  |  (p=0, decision=d; submit_at scheduled, or 0 in the window)          |              |
  |  "Handed off" = definite acceptance of every share (in-window: reveal) |              |
  |                            |            |<--0x04 reveal(0,d)+ZKP3 (random, or at once)|
  |                            |            | Pool[d] += share                          |
  |                            |            |<----------------------- 0x0C route {p: o} signed by DRK
  |--poll route list; helper share-status-->|                            |              |
  |                         ... vote_end_time ...                        |              |
  |                            |            | EndBlock ACTIVE->TALLYING:                 |
  |                            |            |  agg[p][route(d,p)] += Pool[d]; += Enc(0;rho)
  |                            |            |<-- partial decryptions (DLEQ), MsgSubmitTally as today -> FINALIZED
  |                            |            |<-- 0x0F pool partial chunks (DLEQ), t per chunk, one per block
  |                            |            | chain: Lagrange + BSGS per chunk -> DelegatePoolResults (0x1F);
  |                            |            |        COMPLETE | EXPIRED | FAILED
  |--IAVL-prove route sets and share nfs (sticky set)-->|               |              |
```

### 3.3 Data and trust boundaries

- **Chain-authoritative:** index, DIK, DRK, `key_epoch`, status, suspension, routes, pools, nullifiers, `NextDelegateIndex`, the proxy-action records, pool results and disclosure status.
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
- PRF domains 0x10-0x14 keyed by `dc_seed`: 0x10 El Gamal, 0x11 blind, 0x12 shuffle, 0x13 remainder (layout 0); 0x14 El Gamal for layout 1, the last-moment single share, mirroring `VOTE_PRF_DOMAIN_ELGAMAL_SINGLE_SHARE = 0x04` (`vote_proof/builder.rs:538-550`, `domain_tags.rs:42-43` [code]);
- `DOMAIN_VC` documented as live for proposal-0 pool commitments.

**Invariant (normative in ZIP-PD).** `rand` is constant along every transition that keeps MAX authority. Any future VAN-transforming circuit must either preserve `rand` or carry a slot counter; otherwise C14's cap silently resets (CF-3). A cross-circuit test pins it.

**Measured costs** ([measured], loaded M3 Ultra): ZKP4 proves in 530, 278, 161 and 96 ms at 1, 2, 4 and 8 threads (within 2% of ZKP2), verifies in about 1.7 ms, and is 11,008 B (cap 15,360 B). Keygen peak RSS is about 140 MiB; ZKP4, ZKP2 and ZKP3 resident use 259 MiB, plus about 182 MiB for ZKP1. At 4 threads, ZKP1 + 10 ZKP4 + 50 ZKP2 takes 10.2 s, and "delegate everything" 1.9 s. [inference] Mid-range Android at 2.5-4x slower still meets Vizor's ≤2 s per proof target.

**ZKP3 reuse.** ZKP3 is unchanged. Real proofs [measured] for `(0, d)` at a non-zero tree position verify for `d = 4242` and `d = u32::MAX`. They are rejected for `d + 1` and for proposal 1. The ZKP1, ZKP2 and ZKP3 `vk_fingerprint_unchanged` tests are a release gate.

**ZKP2 gate.** ZKP2's `proposal_id ≠ 0` inverse gate (`authority_decrement.rs:398-416` [code]) is now **load-bearing**: without it, ZKP2 could emit `VC(0, d)` while keeping full weight. Re-document it, make `proposal_id_zero_fails` a non-ignored proxy soundness test, and require it on every ZKP2 rewrite.

**Builders and exports.** All APIs are ballot-denominated:
- `build_proxy_delegation_proof(..., delegate_index, ballots, dc_slot, next_delegate_index, dc_seed, layout: ShareLayout)`. The builder computes `bound = next_delegate_index − 1` internally, from IAVL-verified state read just before proving, and there is no free bound parameter (CF-1). The public bound then reveals only the registry height at proving time; a caller-chosen `bound = d` would have revealed the delegate.
- `derive_proxy_delegation_transition` (native, proof-free).
- `derive_proxy_share_secrets(dc_seed, ballots, layout)`. PRF: `BLAKE2b-512(personal "ZcashVoteProxyEx", dc_seed ‖ domain ‖ share_index)`. **Layout 0:** denomination split (remainder 0x13), shuffle (0x12), `r_i` from 0x10, `blind_i` from 0x11. **Layout 1:** `shares = [w, 0×15]`, unshuffled, `r_i` from 0x14, `blind_i` from 0x11, mirroring the vote builder's single-share path (`builder.rs:538-550, 812-818` [code]). All 16 ciphertexts and commitments are still built because `shares_hash` covers 16, so the ZKP4 VK, rows (2,015) and proof size (11,008 B) are unchanged. C9 holds because `w ≤ W < 2^30`, and C12 because `w ≠ 0`.
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
- Layout 1: MockProver positives for `w = 1` and for `w = W = 2^30 − 1` with `W_new = 0`; the negative `share0 = 2^30` with `W = 2^30 + 1` (so `W_new = 1` passes C7, and the test asserts that the failing constraint is the C9 range check); a real layout-1 proof under the unchanged VK; a ZKP3 reveal of layout-1 share 0 as `(0, d)`; pinned vectors for 0x14 randomness and the layout-1 `shares_hash`; a check that a layout change alters every `r_i` and blind. [measured] The cloned prototype test `zkp4_single_share_layout_accepts_and_reveals` passes the positives (`w = 4,000`; `w = W = 2^30 − 1`) and the ZKP3 `(0, d)` reveal in 0.74 s (`rev-lastmoment/proto/single_share_test.log`). Its negative case used `W = 2^31`, which also violates C7, so the C9 rejection is not yet isolated.

### 4.2 Chain (vote-sdk v1.7.0)

#### Messages and tags

New tags are 0x09, 0x0B, 0x0C and the injected 0x0F (`MsgSubmitPoolPartialDecryption`, §4.2 Pool disclosure), and 0x04 gains a branch. 0x0F takes an explicit `IsCeremonyTag` branch (`api/codec.go:65-69` [code]), the auto-inject wire decode (`api/codec.go:203-242` [code]), and the same fee-exempt, infinite-gas ceremony ante path as 0x0D. Every new client tag needs:
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

**0x04 `MsgRevealShare` with `proposal_id = 0`.** Accepted if and only if `round.proxy_delegation.enabled` and `1 ≤ d < NextDelegateIndex`; delegate status is ignored. Effects: `AddToTally(round, 0, d)`, `IncrementShareCount(round, 0, d)`, and a `reveal_pool_share{round, delegate_index, share_nf}` event (never `reveal_share`). It counts toward the 256-per-block cap. Errors `ErrProxyDelegationDisabled` and `ErrDelegateNotFound` are permanent. A layout-1 (last-moment) DC contributes one reveal and a layout-0 DC sixteen; neither needs a chain change, and the chain cannot tell them apart (`MsgRevealShare` carries no share index, `proto/svote/v1/tx.proto:120-128` [code]).

**Coordinator payloads.**
- `MsgSetProxyDelegationParams`: `enable_for_new_rounds`, `proxy_dc_paused`, `registration_enabled`, change delay (≥72 h) and recovery delay (≥7 d) as consensus floors, `max_attestation_validity` (≤72 h), `max_key_changes`, `max_delegates` (≤2^30; launch 20,000), and the disclosure params `pool_disclosure_chunk_entries`, `pool_disclosure_timeout` and `pool_disclosure_paused` (§4.2 Pool disclosure).
- `MsgSetProxyDelegateVerifiers`: 1..16 verifiers, unique ids and pubkeys, two thresholds, and a stored `added_at_time`. A verifier may attest only after `added_at_time + recovery_delay`, except in the activation set (IDN-3).
- `MsgSetDelegateSuspension`: immediate, freeze-only, public reason code (1 impersonation, 2 key compromise, 3 legal, 4 verifier review, 99 other). It cannot create entries, change keys or void routes. Its pool is still disclosed after the round.
- **Approval floor (D7, Q5 decided).** These three payload types execute only with at least `max(2, policy.threshold)` distinct vote-manager approvals, so they never execute inside the proposing tx (today a threshold-1 action does, `msg_server_coordinator_actions.go:34-36` [code], and the policy threshold defaults to 1, `docs/vote-coordinator-actions.md:19` [doc]). Activation therefore needs at least 2 vote managers in the coordinator policy before the first proxy payload (§8.3), and the policy reaches 2-of-3 within the first two proxy rounds. Production runs one vote manager today (`scripts/init.sh:27,74-75` [code], per IDN-3).

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
| `0x1F round 0x01 u32be(d)` | `DelegatePoolResult` {delegate_index, total_ballots, revealed_share_count (copied from `ShareCountKey(round,0,d)`), status DISCLOSED/UNDECRYPTABLE, height} |
| `0x20 round 0x01 u32be(c) u32be(vidx)` | `PoolPartialChunk` {validator_index, entries}: at most t per chunk, pruned after the chunk is combined |
| `0x20 round 0x02 u32be(c)` | start `d` of chunk `c` |
| `0x20 round 0x03 u32be(c) u32be(vidx)` | submitter marker (empty), kept until disclosure ends, for the t-submitter and duplicate checks |
| `VoteRound` field 31 | `proxy_delegation {enabled, next_delegate_index_at_creation, pools_routed, routed_pool_count, applied_route_count, disclose_pool_totals (6; true at creation in v1, no toggle), pool_disclosure (7) {state NOT_STARTED/PENDING/COMPLETE/EXPIRED/FAILED/SKIPPED, started_at, started_height, deadline_time, min_disclosure_blocks, pool_count, chunk_entries, chunk_count, chunks_done, pools_disclosed, pools_undecryptable}}` (outside the round-id preimage). Sub-field 7 is written only for proxy rounds |

**Route absence.** A route is absent when `p` is missing from an IAVL-proven `0x1B` set, or when the whole key is proven a non-member, at a height at or after the tally transition. It is never proven through a per-proposal key (OPS-8). A golden key-derivation vector is shared by `keys.go` tests and Vizor's reader.

**Genesis.** One normative GenesisState section covers:
- nullifier types 0-3: `ValidateGenesisState` must accept type 3, because `genesis.go:94-97` rejects anything above 2 today [code];
- every 0x1A key;
- routes, routing bases, proxy-action records and params;
- `delegate_pool_results` and in-flight `pool_partial_chunks` with submitter markers, each carrying its round id and chunk index. Validation: FINALIZED proxy rounds only; chunk index < `chunk_count`; at most t submitters per chunk, all in the ceremony set.

Derived indexes are rebuilt on import. `TestExportImportGenesis` is extended with one finished and one active proxy round, plus a round mid-disclosure that completes with identical results after import.

**Reset rule.** Any chain reset that does not export and import the registry **must change `chain_id`**. That kills replay of old registry and route signatures.

#### Queries, REST and capabilities

- `delegates?start_after=&limit≤1000&updated_since_height=` (backed by 0x1A08, always including 0x1A09 pending entries). `delegates/{index}` and `delegates/by-key/{hex}` serve delegate tooling and explorers, **not** the delegator path.
- `delegate-routes/{round}` (key-paged, `updated_since_height` via 0x1E); `delegate-pools/{round}` (revealed-share counts only, live during the round); `nullifier/{round}/{type 0..3}/{nf}`; `proxy-actions/{round}` and `/{van_nf}` (IAVL-provable; `tx_hash` optional); params; verifiers.
- `ProposalTally` rejects `proposal_id = 0` and ids above `len(proposals)` (OPS-12).
- `delegate-pool-results/{round}?start_after=&limit≤1000`: disclosure status plus results sorted by `d`, rows without a per-row round id. Wallets sync the whole list (about 0.5 MB at 20k pools, less compressed). `delegate-pool-results/{round}/{d}` serves tooling only, never the delegator path. Units are ballots of 0.125 ZEC, as in `TallyResult.total_value`.
- `tally-breakdown/{round}`, computed on read and **not** in consensus: for each (p, o) the total, delegated (Σ routed DISCLOSED pools), direct = total − delegated, the router count, `partial` when a routed pool is undisclosed, and `routing_verified` from a homomorphic recompute of the routed bucket (0x1C base ⊕ routed pools ⊕ `Enc(0; ρ)`; ρ is deterministic from public data). Per proposal, the delegated weight not counted (abstain routes and unrouted pools). The V20 canary and an alert assert `routing_verified`.
- Events: `pool_disclosure_started` (with `expected_blocks = t·C`), `pool_partial_decryption{round, validator_index, chunk_index, entry_count}`, `delegate_pool_result{round, delegate_index, total_ballots, revealed_share_count, status}` (one per pool) and `pool_disclosure_status{round, state, pools_disclosed, undecryptable}`.
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
  | 14 | `delegate_pool_results` (bool) |

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

**Why this is safe.** Pools are final: the transition block admits no reveals or routes (`keeper_voting.go:336-338`, `msgs.go:554-557` [code]). ρ (deterministic; curvey `expand_message_xmd` with BLAKE2b) blocks a forced identity C1 that would fail every DLEQ and time out the round [measured in prototype]. Buckets stay under 1.68·10^8 ballots, below `TallyBSGSBound = 2^28`. Proposal 0 stays excluded from the main partial decryption (0x0D), completeness, `ValidateEntryBounds` and `MsgSubmitTally` [code]; tests lock it in. Pools are decrypted only by the post-FINALIZED disclosure (0x0F). Pool C1 is never identity (`keeper_tally.go:54,69-75` [code]), so disclosure DLEQs cannot be griefed. ρ stays for the combined buckets, and anyone can recompute the routing identity from 0x1C, the pools and ρ.

**Cost.** [measured] 10k delegates × 50 routes took 1.39 s in memory on an M3 Ultra. Benchmark the real IAVL-backed hook on the 2-4 vCPU validator droplets in CI. Launch `max_delegates` is 20,000, raised only with benchmark evidence. Fallback: spread routing over the first K TALLYING blocks, gating partial-decrypt injection on `pools_routed` (OPS-13).

**Reveal close.** Route the routing trigger and reveal acceptance through one keeper function, `revealCloseTime(round)`, that returns `vote_end_time` in v1. It must be a pure refactor that reads only the already-loaded round, with a GasUsed-equality test (OPS-6); otherwise defer it to v1.1. A test pins that pools are routed before any partial decryption is accepted, including any pool-disclosure decryption. This prepares the optional post-close reveal grace window (Q18): reveals only, for votes and DCs alike, accepted until `vote_end + G` while casts, DCs and routes still close at `vote_end`, with routing and every decryption moved after `vote_end + G`. It changes the tally lifecycle for every round and is not in v1.

#### Pool disclosure (post-finalization; D4 revised)

Per-option results never wait on pools. `TallyResults`, `VoteSummary`, `MsgSubmitTally` and 0x0D are unchanged; `GetAllTallyResults` scans `0x07 || round` only (`keeper_tally.go:274-296` [code]), so no proposal-0 entry can ever appear there. Pool totals are decrypted afterwards, in a separate phase whose failure cannot touch the results (decisions 31-36).

**States** (`VoteRound` field 31, sub-field 7 `pool_disclosure`).
- Proxy rounds start NOT_STARTED, move to PENDING, and end COMPLETE or EXPIRED.
- FAILED means the start errored (decision 36). SKIPPED means the main tally timed out; EndBlock step 5 writes it (`module.go:641-662` [code]), for proxy rounds only, behind the const gate.
- Non-proxy rounds never write the sub-field; its absence reads as NOT_APPLICABLE.

**Start.** Inside `SubmitTally`, after the round is set to FINALIZED (`msg_server_tally_decrypt.go:180-183` [code]), and only when `proxy_delegation.enabled && pools_routed`:
- It runs in `ctx.CacheContext()`. On any error: discard the cache, set FAILED, emit `pool_disclosure_status{FAILED}`, and still return success for `MsgSubmitTally`. An error on the tally tx's own path would re-fail every block until the 6 h tally timeout finalizes the round with empty results (`x/vote/types/keys.go:28-31`, `module.go:641-662` [code]).
- On success, freeze `L` = every key under `TallyPrefixForProposal(round, 0)` in ascending `d`. This covers frozen, suspended and revoked delegates and unrouted or abstaining pools, and excludes routing bases (decision 32).
- Store `pool_count P`, `chunk_entries E` (snapshot), `chunk_count C = ceil(P/E)`, the start `d` of each chunk at `0x20 round 0x02 u32be(c)` (IAVL has no positional seek, so this avoids iterating `L` from the start every block), `started_at`, `started_height`, `deadline_time = blockTime + timeout`, and `min_disclosure_blocks = 2·t·C + 100`. Emit `pool_disclosure_started{round, pool_count, chunk_count, chunk_entries, deadline_time, expected_blocks = t·C}`. `P = 0` gives COMPLETE.

**Message (injected tag 0x0F).**

```proto
message MsgSubmitPoolPartialDecryption {
  bytes  vote_round_id   = 1;
  string creator         = 2;  // validator operator; must be the block proposer
  uint32 validator_index = 3;  // ShamirIndex
  uint32 chunk_index     = 4;
  repeated PartialDecryptionEntry entries = 5; // reused type; entries are (0, d)
}
```

0x0D is not reused. It allows one submission per validator per round (`msg_server_tally_decrypt.go:239-247`, `keeper_partial_decrypt.go:84-102` [code]), requires TALLYING and main-accumulator completeness, and its `ValidateEntryBounds` rejects `p < 1` (`keeper_voting.go:291-295` [code]). So 0x0D, its size and its bounds stay untouched, and pools never enter the main injector, which iterates `round.Proposals` only (`prepare_proposal_partial_decrypt.go:152` [code]).

**Injector** (`PoolDisclosurePrepareProposalInjector`, after the 0x0D injector, `app/prepare_proposal.go:70-81` [code]).
- Choose the first PENDING round in which the proposer is a ceremony validator with a loadable share and an eligible chunk: the lowest `c` with fewer than t submitters that this validator has not submitted. Inject exactly one 0x0F.
- A background precompute covers every entry for the node's own share. It uses `max(1, NumCPU−1)` workers, pauses while the node still owes a 0x0D, and copies the share scalar, zeroing only its own copy (today's eviction zeroes the cached scalar in place, `prepare_proposal_partial_decrypt.go:92` [code]). [measured] 0.36-0.41 ms per entry wall on 4 workers: about 8 s and 2 MB at 20k pools.
- The inline fallback is allowed only within the L9-measured budget (`TimeoutPropose = 1.8 s`, `cmd/svoted/cmd/commands.go:47` [code]).
- The injector runs inside its own `recover()`: a PrepareProposal panic makes baseapp return `req.Txs`, which drops every injected tx including 0x0D and `MsgSubmitTally` (cosmos-sdk v0.53.5-valar.3 `baseapp/abci.go:432-440` [code]).

**Share retention.** Today the eviction loop and `cleanOrphanedShareFiles` zero and delete a round's Shamir share at FINALIZED (`prepare_proposal_partial_decrypt.go:79-100, 276-281` [code]). FINALIZED with disclosure PENDING now counts as live; shares are deleted at COMPLETE, EXPIRED, FAILED or SKIPPED. The retention check is a fail-safe helper: it reads field 31 nil-safely, keeps the file on any error, and never returns early from the 0x0D path.

**ProcessProposal** (`validateInjectedPoolPartialDecrypt`, structural only, like `validateInjectedPartialDecrypt`, `app/process_proposal.go:146-197` [code], which does not verify DLEQs):
- round FINALIZED, not `tally_timed_out`, disclosure PENDING, not paused, before expiry;
- creator is the proposer and a ceremony validator with a matching ShamirIndex;
- `chunk_index < C`; fewer than t submitters for the chunk, and not this validator;
- exactly the expected refs, in order from the stored chunk start; 32-byte points and 64-byte proofs;
- at most one 0x0F tx per block.

**Handler.**
1. Re-run every check above. Finish all KV reads before any fan-out.
2. Derive `VK_i` from the Feldman commitments, as `SubmitPartialDecryption` does (`msg_server_tally_decrypt.go:257-279` [code]).
3. Verify every entry with `VerifyPartialDecryptDLEQ(proof, VK_i, C1 of Pool[d], D_i)`. Fan-out across up to 4 goroutines is allowed if deterministic: the error names the lowest failing index. Any failure fails the tx and writes nothing.
4. Store the blob at `0x20 round 0x01 u32be(c) u32be(vidx)`.
5. On the t-th submitter: compute the Lagrange coefficients once for the chunk (all entries share the same t submitters); combine each entry; run BSGS on the shared, lazily built 2^28 table (`crypto/elgamal/bsgs.go:47-70` [code]; 0.19-0.22 s and 1.5 MiB to build [measured]); write `DelegatePoolResult` as DISCLOSED, or UNDECRYPTABLE without failing the tx, so a chunk never gets stuck; delete the chunk's blobs (submitter markers at `0x20 round 0x03` stay until disclosure ends); increment `chunks_done`. COMPLETE when `chunks_done = C`.

**Expiry and brake.** EndBlock step 5b expires a disclosure only when `blockTime ≥ deadline_time` **and** `height ≥ started_height + min_disclosure_blocks`, so a halt or upgrade cannot expire it before 0x0F txs can land. Its round filter must include FINALIZED rounds with disclosure PENDING; today EndBlock skips terminal rounds (`module.go:423-435` [code]). Results already written stay, undisclosed pools have no row, and there is no retry. `pool_disclosure_paused` makes ProcessProposal reject 0x0F while the deadline keeps running.

**Params** (`ProxyDelegationParams`). `pool_disclosure_chunk_entries`: default 250, range 50-500, set from L9; raise the maximum only with L9 evidence or batched verification. `pool_disclosure_timeout`: default 21,600 s, floor 3,600 s, ceiling 72 h. `pool_disclosure_paused`. All are snapshotted at start except `paused`.

**Why this is safe.** Pool C1 is never identity, because pools are `AddToTally` accumulators (`keeper_tally.go:54, 69-75` [code]), so disclosure DLEQs cannot hit the identity-C1 rejection (`crypto/elgamal/dleq.go:95-97` [code]). Exactly t submitters per chunk caps verification at `t·P` entries. A pool of `v` ballots costs `floor(v/16,384) + 1` BSGS giant steps. Weight is conserved and bounded by supply (≤1.68·10^8 ballots), so the whole disclosure needs at most `P + 10,254` giant steps, about 0.37 s at 20k pools [inference from measured 10-12 µs per step]; the worst single value (2^28 − 1) takes 0.19-0.20 s [measured]. Decrypting routing bases would break this bound (base steps scale with proposals × direct turnout), which is one reason decision 32 excludes them.

**Cost and sizes.** [measured] on a loaded M3 Ultra, one core, with the `rev-pooltotals/` harness calling vote-sdk `crypto/elgamal` and `crypto/shamir` unchanged (n = 10, t = 7): 105 B per `PartialDecryptionEntry` in the message; DLEQ verification 0.93-1.72 ms per entry; combine plus BSGS 2.06-2.5 ms per entry. At E = 250 [inference from measured]:

| P pools | Bytes per validator | Chunks C | Submissions t·C | Wall time at 1.2-2.8 s per block | Chain CPU, one core |
|---|---|---|---|---|---|
| 1,000 | 105 KB | 4 | 28 | about 0.6-1.3 min | about 9-14 s |
| 5,000 | 525 KB | 20 | 140 | about 3-6.5 min | about 43-71 s |
| 20,000 | 2.10 MB | 80 | 560 | about 11-26 min | about 171-282 s |

- Blocks with transactions commit at about 1.2 s today (`docs/blocktimes.md` [doc]); 2.8 s is a conservative figure with disclosure work in FinalizeBlock [inference].
- Per chunk at E = 250: about 26 KB raw; verification takes 0.23-0.43 s of FinalizeBlock on one core; the t-th submission adds about 0.5-0.6 s.
- Unchunked, one 20k-pool message is 2,103,576 B [measured], over Comet's 1,000,000 B RPC body, and its verification would take 19-35 s of FinalizeBlock on one core.
- Validators are shared-CPU `s-2vcpu-8gb-amd` and `s-4vcpu-16gb-amd` droplets (`docs/production-setup.md:222-223` [doc]), so the L9 benchmark sets E. At E = 2000 the t-th submission would cost about 6-8.4 s of FinalizeBlock on one core (verification plus combine), against `TimeoutCommit = 950 ms` (`commands.go:48` [code]), which is why the range stops at 500.
- Throughput is about 36 pools per block, so raising `max_delegates` above 20,000 needs L9 evidence that disclosure fits the timeout.
- Fallback if droplets miss the budget: batched DLEQ and MSM verification through the Rust FFI (pasta_curves), a consensus-critical port with byte-for-byte vectors against curvey.

#### Limits, spam and per-block rules

- Slot cap: 16 DCs per registration per round. Per attacker proof this gives 16 reveals, the same as a cast.
- 0x09 caps at 10 DCs and 50 actions. [measured] ZKP1 + 50 casts is 576,769 B raw and 769,100 B as RPC JSON; ZKP1 + 10 DCs + 40 casts is about 577.5 KB raw and 770 KB as RPC JSON. That is about 23% headroom under the 1 MiB REST and 1,000,000 B Comet limits.
- Prepare/ProcessProposal dedupe: at most one registry op per `delegate_index` per block (one per new DIK for `register`), and one route tx per `(round, d)` per block. The global registry-op cap (64) stays as a backstop.
- No per-block proof-verification cap in v1 (decision 24). If one is ever added, it is weight-based, at least 51 proofs per tx, and FIFO across tags.
- At most one 0x0F tx per block; a chunk accepts exactly t submitters.
- The tree-capacity guard returns `ErrCommitmentTreeFull` inside `AppendCommitment`, and `MaxTreePosition` is fixed to 2^24−1.

#### Dormant merges and activation

A `ProxyDelegationEnabled = false` const gates tag decode, interface **and Msg-service** registration, the p=0 branch, routing, new payloads, capability fields and any change to existing validation. It also gates tag 0x0F, the pool-disclosure injector, its ProcessProposal branch, the share-file retention change in `prepare_proposal_partial_decrypt.go`, the `SubmitTally` disclosure-start hook, the EndBlock FINALIZED-round filter and step 5b, and the step-5 SKIPPED write for proxy rounds. While the const is false, field 31 is never written. It is checked **before any new KV read**, so existing paths keep identical GasUsed (OPS-6). Proxy PRs are labeled `V:state/breaking`, never backported to v1.6.x, and ship only in v1.7.0. The activation commit flips the const and registers a no-op `v1_7_0` handler.

### 4.3 Helper (vote-sdk `internal/helper`)

**Proposal-0 branch.** `validatePayload` accepts `proposal_id = 0` with a u32 `vote_decision ≥ 1` and skips the `< 8` option check. For p=0, `verifyCommitment` runs **first** (the leaf at `tree_position` must equal the recomputed DC hash; an absent leaf returns a retryable 503). Only then is `1 ≤ d < NextDelegateIndex` checked, with a permanent distinct error, which `bound` makes unreachable once the leaf exists (LIV-9, OPS-11). Scheduling, store, retries and prover are unchanged; under V2, p=0 routes to the scalar ZKP3 prover. Layout-1 DC payloads need no helper change: one payload, share index 0, `submit_at = 0`, handled like single-share votes (enqueue schedules `submit_at = 0` at arrival, `internal/helper/store.go:459-472, 503-509` [code]; acceptance requires an ACTIVE round, `api.go:328-333` [code]).

**Telemetry hygiene** (PRV-6; also fixes votes today). Remove `proposal_id`, `tree_position` and `submit_at` from span data and never add `vote_decision` or `d`; name HTTP transactions by `mux` route template, not raw path; add a test that fails if span data contains these keys.

**Capacity** (LIV-1, medium). Each DC adds 16 helper proofs to the FIFO shared with votes, and delegators who would not otherwise vote are pure added load. [measured] A queue model (10 operators × 2 workers × 0.58 shares/s = 11.6/s) loses about 20% of both DC and Zodl direct-vote weight at 5k delegators × 3 with deadline-heavy arrivals and 2× duplicate proving. 4 workers per operator remove the loss even at 50k × 3 (dup = 1). The same model also predicts loss for direct votes alone at 5k × 37 proposals, so its inputs may be pessimistic; capacity must be measured, not assumed. Required:
1. Replace the "reduces load" claim with a budget: added reveals = 16 × DCs made before the last-moment window + 1 × DCs made inside it, from delegators who would not otherwise vote. Parity moves part of this load into the window, where it competes with Zodl's last-moment votes and queues behind any older backlog (`docs/helper_submission_invariants.md:27-38` [doc]).
2. Metrics: proofs per unique reveal; queue depth by class (bounded labels).
3. Raise workers to the measured headroom before the first public proxy round.
4. Release gate: in the mixed load scenario, which includes in-window DCs, Zodl direct-vote reveal loss and latency must not regress against the direct-only baseline.

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
- Per bundle: `[ZKP1?] → DC… → casts`. Casts ride along only when the remainder is kept and the roster is terminal; otherwise they follow later as an ordinary 0x06 on the remainder VAN. Inside the last-moment window with `KeepForSelf`, Vizor asks for the remainder's choices before dispatch so the casts ride in the same 0x09; if the user declines, it warns that votes sent later may not be counted.
- The packer uses `max_vote_tx_bytes` from capabilities (default budget 700 KB) and the 50-action total. It never splits a bundle's DCs.
- A `W_new = 0` successor never gets casts (CF-2).
- Proving runs in phases: ZKP1, then all ZKP4s, then ZKP2s. Each proving key is loaded for its phase and evicted before the next. ZKP4 is never pre-warmed at app start, and a peak-RSS budget is part of the device benchmark gate (CF-4).
- **Last-moment parity (D5, O3).** A new allocation or proxy batch is allowed while `host.now_seconds < vote_end_time` and returns `VoteEnded` afterwards, mirroring `vote_work/cast_vote.rs:39-52` [code]; in-flight work advances, and the chain rejects anything landing at `blockTime ≥ vote_end_time` without spending the VAN (`keeper_voting.go:336-338` [code]). `proxy::availability` requires authenticated `ceremony_start_seconds` and `vote_end_time_seconds` in the `RoundHostContext`, because without them the SDK skips `VoteEnded` and `is_last_moment()` returns false (`cast_vote.rs:39`, `vote_work/mod.rs:117-124, 136-138` [code]); committed work still advances (LIV-12).
- **Layout.** Chosen **once per 0x09 batch** at planning: single share (layout 1) if and only if `RoundHostContext::is_last_moment()`, the same call casts use (`cast_vote.rs:53` [code]). The window is `min(40% of round, 6 h)` (`share_policy/timing.rs:19-45` [code]). The layout is persisted with the batch before the first proof and used for every DC and every cast's `DraftVote.single_share` on every re-prove, split-off re-plan and recovery, because `recovery_matches_draft` rejects a cast recovery whose `single_share` differs (`vote.rs:4447-4452` [code]). A layout-0 DC that lands inside the window delivers its 16 shares at `submit_at = 0` (`submission_schedule.rs:128-131` [code]), as a pre-window vote that lands late does. The clock is the same host clock votes use; a clock-skew warning is optional and shared with votes. The rev-1 early close and its 30 min and 10 min constants are deleted.

**Secrets, ARK and recovery hint** (PRV-1).
- **Software accounts:**

  ```
  ARK = BLAKE2b-256(key = PRF^expand(sk_orchard, [t_ARK]), personal "ZVoteProxyARK_v2", net_u8 ‖ round_id)
  ```

  `t_ARK` is a new domain byte registered in the voting ZIP; crypto review confirms it collides with no Orchard or ZIP-32 use.
- **Hardware accounts and imported capabilities:** `ARK = BLAKE2b-256(key = hotkey_secret, personal "ZVoteProxyHRK_v1", net_u8 ‖ round_id)`.
- The ARK is **never derived from the OVK or FVK**, so UFVK holders cannot read delegations. Vizor derives it at commit and stores it in app secure storage keyed by `(account, round)` next to the hotkey.
- Per-action secrets, where `layout` is 0 (standard) or 1 (single share) and `slot` is `dc_slot`:

  ```
  hint_key = BLAKE2b-256(key=ARK, personal "ZVoteProxyHint01", van_nf)
  dc_seed  = BLAKE2b-256(key=ARK, personal "ZVoteProxySeed01", van_nf ‖ d_le32 ‖ w_le64 ‖ layout_u8)   // 45-byte input
  recovery_hint = ChaCha20Poly1305(hint_key, nonce 0^12, aad "ZVoteProxyHint01" ‖ round,
                                   0x01 ‖ layout_u8 ‖ slot_u8 ‖ 0x00 ‖ d_le32 ‖ w_le64)   // 16 B plaintext, 32 B hint
  ```
- `layout` stays out of `hint_key`, so recovery needs one AEAD open per record. Recovery rejects a layout byte other than 0 or 1.
- **Invariant:** the batch layout and each slot's `(d, w, slot, layout)` are persisted before the first proof and reused by every re-prove, split-off re-plan and recovery, so only one plaintext is ever published per hint key, and the zero nonce is safe (SND-8). A released slot (LIV-3) was never POSTed. This rests on policy, not cryptography; optional hardening for audit tranche 2 is a deterministic SIV AEAD (AES-SIV, RFC 5297), exactly 32 B for the 16 B plaintext with no nonce, so a freeze bug would leak only plaintext equality.

**Persistence (schema v25, one-way).** New tables for allocations and entries, batches (with `layout` 0/1, written before proving), slots (with `dc_slot`, `dc_slot_nf` and the batch `layout`, written before proving), DC share plans and deliveries, the recovery cursor, the sticky proof-key set, and delegate identity metadata; two new `chain_submissions` kinds; `ShareKey` gains `CommitmentRef::{Vote, Proxy}`. Effective VAN weight, `floor(total_note_value / 12,500,000) − Σ dispatched or confirmed slot ballots`, replaces `total_note_value` at every VAN-recompute site (`zkp2.rs`, `vote.rs`, `load_van_tree_entries`, tree sync and others). Account deletion removes all proxy tables.

**NextStep and planner obligations.**
- New kinds `ProxyDelegate`, `AdvanceProxyBatch`, `SubmitProxyShares`, `ConfirmProxyShare`, with exhaustive matches. Proxy-only rounds plan ZKP1 with zero ballot intents. A bundle with a proxy batch in flight is held from casts, and vice versa.
- **Release path (LIV-3).** `release_proxy_bundle(bundle)` is allowed when the bundle is `ProxyBlocked{DelegateChanged}` without continuity, or `ChainTerminal`, and none of its batches has a reserved POST or landed tx. It drops those slots, turns their weight into Keep, lifts the hold and records a new `plan_digest` generation; landed slots stay final. To narrow the window, prove every bundle's batch before broadcasting any.
- **Split-off.** "Registration landed, batch rejected" is a normal transition: re-plan the same DCs and casts against the real anchor (CF-5).

**DC shares.** The existing `VoteShareWire` carries `proposal_id = 0` and `vote_decision = d`. Layout 0 sends 16 shares on the standard `submit_at` schedule; layout 1 sends one share (index 0) with `submit_at = 0` to `ceil(N/2)` helpers (`server_order.rs:51-53` [code]). The proxy share planner keys plans by `(round, wallet, bundle, dc_slot)` and passes `single_share = (layout == 1)` explicitly to `plan_share_submissions_with_preferred_servers`. It never infers layout from the payload count, and never reuses the votes-table planner, which is keyed by `proposal_id` (`share_tracking/delivery_plan.rs:44-64, 99-120` [code]). DC shares stay out of `derive_immediate_share` and `validate_round_immediate_plans`. **No DC share is ever the designated immediate share**, so proxy-only rounds have none (PRV-3); inside the window every share, vote or DC, is immediate anyway. "Handed off" means definite acceptance of every DC share (1 or 16) by its target helpers, reported separately from reveal progress (LIV-2). For a DC whose shares were planned with `submit_at = 0` (inside the window, either layout), completion additionally waits for the confirmed reveal of share 0 (§4.5 Job).

**Verification ("did my delegate vote").** Per delegate: ballots, inclusion, shares k/n (n = 16, or 1 for a last-moment DC). Per proposal: `Voted(o) | Abstained | NotYet | DidNotVote`. Before TALLYING, share progress comes from helper share-status, and routes from the whole-round paged list (no per-delegate query). After disclosure COMPLETE, per delegate: the pool total and the approximate delegation count, both from the whole-list `delegate-pool-results` (no per-index query). **Sticky proof set (PRV-5, CMP-19):** at commit the client fixes and persists a 10-key set (chosen delegates plus random decoys; share-nullifier decoys come from other pools' public reveal nullifiers) and proves exactly that set at commit, pre-prove, status and final verification, so intersecting checks gains nothing. The set also IAVL-proves those indices' `0x1F` result keys after disclosure. Share-nullifier IAVL proofs run once after TALLYING, and every sticky key gets the same number `m` of them, with `m = 16 ×` the largest number of real DCs to any one chosen key: real keys are padded with public reveal nullifiers from the same pool, and decoys get `m` from their own pools (all of them if a pool has fewer than `m`), so per-key query counts reveal neither real keys nor layouts (reveal events map each nullifier to its pool). Docs say plainly that helpers and the RPC operator learn the chosen delegates unless Tor is on. Leaderboard and track-record data require capability field 14.

**Recovery.**
- **R0:** the DB is present.
- **R1** (DB lost, hotkey kept; the hotkey supplies the VAN `nk`): derive `van_nf(VAN_0)`, look it up in `0x1D` (round-prefix paging by default, point lookups over Tor), decrypt the hint with the ARK (the layout comes from the hint), and follow the VAN chain. This recovers every slot and the remainder VAN, so remainder voting resumes.
- **R2** (software accounts after seed restore): the seed-derived ARK trial-decrypts the paged `0x1D` feed (about 1 µs per open). On a hit, take the layout from the hint (a byte other than 0 or 1 is rejected), rebuild the DC from `dc_seed(layout)`, **require it to equal the on-chain DC**, then rebuild 1 or 16 payloads and redeliver missing shares. Remainder voting stays lost, as today.

Hardware accounts get R1 only (Q4). Recovery is idempotent; recovered rows are locked.

**Registry client.** Parse the static `proxy_delegation` section; verify the directory index (online/offline key certificate, `seq` monotonicity, expiry, cross-mirror equivocation); mirror `/delegates` (paged, `updated_since_height`). Overlay checks: chain DIK and status match; `subject_commit = H(provider, id, salt)`; Featured needs a valid curator-threshold signature; handles are pinned per favourite and past delegation, with a warning on change. Refuse delegates that are not ACTIVE, are suspended, have a pending **recovery**, have `accepting = false`, or have a removed proof; allow a co-signed pending change with a banner (LIV-10). `proxyAvailability` also requires proven non-membership of the account's gov nullifiers; if they are spent and no local state exists, report "used from another device" and offer R2 (LIV-8).

**Gating.** No `vote_protocol` or `auth_version` bump. Requires `WalletCapabilities.proxy_delegation = ["v1"]`, a dynamic `extensions.proxy_delegation_v1.rounds[id]` entry signed by `trusted_keys` under `zcash-shielded-vote:round-proxy:v1` over binary `RoundProxyAuthPayloadV1`, and chain capabilities whose four circuit fingerprints match the compiled ones. Gates control only preview and commit of **new** allocations; committed work, share delivery and recovery always run (LIV-12). Vizor calls `proxy::availability` and never parses these fields.

**Delegate APIs.** Phrase create and restore (§4.6); DIK/DRK derivation and restore by scanning `i < 8`, `j < 64` against `by-key`; marker text, statement and resolve/attest client; encoders for every digest; route preflight (re-fetch the route set, submit only the unrouted delta), signing and submission; rotate, freeze, revoke, cancel, change identity; the off-chain DIK-signed statement `{accepting, text ≤ 280 chars, no links, consent line}`.

### 4.5 Vizor

**Screens.** Keep the PD-1..PD-13 (delegator) and DG-1..DG-11 (delegate) inventory, with changes:
- PD-1 drops "Delegate more".
- PD-2: the All sort menu adds "Most delegated (last round)" and "Most active", under the PD-14 listing rules. Defaults stay Featured weighted-random and All Random, including in picker mode.
- PD-3 adds a "Track record" section: per finished round, delegated ZEC, "about N delegations", voted N of M, and "suspended during round" if it applies; for the current round, "about N delegations so far".
- PD-4 shows "Voting ends {time}" instead of a cutoff time, and keeps the 24 h "delegates may not have time to vote" notice. `tooLate` is the round-closed state votes use (`now ≥ vote_end`); the rev-1 `end − 10 min` block is removed.
- PD-5/PD-10 have the finality checkbox, an amount-naming CTA, the list of covered proposals, and the lone-delegator notice (copy key "Lone notice") when the delegate's pool has ≤16 revealed shares so far. The copy is probabilistic, because early reveals land at random times and a reveal count cannot tell one standard delegation from several late ones.
- PD-6 completes at "Handed off". For a DC whose shares were planned with `submit_at = 0`, it waits for the confirmed reveal of share 0, as a vote's immediate share does; at `vote_end` it fails with the vote path's expired-share message, and after TALLYING the DC shows "Not counted".
- PD-7 shows shares as k/n (n ∈ {1, 16}), adds a pending-recovery banner and an "on track" state for slow shares, and after COMPLETE "Pool total: X ZEC from about N delegations".
- PD-8 adds the direct versus delegated split per option from `tally-breakdown` (marked partial when incomplete) and "delegated weight that didn't vote".
- **New PD-14 Top delegates** (`/voting/delegates/leaderboard?round=`), reached from PD-2 and PD-8. A finished-round selector with pending ("Totals are being published"), complete, expired (partial), failed and none states. Columns: rank, delegated ZEC, "about N delegations", voted N of M, share of all delegated ZEC; a concentration line such as "Top 5 received 48% of delegated ZEC". Read-only, with no Add buttons. Only listed, unflagged entries show a handle and avatar; others render as `#index · fingerprint` with no avatar; lookalike and new-this-round badges are shown; ranking is by ZEC only. Requires capability field 14. Leaderboard rows are display-only; only the user's own delegates' `0x1F` keys are IAVL-proven.
- DG-2 becomes phrase create/confirm; DG-12 (software account required) is removed; DG-8 shows last round's total; DG-9 drops the side-by-side private-vote column; DG-11 gains "Stop accepting" (a statement) and "Emergency pause" (freeze).

**Job.**
- `VotingSubmissionJobNotifier` gains `kind {ballot, proxy, proxyAndBallot}`, `ProxyJobStage` and `terminalReason`.
- The proxy allocation is recorded before the hardware/software branch, so Keystone and Ledger users who only delegate are still asked to sign ZKP1.
- Cancel is allowed only before the first broadcast.
- COMPLETE requires definite helper acceptance of every DC share and, for DCs whose shares were planned with `submit_at = 0`, the confirmed reveal of share 0. If `vote_end` passes first, the job fails like an unconfirmed immediate vote share (`voting_submission_job_provider.dart:1469-1497` [code]). Without this, a late proxy-only DC would report success, because `hasConfirmedImmediateShare` returns true when there is no designated immediate share (`voting_resume_plan.dart:16-19` [code]).
- Hold a wakelock from proving through hand-off (optional polish).
- On startup or poll open, run R2 for software accounts whose gov nullifiers are spent in a proxy-enabled ACTIVE round with no local state, then redeliver.

**Delegate mode.**
- The phrase root sits in app-level secure storage behind re-auth.
- The DRK is stored with **per-use user presence** (Keychain/Keystore access control), not session unlock (IDN-7).
- Routes need re-auth plus a confirm sheet naming the choice.
- Tor is **default-on** in delegate mode for route submission and for the delegate account's own vote and share traffic. If Tor is off, a blocking prompt appears before the first route or private cast (PRV-7).
- The dashboard shows a blocking banner when any recovery is pending on the user's entry.
- The route deadline is `vote_end_time`, with a warning in the final 5 minutes and a blocking confirm in the final 60 s saying that a route landing after close leaves the whole pool abstaining on that proposal (the transition block rejects routes, `keeper_voting.go:336-338`, `module.go:491-497` [code]; idempotent retries cannot help after close).
- The dashboard shows the delegate's own revealed-share count live (approximate delegations) and, after each round, the published pool total. The rev-1 isolation warning is dropped, because pool totals are published anyway.

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
| C4 | "Your delegate can't see who you are. After voting ends, the total delegated to each delegate is published. If you're the only person delegating to them, that total is your amount, and if you delegate everything it is your full voting balance for the round." |
| C7 | "Helpers add your delegation to the count before voting ends, at random times if you delegate early, or right away in the final hours. Vote servers can see which delegate you chose, but not who you are." |
| C8 | "If you reinstall Vizor before your delegation finishes sending, open Vizor again before voting ends so it can finish." Add for software accounts: "After restoring from your recovery phrase, Vizor can find your delegations." |
| C9 | "Your delegate votes for you on every proposal in this round. Delegate some or all of your voting power to people you trust." |
| Voting ends | "You can delegate until voting ends {time}, just like voting." |
| Late (optional, shared by votes and delegations, final 10 minutes) | "Voting ends in {m} minutes. Votes and delegations sent now may not be counted in time." |
| Not counted | "Voting ended before a helper could count this delegation. It was not counted." |
| Lone notice | "Vizor hasn't seen other delegations to @x yet this round. If you end up the only one, the amount you delegate will be public after voting ends. Your name isn't published, but anyone who knows you delegated to @x could learn your amount." |
| Leaderboard info | "Totals are published by the voting chain after each round. Delegation counts are approximate. A large total doesn't mean a delegate is right." |
| DG-1 additions | "Your public votes stay linked to your X account permanently, even if you delete your post or account." / "After each round, the total delegated to you and an approximate count of delegations are published next to your public votes, permanently." / "Your private votes are hidden from the public. Vote servers can link them to you unless Tor is on." |
| Stop accepting | "Vizor will stop offering you as a delegate. People who already delegated stay with you, and you can still vote publicly." |
| Emergency pause | "Pausing blocks your public votes until you set a new route key with your identity key. Proposals you haven't voted on will count as abstain for everyone who delegated to you." |
| Kept ZEC | "Vote with the ZEC you kept from this device before voting ends." |

Other copy fixes:
- The ZKP1 copy becomes "voting authorization" everywhere (including the Ledger strings).
- Delegation counts are always shown as approximate ("about N", N = `ceil(R/16)` for R revealed shares) and are never a sort key.
- Optional and shared by votes and delegations: a device-clock skew warning when the device is more than 60 s off chain time.

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
| Which delegates a delegator chose | The public, the EA and the delegate, at DC time | **Not hidden from helpers**: each share fans out to `ceil(N/2)` vote servers, about every server, which learn `d`, the DC leaf and IP. The DC count per tx is public. For delegates with very few delegators, the 16 reveal times can narrow which DC tx it was: [measured] median candidate sets of 4, 8, 29 and 112 DC txs at 0.1, 0.25, 1 and 4 DC txs per hour, unique in 17%, 5%, 1% and 0% of trials. A DC made in the last-moment window is revealed within seconds of its tx, which publicly links the tx to its delegates, exactly as a late vote's reveal links its tx to its option; a batch with k late DCs links its k delegates together. |
| Amount per delegation | The public (except a pool's only delegator, after the round), helpers and the delegate | Pool totals and revealed-share counts are published after the round, so a lone delegator's amount is public (identity is not). Pools with several delegators publish only the sum. A ≥t EA coalition decrypts share plaintexts; a last-moment DC is a single share, so the coalition gets its exact amount, as for last-moment votes. |
| A delegator's total and kept weight | The public | Only pool sums are published, and kept weight is voted privately. Exception: a lone delegator who delegated everything has their whole round weight published as the pool total. |
| Delegator's choices from viewing-key holders | UFVK holders | The ARK is never derived from the FVK. |

**Public:** routes; that an anonymous batch contains k DCs; per-delegate pool reveal counts, live during the round (16 per DC made before the last-moment window, 1 per DC made inside it, so delegation counts are approximate); per-delegate pool totals and revealed-share counts (after FINALIZED); exact direct per-option totals (derived as final total minus routed pool totals); delegate registry entries; per-option direct share counts (VoteSummary `BallotCount`).

**Decision 4 (revised) and D5, normative wording for ZIP-PD, the book and the FAQ:**

> After a proxy-enabled round's per-option results are final, the protocol threshold-decrypts every delegate pool that received shares and publishes each delegate's exact pool total and the number of revealed pool shares. Pool totals are public by design. No pool is decrypted during the round.
> (i) If a pool's delegations all came from one delegator, that delegator's amount is public, and if they delegated everything it equals their whole voting weight for the round. Their identity is not published, but anyone who knows out of band that they delegated to that delegate learns the amount, and equal amounts across rounds can link the same delegator.
> (ii) Direct per-option totals are exactly computable as the final total minus routed pool totals.
> (iii) Which delegates a delegator chose is not published, but vote servers learn the delegate of each delegation commitment they process. Reveal timing can narrow which anonymous transaction fed a pool with few commitments. A commitment made in the last-moment window is revealed within seconds of its transaction, which publicly links that transaction to its delegate, as a late vote's reveal links its transaction to its option. So a late commitment that is the only contribution to its pool has its exact amount public and linked by timing to its anonymous transaction.
> (iv) Any coalition holding at least t election-authority key shares can decrypt individual pool shares, and therefore the exact amount of any commitment made in the last-moment window. Validators keep their round key shares until pool disclosure ends.
> (v) Approximate delegation counts are public, live during the round: a pool's revealed-share count R bounds its number of delegation commitments between ceil(R/16) and R.
> Delegations do not have the same privacy as direct votes.

The optional post-close reveal grace window (Q18) would remove the timing link in (iii) for votes and delegations alike; it is not in v1.

*Historical note (rev 1, no longer applies to pool totals):* [measured] with sentinel abstain and Yes/No proposals, 0% of pools were exactly solvable from public results; an explicit Abstain option made 42-44% solvable at 15 proposals, 30 direct voters and 5 delegates. This argued against an Abstain option on privacy grounds. With totals published, Q2 is a product question only.

**Integrity.** A VAN is either cast from or delegated from, never both (one nullifier set). No `VC(0, d)` can exist (the ZKP2 gate, and the chain rejecting casts with p < 1). Weight is conserved over the integers, each share is added once, and each pool lands in at most one option per proposal. **Accountability:** routes are signed, immutable and applied to the whole pool; a delegate cannot drop individual delegators. Pool totals are DLEQ-verified threshold decryptions published on chain, and anyone can recompute the routing identity (`tally-breakdown` `routing_verified`). Not covered: a delegate's private vote may contradict its route, and the X binding is trusted to the verifier.

**Identity attacks and defenses.**

| Attack | Defense |
|---|---|
| X takeover recovery | 7 d delay; DIK-only cancel; recovered keys cannot route rounds that already existed; pending recovery blocks new delegations; banners; a removed verifier voids its pending recoveries |
| Verifier API compromise | The signer re-verifies; independent re-verifier pages |
| Directory key theft | Featured needs curator signatures; wallets pin handles; equivocation checks; offline revocation |
| DRK theft | Per-use presence; DIK rotation; can route this round's unrouted proposals once (accepted) |
| Coordinator key | Consensus delay floors; verifier warm-up; suspension is immediate, freeze-only and public with a reason code; every proxy payload needs at least 2 approvals, reaching 2-of-3 within the first two proxy rounds (D7). Coordinators remain fully trusted, as they already are via x/upgrade. |

**Not provided:** receipt-freeness (a delegator can prove its DC opening; pools are vote-market aggregation points, cf. LobbyFi), and per-delegate censorship resistance (reveals and routes tagged `d` are attributable; helper redundancy and multiple vote servers mitigate). Pool sizes are now publicly verifiable, which makes pools priced aggregation points for vote markets, and leaderboards add herding pressure; the anti-herding defaults and curated leaderboards (§4.5) mitigate only partly.

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
| IDN-3 | medium | One coordinator key can install verifiers and zero delays | Consensus delay floors; verifier warm-up; trust model stated; every proxy payload needs ≥2 approvals, 2-of-3 within two proxy rounds (Q5 decided, D7) |
| IDN-5 | medium | Any post or retweet with the marker binds an account | Original post, whole-template match |
| IDN-6(a,c) | medium | DRK thief blocks recovery; cancels burn change cap | DIK-only RECOVER cancel; cap counts effective changes; 30 d cooldown |
| IDN-11 | medium | Lookalike precedence by registration order; empty Featured; inherited successors | First-seen precedence; rename re-check; reservations; OOB Featured; signed successor links |
| IDN-13, CMP-22 | medium | Conflicting app-store UGC controls | In-app report, `text_state`, one text limit, terms |
| PRV-1 | medium | OVK-keyed ARK exposes delegations to UFVK holders | Spending-key or hotkey ARK |
| PRV-2, CMP-17 | medium | Abstain option creates solvable buckets; claims overstated | Superseded by public pool totals (owner D4 revision): no Abstain mitigation needed; isolation warning dropped; new §5 wording and C4 |
| PRV-4, LIV-4, CMP-11, SND-8 | medium | Five cutoff rules; single-share DCs leak; hint key reuse | Vote parity (D5): one rule shared with casts (open until `vote_end_time`; single share iff `is_last_moment()` at batch planning); layout frozen per batch for DCs and casts, `(d, w, slot, layout)` per `van_nf`, before the first proof; same host clock as votes; authenticated timing required; timing and EA exposure stated as identical to late votes |
| PRV-7 | medium | Vote servers link delegate's private votes to identity | Tor default-on in delegate mode; honest copy |
| LIV-1 | medium | Helper fleet caps scale; Zodl votes also lost | Budget (16 per pre-window DC, 1 per in-window DC), metrics, more workers, Zodl non-regression gate including in-window DCs |
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
| CMP-16, PRV-3 | medium | Immediate DC share links tx to delegate | No designated immediate DC share; window-wide immediacy as for votes; in-window DCs gate completion on their reveal |
| CMP-18, LIV-2 | medium | Recovery unwired; wrong "still counts" copy | R2 in Vizor; COMPLETE on hand-off; new C8 |
| CMP-19, PRV-5 | medium | Per-index lookups leak choices | Whole-list sync (routes and pool results); sticky proof set with equal per-key nullifier counts |
| CMP-23 | medium | Audit scope too narrow | Two tranches |
| CMP-25 | medium | Unlisted owner questions | §7 |
| SND-1, CF-3 | low | Slot nf missing from chain and genesis paths; rand invariant | Full wiring checklist (§4.2); invariant in ZIP-PD |
| SND-6, CF-1 | low | Bound open; width and value rule | Adopted at 30 bits; builder takes `NextDelegateIndex` |
| SND-7 (unv.) | low | Mixed anchor burns a registration | Registration ⇔ anchor 0 |
| SND-4 (unv.) | low | Stolen DRK routes irreversibly | Per-use DRK presence; DIK override deferred |
| SND-5 (unv.) | low | Suspension forces abstention | Kept; owner decided (Q5, D7): immediate, freeze-only, public reason, ≥2 approvals |
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

**Revision 2 rows** (owner changes and their skeptic reviews):

| ID(s) | Sev. | Issue | Resolution |
|---|---|---|---|
| PUB-1 | owner | Per-delegate totals become public (D4 revised) | Post-FINALIZED chunked disclosure (0x0F); results never wait on pools; approximate counts; curated leaderboards; lone-delegator copy; new §5 wording |
| PUB-2 | high | Disclosure start inside `SubmitTally` could fail the tally tx every block until the 6 h timeout wipes results | Start runs in a cache context after FINALIZED; any error records FAILED and the tally still succeeds (decision 36) |
| PUB-3 | medium | Share-retention change sits before the 0x0D path; a panic drops every injection; eviction zeroes the cached scalar in place | Fail-safe retention helper; injector inside its own `recover()`; precompute copies the scalar; tests on non-proxy rounds and malformed field 31 |
| PUB-4 | medium | "Exact delegation count" claimed; the chain counts reveals, not DCs (no share index; missed shares; layout-1 DCs) | Publish `revealed_share_count`; show "about N"; never a sort key; normative (v) bounds DCs between `ceil(R/16)` and R |
| PUB-5 | medium | Decrypting routing bases adds no information and breaks the BSGS step bound inside FinalizeBlock | Bases excluded; direct derived as total minus routed pools; `routing_verified` recompute (decision 32) |
| PUB-6 | medium | Chunk maximum of 2000 is unsafe on shared-CPU droplets | Range 50-500, default 250, set from L9; Lagrange once per chunk; deterministic fan-out |
| PUB-7 | medium | Leaderboards bypass curation and lookalike defenses; counts are cheap to inflate (16 DCs per registration) | Handles and avatars only for listed, unflagged entries; others as `#index · fingerprint`; badges; rank by ZEC only |
| PUB-8 | medium | Privacy wording understated whole-weight exposure, cross-round linkage, out-of-band identification and longer share retention; the client cannot know "no one else delegated" | §5 (i)-(v); probabilistic lone notice; C4 and DG-1 rewritten |
| PUB-9 | low | Non-proxy "NOT_APPLICABLE" writes; SKIPPED writer unstated; O(P) chunk seek; wall-clock-only expiry; first-round selection blocking; precompute CPU; blob growth; throughput vs `max_delegates` | Sub-field written only for proxy rounds; SKIPPED in EndBlock step 5; stored chunk starts; time-and-height expiry, pause param, upgrade halts wait; per-round eligibility; `NumCPU−1` workers paused while a 0x0D is owed; blobs pruned; L9 gate on `max_delegates` |
| LMD-1 | owner | Delegation closed up to 6 h before voting ended | Parity (D5): open until `vote_end_time`; single share in the window; pools routed at close; no chain, helper or VK change |
| LMD-2 | medium | Layout frozen per DC only; casts in a batch re-proved across the window boundary fail `recovery_matches_draft` | Layout persisted once per 0x09 batch before the first proof, for DCs and casts |
| LMD-3 | medium | A late proxy-only DC whose reveal misses close reports success (no designated immediate share) | In-window DC completion waits for the reveal of share 0; fails at `vote_end`; "Not counted" |
| LMD-4 | medium | Sticky-set share-nullifier counts (1 vs 16 per DC) reveal real keys and layouts | Equal count `m` per key, padded from the same pool |
| LMD-5 | medium | Under published totals, a late lone DC's exact amount is tied by timing to its anonymous tx | Accepted as parity with a late sole vote on an option; stated in §5 (iii); grace window later (Q18) |
| LMD-6 | low | The votes-table delivery planner cannot hold DCs; layout inferred from payload count | Proxy planner keyed by `dc_slot`; explicit `single_share` |
| LMD-7 | low | Late `KeepForSelf` remainder needs a second tx-and-reveal cycle | Ask for remainder choices before dispatch so casts ride in the same 0x09; warn if declined |
| LMD-8 | low | Missing round timing silently disables `VoteEnded` and `is_last_moment()` | `proxy::availability` requires authenticated timing for new allocations |
| LMD-9 | low | Route-margin removal shifts risk; load framing optimistic; C9 rejection not isolated; SND-8 safe by policy only; `revealCloseTime` touches shared validation | Final-5-minute warning and 60 s confirm; pass criteria scoped to shares accepted ≥10 min before close; isolated C9 test; optional AES-SIV hint; pure refactor with a GasUsed test, or defer |
| CRD-1 | low | Under D7, incident levers (`proxy_dc_paused`, `pool_disclosure_paused`, suspension) need two coordinator approvals | Two coordinators on call for every proxy round; runbook pre-drafts the lever payloads |

**Partly refuted:** PRV-2's 81% assumed unrouted pools map to Abstain (real figure 42-44%); OPS-4's Zodl visibility claim fails for Zodl ≥3.9.5, which shows only endorsed rounds (only ≤3.13.x throws on unparseable rounds); IDN-8's replay-after-reset (the verifier refuses reused DIKs); LIV-2's "COMPLETE before delivery"; IDN-6(c)'s 64-cycle exhaustion; IDN-3's "new trust" (coordinators can already push binaries); OPS-7's reveal pause (unimplementable); CMP-3's on-chain X ids (no proto field exists).

---

## 7. Owner questions

### 7.1 Decided

| # | Decision | Where |
|---|---|---|
| Q1 | **Answered (rev 2): delegation stays open until voting ends, like voting.** DCs in a batch planned in the last-moment window use the vote path's single-share layout and are sent at once; pools are routed at close with every reveal that landed before `vote_end_time`. As with votes, a delegation whose reveal misses close is not counted, and Vizor says so. The rev-1 early close at `vote_end − max(buffer, 30 min)` is withdrawn. | D5, O3, §4.4 |
| Q5 | **Accepted (rev 2): coordinator suspension with safeguards.** Immediate, freeze-only, with a public reason code; it cannot create entries, change keys or void routes. Every proxy payload (params, verifiers, suspension) needs at least 2 vote-manager approvals regardless of the global threshold; at least 2 vote managers before activation, and 2-of-3 within the first two proxy rounds. | D7, §4.2, §8.3 |
| Q8 | **Decided in part (rev 2): totals are public and leaderboards are allowed.** Remaining defaults: totals and approximate delegation counts on profiles, PD-14 and the delegate dashboard after each round, and "about N delegations so far" on profiles during the round. The default directory order stays Featured weighted-random and All Random. "Most delegated" is opt-in, never default, ranks by ZEC only, and shows handles only for listed entries. | D4, §4.5 |
| O4 | **Confirmed: a separate delegate key phrase.** | D6, §4.6 |

### 7.2 Open

| # | Question | Recommended default |
|---|---|---|
| Q2 | Add an explicit "Abstain" option to proposals in proxy rounds, for product reasons? Privacy no longer argues against it, but Zodl must present it. | No in v1, for compatibility. Keep the recorded-not-counted sentinel. |
| Q3 | Minimum per delegate | 1 ballot (0.125 ZEC) protocol floor and round default. Raise `min_ballots_per_delegate` per round only if load tests show pressure. |
| Q4 | Offer Keystone/Ledger delegators opt-in recovery keyed to the viewing key? Viewing-key holders would see their delegations. | Not in v1. Hardware delegators keep R1 only, the same as votes today. |
| Q6 | Verifier trust at launch | 1-of-1 Valar for registration; recover threshold 2 (two Valar keys, two-person approval); an independent second verifier within 6 months |
| Q7 | Beta venue | Stage chain. Mainnet test rounds only within old-Zodl limits (≤15 proposals, 2-8 contiguous options), never Zodl-endorsed. |
| Q9 | Featured and avatar policy | ≤30 Featured, 2-of-3 curators (Valar, Vizor), public log, OOB confirmation for launch entries. Human review for featured avatars, automated checks for others. |
| Q10 | May other wallets consume the X-hydrated profiles doc? | Registry doc open now; profiles doc Vizor-only until counsel clears it. |
| Q11 | Who handles reports, and how fast? | Valar support; 24 h for impersonation and offensive content; same-day delisting when confirmed. |
| Q12 | Per-round enablement | Opt-in per round; enable for all public rounds after one successful mainnet proxy round. |
| Q13 | Ship follow-mode v0 (directory plus signed voting guides) a round early? | Only if the verifier MVP is early and it does not touch the circuit path. Do not plan on it. |
| Q14 | X API budget: de-hydrate dormant delegates (no route in 3 rounds, not featured)? | Yes (about $1k a month at 10k delegates instead of about $3.4k). |
| Q15 | Top-ups ("Delegate more") | Not in v1; append-only allocation generations in v1.1. |
| Q16 | If pool disclosure expires or fails (for example a validator outage), accept partially published totals with no retry? | Yes. Per-option results are unaffected; the gap shows as "expired (partial)" on PD-14 and track records. |
| Q17 | Leaderboard ranking basis | Last finished round (default), not a trailing three-round window. |
| Q18 | Post-close reveal grace window G (reveals only, accepted until `vote_end + G`, before routing and any decryption, including pool disclosure), shared by votes and delegations? It removes the timing link of late votes and DCs and lets helper backlog drain. | Not in v1. Design for v1.1 after load-lab data, as a separate coordinated upgrade, with Zodl notified (results arrive G later). v1 only adds the `revealCloseTime` refactor (§4.2). |

---

## 8. Rollout and milestones

### 8.1 Work packages

Sizes: S is 1-3 days, M is 1-2 eng-weeks, L is 2-4 eng-weeks. Per-repo totals are rough.

- **Specs (chain lead, spec lead):** SP1 `contracts-v1` (M, weeks 0-3): proto, JSON names, tags, REST, digests, KV table, capabilities, errors and vocabulary, FRB DTOs, Go and Rust golden vectors in CI; **blocks every implementation PR**. SP2: ZIP-A errata (S), ZIP-PD (M; ZKP4 statement frozen by week 3; its timing section, spec_rollout §1.4.11, is rewritten for parity: delete "SHOULD finish before vote_end − buffer" and "MUST NOT start after vote_end − 30 min"; the wallet MUST use the vote path's last-moment predicate and `VoteEnded` check, MUST require authenticated round timing for new allocations, and MUST freeze the batch layout and `(d, w, slot, layout)` per `van_nf` before the first proof; it also carries the §5 disclosure wording), ZIP-DR (M; states the coordinator trust model and the D7 approval floor), WAPI/SUB/SETUP amendments (S), book rewrite (M, to week 15).
- **voting-circuits (about 8-9 eng-weeks):** constants (S); ZKP4 with C14 and C15 (M); MockProver suite (M); builders with the `dc_seed` PRF (M); layout-1 secrets, 0x14 path and vectors (S); prove, verify and fingerprints (S); exports and vectors (S); cross-circuit tests (S); benchmarks with mobile RSS (S). rc.1 by week 9.
- **vote-sdk (about 23-24 eng-weeks):** proto and dormant gating (S); types and digests (M); capacity guard (S); registry (L); routes (M); ZKP4 FFI (M); 0x09 (L); p=0 reveal (S); round snapshot, params, pause (S); routing hook (M); queries, indexes, feed, capabilities (M); genesis (M); helper branch, telemetry, metrics (M); upgrade and runbooks (S); e2e (L); determinism and droplet benchmark (S); corpus replay gate (S); activation (S); D7 approval floor for proxy payloads (S); pool disclosure: 0x0F, injector with background precompute, fail-safe share retention, infallible start, handler with chain-side combine and BSGS, 0x1F/0x20 store with pruning, EndBlock expiry and SKIPPED, pause param, queries, `tally-breakdown`, events, genesis (M-L, about 3-4 eng-weeks); disclosure benchmark L9 (S). **V20 ops (M):** CLI and coordinator-UI support for new payloads, including multi-approval proxy payloads; Prometheus metrics with an alert on permanent p=0 rejections and on disclosure EXPIRED, FAILED or UNDECRYPTABLE; a scripted canary delegate and delegator per proxy round (one pre-window DC and one in-window DC at about T−10 min) asserting `tally-breakdown` `routing_verified` and that pool totals match the canary's plaintext model; explorer event docs.
- **zcash_voting (about 19-20 eng-weeks):** planner (M); secrets, phrase, digests (M); circuits integration and phased proving (M); schema v25 (L); effective-weight refactor (L); batch builder and packer (L); submission lifecycle and split-off (L); planner obligations and release path (L); DC share delivery (M); verification and sticky proofs (M); recovery (M); gating (M); directory client (M); delegate APIs and crate (M); integration, vectors, mobile benchmarks (M); layout choice, batch freeze, explicit-layout share planner, equal-count sticky proofs (S-M); pool results client and sticky-set `0x1F` proofs (S).
- **Verifier service (about 10 eng-weeks):** API and providers (L); hardened signer and key ceremony (M); indexer, re-verifier, refresher, retention (M); avatar pipeline (M); publisher, mirrors, curator signing (M); curation, reports, moderation (M); ops (M). Legal review runs externally in weeks 4-10 and gates the X provider.
- **Config repo** (S) and **deeplink server** (S).
- **Vizor (about 16 eng-weeks):** UI on mocks from week 4, integration from week 11. Revision 2 adds PD-14 with listing rules, track record, breakdown and copy (M), and in-window DC completion gating with the "Not counted" state (S).
- **Stage, load lab and QA (about 6 eng-weeks, including L9-L12); specs and book (about 6-7).**

### 8.2 Timeline and critical path

| Milestone | Weeks | Notes |
|---|---|---|
| M0 contracts-v1, ZKP4 statement, ZIP-DR digests frozen | 0-3 | Critical |
| M1 voting-circuits 0.13.0-rc.1 | 2-9 | Critical |
| M2a Audit tranche 1: ZKP4 (both layouts), 0x09, p=0 reveal, routing and ρ, registry handlers, pool disclosure (chunk rules, chain-side Lagrange and BSGS, infallible start, share retention, determinism of parallel verification) | 9-13 | Critical; book auditors now, with the disclosure scope |
| M2b Audit tranche 2: client secrets and recovery (including layout-1 `dc_seed` and hint vectors, and the optional SIV hint), phrase format, registry digests and attestation counting, signer custody, directory signing, Rust image decode, IAVL verification | 12-15 | Launch gate |
| M3 vote-sdk dormant merges plus helper | 3-12 | Registry and routes need no circuits; pool disclosure needs the chain team at 3 engineers to hold week 12 |
| M4 Verifier MVP (X, GitHub, DNS), then hardening (IDN-2) | 3-10, 10-12 | |
| M5 zcash_voting | 4-13 | Needs M1 rc |
| M6 Vizor | 4-16 | |
| M7 Config, deeplink, legal sign-off | 6-10 | |
| M8 Stage activation; T1, T2, T3; load lab; capacity gate | 13-17 | Critical |
| M9 Mainnet activation between rounds | 17-18 | Critical |
| M10 First public proxy round | 18-19 | |

- **Critical path:** M0 → M1 → M2a → final tags (0.13.0, v1.7.0, zcash_voting, Vizor) → M8 → M9 → M10, about 19 weeks.
- M2b and the legal sign-off run on parallel paths that must close before M8 ends.
- **Total:** about 88-93 eng-weeks plus two audits [inference]. The earlier 60 eng-week estimate predates the scope the review added (verifier hardening, recovery records, ops tooling, the second audit tranche); revision 2 adds about 6-8 (pool disclosure, leaderboards, last-moment parity).
- **Schedule risk:** the larger tranche-1 scope could push M2a if auditors are booked tightly; book them with the disclosure scope now.
- **Staffing:** circuits 1-2, chain 3, client 2, Vizor 2, identity 2, specs/QA 1.

### 8.3 Activation

1. Merge dormant PRs to main under `V:state/breaking`.
2. Run the corpus replay gate: the candidate binary's FFI verifiers check every vote tx from recent mainnet rounds (ZKP1/2/3 proofs, RedPallas, TX1 sighash) with byte-identical accept/reject results against v1.6.x. This covers the Zakura 2.0 dependency swap (OPS-5).
3. Run the coordinated v1.7.0 halt **between rounds**: all rounds FINALIZED, no pool disclosure PENDING, helper queues empty. Every later upgrade halt follows the same rule.
4. Post-checks: capabilities on (including field 14), the four fingerprints match, the registry is empty.
5. Coordinator actions: confirm at least 2 vote managers in the coordinator policy (add one with `MsgUpdateVoteManagers` if needed), because every proxy payload needs 2 approvals (D7); then set the verifier set and params (`enable_for_new_rounds` false). Reach 2-of-3 within the first two proxy rounds.
6. Register the canary and featured delegates. Publish the directory to three mirrors.
7. Per round: change `enable_for_new_rounds` only when no create-session action is pending, verify the created round's snapshot, then sign the config extension.

**Rollback.**
- No binary rollback after the first proxy tx.
- Incident levers: `proxy_dc_paused` (stops new DCs; existing DCs, reveals and routes stand), `pool_disclosure_paused` (stops 0x0F injection; the deadline keeps running; results unaffected), disable for new rounds, and the Vizor kill switch. Under D7 each param lever needs two coordinator approvals, so two coordinators are on call for every proxy round (CRD-1).

### 8.4 Stage and beta

- **T1 (internal):** Vizor (software, Keystone, Ledger) plus a current Zodl build and an old Vizor build in one proxy round. Includes a Keystone-signed registration and an in-window DC at about T−10 min alongside a pre-window DC. A scripted check confirms `tally-breakdown` `routing_verified = true` and that pool totals match the plaintext model.
- **T2 (delegate beta):** 10-20 real influencers through the stage verifier.
- **T3:** the adversarial list plus the load matrix on the ten-validator lab, including: a DC landing in the transition block is rejected with the VAN unspent; a batch re-proved across the window boundary keeps one layout.
- Then one mainnet proxy round per Q7 and Q12.

### 8.5 Zodl coordination

Send a written notice listing what Zodl will misreport:
- **M1:** totals include delegated weight; per-delegate totals and a direct/delegated breakdown are published via new queries Zodl does not read.
- **M2:** `VoteSummary` share counts exclude pools.
- **M3:** a seed that delegated in Vizor shows "already used" in Zodl.
- **M4:** old Vizor classifies "delegated" as "voted".
- **M5:** explorers see new event types.
- **M6:** DB v25 cannot be downgraded.
- **M7:** injected 0x0F txs and new pool events appear in blocks after FINALIZED, and `VoteRound` field 31 gains sub-fields 6 and 7.

Zodl maintainers then confirm on a current production build that extra round fields and `extensions` are ignored, and sign off before the first proxy round. Zodl builds 3.13.x and earlier throw on rounds they cannot parse, so mainnet test rounds must stay within that parser's limits until those builds age out. Optional Zodl copy: "Totals include votes cast by public delegates."

### 8.6 Private-vote V2 coordination

Acceptance criteria on the V2 PR, added now: keep the v1 scalar ZKP3 byte-identical as `pool_share_reveal` (VK pinned); keep v1 `shares_hash`, El Gamal gadgets and `DOMAIN_VC` live; keep the compact ZKP2's `p ≠ 0` gate, VAN format, nullifier tag and `rand` invariant; keep PRF domains 0x10-0x14 assigned to proxy secrets (0x14 is live for layout 1); put vector reveals on a new tag and restrict 0x04 to p=0 in V2 rounds; use per-circuit proof caps (15 KiB for ZKP1-4); pack V2 casts by byte budget (about 11 per tx [measured]); count 0x04 and the V2 reveal tag in one per-block cap; extend the name-keyed fingerprint list; pin ZKP4 and `pool_share_reveal` fingerprints in V2 CI.

---

## 9. Test strategy and launch checklist

### 9.1 Tests by layer

- **Circuits:** the §4.1 suite, including the layout-1 tests; ZKP1-3 `vk_fingerprint_unchanged` (release gate); real-proof size ≤15 KiB; CI benchmarks.
- **Chain:** slot nf duplicates (intra-message, across txs in CheckTx and RecheckTx, genesis type 3); registration ⇔ anchor 0 in both directions; D1, route and registry digest golden vectors (Go = Rust = e2e `sighash.rs`); prefix-free `SVOTE_` domains; ZIP-215 torsion vectors; `bound ≥ NextDelegateIndex` rejected and registry capped at 2^30; p=0 reveals accepted for frozen, suspended and revoked delegates and rejected for `d ≥ Next`; proposal 0 never in VoteSummary, TallyResults, 0x0D partials, completeness or `SubmitTally`, and `ProposalTally(0)` rejected; routing conservation against a plaintext model, byte-identical output under reversed order, the identity-C1 vector, `pools_routed` once; registry rules (no unfreeze, freeze replay after rotation fails, DRK cannot cancel RECOVER, removed verifier voids recovery, recovered entry cannot route older rounds, verifier warm-up, delay floors, per-block dedupe, idempotent route entries); dormancy (const before KV reads); two-node determinism across the transition block; genesis round trip with a finished and an active proxy round; droplet routing benchmark; D7: proxy payloads never execute below 2 approvals, even at policy threshold 1.
- **Chain, pool disclosure:** `L` is exact (no routing bases) and results match a plaintext model (proptest); TallyResults and VoteSummary are byte-identical before and after disclosure; a p = 0 entry in `MsgSubmitTally` is rejected; a start fault injected after FINALIZED leaves TallyResults byte-identical and records FAILED; a malformed field 31 still lets 0x0D and `MsgSubmitTally` be injected; non-proxy rounds never write sub-field 7 and keep identical GasUsed; chunk range, order and stored chunk start; duplicate, (t+1)-th, multiple-0x0F, paused and non-proxy-round rejections; non-proposer creator; a bad DLEQ writes nothing; the t-th submission writes results and prunes blobs; UNDECRYPTABLE via a test hook leaves the tx successful; EXPIRED needs both the time and height conditions, survives a simulated halt, keeps partial results and leaves TallyResults untouched; SKIPPED follows a tally timeout; share files are kept while PENDING and deleted afterwards; round selection skips a round the proposer cannot serve; GOMAXPROCS 1 and 8 give the same app hash and GasUsed; a whale pool near 1.68·10^8 ballots decrypts; the BSGS table is built lazily on a non-validator; genesis round trip mid-disclosure; dormancy; capability 14; pool C1 is never identity; pools are routed before any partial decryption is accepted (the `revealCloseTime` ordering test).
- **Helper:** leaf-first ordering with 503 on an absent leaf; a layout-1 DC payload (one share, `submit_at = 0`) is scheduled at arrival and revealed; span-data key test; capacity metrics.
- **e2e:** register; create round; 0x09 with and without registration; helper reveals; routes including abstain and a missing route; `tally-breakdown` `routing_verified = true` and per-pool totals match the plaintext model; split-off recovery; R1 and R2 against `0x1D`; a Zodl-style flow in the same round; a layout-1 DC landing at T−60 s is revealed and routed and raises the pool share count by 1; a pool reveal in the transition block is rejected and routing completes; a combined 0x09 with a registration works inside the window.
- **zcash_voting:** planner proptests (conservation, `D ≤ #DC ≤ D + B − 1`, within-1 quota); hint, ARK and `dc_seed` vectors; delegate phrases never validate as BIP-39 and restore rejects BIP-39; release-path state machine; no designated immediate DC share; `VoteEnded` at `now ≥ vote_end` while in-flight work advances, and no new allocation without authenticated timing; layout = 1 iff `is_last_moment()` at batch planning; batch layout persisted before the first proof and reused for DCs and casts across terminal-rejection re-prove, split-off re-plan and recovery, with byte-identical hint and DC; a layout-0 DC dispatched in the window plans 16 shares at `submit_at = 0`; layout-1 delivery plans exactly one payload at `submit_at = 0` with explicit `single_share`; hint and `dc_seed` vectors for layout 1; R1 and R2 rebuild layout-1 DCs; the rev-1 early-close tests are deleted; sticky proof set identical across checks, with equal per-key share-nullifier query counts and sticky-set `0x1F` proofs; whole-list `delegate-pool-results` only; no per-index REST on delegator paths; gates never stop committed work; v24→v25 migration preserves rows; mobile benchmarks with peak RSS.
- **Vizor:** widget tests for every PD and DG state; `tooLate` only after `vote_end` and no 10-minute block; PD-6 waits for the in-window DC reveal and fails at `vote_end`; PD-7 shows 1/1; PD-14 states, listing rules, and a default order that is never popularity; the rev-2 copy keys; the optional shared late and skew warnings appear for both flows; `Image.network`/codec ban; route-table audit; kill switch both ways, including an in-flight DC; deletion cleanup; deep-link fragment parsing and mismatch blocking; accessibility.
- **Verifier:** reply, quote, retweet and template-mismatch proofs rejected; eTLD+1 uniqueness; signer refuses mismatched fields; re-verifier pages on a forged REGISTER; purge clears historical objects on every mirror.
- **Adversarial (stage T3):** ZKP4 after a partial vote; ZKP2 and ZKP4 on one VAN; one VAN twice in a batch; field-wrap inflation; a VC revealed as proposal 0 and a DC as proposal p; proposal-0 entries in 0x0D or `SubmitTally`; forged or out-of-range pool chunk; extra chunk submitter; pool partials withheld until expiry; 16-DC self-delegation count inflation shown as approximate; an unlisted lookalike with a large pool on PD-14; a DC landing in the transition block (rejected, VAN unspent); a batch re-proved across the window boundary; route replay, race, and routes from revoked or rotated keys; registry spam, expired-attestation reuse, double-counted signers; reveals to unregistered indices; targeted helper censorship of one index; non-canonical encodings; tree fill; oversized batch; snapshot equivocation; malicious avatar; deep link carrying an address; lookalike of a Featured handle; X takeover recovery; mixed-anchor front-running; freeze replay; DRK cancel of recovery; directory-key relabelling; retweet binding; subdomain sybils; fresh-signature route floods; Zodl and old Vizor in the same round.

**Load matrix:**

| Scenario | Mix |
|---|---|
| L1 | 5k direct voters × 10 proposals |
| L1b | 2k direct voters × 37 proposals, deadline-heavy |
| L2 | L1 plus 10k delegators × 3, uniform arrivals |
| L3 | L2 with deadline-heavy arrivals, including in-window DCs at 1 reveal each |
| L4 | 50k delegators × 3, deadline-heavy, including in-window DCs |
| L5 | Routing with 20k delegates × 50 proposals on validator hardware |
| L6 | 100 batches × 10 DCs in one block |
| L7 | Directory CDN at 50k wallets |
| L8 | Verifier spike at the daily cap |
| L9 | Pool disclosure at 1k, 5k and 20k pools on 2- and 4-vCPU droplets, 10 validators, t = 7 |
| L10 | L9 at 20k concurrent with L1 voting and another round's TALLYING |
| L11 | Whale pool distribution |
| L12 | Exactly t validators online during disclosure |

Pass criteria:
- every honest share accepted by helpers at least 10 min before `vote_end_time` is revealed in time with at least 30% headroom; for later shares, the reveal-before-close rate and latency are reported by class (vote, pre-window DC, in-window DC), because no fleet can guarantee shares posted in the final seconds;
- Zodl direct-vote loss and latency do not regress against L1/L1b, with in-window DCs present;
- block time p99 is at most 3 s;
- routing EndBlock is within the budget set from the droplet benchmark;
- no app-hash divergence;
- proofs per unique reveal are measured and reported;
- disclosure COMPLETE within 2 h at 20k pools; the disclosure injector adds ≤ 0.1 s to PrepareProposal with a warm cache; the t-th-chunk FinalizeBlock stays within the block budget; main tally finalization latency is unchanged against a non-proxy baseline; `pool_disclosure_chunk_entries` is set from L9.

### 9.2 Launch checklist

- [ ] contracts-v1 frozen. Golden vectors pass in Go, Rust and e2e. ZIP-PD and ZIP-DR published. Book privacy and threat pages carry the §5 wording.
- [ ] Both audit tranches closed. voting-circuits 0.13.0, vote-sdk v1.7.0, zcash_voting and the Vizor store release are final. Fingerprints for all four circuits published.
- [ ] Corpus replay gate passed. Stage T1, T2 and T3 passed, including Zodl and old Vizor, `routing_verified`, pool totals against the plaintext model, and an in-window DC.
- [ ] Load gate passed. Helper workers set to the measured headroom. Disclosure benchmark (L9) passed and chunk size set.
- [ ] Mainnet upgrade applied between rounds, with no disclosure PENDING; the upgrade runbook waits for that on every later halt. Capabilities verified (fields 4-14). At least 2 vote managers in the coordinator policy; proxy payloads need 2 approvals. Verifier set (1 register, 2 recover) and params set. Delay floors confirmed. `max_delegates` 20,000.
- [ ] Verifier hardening live: signer re-verification, independent re-verifier, read-only publisher replica. Legal sign-off recorded. Purge SLA tested end to end, including all three mirrors.
- [ ] Featured delegates OOB-confirmed and curator-signed. Reserved-names list seeded. Directory on three mirrors.
- [ ] Static pin and per-round extension signed and CI-verified. Old pins untouched. `supported_versions` unchanged.
- [ ] Zodl written notice sent and sign-off received.
- [ ] Kill switch, `proxy_dc_paused` and `pool_disclosure_paused` tested. Two coordinators on call for each proxy round.
- [ ] Dashboards live: pool reveals, routes, helper queue by class, permanent p=0 rejections, verifier and re-verifier health, X API errors, disclosure progress, and EXPIRED, FAILED and UNDECRYPTABLE alerts.
- [ ] Canary delegate and delegator scheduled for the first round, with a pre-window and an in-window DC; the canary asserts `routing_verified` and pool totals.
- [ ] Public docs published: delegator FAQ (finality, abstain, last-moment behavior, which is the same as voting, including that a delegation sent in the final minute may not be counted; what is public: per-delegate totals, approximate delegation counts, lone-delegator amounts including whole-balance exposure; device loss), delegate guide (phrase safety, Tor, public and permanent attribution, published totals and approximate counts), no-receipt-freeness disclosure, support runbook with escalation to coordinators.
- [ ] App-store UGC review passed (report, hide, moderation, contact, terms).
- [ ] On-call owners named for chain, helper, verifier and Vizor.
