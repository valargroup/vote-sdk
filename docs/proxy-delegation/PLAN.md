# Proxy delegation: final implementation plan

Status: final plan for implementation, revision 3, 2026-10-05. It supersedes integrated-design-v1 and the six component specs wherever they disagree. It folds in seven adversarial review lenses (soundness, privacy, liveness, identity, chain ops, circuit feasibility, completeness) and their skeptic verdicts, the owner's revision-2 feedback and its two skeptic reviews, the owner's revision-3 decisions, and an evaluation of label-free pool reveals.

Evidence labels: [code] read in a repo, [doc] from a spec or doc, [measured] from a prototype run, [inference] reasoned. Repos: **vote-sdk** (chain, helper, FFI), **voting-circuits**, **zcash_voting** (client SDK), **Vizor**, **verifier service** (new repo), **token-holder-voting-config**, **vizor-deeplink-server**, **zips/book**.

Terms: *registration* is today's ZKP1 (notes to a VAN, shown in Vizor as "voting authorization"). *Proxy delegation* is the new feature. A *DC* (delegation commitment) is the leaf a delegator creates. *Pool[d]* is the El Gamal accumulator `TallyKey(round, 0, d)`. A *route* is a delegate's public choice on one proposal. *DIK* and *DRK* are the delegate's identity key and route key. The *last-moment window* is the final `min(40% of the round, 6 h)` before `vote_end_time` (`share_policy/timing.rs:19-24, 61-68` [code]). *Layout 0* is the standard 16-share DC; *layout 1* is the last-moment single-share DC. The *vetted list* is the curator-signed set of delegates shown in the directory's browse list.

### Revision history

- **Rev 1:** initial plan.
- **Rev 2:** last-moment delegation parity, published per-delegate totals, and coordinator suspension safeguards.
- **Rev 3 (this revision):** per-delegate totals are hidden again and no pool is ever decrypted on its own, which reverts rev 2's published totals together with their post-round decryption phase and leaderboards (D4); one delegation per round, never changeable (D2); at most 8 delegates per wallet and 8 DCs per registration per round (D8); a mid-round off switch (D9); a vetted-only browse list with exact search for every other delegate (D3); mid-round registration stated (D10); no small-pool notice (D11); a validator trust note (§5); and a decision to keep labeled pool reveals (decision 31). Estimate: about 83-86 eng-weeks [inference].

---

## 1. Summary

1. **What we are building.** In proxy-enabled rounds, a Vizor user can give some or all of their round voting weight to 1 to 8 registered delegates and keep the rest to vote privately. Delegates vote publicly, one route per proposal. Delegated weight is counted through encrypted per-delegate pools. No pool is ever decrypted on its own; only the per-option totals, which include routed pools, are decrypted.
2. **Why.** Many holders do not want to read every proposal. Today their weight simply goes unused. Public delegates with verifiable route records give that weight a voice without revealing who the delegators are, how much each gave, or how much each delegate received.
3. **Circuits.** One new additive circuit, ZKP4, spends a full-authority VAN. It outputs a DC that commits to a hidden delegate index `d` and an amount `w`, plus a successor VAN holding `W − w`. ZKP1, ZKP2 and ZKP3 VKs stay byte-identical, so Zodl and old Vizor keep working unchanged.
4. **Pools.** Each DC is split into 16 El Gamal shares or, when its batch is planned in the last-moment window, one share holding the whole amount, exactly as votes are. Existing helpers reveal them with the **unchanged ZKP3** as `(proposal 0, decision d)`, and the chain adds them into `Pool[d]`.
5. **Tally.** At the ACTIVE→TALLYING EndBlock, the chain adds each pool into the option bucket its delegate routed, plus a deterministic `Enc(0; ρ)` re-randomizer. Threshold decryption of per-option results then proceeds exactly as today. Pools are never decrypted separately, so per-delegate totals stay hidden. Unrouted proposals abstain.
6. **Delegate identity.** A dedicated delegate key phrase derives two Ed25519 keys: a DIK (identity, can be kept cold) and a DRK (routes, hot). The delegate proves control of an X account (or a GitHub account or domain) with a marker post. A Valar verifier issues a threshold attestation. The chain registry stores keys, status and a salted commitment only. No X content goes on chain. Delegates can register at any time, including mid-round.
7. **Discovery.** Wallets download a signed directory: a registry doc, a profiles doc, and avatar packs re-encoded server-side. The browse list shows only delegates the curators vetted (2-of-3 Valar and Vizor signatures). Any other registered delegate is reachable only by exact search on X handle, GitHub handle, domain or key fingerprint, or by a deep link, and appears with no profile picture or display text. Search runs locally on the device. Fingerprints and lookalike warnings appear on every delegate surface. There is no leaderboard.
8. **Compatibility and control.** Gating is additive only: no `vote_protocol` or `auth_version` bump. The feature turns on through chain capabilities plus a VK fingerprint match, a signed per-round config extension, and a Vizor kill switch. Mid-round, a coordinator pause makes the chain reject new delegations and the kill switch stops new ones in Vizor, without touching delegations already made (§8.3).
9. **Delivery.** voting-circuits 0.13.0, vote-sdk v1.7.0 (dormant merges, one coordinated activation), zcash_voting, a new verifier service, and Vizor, with two audit tranches and stage rounds first. The critical path is about 19 weeks.
10. **Honest limits.** Per-delegate totals are hidden, but a pool that is the only weight on an option is exposed by that option's published total, as a lone direct voter's amount is. Approximate delegation counts (pool reveal counts) are public live. Helpers learn each DC's delegate, as they learn each vote's option; a DC made in the last-moment window is linked to its tx by reveal timing, as a late vote is; reveal timing can narrow small pools to a few transactions. Amount privacy, for delegations and direct votes alike, assumes fewer than t election-key holders collude; validators run the helpers, so a coalition of at least t validators can reconstruct individual amounts. There is no receipt-freeness (§5).

**Answers to the owner's original questions.**

- **(a) Delegate some or all of my weight to up to ~10 others, and keep some or none?** Yes, to up to 8. You choose 1 to 8 delegates with percentages, and either keep a remainder to vote yourself or delegate everything. Amounts are whole ballots (0.125 ZEC), at least 1 ballot per delegate, assigned by Hamilton largest-remainder rounding and shown exactly before you confirm. Three rules apply. You must delegate before your first own vote in the round, because the circuit needs full authority. You delegate once per round. And that delegation is final: it can never be changed or added to. You can delegate until voting ends, just as you can vote. In the final hours your delegation is sent to helpers at once, as a late vote is, and as with a late vote, one sent in the final minute may not be counted.
- **(b) ICNS-style X linking with a profile picture to look up influencers?** Yes, without ICNS's flaws. The delegate posts a marker line containing their key; the verifier binds the **numeric** X account id (never the handle) via the official X API, and the delegate's key signs the binding back. Profile pictures, for vetted delegates only, are re-encoded server-side and decoded in Rust; wallets never contact X. You browse the vetted list or search exact handles locally, and see a fingerprint plus lookalike, "Not reviewed" and "new this round" labels. GitHub and domain proofs ship as fallbacks.
- **(c) If I delegate to an influencer, can I find out whether they voted with it?** Yes, per proposal. Routes are public and IAVL-provable, and Vizor shows each proposal as Voted (their choice), Abstained, Not yet, or Didn't vote (abstain once the round ends). It also shows that your shares reached their pool (k/n, where n = 16, or 1 for a last-moment DC; provable via share nullifiers). Their route applies to the whole pool, your weight included. The pool's total is never published. You cannot see their private vote with their own ZEC, which may differ from their route.
- **(d) Can someone delegate at the last moment, just as they can vote at the last moment, with pools tallied at close?** Yes (decided, Q1). The client allows a new delegation while `now < vote_end_time`, the same check casts use (`vote_work/cast_vote.rs:39-52` [code]). In the last-moment window a DC uses the vote path's single-share layout: one share carrying all of `w`, sent at once to `ceil(N/2)` helpers with `submit_at = 0` (`submission_schedule.rs:113-134` [code]). The chain accepts the DC and its reveal while `blockTime < vote_end_time` (`keeper_voting.go:313-341` [code]), and the closing EndBlock routes each pool with every reveal that landed before close. Genuine differences from a late vote: one reveal carries the DC's weight on every proposal, so a missed reveal loses the whole DC; and delegation needs a few extra seconds before proving (directory, sticky proofs, `NextDelegateIndex`). Shared with votes: the reveal must land before close (about 5-10 s at best, minutes under helper backlog [inference]), a tx in the final block is never counted, and an in-window reveal links its tx to its choice (the delegate, or the option).

**Top changes from integrated-design-v1:** ARK no longer keyed by the viewing key; no unfreeze op, DIK-only recovery cancellation, recovery hardening; 30-bit `delegate_index_bound`; last-moment parity: a batch planned in the last-moment window uses the vote path's single-share layout for its DCs and casts, and delegation stays open until `vote_end_time`; no DC share is the designated immediate share; the layout is frozen per batch, and `(d, w, slot, layout)` per `van_nf`, before the first proof; a normative contract artifact with `chain_id`-bound digests; an on-chain proxy-action record for recovery; no Abstain option, as a privacy mitigation; a curator-signed vetted browse list with exact search for everyone else; one final delegation per round, plus a release path for stuck bundles; at most 8 DCs per registration per round.

---

## 2. Decisions log

### 2.1 Owner decisions (fixed)

| # | Decision | How the plan honors it | Where it cannot be fully honored |
|---|---|---|---|
| D1 | Delegate votes are public, via pools | Routes are public, signed by the DRK, IAVL-provable, and applied to the whole pool | A delegate's own private vote can differ from its route |
| D2 (rev 3) | Proxy delegation is final; one delegation per round, never changeable; unrouted means abstain | No override, revocation or amendment message exists. A wallet commits one allocation per round, which then locks. A DC is final once included | Slots whose batch never landed (never broadcast, or definitively rejected) can be released to the remainder. This is failure recovery, not a change (LIV-3) |
| D3 (rev 3) | Open registry with proof; Valar verifier; chain registry; vetted-only browse list | X, GitHub and DNS providers; threshold attestations; registry 0x1A. The browse list shows only curator-signed vetted delegates (2-of-3 Valar and Vizor); every other registered delegate is reachable by exact search or deep link (§4.6 Curation) | Launch is 1-of-1 Valar, so the verifier is a trust and censorship point until a second verifier exists. Curators decide who is browsable, so an unvetted delegate relies on sharing their handle or link |
| D4 (restored in rev 3) | Per-delegate totals hidden; pools never individually decrypted | Pools are added into the routed option buckets at close and only per-option totals are decrypted (§4.2 Routing). No leaderboard and no ZEC totals on profiles or dashboards. A route track record ("voted on N of M proposals") is public. Approximate delegation counts are public on chain but shown only on the delegate's own dashboard, marked approximate, never as a sort key | A pool that is the only weight on an option is exposed by that option's total, as a lone direct voter is. A coalition of at least t election-key holders can decrypt any pool (§5 Validator trust). Reveal counts are public live. Reveal timing links a late DC's tx to its delegate |
| D5 (rev 2) | Last-moment delegation parity: delegate until voting closes, as for votes | Delegation stays open while `now < vote_end_time`; a batch planned in the last-moment window uses the single-share layout and immediate delivery; pools are routed at close with every reveal that landed before `vote_end_time` (§4.4, O3) | As for a late vote, the reveal must land before close. One reveal carries a late DC's weight on every proposal. Reveal timing links a late DC's tx to `d`, as it links a late vote's tx to its option |
| D6 (confirmed) | Delegate keys come from a separate delegate key phrase | Non-BIP-39 24-word phrase derives DIK and DRK (§4.6, O4) | A lost phrase falls back to verifier-attested recovery with a 7 d delay |
| D7 (rev 2) | Coordinator suspension with safeguards | `MsgSetDelegateSuspension` is immediate and freeze-only, with a public reason code; every proxy payload needs at least 2 coordinator approvals; 2-of-3 vote managers within the first two proxy rounds (§4.2, Q5) | Coordinators remain fully trusted (they can already push binaries via x/upgrade). A quorum can still make an unrouted pool abstain mid-round. Incident levers need two coordinators on call |
| D8 (rev 3) | At most 8 delegates per wallet and 8 DCs per registration per round | C14 range `[0,8)` with a 3-bit check; 0x09 carries at most 8 DCs; capabilities advertise 8; the planner and UI cap 8 delegates (decision 5) | Nothing lost: no bundle ever needs more than 8 DCs, because one DC per delegate per bundle suffices |
| D9 (rev 3) | Mid-round off switch, pause-only | `proxy_dc_paused` (at least 2 approvals) makes the chain reject every new DC at once, including batches already committed; the Vizor remote kill switch stops new allocations in Vizor, while batches already committed still broadcast and complete (LIV-12). The round stays active; included DCs are still revealed, counted and routed; both can be resumed (§8.3) | It cannot undo existing delegations, which are final. No "discard delegated weight this round" lever in v1 |
| D10 (rev 3) | Mid-round registration stays | Delegates can register at any time while registration is enabled and route in rounds already running; the directory is not frozen per round (§4.6 Freshness) | A mid-round registrant has had little scrutiny; Vizor labels it "new this round" and treats it as unvetted until curators add it |
| D11 (rev 3) | No small-pool notice in v1 | No delegator notice about small or lone pools and no delegate-side warning; §5 and the FAQ state the exposure, which matches a lone direct voter's | A lone delegator is not warned in the app |

### 2.2 Open items from v1, resolved

| Item | Resolution | Evidence |
|---|---|---|
| O1 `delegate_index_bound` | **Adopt** as public input 11: in-circuit `1 ≤ d ≤ bound` via 30-bit range checks on `d−1` and `bound−d` [inference]; chain requires `bound < NextDelegateIndex`, registry ≤ 2^30; the builder sets `bound = NextDelegateIndex − 1` itself (§4.1) | [measured] With the prototype's 40-bit bound check (`BOUND_WORDS = 4`): still 2,015/2,048 rows, 78 fixed columns, 11,008 B proof, prove time within noise; soundness suite passes; a real proof fails if PI[11] changes. The 30-bit width needs `BOUND_WORDS = 3` (§4.1 C15) |
| O2 one tag vs two | **One tag 0x09** with optional registration; registry 0x0B, routes 0x0C; `anchor_height == 0` if and only if a registration is present | SND-7 front-running analysis |
| O3 chain DC cutoff | **Decided (rev 2, D5): no consensus deadline and no early client close (vote parity).** The SDK uses the cast path's predicates on the same `RoundHostContext`: no new allocation or proxy batch once `now ≥ vote_end_time` (`VoteEnded`); in-flight work advances and the chain rejects late txs without spending the VAN; new allocations require authenticated round timing; layout = single share if and only if `is_last_moment()` at batch planning, persisted once per 0x09 batch for its DCs and casts before the first proof, with `(d, w, slot, layout)` per `van_nf`. Coordinator `proxy_dc_paused` brake (D9). | PRV-4, LIV-4, CMP-11, SND-8; `cast_vote.rs:35-53`, `vote.rs:4447-4452`, `submission_schedule.rs:113-134` (zcash_voting), `keeper_voting.go:313-341`, `module.go:491-497` (vote-sdk) [code]; single-share ZKP4 MockProver positives and ZKP3 reveal (`prototypes/circuits/single_share_test.log`) [measured] |
| O4 delegate keys | **Dedicated delegate key phrase**, in a non-BIP-39 format; hardware-account users can be delegates. Owner confirmed (D6). | CMP-5, IDN-14 |
| O5 Abstain option | **No Abstain option in v1.** Keep the sentinel `0xFFFFFFFF` (recorded, not counted). While totals are hidden, an explicit Abstain option makes pools solvable from public results, so it is a privacy risk; it remains open product question Q2 (default no). | [measured] With sentinel abstain and Yes/No proposals, 0% of pools were exactly solvable; with an Abstain option, 42-44% were (15 proposals, 30 direct voters, 5 delegates). The checked-in `prototypes/privacy/pool_inference.out` shows 81-87% for the same case because it maps unrouted pools to Abstain; the skeptic's corrected figure is 42-44% |

### 2.3 Design decisions

| # | Decision | Rationale | Rejected |
|---|---|---|---|
| 1 | B2: DC shares through helpers | Hides delegate choice from the public and the delegate (helpers, which run on validators, still learn it), and hides per-DC amounts; only per-option totals are ever decrypted | B1 in-tx slots: EA sees exact amounts; delegate set linked to tx |
| 2 | ZKP4 as a separate circuit at K=11 | No existing VK changes | A ZKP2 mode: new VK for all voters |
| 3 | DC reuses `DOMAIN_VC` with constant proposal 0 | ZKP3 unchanged; ZKP2's `p ≠ 0` gate keeps it sound | New `DOMAIN_DC` and reveal VK |
| 4 | Input and successor VAN authority MAX | No double counting of cast weight | Per-proposal pools |
| 5 | Slot nullifier: ≤8 DCs per registration per round, via a 3-bit short range check | Bounds helper load and spam at 8 × 16 = 128 pool reveals per registration. The 3-bit check costs no more than the old 4-bit one: rows (2,015), columns, proof size (11,008 B) and prove time are unchanged [measured]. No single bundle ever needs more than 8 DCs, because a wallet has at most 8 delegates and one DC per delegate per bundle suffices | A 4-bit check (16 slots); minimum size only (count grows with W) |
| 6 | p=0 reveals accepted for any index that exists at reveal time | Status changes never strand weight; mid-round registrants receive pool shares at once | Status check at reveal |
| 7 | EndBlock routing with ρ re-randomization | Pools final; blocks identity-C1 round kill on the combined buckets; per-option results never need a pool decrypted | Lazy routing in three code paths |
| 8 | Pool counts at `ShareCountKey(round,0,d)`; distinct event | Free genesis export; VoteSummary never sees p=0 | Separate key (OPS-2) |
| 9 | Digests: BLAKE2b, ASCII domain, `lp8(chain_id)`, 32-byte fields | One convention; no cross-chain replay | `net` string, LE fields |
| 10 | DIK and DRK from a delegate phrase | Hot-key rotation keeps the index; hardware users included | Single key; seed derivation |
| 11 | No unfreeze; DIK rotation exits FROZEN | Stolen DRK cannot undo a freeze; no replay | DIK-or-DRK unfreeze |
| 12 | DIK-only recovery cancel; recovered keys cannot route older rounds | Takeover cannot capture formed pools | v1 rules |
| 13 | Delay floors; verifier warm-up | One coordinator key cannot re-key quickly | Unconstrained params |
| 14 | ARK from spending key or hotkey, never OVK | Viewing keys don't reveal votes | OVK-keyed ARK |
| 15 | No designated immediate DC share | No public tx-to-delegate link for early DCs; inside the last-moment window every share, vote or DC, is immediate anyway | DC share 0 immediate in every window |
| 16 | One locked allocation per round, never changed | Simple schema and recovery; matches D2 | Amending or adding to a committed allocation |
| 17 | On-chain `ProxyActionRecord` per DC | Complete, provable recovery | Event or tx-index feed |
| 18 | Whole-list sync; sticky proof set | No per-index interest leak | Per-index REST; fresh decoys |
| 19 | Browse list = curator-signed vetted entries only; everyone else by exact search or deep link, without avatar or display text | Directory-key theft can't mint vetted entries; an impersonator needs an exact query and still meets lookalike warnings; moderation and X hydration scale with the vetted list only | Plain flag; an open browse listing gated by account age and follower thresholds |
| 20 | X proof: original post, exact template | No binding via replies or retweets | Any post with the line |
| 21 | Signer recomputes and enforces budgets | API compromise isn't an oracle | Opaque-digest signer |
| 22 | Proxy PRs `V:state/breaking`, v1.7.0 only | No GasUsed divergence | Rolling dormant releases |
| 23 | 0x09 caps at 8 DCs and 50 actions, plus an advertised byte budget | Same envelope as today's 50-cast composite; 8 DCs covers a wallet's maximum delegates | Larger batches (over 1 MB RPC limits) |
| 24 | No per-block proof-verification cap in v1 | 0x06/0x07 already set this load class; a cap under 51 strands Zodl batches | A proof-action cap (OPS-10) |
| 25 | Per-block dedupe: one registry op per delegate, one route tx per (round, d) | Stops duplicate-signature floods | A global cap only (IDN-9) |
| 26 | Route entries identical to stored routes are no-ops | Safe retries near the deadline | Strict all-or-nothing (LIV-11) |
| 27 | ZIP-215 Ed25519 everywhere; torsion-component keys rejected | Chain, verifiers and auditors agree | Mixed stdlib, dalek and CometBFT rules (IDN-15) |
| 28 | Deep-link payload only in the URL fragment, fingerprint mandatory | Host and CDN logs never see the index | Index in the path (IDN-16) |
| 29 | Pool reveals share the 256/block cap and helper FIFO with votes | No new scheduling class | A priority class |
| 30 | Last-moment parity: single-share DCs in the window, open until `vote_end_time`, layout frozen per batch | Delegating behaves like voting; 1 reveal per late DC; one clock and one predicate for DCs and casts | Early close at `vote_end − max(buffer, 30 min)`; chain `cutoff_time`; a DC-only chain-time clock (breaks batches that mix DCs and casts) |
| 31 | Pool reveals carry a public delegate label `(0, d)` | The label-free alternative works: [measured] a separate-VK ZKP3R prototype is K=11, 1,016 rows, 8,352 B proof, and about 2.6× ZKP3's prove time (90 ms vs 35 ms at 4 threads) (`prototypes/labelfree/zkp3r_rows.log`, `zkp3r_timing.log`). But it needs 16 × P_routed reveals per delegation, which is 1.8-17× total reveals, 6-12× today's helper fleet at 20k delegators × 3 delegates × 10 proposals, and infeasible at any fleet size with 37 proposals because the 256/block cap binds [inference from measured queue models: `prototypes/labelfree/seeds_deadline.out`, `counts.out`, `load.out`]. It needs a delegate route deadline, which breaks last-moment parity (D5), and late routes crowd out direct last-moment votes. It buys little privacy, because validators that run helpers can already group shares (§5 Validator trust), and per-delegate counts remain solvable from per-proposal cast counts when delegates are few relative to proposals [measured, `prototypes/labelfree/unified_leak_lone.out`]. Revisit only if the helper fleet grows substantially | Label-free route-proving reveals: each share is revealed per routed proposal with a proof that its hidden delegate chose `(p, o)`, and lands directly in that bucket |

---

## 3. Architecture

### 3.1 Actors

- **Delegator wallet:** Vizor plus the zcash_voting SDK; plans, proves, submits, delivers shares, verifies. Software, Keystone and Ledger accounts; devices sign only ZKP1.
- **Delegate wallet:** Vizor delegate mode plus the `zcash_vote_delegate` crate; holds the phrase, DIK and DRK; onboards; signs routes and registry ops.
- **Vote chain:** vote-sdk validators; verify proofs, hold registry, routes and pools, route at the tally transition, and hold EA key shares. Pools are never decrypted on their own.
- **Helpers:** the svoted helper, which runs in-process on each validator node (`docs/runbooks/join-chain.md:173` [doc]); receive DC shares (learning `d`, the DC leaf and the client IP unless Tor is on), prove ZKP3, reveal at randomized times.
- **Verifier service:** Valar-run verifier-api, attest-signer (KMS/HSM), publisher, refresher, chain-indexer and independent re-verifier.
- **Curators:** hold the curator keys (Valar and Vizor, 2-of-3) and sign the vetted list.
- **Directory mirrors:** the valargroup origin, `functions.vizor.cash` and a third independent mirror, all untrusted for integrity.
- **Config repo:** a new static pin and a per-round signed dynamic extension.
- **Coordinators:** vote managers who set params and verifiers, may suspend a delegate, and may pause new delegations mid-round. Every proxy payload needs at least 2 approvals (D7).

### 3.2 Sequence

```
Delegator (Vizor+SDK)      Directory    Vote chain                  Helpers        Delegate (Vizor)
  |                            |            |  (any time, mid-round too) phrase -> DIK/DRK; marker post; verifier attests;
  |                            |            |<--------- 0x0B register {DIK, DRK, subject_commit, attestation}
  |                            |            |  index d assigned at once (append-only, from 1)
  |--GET signed docs---------->|            |                            |              |
  |--page /delegates, IAVL-prove sticky 10-key set (commit, pre-prove)-->|              |
  | plan: Hamilton -> slots; dc_seed(ARK); hint; bound = Next-1           |              |
  | prove: [ZKP1] -> ZKP4 x k (k <= 8) -> [ZKP2 x m] (phased)            |              |
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
  |--IAVL-prove route sets and share nfs (sticky set)-->|               |              |
```

### 3.3 Data and trust boundaries

- **Chain-authoritative:** index, DIK, DRK, `key_epoch`, status, suspension, routes, pools (as ciphertexts), pool reveal counts, nullifiers, `NextDelegateIndex` and the proxy-action records.
- **Display-only:** the directory (handles, names, avatars, statements). A directory lie cannot redirect a delegation to a different index or key. Vetted status is protected by curator signatures; handles by the directory signature, `subject_commit` and wallet pinning (§4.6).
- **Third-party consumers:** the registry doc (IDs plus current handles) may be consumed by other wallets once counsel clears handle redistribution (Q10). The profiles doc stays Vizor-only until counsel clears redistribution.

---

## 4. Component designs

### 4.1 Circuits (voting-circuits 0.13.0)

**ZKP4 conditions.** All are measured at K=11 with the 3-bit slot check: 2,015/2,048 rows, 34 advice and 78 fixed columns, 4 lookups, identical to the 4-bit version [measured, rev-3 prototype `prototypes/circuits/zkp4-prototype.patch`].

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
| C14 | `dc_slot ∈ [0,8)` via a 3-bit short range check, `copy_short_check(dc_slot, 3)`, so at most 8 DCs per registration per round; `dc_slot_nf = Poseidon4(nk, TAG("proxy delegation slot") + slot, round, rand)`. The prototype (`prototypes/circuits/zkp4-prototype.patch`, applied with `git apply -p2` to voting-circuits v0.12.2) sets `DC_SLOT_BITS = 3` at patch lines 798-799 and uses it at line 1243 [code]. Changing `DC_SLOT_BITS` changes the ZKP4 VK (the prototype VK fingerprint now starts `9bb86dda`), which is why it is frozen before rc.1 |
| C15 | `1 ≤ d ≤ bound`: witness `e1 = d − 1` and `e2 = bound − d`, each a strict 3×10-bit lookup range check [inference], in a one-row custom gate (bound copied from instance 11). The measured prototype uses 4 words (`BOUND_WORDS = 4`, 40 bits) with the same row count, and accepts `d − 1 = 2^30` and `bound − d = 2^30`; it switches to 3 words before rc.1. A throwaway 3-word copy kept 2,015 rows and rejected both [measured, not checked in] |

C7 plus C9 plus C12, together with ZKP1's `W ≤ 2^30`, give `1 ≤ w ≤ W` over the integers.

**Public inputs (12, in this order).** `van_nf, r_vpk_x, r_vpk_y, van_new, dc, root, anchor_height, round, ea_pk_x, ea_pk_y, dc_slot_nf, delegate_index_bound`. The voting-circuits `Instance` constructor is the only source of this order. The chain FFI must build instances through it, and a golden vector pins the packing.

**Constants and registrations.** All of these are frozen with pinned-value tests before rc.1, because changing them later breaks either a VK or recovery:
- `POOL_PROPOSAL_ID = 0`;
- `DC_SLOT_BITS = 3` and the slot tag text with offsets 0..7;
- PRF domains 0x10-0x14 keyed by `dc_seed`: 0x10 El Gamal, 0x11 blind, 0x12 shuffle, 0x13 remainder (layout 0); 0x14 El Gamal for layout 1, the last-moment single share, mirroring `VOTE_PRF_DOMAIN_ELGAMAL_SINGLE_SHARE = 0x04` (`vote_proof/builder.rs:538-550`, `domain_tags.rs:42-43` [code]);
- `DOMAIN_VC` documented as live for proposal-0 pool commitments.

**Invariant (normative in ZIP-PD).** `rand` is constant along every transition that keeps MAX authority. Any future VAN-transforming circuit must either preserve `rand` or carry a slot counter; otherwise C14's cap silently resets (CF-3). A cross-circuit test pins it.

**Measured costs** ([measured], loaded M3 Ultra): with the 3-bit slot check, ZKP4 proves in 519, 272, 158 and 95 ms at 1, 2, 4 and 8 threads, and 66 ms with all 28 threads (530, 278, 161 and 96 ms with the 4-bit check, within noise, and within 2% of ZKP2), verifies in about 2 ms (1.6-2.0 ms at 8 or more threads), and is 11,008 B (cap 15,360 B). Keygen peak RSS is about 140 MiB; ZKP4, ZKP2 and ZKP3 resident use 259 MiB, plus about 182 MiB for ZKP1. At 4 threads, ZKP1 + 10 ZKP4 + 50 ZKP2 took 10.2 s and ZKP1 + 10 ZKP4 took 1.9 s, so the largest batch under the 8-DC cap (ZKP1 + 8 ZKP4 + 42 ZKP2) takes about 8.6 s and "delegate everything" to 8 delegates about 1.6 s [inference from measured]. [inference] Mid-range Android at 2.5-4x slower still meets Vizor's ≤2 s per proof target.

**ZKP3 reuse.** ZKP3 is unchanged. Real proofs [measured] for `(0, d)` at a non-zero tree position verify for `d = 4242` and `d = u32::MAX`. They are rejected for `d + 1` and for proposal 1. The ZKP1, ZKP2 and ZKP3 `vk_fingerprint_unchanged` tests are a release gate.

**ZKP2 gate.** ZKP2's `proposal_id ≠ 0` inverse gate (`authority_decrement.rs:398-416` [code]) is now **load-bearing**: without it, ZKP2 could emit `VC(0, d)` while keeping full weight. Re-document it, make `proposal_id_zero_fails` a non-ignored proxy soundness test, and require it on every ZKP2 rewrite.

**Builders and exports.** All APIs are ballot-denominated:
- `build_proxy_delegation_proof(..., delegate_index, ballots, dc_slot, next_delegate_index, dc_seed, layout: ShareLayout)`. The builder rejects `dc_slot ≥ 8`. It computes `bound = next_delegate_index − 1` internally, from IAVL-verified state read just before proving, and there is no free bound parameter (CF-1). The public bound then reveals only the registry height at proving time; a caller-chosen `bound = d` would have revealed the delegate.
- `derive_proxy_delegation_transition` (native, proof-free).
- `derive_proxy_share_secrets(dc_seed, ballots, layout)`. PRF: `BLAKE2b-512(personal "ZcashVoteProxyEx", dc_seed ‖ domain ‖ share_index)`. **Layout 0:** denomination split (remainder 0x13), shuffle (0x12), `r_i` from 0x10, `blind_i` from 0x11. **Layout 1:** `shares = [w, 0×15]`, unshuffled, `r_i` from 0x14, `blind_i` from 0x11, mirroring the vote builder's single-share path (`builder.rs:538-550, 812-818` [code]). All 16 ciphertexts and commitments are still built because `shares_hash` covers 16, so the ZKP4 VK, rows (2,015) and proof size (11,008 B) are unchanged. C9 holds because `w ≤ W < 2^30`, and C12 because `w ≠ 0`.
- Also exported: `proxy_delegation_commitment_hash`, `dc_slot_nullifier`, `dc_share_nullifiers`, `share_reveal::build_pool_share_reveal`, ballot variants of the ZKP2 builders, and `vk_fingerprints()` for ZKP1-4.
- Frozen vectors for every formula.

**Zero-weight successor.** A successor with `W_new = 0` ("delegate everything") can still produce zero-weight ZKP2 casts [measured]. By convention it is **dead**: clients never cast from it, and ZIP-PD says so (CF-2). The behavior is pinned with a test.

**Tests.**
- MockProver negatives for C1-C15. For C14: `dc_slot = 7` is accepted and `dc_slot = 8` is rejected by the 3-bit check, and `dc_slot_nf` is distinct for every `s` in 0..8. For C15: `d = 0`, `d = bound + 1`, `bound < d`, `d − 1 = 2^30`, `bound − d = 2^30`, `d = p − 1`, plus a positive at `bound = d = 2^30 − 1`, which replaces the prototype's `review_bound_u32max_ok` (a positive at `u32::MAX` that only a 40-bit check accepts).
- Nullifier parity with ZKP2.
- Chains: ZKP4→ZKP4→ZKP2 passes; ZKP2→ZKP4 fails; a further ZKP4 after `W_new = 0` fails.
- Replay of every public input.
- A ZKP4 proof under the ZKP2 VK fails.
- Witness independence (`Circuit::default()` gives the same row count).
- Row budget: re-measure after every change, because the 33-row headroom is shared.
- [measured] In the rev-3 prototype re-run, slots 0 and 7 verify; slots 8, 15 and 16 fail only at the 3-bit range-check lookup; the slot tags for `s` in 0..8 are distinct from each other and from the crate's other domain tags; and the 20-test proxy suite passes (with the 40-bit C15 check; the suite count changes once C15 moves to 30 bits).
- Layout 1: MockProver positives for `w = 1` and for `w = W = 2^30 − 1` with `W_new = 0`; the negative `share0 = 2^30` with `W = 2^30 + 1` (so `W_new = 1` passes C7, and the test asserts that the failing constraint is the C9 range check); a real layout-1 proof under the unchanged VK; a ZKP3 reveal of layout-1 share 0 as `(0, d)`; pinned vectors for 0x14 randomness and the layout-1 `shares_hash`; a check that a layout change alters every `r_i` and blind. [measured] The cloned prototype test `zkp4_single_share_layout_accepts_and_reveals` passes the positives (`w = 4,000`; `w = W = 2^30 − 1`) and the ZKP3 `(0, d)` reveal in 0.74 s (`prototypes/circuits/single_share_test.log`); the test is now part of `zkp4-prototype.patch` and passes in its 20-test suite. Its negative case used `W = 2^31`, which also violates C7, so the C9 rejection is not yet isolated.

### 4.2 Chain (vote-sdk v1.7.0)

#### Messages and tags

New tags are 0x09, 0x0B and 0x0C, and 0x04 gains a branch. Every new client tag needs:
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
- `1 ≤ len(actions) ≤ 50`; `1 ≤ #DC ≤ 8`; DCs strictly precede casts. Pure-cast lists keep using 0x06/0x07.
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
2. `ValidateRoundForVoting`, then `round.proxy_delegation.enabled`, then `!params.proxy_dc_paused` (`ErrProxyDelegationPaused`, retryable; D9).
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
| `register` | DIK + DRK over `reg`; verifier threshold over `attest` | New ACTIVE entry, `key_epoch = 0`, index assigned at once | Accepted at any time, including mid-round (D10); `next_index ≤ min(max_delegates, 2^30)`; keys never reused |
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

Accepted when: the round is ACTIVE, enabled, and `blockTime < vote_end_time`; the entry is effectively ACTIVE and not suspended; `key_epoch` matches; `round.created_at_time ≥ recovered_at_time` if that is set (IDN-1); every `p` is in `round.Proposals`; and every decision is `< num_options(p)` or `0xFFFFFFFF` (explicit abstain, recorded, not counted). A delegate registered after the round was created routes under the same rules. `proxy_dc_paused` does not affect routes. An entry identical to a stored route is a no-op; a conflicting one rejects the tx (`ErrDelegateRouteExists`). Routes already made survive a later freeze, suspension or revocation.

**0x04 `MsgRevealShare` with `proposal_id = 0`.** Accepted if and only if `round.proxy_delegation.enabled` and `1 ≤ d < NextDelegateIndex` at reveal time; delegate status and `proxy_dc_paused` are ignored. Effects: `AddToTally(round, 0, d)`, `IncrementShareCount(round, 0, d)`, and a `reveal_pool_share{round, delegate_index, share_nf}` event (never `reveal_share`). It counts toward the 256-per-block cap. Errors `ErrProxyDelegationDisabled` and `ErrDelegateNotFound` are permanent. A layout-1 (last-moment) DC contributes one reveal and a layout-0 DC sixteen; neither needs a chain change, and the chain cannot tell them apart (`MsgRevealShare` carries no share index, `proto/svote/v1/tx.proto:120-128` [code]).

**Coordinator payloads.**
- `MsgSetProxyDelegationParams`: `enable_for_new_rounds`, `proxy_dc_paused` (the mid-round off switch, §8.3), `registration_enabled`, change delay (≥72 h) and recovery delay (≥7 d) as consensus floors, `max_attestation_validity` (≤72 h), `max_key_changes`, and `max_delegates` (≤2^30; launch 20,000).
- `MsgSetProxyDelegateVerifiers`: 1..16 verifiers, unique ids and pubkeys, two thresholds, and a stored `added_at_time`. A verifier may attest only after `added_at_time + recovery_delay`, except in the activation set (IDN-3).
- `MsgSetDelegateSuspension`: immediate, freeze-only, public reason code (1 impersonation, 2 key compromise, 3 legal, 4 verifier review, 99 other). It cannot create entries, change keys or void routes.
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
| `0x1C round u32be(p) u32be(o)` | routing base (pre-routing accumulator, for the routing audit) |
| `0x1D round van_nf[32]` | `ProxyActionRecord` {dc, dc_leaf_index, dc_slot_nf, recovery_hint, final_van_leaf_index, height} (about 150 B) |
| `0x1E round u64be(height) u32be(d)` | route change index (derived) |
| `TallyKey(round,0,d)` | `Pool[d]` (ciphertext; never decrypted on its own) |
| `ShareCountKey(round,0,d)` | pool share count |
| `NullifierKey(0x03, round, nf)` | DC slot nullifiers (`NullifierTypeProxySlot = 0x03`) |
| `VoteRound` field 31 | `proxy_delegation {enabled, next_delegate_index_at_creation, pools_routed, routed_pool_count, applied_route_count}` (outside the round-id preimage); written only for proxy rounds |

**Route absence.** A route is absent when `p` is missing from an IAVL-proven `0x1B` set, or when the whole key is proven a non-member, at a height at or after the tally transition. It is never proven through a per-proposal key (OPS-8). A golden key-derivation vector is shared by `keys.go` tests and Vizor's reader.

**Genesis.** One normative GenesisState section covers:
- nullifier types 0-3: `ValidateGenesisState` must accept type 3, because `genesis.go:94-97` rejects anything above 2 today [code];
- every 0x1A key;
- routes, routing bases, proxy-action records and params.

Derived indexes are rebuilt on import. `TestExportImportGenesis` is extended with one finished and one active proxy round.

**Reset rule.** Any chain reset that does not export and import the registry **must change `chain_id`**. That kills replay of old registry and route signatures.

#### Queries, REST and capabilities

- `delegates?start_after=&limit≤1000&updated_since_height=` (backed by 0x1A08, always including 0x1A09 pending entries). `delegates/{index}` and `delegates/by-key/{hex}` serve delegate tooling and explorers, **not** the delegator path.
- `delegate-routes/{round}` (key-paged, `updated_since_height` via 0x1E); `delegate-pools/{round}` (revealed-share counts only, live during the round); `nullifier/{round}/{type 0..3}/{nf}`; `proxy-actions/{round}` and `/{van_nf}` (IAVL-provable; `tx_hash` optional); params; verifiers.
- `ProposalTally` rejects `proposal_id = 0` and ids above `len(proposals)` (OPS-12).
- `routing-audit/{round}`, computed on read and **not** in consensus: for each routed (p, o), the router count and `routing_verified` from a homomorphic recompute of the bucket (0x1C base ⊕ routed pools ⊕ `Enc(0; ρ)`; ρ is deterministic from public data). It works on ciphertexts and decrypts nothing. Per proposal, it lists the delegates whose pools were not counted (abstain routes and missing routes). The V20 canary and an alert assert `routing_verified`.
- Event names and fields are catalogued in contracts-v1.
- `ProtocolCapabilities` appends fields once:

  | Field | Value |
  |---|---|
  | 4 | `proxy_delegation` (bool) |
  | 5-7 | wire tags 0x09, 0x0B, 0x0C |
  | 8 | `max_dc_actions_per_tx = 8` |
  | 9 | `max_vote_actions_per_tx = 50` |
  | 10 | `max_vote_tx_bytes` |
  | 11 | `proxy_delegation_protocol_version = 1` |
  | 12 | `repeated CircuitFingerprint{name, vk_blake2b}` for ZKP1-4 |
  | 13 | `max_dc_slots = 8` |

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

**Why this is safe.** Pools are final: the transition block admits no reveals or routes (`keeper_voting.go:336-338`, `msgs.go:554-557` [code]). ρ (deterministic; curvey `expand_message_xmd` with BLAKE2b) blocks a forced identity C1 that would fail every DLEQ and time out the round [measured in `prototypes/chain/route_test.go`]. Buckets stay under 1.68·10^8 ballots, below `TallyBSGSBound = 2^28`. Proposal 0 stays excluded from the partial decryption (0x0D), completeness, `ValidateEntryBounds` and `MsgSubmitTally` [code]; tests lock it in. No code path decrypts a pool on its own (D4). Anyone can recompute the routing identity on ciphertexts from 0x1C, the pools and ρ (`routing-audit`).

**Cost.** [measured] 10k delegates × 50 routes took 1.39 s in memory on an M3 Ultra. Benchmark the real IAVL-backed hook on the 2-4 vCPU validator droplets in CI. Launch `max_delegates` is 20,000, raised only with benchmark evidence. Fallback: spread routing over the first K TALLYING blocks, gating partial-decrypt injection on `pools_routed` (OPS-13).

**Reveal close.** Route the routing trigger and reveal acceptance through one keeper function, `revealCloseTime(round)`, that returns `vote_end_time` in v1. It must be a pure refactor that reads only the already-loaded round, with a GasUsed-equality test (OPS-6); otherwise defer it to v1.1. A test pins that pools are routed before any partial decryption is accepted. This prepares the optional post-close reveal grace window (Q18): reveals only, for votes and DCs alike, accepted until `vote_end + G` while casts, DCs and routes still close at `vote_end`, with routing and decryption moved after `vote_end + G`. It changes the tally lifecycle for every round and is not in v1.

#### Limits, spam and per-block rules

- Slot cap: 8 DCs per registration per round. Per attacker proof this gives 16 reveals, the same as a cast, and a registration can cause at most 8 × 16 = 128 pool reveals.
- 0x09 caps at 8 DCs and 50 actions. [measured] ZKP1 + 50 casts is 576,769 B raw and 769,100 B as RPC JSON, and ZKP1 + 10 DCs + 40 casts was about 577.5 KB raw and 770 KB as RPC JSON, so the largest batch under the cap, ZKP1 + 8 DCs + 42 casts, is about 577.4 KB raw and 770 KB as RPC JSON [inference from measured]. That is about 23% headroom under the 1 MiB REST and 1,000,000 B Comet limits.
- Prepare/ProcessProposal dedupe: at most one registry op per `delegate_index` per block (one per new DIK for `register`), and one route tx per `(round, d)` per block. The global registry-op cap (64) stays as a backstop.
- No per-block proof-verification cap in v1 (decision 24). If one is ever added, it is weight-based, at least 51 proofs per tx, and FIFO across tags.
- The tree-capacity guard returns `ErrCommitmentTreeFull` inside `AppendCommitment`, and `MaxTreePosition` is fixed to 2^24−1.

#### Dormant merges and activation

A `ProxyDelegationEnabled = false` const gates tag decode, interface **and Msg-service** registration, the p=0 branch, routing, new payloads, capability fields and any change to existing validation. While the const is false, field 31 is never written. It is checked **before any new KV read**, so existing paths keep identical GasUsed (OPS-6). Proxy PRs are labeled `V:state/breaking`, never backported to v1.6.x, and ship only in v1.7.0. The activation commit flips the const and registers a no-op `v1_7_0` handler.

### 4.3 Helper (vote-sdk `internal/helper`)

**Proposal-0 branch.** `validatePayload` accepts `proposal_id = 0` with a u32 `vote_decision ≥ 1` and skips the `< 8` option check. For p=0, `verifyCommitment` runs **first** (the leaf at `tree_position` must equal the recomputed DC hash; an absent leaf returns a retryable 503). Only then is `1 ≤ d < NextDelegateIndex` checked, with a permanent distinct error, which `bound` makes unreachable once the leaf exists (LIV-9, OPS-11). Scheduling, store, retries and prover are unchanged. Layout-1 DC payloads need no helper change: one payload, share index 0, `submit_at = 0`, handled like single-share votes (enqueue schedules `submit_at = 0` at arrival, `internal/helper/store.go:459-472, 503-509` [code]; acceptance requires an ACTIVE round, `api.go:328-333` [code]).

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
- **Lifecycle.** One durable allocation per `(round, wallet)`: preview → commit (with `plan_digest`) → locked. 1 to 8 entries `(d, dk, bps, display_handle)`; the planner rejects a ninth. Remainder is `KeepForSelf` or `DelegateEverything`.
- **Rounding.** Hamilton largest remainder in u128. Minimum 1 ballot per delegate, funded by Keep or the largest delegate. A round may raise the floor through `min_ballots_per_delegate` in the signed round extension.
- **Packing.** An exact split-free search when B ≤ 4, then best-fit decreasing, then greedy splitting. The objective is fewest DCs, then fewest bundles touched. The bound is `D ≤ #DC ≤ D + B − 1` [measured over about 12k cases]. A bundle never carries two DCs to the same delegate, so it holds at most 8 DCs.
- **Slots.** A slot is `(bundle, slot_index 0..7)` and also carries `dc_slot` (0..7) for C14. `dc_slot` equals the DC's position in that bundle's action list, and positions follow a **CSPRNG shuffle** taken at commit so action order does not reveal size ranking (PRV-9). Slot positions and nullifiers are persisted before proving and reused on retry.
- **One delegation per round (D2).** Delegation is available only until commit. The allocation then locks for the round and is never changed or extended; there is no API to amend it.

**Batch composition.**
- Per bundle: `[ZKP1?] → DC… → casts`. Casts ride along only when the remainder is kept and the roster is terminal; otherwise they follow later as an ordinary 0x06 on the remainder VAN. Inside the last-moment window with `KeepForSelf`, Vizor asks for the remainder's choices before dispatch so the casts ride in the same 0x09; if the user declines, it warns that votes sent later may not be counted.
- The packer uses `max_vote_tx_bytes` from capabilities (default budget 700 KB) and the 50-action total. It never splits a bundle's DCs.
- A `W_new = 0` successor never gets casts (CF-2).
- Proving runs in phases: ZKP1, then all ZKP4s, then ZKP2s. Each proving key is loaded for its phase and evicted before the next. ZKP4 is never pre-warmed at app start, and a peak-RSS budget is part of the device benchmark gate (CF-4).
- **Last-moment parity (D5, O3).** A new allocation or proxy batch is allowed while `host.now_seconds < vote_end_time` and returns `VoteEnded` afterwards, mirroring `vote_work/cast_vote.rs:39-52` [code]; in-flight work advances, and the chain rejects anything landing at `blockTime ≥ vote_end_time` without spending the VAN (`keeper_voting.go:336-338` [code]). `proxy::availability` requires authenticated `ceremony_start_seconds` and `vote_end_time_seconds` in the `RoundHostContext`, because without them the SDK skips `VoteEnded` and `is_last_moment()` returns false (`cast_vote.rs:39`, `vote_work/mod.rs:117-124, 136-138` [code]); committed work still advances (LIV-12).
- **Layout.** Chosen **once per 0x09 batch** at planning: single share (layout 1) if and only if `RoundHostContext::is_last_moment()`, the same call casts use (`cast_vote.rs:53` [code]). The window is `min(40% of round, 6 h)` (`share_policy/timing.rs:19-45` [code]). The layout is persisted with the batch before the first proof and used for every DC and every cast's `DraftVote.single_share` on every re-prove, split-off re-plan and recovery, because `recovery_matches_draft` rejects a cast recovery whose `single_share` differs (`vote.rs:4447-4452` [code]). A layout-0 DC that lands inside the window delivers its 16 shares at `submit_at = 0` (`submission_schedule.rs:128-131` [code]), as a pre-window vote that lands late does. The clock is the same host clock votes use; a clock-skew warning is optional and shared with votes.

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
- `layout` stays out of `hint_key`, so recovery needs one AEAD open per record. Recovery rejects a layout byte other than 0 or 1, and a slot byte of 8 or more.
- **Invariant:** the batch layout and each slot's `(d, w, slot, layout)` are persisted before the first proof and reused by every re-prove, split-off re-plan and recovery, so only one plaintext is ever published per hint key, and the zero nonce is safe (SND-8). A released slot (LIV-3) never landed: its batch was never POSTed, or was definitively rejected. Its hint key is never used again, because the allocation is locked and the released weight is only cast. This rests on policy, not cryptography; optional hardening for audit tranche 2 is a deterministic SIV AEAD (AES-SIV, RFC 5297), exactly 32 B for the 16 B plaintext with no nonce, so a freeze bug would leak only plaintext equality.

**Persistence (schema v25, one-way).** New tables for allocations and entries, batches (with `layout` 0/1, written before proving), slots (with `dc_slot`, `dc_slot_nf` and the batch `layout`, written before proving), DC share plans and deliveries, the recovery cursor, the sticky proof-key set, and delegate identity metadata; two new `chain_submissions` kinds; `ShareKey` gains `CommitmentRef::{Vote, Proxy}`. Effective VAN weight, `floor(total_note_value / 12,500,000) − Σ dispatched or confirmed slot ballots`, replaces `total_note_value` at every VAN-recompute site (`zkp2.rs`, `vote.rs`, `load_van_tree_entries`, tree sync and others). Account deletion removes all proxy tables.

**NextStep and planner obligations.**
- New kinds `ProxyDelegate`, `AdvanceProxyBatch`, `SubmitProxyShares`, `ConfirmProxyShare`, with exhaustive matches. Proxy-only rounds plan ZKP1 with zero ballot intents. A bundle with a proxy batch in flight is held from casts, and vice versa.
- **Release path (LIV-3).** `release_proxy_bundle(bundle)` is allowed when the bundle is `ProxyBlocked{DelegateChanged}` without continuity, `ProxyBlocked{Paused}` (the chain returned `ErrProxyDelegationPaused`, D9), or `ChainTerminal`, and none of its batches has a reserved POST (an unresolved in-flight POST) or a landed tx. It drops those slots, turns their weight into Keep, lifts the hold and records the release in the allocation's `plan_digest`; landed slots stay final. This is failure recovery for weight that never left the VAN, not a change to a delegation. To narrow the window, prove every bundle's batch before broadcasting any. A paused batch is retried automatically if the pause lifts before `vote_end_time`.
- **Split-off.** "Registration landed, batch rejected" is a normal transition: re-plan the same DCs and casts against the real anchor (CF-5).

**DC shares.** The existing `VoteShareWire` carries `proposal_id = 0` and `vote_decision = d`. Layout 0 sends 16 shares on the standard `submit_at` schedule; layout 1 sends one share (index 0) with `submit_at = 0` to `ceil(N/2)` helpers (`server_order.rs:51-53` [code]). The proxy share planner keys plans by `(round, wallet, bundle, dc_slot)` and passes `single_share = (layout == 1)` explicitly to `plan_share_submissions_with_preferred_servers`. It never infers layout from the payload count, and never reuses the votes-table planner, which is keyed by `proposal_id` (`share_tracking/delivery_plan.rs:44-64, 99-120` [code]). DC shares stay out of `derive_immediate_share` and `validate_round_immediate_plans`. **No DC share is ever the designated immediate share**, so proxy-only rounds have none (PRV-3); inside the window every share, vote or DC, is immediate anyway. "Handed off" means definite acceptance of every DC share (1 or 16) by its target helpers, reported separately from reveal progress (LIV-2). For a DC whose shares were planned with `submit_at = 0` (inside the window, either layout), completion additionally waits for the confirmed reveal of share 0 (§4.5 Job).

**Verification ("did my delegate vote").** Per delegate: ballots, inclusion, shares k/n (n = 16, or 1 for a last-moment DC). Per proposal: `Voted(o) | Abstained | NotYet | DidNotVote`. Before TALLYING, share progress comes from helper share-status, and routes from the whole-round paged list (no per-delegate query). **Sticky proof set (PRV-5, CMP-19):** at commit the client fixes and persists a 10-key set (chosen delegates plus random decoys, at least 2 because a wallet has at most 8 delegates; share-nullifier decoys come from other pools' public reveal nullifiers) and proves exactly that set at commit, pre-prove, status and final verification, so intersecting checks gains nothing. Share-nullifier IAVL proofs run once after TALLYING, and every sticky key gets the same number `m` of them, with `m = 16 ×` the largest number of real DCs to any one chosen key: real keys are padded with public reveal nullifiers from the same pool, and decoys get `m` from their own pools (all of them if a pool has fewer than `m`), so per-key query counts reveal neither real keys nor layouts (reveal events map each nullifier to its pool). Docs say plainly that helpers always learn each DC's delegate, and that helpers and the RPC operator can link the chosen delegates to the user's IP unless Tor is on.

**Recovery.**
- **R0:** the DB is present.
- **R1** (DB lost, hotkey kept; the hotkey supplies the VAN `nk`): derive `van_nf(VAN_0)`, look it up in `0x1D` (round-prefix paging by default, point lookups over Tor), decrypt the hint with the ARK (the layout comes from the hint), and follow the VAN chain. This recovers every slot and the remainder VAN, so remainder voting resumes.
- **R2** (software accounts after seed restore): the seed-derived ARK trial-decrypts the paged `0x1D` feed (about 1 µs per open). On a hit, take the layout from the hint (a byte other than 0 or 1 is rejected), rebuild the DC from `dc_seed(layout)`, **require it to equal the on-chain DC**, then rebuild 1 or 16 payloads and redeliver missing shares. Remainder voting stays lost, as today.

Hardware accounts get R1 only (Q4). Recovery is idempotent; recovered rows are locked.

**Registry client.** Parse the static `proxy_delegation` section; verify the directory index (online/offline key certificate, `seq` monotonicity, expiry, cross-mirror equivocation); mirror `/delegates` (paged, `updated_since_height`). Overlay checks: chain DIK and status match; `subject_commit = H(provider, id, salt)`; a vetted entry needs a valid curator-threshold signature; handles are pinned per favourite and past delegation, with a warning on change. Exact search matches the registry doc's current handles, domains and fingerprints locally, after a refresh (§4.6 Freshness). Immediately before proving, re-read the chosen delegates' entries from IAVL-verified chain state and require them to match the overlay. Refuse delegates that are not ACTIVE, are suspended, have a pending **recovery**, have `accepting = false`, or have a removed proof; allow a co-signed pending change with a banner (LIV-10). `proxyAvailability` also requires proven non-membership of the account's gov nullifiers; if they are spent and no local state exists, report "used from another device" and offer R2 (LIV-8).

**Gating.** No `vote_protocol` or `auth_version` bump. Requires `WalletCapabilities.proxy_delegation = ["v1"]`, a dynamic `extensions.proxy_delegation_v1.rounds[id]` entry signed by `trusted_keys` under `zcash-shielded-vote:round-proxy:v1` over binary `RoundProxyAuthPayloadV1`, and chain capabilities whose four circuit fingerprints match the compiled ones and whose `max_dc_slots` equals the compiled `2^DC_SLOT_BITS`. Gates control only preview and commit of **new** allocations; committed work, share delivery and recovery always run (LIV-12). Vizor calls `proxy::availability` and never parses these fields.

**Delegate APIs.** Phrase create and restore (§4.6); DIK/DRK derivation and restore by scanning `i < 8`, `j < 64` against `by-key`; marker text, statement and resolve/attest client; encoders for every digest; route preflight (re-fetch the route set, submit only the unrouted delta), signing and submission; rotate, freeze, revoke, cancel, change identity; the off-chain DIK-signed statement `{accepting, text ≤ 280 chars, no links, consent line}`.

### 4.5 Vizor

**Screens.** Keep the PD-1..PD-13 (delegator) and DG-1..DG-11 (delegate) inventory, with changes:
- PD-1 offers one delegation per round; after commit it shows the locked allocation.
- PD-2 (directory) browses only vetted delegates, in weighted-random order, including in picker mode; there is no popularity sort and no other browse list. Search matches only an exact X handle, GitHub handle, domain or key fingerprint, and refreshes the directory first (§4.6 Freshness); if the refresh fails, search shows an "out of date, search unavailable" state for anything outside the cached vetted fallback. Unvetted results show the handle, fingerprint, an identicon and a "Not reviewed" label, with no profile picture, display name or statement; lookalike warnings apply to search results. Entries registered after the round was created show "new this round".
- PD-3 (profile) adds a "Track record" section built from public routes: per finished round, "voted on N of M proposals", and "suspended during round" if it applies. It shows no ZEC totals and no delegation counts.
- PD-4 shows "Voting ends {time}" and keeps the 24 h "delegates may not have time to vote" notice. `tooLate` is the round-closed state votes use (`now ≥ vote_end`).
- PD-5/PD-10 have the finality checkbox (one delegation per round, final), an amount-naming CTA and the list of covered proposals. The delegate picker stops at 8.
- PD-6 completes at "Handed off". For a DC whose shares were planned with `submit_at = 0`, it waits for the confirmed reveal of share 0, as a vote's immediate share does; at `vote_end` it fails with the vote path's expired-share message, and after TALLYING the DC shows "Not counted".
- PD-7 shows shares as k/n (n ∈ {1, 16}) and adds a pending-recovery banner and an "on track" state for slow shares.
- PD-8 (results) shows per-option totals as today, which include delegated weight. For each option it names the vetted delegates whose routes chose it and summarizes the rest as "and N unreviewed delegates", so results never become a browsable list of unvetted delegates. It shows no direct/delegated split, because pool totals are hidden.
- DG-2 becomes phrase create/confirm; DG-12 (software account required) is removed; DG-8 (dashboard) shows the delegate's own approximate delegation count, live, and their route track record, and no ZEC total; DG-9 drops the side-by-side column of the delegate's own private votes; DG-11 gains "Stop accepting" (a statement) and "Emergency pause" (freeze).

**Job.**
- `VotingSubmissionJobNotifier` gains `kind {ballot, proxy, proxyAndBallot}`, `ProxyJobStage` and `terminalReason`.
- The proxy allocation is recorded before the hardware/software branch, so Keystone and Ledger users who only delegate are still asked to sign ZKP1.
- Cancel is allowed only before the first broadcast.
- COMPLETE requires definite helper acceptance of every DC share and, for DCs whose shares were planned with `submit_at = 0`, the confirmed reveal of share 0. If `vote_end` passes first, the job fails like an unconfirmed immediate vote share (`voting_submission_job_provider.dart:1469-1497` [code]). Without this, a late proxy-only DC would report success, because `hasConfirmedImmediateShare` returns true when there is no designated immediate share (`voting_resume_plan.dart:16-19` [code]).
- If the chain is paused (D9), the job shows the "Paused" copy, retries if the pause lifts before `vote_end`, and offers "Vote with this ZEC instead", which runs the release path (§4.4).
- Hold a wakelock from proving through hand-off (optional polish).
- On startup or poll open, run R2 for software accounts whose gov nullifiers are spent in a proxy-enabled ACTIVE round with no local state, then redeliver.

**Kill switch.** The Vizor remote kill switch hides the delegation entry points and stops new allocations from being committed, without a coordinator quorum. Committed work, share delivery, verification and recovery keep running (LIV-12). Its round-level effects are in §8.3.

**Delegate mode.**
- The phrase root sits in app-level secure storage behind re-auth.
- The DRK is stored with **per-use user presence** (Keychain/Keystore access control), not session unlock (IDN-7).
- Routes need re-auth plus a confirm sheet naming the choice.
- Tor is **default-on** in delegate mode for route submission and for the delegate account's own vote and share traffic. If Tor is off, a blocking prompt appears before the first route or private cast (PRV-7).
- The dashboard shows a blocking banner when any recovery is pending on the user's entry.
- The route deadline is `vote_end_time`, with a warning in the final 5 minutes and a blocking confirm in the final 60 s saying that a route landing after close leaves the whole pool abstaining on that proposal (the transition block rejects routes, `keeper_voting.go:336-338`, `module.go:491-497` [code]; idempotent retries cannot help after close).

**Images and networking.**
- WebP only, at exactly 64 or 256 px.
- The sha256 is checked against the profiles doc, the image is decoded in Rust with `image-webp`, and drawn with `decodeImageFromPixels`.
- `Image.network`, `Image.memory` with encoded bytes, and `instantiateImageCodec` on network bytes are banned by a grep test (CMP-14).
- Directory, verifier and mirror hosts go in the Tor-aware route table.
- Avatars exist only for vetted delegates and always come from packs: 64-px packs are sharded by index range, and 256-px images come only through prefix-shard packs.

**Deep links.**
- Format: `https://link.vizor.cash/d#v1.<index>.<fp>`. The fingerprint is mandatory.
- A deep link opens the delegate's entry whether or not it is vetted; an unvetted entry shows the "Not reviewed" presentation.
- On mismatch, Delegate is blocked until the user reviews both keys.
- There is a desktop paste fallback, and the server has one exact `/d` route.

**Directory freshness.**
- Fail closed for directory-only blocking flags (removed proof, impersonation, lookalike, "under review").
- When a refresh fails, accept a verified cached snapshot up to 1 h old, with an "out of date" banner, but only for vetted, unflagged entries registered before the round (LIV-6). Search uses the same rule, so unvetted delegates, and vetted ones registered after the round was created, are found only after a successful refresh.

**Copy rules** (sentence case, no em dashes). These replace the v1 strings:

| Key | New string |
|---|---|
| C4 | "Your delegate can't see who you are or how much you delegated, and the total each delegate receives is never published. Your ZEC counts in your delegate's public votes with the same privacy as voting yourself." |
| C7 | "Helpers add your delegation to the count before voting ends, at random times if you delegate early, or right away in the final hours. Vote servers can see which delegate you chose, but not who you are." |
| C8 | "If you reinstall Vizor before your delegation finishes sending, open Vizor again before voting ends so it can finish." Add for software accounts: "After restoring from your recovery phrase, Vizor can find your delegations." |
| C9 | "Your delegate votes for you on every proposal in this round. Delegate some or all of your voting power to people you trust." |
| Finality | "You can delegate once per round. After you confirm, you can't change it." |
| Voting ends | "You can delegate until voting ends {time}, just like voting." |
| Late (optional, shared by votes and delegations, final 10 minutes) | "Voting ends in {m} minutes. Votes and delegations sent now may not be counted in time." |
| Not counted | "Voting ended before a helper could count this delegation. It was not counted." |
| Not reviewed | "Not reviewed. Vizor hasn't checked this delegate. Compare the fingerprint with the one in their post before you delegate." |
| New this round | "New this round. Registered after voting started." |
| Paused | "Delegating is paused for this round. Delegations already made still count, and you can still vote." |
| DG-1 additions | "Your public votes stay linked to your X account permanently, even if you delete your post or account." / "An approximate count of delegations to you is public on the voting chain. Vizor shows it only on your dashboard." / "Your private votes are hidden from the public. Vote servers can link them to you unless Tor is on." |
| Not browsable (DG-1 and dashboard, until vetted) | "You won't appear in the browse list unless curators review you. Share your link or handle so people can find you." |
| Stop accepting | "Vizor will stop offering you as a delegate. People who already delegated stay with you, and you can still vote publicly." |
| Emergency pause | "Pausing blocks your public votes until you set a new route key with your identity key. Proposals you haven't voted on will count as abstain for everyone who delegated to you." |
| Kept ZEC | "Vote with the ZEC you kept from this device before voting ends." |

Other copy fixes:
- The ZKP1 copy becomes "voting authorization" everywhere (including the Ledger strings).
- Delegation counts appear only on the delegate's own dashboard, always as approximate ("at least N", N = `ceil(R/16)` for R revealed shares; the true count lies between `ceil(R/16)` and R, §5 (iv)), and are never a sort key.
- Optional and shared by votes and delegations: a device-clock skew warning when the device is more than 60 s off chain time.

**UGC (IDN-13, CMP-22).**
- An in-app report sheet POSTs through `NetworkHttpClient` to the verifier's `/v1/reports`, with no account id. A local "Hide delegate" option.
- Display name, statement and avatar exist only for vetted delegates, and name and statement are shown only when the profiles doc marks `text_state = approved`. Unvetted delegates show only a handle, fingerprint and identicon.
- Delegate onboarding requires accepting the content terms.
- App Privacy labels; reviewer notes with a stage build, a demo delegate and an open test round, explaining that user-generated profiles are limited to the curated, moderated vetted list and that other delegates appear only by exact search, without pictures or text.

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
- It enforces its own per-kind budgets (RECOVER far below REGISTER), a per-index cooldown and a vetted-entry refusal, using its own chain view.
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
- **Registry doc** (every registered delegate): index, DIK, provider, subject id, the current X or GitHub handle or domain (so exact search works), salt, flags, the `accepting` flag from the latest DIK-signed statement (so the registry client can refuse any delegate that stopped accepting), and the curator-signed vetted list and log. Open to other wallets once counsel clears handle redistribution (Q10).
- **Profiles doc** (vetted delegates only; Vizor-only until counsel clears redistribution): display name, statement text, `text_state`, avatar hashes. `expires_at ≤ issued + 24 h − purge SLA` (PRV-8).
- **Avatar packs** (vetted delegates only): content-addressed and sharded by index range.
- Served from three mirrors, with wallet equivocation checks.
- No per-round pinning; the chain overlay plus the hash chain is the consistency anchor (CMP-13).

**Freshness and mid-round registration (D10).**
- Delegates can register at any time while `registration_enabled` is on (§4.2 Coordinator payloads). The chain assigns the next index immediately (`0x1A 04`), and the delegate can route in rounds already running (§4.2, 0x0C).
- The directory is not frozen per round. The chain registry is append-only and synced with `updated_since_height`, and the publisher regenerates the signed directory within about 10 minutes of a chain registry change.
- In Vizor, a search triggers a directory refresh first. Vizor downloads the whole registry doc and searches it locally, so nobody learns who a user looked up.
- Before proving, Vizor cross-checks the chosen delegate against IAVL-verified chain state (§4.4 Registry client), and the proof's `delegate_index_bound` is `NextDelegateIndex − 1` at proving time (§4.1).
- The chain accepts pool shares for any index that exists at reveal time (§4.2, 0x04).
- Entries whose index is at least the round's `next_delegate_index_at_creation` show "new this round". Curators may add them to the vetted list mid-round.

**Retention and deletion** (PRV-8, IDN-12).
- On a purge, delete or rewrite every historical registry, profiles and pack object containing the subject. Keep only the current documents plus a short overlap, and purge the CDN and every mirror.
- Keep a keyed-hash tombstone of `(provider, subject_id)` for uniqueness checks.
- The delegate's wallet keeps its own salt, so a later recovery can still prove the subject.
- The "crypto-shredding makes the commit unlinkable" claim is withdrawn: published salts mean attribution is permanent, and DG-1 says so.

**Curation.**
- **Vetted list.** The browse list contains only delegates in the curator-signed vetted list: 2-of-3 curator signatures (Valar and Vizor keys pinned in the static config), no size cap, and a public log of additions and removals. Curators review the proof, account history and lookalikes, and confirm the fingerprint out of band for every vetted entry (IDN-11). They may add a delegate at any time, including mid-round. Only vetted delegates get hydrated avatars and display text, and those are moderated.
- **Exact search.** Every other registered, unflagged delegate is reachable only by exact (case-insensitive) search on X handle, GitHub handle, domain or key fingerprint, or by a deep link. Results show the handle, fingerprint, an identicon and a "Not reviewed" label, with no profile picture, display name or statement.
- **Lookalike detection** uses UTS #39 skeletons and applies to the vetted list and to search results:
  - precedence goes to whichever `(skeleton, subject)` pair the directory saw first, not to registration order;
  - the check is re-run on every handle change, flagging the entry that renamed;
  - skeletons are reserved for 12 months, shown as "handle previously used by #N";
  - a seeded list of reserved ecosystem names.
- **Successor links** ("re-registered as #N") come only from a DIK-signed statement or after curator review.
- **Reports:** Valar support, with a 24 h target for impersonation.

**Ops.** valargroup DigitalOcean hosting; signer keys in KMS or an HSM on a dedicated host (no YubiHSM on DO); monitoring of X API errors and spend, purge SLA, mirror divergence, directory freshness (regeneration within about 10 minutes of a chain registry change) and the re-verifier; playbooks for verifier-key compromise (removing the key now voids its pending recoveries), X outage (GitHub and DNS continue), and directory-key compromise (offline-key revocation).

**Legal (CMP-21).** Before the X provider reaches production, counsel signs off on the X Developer Agreement in force at launch (attestation use, handle and profile redistribution, political-data rule), privacy notices and retention, the CSAM reporting duty, the vetted-list disclaimer and vote-market policy.

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
| Which delegates a delegator chose | The public and the delegate, at DC time | **Not hidden from helpers**: each share fans out to `ceil(N/2)` vote servers, about every server, which learn `d`, the DC leaf and IP, as they learn each vote's option today (the helper payload carries `proposal_id` and `vote_decision`, `internal/helper/types.go:121-131` [code]). Helpers run inside validator nodes, so the EA key holders' own helpers learn `d` (§5 Validator trust). The DC count per tx is public. For delegates with very few delegators, the 16 reveal times can narrow which DC tx it was: [measured] median candidate sets of 4, 8, 29 and 112 DC txs at 0.1, 0.25, 1 and 4 DC txs per hour, unique in 17%, 5%, 1% and 0% of trials (`prototypes/privacy/reveal_linking.out`). A DC made in the last-moment window is revealed within seconds of its tx, which publicly links the tx to its delegates, exactly as a late vote's reveal links its tx to its option; a batch with k late DCs links its k delegates together. |
| Amount per delegation | The public, helpers and the delegate | Only per-option totals are decrypted. An amount is exposed only by isolation, when its pool is the only weight on an option and has one delegator, as a lone direct voter's amount is; isolation is easy to spot, because `BallotCount` counts only direct shares (`keeper_tally.go:197-206` [code]). Validator collusion is covered under Validator trust. |
| Per-delegate pool totals | The public and the delegate | Never decrypted on their own. Exposed only when a pool is the only weight on an option, or by a coalition of at least t validators. Approximate counts are public (below). |
| A delegator's total and kept weight | The public | Kept weight is voted privately like any vote. Splitting across several delegates means any single exposure reveals only that slice. |
| Delegator's choices from viewing-key holders | UFVK holders | The ARK is never derived from the FVK. |

**Public:** routes; that an anonymous batch contains k DCs; per-delegate pool reveal counts, live during the round (16 per DC made before the last-moment window, 1 per DC made inside it, so delegation counts are approximate; Vizor shows them only on the delegate's own dashboard); per-option totals, which include routed pools; per-option direct share counts (VoteSummary `BallotCount`, which excludes pool shares); delegate registry entries and route track records.

**Validator trust.**
- Amount privacy for both direct votes and delegations relies on fewer than t election-key holders colluding (t = 7 of 10 validators, `ThresholdForN`, `x/vote/keeper/keeper_ceremony.go:53-65` [code]).
- The helper runs inside validator svoted nodes (`docs/runbooks/join-chain.md:173` [doc]), and each helper payload carries the VC tree position and all 16 share commitments (`internal/helper/types.go:121-131` [code]: `tree_position`, `share_comms`), so a helper can tell which shares belong to the same vote or delegation.
- A coalition of at least t validators that keeps helper intake can therefore reconstruct individual amounts, for direct votes and delegations alike. With keys alone it can decrypt any pool ciphertext and any single share, including the whole amount of a last-moment DC or vote. Decrypting `Pool[d]` with keys alone gives that delegate's total and, if the pool has one delegator, that delegator's whole amount; grouping a direct voter's 16 shares needs helper intake (a last-moment single share does not).
- Delegators get the same protection as direct voters, under the same assumption.
- A lone delegator in a pool has the same exposure as a lone direct voter on an option: an anonymous amount, exposed only by isolation in the published per-option results or by validator collusion.
- Splitting across several delegates means any single exposure reveals only that slice.

**D4 and D5, normative wording for ZIP-PD, the book and the FAQ:**

> Per-delegate pool totals are not published. When voting ends, the protocol adds each delegate pool into the option its delegate routed on each proposal and threshold-decrypts only the per-option totals. No pool is decrypted on its own.
> (i) Per-option totals include delegated weight. A pool that is the only weight on an option is exposed by that option's total, as a lone direct voter's amount is. If all of that pool's delegations came from one delegator, the amount is theirs, though their identity is not published.
> (ii) Which delegates a delegator chose is not published, but vote servers learn the delegate of each delegation commitment they process, as they learn the option of each vote. Reveal timing can narrow which anonymous transaction fed a pool with few commitments. A commitment made in the last-moment window is revealed within seconds of its transaction, which publicly links that transaction to its delegate, as a late vote's reveal links its transaction to its option.
> (iii) Amount privacy, for delegations and direct votes alike, assumes that fewer than t election-authority key holders collude. Validators hold the key shares and run the vote servers, so a coalition of at least t validators can decrypt any pool and, using what its vote servers received, reconstruct individual amounts.
> (iv) Approximate delegation counts are public, live during the round: a pool's revealed-share count R bounds its number of delegation commitments between ceil(R/16) and R.
> Delegators get the same amount privacy as direct voters, under the same assumption.

The optional post-close reveal grace window (Q18) would remove the timing link in (ii) for votes and delegations alike; it is not in v1.

**Abstain option.** [measured] With sentinel abstain and Yes/No proposals, 0% of pools were exactly solvable from public results; an explicit Abstain option made 42-44% solvable at 15 proposals, 30 direct voters and 5 delegates. While totals are hidden, an Abstain option is therefore a privacy risk, so v1 keeps the recorded-not-counted sentinel and leaves the option as product question Q2 (default no).

**Integrity.** A VAN is either cast from or delegated from, never both (one nullifier set). No `VC(0, d)` can exist (the ZKP2 gate, and the chain rejecting casts with p < 1). Weight is conserved over the integers, each share is added once, and each pool lands in at most one option per proposal. **Accountability:** routes are signed, immutable and applied to the whole pool; a delegate cannot drop individual delegators. Anyone can recompute the routing identity on ciphertexts (`routing-audit` `routing_verified`). Not covered: a delegate's private vote may contradict its route, and the X binding is trusted to the verifier.

**Identity attacks and defenses.**

| Attack | Defense |
|---|---|
| X takeover recovery | 7 d delay; DIK-only cancel; recovered keys cannot route rounds that already existed; pending recovery blocks new delegations; banners; a removed verifier voids its pending recoveries |
| Verifier API compromise | The signer re-verifies; independent re-verifier pages |
| Directory key theft | The vetted list needs curator signatures; wallets pin handles; equivocation checks; offline revocation |
| Impersonation by an unvetted lookalike | Not browsable, including in PD-8 results, which name only vetted delegates; exact search only; no avatar or display text; "Not reviewed" label; lookalike warnings on search results; fingerprint on every surface |
| DRK theft | Per-use presence; DIK rotation; can route this round's unrouted proposals once (accepted) |
| Coordinator key | Consensus delay floors; verifier warm-up; suspension is immediate, freeze-only and public with a reason code; every proxy payload needs at least 2 approvals, reaching 2-of-3 within the first two proxy rounds (D7). Coordinators remain fully trusted, as they already are via x/upgrade. |

**Not provided:** receipt-freeness (a delegator can prove its DC opening; pools are vote-market aggregation points, cf. LobbyFi), and per-delegate censorship resistance (reveals and routes tagged `d` are attributable; helper redundancy and multiple vote servers mitigate).

**Delegates' own privacy.** Vote servers can link a delegate's private votes to their public routes unless Tor is on, so delegate mode defaults Tor on.

---

## 6. Hole register

Severity is after skeptic review. "Unverified" means not re-checked by a skeptic; these are adopted where cheap. Grouped rows share one fix.

| ID(s) | Sev. | Issue | Resolution |
|---|---|---|---|
| IDN-1 | high | Takeover or rogue-verifier recovery captures pools mid-round; removing a verifier doesn't stop it | `recovered_at_time` bars older rounds; pending recovery voided below threshold; DIK-only cancel; banners |
| IDN-2 | high | Signer signs opaque digests; alarm reads API-written table | Signer recomputes and re-fetches; own budgets; independent re-verifier |
| IDN-4 | high | One online directory key controls handle, avatar and curated status | Curator-signed vetted list; handle pinning; independent auditor preferred |
| CMP-1, OPS-1, IDN-6(b), LIV-10(b) | high | DRK unfreeze defeats freeze; freezes replayable | No unfreeze; exit FROZEN only by DIK rotation; per-block dedupe |
| IDN-3 | medium | One coordinator key can install verifiers and zero delays | Consensus delay floors; verifier warm-up; trust model stated; every proxy payload needs ≥2 approvals, 2-of-3 within two proxy rounds (Q5 decided, D7) |
| IDN-5 | medium | Any post or retweet with the marker binds an account | Original post, whole-template match |
| IDN-6(a,c) | medium | DRK thief blocks recovery; cancels burn change cap | DIK-only RECOVER cancel; cap counts effective changes; 30 d cooldown |
| IDN-11 | medium | Lookalike precedence by registration order; empty curated list at launch; inherited successors | First-seen precedence; rename re-check; reservations; OOB fingerprint confirmation for every vetted entry; signed successor links |
| IDN-13, CMP-22 | medium | Conflicting app-store UGC controls | UGC limited to the vetted list; in-app report, `text_state`, one text limit, terms |
| PRV-1 | medium | OVK-keyed ARK exposes delegations to UFVK holders | Spending-key or hotkey ARK |
| PRV-2, CMP-17 | medium | Abstain option creates solvable buckets; claims overstated | No Abstain option in v1 (sentinel kept; Q2, default no); §5 claims restated at parity with direct votes; exposure stated in §5 rather than warned in the app (D11) |
| PRV-4, LIV-4, CMP-11, SND-8 | medium | Five cutoff rules; single-share DCs leak; hint key reuse | Vote parity (D5): one rule shared with casts (open until `vote_end_time`; single share iff `is_last_moment()` at batch planning); layout frozen per batch for DCs and casts, `(d, w, slot, layout)` per `van_nf`, before the first proof; same host clock as votes; authenticated timing required; timing and EA exposure stated as identical to late votes |
| PRV-7 | medium | Vote servers link delegate's private votes to identity | Tor default-on in delegate mode; honest copy |
| LIV-1 | medium | Helper fleet caps scale; Zodl votes also lost | Budget (16 per pre-window DC, 1 per in-window DC), metrics, more workers, Zodl non-regression gate including in-window DCs |
| LIV-3 | medium | Locked allocation strands unexecuted bundles | `release_proxy_bundle`, including bundles a chain pause rejected |
| LIV-5, CMP-10 | medium | Adding to a locked allocation was undefined; no `dc_slot` | One delegation per round, never changeable (D2); `dc_slot` persisted |
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
| CMP-19, PRV-5 | medium | Per-index lookups leak choices | Whole-list sync (registry and routes); sticky proof set with equal per-key nullifier counts |
| CMP-23 | medium | Audit scope too narrow | Two tranches |
| CMP-25 | medium | Unlisted owner questions | §7 |
| SND-1, CF-3 | low | Slot nf missing from chain and genesis paths; rand invariant | Full wiring checklist (§4.2); invariant in ZIP-PD |
| SND-6, CF-1 | low | Bound open; width and value rule | Adopted at 30 bits (the prototype still checks 40, §4.1 C15); builder takes `NextDelegateIndex` |
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
| LIV-6 | low | Mirror outage blocks delegation | 1 h cached fallback for clean vetted entries; third mirror |
| LIV-7, LIV-8 (unv.) | low | C9 misleads; cross-install surprises | C9 reworded; gov-nullifier gate |
| LIV-9, OPS-11 (unv.) | low | Helper index check before leaf | Leaf first; 503 |
| LIV-11, LIV-12 (unv.) | low | Route retries; gates halting work | Idempotent entries; gates only for new allocations |
| OPS-2 | low | Genesis rejects type 3 | Genesis section; `ShareCountKey` reuse |
| OPS-4, CMP-20 | low | Beta and Zodl; enable race | Stage beta; Zodl limits; runbook rule; sign-off |
| OPS-5 | low | Zakura 2.0 swap unchecked | Corpus replay gate |
| OPS-7 | low | No mid-round brake | `proxy_dc_paused` plus the Vizor kill switch (D9) |
| OPS-9 | n/a | Not applicable: depended on a redesign that is not shipping | n/a |
| OPS-10 (unv.) | low | Proof-verification cap risk | No cap in v1 (decision 24) |
| OPS-12, OPS-13 (unv.) | low | `ProposalTally(0)`; routing cost | Reject 0; droplet benchmark; `max_delegates` 20,000 |
| CF-2, CF-4, CF-5 (unv.) | low | W=0 casts; key RAM; digest kind tag and split-off | Dead successor; phased proving; kind byte; re-plan |
| CMP-9 | low | ZIP-PD leaf order reversed | Corrected |
| CMP-21, CMP-24 | low | No legal item; missing ops tooling | Legal gate; V20 |
| CMP-26..29 (unv.) | low | Vocabulary, analytics, perf, support gaps | contracts-v1 vocabulary; aggregate analytics with no per-delegate counts or totals; device and droplet benchmarks; support runbook and delegate reminders |

**Revision 2 and 3 rows** (owner changes and their reviews):

| ID(s) | Sev. | Issue | Resolution |
|---|---|---|---|
| PUB-1..9 | n/a | Superseded: totals hidden in rev 3 | n/a |
| LMD-1 | owner | Delegation closed up to 6 h before voting ended | Parity (D5): open until `vote_end_time`; single share in the window; pools routed at close; no chain, helper or VK change |
| LMD-2 | medium | Layout frozen per DC only; casts in a batch re-proved across the window boundary fail `recovery_matches_draft` | Layout persisted once per 0x09 batch before the first proof, for DCs and casts |
| LMD-3 | medium | A late proxy-only DC whose reveal misses close reports success (no designated immediate share) | In-window DC completion waits for the reveal of share 0; fails at `vote_end`; "Not counted" |
| LMD-4 | medium | Sticky-set share-nullifier counts (1 vs 16 per DC) reveal real keys and layouts | Equal count `m` per key, padded from the same pool |
| LMD-5 | medium | A late DC's reveal links its anonymous tx to its delegate | Accepted as parity with a late vote's reveal; stated in §5 (ii); grace window later (Q18) |
| LMD-6 | low | The votes-table delivery planner cannot hold DCs; layout inferred from payload count | Proxy planner keyed by `dc_slot`; explicit `single_share` |
| LMD-7 | low | Late `KeepForSelf` remainder needs a second tx-and-reveal cycle | Ask for remainder choices before dispatch so casts ride in the same 0x09; warn if declined |
| LMD-8 | low | Missing round timing silently disables `VoteEnded` and `is_last_moment()` | `proxy::availability` requires authenticated timing for new allocations |
| LMD-9 | low | Route-margin removal shifts risk; load framing optimistic; C9 rejection not isolated; SND-8 safe by policy only; `revealCloseTime` touches shared validation | Final-5-minute warning and 60 s confirm; pass criteria scoped to shares accepted ≥10 min before close; isolated C9 test; optional AES-SIV hint; pure refactor with a GasUsed test, or defer |
| CRD-1 | low | Under D7, incident levers (`proxy_dc_paused`, suspension) need two coordinator approvals | Two coordinators on call for every proxy round; runbook pre-drafts the lever payloads; the Vizor kill switch needs no coordinator quorum |
| TRU-1 | high | Helpers run inside validators and see each payload's tree position and all 16 share commitments, so a coalition of at least t validators that keeps helper intake can reconstruct individual amounts, for direct votes too | Stated as the v1 trust assumption in §5 Validator trust and the normative wording (iii); label-free reveals would not fix it (decision 31); a real fix needs helpers separated from key holders, which is out of v1 scope |

**Partly refuted:** PRV-2's 81% assumed unrouted pools map to Abstain (real figure 42-44%); OPS-4's Zodl visibility claim fails for Zodl ≥3.9.5, which shows only endorsed rounds (only ≤3.13.x throws on unparseable rounds); IDN-8's replay-after-reset (the verifier refuses reused DIKs); LIV-2's "COMPLETE before delivery"; IDN-6(c)'s 64-cycle exhaustion; IDN-3's "new trust" (coordinators can already push binaries); OPS-7's reveal pause (unimplementable); CMP-3's on-chain X ids (no proto field exists).

---

## 7. Owner questions

Q16 and Q17 were withdrawn in rev 3 with the published totals.

### 7.1 Decided

| # | Decision | Where |
|---|---|---|
| Q1 | **Answered (rev 2): delegation stays open until voting ends, like voting.** DCs in a batch planned in the last-moment window use the vote path's single-share layout and are sent at once; pools are routed at close with every reveal that landed before `vote_end_time`. As with votes, a delegation whose reveal misses close is not counted, and Vizor says so. | D5, O3, §4.4 |
| Q5 | **Accepted (rev 2): coordinator suspension with safeguards.** Immediate, freeze-only, with a public reason code; it cannot create entries, change keys or void routes. Every proxy payload (params, verifiers, suspension) needs at least 2 vote-manager approvals regardless of the global threshold; at least 2 vote managers before activation, and 2-of-3 within the first two proxy rounds. | D7, §4.2, §8.3 |
| Q8 | **Decided (rev 3): per-delegate totals hidden.** No leaderboard and no ZEC totals on profiles or dashboards. Profiles show a route track record ("voted on N of M proposals"). Approximate delegation counts appear only on the delegate's own dashboard, marked approximate, never as a sort key. | D4, §4.5 |
| Q9 | **Decided (rev 3): vetted-only browse list.** The browse list shows only vetted delegates, uncapped, signed 2-of-3 by curators (Valar, Vizor), with a public log and OOB fingerprint confirmation for every vetted entry. Human review for vetted avatars; unvetted delegates have no avatar or display text. | D3, §4.6 |
| Q15 | **Decided (rev 3): never.** One delegation per round; a committed allocation is never changed or added to. The release path for bundles whose batch never landed is failure recovery, not a change. | D2, §4.4 |
| O4 | **Confirmed: a separate delegate key phrase.** | D6, §4.6 |

### 7.2 Open

| # | Question | Recommended default |
|---|---|---|
| Q2 | Add an explicit "Abstain" option to proposals in proxy rounds? While totals are hidden it is a privacy risk ([measured] 42-44% of pools become exactly solvable in small rounds, against 0% with the sentinel), and Zodl would have to present it. | No. Keep the recorded-not-counted sentinel. |
| Q3 | Minimum per delegate | 1 ballot (0.125 ZEC) protocol floor and round default. Raise `min_ballots_per_delegate` per round only if load tests show pressure. |
| Q4 | Offer Keystone/Ledger delegators opt-in recovery keyed to the viewing key? Viewing-key holders would see their delegations. | Not in v1. Hardware delegators keep R1 only, the same as votes today. |
| Q6 | Verifier trust at launch | 1-of-1 Valar for registration; recover threshold 2 (two Valar keys, two-person approval); an independent second verifier within 6 months |
| Q7 | Beta venue | Stage chain. Mainnet test rounds only within old-Zodl limits (≤15 proposals, 2-8 contiguous options), never Zodl-endorsed. |
| Q10 | May other wallets consume the directory? | Registry doc (IDs and current handles) once counsel clears handle redistribution; profiles doc Vizor-only until counsel clears it. |
| Q11 | Who handles reports, and how fast? | Valar support; 24 h for impersonation and offensive content; same-day delisting when confirmed. |
| Q12 | Per-round enablement | Opt-in per round; enable for all public rounds after one successful mainnet proxy round. |
| Q13 | Ship follow-mode v0 (directory plus signed voting guides) a round early? | Only if the verifier MVP is early and it does not touch the circuit path. Do not plan on it. |
| Q14 | X API budget. Avatars and display text are hydrated only for vetted delegates (decided, D3); the registry doc still needs current handles for every registered delegate. Refresh unvetted handles daily, and weekly once dormant (no route in 3 rounds)? | Yes. [inference] API spend scales with lookups (all registered delegates at the daily or weekly cadence), because one users lookup returns the handle and profile together; de-hydrating unvetted delegates mainly removes avatar re-encoding and moderation. Re-estimate against the X price list at launch. A stale handle can only point at the delegate bound by numeric id, and handle pinning warns on renames. |
| Q18 | Post-close reveal grace window G (reveals only, accepted until `vote_end + G`, before routing and any decryption), shared by votes and delegations? It removes the timing link of late votes and DCs and lets helper backlog drain. | Not in v1. Design for v1.1 after load-lab data, as a separate coordinated upgrade, with Zodl notified (results arrive G later). v1 only adds the `revealCloseTime` refactor (§4.2). |

---

## 8. Rollout and milestones

### 8.1 Work packages

Sizes: S is 1-3 days, M is 1-2 eng-weeks, L is 2-4 eng-weeks. Per-repo totals are rough.

- **Specs (chain lead, spec lead):** SP1 `contracts-v1` (M, weeks 0-3): proto, JSON names, tags, REST, digests, KV table, capabilities, errors and vocabulary, FRB DTOs, Go and Rust golden vectors in CI; **blocks every implementation PR**. SP2: ZIP-A errata (S), ZIP-PD (M; ZKP4 statement, including the 3-bit slot check, frozen by week 3; its timing section, spec_rollout §1.4.11, is rewritten for parity: delete "SHOULD finish before vote_end − buffer" and "MUST NOT start after vote_end − 30 min"; the wallet MUST use the vote path's last-moment predicate and `VoteEnded` check, MUST require authenticated round timing for new allocations, and MUST freeze the batch layout and `(d, w, slot, layout)` per `van_nf` before the first proof; it also carries the §5 normative wording and the validator trust assumption), ZIP-DR (M; states the coordinator trust model and the D7 approval floor), WAPI/SUB/SETUP amendments (S), book rewrite (M, to week 15).
- **voting-circuits (about 8-9 eng-weeks):** constants (S); ZKP4 with C14 (3-bit slot check) and C15 (M); MockProver suite (M); builders with the `dc_seed` PRF (M); layout-1 secrets, 0x14 path and vectors (S); prove, verify and fingerprints (S); exports and vectors (S); cross-circuit tests (S); benchmarks with mobile RSS (S). rc.1 by week 9.
- **vote-sdk (about 20 eng-weeks):** proto and dormant gating (S); types and digests (M); capacity guard (S); registry (L); routes (M); ZKP4 FFI (M); 0x09 (L); p=0 reveal (S); round snapshot, params, pause (S); routing hook (M); queries, indexes, feed, capabilities, `routing-audit` (M); genesis (M); helper branch, telemetry, metrics (M); upgrade and runbooks (S); e2e (L); determinism and droplet benchmark (S); corpus replay gate (S); activation (S); D7 approval floor for proxy payloads (S). **V20 ops (M):** CLI and coordinator-UI support for new payloads, including multi-approval proxy payloads and a pre-drafted `proxy_dc_paused` on/off pair; Prometheus metrics with an alert on permanent p=0 rejections and on `routing_verified = false`; a scripted canary delegate and delegator per proxy round (one pre-window DC and one in-window DC at about T−10 min) asserting `routing-audit` `routing_verified` and that the canary's shares and route landed; explorer event docs.
- **zcash_voting (about 19 eng-weeks):** planner with the 8-delegate cap (M); secrets, phrase, digests (M); circuits integration and phased proving (M); schema v25 (L); effective-weight refactor (L); batch builder and packer (L); submission lifecycle and split-off (L); planner obligations and release path, including paused batches (L); DC share delivery (M); verification and sticky proofs (M); recovery (M); gating (M); directory client with exact search and the pre-proving chain cross-check (M); delegate APIs and crate (M); integration, vectors, mobile benchmarks (M); layout choice, batch freeze, explicit-layout share planner, equal-count sticky proofs (S-M).
- **Verifier service (about 10 eng-weeks):** API and providers (L); hardened signer and key ceremony (M); indexer, re-verifier, refresher (vetted hydration, handle-only refresh for the rest), retention (M); avatar pipeline for vetted delegates (M); publisher (registry doc with current handles, regenerated within about 10 minutes of chain changes), mirrors, curator signing of the vetted list (M); curation, reports, moderation (M); ops (M). Legal review runs externally in weeks 4-10 and gates the X provider.
- **Config repo** (S) and **deeplink server** (S).
- **Vizor (about 15 eng-weeks):** UI on mocks from week 4, integration from week 11. Includes the vetted browse list, exact search with the "Not reviewed" and "new this round" states, the route track record and the dashboard's approximate count (S-M), the paused state and release offer (S), and in-window DC completion gating with the "Not counted" state (S).
- **Stage, load lab and QA (about 5 eng-weeks); specs and book (about 6).**

### 8.2 Timeline and critical path

| Milestone | Weeks | Notes |
|---|---|---|
| M0 contracts-v1, ZKP4 statement, ZIP-DR digests frozen | 0-3 | Critical |
| M1 voting-circuits 0.13.0-rc.1 | 2-9 | Critical |
| M2a Audit tranche 1: ZKP4 (both layouts, 3-bit slot check), 0x09, p=0 reveal, routing and ρ, registry handlers | 9-13 | Critical; book auditors now |
| M2b Audit tranche 2: client secrets and recovery (including layout-1 `dc_seed` and hint vectors, and the optional SIV hint), phrase format, registry digests and attestation counting, signer custody, directory and vetted-list signing, Rust image decode, IAVL verification | 12-15 | Launch gate |
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
- **Total:** about 83-86 eng-weeks plus two audits [inference]. The earlier 60 eng-week estimate predates the scope the review added (verifier hardening, recovery records, ops tooling, the second audit tranche). Revision 2 added about 6-8 eng-weeks (published totals, leaderboards, last-moment parity); revision 3 removes about 5-7 of those (the post-round decryption phase and its load tests, leaderboards, the results client) and adds about 1-2 (vetted-only browse with exact search, "new this round", paused-batch release, the pre-proving chain cross-check, handle-only refresh, the routing audit query), so last-moment parity is the main net addition over revision 1's 80-85 [inference].
- **Staffing:** circuits 1-2, chain 2-3, client 2, Vizor 2, identity 2, specs/QA 1.

### 8.3 Activation

1. Merge dormant PRs to main under `V:state/breaking`.
2. Run the corpus replay gate: the candidate binary's FFI verifiers check every vote tx from recent mainnet rounds (ZKP1/2/3 proofs, RedPallas, TX1 sighash) with byte-identical accept/reject results against v1.6.x. This covers the Zakura 2.0 dependency swap (OPS-5).
3. Run the coordinated v1.7.0 halt **between rounds**: all rounds FINALIZED and helper queues empty. Every later upgrade halt follows the same rule.
4. Post-checks: capabilities on (fields 4-13, with `max_dc_actions_per_tx = 8` and `max_dc_slots = 8`), the four fingerprints match, the registry is empty.
5. Coordinator actions: confirm at least 2 vote managers in the coordinator policy (add one with `MsgUpdateVoteManagers` if needed), because every proxy payload needs 2 approvals (D7); then set the verifier set and params (`enable_for_new_rounds` false). Reach 2-of-3 within the first two proxy rounds.
6. Register the canary delegate, sign the launch vetted list, and publish the directory to three mirrors.
7. Per round: change `enable_for_new_rounds` only when no create-session action is pending, verify the created round's snapshot, then sign the config extension.

**Mid-round off switch (D9).** Two levers turn proxy delegation off during a round: the coordinator param `proxy_dc_paused` (at least 2 approvals, D7) and the Vizor remote kill switch (§4.5).
- `proxy_dc_paused`: the chain rejects every new 0x09 immediately at ante step 2 without spending the VAN, including batches that were already committed. Owners who want immediate rejection use this lever.
- The Vizor kill switch alone: Vizor stops committing new allocations, but batches already committed still broadcast and complete (LIV-12).
- Under either lever the round stays active and voting is unaffected. DCs already included are still revealed and counted, and delegates can keep routing.
- Either lever can be lifted to resume. Wallets retry paused batches if the pause lifts before `vote_end_time`, or release them so the weight can be voted directly (§4.4).
- Neither can undo existing delegations: they are final, and the weight has left the delegator's VAN.
- A heavier "discard delegated weight this round" lever is deliberately not included in v1.

**Rollback.**
- No binary rollback after the first proxy tx.
- Incident levers: the mid-round off switch above, `MsgSetDelegateSuspension`, and disabling for new rounds. Under D7 each param lever needs two coordinator approvals, so two coordinators are on call for every proxy round (CRD-1); the Vizor kill switch needs no coordinator quorum.

### 8.4 Stage and beta

- **T1 (internal):** Vizor (software, Keystone, Ledger) plus a current Zodl build and an old Vizor build in one proxy round. Includes a Keystone-signed registration, an in-window DC at about T−10 min alongside a pre-window DC, a delegate registered mid-round who is found by exact search, receives a delegation and routes in the same round, and a `proxy_dc_paused` on/off drill. A scripted check confirms `routing-audit` `routing_verified = true` and that per-option totals match the plaintext model.
- **T2 (delegate beta):** 10-20 real influencers through the stage verifier.
- **T3:** the adversarial list plus the load matrix on the ten-validator lab, including: a DC landing in the transition block is rejected with the VAN unspent; a batch re-proved across the window boundary keeps one layout.
- Then one mainnet proxy round per Q7 and Q12.

### 8.5 Zodl coordination

Send a written notice listing what Zodl will misreport:
- **M1:** per-option totals include delegated weight, and Zodl cannot show which part was delegated.
- **M2:** `VoteSummary` share counts exclude pools.
- **M3:** a seed that delegated in Vizor shows "already used" in Zodl.
- **M4:** old Vizor classifies "delegated" as "voted".
- **M5:** explorers see new event types.
- **M6:** DB v25 cannot be downgraded.
- **M7:** `VoteRound` gains field 31 (`proxy_delegation`) in proxy rounds.

Zodl maintainers then confirm on a current production build that extra round fields and `extensions` are ignored, and sign off before the first proxy round. Zodl builds 3.13.x and earlier throw on rounds they cannot parse, so mainnet test rounds must stay within that parser's limits until those builds age out. Optional Zodl copy: "Totals include votes cast by public delegates."

---

## 9. Test strategy and launch checklist

### 9.1 Tests by layer

- **Circuits:** the §4.1 suite, including the layout-1 tests and the C14 slot tests (slot 7 accepted, slot 8 rejected, slot nullifiers distinct over 0..8); ZKP1-3 `vk_fingerprint_unchanged` (release gate); real-proof size ≤15 KiB; CI benchmarks.
- **Chain:** slot nf duplicates (intra-message, across txs in CheckTx and RecheckTx, genesis type 3); `1 ≤ #DC ≤ 8` (a 9-DC 0x09 is rejected in ValidateBasic); registration ⇔ anchor 0 in both directions; D1, route and registry digest golden vectors (Go = Rust = e2e `sighash.rs`); prefix-free `SVOTE_` domains; ZIP-215 torsion vectors; `bound ≥ NextDelegateIndex` rejected and registry capped at 2^30; p=0 reveals accepted for frozen, suspended and revoked delegates and for an index registered after round creation, and rejected for `d ≥ Next`; proposal 0 never in VoteSummary, TallyResults, 0x0D partials, completeness or `SubmitTally`, and `ProposalTally(0)` rejected; no injected or client message carries a partial decryption of a proposal-0 entry; routing conservation against a plaintext model, byte-identical output under reversed order, the identity-C1 vector, `pools_routed` once; `routing-audit` recompute matches; pools are routed before any partial decryption is accepted (the `revealCloseTime` ordering test); registry rules (no unfreeze, freeze replay after rotation fails, DRK cannot cancel RECOVER, removed verifier voids recovery, recovered entry cannot route older rounds, verifier warm-up, delay floors, per-block dedupe, idempotent route entries); a mid-round `register` gets the next index at once and can route in the running round; `proxy_dc_paused` rejects new 0x09 immediately, with the VAN unspent, while included DCs' reveals and routes still land and count, and clearing it restores acceptance; capability field 13 equals the compiled `2^DC_SLOT_BITS`; dormancy (const before KV reads); two-node determinism across the transition block; genesis round trip with a finished and an active proxy round; droplet routing benchmark; D7: proxy payloads never execute below 2 approvals, even at policy threshold 1.
- **Helper:** leaf-first ordering with 503 on an absent leaf; a layout-1 DC payload (one share, `submit_at = 0`) is scheduled at arrival and revealed; span-data key test; capacity metrics.
- **e2e:** register; create round; 0x09 with and without registration; helper reveals; routes including abstain and a missing route; `routing-audit` `routing_verified = true` and per-option totals match the plaintext model; split-off recovery; R1 and R2 against `0x1D`; a Zodl-style flow in the same round; a layout-1 DC landing at T−60 s is revealed and routed and raises the pool share count by 1; a pool reveal in the transition block is rejected and routing completes; a combined 0x09 with a registration works inside the window; a delegate registered mid-round receives and routes a delegation; a mid-round pause and resume.
- **zcash_voting:** planner proptests (conservation, `D ≤ #DC ≤ D + B − 1`, within-1 quota, at most 8 delegates, `slot_index` and `dc_slot` never above 7, no two DCs to one delegate in a bundle); no API amends a committed allocation; hint, ARK and `dc_seed` vectors; delegate phrases never validate as BIP-39 and restore rejects BIP-39; release-path state machine, including a paused batch; no designated immediate DC share; `VoteEnded` at `now ≥ vote_end` while in-flight work advances, and no new allocation without authenticated timing; layout = 1 iff `is_last_moment()` at batch planning; batch layout persisted before the first proof and reused for DCs and casts across terminal-rejection re-prove, split-off re-plan and recovery, with byte-identical hint and DC; a layout-0 DC dispatched in the window plans 16 shares at `submit_at = 0`; layout-1 delivery plans exactly one payload at `submit_at = 0` with explicit `single_share`; hint and `dc_seed` vectors for layout 1; R1 and R2 rebuild layout-1 DCs; sticky proof set identical across checks, with equal per-key share-nullifier query counts; no per-index REST on delegator paths; a search refreshes the directory before matching; the pre-proving chain cross-check rejects an entry whose DIK or status differs from the directory; gates never stop committed work; v24→v25 migration preserves rows; mobile benchmarks with peak RSS.
- **Vizor:** widget tests for every PD and DG state; the browse list shows only curator-signed vetted entries; unvetted entries appear only through exact search or a deep link, with handle, fingerprint, identicon and "Not reviewed", and never an avatar, display name or statement; "new this round" appears for indices at or above `next_delegate_index_at_creation`; no screen except the delegate's own dashboard shows a delegation count, and none shows a ZEC total; the picker stops at 8 delegates; `tooLate` only after `vote_end` and no 10-minute block; PD-6 waits for the in-window DC reveal and fails at `vote_end`; PD-7 shows 1/1; the paused state and its release offer; PD-8 names only vetted delegates and summarizes the rest as "and N unreviewed delegates"; DG-8 shows the count as "at least N"; the §4.5 copy keys, including "Not browsable"; the optional shared late and skew warnings appear for both flows; `Image.network`/codec ban; route-table audit; kill switch both ways, including an in-flight DC; deletion cleanup; deep-link fragment parsing and mismatch blocking; accessibility.
- **Verifier:** reply, quote, retweet and template-mismatch proofs rejected; eTLD+1 uniqueness; signer refuses mismatched fields; re-verifier pages on a forged REGISTER; purge clears historical objects on every mirror; the registry doc carries current handles and the `accepting` flag for every registered delegate (an unvetted delegate's `accepting:false` reaches it), and the profiles doc and avatar packs carry only vetted delegates; the directory regenerates within about 10 minutes of a chain registry change.
- **Adversarial (stage T3):** ZKP4 after a partial vote; ZKP2 and ZKP4 on one VAN; one VAN twice in a batch; field-wrap inflation; a ZKP4 with `dc_slot = 8`; a 9-DC 0x09; a VC revealed as proposal 0 and a DC as proposal p; proposal-0 entries in 0x0D or `SubmitTally`; 8-DC self-delegation to inflate a reveal count (visible only as an approximate count on that delegate's dashboard); an unvetted lookalike reached by exact search (lookalike warning, no avatar or display text); a DC landing in the transition block (rejected, VAN unspent); a batch re-proved across the window boundary; a 0x09 sent while paused (rejected, VAN unspent); route replay, race, and routes from revoked or rotated keys; registry spam, expired-attestation reuse, double-counted signers; reveals to unregistered indices; targeted helper censorship of one index; non-canonical encodings; tree fill; oversized batch; snapshot equivocation; malicious avatar; deep link carrying an address; lookalike of a vetted handle; X takeover recovery; mixed-anchor front-running; freeze replay; DRK cancel of recovery; directory-key relabelling; retweet binding; subdomain sybils; fresh-signature route floods; Zodl and old Vizor in the same round.

**Load matrix:**

| Scenario | Mix |
|---|---|
| L1 | 5k direct voters × 10 proposals |
| L1b | 2k direct voters × 37 proposals, deadline-heavy |
| L2 | L1 plus 10k delegators × 3, uniform arrivals |
| L3 | L2 with deadline-heavy arrivals, including in-window DCs at 1 reveal each |
| L4 | 50k delegators × 3, deadline-heavy, including in-window DCs |
| L5 | Routing with 20k delegates × 50 proposals on validator hardware |
| L6 | 100 batches × 8 DCs in one block |
| L7 | Directory CDN at 50k wallets |
| L8 | Verifier spike at the daily cap |

Pass criteria:
- every honest share accepted by helpers at least 10 min before `vote_end_time` is revealed in time with at least 30% headroom; for later shares, the reveal-before-close rate and latency are reported by class (vote, pre-window DC, in-window DC), because no fleet can guarantee shares posted in the final seconds;
- Zodl direct-vote loss and latency do not regress against L1/L1b, with in-window DCs present;
- block time p99 is at most 3 s;
- routing EndBlock is within the budget set from the droplet benchmark;
- no app-hash divergence;
- proofs per unique reveal are measured and reported.

### 9.2 Launch checklist

- [ ] contracts-v1 frozen. Golden vectors pass in Go, Rust and e2e. ZIP-PD and ZIP-DR published. Book privacy and threat pages carry the §5 wording, including the validator trust assumption.
- [ ] Both audit tranches closed. voting-circuits 0.13.0, vote-sdk v1.7.0, zcash_voting and the Vizor store release are final. Fingerprints for all four circuits published. ZKP4 rows re-measured on the release circuit.
- [ ] Corpus replay gate passed. Stage T1, T2 and T3 passed, including Zodl and old Vizor, `routing_verified`, per-option totals against the plaintext model, an in-window DC, a mid-round registrant, and a pause/resume drill.
- [ ] Load gate passed. Helper workers set to the measured headroom.
- [ ] Mainnet upgrade applied between rounds; the upgrade runbook keeps that rule for every later halt. Capabilities verified (fields 4-13, with 8 for fields 8 and 13). At least 2 vote managers in the coordinator policy; proxy payloads need 2 approvals. Verifier set (1 register, 2 recover) and params set. Delay floors confirmed. `max_delegates` 20,000.
- [ ] Verifier hardening live: signer re-verification, independent re-verifier, read-only publisher replica. Legal sign-off recorded. Purge SLA tested end to end, including all three mirrors.
- [ ] Every launch vetted entry's fingerprint OOB-confirmed, and the list curator-signed. Reserved-names list seeded. Directory on three mirrors, regenerating within about 10 minutes of chain changes.
- [ ] Static pin and per-round extension signed and CI-verified. Old pins untouched. `supported_versions` unchanged.
- [ ] Zodl written notice sent and sign-off received.
- [ ] Kill switch and `proxy_dc_paused` tested mid-round: the pause rejects new DCs, the kill switch stops new allocations while committed batches complete, included DCs are counted, routes are accepted, and resume works. Two coordinators on call for each proxy round.
- [ ] Dashboards live: pool reveals, routes, helper queue by class, permanent p=0 rejections, `routing_verified`, verifier and re-verifier health, X API errors, directory freshness.
- [ ] Canary delegate and delegator scheduled for the first round, with a pre-window and an in-window DC; the canary asserts `routing_verified` and that its shares and route landed.
- [ ] Public docs published: delegator FAQ (one delegation per round and finality, abstain, last-moment behavior, which is the same as voting, including that a delegation sent in the final minute may not be counted; what is public: routes and approximate delegation counts, while per-delegate totals are hidden; a lone delegator's amount is exposed only by isolation on an option or by validator collusion, as a lone direct voter's is; splitting limits any exposure to a slice; device loss), delegate guide (phrase safety, Tor, public and permanent attribution, the approximate count on the dashboard, vetted versus unvetted discovery), no-receipt-freeness disclosure, support runbook with escalation to coordinators.
- [ ] App-store UGC review passed (report, hide, moderation, contact, terms, vetted-only profiles).
- [ ] On-call owners named for chain, helper, verifier and Vizor.
