# Proxy delegation: final implementation plan

Status: final plan for implementation, revision 8, 2026-10-06. It supersedes integrated-design-v1 and the six component specs wherever they disagree. It folds in seven adversarial review lenses (soundness, privacy, liveness, identity, chain ops, circuit feasibility, completeness) and their skeptic verdicts, the owner's revision-2 feedback and its two skeptic reviews, the owner's revision-3 decisions, an evaluation of label-free pool reveals, an external review of revision 3 with the owner's revision-4 decisions and an exact pool-solvability simulation, the owner's revision-5 decisions (complete delegate ballots, no delegator proofs) with an independent cross-check by a second model (Astra), a revision-6 simplification pass that the owner adopted in full (`archive/review/rev6-simplification.md`), the owner's revision-7 decision to pin the vetted list per round in the dynamic config, and the owner's revision-8 decisions on registration cost, re-registration and profile data.

Evidence labels: [code] read in a repo, [doc] from a spec or doc, [measured] from a prototype run, [inference] reasoned, [new] does not exist yet and is work in this plan (§4 Existing vs new). Repos: **vote-sdk** (chain, helper, FFI), **voting-circuits**, **zcash_voting** (client SDK), **Vizor**, **verifier service** (new repo), **token-holder-voting-config**, **vizor-deeplink-server**, **zips/book**.

Terms: *registration* is today's ZKP1 (notes to a VAN; Vizor shows it as "voting delegation" today and this plan renames that copy to "voting authorization" [new], §4.5). *Proxy delegation* is the new feature. A *DC* (delegation commitment) is the leaf a delegator creates. *Pool[d]* is the El Gamal accumulator `TallyKey(round, 0, d)`. A *ballot* (delegate ballot) is a delegate's one public, signed vote for a round: a counted option for every proposal of the round, submitted once. *Routing* is the chain step at close that adds each pool into the options its delegate's ballot chose. The *delegate key* (DK) is a delegate's one Ed25519 key; it signs registration, revocation and the ballot. The *last-moment window* is the final `min(40% of the round, 6 h)` before `vote_end_time` (`share_policy/timing.rs:19-24, 61-68` [code]). *Layout 0* is the standard 16-share DC; *layout 1* is the last-moment single-share DC. The *vetted list* is the set of delegates shown in the browse list: approved by Valar and Vizor reviewers in the config repo and pinned per round in the signed dynamic config (§4.6). A round's *proxy entry* is its signed entry in that config, carrying the vetted list's hash and the live directory's key (§4.7).

### Revision history

- **Rev 1:** initial plan.
- **Rev 2:** last-moment delegation parity, published per-delegate totals, and coordinator suspension safeguards.
- **Rev 3:** per-delegate totals are hidden again and no pool is ever decrypted on its own, which reverts rev 2's published totals together with their post-round decryption phase and leaderboards (D4); one delegation per round, never changeable (D2); at most 8 delegates per wallet and 8 DCs per registration per round (D8); a mid-round off switch (D9); a vetted-only browse list with exact search for every other delegate (D3); mid-round registration stated (D10); no small-pool notice (D11); a validator trust note (§5); and a decision to keep labeled pool reveals (decision 31). Estimate: about 83-86 eng-weeks [inference].
- **Rev 4, from an external review of rev 3:**
  - Pool privacy is restated. A pool is the same unknown on every proposal its delegate routes, so published per-option totals combine across proposals and can determine pool totals, which makes lone delegators more exposed than direct voters. §5 Pool solvability gives measured results; automatic mitigations are evaluated but not adopted; Q19 asks whether to accept the exposure. D4, D11, decision 31, the normative wording, copy C4, the FAQ and the hole register follow (XR-1).
  - The Vizor kill switch is the `enabled` field of the signed per-round extension, failing closed; there is no unsigned remote flag (D9, XR-2).
  - The recovery hint is AES-SIV, so SND-8 is resolved by construction (XR-3).
  - CI runs the ignored `vk_fingerprint_unchanged` tests for ZKP1-4 as the release gate (XR-4).
  - New work is labeled [new] (§4 Existing vs new, XR-5).
  - Chain-state verification runs in Vizor's existing participation reader, extended with the new keys; zcash_voting supplies key derivations and expected values (XR-6).
  - Routing iterates the smaller of the round's stored delegate votes and its revealed pools (XR-7).
  - Two owner decision records: the automated verifier at launch and 16-share DCs (decisions 32 and 33).
  - Estimate unchanged, about 83-86 eng-weeks [inference].
- **Rev 5, owner decisions after the external review:**
  - Complete ballots (b1; Q19 decided). One atomic, one-shot `MsgDelegateBallot` (tag 0x0C) per delegate per round, with a counted option for every proposal, replaces rev 4's proposal-by-proposal routes. Delegates have no uncounted abstain: a delegate who sits out submits no ballot, and its pool counts nowhere. At close each ballot's pool is added on every proposal, so comparing per-proposal totals reveals nothing (channel B is closed by construction), and results are decrypted once (D2, D7, decisions 34 and 37, §4.2). An independent cross-check by a second model (Astra) agreed b1 is the best available within this architecture; its caveat that the earlier simulation assumed complete ballots is why the ballot is atomic (§5).
  - Public ballots are a side effect, not a promise. Delegators get no cryptographic proofs of ballots or of their own shares: the light-client proofs of delegate votes and DC share nullifiers, the fixed 10-key decoy proof set with its equal-count padding, and the verified per-proposal status model are removed. The pre-proving registry check, whole-list ballot fetches, helper share-status and recovery stay (D1, decisions 18 and 35, §4.4, §4.5).
  - Privacy restated for complete ballots: pool totals are never shown; per-option totals can still determine a pool through options no direct voter picked (channel A); the rev 4 channel-B figures are relabeled as upper bounds (§5, copy C4, the FAQ).
  - New records: one pool per delegate rather than a shared pool (decision 36), the one-final-tally guardrail (decision 37), a plain-language framing in §1, and Astra's hidden-direct-count research path as a possible v1.1 upgrade (Q20).
  - Estimate: about 79-84 eng-weeks [inference], about 2-4 lower than rev 4.
- **Rev 6, simplification pass, adopted in full by the owner.** Three reviewers (chain and circuits; client and Vizor; identity, specs and rollout) checked rev 5 for work that no owner decision needs (`archive/review/rev6-simplification.md`). No owner decision changes, and several high-severity holes close because the code they lived in is gone (§6).
  - **One delegate key** (decision 10). A delegate has one Ed25519 key, and the registry has two ops, `register` and `revoke`, plus coordinator suspension. Removed: the DIK/DRK split, route-key rotation, identity change, verifier-attested recovery, cancel, self-freeze, pending changes and their delays, verifier warm-up and the recover threshold. A lost or leaked key means re-registering under a new index (§4.6 Re-registration and key loss).
  - **No delegator recovery after a reinstall** (decisions 14 and 17). DC secrets derive from the random per-round hotkey, as vote secrets do. Removed: the ARK, the AES-SIV recovery hint, the `0x1D` proxy-action record, its feed and recovery tiers R1 and R2. This matches votes today: Vizor's hotkeys cannot be recreated from the wallet seed.
  - **0x09 carries only DCs** (decision 23). Its optional registration stays, so last-moment parity holds for first-time delegators. Kept weight is voted later with ordinary vote transactions. Removed: mixed DC and cast batches, the 50-action cap, the byte-budget packer and the combined review screen.
  - **Chain trims.**
    - No `delegate_index_bound` (C15 and public input 11; O1).
    - No `routing-audit` query, `0x1C`, `0x1E` or `0x1F`. Routing iterates ballots, which reverts XR-7.
    - No derived indexes or `updated_since_height` paging. The verifier set moves into params.
    - Capabilities shrink to 2 new fields. Registry ops and ballots share tag 0x0B.
    - A ballot is a dense options array, and any second ballot is rejected (decision 26).
    - One dormant const without GasUsed tests. The `revealCloseTime` refactor is deferred to v1.1.
    - The round's proxy flag is set in `MsgCreateVotingSession` rather than a params flag.
  - **Curated profiles and a simpler directory** (decision 39). Vetted delegates send their own picture and statement to curators. Curators publish them as a curator-signed section inside one signed `directory.json`, plus one avatar pack, on two mirrors. Removed: X hydration, the automated avatar pipeline, the profiles doc, `index.json`, the hash chain, equivocation checks and the third mirror.
  - **Identity and verifier trims.**
    - Providers are X and GitHub only (no DNS).
    - The signer re-fetches every proof itself, so there is no separate re-verifier. The refresher looks up handles and re-checks each new registration's proof once.
    - Curation runs from a curator CLI.
    - Reports go to a web form or email. There is no "Stop accepting" flag.
    - Lookalike detection is a simple ASCII check against vetted handles and a reserved list.
  - **Client trims.**
    - A sequential-fill packer replaces the exact search ([measured] +0.011 DCs per delegator, `prototypes/client/compare_packers.out`).
    - Schema v25 is leaner, and there are 2 new job kinds instead of 4.
    - There is no local registry mirror; one proven check of the chosen entries runs at confirm.
    - `min_ballots_per_delegate`, the wakelock and the optional late and clock-skew warnings are dropped.
  - **Owner calls A, B and C.**
    - (A) No deep links in v1.
    - (B) A lean status view. It shows "Handed off", then "Counted" or "Not counted" next to the delegate's ballot. It drops the k/n share counter and the per-option delegate names on results, and the ballot track record waits for v1.1.
    - (C) One off switch, `proxy_dc_paused`, which Vizor reads before every commit. This replaces rev 4's signed per-round config extension (D9, decision 38).
  - **Specs, stage and audit.**
    - One ZIP (ZIP-DR becomes a section of ZIP-PD), and book edits only where pages become false.
    - The internal and delegate-beta stage rounds merge.
    - Audit tranche 2 is about a third smaller.
  - **Estimate:** about 53-61 eng-weeks [inference], down from 79-84. The critical path is about 17-18 weeks, down from 19, because it runs through the circuit, the audit and the stage rounds.
- **Rev 7, owner decision on the vetted list.** The vetted list moves into the dynamic config wallets already fetch (decision 39, Q22, §4.6, §4.7).
  - Each round gets a signed proxy entry next to its round entry, signed by the same admin key. The entry pins the vetted list's hash and names the live directory's signing key.
  - The list is approved by a Valar reviewer and a Vizor reviewer in a config-repo PR, which replaces the 2-of-3 curator keys.
  - Before each round, a curator tool refreshes the vetted delegates' pictures, names and handles from X or GitHub by account id.
  - The list is fixed for the round. Additions wait for the next round, and removals still act at once through directory flags or suspension.
  - Handle renames are handled between rounds (§4.6 Renames).
  - Removed: curator keys, the vetted section's own `seq`, the offline-key certificate and the new static pin.
  - Estimate: about 53-60 eng-weeks [inference].
- **Rev 8 (this revision), owner decisions on registration and profile data** (decisions 10 and 46-48, Q23):
  - **Delegate phrases** are 12 words.
  - **Re-registration.** Registering again from the same X or GitHub account re-keys the delegate's existing entry: same number, new key. There is no support process.
    - The old key stops at once.
    - The new key votes only in rounds created after the change.
    - The verifier allows one registration per account per 14 days, checked before any paid call.
  - **Registration cost.** A free oEmbed check comes first, then one paid post lookup by the signer under a daily spend cap.
  - **Refresh.** The daily handle refresh reads each registration post through oEmbed for free.
  - **Pictures.** Vetted delegates' pictures come from their public X profile pages, fetched once per round by Valar's own tool, an owner-accepted terms risk.
  - **Storage.** Profile content moves out of git to Valar's store, and the per-round list pins only salted hashes.
  - **Removed:** the successor authorization and the support override.
  - **Estimate:** about 54-61 eng-weeks [inference].

---

## 1. Summary

**In plain terms.** A delegation is a normal 16-share vote whose option is "whatever delegate #N votes". Its shares go through the same helpers, and its bucket (the pool) is added at close to whatever #N chose.

1. **What we are building.** In proxy-enabled rounds, a Vizor user can give some or all of their round voting weight to 1 to 8 registered delegates and keep the rest to vote privately. Each delegate votes publicly with one complete ballot per round: a choice on every proposal, submitted once before voting ends. Delegated weight is counted through encrypted per-delegate pools. No pool is ever shown or decrypted on its own; only the per-option totals, which include counted pools, are decrypted, once, at close.
2. **Why.** Many holders do not want to read every proposal. Today their weight simply goes unused. Public delegates give that weight a voice without publishing who the delegators are, how much each gave, or how much each delegate received (published results can still reveal some amounts, item 10). A delegator trusts the delegate's judgment. The delegate's ballot is public because the tally needs it, and Vizor shows it, but it is a side effect of counting, not a promise backed by proofs to the delegator (D1).
3. **Circuits.** One new additive circuit, ZKP4, spends a full-authority VAN. It outputs a DC that commits to a hidden delegate index `d` and an amount `w`, plus a successor VAN holding `W − w`. ZKP1, ZKP2 and ZKP3 VKs stay byte-identical, so Zodl and old Vizor keep working unchanged.
4. **Pools.** Each DC is split into 16 El Gamal shares or, when its batch is planned in the last-moment window, one share holding the whole amount, exactly as votes are. Existing helpers reveal them with the **unchanged ZKP3** as `(proposal 0, decision d)`, and the chain adds them into `Pool[d]`.
5. **Tally.** At the ACTIVE→TALLYING EndBlock, the chain adds each pool, on every proposal, into the option its delegate's ballot chose, plus a deterministic `Enc(0; ρ)` re-randomizer. A delegate with no ballot contributes nothing anywhere: its pool is not counted this round. Threshold decryption of per-option results then proceeds exactly as today, once; there are no running totals and no re-tallies. Pools are never decrypted separately, so per-delegate totals are never published, although the published per-option totals can determine some of them (item 10).
6. **Delegate identity.** A dedicated 12-word delegate key phrase derives one Ed25519 delegate key, which signs registration, revocation and the ballot. The delegate proves control of an X account (or a GitHub account) with a marker post. A Valar verifier issues an attestation. The chain registry stores the key, status and a salted commitment only. No X content goes on chain. Delegates can register at any time, including mid-round. The phrase is 12 words. A delegate who loses or leaks the key registers again from the same X or GitHub account, which re-keys their existing entry; the new key votes from the next round (§4.6 Re-registration and key loss).
7. **Discovery.** The browse list shows only vetted delegates. Valar and Vizor approve each round's list in the config repo, and the round's signed entry in the dynamic config wallets already use pins it, so the list is fixed for the round. Before each round, the vetted delegates' pictures, names and handles are refreshed from X or GitHub. Wallets also download a live signed directory with every registered delegate's current handle. Any other registered delegate is reachable only by exact search on X handle, GitHub handle or key fingerprint, and appears with no profile picture or display text. Search runs locally on the device. Fingerprints and lookalike warnings appear on every delegate surface. There is no leaderboard.
8. **Compatibility and control.** Gating is additive only: no `vote_protocol` or `auth_version` bump. The feature turns on through chain capabilities plus a VK fingerprint match, and a per-round proxy flag that coordinators set when they create the round. Mid-round, a coordinator pause (two approvals, D7) makes the chain reject new delegations. Vizor reads the same pause before every commit, so it stops offering delegation, without touching delegations already made (§8.3).
9. **Delivery.** voting-circuits 0.13.0, vote-sdk v1.7.0 (dormant merges, one coordinated activation), zcash_voting, a new verifier service, and Vizor, with two audit tranches and stage rounds first. The critical path is about 17-18 weeks.
10. **Honest limits.** Pool totals are never shown; tally results show only per-option totals, and your weight follows your delegate's public ballot. Because every counted pool is in every proposal's total, comparing totals across proposals reveals nothing. The remaining exposure: an option that no direct voter picked equals the sum of the pools whose delegates chose it, and several such options can combine (for example as pair sums) to pin down a pool even when it is not alone. If you are that pool's only delegator, your amount is exposed, though never your identity. This mostly affects small rounds and fringe options, and it is the same kind of exposure a direct voter has when alone on an option; in small rounds delegators still have somewhat less amount privacy than direct voters (§5 Pool solvability). [measured] With complete ballots, 16-26% of simulated launch-size rounds (8 to 15 delegates, 12 to 37 proposals) exposed at least one lone delegator's amount, and about 0% of large rounds (100 delegates, 15 proposals, 5,000 direct voters). Per-delegate delegation counts (pool reveal counts) are public live, which is how an observer can guess that a pool is one person, but counts alone reveal no amount. Helpers learn each DC's delegate, as they learn each vote's option; a DC made in the last-moment window is linked to its tx by reveal timing, as a late vote is; reveal timing can narrow small pools to a few transactions. Amount privacy, for delegations and direct votes alike, assumes fewer than t election-key holders collude; validators run the helpers, so a coalition of at least t validators can reconstruct individual amounts. There is no receipt-freeness (§5).

**Answers to the owner's original questions.**

- **(a) Delegate some or all of my weight to up to ~10 others, and keep some or none?** Yes, to up to 8. You choose 1 to 8 delegates with percentages, and either keep a remainder to vote yourself or delegate everything. Amounts are whole ballots (0.125 ZEC), at least 1 ballot per delegate, assigned by Hamilton largest-remainder rounding and shown exactly before you confirm. Four rules apply. You must delegate before your first own vote in the round, because the circuit needs full authority. You delegate once per round. That delegation is final: it can never be changed or added to. And you vote the kept remainder afterwards, as ordinary votes. Your delegated ZEC then counts only if your delegate submits a complete ballot before voting ends; if they don't, it isn't counted this round. You can delegate until voting ends, just as you can vote. In the final hours your delegation is sent to helpers at once, as a late vote is, and as with a late vote, one sent in the final minute may not be counted.
- **(b) ICNS-style X linking with a profile picture to look up influencers?** Yes, without ICNS's flaws. The delegate posts a marker line containing their key; the verifier binds the **numeric** X account id (never the handle) via the official X API, and the delegate's key signs the binding back. Before each round, the vetted delegates' current profile pictures (from their public profile pages) and names (from X's free embed endpoint) are refreshed, reviewed, and pinned by hash in that round's signed config, while the pictures themselves stay on Valar's servers; Vizor decodes the pictures in Rust, and wallets never contact X. A handle change between rounds changes nothing on chain; the next round's list shows the new handle. You browse the vetted list or search exact handles locally, and see a fingerprint plus lookalike, "Not reviewed" and "new this round" labels. GitHub proofs ship as the fallback.
- **(c) If I delegate to an influencer, can I find out whether they voted with it?** Yes. Vizor shows your delegate's public ballot as published on the voting chain (shown, not proven to you), plus whether your own delegation was handed off to the vote servers. After voting ends it shows "Counted" with the ballot, or "Not counted". The ballot covers every proposal and applies to the whole pool, your weight included. If your delegate submits no ballot before voting ends, your delegated weight is not counted this round, and Vizor says so. The pool's total is never shown or decrypted on its own, but public per-option results can sometimes determine it through options no direct voter picked, and if you are its only delegator that reveals your amount, though never who you are; in small rounds this is somewhat more exposure than voting directly (§5 Pool solvability). You cannot see their private vote with their own ZEC, which may differ from their ballot.
- **(d) Can someone delegate at the last moment, just as they can vote at the last moment, with pools tallied at close?** Yes (decided, Q1). The client allows a new delegation while `now < vote_end_time`, the same check casts use (`vote_work/cast_vote.rs:39-52` [code]). In the last-moment window a DC uses the vote path's single-share layout: one share carrying all of `w`, sent at once to `ceil(N/2)` helpers with `submit_at = 0` (`submission_schedule.rs:113-134` [code]). The chain accepts the DC and its reveal while `blockTime < vote_end_time` (`keeper_voting.go:313-341` [code]), and the closing EndBlock adds each pool, with every reveal that landed before close, to the options its delegate's ballot chose; as for any delegation, it counts only if the delegate's ballot is on chain before close. Genuine differences from a late vote: one reveal carries the DC's weight on every proposal, so a missed reveal loses the whole DC; delegation needs a few extra seconds before proving (a directory refresh if the cached copy is older than 10 minutes, and the proven registry check of the chosen delegates); and a kept remainder is voted in a separate transaction after the delegation, so a remainder vote sent in the final minute may miss close. Shared with votes: the reveal must land before close (about 5-10 s at best, minutes under helper backlog [inference]), a tx in the final block is never counted, and an in-window reveal links its tx to its choice (the delegate, or the option).

**Top changes from integrated-design-v1:**
- **Delegate keys and identity:** one delegate key from a 12-word phrase, replaced by registering again from the same account, instead of identity and route keys with rotation and recovery; X and GitHub proofs only; a vetted browse list pinned per round in the signed dynamic config, with pictures and names refreshed from X or GitHub before each round, and exact search for everyone else.
- **Delegator secrets:** DC secrets come from the random per-round hotkey, never from viewing keys, and there is no recovery after a reinstall.
- **Circuit:** no `delegate_index_bound`.
- **Last-moment parity:** delegation stays open until `vote_end_time`, and a batch planned in the last-moment window uses the vote path's single-share layout. No DC share is the designated immediate share. The layout is frozen per batch, and `(d, w, slot, layout)` per `van_nf`, before the first proof.
- **Transactions and contracts:** 0x09 carries DCs only. A normative contract artifact defines `chain_id`-bound digests.
- **Delegation rules:** one final delegation per round, plus a release path for stuck bundles; at most 8 DCs per registration per round.
- **Ballots and tally:** one complete, final ballot per delegate per round, with no uncounted abstain. A pool without a ballot counts nowhere. Routing iterates ballots.
- **Privacy and what delegators see:** no Abstain option by default, as a privacy mitigation; delegate ballots shown to delegators as published, without proofs.
- **Off switch:** one off switch, the chain pause, which Vizor also reads.

---

## 2. Decisions log

### 2.1 Owner decisions (fixed)

| # | Decision | How the plan honors it | Where it cannot be fully honored |
|---|---|---|---|
| D1 (rev 5) | Delegate votes are public, via pools; a public ballot is a side effect of counting, not a promise to delegators | Each delegate's ballot is public, signed by the delegate key and applied to the whole pool. It is on chain because the tally needs it, and Vizor displays it, but delegators get no cryptographic proof of ballots or of their own shares: "if you delegate to someone you are trusting their judgement", and delegates usually post their choices publicly anyway (decision 35). Before proving, Vizor still checks the chosen delegate's registry entry against proven chain state, so a lying server cannot redirect a delegation (§4.4 Registry client) | A delegate's own private vote can differ from its ballot. Vizor shows ballots and hand-off status as the RPC server and helpers report them, so a lying server can misreport them in the app, although the chain counts the real ballot |
| D2 (rev 3; rev 5) | Proxy delegation is final and done once per round; if your delegate does not submit a complete ballot before close, your delegated weight is not counted this round (the risk the delegator accepts) | No override, revocation or amendment message exists. A wallet commits one allocation per round, which then locks. A DC is final once included. A pool counts only through its delegate's complete ballot, on every proposal; a delegate with no ballot contributes nothing anywhere (§4.2 Ballot op, Routing). Vizor states the rule before commit and on the status screen (copy "Counts only with a ballot", §4.5) | Slots whose batch never landed (never broadcast, or definitively rejected) can be released to the remainder. This is failure recovery, not a change (LIV-3). A delegator cannot move weight away from a delegate who misses the ballot deadline |
| D3 (rev 3) | Open registry with proof; Valar verifier; chain registry; vetted-only browse list | X and GitHub providers; attestations counted against a k-of-n verifier set in params; registry 0x1A. The browse list shows only vetted delegates, approved by Valar and Vizor reviewers and pinned per round in the signed dynamic config (owner rev 7); every other registered delegate is reachable by exact search (§4.6 Curation) | Launch is 1-of-1 Valar, so the verifier is a trust and censorship point until a second verifier exists. Valar and Vizor decide who is browsable, and additions take effect from the next round, so an unvetted delegate relies on sharing their handle or fingerprint. Delegates with only a domain cannot register in v1 |
| D4 (restored in rev 3) | Per-delegate totals hidden; pools never individually decrypted | At close each pool is added into the options its delegate's ballot chose, and only per-option totals are decrypted, once (§4.2 Routing, decision 37). No leaderboard and no ZEC totals on profiles or dashboards. Ballots are public, so a track record can be computed from them; Vizor shows one from v1.1 (decision 42). Approximate delegation counts are public on chain but shown only on the delegate's own dashboard, marked approximate, never as a sort key | Complete ballots (D2) put every counted pool in every proposal's total, so comparing per-proposal totals reveals nothing. But an option that no direct voter picked equals the sum of the pools whose delegates chose it, and several such options can combine to determine a pool even when it is not alone (§5 Pool solvability). A determined pool with one delegator exposes that delegator's amount: the same kind of exposure a lone direct voter has, somewhat more likely for delegators in small rounds. A coalition of at least t election-key holders can decrypt any pool (§5 Validator trust). Reveal counts are public live. Reveal timing links a late DC's tx to its delegate |
| D5 (rev 2) | Last-moment delegation parity: delegate until voting closes, as for votes | Delegation stays open while `now < vote_end_time`; a batch planned in the last-moment window uses the single-share layout and immediate delivery; at close, pools are added to their delegates' ballots with every reveal that landed before `vote_end_time` (§4.4, O3) | As for a late vote, the reveal must land before close. One reveal carries a late DC's weight on every proposal. Reveal timing links a late DC's tx to `d`, as it links a late vote's tx to its option |
| D6 (confirmed; one key in rev 6; 12 words in rev 8) | Delegate keys come from a separate delegate key phrase | A non-BIP-39 12-word phrase derives one delegate key; the 12 words are the export and import format (§4.6, O4, decision 10) | A lost or leaked phrase is replaced by registering again from the same X or GitHub account with a new phrase, which re-keys the entry (no support process). The new key votes from the next round, so if this happens before the delegate's ballot, that round's pool is not counted |
| D7 (rev 2; rev 5) | Coordinator suspension with safeguards | `MsgSetDelegateSuspension` is immediate and freeze-only, with a public reason code; every proxy payload needs at least 2 coordinator approvals; 2-of-3 vote managers within the first two proxy rounds (§4.2, Q5). A suspension before the ballot means no ballot, so the pool counts nowhere; a ballot already accepted stands | Coordinators remain fully trusted (they can already push binaries via x/upgrade). A quorum can still stop a delegate's whole pool from counting by suspending it before its ballot lands. Incident levers need two coordinators on call |
| D8 (rev 3) | At most 8 delegates per wallet and 8 DCs per registration per round | C14 range `[0,8)` with a 3-bit check; 0x09 carries at most 8 DCs; `proxy_delegation_version = 1` implies 8 slots; the planner and UI cap 8 delegates (decision 5) | Nothing lost: no bundle ever needs more than 8 DCs, because one DC per delegate per bundle suffices |
| D9 (rev 3; one switch in rev 6) | Mid-round off switch, pause-only | One lever: `proxy_dc_paused`, set with `MsgSetProxyDelegationPause` (at least 2 approvals, D7). The chain then rejects every new DC at once, including batches already committed, without spending the VAN. Vizor reads the pause before preview and again immediately before every commit (§4.4 Gating), so it stops offering and committing new allocations, and shows "Paused" (decision 38). The round stays active; included DCs are still revealed and counted through their delegates' ballots; share delivery and status continue; lifting the pause resumes (§8.3) | It cannot undo existing delegations, which are final. No "discard delegated weight this round" lever in v1. Switching off needs two coordinator approvals, so two coordinators are on call for every proxy round. A lying RPC server can hide the pause from Vizor, but the chain still rejects the batch and the VAN stays unspent |
| D10 (rev 3) | Mid-round registration stays | Delegates can register at any time while the verifier set is non-empty and submit a ballot in rounds already running; the directory is not frozen per round (§4.6 Freshness) | A mid-round registrant has had little scrutiny; Vizor labels it "new this round" and treats it as unvetted until a later round's vetted list includes it |
| D11 (rev 3, kept in rev 4 and rev 5) | No small-pool notice in v1 | No delegator notice about small or lone pools and no delegate-side warning. §5, the FAQ and copy C4 state the exposure, which in small rounds is somewhat greater than a lone direct voter's (§5 Pool solvability). [inference] A commit-time notice could flag only pools that look lone so far; whether a pool is determined depends on every delegate's ballot and on the per-option direct share counts, known only at close | A lone delegator is not warned in the app, although in small rounds its exposure is somewhat greater than a direct voter's. The remaining exposure is accepted for v1 with that disclosure (Q19 decided); Q20 records a possible v1.1 upgrade |

### 2.2 Open items from v1, resolved

| Item | Resolution | Evidence |
|---|---|---|
| O1 `delegate_index_bound` | **Dropped (rev 6).** Rev 1-5 adopted it as ZKP4 public input 11, an in-circuit `1 ≤ d ≤ bound` check, with the chain requiring `bound < NextDelegateIndex`. It only constrains the delegator's own prover. Honest wallets prove the chosen entry exists before proving (§4.4 Registry client), and indices are never reused. The chain still accepts a pool reveal only for `1 ≤ d < NextDelegateIndex` at reveal time (§4.2, 0x04), so a DC to an unregistered index hurts only its sender. An index of 2^32 or more can never be revealed, because `MsgRevealShare.vote_decision` is a u32 (`proto/svote/v1/tx.proto:124` [code]). Dropping it also removes the public bound's leak of the registry height and the "bound = d" pitfall (CF-1) | [measured] The bound check added no rows (2,015/2,048 with and without it, SND-7 review run); removing it changes the ZKP4 VK, so rows and fingerprint are re-measured before rc.1 (§4.1) |
| O2 one tag vs two | **One tag 0x09** with an optional registration and 1 to 8 DCs, no casts (rev 6); `anchor_height == 0` if and only if a registration is present. Registry ops and ballots share tag 0x0B (rev 6) | SND-7 front-running analysis; rev 6 chain review |
| O3 chain DC cutoff | **Decided (rev 2, D5): no consensus deadline and no early client close (vote parity).** The SDK uses the cast path's predicates on the same `RoundHostContext`: no new allocation or proxy batch once `now ≥ vote_end_time` (`VoteEnded`); in-flight work advances and the chain rejects late txs without spending the VAN; new allocations require authenticated round timing; layout = single share if and only if `is_last_moment()` at batch planning, persisted once per 0x09 batch for its DCs before the first proof, with `(d, w, slot, layout)` per `van_nf`. Coordinator `proxy_dc_paused` brake (D9). | PRV-4, LIV-4, CMP-11, SND-8; `cast_vote.rs:35-53`, `vote.rs:4447-4452`, `submission_schedule.rs:113-134` (zcash_voting), `keeper_voting.go:313-341`, `module.go:491-497` (vote-sdk) [code]; single-share ZKP4 MockProver positives and ZKP3 reveal (`prototypes/circuits/single_share_test.log`) [measured] |
| O4 delegate keys | **Dedicated delegate key phrase**, in a non-BIP-39 format; hardware-account users can be delegates. Owner confirmed (D6). | CMP-5, IDN-14 |
| O5 Abstain (rev 5) | **No uncounted abstain for delegates, and no Abstain option by default.** A ballot assigns a counted option to every proposal (§4.2 Ballot op); a delegate who wants to sit out submits no ballot and sits out the whole round. The uncounted abstain sentinel of earlier revisions is removed, because a pool that counts on only some proposals reopens the cross-proposal channel (§5 Pool solvability). A round may include a real, counted "Abstain" option, which is an ordinary option for direct voters and delegates alike; it is off by default because an option few direct voters pick is a channel-A risk (Q2, default no). | [measured] Under rev 4's proposal-by-proposal routes, with turnout equations ignored, an Abstain option made 42-44% of pools exactly solvable (15 proposals, 30 direct voters, 5 delegates), against 0% with the uncounted sentinel. The checked-in `prototypes/privacy/pool_inference.out` shows 81-87% for the same case because it maps pools without a choice on a proposal to Abstain; the skeptic's corrected figure is 42-44%. With turnout equations the sentinel was not exposure-free: with direct voters voting on every proposal, 3.3% of pools and 43% of rounds had a solvable lone pool at 500 direct voters, 30 delegates and 10 Yes/No proposals; with complete ballots the same scenario had none [measured, `prototypes/privacy/pool_solvability/sim.py`; §5 Pool solvability] |

### 2.3 Design decisions

| # | Decision | Rationale | Rejected |
|---|---|---|---|
| 1 | B2: DC shares through helpers | Hides delegate choice from the public and the delegate (helpers, which run on validators, still learn it), and hides per-DC amounts; only per-option totals are ever decrypted | B1 in-tx slots: EA sees exact amounts; delegate set linked to tx |
| 2 | ZKP4 as a separate circuit at K=11 | No existing VK changes | A ZKP2 mode: new VK for all voters |
| 3 | DC reuses `DOMAIN_VC` with constant proposal 0 | ZKP3 unchanged; ZKP2's `p ≠ 0` gate keeps it sound | New `DOMAIN_DC` and reveal VK |
| 4 | Input and successor VAN authority MAX | No double counting of cast weight | Per-proposal pools |
| 5 | Slot nullifier: ≤8 DCs per registration per round, via a 3-bit short range check | Bounds helper load and spam at 8 × 16 = 128 pool reveals per registration. The 3-bit check costs no more than the old 4-bit one: rows (2,015), columns, proof size (11,008 B) and prove time are unchanged [measured]. No single bundle ever needs more than 8 DCs, because a wallet has at most 8 delegates and one DC per delegate per bundle suffices | A 4-bit check (16 slots); minimum size only (count grows with W) |
| 6 | p=0 reveals accepted for any index that exists at reveal time | Reveals never depend on delegate status, and mid-round registrants receive pool shares at once; whether a pool counts depends only on its delegate's ballot (D2) | Status check at reveal |
| 7 | EndBlock routing with ρ re-randomization | Pools and ballots final; blocks identity-C1 round kill on the combined buckets; per-option results never need a pool decrypted | Lazy routing in three code paths |
| 8 | Pool counts at `ShareCountKey(round,0,d)`; distinct event | Free genesis export; VoteSummary never sees p=0 | Separate key (OPS-2) |
| 9 | Digests: BLAKE2b, ASCII domain, `lp8(chain_id)`, 32-byte fields | One convention; no cross-chain replay | `net` string, LE fields |
| 10 (rev 6; re-key in owner rev 8) | One delegate key from a delegate phrase; registry ops are `register` and `revoke`, plus coordinator suspension; registering again from the same account re-keys the entry | A delegate signs once per round, so a hot/cold key split buys little, and both keys came from one phrase on one device anyway (IDN-7). With no pending changes, the proven stored entry is the effective entry. The account is the master identity, with no support process (owner rev 8): a re-key keeps the index, the old key stops at once, and the new key can ballot only in rounds created after the re-key, so formed pools cannot be captured (IDN-1); the verifier allows one registration per account per 14 days (§4.6); hardware users included | DIK and DRK with route-key rotation, identity change, verifier-attested recovery with a 7 d delay, cancel and self-freeze (rev 1-5); seed derivation |
| 11 | Superseded in rev 6 by decision 10 (rev 1-5: no unfreeze; DIK rotation exits FROZEN) | No freeze op exists | n/a |
| 12 | Rev 1-5's rule that recovered keys cannot submit ballots in older rounds returns in rev 8 as the re-key rule of decision 10; the DIK-only cancel stays superseded | A re-key has no delay to cancel | n/a |
| 13 | Superseded in rev 6 by decision 10 (rev 1-5: delay floors; verifier warm-up) | No delayed key change exists; every proxy payload still needs at least 2 approvals (D7) | n/a |
| 14 (rev 6) | DC secrets from a per-round key derived from the hotkey, for every account; never from viewing keys | Same as vote secrets: Vizor's hotkey is random per account and round and app-owned, so UFVK holders cannot read delegations, and no seed material crosses the wallet boundary | OVK-keyed ARK; spending-key ARK for seed-restore recovery (rev 1-5) |
| 15 | No designated immediate DC share | No public tx-to-delegate link for early DCs; inside the last-moment window every share, vote or DC, is immediate anyway | DC share 0 immediate in every window |
| 16 | One locked allocation per round, never changed | Simple schema and recovery; matches D2 | Amending or adding to a committed allocation |
| 17 (rev 6) | No delegator recovery beyond the local database | Parity with votes: Vizor's hotkeys are random per account and round and "v2 does not try to recover deterministic hotkeys from the wallet seed" (Vizor `rust/src/wallet/voting/README.md:42-48` [code]), and re-importing a wallet mints a new account id, so a hotkey-keyed recovery could never run. Ambiguous submissions are still resolved from the local database by exact commitment-tree recovery (§4.4 Recovery) | An on-chain `ProxyActionRecord` per DC with an AES-SIV recovery hint, and recovery tiers R1 (hotkey kept) and R2 (seed restore) (rev 1-5); an event or tx-index feed |
| 18 | Whole-list sync of the directory and ballots; no delegator proofs (rev 5) | No per-index interest leak for directory or ballot reads; with no per-delegate proofs there is no decoy set to keep consistent (decision 35). The one per-key request left is the pre-proving registry check (§4.4 Registry client) | Per-index REST; per-delegate proofs with a fixed decoy set (rev 4) |
| 19 | Browse list = vetted entries only, pinned per round; everyone else by exact search, without avatar or display text | Directory-key theft can't mint vetted entries, because the list is pinned by the round's signed entry; an impersonator needs an exact query and still meets lookalike warnings; moderation scales with the vetted list only | Plain flag; an open browse listing gated by account age and follower thresholds |
| 20 | X proof: original post, exact template | No binding via replies or retweets | Any post with the line |
| 21 | Signer recomputes the digest, re-checks every post through oEmbed, makes the one paid lookup with its own credentials, and enforces budgets and the daily spend cap | API compromise isn't an oracle; the refresher's daily oEmbed read covers every registration post, so a separate re-verifier adds little (rev 6, rev 8, §4.6) | Opaque-digest signer; an independent re-verifier service (rev 1-5) |
| 22 | Proxy PRs `V:state/breaking`, v1.7.0 only | v1.7.0 is a halt between rounds and is never backported, so no two binaries run the same heights | Rolling dormant releases |
| 23 (rev 6) | 0x09 carries an optional registration and 1 to 8 DCs, no casts | ZKP1 plus 8 DCs is about 100-150 KB, so no byte budget or packer is needed; the handler is a near copy of the 0x06/0x07 batch code. Keeping the registration in 0x09 keeps last-moment parity for first-time delegators. Kept weight is voted later with ordinary 0x06 casts | Mixed DC and cast batches with a 50-action cap and an advertised byte budget (rev 1-5); taking the registration out too (an extra block for first-time last-moment delegators) |
| 24 | No per-block proof-verification cap in v1 | 0x06/0x07 already set this load class; a cap under 51 strands Zodl batches | A proof-action cap (OPS-10) |
| 25 | Per-block dedupe: one ballot tx per (round, d) per block, in PrepareProposal | Stops duplicate-signature ballot floods. Registry ops need either a fresh attested key (`register`, at most once per account per 14 days) or the entry's own key (`revoke`), so they need no dedupe (rev 6, rev 8) | A global cap only (IDN-9); a registry-op dedupe and cap (rev 1-5) |
| 26 (rev 6) | Any second ballot for a `(round, d)` is rejected with `ErrDelegateBallotExists`; the client treats an identical stored ballot as success | Safe retries near the deadline without allowing amendment (D2), and no no-op success that anyone holding a copy of the signed ballot could keep landing | Identical resubmission as a no-op success (rev 1-5); amendable ballots (LIV-11) |
| 27 | ZIP-215 Ed25519 everywhere; torsion-component keys rejected | Chain, verifiers and auditors agree | Mixed stdlib, dalek and CometBFT rules (IDN-15) |
| 28 | Superseded in rev 6 by decision 41 (rev 1-5: deep-link payload only in the URL fragment, fingerprint mandatory) | No deep links in v1 | n/a |
| 29 | Pool reveals share the 256/block cap and helper FIFO with votes | No new scheduling class | A priority class |
| 30 | Last-moment parity: single-share DCs in the window, open until `vote_end_time`, layout frozen per batch | Delegating behaves like voting; 1 reveal per late DC; one clock and one predicate for DCs and casts | Early close at `vote_end − max(buffer, 30 min)`; chain `cutoff_time`; a DC-only chain-time clock (DCs and the remainder vote that follows would run on different clocks) |
| 31 | Pool reveals carry a public delegate label `(0, d)` | The label-free alternative works: [measured] a separate-VK ZKP3R prototype is K=11, 1,016 rows, 8,352 B proof, and about 2.6× ZKP3's prove time (90 ms vs 35 ms at 4 threads) (`prototypes/labelfree/zkp3r_rows.log`, `zkp3r_timing.log`). But it needs 16 reveals per delegation for every proposal its delegate votes on (every proposal, with complete ballots), which is 1.8-17× total reveals, 6-12× today's helper fleet at 20k delegators × 3 delegates × 10 proposals, and infeasible at any fleet size with 37 proposals because the 256/block cap binds [inference from measured queue models: `prototypes/labelfree/seeds_deadline.out`, `counts.out`, `load.out`]. It needs a delegate ballot deadline before close, which breaks last-moment parity (D5), and late ballots' reveals crowd out direct last-moment votes. It buys little privacy: validators that run helpers can already group shares (§5 Validator trust); per-delegate counts remain solvable from per-proposal cast counts when delegates are few relative to proposals [measured, `prototypes/labelfree/unified_leak_lone.out`]; and a delegate's delegated weight still enters every proposal with the same value, so the zero-direct-option pool solvability of §5 applies to it unchanged [inference]. Revisit only if the helper fleet grows substantially | Label-free choice-proving reveals: each share is revealed once per proposal with a proof that its hidden delegate's ballot chose `(p, o)`, and lands directly in that bucket |
| 32 | Automated verifier at launch (owner rev 4) | Keep the automated X and GitHub verifier (DNS dropped in rev 6), with attestations counted against the params verifier set (§4.6). It registers delegates without manual latency, at any time including mid-round (D10), and tracks handle changes automatically, because it binds the numeric account id and the refresher updates the current handle. Valar and Vizor still approve the vetted browse list (decision 19) | Curator-signed manual registration: saves about 6-8 eng-weeks of verifier work [inference], but adds manual latency to every registration and has no automatic handle tracking |
| 33 | 16-share DCs, not single-share (owner rev 4) | Outside the last-moment window each DC is split into 16 shares, as a vote is. A coalition of at least t key holders that decrypts shares then learns only fragments, and must also pool helper intake to group a DC's shares and learn its amount (§5 Validator trust); decrypting a pool with one delegator still gives that delegator's amount. DCs stay at parity with votes. The protection assumes key holders do not pool helper intake | A single share per DC: 16× less helper load, but a key-holding coalition learns each DC's exact amount from one decryption, and DCs would no longer match votes |
| 34 | Complete delegate ballots (b1; owner rev 5, Q19) | One atomic ballot op (tag 0x0B since rev 6) per (round, d), with exactly one counted option for every proposal, one-shot and final (§4.2 Ballot op). At close each ballot's pool is added on every proposal; a pool without a ballot counts nowhere (D2). Every counted pool is then in every proposal's total, which closes channel B by construction (§5 Pool solvability). [measured] Launch-size rounds exposing a lone delegator fell from 98-100% (an upper bound for proposal-by-proposal routes) to 16-26%. An independent cross-check by a second model (Astra) agreed b1 is the best available within this architecture. Astra's caveat: the simulation that measured b1 assumed delegates complete their ballots; applied as a filter to partial per-proposal behavior, b1 would have dropped most pools (about 12-35% retained), which is why the ballot is atomic and Vizor requires every answer before Submit | Proposal-by-proposal routes with an uncounted abstain (rev 4); b1 as a filter over proposal-by-proposal routes; an amendable ballot |
| 35 | Public ballots are a side effect, not a promise (owner rev 5) | Ballots are on chain because the tally needs them, and Vizor displays them, but delegators get no cryptographic proofs of ballots or of their own shares: a delegator trusts the delegate's judgment, and delegates usually post their choices publicly anyway (D1). Kept: the proven pre-proving registry check (safety: a lying server cannot redirect a delegation), whole-list ballot fetches (privacy), and helper share-status for "handed off" (liveness; rev 6 drops the "revealed k/n" counter, decision 42, and reinstall recovery (R1/R2), decision 17). Saves about 2-4 eng-weeks across zcash_voting and Vizor [inference] | Light-client (IAVL/ICS23) proofs of ballots and DC share nullifiers for delegators, with a fixed 10-key decoy proof set, equal-count padding and a verified per-proposal status model (rev 4) |
| 36 | One pool per delegate, not a single shared pool | A shared pool cannot be split at close between delegates who vote differently, and since delegates may vote until close the chain must keep each delegate's weight separate. Separate pools are not what causes the equation leak (the voting rule is) | One shared delegation pool |
| 37 | One final tally (guardrail) | Results are decrypted and published once, at close (§4.2 Routing). No running or intermediate decrypted totals, no re-tallies, and no rule that makes a pool count on only some proposals: each would add public equations in the pool totals (§5 Pool solvability) | Running delegated totals during the round; re-tallies; partial-counting rules |
| 38 (owner rev 6, C) | One off switch: `proxy_dc_paused`, which Vizor reads before every commit | The chain pause already stops every new DC mid-round with the round still active. Vizor reading it gives the same in-app effect as a separate config switch, without a second signed object, its `seq` rule, CI signing, an audit item and a per-round signing step. A lying RPC server can only hide the pause, and then the chain rejects the batch with the VAN unspent | A signed per-round config extension whose `enabled` field was Vizor's kill switch (rev 4-5, XR-2); an unsigned remote flag |
| 39 (rev 6; owner rev 7) | The vetted list is pinned per round in the signed dynamic config; its pictures, names and handles are refreshed from X or GitHub before each round; a live signed directory serves exact search | Wallets already fetch and verify the dynamic config, whose round entries are signed by the admin key pinned through the wallet's static config. A separately signed per-round proxy entry that carries the vetted list's hash and the live directory's key needs no new keys or pins, and fixes the list for the round. Approval is a config-repo PR that a Valar reviewer and a Vizor reviewer must approve, instead of curator keys (owner rev 7). The curator tool pulls pictures and names by account id before each round, so profiles stay current between rounds without an automated server-side image pipeline. The live directory lists handles for exact search and can hide entries, never add them (§4.6, §4.7) | X hydration and an automated picture pipeline (rev 1-5); a curator-signed (2-of-3) vetted section inside the directory with delegate-supplied pictures, a new static pin and an offline-key certificate (rev 6); `index.json`, a profiles doc and sharded packs on three mirrors with a hash chain and equivocation checks (rev 1-5) |
| 40 (rev 6) | Routing iterates the round's ballots; no routing-audit query and no derived routing state | The scan is bounded by `max_delegates` either way, and the elliptic-curve work is the same. Auditors can recompute routing from archival state at the height before the transition. This removes the `0x1C` bases, the `0x1F` counters written on the reveal and ballot paths, a forced-branch test and a genesis rebuild | Iterating the smaller of ballots and revealed pools via `0x1F` counts (XR-7, rev 4-5); a `routing-audit` query over `0x1C` bases (rev 1-5) |
| 41 (owner rev 6, A) | No deep links in v1 | Exact search on a handle or fingerprint covers discovery; delegates share their handle or fingerprint. It removes a new deeplink-server route, its privacy review, and Vizor's link parsing and mismatch blocking | Fragment-only deep links `link.vizor.cash/d#v1.<index>.<fp>` (rev 1-5) |
| 42 (owner rev 6, B) | Lean delegator status | Status shows "Handed off", then "Counted" with the delegate's ballot, or "Not counted". There is no k/n reveal counter, "on track" state or per-option delegate names on results, and the ballot track record waits for v1.1 (it would be empty in the first proxy round) | Reveal progress k/n, per-option delegate annotations on results and a "voted in N of M rounds" profile section (rev 3-5) |
| 43 (rev 6) | X and GitHub proofs only; a simple ASCII lookalike check against vetted handles and a reserved list | Handles are ASCII, and unvetted delegates show no display name, so a fold-and-edit-distance check covers what matters. Organizations can register through a representative's X or GitHub account | DNS and `.well-known` proofs with eTLD+1 rules; UTS #39 skeletons with first-seen precedence and 12-month skeleton reservations for every handle, and perceptual hashing (rev 1-5) |
| 44 (rev 6) | Allocation packing by sequential fill | Same bound `D ≤ #DC ≤ D + B − 1`; [measured] 2.845 DCs per delegator against 2.834 for the exact packer, worse in 1.2% of cases (`prototypes/client/compare_packers.out`) | Exact split-free search, best-fit decreasing and greedy splitting (rev 1-5); per-bundle Hamilton, which made 44% more DCs |
| 45 (rev 6) | The round's proxy flag is set in `MsgCreateVotingSession` | Each round's enablement is explicit at creation, so no runbook rule about changing a params flag while a create-session action is pending. A create-session action that sets the flag needs at least 2 approvals (D7) | `enable_for_new_rounds` in params, snapshotted at round creation (rev 1-5) |
| 46 (owner rev 8) | Registration pays for one call only | Junk attempts cost nothing, spend is capped, and an account cannot make us pay repeatedly. **Free check:** oEmbed, X's published embed endpoint, resolves posts by id, so it gives the post text, the current handle and display name, and a 404 for deleted posts. **Paid call:** one post lookup with its author (about $0.015), made by the signer under a daily spend cap; it gives the numeric id and the account's age, which oEmbed cannot. **Cooldown:** one registration per account per 14 days, checked before the paid call. **Refresh:** the daily refresh reads each registration post through oEmbed for free | Paid user lookups for the daily refresh (about $63 a month at 200 delegates, $3,050 at 10,000); a ZEC registration fee, which needs a payment-watching wallet, confirmation waits and ZEC for every delegate; scraping for registration |
| 47 (owner rev 8) | Vetted delegates' X pictures come from their public profile pages, fetched by Valar's own tool once per round; the official API is used only for registration | Pictures are needed once per round for a small vetted set. The owner accepts the terms-of-service risk (X's terms prohibit scraping without written consent; R8-2) with guardrails: vetted delegates only, once per round, a low rate, an identicon on failure and the paid user lookup as fallback. X's verified badge is not shown, because the blue check is a paid subscription | Paid user lookups for pictures (about $0.01 per vetted delegate per round); third-party scrapers such as unavatar.io; delegate-supplied pictures (rev 6) |
| 48 (rev 8) | Profile content stays out of git: the per-round list pins salted hashes, and pictures, names, statements and handles live on Valar's store | X's developer terms require stored X content to be kept current and deleted within 24 h, which git history cannot do (identity research), and delegates can ask for removal. Salted hashes reveal nothing once the content and its salt are deleted, the same idea as the salted on-chain subject commitment | Pictures and names committed to the config repo (rev 7) |

---

## 3. Architecture

### 3.1 Actors

- **Delegator wallet:** Vizor plus the zcash_voting SDK; plans, proves, submits, delivers shares and shows status. Before proving, Vizor's participation reader checks the chosen delegates against proven chain state (§4.4 Chain verification); ballots and hand-off status are shown as reported, without proofs (D1). Software, Keystone and Ledger accounts; devices sign only ZKP1.
- **Delegate wallet:** Vizor delegate mode plus the `zcash_vote_delegate` crate; holds the 12-word phrase and the delegate key; onboards; signs its one ballot per round, its registrations and, if it retires, its revocation.
- **Vote chain:** vote-sdk validators; verify proofs, hold registry, ballots and pools, add each pool to its delegate's ballot at the tally transition, and hold EA key shares. Pools are never decrypted on their own.
- **Helpers:** the svoted helper, which runs in-process on each validator node (`docs/runbooks/join-chain.md:173` [doc]); receive DC shares (learning `d`, the DC leaf and the client IP unless Tor is on), prove ZKP3, reveal at randomized times.
- **Verifier service:** Valar-run verifier-api (free oEmbed checks and rate limits), attest-signer (KMS/HSM, on its own host; makes the one paid X call per registration under a daily spend cap), publisher, refresher (daily oEmbed reads of registration posts) and chain-indexer.
- **Curators:** Valar and Vizor reviewers. They vet delegates (out-of-band fingerprint confirmation, lookalikes, statements), run the curator tool before each round, and approve the round's vetted list in a config repo PR. They hold no keys.
- **Directory mirrors:** the valargroup origin and the `functions.vizor.cash` pull-through for the live directory, both untrusted for integrity.
- **Config repo:** the dynamic config gains a per-round proxy entry, signed by the existing admin key, that pins the vetted list and names the live directory's signing key. The list and its picture pack sit beside the config under their hashes. There is no new static pin and no on/off flag.
- **Coordinators:** vote managers who set params and the verifier set, flag proxy rounds at creation, may suspend a delegate, and may pause new delegations mid-round. Every proxy payload needs at least 2 approvals (D7).

### 3.2 Sequence

```
Delegator (Vizor+SDK)      Directory    Vote chain                  Helpers        Delegate (Vizor)
  |                            |            |  (any time, mid-round too) phrase -> DK; marker post; verifier attests;
  |                            |            |<--------- 0x0B register {DK, subject_commit, attestation}
  |                            |            |  index d assigned at once (append-only, from 1)
  |--GET signed directory.json>|            |                            |              |
  |--check round flag and pause; pre-prove: proven entries of chosen d-->|              |
  | plan: Hamilton -> sequential fill -> slots; dc_seed(hotkey)           |              |
  | prove: [ZKP1] -> ZKP4 x k (k <= 8) (phased)                           |              |
  |--0x09 {reg?, DC...} signed over D1--------------->|                  |              |
  |                            |            | verify; set nfs 0x00/0x01/0x03;            |
  |                            |            | append final VAN, then DCs                 |
  |<--event: leaf indices------------------|                            |              |
  |--16 shares/DC, or 1 if planned in the last-moment window ------------>|              |
  |  (p=0, decision=d; submit_at scheduled, or 0 in the window)          |              |
  |  "Handed off" = definite acceptance of every share (in-window: reveal) |              |
  |  (kept remainder: ordinary 0x06 votes later, from the remainder VAN)  |              |
  |                            |            |<--0x04 reveal(0,d)+ZKP3 (random, or at once)|
  |                            |            | Pool[d] += share                          |
  |                            |            |<-------- 0x0B ballot {option per p}, once, signed by DK
  |--poll whole ballot list; helper hand-off status (shown, not proven)-->|             |
  |                         ... vote_end_time ...                        |              |
  |                            |            | EndBlock ACTIVE->TALLYING:                 |
  |                            |            |  for each ballot(d), every p:              |
  |                            |            |   agg[p][option(d,p)] += Pool[d]; += Enc(0;rho)
  |                            |            |  no ballot: Pool[d] counts nowhere         |
  |                            |            |<-- partial decryptions (DLEQ), MsgSubmitTally as today -> FINALIZED
  |                            |            |  results decrypted once                    |
```

### 3.3 Data and trust boundaries

- **Chain-authoritative:** index, delegate key, status, suspension, ballots, pools (as ciphertexts), pool reveal counts, nullifiers, `NextDelegateIndex`, params (including the pause and the verifier set) and each round's proxy flag. Vizor proves only the registry facts a delegation relies on (§4.4 Chain verification); it shows ballots and hand-off status as the RPC server and helpers report them (D1).
- **Display-only:** the live directory (every delegate's handle) and the round's vetted list (vetted delegates' names, pictures and statements). A directory lie cannot redirect a delegation to a different index or key, because Vizor proves the chosen entries before proving. Vetted status and vetted profiles are protected by the per-round pin in the signed dynamic config; handles by the directory signature, `subject_commit` and wallet pinning (§4.6).
- **Third-party consumers:** the directory's registry entries (IDs plus current handles) may be consumed by other wallets once counsel clears handle redistribution (Q10). Vetted profiles include X or GitHub pictures and names, so the same counsel decision covers them.

---

## 4. Component designs

**Existing vs new.** Existing code the plan builds on is cited with [code]. Everything this plan introduces is new work, including the new tags, keys, circuit, crate and services. These items are marked [new] because they are easy to mistake for existing hooks:
- voting-circuits `vk_fingerprints()` (only the per-circuit `vk_fingerprint_unchanged` tests exist [code]);
- the "voting authorization" copy (Vizor shows ZKP1 as "voting delegation" today, `lib/src/features/voting/screens/voting_status_screen.dart:1262-1263, 1329` [code]);
- `ProtocolCapabilities` fields 4 and 5 (fields 1-3 exist, `proto/svote/v1/query.proto:73-77` [code]);
- the proxy flag in `MsgCreateVotingSession` and `VoteRound` field 31;
- the dynamic-config `extensions` object and `extensions.proxy_delegation_v1` (today's `prod/dynamic-voting-config.json` in token-holder-voting-config has no `extensions` key [code]);
- Vizor network-role entries for the directory mirror and verifier hosts. The network-role table itself is an existing convention in Vizor's voting README (`rust/src/wallet/voting/README.md:322-337` [code]), which requires new network roles to be added to that table and to the service tests, so the new hosts must be added there.

### 4.1 Circuits (voting-circuits 0.13.0)

**ZKP4 conditions.** The rev-3 prototype (`prototypes/circuits/zkp4-prototype.patch`), which still contains the C15 bound that rev 6 removes, measures at K=11 with the 3-bit slot check: 2,015/2,048 rows, 34 advice and 78 fixed columns, 4 lookups, identical to the 4-bit version [measured]. The prototype before C15 was added also used 2,015 rows (SND-7 review run [measured]), so v1 is expected at the same row count; rows, columns and the VK fingerprint are re-measured before rc.1.

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
C7 plus C9 plus C12, together with ZKP1's `W ≤ 2^30`, give `1 ≤ w ≤ W` over the integers.

**No delegate index bound (rev 6, O1).** Rev 1-5 had a C15, `1 ≤ d ≤ bound` against a public `delegate_index_bound`. It is removed, together with its public input, its range checks and the builder's `next_delegate_index` parameter. The delegate index `d` is a private u32 that the circuit only hashes into the DC (C13). The chain's reveal-time check `1 ≤ d < NextDelegateIndex` (§4.2, 0x04) is what keeps pool shares on registered indices. Removing C15 deletes the prototype's bound gate and its tests (`zkp4-prototype.patch`) before rc.1.

**Public inputs (11, in this order).** `van_nf, r_vpk_x, r_vpk_y, van_new, dc, root, anchor_height, round, ea_pk_x, ea_pk_y, dc_slot_nf`. The voting-circuits `Instance` constructor is the only source of this order. The chain FFI must build instances through it, and a golden vector pins the packing.

**Constants and registrations.** All of these are frozen with pinned-value tests before rc.1, because changing them later breaks either a VK or the byte-identical re-prove that exact-tree recovery of an ambiguous submission relies on (§4.4 Recovery):
- `POOL_PROPOSAL_ID = 0`;
- `DC_SLOT_BITS = 3` and the slot tag text with offsets 0..7;
- PRF domains 0x10-0x14 keyed by `dc_seed`: 0x10 El Gamal, 0x11 blind, 0x12 shuffle, 0x13 remainder (layout 0); 0x14 El Gamal for layout 1, the last-moment single share, mirroring `VOTE_PRF_DOMAIN_ELGAMAL_SINGLE_SHARE = 0x04` (`vote_proof/builder.rs:538-550`, `domain_tags.rs:42-43` [code]);
- `DOMAIN_VC` documented as live for proposal-0 pool commitments.

**Invariant (normative in ZIP-PD).** `rand` is constant along every transition that keeps MAX authority. Any future VAN-transforming circuit must either preserve `rand` or carry a slot counter; otherwise C14's cap silently resets (CF-3). A cross-circuit test pins it.

**Measured costs** ([measured], loaded M3 Ultra): with the 3-bit slot check, ZKP4 proves in 519, 272, 158 and 95 ms at 1, 2, 4 and 8 threads, and 66 ms with all 28 threads (530, 278, 161 and 96 ms with the 4-bit check, within noise, and within 2% of ZKP2), verifies in about 2 ms (1.6-2.0 ms at 8 or more threads), and is 11,008 B (cap 15,360 B). Keygen peak RSS is about 140 MiB; ZKP4, ZKP2 and ZKP3 resident use 259 MiB, plus about 182 MiB for ZKP1. At 4 threads, ZKP1 + 10 ZKP4 took 1.9 s, so the largest 0x09 (ZKP1 + 8 ZKP4) takes about 1.6 s [inference from measured]; kept-weight casts are proved later, in the ordinary vote flow. [inference] Mid-range Android at 2.5-4x slower still meets Vizor's ≤2 s per proof target.

**ZKP3 reuse.** ZKP3 is unchanged. Real proofs [measured] for `(0, d)` at a non-zero tree position verify for `d = 4242` and `d = u32::MAX`. They are rejected for `d + 1` and for proposal 1.

**VK release gate.** The ZKP1, ZKP2 and ZKP3 `vk_fingerprint_unchanged` tests are `#[ignore]`d because they run keygen (`src/delegation/prove.rs:271`, `src/vote_proof/prove.rs:282`, `src/share_reveal/prove.rs:326` [code]), so a plain `cargo test` skips them. ZKP4 gets a test with the same name and attribute, pinning its fingerprint. The release gate is a CI step that runs `cargo test --release -- --ignored vk_fingerprint_unchanged` for ZKP1-4 and must pass on the release commit before 0.13.0 is tagged. voting-circuits CI already runs this filter with `--ignored` on pull requests (`.github/workflows/test.yml:91`) and every ignored test on pushes to main (line 192) [code]; the gate adds ZKP4 to that run and makes it a required check for the release tag.

**ZKP2 gate.** ZKP2's `proposal_id ≠ 0` inverse gate (`authority_decrement.rs:398-416` [code]) is now **load-bearing**: without it, ZKP2 could emit `VC(0, d)` while keeping full weight. Re-document it, make `proposal_id_zero_fails` a non-ignored proxy soundness test, and require it on every ZKP2 rewrite.

**Builders and exports.** All APIs are ballot-denominated:
- `build_proxy_delegation_proof(..., delegate_index: u32, ballots, dc_slot, dc_seed, layout: ShareLayout)`. The builder rejects `dc_slot ≥ 8` and `delegate_index = 0`. The wallet passes only an index whose entry Vizor's participation reader verified just before proving (§4.4 Chain verification).
- `derive_proxy_delegation_transition` (native, proof-free).
- `derive_proxy_share_secrets(dc_seed, ballots, layout)`. PRF: `BLAKE2b-512(personal "ZcashVoteProxyEx", dc_seed ‖ domain ‖ share_index)`. **Layout 0:** denomination split (remainder 0x13), shuffle (0x12), `r_i` from 0x10, `blind_i` from 0x11. **Layout 1:** `shares = [w, 0×15]`, unshuffled, `r_i` from 0x14, `blind_i` from 0x11, mirroring the vote builder's single-share path (`builder.rs:538-550, 812-818` [code]). All 16 ciphertexts and commitments are still built because `shares_hash` covers 16, so the ZKP4 VK, rows (2,015) and proof size (11,008 B) are unchanged. C9 holds because `w ≤ W < 2^30`, and C12 because `w ≠ 0`.
- Also exported: `proxy_delegation_commitment_hash`, `dc_slot_nullifier`, `dc_share_nullifiers`, `share_reveal::build_pool_share_reveal`, ballot variants of the ZKP2 builders, and `vk_fingerprints()` for ZKP1-4 [new], returning the values the `vk_fingerprint_unchanged` tests pin.
- Frozen vectors for every formula.

**Zero-weight successor.** A successor with `W_new = 0` ("delegate everything") can still produce zero-weight ZKP2 casts [measured]. By convention it is **dead**: clients never cast from it, and ZIP-PD says so (CF-2). The behavior is pinned with a test.

**Tests.**
- MockProver negatives for C1-C14. For C14: `dc_slot = 7` is accepted and `dc_slot = 8` is rejected by the 3-bit check, and `dc_slot_nf` is distinct for every `s` in 0..8. A positive at `d = u32::MAX` pins that the circuit places no bound on `d`.
- Nullifier parity with ZKP2.
- Chains: ZKP4→ZKP4→ZKP2 passes; ZKP2→ZKP4 fails; a further ZKP4 after `W_new = 0` fails.
- Replay of every public input.
- A ZKP4 proof under the ZKP2 VK fails.
- Witness independence (`Circuit::default()` gives the same row count).
- Row budget: re-measure after every change, because the 33-row headroom is shared.
- [measured] In the rev-3 prototype re-run, slots 0 and 7 verify; slots 8, 15 and 16 fail only at the 3-bit range-check lookup; the slot tags for `s` in 0..8 are distinct from each other and from the crate's other domain tags; and the 20-test proxy suite passes (including the C15 tests that rev 6 removes with C15).
- Layout 1: MockProver positives for `w = 1` and for `w = W = 2^30 − 1` with `W_new = 0`; the negative `share0 = 2^30` with `W = 2^30 + 1` (so `W_new = 1` passes C7, and the test asserts that the failing constraint is the C9 range check); a real layout-1 proof under the unchanged VK; a ZKP3 reveal of layout-1 share 0 as `(0, d)`; pinned vectors for 0x14 randomness and the layout-1 `shares_hash`; a check that a layout change alters every `r_i` and blind. [measured] The cloned prototype test `zkp4_single_share_layout_accepts_and_reveals` passes the positives (`w = 4,000`; `w = W = 2^30 − 1`) and the ZKP3 `(0, d)` reveal in 0.74 s (`prototypes/circuits/single_share_test.log`); the test is now part of `zkp4-prototype.patch` and passes in its 20-test suite. Its negative case used `W = 2^31`, which also violates C7, so the C9 rejection is not yet isolated.

### 4.2 Chain (vote-sdk v1.7.0)

#### Messages and tags

New tags are 0x09 (DC batches) and 0x0B (delegate ops: register, revoke and ballot), and 0x04 gains a branch. 0x0C stays unassigned. Every new client tag needs:
- an explicit `IsVoteTag`/`IsCustomTag` branch (the existing check is a contiguous range, `api/codec.go:57-59` [code]);
- canonical protobuf encoding;
- strict JSON at REST;
- fee-less handling with an infinite gas meter.

**0x09 `MsgProxyDelegationBatch`** (DCs only, rev 6; decision 23). It is written as a near copy of the existing 0x06/0x07 batch code: ValidateBasic `types/msgs.go:167-235`, ante `ante/validate.go:185-243, 383-448`, handler `keeper/msg_server.go:221-354` and sighash shape `types/sighash.go:77-132` [code].

```proto
message ProxyDelegationAction {
  bytes  van_nullifier = 1;               // 32
  bytes  r_vpk = 2;                       // 32, compressed, non-identity
  bytes  vote_authority_note_new = 3;     // 32, successor VAN (W-w, MAX)
  bytes  proxy_delegation_commitment = 4; // 32
  bytes  dc_slot_nullifier = 5;           // 32
  bytes  proof = 6;                       // ZKP4, <= MaxProofSize
  bytes  vote_auth_sig = 7;               // 64, RedPallas under r_vpk over D1
}
message MsgProxyDelegationBatch {
  bytes vote_round_id = 1;
  uint64 vote_comm_tree_anchor_height = 2;   // 0 iff registration present
  MsgDelegateVote registration = 3;          // optional ZKP1
  repeated ProxyDelegationAction dcs = 4;    // 1..8
}
```

**ValidateBasic.**
- `1 ≤ len(dcs) ≤ 8`. Casts never ride in 0x09; kept weight is voted later with ordinary 0x06 casts from the remainder VAN (decision 23).
- A registration is present **if and only if** `anchor_height == 0` (SND-7). The registration carries the batch round.
- `registration.ValidateBasic()`.
- Intra-message uniqueness of `van_nullifier`, of `dc_slot_nullifier`, and of the set {DCs, final `van_new`}. These follow `msgs.go:180-205` [code], because `CheckNullifiersUnique` is store-only (`keeper_voting.go:54-66` [code]).
- Field sizes are checked.

**Ante.** Every step also runs on RecheckTx.
1. Dormant gate.
2. `ValidateRoundForVoting`, then `round.proxy_delegation.enabled`, then `!params.proxy_dc_paused` (`ErrProxyDelegationPaused`, retryable; D9).
3. `EnsureCommitmentCapacity(1 + n)`.
4. Nullifiers: an explicit 0x09 case next to the 0x07 special case (`validate.go:123-128` [code]) that checks gov (0x00, only when a registration is present), VAN (0x01) and slot (0x03).
5. Every DC signature over D1, all checked before any proof.
6. The registration's own verifier.
7. Proofs. `root0 = SingleLeafRoot(van_cmx)` if a registration is present, else `GetCommitmentRootAtHeight(anchor)`, which must be non-nil. Each later root is `SingleLeafRoot(prev.van_new)`.

**Handler.** Re-run every check (all three nullifier sets before any write); set nullifiers; append the final VAN, then each DC in order (never intermediate VANs or `van_cmx`); emit `proxy_delegation_batch{round, batch_digest, has_registration, final_van_leaf_index, dc_leaf_indices, van_nullifiers, dc_count}`.

**D1 digest** (signed by each DC's `r_vpk`):

```
BLAKE2b-256("SVOTE_PROXY_DELEGATION_BATCH_SIGHASH_V1" || lp8(chain_id) || write32(round)
  || writeU64As32(anchor_height) || u8 has_registration || [write32(van_cmx)]
  || writeU32As32(n) || for i: writeU32As32(i) || write32(r_vpk) || write32(van_nf)
     || write32(van_new) || write32(dc) || write32(dc_slot_nf))
```

D1 stops grafting and truncation of DCs between batches. The registration keeps its own self-contained signature, as in today's 0x07. A relayer can therefore lift it out and submit it alone; the batch then fails on spent gov nullifiers, and the client re-plans the same DCs against the real anchor (CF-5).

**0x0B `MsgDelegateOp`** (one delegate key, rev 6; re-key by re-registration, rev 8; decision 10). One tag carries the three delegate-signed ops:

| Op | Signed by | Effect | Notes |
|---|---|---|---|
| `register {delegate_index, dk, provider, subject_commit, attestations[], dk_sig}` | The new DK over REGISTER (proof of possession); verifier threshold over ATTEST | `delegate_index = 0`: a new ACTIVE entry, index assigned at once. `delegate_index = d`: a re-key of entry `d`, which the verifier attests is the same account. The DK is replaced, the status becomes ACTIVE (a coordinator suspension stays), and `rekeyed_at_time` is set | Accepted at any time while the verifier set is non-empty, including mid-round (D10); `next_index ≤ min(max_delegates, 2^30)`; a re-key must carry the entry's own `subject_commit`; a key is never reused, so an attestation cannot be replayed |
| `revoke {delegate_index, dk_sig}` | The entry's DK over REVOKE | ACTIVE→REVOKED. Registering again from the same account reactivates the entry by re-keying it | A ballot already accepted stands |
| `ballot {vote_round_id, delegate_index, options[], dk_sig}` | The entry's DK over BALLOT | Stores the round's one ballot for `d` | Rules below |

There is no separate rotation, recovery, cancel or freeze op, and no pending state or delay. A verifier-attested re-registration from the same account is the only key change, and the stored entry is always the effective entry.
- The old key stops at once.
- The new key can submit ballots only in rounds created after `rekeyed_at_time` (Ballot op), so a re-key never captures pools formed before it (IDN-1).
- The verifier, not the chain, enforces a 14-day cooldown between registrations of one account (§4.6 Verifier policy). A lost or leaked key is replaced this way (§4.6 Re-registration and key loss).

**Attestations.** Each is `{verifier_id, not_before, expires_at, sig}` over ATTEST, which binds `chain_id`, `delegate_index` (0 for a new entry), the DK, the provider, `subject_commit`, `not_before` and `expires_at`. The validity window is at most 72 h (a constant). `register` needs at least `params.verifier_threshold` valid attestations from distinct verifiers in the current params verifier set (launch: 1-of-1). There is no single-use `attestation_id`, because the DK is never reused (rev 6).

**Validation and preimages.** One digest convention:

```
BLAKE2b-256(domain || lp8(chain_id) || fields as write32 / writeU32As32 / writeU64As32)
```

The domains are `SVOTE_PROXY_DELEGATE_{SUBJECT, REGISTER, ATTEST, REVOKE, BALLOT}_V1`. REGISTER and ATTEST bind `delegate_index`, so a new registration and a re-key cannot be confused. A unit test asserts that every `SVOTE_` domain, including `SVOTE_PROXY_DELEGATION_BATCH_SIGHASH_V1`, is prefix-free. [measured] The 24 domains checked in review were prefix-free; [inference] the rev 6 set is a subset plus the renamed D1 domain, and the test pins it.

**Ed25519 rule.** ZIP-215 verification everywhere: CometBFT `crypto/ed25519` in Go, and `ed25519-zebra` or `ed25519-consensus` in Rust. Keys must be canonical, not small-order, and free of torsion (`[ℓ]A = O`).

[measured] A key with an order-2 component passed the old key check. CometBFT accepted 2,000 of 2,000 of its signatures, but Go stdlib accepted only 1,029 (IDN-15).

**Ballot op.** One atomic ballot per `(round, d)`, covering every proposal of the round (D2, decision 34). Proposal ids are always `1..n` (`keeper_voting.go:255-263` [code]), so `options` is a dense array: `options[i]` is the choice on proposal `i + 1`. The digest:

```
BLAKE2b-256("SVOTE_PROXY_DELEGATE_BALLOT_V1" || lp8(chain_id) || write32(vote_round_id)
  || writeU32As32(d) || writeU32As32(n) || n × writeU32As32(option))
```

ValidateBasic checks `n ≥ 1` and a 64-byte `dk_sig`. The handler accepts the ballot only when:
- the round is ACTIVE and `round.proxy_delegation.enabled`, and `blockTime < vote_end_time`;
- `n = len(round.Proposals)`, so the ballot covers **every** proposal of the round;
- every `options[i] < num_options(i + 1)`. There is no uncounted abstain value, and any other value is rejected. A round may include a real "Abstain" option, which is an ordinary option (Q2);
- the delegate is ACTIVE (not REVOKED) and not suspended;
- if the entry was re-keyed, the round was created after `rekeyed_at_time`;
- no ballot is stored for `(round, d)`. The ballot is one-shot and cannot be amended: any second ballot, identical or not, is rejected with `ErrDelegateBallotExists` (decision 26). The client treats that error as success when the stored ballot equals its own, so retries near the deadline stay safe, and no copy of a signed ballot can keep landing as a no-op.

A delegate registered after the round was created submits under the same rules. `proxy_dc_paused` does not affect ballots. A ballot already accepted stands through a later suspension, revocation or re-key; a delegate suspended or revoked before its ballot lands cannot submit one, so its pool counts nowhere this round (D7). Writing the `0x1B` key for `(round, d)` emits a `delegate_ballot{round, delegate_index, options}` event.

**0x04 `MsgRevealShare` with `proposal_id = 0`.** Accepted if and only if `round.proxy_delegation.enabled` and `1 ≤ d < NextDelegateIndex` at reveal time; delegate status and `proxy_dc_paused` are ignored. Effects: `AddToTally(round, 0, d)`, `IncrementShareCount(round, 0, d)`, and a `reveal_pool_share{round, delegate_index, share_nf}` event (never `reveal_share`). With no in-circuit bound (O1), this check is what keeps pool shares on registered indices; a DC to an unregistered index is never revealed, which hurts only its sender. It counts toward the 256-per-block cap. Errors `ErrProxyDelegationDisabled` and `ErrDelegateNotFound` are permanent. A layout-1 (last-moment) DC contributes one reveal and a layout-0 DC sixteen; neither needs a chain change, and the chain cannot tell them apart (`MsgRevealShare` carries no share index, `proto/svote/v1/tx.proto:120-128` [code]).

**Coordinator payloads.** Each is one more case in the closed payload switch (`msg_server_coordinator_actions.go:228-338` [code]).
- `MsgSetProxyDelegationParams`: the verifier set (0 to 16 verifiers with unique ids and Ed25519 pubkeys), `verifier_threshold` (k-of-n counting is implemented from the start, so adding the planned second verifier, Q6, needs no upgrade) and `max_delegates` (≤2^30; launch 20,000). An empty verifier set turns registration off. There are no delay floors, key-change caps or verifier warm-up, because no delayed key change exists (rev 6). It never changes `proxy_dc_paused`.
- `MsgSetProxyDelegationPause {paused}`: sets `proxy_dc_paused`, the one mid-round off switch (D9, §8.3). It is its own payload so that a pre-drafted pause and unpause pair can never roll back other params.
- `MsgSetDelegateSuspension`: immediate, freeze-only, public reason code (1 impersonation, 2 key compromise, 3 legal, 4 verifier review, 99 other). It cannot create entries, change keys or void a ballot already accepted. A suspension before the ballot means no ballot, so the pool counts nowhere (D7). It is also how a stolen key is stopped when its owner cannot revoke (§4.6 Re-registration and key loss).
- **Proxy rounds** are flagged in `MsgCreateVotingSession`, which gains a `proxy_delegation` bool [new] (decision 45). The flag and `next_delegate_index_at_creation` are written to `VoteRound` field 31 at creation.
- **Approval floor (D7, Q5 decided).** These three payload types, and a `MsgCreateVotingSession` that sets the proxy flag, execute only with at least `max(2, policy.threshold)` distinct vote-manager approvals, so they never execute inside the proposing tx (today a threshold-1 action does, `msg_server_coordinator_actions.go:34-36` [code], and the policy threshold defaults to 1, `docs/vote-coordinator-actions.md:19` [doc]); the floor goes at `msg_server_coordinator_actions.go:170-176` [code]. Activation therefore needs at least 2 vote managers in the coordinator policy before the first proxy payload (§8.3), and the policy reaches 2-of-3 within the first two proxy rounds. Production runs one vote manager today (`scripts/init.sh:27,74-75` [code], per IDN-3).

#### State (normative key table)

| Key | Value |
|---|---|
| `0x1A 01 u32be(d)` | `ProxyDelegateEntry` {index, dk, status ACTIVE/REVOKED, suspended + reason, provider, subject_commit, registered_height, revoked_height, rekeyed_at_time} |
| `0x1A 02 dk[32]` | `u32be(d)`: every delegate key ever registered, including revoked ones, so a key is never reused (forward plus reverse index, as in the Pallas-key registry, `keeper_pallas_registry.go:33-48, 102-117` [code]) |
| `0x1A 04` | `next_index` (genesis 1; index 0 never assigned) |
| `0x1A 06` | `ProxyDelegationParams` {verifiers, verifier_threshold, max_delegates, proxy_dc_paused} (the only params key) |
| `0x1A 07 u64be(seq)` | reserved: Phase 2 directory anchor |
| `0x1B round u32be(d)` | `DelegateBallot` {options (one per proposal, in proposal order), height}: at most one per `(round, d)`, never overwritten |
| `TallyKey(round,0,d)` | `Pool[d]` (ciphertext; never decrypted on its own) |
| `ShareCountKey(round,0,d)` | pool share count |
| `NullifierKey(0x03, round, nf)` | DC slot nullifiers (`NullifierTypeProxySlot = 0x03`) |
| `VoteRound` field 31 | `proxy_delegation {enabled, next_delegate_index_at_creation, pools_routed}` (outside the round-id preimage); written only for proxy rounds, from the `MsgCreateVotingSession` flag |

Rev 6 removes `0x1A 03` (attestation ids), `0x1A 05` (verifier set, now in params), the derived `0x1A 08` and `0x1A 09` indexes, `0x1C` (routing bases), `0x1D` (proxy-action records), `0x1E` (ballot height index) and `0x1F` (routing counts). Those prefixes stay unused. No derived state remains to rebuild.

**Ballot absence.** A delegate has no ballot in a round when `0x1B round u32be(d)` is absent at or after the tally transition. There is one key per `(round, d)` and never a per-proposal key (OPS-8). Vizor reads ballots and their absence from the whole-round ballot list, without proofs (D1). A golden key-derivation vector for the registry entries Vizor does prove (§4.4 Chain verification) is shared by `keys.go` tests and Vizor's reader.

**Genesis.** One normative GenesisState section covers:
- nullifier types 0-3: `ValidateGenesisState` must accept type 3, because `genesis.go:94-97` rejects anything above 2 today [code];
- the registry entries, ballots and params. `0x1A 02` and `next_index` are rebuilt from the entries on import.

`TallyKey` and `ShareCountKey`, including the proposal-0 pools, are already exported in full (`keeper/genesis.go:267-279` [code]). `TestExportImportGenesis` is extended with one finished and one active proxy round.

**Reset rule.** Any chain reset that does not export and import the registry **must change `chain_id`**. That kills replay of old registry and ballot signatures.

#### Queries, REST and capabilities

- `delegates?start_after=&limit≤1000` (key-paged over `0x1A 01`), `delegates/{index}` and `delegates/by-key/{hex}`. They serve the directory publisher, delegate tooling and explorers, **not** the delegator path.
- `delegate-ballots/{round}`: the whole round's ballots, key-paged, live during the round.
- Params, including the verifier set and `proxy_dc_paused`.
- `ProposalTally` rejects `proposal_id = 0` and ids above `len(proposals)` (OPS-12).
- Event names and fields are catalogued in contracts-v1.
- Rev 6 removes `updated_since_height` paging, `delegate-pools`, the `nullifier` query, the `proxy-actions` feed and `routing-audit` (decision 40). Queries are not consensus, so one can return in a v1.7.x patch without a halt. A delegate's dashboard reads its own pool's share count with a raw store query on `ShareCountKey(round, 0, d)` (`abci_query`).
- `ProtocolCapabilities` has fields 1-3 today (`proto/svote/v1/query.proto:73-77` [code]) and appends two fields [new]:

  | Field | Value |
  |---|---|
  | 4 | `proxy_delegation_version` (uint32; 0 = off; 1 = tags 0x09 and 0x0B with 8 DC slots per registration) |
  | 5 | `repeated CircuitFingerprint{name, vk_blake2b}` for ZKP1-4 |

  The identity work claims no field numbers of its own (CMP-12).

#### Routing algorithm (EndBlock, in the iteration that sets ACTIVE→TALLYING, before `SetVoteRound`)

Routing adds each pool whose delegate has a ballot into the option that ballot chose, on every proposal; a pool without a ballot is added nowhere. It iterates the round's ballots (`0x1B||round`, ascending `d`) and looks up each delegate's pool (decision 40). Only a `d` with both a ballot and a pool contributes.

```
if !enabled or pools_routed: return
for each ballot b under 0x1B||round (d ascending):
    pool := GetTally(round,0,d); if nil: continue        // no reveals
    // a decode failure, or a stored ballot without exactly one option per proposal, is state corruption -> halt
    for p in 1..n: sums[(p, b.options[p-1])] ⊕= pool      // every proposal of the round
for (p,o) in sums ascending:
    combined := GetTally(round,p,o) ⊕ sums[(p,o)]
    rho := HashToScalarPallas("svote-route-rerand-v1" || round || u32be(p) || u32be(o) || Marshal(combined))
    final := combined ⊕ (rho·G, rho·ea_pk)   // retry with rho := H(rho) while C1 or C2 is identity (≤8 tries)
    Set(TallyKey(round,p,o), final)
set pools_routed; emit proxy_pools_routed{round, ballots, routed_pools}
```

**Why this is safe.** Pools and ballots are final: the transition block admits no reveals or ballots (`keeper_voting.go:336-338`, `msgs.go:554-557` [code]). Every counted pool is added once on every proposal, so per-proposal totals differ only by direct votes, which closes the cross-proposal turnout channel by construction (§5 Pool solvability). Results are then decrypted once, as today, and never re-tallied (decision 37). ρ (deterministic; curvey `expand_message_xmd` with BLAKE2b) blocks a forced identity C1 that would fail every DLEQ and time out the round [measured in `prototypes/chain/route_test.go`]. `AddToTally` rejects identity ciphertexts on each reveal (`keeper_tally.go:45-81` [code]), but the routed sum is formed in EndBlock, where nothing can be rejected, so ρ stays. Buckets stay under 1.68·10^8 ballots, below `TallyBSGSBound = 2^28`. Proposal 0 stays excluded from the partial decryption (0x0D), completeness, `ValidateEntryBounds` and `MsgSubmitTally` [code]; tests lock it in. No code path decrypts a pool on its own (D4) or produces a running or intermediate decrypted total. An auditor with archival state can recompute routing on ciphertexts from the buckets and pools at the height before the transition, the ballots and ρ; there is no audit query (decision 40).

**Cost.** [measured] 10k delegates × 50 choices (one per proposal) took 1.39 s in memory on an M3 Ultra. The scan is bounded by the round's ballots, at most `max_delegates`. Benchmark the real IAVL-backed hook on the 2-4 vCPU validator droplets in CI. Launch `max_delegates` is 20,000, raised only with benchmark evidence. Fallback: spread routing over the first K TALLYING blocks, gating partial-decrypt injection on `pools_routed` (OPS-13).

**Reveal close.** A test pins that pools are routed before any partial decryption is accepted. The `revealCloseTime(round)` refactor that would prepare the optional post-close reveal grace window (Q18) is deferred to v1.1 with Q18 (rev 6): it touches shared validation for every round and buys nothing in v1.

#### Limits, spam and per-block rules

- Slot cap: 8 DCs per registration per round. Per attacker proof this gives 16 reveals, the same as a cast, and a registration can cause at most 8 × 16 = 128 pool reveals.
- 0x09 caps at 8 DCs and carries no casts. [inference from measured] Each proof costs about 15 KB in the RPC body, so ZKP1 plus 8 DCs is about 100-150 KB, far under the 1 MiB REST and 1,000,000 B Comet limits; no byte budget or packer is needed.
- PrepareProposal admits at most one ballot tx per `(round, d)` per block, with the filter copied from the existing reveal filter (`app/vote_share_submission_proposal.go:28-87` [code]). Registry ops need no dedupe or cap: `register` needs a fresh attested key, and `revoke` is terminal.
- No per-block proof-verification cap in v1 (decision 24). If one is ever added, it is weight-based, at least 51 proofs per tx, and FIFO across tags.
- The tree-capacity guard returns `ErrCommitmentTreeFull` inside `AppendCommitment`, and `MaxTreePosition` is fixed to 2^24−1.

#### Dormant merges and activation

A single `ProxyDelegationEnabled = false` const gates tag decode, interface **and Msg-service** registration, the p=0 branch, routing, new payloads, the `MsgCreateVotingSession` proxy flag, capability fields and any change to existing validation. While the const is false, field 31 is never written. Proxy PRs are labeled `V:state/breaking`, never backported to v1.6.x, and ship only in v1.7.0, which activates at a halt between rounds; no two binaries run the same heights, so there are no GasUsed-equality tests for the dormant paths (rev 6, OPS-6). The activation commit flips the const and registers a no-op `v1_7_0` handler.

### 4.3 Helper (vote-sdk `internal/helper`)

**Proposal-0 branch.** `validatePayload` accepts `proposal_id = 0` with a u32 `vote_decision ≥ 1` and skips the `< 8` option check. For p=0, `verifyCommitment` runs **first** (the leaf at `tree_position` must equal the recomputed DC hash; an absent leaf returns a retryable 503). Only then is `1 ≤ d < NextDelegateIndex` checked, as one registry read in the choice validator (`internal/helper/api.go:734-743` [code]), with a permanent distinct error (LIV-9, OPS-11). With no in-circuit bound (O1), this error is reachable for a DC whose sender chose an unregistered index; the helper drops such a payload. Scheduling, store, retries and prover are unchanged. Layout-1 DC payloads need no helper change: one payload, share index 0, `submit_at = 0`, handled like single-share votes (enqueue schedules `submit_at = 0` at arrival, `internal/helper/store.go:459-472, 503-509` [code]; acceptance requires an ACTIVE round, `api.go:328-333` [code]).

**Telemetry hygiene** (PRV-6; also fixes votes today). Remove `proposal_id`, `tree_position` and `submit_at` from span data and never add `vote_decision` or `d`; name HTTP transactions by `mux` route template, not raw path; add a test that fails if span data contains these keys.

**Capacity** (LIV-1, medium). Each DC adds 16 helper proofs to the FIFO shared with votes, and delegators who would not otherwise vote are pure added load. [measured] A queue model (10 operators × 2 workers × 0.58 shares/s = 11.6/s) loses about 20% of both DC and Zodl direct-vote weight at 5k delegators × 3 with deadline-heavy arrivals and 2× duplicate proving. 4 workers per operator remove the loss even at 50k × 3 (dup = 1). The same model also predicts loss for direct votes alone at 5k × 37 proposals, so its inputs may be pessimistic; capacity must be measured, not assumed. Required:
1. Replace the "reduces load" claim with a budget: added reveals = 16 × DCs made before the last-moment window + 1 × DCs made inside it, from delegators who would not otherwise vote. Parity moves part of this load into the window, where it competes with Zodl's last-moment votes and queues behind any older backlog (`docs/helper_submission_invariants.md:27-38` [doc]).
2. Metrics: proofs per unique reveal; queue depth by class (bounded labels).
3. Raise workers to the measured headroom before the first public proxy round.
4. Release gate: in the mixed load scenario, which includes in-window DCs, Zodl direct-vote reveal loss and latency must not regress against the direct-only baseline.

A deterministic primary helper per share is an optional, separately tracked improvement.

### 4.4 Client library (zcash_voting)

**Modules.** `proxy::{planner, batch, secrets, registry, status, gating, delegate}`, plus the shared `zcash_vote_delegate` crate (digests, marker parser, phrase, fingerprint, lookalike fold; cross-language vectors). A dormant `PROXY_DELEGATION_ENABLED` const.

**Allocation and planner** (`PROXY_PLANNER_VERSION = 1`).
- **Lifecycle.** One durable allocation per `(round, wallet)`: preview → commit (with `plan_digest`) → locked. 1 to 8 entries `(d, dk, bps, display_handle)`, stored as one immutable row; the planner rejects a ninth. Remainder is `KeepForSelf` or `DelegateEverything`. Vizor's editor takes percentages.
- **Rounding.** Hamilton largest remainder in u128. Minimum 1 ballot per delegate, funded by Keep or the largest delegate. The floor is fixed at 1 ballot (Q3).
- **Packing (decision 44).** Sequential fill: take bundles largest first and delegates largest first, and lay each delegate's quota into the current bundle, moving to the next bundle when it is full. The kept remainder is whatever capacity is left. Every boundary split adds one DC, so the bound is `D ≤ #DC ≤ D + B − 1`. A bundle never carries two DCs to the same delegate, so it holds at most 8 DCs. [measured] Over 19,961 simulated wallets the fill made 2.845 DCs per delegator, against 2.834 for rev 5's exact packer, and more DCs in 1.2% of cases; with one bundle, the common case, the two are identical (`prototypes/client/compare_packers.out`).
- **Slots.** A slot is `(bundle, dc_slot 0..7)`, and `dc_slot` is the C14 slot. It equals the DC's position in that bundle's 0x09, and positions follow a **CSPRNG shuffle** taken at commit so DC order does not reveal size ranking (PRV-9). Slot positions and nullifiers are persisted before proving and reused on retry.
- **One delegation per round (D2).** Delegation is available only until commit. The allocation then locks for the round and is never changed or extended; there is no API to amend it.

**Batch composition (decision 23).**
- Per bundle, one 0x09: `[ZKP1?] → DC…`. The kept remainder is always voted later with ordinary 0x06 casts on the remainder VAN, through the existing vote flow. Inside the last-moment window, Vizor takes the user from the delegation straight to that vote flow, and a remainder vote sent in the final minute may miss close, as any late vote may (LMD-7).
- A `W_new = 0` successor never gets casts (CF-2).
- Proving runs in phases: ZKP1, then all ZKP4s. Each proving key is loaded for its phase and evicted before the next. ZKP4 is never pre-warmed at app start, and a peak-RSS budget is part of the device benchmark gate (CF-4).
- **Last-moment parity (D5, O3).** A new allocation or proxy batch is allowed while `host.now_seconds < vote_end_time` and returns `VoteEnded` afterwards, mirroring `vote_work/cast_vote.rs:39-52` [code]; in-flight work advances, and the chain rejects anything landing at `blockTime ≥ vote_end_time` without spending the VAN (`keeper_voting.go:336-338` [code]). `proxy::availability` requires authenticated `ceremony_start_seconds` and `vote_end_time_seconds` in the `RoundHostContext`, because without them the SDK skips `VoteEnded` and `is_last_moment()` returns false (`cast_vote.rs:39`, `vote_work/mod.rs:117-124, 136-138` [code]); committed work still advances (LIV-12).
- **Layout.** Chosen **once per 0x09 batch** at planning: single share (layout 1) if and only if `RoundHostContext::is_last_moment()`, the same call casts use (`cast_vote.rs:53` [code]). The window is `min(40% of round, 6 h)` (`share_policy/timing.rs:19-45` [code]). The layout is persisted with the batch before the first proof and used for every DC on every re-prove and split-off re-plan, so a re-prove yields a byte-identical DC and exact-tree recovery of an ambiguous submission can match it (§4.4 Recovery). Remainder casts are separate votes, with the layout the vote path chooses for them. A layout-0 DC that lands inside the window delivers its 16 shares at `submit_at = 0` (`submission_schedule.rs:128-131` [code]), as a pre-window vote that lands late does. The clock is the same host clock votes use.

**DC secrets** (PRV-1; decision 14). Every account, software or hardware, derives DC secrets from its voting hotkey, which Vizor generates at random per account and round and keeps in app secure storage (Vizor `rust/src/wallet/voting/README.md:42-48` [code]):

```
hrk     = BLAKE2b-256(key = hotkey_secret, personal "ZVoteProxyHRK_v1", net_u8 ‖ round_id)
dc_seed = BLAKE2b-256(key = hrk, personal "ZVoteProxySeed01", van_nf ‖ d_le32 ‖ w_le64 ‖ layout_u8)   // 45-byte input
```

`layout` is 0 (standard) or 1 (single share). This mirrors how votes derive share randomness (`zkp2.rs:89-93` [code]). Nothing derives from the OVK, FVK or spending key, so UFVK holders cannot read delegations and no seed material crosses the wallet boundary. The circuits are unchanged, because the builder already takes `dc_seed` as an input. `dc_seed` binds `(van_nf, d, w, layout)`, so two DCs never share El Gamal randomness.

**Invariant:** the batch layout and each slot's `(d, w, slot, layout)` are persisted before the first proof and reused by every re-prove and split-off re-plan, so every re-prove publishes a byte-identical DC (§4.4 Recovery). A released slot (LIV-3) never landed: its batch was never POSTed, or was definitively rejected.

**Persistence (schema v25, one-way).** New tables:
- allocations, each one immutable row with its entries;
- slots, with `dc_slot`, `dc_slot_nf` and the batch `layout`, written before proving;
- delegate identity metadata;
- the delegate's signed ballot per round, so a retry resubmits it unchanged.

Batch state lives in `chain_submissions` (one or two new kinds). DC share plans and deliveries use the existing share tables, with `ShareKey` gaining `CommitmentRef::{Vote, Proxy}`.

Effective VAN weight, `floor(total_note_value / 12,500,000) − Σ dispatched or confirmed slot ballots`, replaces `total_note_value` at every VAN-recompute site (`zkp2.rs`, `vote.rs`, `load_van_tree_entries`, tree sync and others; about 72 uses across 10 files). Account deletion removes all proxy tables.

**NextStep and planner obligations.**
- **New kinds.** There are two: `ProxyDelegate` (commit, plan and prove) and `AdvanceProxyBatch` (submit and confirm), with exhaustive matches. DC shares reuse `SubmitShares` and `ConfirmShare` through `CommitmentRef::Proxy`. Proxy-only rounds plan ZKP1 with zero ballot intents. A bundle with a proxy batch in flight is held from casts, and vice versa.
- **Release path (LIV-3).**
  - *When it is allowed:* the bundle is `ProxyBlocked{Paused}` (the chain returned `ErrProxyDelegationPaused`, D9) or `ChainTerminal`, and none of its batches has a reserved POST (an unresolved in-flight POST) or a landed tx.
  - *What it does:* `release_proxy_bundle(bundle)` drops those slots, turns their weight into Keep, lifts the hold, and records the release on the released slots; the allocation row and its `plan_digest` stay unchanged. Landed slots stay final. This is failure recovery for weight that never left the VAN, not a change to a delegation.
  - *Narrowing the window:* prove every bundle's batch before broadcasting any. A paused batch is retried automatically if the pause lifts before `vote_end_time`.
- **Split-off.** "Registration landed, batch rejected" is a normal transition: re-plan the same DCs against the real anchor (CF-5).

**DC shares.** The existing `VoteShareWire` carries `proposal_id = 0` and `vote_decision = d`. Layout 0 sends 16 shares on the standard `submit_at` schedule; layout 1 sends one share (index 0) with `submit_at = 0` to `ceil(N/2)` helpers (`server_order.rs:51-53` [code]). The proxy share planner keys plans by `(round, wallet, bundle, dc_slot)` and passes `single_share = (layout == 1)` explicitly to `plan_share_submissions_with_preferred_servers`. It never infers layout from the payload count, and never reuses the votes-table planner, which is keyed by `proposal_id` (`share_tracking/delivery_plan.rs:44-64, 99-120` [code]). DC shares stay out of `derive_immediate_share` and `validate_round_immediate_plans`. **No DC share is ever the designated immediate share**, so proxy-only rounds have none (PRV-3); inside the window every share, vote or DC, is immediate anyway. "Handed off" means definite acceptance of every DC share (1 or 16) by its target helpers (LIV-2); no reveal progress is shown (decision 42). For a DC whose shares were planned with `submit_at = 0` (inside the window, either layout), completion additionally waits for the confirmed reveal of share 0 (§4.5 Job).

**Status view ("did my delegate vote").** A plain view model, with no proofs (D1, decision 35) and no reveal counter (decision 42).
- *Per delegation:* the delegate, the amount, inclusion (from the 0x09 result and event) and "Handed off" from helper share-status, which is liveness information, not verification.
- *Per chosen delegate:* the ballot as published. While the round is ACTIVE this is `Submitted(options)` or `NoBallotYet`; after the tally transition it is `Counted(options)`, or `NotCounted` when no ballot landed.
- *Where ballots come from:* the whole-round ballot list (`delegate-ballots/{round}`, key-paged; never a per-delegate query), so the server does not learn which delegates the user follows.
- *What the docs say plainly:* Vizor shows ballots and hand-off status as the RPC server and helpers report them, helpers always learn each DC's delegate, and helpers and the RPC server can link the chosen delegates to the user's IP unless Tor is on.

**Chain verification.** Light-client and IAVL verification lives in Vizor's existing participation reader (`rust/src/wallet/voting/participation.rs` [code]), not in zcash_voting. The reader verifies a signed header with `tendermint_light_client_verifier` against bundled, hash-pinned validator sets, accepting a changed set only with more than 2/3 of both the bundled and the current voting power, and then verifies `ics23` IAVL membership and non-membership proofs under that header's app hash. Today it accepts only governance-nullifier keys, with value `[1]` or absent (`participation.rs:337-430` [code]). Proxy delegation extends it with the registry entry keys (`0x1A 01 u32be(d)`) and a value check for the entry encoding [new], which mostly generalizes the existing value comparison. With the stored entry always the effective entry (decision 10), the reader needs no consensus logic beyond decoding it. It serves two proxy checks: the pre-proving registry check (Registry client, below), which is safety, so a lying server cannot redirect a delegation, and gov-nullifier non-membership (LIV-8), which uses existing keys. Delegators get no proofs of ballots, slot nullifiers or share nullifiers (D1). zcash_voting has no light client: 5.1.1-rc.3, the version Vizor pins (`rust/Cargo.toml:129-130` [code]), has no tendermint or ics23 dependency [code]. It supplies the registry key derivations, with golden vectors shared with `keys.go`, and the expected values (the overlay entries); Vizor verifies and hands the verified values back to the SDK. Every "proven" check in this plan runs this way.

**Recovery (decision 17).** The local database is the only recovery source, as for votes.
- An app restart resumes from the database. A submission whose outcome is unknown, for example a POST that timed out, is resolved by exact commitment-tree recovery (`chain_submission/recovery.rs` [code]). This works for 0x09 because the final VAN is appended before the DCs in order (§4.2), and because every re-prove yields byte-identical DCs (the invariant under DC secrets).
- Hand-off happens at initial delivery: every share goes to its helpers at once, carrying its `submit_at`, and the helpers schedule the reveal (`share_policy/submission_schedule.rs` [code]). A delegation that shows "Handed off" therefore counts (through its delegate's ballot) whatever happens to the device afterwards.
- There is no recovery after a reinstall, seed restore or database loss. Vizor's hotkeys are random per account and round and "cannot be recreated from a wallet seed or Keystone UFVK" (Vizor `docs/voting-participation.md:146` [code]). Re-importing a wallet also mints a new account id. So, exactly as for votes today, a delegation that was not yet handed off is not counted, and the kept remainder cannot be voted from the new install. Copy C8 says so.

**Registry client.**
- **Round proxy entry (decision 39).** From the dynamic config Vizor already fetches and verifies, read the round's entry under `extensions.proxy_delegation_v1.rounds[round_id]` (§4.7). Verify its signature with the existing `trusted_keys`, the same keys that sign the round's `ea_pk`. The entry gives:
  - the vetted list's sha256;
  - the live directory's signing keys for that round.
- **Vetted list.** Fetch the list from beside the dynamic config, at `delegates/<sha256>.json` on the same domain and fallback. Check that its hash matches the signed entry. Then fetch the picture pack named inside the list and check its hash too. The list is fixed for the round. Vizor caches it per round and never accepts another list for that round.
- **Live directory.** Fetch `directory.json` from one of the two directory mirrors, failing over to the other. Verify:
  - the signature, with a key named in the round's proxy entry;
  - that `seq` is no lower than the last one seen;
  - expiry and `chain_id`.

  There is no local registry mirror and no `updated_since_height` paging (rev 6).
- **Overlay checks.**
  - A delegate is vetted for the round only if the round's list has its `(index, DK fingerprint)`.
  - The live directory can hide a vetted entry, or its X-sourced picture and name, through its flags, but it can never add one (§4.6).
  - If a vetted delegate's live handle differs from the list's handle, show the live handle with "renamed from @old" (§4.6 Renames).
  - Handles are pinned per past delegation, with a warning on change.
- **Search.** Exact search matches the live directory's current handles and fingerprints locally, after a refresh (§4.6 Freshness).
- **Pre-proving check.** Immediately before proving, Vizor's participation reader proves the chosen delegates' entries (§4.4 Chain verification). The wallet requires three things, so that a relabelled handle, index or key is caught:
  - each proven entry's DK equals the directory's `dk`;
  - its `subject_commit` equals `H(provider, id, salt)` from the directory entry;
  - the entry is ACTIVE and not suspended.

  An entry re-keyed after the round was created is also refused, because no key can ballot for it this round (§4.2 Ballot op). This is the one registry check, made once at confirm.
  - Bindings never change, because the stored entry is the effective entry. There is therefore no post-commit re-check and no `ProxyBlocked{DelegateChanged}` state.
  - A delegate revoked between confirm and inclusion only leaves that DC uncounted.
  - This proof request names the chosen delegates' registry keys, so the RPC server learns which delegates the wallet is about to use, as helpers learn each DC's delegate (§5); Tor hides the IP.
- **Refusals.** Refuse delegates that are not ACTIVE, are suspended, or carry a directory flag such as retired, impersonation or under review.
- **Gov nullifiers.** `proxy::availability` also requires proven non-membership of the account's gov nullifiers. If they are spent and no local state exists, report "used from another device" (LIV-8).

**Gating (decision 38).** There is no `vote_protocol` or `auth_version` bump. `proxy::availability` requires:
- `WalletCapabilities.proxy_delegation = ["v1"]`;
- chain capabilities with `proxy_delegation_version = 1` and four circuit fingerprints that match the compiled ones;
- the round's `proxy_delegation.enabled` (field 31);
- `proxy_dc_paused = false` in the chain params;
- a verified proxy entry for the round in the dynamic config, because it carries the vetted list and the directory key;
- authenticated round timing (Last-moment parity, above).

Vizor checks these before preview and again immediately before commit. Gates control only preview and commit of **new** allocations; committed work and share delivery always run (LIV-12). Vizor calls `proxy::availability` and never parses these fields.
- **Off switch (D9).** The chain pause is the one off switch. While it is set, `proxy::availability` refuses new allocations, Vizor hides the delegation entry points and shows "Paused", and the chain rejects any committed batch with the VAN unspent. That batch then retries if the pause lifts before `vote_end_time`, or is released (Release path, above).
- **The proxy entry is content, not a switch.** It is published with the round and stays for the round. Removing it would also stop new delegations in Vizor, but it is not an operational lever; the chain pause is.
- **No remote flag.** There is no remote JSON flag for proxy delegation. [code] Vizor's only remote flag today, `swap-enabled.json` (`lib/src/core/config/swap_remote_enable_config.dart`), is an unsigned map from app version to bool.
- **A lying RPC server** can hide the pause, but then the chain rejects the batch. It can also hide the feature, which is a liveness failure like any RPC censorship.

**Delegate APIs.**
- **Phrase and key.** Create and restore the 12-word phrase, and derive the delegate key, one key per phrase (§4.6).
- **Registration.** The marker text, the resolve and attest client, and encoders for the five digests. Registering again from the same account uses the same flow and re-keys the entry.
- **Ballot builder.** It requires one counted option for every proposal of the round and rejects an incomplete ballot before signing.
- **Preflight.** It fetches the delegate's stored ballot from the whole-round list. If one exists, it reports it and never submits a different one.
- **Submission.** It signs, submits once, and retries only the identical stored ballot. `ErrDelegateBallotExists` with an identical stored ballot counts as success.
- **Ops.** `register` (new or again) and `revoke`.

### 4.5 Vizor

**Screens.** Keep the PD (delegator) and DG (delegate) inventory, with changes. Rev 6 removes PD-10 (combined delegate-and-vote review, decision 23), PD-11 (hidden-delegates list; an "Unhide all" row replaces it), PD-12 (in-app report sheet; a report link replaces it) and PD-13 (round picker; the directory opens from a round).
- PD-1 offers one delegation per round; after commit it shows the locked allocation.
- **PD-2 (directory)**
  - Browse shows only vetted delegates, in weighted-random order, including in picker mode. There is no popularity sort and no other browse list.
  - Search matches only an exact X handle, GitHub handle or key fingerprint, and refreshes the directory first (§4.6 Freshness). If the refresh fails, search shows an "out of date, search unavailable" state.
  - Unvetted results show the handle, fingerprint, an identicon and a "Not reviewed" label, with no profile picture, display name or statement. Lookalike warnings apply to search results.
  - Entries registered after the round was created show "new this round".
- PD-3 (profile) shows a vetted delegate's name, statement and picture from the round's vetted list, plus the handle and fingerprint. It shows no ZEC totals and no delegation counts. A ballot track record waits for v1.1 (decision 42).
- PD-4 shows "Voting ends {time}" and keeps the 24 h "delegates may not have time to vote" notice. `tooLate` is the round-closed state votes use (`now ≥ vote_end`).
- PD-5 has the finality checkbox (one delegation per round, final), the "Counts only with a ballot" line next to it, an amount-naming CTA and the list of covered proposals. The delegate picker stops at 8. After a `KeepForSelf` delegation, Vizor offers "Vote with the ZEC you kept", which opens the ordinary vote flow.
- PD-6 completes at "Handed off". For a DC whose shares were planned with `submit_at = 0`, it waits for the confirmed reveal of share 0, as a vote's immediate share does. At `vote_end` it fails with the vote path's expired-share message, and after TALLYING the DC shows "Not counted".
- **PD-7 (status)**
  - It is a plain view of the §4.4 status model (decision 42), for example "Delegated 12.5 ZEC to @alice. Handed off."
  - It shows @alice's published ballot: "No ballot yet" until one lands. After close it shows "Counted" with the ballot, or "No ballot. Not counted this round."
  - It keeps the "Counts only with a ballot" line.
  - It has no reveal counter, no "on track" state, no per-proposal status and no "verified" marks.
- PD-8 (results) shows per-option totals as today, which include delegated weight, with no delegate names and no direct/delegated split, because pool totals are not published.
- **Delegate screens**
  - DG-2 becomes phrase create and confirm. DG-12 (software account required) is removed.
  - DG-8 (dashboard) shows the delegate's own approximate delegation count, live, and whether this round's ballot is on chain. It shows no ZEC total.
  - DG-9 becomes the ballot screen and DG-10 its confirm sheet (Delegate mode, below). DG-9 drops the side-by-side column of the delegate's own private votes.
  - DG-11 offers only "Retire" (revoke), and DG-2 offers "Register again" for a new phrase on the same account.

**Job.**
- `VotingSubmissionJobNotifier` gains `kind {ballot, proxy}`, `ProxyJobStage` and `terminalReason`.
- The proxy allocation is recorded before the hardware/software branch, so Keystone and Ledger users who only delegate are still asked to sign ZKP1.
- Cancel is allowed only before the first broadcast.
- COMPLETE requires definite helper acceptance of every DC share and, for DCs whose shares were planned with `submit_at = 0`, the confirmed reveal of share 0. If `vote_end` passes first, the job fails like an unconfirmed immediate vote share (`voting_submission_job_provider.dart:1469-1497` [code]). Without this, a late proxy-only DC would report success, because `hasConfirmedImmediateShare` returns true when there is no designated immediate share (`voting_resume_plan.dart:16-19` [code]).
- If the pre-commit availability check sees the chain pause (§4.4 Gating), commit fails before any proof with the "Paused" copy and nothing is recorded.
- If the chain rejects a committed batch as paused (D9), the job shows the "Paused" copy, retries if the pause lifts before `vote_end`, and offers "Vote with this ZEC instead", which runs the release path (§4.4).
- A job already past commit otherwise continues to completion (LIV-12).

**Off switch.** Vizor has no switch of its own: it reads the chain pause, `proxy_dc_paused`, before preview and before every commit (§4.4 Gating, decision 38). While it is set, Vizor hides the delegation entry points and shows "Paused"; share delivery and the status view keep running (LIV-12). Its round-level effects are in §8.3.

**Chain verification.** Vizor's participation reader (`rust/src/wallet/voting/participation.rs` [code]) verifies the chain facts that safety relies on: the pre-proving registry check of the chosen entries, and gov-nullifier non-membership (LIV-8). It is extended with the registry keys [new], using zcash_voting's key derivations and expected values (§4.4 Chain verification). Ballots and share status are displayed as reported, without proofs (D1).

**Delegate mode.**
- The phrase root sits in app-level secure storage behind re-auth.
- The delegate key is stored with **per-use user presence** (Keychain/Keystore access control), not session unlock (IDN-7).
- **Ballot screen (DG-9).** It lists every question of the round with its options and enables Submit only when every question has an answer. There is no per-question submission and no per-question abstain; a delegate who wants to sit out submits no ballot. A round's real "Abstain" option, if it has one (Q2), appears as an ordinary option. A banner states the rule (copy "Ballot rules").
- **Confirm and submit (DG-10).** A confirm sheet lists every choice; confirming needs re-auth; Rust then signs one ballot op with the delegate key and Vizor submits it once. After inclusion the ballot is read-only. A retry resubmits the identical stored ballot; if a ballot is already on chain, DG-10 shows it ("You've already submitted your ballot for this round.").
- **Retire (DG-11).** Revokes the entry after re-auth and a confirm ("Retire"). Registering again from the same account reactivates it. A delegate who lost the key registers again instead (§4.6 Re-registration and key loss).
- Tor is **default-on** in delegate mode for ballot submission and for the delegate account's own vote and share traffic. If Tor is off, a blocking prompt appears before the ballot or the first private cast (PRV-7).
- The ballot deadline is `vote_end_time`. DG-8 shows a reminder while no ballot is on chain, a warning in the final 5 minutes, and a blocking confirm in the final 60 s saying that a ballot landing after close is rejected and ZEC delegated to the delegate is then not counted this round (the transition block rejects ballots, `keeper_voting.go:336-338`, `module.go:491-497` [code]; identical retries cannot help after close).

**Images and networking.**
- Avatars exist only for vetted delegates and come from the round's picture pack, built by the curator tool: WebP at exactly 128 px.
- Each image's sha256 is checked against the round's vetted list, the image is decoded in Rust with `image-webp`, and drawn with `decodeImageFromPixels`. The memory-safe decoder stays even though the curator tool re-encodes every image, so that a malformed image that passes the tool and review is not a path to a platform-decoder exploit.
- `Image.network`, `Image.memory` with encoded bytes, and `instantiateImageCodec` on network bytes are banned by a grep test (CMP-14).
- The two directory mirror hosts and the verifier host get new network-role entries [new] in the Tor-aware route table that Vizor's voting README keeps (`rust/src/wallet/voting/README.md:322-337` [code]), plus the matching service tests, as that README requires for every new network role.

**No deep links in v1 (decision 41).** Delegates share their handle or fingerprint, and delegators find them with exact search. Vizor's link allowlist and the deeplink server are unchanged.

**Directory freshness.**
- Fail closed for directory-only blocking flags (retired, impersonation, lookalike, "under review").
- Search always refreshes first and is unavailable if both mirrors fail. Commit requires a directory verified within the last 10 minutes, refreshing first only if the cached one is older. If both mirrors fail, browse still shows the round's vetted list with an "out of date" banner, and commit stays unavailable until a refresh succeeds (LIV-6).

**Copy rules** (sentence case, no em dashes). These replace the v1 strings:

| Key | New string |
|---|---|
| C4 | "Your delegate can't see who you are, and your amount isn't published. Results show only totals per option, never your delegate's total, and your ZEC follows your delegate's public ballot. If nobody voted directly for an option your delegate picked, results can sometimes reveal your delegate's total, and with it your amount if you're their only delegator. Results never reveal who you are." |
| C7 | "Helpers add your delegation to the count before voting ends, at random times if you delegate early, or right away in the final hours. Vote servers can see which delegate you chose, but not who you are." |
| C8 | "Keep Vizor installed until your delegation shows Handed off. If you reinstall or restore your wallet before then, it may not be counted, and you can vote the ZEC you kept only from this install." |
| C9 | "Your delegate votes for you on every proposal in this round. Delegate some or all of your voting power to people you trust." |
| Finality | "You can delegate once per round. After you confirm, you can't change it." |
| Counts only with a ballot (PD-5, PD-7) | "Your delegated ZEC counts only if your delegate submits a complete ballot before voting ends. If they don't, it isn't counted this round." |
| No ballot (PD-7, after close) | "@{handle} didn't submit a ballot before voting ended, so your delegated ZEC wasn't counted this round." |
| Ballot rules (DG-1, DG-9) | "You vote once per round, on every question, for everyone who delegated to you. You can't change your ballot after you submit it. If you don't submit it before voting ends, ZEC delegated to you isn't counted this round." |
| Ballot confirm (DG-10) | "Submit your ballot? It covers all {n} questions for everyone who delegated to you. It's public, and you can't change it." |
| Voting ends | "You can delegate until voting ends {time}, just like voting." |
| Not counted | "Voting ended before a helper could count this delegation. It was not counted." |
| Not reviewed | "Not reviewed. Vizor hasn't checked this delegate. Compare the fingerprint with the one in their post before you delegate." |
| New this round | "New this round. Registered after voting started." |
| Paused | "Delegating is paused for this round. Delegations already made aren't affected, and you can still vote." |
| DG-1 additions | "Your public votes stay linked to your X or GitHub account permanently, even if you delete your post or account." / "An approximate count of delegations to you is public on the voting chain. Vizor shows it only on your dashboard." / "Your private votes are hidden from the public. Vote servers can link them to you unless Tor is on." |
| Not browsable (DG-1 and dashboard, until vetted) | "You won't appear in the browse list unless curators review you. Share your handle or fingerprint so people can find you." |
| Retire (DG-11) | "Retire as a delegate? If you haven't submitted this round's ballot, ZEC delegated to you this round won't be counted. To come back, register again from the same account." |
| Key changed (profiles, search, status) | "Key changed on {date}. Compare the new fingerprint with their latest post." During the round of the change: "Changed keys. Can vote again next round." |
| Register again (DG-2) | "Lost your phrase? Create a new one and post its marker from the same account. Your delegate number stays the same, and your new key can vote from the next round. You can do this once every 14 days." |
| Kept ZEC | "Vote with the ZEC you kept from this device before voting ends." |

Other copy fixes:
- The ZKP1 copy becomes "voting authorization" everywhere, including the Ledger strings [new]. Today it reads "voting delegation" (`lib/src/features/voting/screens/voting_status_screen.dart:1262-1263, 1289, 1329, 1345` [code]), which would collide with proxy delegation.
- Delegation counts appear only on the delegate's own dashboard, always as approximate ("at least N", N = `ceil(R/16)` for R revealed shares; the true count lies between `ceil(R/16)` and R, §5 (iv)), and are never a sort key.

**UGC (IDN-13, CMP-22).**
- Profiles and search results have a "Report" link that opens Valar's web form or an email address. A local "Hide delegate" option, with an "Unhide all" row in settings. The web form or email exposes the reporter's IP or address to Valar, unlike an anonymous in-app POST.
- Display name, statement and avatar exist only for vetted delegates and come only from the round's vetted list. Valar and Vizor reviewers see every change in the list's pull request. Unvetted delegates show only a handle, fingerprint and identicon.
- Delegate onboarding requires accepting the content terms.
- App Privacy labels; reviewer notes with a stage build, a demo delegate and an open test round, explaining that user-generated profiles are limited to the curated vetted list and that other delegates appear only by exact search, without pictures or text.

### 4.6 Identity, verifier and directory

**Delegate key phrase (O4, IDN-14; 12 words since rev 8).**
- **Format.** 12 words from the BIP-39 English list, encoding 128-bit entropy `E` plus a 4-bit check `c`, the first 4 bits of `BLAKE2b-256(personal "ZcashVoteDlgPh01", E)`.
- **Generation** retries until the standard BIP-39 checksum (the first 4 bits of `SHA-256(E)`) differs from `c`, so **no delegate phrase is a valid BIP-39 mnemonic**. About 1 draw in 16 is retried.
- **Restore** rejects any valid BIP-39 mnemonic with "This looks like a wallet recovery phrase. Never enter it here."
- **Derivation.** One delegate key per phrase (decision 10):

  ```
  seed  = BLAKE2b-512(personal "ZcashVoteDlgSd01", E)
  dk_sk = BLAKE2b-256(key = seed, personal "ZcashVoteDK_v1__", "")
  ```

- **Export and import.** The 12 words are the backup. Vizor shows them once at setup (DG-2), and again after re-auth. Restoring them on another device derives the same key and finds its entry by fingerprint; there is no index scan.
- **A lost phrase** is replaced by registering again from the same X or GitHub account with a new phrase (Re-registration, below). A key is never reused.
- The format goes into audit tranche 2.
- A CLI with bring-your-own-key comes in Phase 2.

**Keys and display.**
- The public key is bech32m `zvdk1…`.
- **Fingerprint** = the first 16 data characters of the `zvdk1` string (80 bits), grouped `xxxx-xxxx-xxxx-xxxx`. It can be compared by eye with the X post.
- Canonical display: `#17 · qqqs-yqcy-q5rq-wzqf`.
- The identicon is decoration, not verification.

**Proofs** (decisions 43 and 46).
- The marker line is `zcash-vote-delegate v1 zvdk1…`.
- **X: a free check first, then one paid call.**
  1. **Free.** oEmbed (`publish.x.com/oembed`) is X's free, published embed endpoint. The verifier API calls it on the post link and checks three things:
     - the post exists;
     - its whole text equals the marker template for this key, modulo whitespace (IDN-5);
     - the author's current handle and display name.

     oEmbed looks the post up by its id, so the handle in the link does not matter [measured: `x.com/somebodyelse/status/20` returns `@jack`; a missing post returns 404]. An attempt that fails here costs nothing.
  2. **Paid, made by the signer.** One post lookup (`GET /2/tweets/:id` with `author_id`, `referenced_tweets` and the author's `created_at`), about $0.015. In one call it ties the post to the account's permanent numeric id, confirms an original post (no reply, quote or repost), and gives the account's age.
  - Show the full post text on the resolve screen.
  - Bind the numeric user id.
- **GitHub.** A gist file; bind `owner.id`. GitHub's API is free within rate limits.
- No DNS or `.well-known` proofs in v1. Organizations register through a representative's X or GitHub account; DNS can return in Phase 2.
- The subject is committed on chain only as a salted `subject_commit`. The delegate key signs the binding back (REGISTER), so the binding runs both ways.
- **The registration post stays up.** The refresher reads it every day through oEmbed (Retention and deletion, below). Deleting it delists the delegate until they register again.

**Attestation.**
- Each attestation carries `not_before` and `expires_at` (at most 72 h later), signed over ATTEST. ATTEST binds:
  - `chain_id`;
  - `delegate_index` (0 for a new entry, or the entry being re-keyed);
  - the DK, the provider and `subject_commit`;
  - `not_before` and `expires_at` (§4.2).
- The chain counts a threshold of distinct current verifiers from the params verifier set, deduplicated by `verifier_id` and pubkey. There is no admin bypass.
- Launch threshold: 1, the Valar online key.

**Attest-signer hardening (IDN-2).**
- The signer runs on its own host with a non-exportable key (KMS or HSM).
- It receives structured fields and **recomputes** ATTEST itself.
- For **every** REGISTER, before signing, it does two things itself (decision 21):
  - re-checks the post through oEmbed;
  - makes the one paid lookup with **its own** X credentials, or for GitHub reads the gist itself.

  A compromised verifier API therefore cannot feed it a post or an account id.
- **Daily spend cap (decision 46).** The signer refuses paid calls beyond `max_daily_x_spend`, and pages when it does. At launch the cap is about $5 a day, roughly 330 registrations. Under attack, new registrations pause until the next UTC day, and spend never passes the cap.
- It enforces per-account and per-IP limits, and matches accounts by numeric id against its own log plus its chain view.
- Signer-log reconciliation stays, for key theft. The publisher reads through a read-only database role.
- There is no separate re-verifier service; the refresher's daily oEmbed read covers every registration post. A compromised verifier API can delay or refuse registrations, or spend up to the daily cap. It cannot get an attestation for a post that does not exist, make anyone vetted or redirect a delegation (§5 Identity attacks).

**Verifier policy.**
- **One entry per account.** The subject is the numeric account id (X user id or GitHub `owner.id`).
- **Registering.** A REGISTER from an account without an entry creates one. A REGISTER from an account that already has one re-keys it, whatever its status (owner rev 8): there is no support process and no old-key sign-off.
- **Re-registration cooldown (owner rev 8), checked before any paid call.** The verifier tracks every entry's current handle (Retention and deletion, below), so it can apply limits right after the free oEmbed check, by the post author's handle:
  - An account whose entry was registered or re-keyed in the last 14 days (`reregister_cooldown`) cannot register again until the 14 days pass.
  - Each handle gets at most one failed paid check per day, so an honest mistake such as replying instead of posting retries the next day.
  - A handle renamed since the last daily refresh is not recognized before the paid call. The paid lookup then matches the account id, and the cooldown still refuses it, so a rename can cost at most one paid call. The daily cap bounds the total.
- Per-provider quotas, so GitHub cannot starve X.
- An account-age gate, using `created_at` from the paid lookup, and per-account and per-IP rate caps.
- Unused issuances count against the IP and its /24.

**Re-registration and key loss** (decision 10, owner rev 8). The X or GitHub account is the delegate's master identity.
- **How a key is replaced.** To replace a lost, leaked or old key, the delegate:
  - creates a new 12-word phrase;
  - posts a new marker post from the same account;
  - registers again.

  The verifier sees the same numeric account id and attests a re-key of the existing entry: same index, new key (§4.2 0x0B).
- **Cooldown.** An account can register again only 14 days after its last registration or re-key (Verifier policy, above).
  - Trade-off: a hijacker who re-keys first also locks the owner out for up to 14 days after they regain the account.
  - In that window the hijacker's key can vote only in rounds created after its re-key, and Vizor shows "Key changed" with the date.
  - Curators see the fingerprint change in the next list's PR and can leave that delegate out.
- **Timing rule.**
  - The old key stops working at once.
  - The new key can submit ballots only in rounds created after the re-key. So a hijacker who takes over the account mid-round cannot cast the ballot for pools formed before; at most, that delegate has no ballot this round.
  - An honest re-key loses nothing more, because a lost key could not vote anyway.
- **In Vizor.** It shows "Key changed" with the new fingerprint. It does not offer a delegate whose key changed during the current round ("Changed keys. Can vote again next round.").
- **Retire is reversible.** A REVOKED entry is reactivated the same way, by registering again.
- **Suspension is separate.** A coordinator suspension survives a re-key.
- **What this accepts.** Whoever controls the X or GitHub account controls the delegate, until the owner regains the account and registers again.

**Vetted list, pinned per round** (decision 39; owner rev 7 and rev 8). The browse list for a round is one file, pinned by the round's signed config entry. It holds no profile content.
- **Where it lives.**
  - The file sits at `delegates/<sha256>.json` beside the dynamic config, served from `voting.valargroup.dev` with the config's existing GitHub and Cloudflare fallback.
  - Its header is `{version, round_id, created_at, reserved_names}`, where `reserved_names` holds seeded ecosystem names only.
  - Each delegate is `{index, fingerprint, provider, handle_hash, name_hash, statement_hash, avatar_sha256}`.
  - The text hashes are salted, and each salt lives with its content in Valar's store. Once the content and salt are deleted, the hash left in git reveals nothing (decision 48).
- **Where the content lives.**
  - Pictures, display names, statements and current handles sit in Valar's own store: the live directory and its avatar files, on the directory's two mirrors.
  - There they can be updated, or deleted within 24 h, for X's keep-current and deletion rules and for a delegate's removal request.
  - Git history never holds them. The identity research found that storing X content in git history conflicts with those rules (`archive/research-dossiers/identity_research.summary.md`).
- **How it is pinned.**
  - The round's entry in `extensions.proxy_delegation_v1.rounds[round_id]` carries the list's sha256 and the live directory's signing keys for that round.
  - That entry is signed by the existing `trusted_keys`, the admin keys that already sign every round's `ea_pk` (§4.7).
  - So a round's list cannot be swapped or rolled back, and every wallet sees the same list all round.
- **Display.**
  - Vizor shows a vetted delegate's picture, name, statement and handle only when each matches the round's pinned hash.
  - A picture that is missing or does not match shows the identicon. A handle that does not match shows the live handle with "renamed from @old", taken from the live directory.
- **Approval.**
  - The list for each round is a pull request in the config repo.
  - It merges only with approvals from a Valar reviewer and a Vizor reviewer, enforced by CODEOWNERS and branch protection on `delegates/` and the extension.
  - The curator tool links previews of the new pictures, names and statements on Valar's store, so reviewers see what they approve.
  - The admin signs the round's proxy entry when it adds the round.
  - The admin key is already the root of trust for every round's election key, so pinning the list with it adds no new trust (owner rev 7).
- **Changes mid-round.**
  - Nobody is added; new vetted delegates join from the next round.
  - Removal works at once: deleting the content, a directory flag, or a coordinator suspension.

**Live directory** (decision 39). One `directory.json` at a fixed path on two mirrors: the valargroup origin, and the `functions.vizor.cash` pull-through with a short CDN cache time.
- **Signing.**
  - It is signed by an online key named in the current round's proxy entry, so rotating that key needs a re-signed round entry, not a wallet release; there is no offline key or certificate.
  - It carries `seq`, `issued_at`, `expires_at` (at most 24 h) and `chain_id`.
- **Registry entries.**
  - Every registered delegate appears as `{index, dk, status, rekeyed_at, provider, subject id, salt, current handle, flags}`, so exact search works.
  - `status` is a display hint; the proven check at confirm is authoritative.
  - Other wallets may consume these once counsel clears handle redistribution (Q10).
- **Vetted content.**
  - For vetted delegates, it adds the display name, statement and picture path with their salts.
  - It also carries the reserved handles: those of renamed or retired vetted delegates, kept for 12 months.
- **Avatars.**
  - Pictures sit at `avatars/<sha256>.webp` on the same mirrors.
  - Vizor downloads the round's vetted pictures together as one bundle, regenerated when a picture is deleted, so a server cannot tell which delegate a user looks at. Each picture is checked against its own pinned hash.
- **Flag rule.** Flags can hide a vetted entry, its picture or its name, failing closed, but can never add one.
- **Fetching.** A conditional HTTP GET replaces an index document.
- **Removed (rev 6-8).**
  - The hash chain, cross-mirror equivocation checks, a third mirror, the separate profiles doc and sharded packs.
  - The curator-signed vetted section and the offline-key certificate (rev 7).
  - Profile content in git (rev 8).

  A directory-key thief can publish a higher `seq` anyway, so the protections that matter are the round-pinned hashes and the proven pre-proving check (§4.4 Registry client).

**Freshness and mid-round registration (D10).**
- **Registration.** Delegates can register at any time while the params verifier set is non-empty (§4.2 Coordinator payloads). The chain assigns the next index immediately (`0x1A 04`), and the delegate can submit a ballot in rounds already running (§4.2, 0x0B ballot op).
- **The live directory is not frozen per round.**
  - The publisher lists the chain registry (key-paged `delegates`) and regenerates the signed directory within about 10 minutes of a chain registry change.
  - In Vizor, a search triggers a directory refresh first. Vizor downloads the whole directory and searches it locally, so nobody learns who a user looked up.
- **Before proving,** Vizor proves the chosen delegates' chain entries with its participation reader (§4.4 Registry client, §4.4 Chain verification).
- **Pool shares.** The chain accepts pool shares for any index that exists at reveal time (§4.2, 0x04).
- **New this round.**
  - Entries whose index is at least the round's `next_delegate_index_at_creation` show "new this round".
  - A mid-round registrant can be found by exact search at once, and can join the vetted list from the next round.

**Renames** (between rounds; rev 7 and rev 8).
- Registration binds the numeric account id, never the handle, so a rename changes nothing on chain. The delegate keeps the same index, key and fingerprint.
- The refresher's daily oEmbed read of the registration post returns the new handle, and exact search follows it within about a day.
- For a vetted delegate:
  - The next round's list pins the new handle's hash.
  - Mid-round, Vizor shows the live handle with "renamed from @old", so even a stolen directory key cannot silently relabel a vetted delegate.
- The old handle joins the live directory's reserved handles for 12 months, so someone who takes it on X and registers gets a lookalike warning.

**Retention and deletion** (PRV-8, IDN-12).
- **X data held:**
  - numeric ids: Valar's log, and salted on chain;
  - current handles and display names: the live directory;
  - vetted delegates' profile pictures: Valar's store.

  Statements are the delegate's own text, supplied under the content terms.
- **Refresh (decision 46).**
  - Every day, the refresher reads each registration post through oEmbed. This is free and a published interface. It returns the current handle and display name.
  - A 404 or protected response means the post or account is gone. The handle, name and picture then leave Valar's store within 24 h, and the delegate is delisted until they register again.
  - Network errors and 5xx responses are retried, and never delist.
  - Pictures are refreshed before each round by the curator tool (Curation, below).
- **Objects.** Only the current directory and avatar files exist, so a purge is a deletion plus a CDN purge on both mirrors. The round lists in git hold only hashes and can stay.
- **Uniqueness.** Keep a keyed-hash tombstone of `(provider, subject_id)` for uniqueness checks.
- **Withdrawn claim.** The "crypto-shredding makes the commit unlinkable" claim is withdrawn: published salts mean attribution is permanent, and DG-1 says so.

**Curation.**
- **Vetting.**
  - Curators are Valar and Vizor reviewers. They review the proof, account history and lookalikes, and confirm the fingerprint out of band for every vetted entry (IDN-11).
  - They collect a statement from the delegate (at most 280 characters, no links) under the content terms.
  - The list has no size cap, and its git history is the public log of additions and removals.
- **Before each round, a curator tool:**
  1. reads each vetted delegate's current handle and display name from the live directory;
  2. fetches each vetted X delegate's current profile picture from their public X profile page (decision 47), and GitHub pictures from `avatars.githubusercontent.com` by account id;
  3. decodes each picture with a memory-safe decoder, re-encodes it as 128 px WebP, and uploads it to Valar's store;
  4. writes the hashes-only list and opens the pull request with preview links.

  There is no admin UI and no curator key.
- **Picture fetch guardrails (decision 47).**
  - It runs once per round, for vetted delegates only, at a low rate.
  - It reads only the page's public profile-picture link.
  - If a page does not parse, that delegate shows the identicon for the round.
  - X's verified badge on the page is not shown in Vizor, because the blue check is a paid subscription, not identity verification.
  - If X blocks the fetch, the fallback is one paid user lookup per vetted delegate (about $0.01 each) or identicons.
- **Exact search.** Every other registered, unflagged delegate is reachable only by exact (case-insensitive) search on X handle, GitHub handle or key fingerprint. Results show the handle, fingerprint, an identicon and a "Not reviewed" label, with no profile picture, display name or statement.
- **Lookalike check (decision 43).**
  - It is one wallet function in `zcash_vote_delegate`: lowercase, map `0→o`, `1` and `i→l`, `rn→m`, `vv→w`, `cl→d`, drop `_` and `-`, then allow Damerau distance 1.
  - It runs against vetted handles, the list's `reserved_names` and the live directory's reserved handles.
  - It warns on vetted-list entries and on search results.
- **Re-keyed entries** keep their index, and the directory shows "key changed" with the date. There are no successor links.
- **Reports** go to a Valar web form or email inbox, with a 24 h target for impersonation.

**Ops.**
- **Hosting.** valargroup DigitalOcean hosting. The signer key is in KMS or an HSM on a dedicated host (no YubiHSM on DO).
- **Per round.**
  - Run the curator tool.
  - Merge the reviewed list.
  - Sign the round's proxy entry together with the round entry (§8.3).
- **Monitoring:**
  - daily X spend against the cap, and X API errors;
  - oEmbed failure and delisting rates;
  - picture-fetch parse failures;
  - directory `seq` age and regeneration lag, within about 10 minutes of a chain registry change;
  - a refresher mass-failure breaker;
  - signer-log mismatches.
- **Playbooks:**
  - verifier-key compromise: remove the key from params with 2 approvals, then flag every registration since the suspected time;
  - X outage: GitHub continues, the refresher retries without delisting, and registrations wait;
  - X blocking the picture fetch: identicons, or the paid fallback;
  - directory-key compromise: re-sign the round's proxy entry with a new directory key.

**Legal (CMP-21).** Before the X provider reaches production, counsel signs off on:
- the X Developer Agreement for the registration lookups: numeric ids, handles, and the political-data rule for public ballots linked to X ids;
- oEmbed for registration checks and the daily refresh;
- the owner-accepted risk of fetching public X profile pages for pictures (decision 47), which X's terms prohibit without written consent;
- privacy notices and retention;
- the content terms and consent for delegate statements;
- whether any scanning duty applies to a curated, human-reviewed image set;
- the vetted-list disclaimer and vote-market policy.

### 4.7 Config repo and deeplink server

**Config repo** (decision 39, owner rev 7).
- **No new static pin.** Vizor uses the existing hash-pinned static config and its `trusted_keys` (token-holder-voting-config README, Trust Model [code]).
- **Dynamic config [new].** A top-level `extensions.proxy_delegation_v1` object. Today's dynamic config has no `extensions` key [code]. zcash_voting's config structs and Zodl's parser ignore unknown fields, so old wallets are unaffected.

  ```json
  "extensions": {
    "proxy_delegation_v1": {
      "directory_urls": ["https://<origin>/directory.json", "https://<pull-through>/directory.json"],
      "verifier_api_urls": ["https://<verifier>"],
      "rounds": {
        "<round_id>": {
          "delegate_list_sha256": "<hex>",
          "directory_keys": ["<base64 Ed25519 pubkey>"],
          "signatures": [{ "key_id": "<id>", "alg": "ed25519", "sig": "<base64>" }]
        }
      }
    }
  }
  ```

  - **Signature.** Each round entry is signed by `trusted_keys` over:

    ```
    ProxyRoundPayloadV1 = "zcash-shielded-vote:proxy-round:v1" || round_id || delegate_list_sha256
                          || u8 n || n × directory_key
    ```

    Its domain keeps it apart from the round's own signatures. auth_version 1 signs exactly the 32-byte `ea_pk`, and auth_version 2 signs under `zcash-shielded-vote:round-auth:v2` (`zcash_voting/src/pir.rs:915` [code]). So the round entry, its `auth_version` and Zodl are untouched.
  - **URLs** are unsigned wrapper fields, like `vote_servers`, because the directory and the verifier are authenticated by their own signatures.
- **Signing.** The vote manager signs the proxy entry with the same Keplr-derived admin key, in the same "Sign config entry" step of the vote-sdk admin UI (or the offline `voting-config sign` CLI) that signs the round. The PR carries both payload hashes for reviewers to cross-check.
- **Files.** `prod/delegates/<sha256>.json` and `<sha256>.pack`, and the same under `stage/`. They are immutable and kept at least until their round is finalized.
- **Review.** CODEOWNERS and branch protection require a Valar approval and a Vizor approval for `*/delegates/` and the extension.
- **CI checks:**
  - the extension's shape, and its signatures against `trusted_keys`;
  - that every pinned list and pack exists with the right hash;
  - that each list's `round_id` matches its entry.
- `supported_versions` is never touched, and there is no `registry_snapshot_sha256`.

**Deeplink server.** No change in v1 (decision 41).

---

## 5. Privacy and threat model

**Hidden** (with these exact claims):

| Property | From | Caveats |
|---|---|---|
| Delegator identity | Everyone | VAN anonymity, as for votes. Timing and IP linkage are the same as voting unless Tor is on. |
| Which delegates a delegator chose | The public and the delegate, at DC time | **Not hidden from helpers**: each share fans out to `ceil(N/2)` vote servers, about every server, which learn `d`, the DC leaf and IP, as they learn each vote's option today (the helper payload carries `proposal_id` and `vote_decision`, `internal/helper/types.go:121-131` [code]). Helpers run inside validator nodes, so the EA key holders' own helpers learn `d` (§5 Validator trust). The RPC server that serves the pre-proving registry check also learns the chosen delegates, because the proof request names their registry keys (§4.4 Registry client). The DC count per tx is public. For delegates with very few delegators, the 16 reveal times can narrow which DC tx it was: [measured] median candidate sets of 4, 8, 29 and 112 DC txs at 0.1, 0.25, 1 and 4 DC txs per hour, unique in 17%, 5%, 1% and 0% of trials (`prototypes/privacy/reveal_linking.out`). A DC made in the last-moment window is revealed within seconds of its tx, which publicly links the tx to its delegates, exactly as a late vote's reveal links its tx to its option; a batch with k late DCs links its k delegates together. |
| Amount per delegation | The public, helpers and the delegate | Only per-option totals are decrypted, once, at close. Every counted pool is in every proposal's total, so comparing totals across proposals reveals nothing. But an option that no direct voter picked equals the sum of the pools whose delegates chose it, and several such options can combine to determine a pool (Pool solvability, below). A delegation's amount is exposed when its pool is determined and holds only that delegator's weight, which a small reveal count can suggest. A direct vote is exposed the same way when it is the only direct vote on an option, but a pool appears on every proposal, so its exposure is somewhat more likely, mostly in small rounds. Validator collusion is covered under Validator trust. |
| Per-delegate pool totals | The public and the delegate | Never shown or decrypted on their own, but computable from published per-option totals when zero-direct options determine them (Pool solvability). Approximate counts are public (below), but counts alone reveal no amount. A coalition of at least t validators can decrypt any pool. |
| A delegator's total and kept weight | The public | Kept weight is voted privately like any vote. Splitting across several delegates means any single exposure reveals only that slice. |
| Delegator's choices from viewing-key holders | UFVK holders | DC secrets derive from the random per-round hotkey, never from viewing keys (decision 14). |

**Public:** delegate ballots (one complete ballot per delegate per round, visible once submitted); that an anonymous batch contains k DCs; per-delegate pool reveal counts, live during the round (16 per DC made before the last-moment window, 1 per DC made inside it, so delegation counts are approximate; Vizor shows them only on the delegate's own dashboard), which can suggest that a pool is one person but reveal no amount; per-option totals, decrypted once at close, which include counted pools; per-option direct share counts (VoteSummary `BallotCount`, which excludes pool shares); delegate registry entries, and every ballot, from which anyone can build a delegate's track record.

**Pool solvability [measured].** With complete ballots, each counted pool is the same unknown on every proposal. A direct voter's weight is a fresh unknown on each proposal, and no public data links it across proposals. Published per-option totals are linear equations in the pool totals, and two channels could make them exact:
- **Channel A, options with no direct votes (remains).** Every option bucket with zero direct shares is publicly identifiable, because `BallotCount` counts only direct shares (`keeper_tally.go:197-206` [code]). Its decrypted total equals the sum of the pools whose delegates chose it. Such options combine, so a pool can be solved even when it is never alone on an option: three zero-direct buckets holding pools `{1, 2}`, `{2, 3}` and `{1, 3}` (pair sums) solve all three.
- **Channel B, comparing per-proposal totals (closed).** If a pool counted on some proposals and not others, then whenever the same direct voters vote on two proposals, the difference between their overall totals would equal the difference between the pools counted on each, isolating pools whose delegates sat out a proposal. Complete ballots (D2, decision 34) put every counted pool on every proposal, so those differences contain no pool term, and the one-final-tally rule (decision 37) keeps it that way.

A solved pool that holds one delegator exposes that person's amount, never their identity. Per-delegate reveal counts are public: a pool's reveal count R bounds its DCs between `ceil(R/16)` and R, so R ≤ 16 is consistent with a single DC. That is how an observer can guess that a pool is one person, but counts alone reveal no amount. An observer that knows some DCs in a pool, for example its own, can subtract them, so a pool with one DC unknown to the observer is exposed like a lone pool. Published totals also bound from above every pool on an option. A direct voter alone on an option is exposed the same way, but a pool appears on every proposal, so zero-direct options on different proposals combine. Delegators therefore have somewhat less amount privacy than direct voters, mostly in small rounds and on fringe options.

[measured] Exact rank analysis over the rationals (FLINT fraction-free RREF), 100 simulated rounds per scenario, with an observer that uses only ballots, per-bucket direct share counts, exact per-option totals and reveal counts (`prototypes/privacy/pool_solvability/sim.py`, variant `b1_complete_routes`; summary tables in `headline.md`, full tables in `results.md`, solver cross-checks in `validate.py`). Every determined value was also solved and checked against the ground truth. `s` is the probability that a direct voter skips a given proposal; 95% of delegates submit a complete ballot and the rest none; about 2/3 of pools have one delegator; 20% of delegates are contrarians who favor options with little direct support. The last column is rev 4's proposal-by-proposal routes (the `baseline` variant, where each active delegate chose on each proposal with probability 0.8-1.0), for comparison.

| Scenario (direct voters / delegates / proposals) | Rounds exposing a lone delegator, s = 0 / s = 0.1 | Delegators exposed (mean), s = 0 / s = 0.1 | Pools determined (mean), s = 0 / s = 0.1 | Direct voters exposed (mean) | Rev 4 routes: rounds exposing a lone delegator, s = 0 (upper bound) / s = 0.1 |
|---|---|---|---|---|---|
| Launch-size, few delegates (500 / 8 / 12) | 26% / 28% | 2.1% / 2.2% | 5.4% / 5.9% | 0.1% | 98% / 29% |
| Few delegates, many proposals (2,000 / 15 / 37) | 16% / 17% | 0.8% / 0.9% | 1.6% / 1.8% | under 0.1% | 100% / 19% |
| Small (50 / 10 / 5) | 38% / 39% | 4.0% / 4.1% | 7.9% / 8.2% | 1.0-1.1% | 86% / 41% |
| Medium (500 / 30 / 10) | 12% / 12% | 0.3% / 0.3% | 0.7% / 0.7% | 0% | 53% / 13% |
| Medium, Yes/No only (500 / 30 / 10) | 0% / 0% | 0% / 0% | 0% / 0% | 0% | 43% / 1% |
| Many proposals (2,000 / 50 / 37) | 5% / 6% | 0.1% / 0.1% | 0.2% / 0.2% | 0% | 40% / 7% |
| Large (5,000 / 100 / 15) | 0% / 0% | 0% / 0% | 0% / 0% | 0% | 0% / 0% |

- The current expectation is the complete-ballot columns: 16-26% of launch-size rounds exposed at least one lone delegator's amount, and large rounds about 0%.
- Every remaining exposure comes from channel A, mostly through delegates who choose options with little direct support: without contrarian delegates, the same runs exposed a lone delegator in 0-7% of rounds (`sensitivity.md`). Whether direct voters skip proposals now matters little (s = 0 against s = 0.1), because channel B is closed.
- The rev 4 s = 0 figures, including 98-100% in the launch-size scenarios, are channel B and an upper bound: the simulator gave the observer the true direct-voter sets, and equal per-proposal direct share counts on mainnet make that scenario plausible but do not prove it.
- Simulator caveat (Astra cross-check): the complete-ballot variant gave every active delegate a complete ballot. Applied instead as a filter to the per-proposal behavior the simulator models, the same rule would have dropped most pools, retaining about 12-35% at 12-37 proposals. That is why v1 makes the ballot one atomic message and Vizor requires every answer before Submit (decision 34). The figures assume 95% of delegates submit a ballot; [inference] more delegates sitting out leaves fewer pools on each zero-direct option, which can expose the remaining ones more.
- Medians hide the risk: in most scenarios the median round exposes nobody, so the share of rounds exposing someone is the honest headline.
- Bounds: inequality and integer bounds, an active observer that subtracts its own DCs, and reveal timing are not modeled, so the complete-ballot figures are lower bounds. The observer's knowledge of the true direct-voter sets matters only for channel B.

**Mitigations evaluated (b1 adopted).** [measured, `prototypes/privacy/pool_solvability/headline.md`; per-variant sensitivity runs in `sensitivity.md`] Each cell is mean delegators exposed / rounds exposing a lone delegator.

| Rule | Launch-size 500/8/12, s = 0 | Few delegates 2,000/15/37, s = 0 | Small 50/10/5, s = 0.1 | Assessment |
|---|---|---|---|---|
| None (rev 4 proposal-by-proposal routes) | 28.8% / 98% | 35.4% / 100% | 4.4% / 41% | Replaced by b1 |
| b1: complete ballots | 2.1% / 26% | 0.8% / 16% | 4.1% / 39% | Adopted for v1 (D2, decision 34) |
| b2: zero-weight cover votes | 27.4% / 98% | 34.7% / 100% | 0.9% / 8% | Optional; v1.1 candidate on top of b1 |
| b1 + b2 | 0.5% / 6% | 0.2% / 3% | 0.8% / 7% | Lowest exact exposure; needs b2 |
| a: decrypted delegated-abstain bucket | 30.3% / 99% | 36.3% / 100% | 16.3% / 89% | Rejected |
| b3: no routing below 2 DCs | 0% / 0% | 0% / 0% | 0% / 0% | Rejected |

- **b1, complete ballots (adopted).** Every counted pool is in every proposal's total, which closes channel B exactly; channel A remains, so b1 changes little when direct voters skip proposals. Yes/No-only rounds at s = 0 went from 1.4% / 43% to 0% / 0%. Because of the simulator caveat above, v1 implements b1 as one atomic ballot per delegate (§4.2 Ballot op), not as a filter over per-proposal choices. An independent cross-check by a second model (Astra) agreed it is the best available within this architecture. Costs: weight whose delegate submits no ballot is not counted (D2), delegates have no per-proposal abstain (O5), a suspension before the ballot leaves the pool uncounted (D7), and Vizor's ballot screen requires an answer on every question (§4.5).
- **b2, automatic zero-weight cover votes.** Wallets that delegated everything cast zero-weight votes from the `W = 0` successor on a uniform option per proposal, about 2 per bucket. It needs no circuit change (zero-weight casts work [measured], §4.1), but it lifts CF-2's dead-successor convention and needs randomized timing and about `2 × ΣK × 16` extra reveals per round (about 3,700 at 37 proposals [inference]). It removes certainty from channel A, not the leak: an observer who guesses that buckets showing one vote are cover-only is right 50-83% of the time (`posterior.out`). It does nothing for channel B, which b1 closes anyway. With b1 it gave at most 0.8% of delegators and 7% of rounds exposed in every scenario.
- **a, a decrypted per-proposal delegated-abstain bucket** turns turnout differences into exact sums of the abstaining pools, which are usually small sets. It was worse than baseline everywhere, including when direct voters skip (few delegates, 37 proposals, s = 0.1: 36.3% / 100% against 1.0% / 19%). An Abstain bucket open to direct votes with cover votes still exposed a lone delegator in 25-60% of small or few-delegate rounds. This supports Q2's default.
- **b3, no routing for pools with fewer than 2 DCs,** removes lone exposure by construction but drops 24-35% (median) of all delegated weight, makes more multi-delegator totals solvable (medium, s = 0: 4.1% to 14.2% of pools), and is defeated by an observer that adds one DC of its own.
- An in-app notice (revisiting D11) is a UI measure and is listed only as an option.

**Possible v1.1 upgrade: hidden per-option direct counts (Astra research path; not adopted, Q20).** An additive reveal circuit for direct votes that outputs one encrypted value per option would hide per-option direct counts, so an observer could no longer tell which options received no direct votes. That removes channel A detection. Existing verifying keys stay unchanged, because the circuit is added alongside them, as ZKP4 is. Costs [inference, Astra's estimate]: about 3× helper proving per share, larger reveals, and the loss of the live per-option vote bars in the vote-sdk UI, which reads per-option `ballot_count` from `vote-summary` while a round is active (`ui/src/App.tsx:3733-3734, 3805-3808`, `ui/src/api/chain.ts:769-773` [code]).

**Validator trust.**
- Amount privacy for both direct votes and delegations relies on fewer than t election-key holders colluding (t = 7 of 10 validators, `ThresholdForN`, `x/vote/keeper/keeper_ceremony.go:53-65` [code]).
- The helper runs inside validator svoted nodes (`docs/runbooks/join-chain.md:173` [doc]), and each helper payload carries the VC tree position and all 16 share commitments (`internal/helper/types.go:121-131` [code]: `tree_position`, `share_comms`), so a helper can tell which shares belong to the same vote or delegation.
- A coalition of at least t validators that keeps helper intake can therefore reconstruct individual amounts, for direct votes and delegations alike. With keys alone it can decrypt any pool ciphertext and any single share, including the whole amount of a last-moment DC or vote. Decrypting `Pool[d]` with keys alone gives that delegate's total and, if the pool has one delegator, that delegator's whole amount; grouping a direct voter's 16 shares needs helper intake (a last-moment single share does not).
- Against validator collusion, delegators get the same protection as direct voters, under the same assumption. Against public observers they get somewhat less, mostly in small rounds (Pool solvability).
- A lone delegator's amount is exposed whenever its pool is determined by the published results (Pool solvability), and by validator collusion.
- Splitting across several delegates means any single exposure reveals only that slice.

**D2, D4 and D5, normative wording for ZIP-PD, the book and the FAQ:**

> Per-delegate pool totals are not published. Each delegate submits at most one ballot per round, with exactly one counted option for every proposal of the round, and a ballot cannot be changed. When voting ends, the protocol adds the pool of each delegate with an accepted ballot into the option that ballot chose on every proposal, and adds a pool without a ballot to no bucket. It then threshold-decrypts only the per-option totals, once. No pool is decrypted on its own, and no running, intermediate or repeated tally is decrypted.
>
> (i) Per-option totals include delegated weight, and each counted pool counts with the same value on every proposal. Because every counted pool is in every proposal's total, differences between per-proposal totals contain no pool term. But ballots and per-option direct share counts are public, so an option that received no direct votes reveals the sum of the pools whose delegates chose it, and several such options can combine to determine a pool total even if the pool is never alone on an option. If a determined pool holds one delegator's weight, which its reveal count can suggest, that delegator's amount is exposed, though their identity is not. An observer that knows some delegations in a pool, for example its own, can subtract them, and published totals also bound from above every pool on an option. A direct vote's amount is exposed in the same way when it is the only direct vote on an option and the delegated weight on that option is zero or itself determined, but no public data links a direct vote's weight across proposals, while a pool appears on every proposal. Delegators therefore have somewhat less amount privacy against the public than direct voters, mostly in small rounds. Specifications, wallets and documentation MUST NOT state that a delegator's amount privacy against the public equals a direct voter's.
>
> (ii) Which delegates a delegator chose is not published, but vote servers learn the delegate of each delegation commitment they process, as they learn the option of each vote. Reveal timing can narrow which anonymous transaction fed a pool with few commitments. A commitment made in the last-moment window is revealed within seconds of its transaction, which publicly links that transaction to its delegate, as a late vote's reveal links its transaction to its option.
>
> (iii) Amount privacy, for delegations and direct votes alike, assumes that fewer than t election-authority key holders collude. Validators hold the key shares and run the vote servers, so a coalition of at least t validators can decrypt any pool and, using what its vote servers received, reconstruct individual amounts.
>
> (iv) Approximate delegation counts are public, live during the round: a pool's revealed-share count R bounds its number of delegation commitments between ceil(R/16) and R. Counts alone reveal no amount.
>
> Against election-authority key holders, delegators get the same amount privacy as direct voters, under the same assumption (iii). Against the public they get somewhat less, as (i) describes.

Normative rules for ZIP-PD: "A delegate ballot MUST contain exactly one counted option for every proposal of the round, and a chain MUST reject any second ballot for the same delegate and round. At routing, the pool of each delegate with an accepted ballot MUST be added to the bucket its ballot chose on every proposal, and a pool without a ballot MUST NOT be added to any bucket. Per-option results MUST be decrypted once, after routing."

Informative note for ZIP-PD: exposure is most likely in small rounds, with few delegates relative to proposals, with few direct voters, and when delegates choose options with little direct support. An Abstain option that few direct voters pick is such an option (Q2).

The optional post-close reveal grace window (Q18) would remove the timing link in (ii) for votes and delegations alike; it is not in v1.

**FAQ wording (plain language).**
- *Can anyone see how much I delegated?* Nobody sees your delegation directly, and no delegate's total is ever shown. Results show only totals per option, and your ZEC follows your delegate's public ballot on every question. Because every delegate's pool counts on every question, comparing results across questions reveals nothing. But if nobody voted directly for an option your delegate picked, that option's total is just the sum of the pools of the delegates who picked it, and a few such options together can pin down your delegate's total. If you are that delegate's only delegator, that total is exactly what you delegated. Your identity and address are never revealed, only the amount.
- *When is this likely?* Mostly in small rounds: when your delegate picks options that almost nobody votes for directly, when there are few delegates compared with questions, or when few people vote at all. In our simulations of rounds with 8 to 15 delegates and 12 to 37 questions, at least one lone delegator's amount could be worked out in about 16 to 26 percent of rounds; across those rounds, an observer could work out about 2 to 6 percent of delegates' totals and about 1 to 2 percent of delegators' amounts. In rounds with 100 delegates, 15 questions and 5,000 direct voters, it could work out none.
- *Is that the same as voting directly?* It is the same kind of exposure: a direct vote's amount can be worked out if you are the only person who picked an option on a question. But your delegate's pool appears on every question, so options on different questions can be combined, which makes it somewhat more likely for delegators, mostly in small rounds. In the same simulations, about 1 percent or fewer of direct voters were exposed.
- *How can I lower the risk?* Delegate to delegates with many delegators, split your weight across several delegates so any exposure reveals only a slice, or vote directly. Someone who also delegates to your delegate can subtract their own amount, so "many delegators" protects you only if they are different people.
- *Can I see how my delegate voted?* Yes. Vizor shows your delegate's ballot as published on the voting chain, whether your delegation was handed off to the vote servers, and, after voting ends, whether it was counted. Vizor shows what the chain and vote servers report; it doesn't give you a cryptographic proof, because delegating means trusting your delegate's judgment, and many delegates also post their choices publicly.
- *What if my delegate doesn't vote?* Your delegated ZEC counts only if your delegate submits a complete ballot before voting ends. If they don't, it isn't counted this round. You can't move it to someone else, because a delegation is final.

**Abstain.** Delegates have no uncounted abstain: a ballot assigns a counted option to every proposal, because a pool that counts on only some proposals reopens channel B (Pool solvability). A delegate who wants to sit out submits no ballot, and its pool is not counted this round (D2). A round may include a real, counted "Abstain" option, which is an ordinary option for direct voters and delegates alike. It is off by default (Q2), because an option few direct voters pick is a channel-A equation. [measured, under rev 4's proposal-by-proposal routes] An Abstain option made 42-44% of pools exactly solvable at 15 proposals, 30 direct voters and 5 delegates when turnout equations were ignored; a decrypted delegated-abstain bucket was worse than baseline in every scenario; and an Abstain bucket open to direct votes, even with cover votes, still exposed a lone delegator in 25-60% of small or few-delegate rounds.

**Integrity.** A VAN is either cast from or delegated from, never both (one nullifier set). No `VC(0, d)` can exist (the ZKP2 gate, and the chain rejecting casts with p < 1). Weight is conserved over the integers, each share is added once, and each counted pool lands in exactly one option on every proposal, while a pool without a ballot lands in none. **Accountability:** ballots are signed, one-shot, immutable and applied to the whole pool; a delegate cannot drop individual delegators. Anyone with archival state can recompute the routing identity on ciphertexts from the pre-transition buckets and pools, the ballots and ρ (decision 40). Not covered: a delegate's private vote may contradict its ballot; delegators get no proof of the ballot or of their own shares (D1); and the X binding is trusted to the verifier.

**Identity attacks and defenses.**

| Attack | Defense |
|---|---|
| X account takeover | The account is the master identity (owner rev 8), so a hijacker can re-key the entry. The new key can ballot only in rounds created after the re-key, so formed pools cannot be captured (IDN-1); Vizor shows "Key changed" with the date; curators see the fingerprint change in the next list's PR. The owner registers again after regaining the account, after the 14-day cooldown (§4.6) |
| Verifier API compromise | The signer recomputes the digest, re-checks the post through oEmbed and makes the paid lookup with its own credentials, budget and log, so the API can delay or refuse registrations, or spend up to the daily cap, but cannot mint one; the refresher reads every registration post daily; signer-log reconciliation |
| Signer host compromise | Can mint unvetted impostors only for subjects that never registered; the refresher's re-check and log reconciliation detect it within about a day; containment is removing the verifier key from params (2 approvals) and flagging registrations since the suspected time |
| Directory key theft | The vetted list is pinned per round by the admin key, not the directory key; Vizor proves the chosen entries before proving, so a relabelled index, key or handle is caught; a vetted delegate's handle change is always shown as "renamed from"; wallets pin handles; `seq` and expiry; re-sign the round's proxy entry with a new key |
| Admin key (`trusted_keys`) compromise | Could pin an impostor into a round's vetted list. The same key already authorizes every round's election key, so it is already the root of trust for vote privacy; the list also needs Valar and Vizor PR approval, and Vizor's pre-proving check still binds every delegation to the chain entry it names |
| Impersonation by an unvetted lookalike | Not browsable; exact search only; no avatar or display text; "Not reviewed" label; lookalike check against vetted handles and reserved names; fingerprint on every surface |
| Delegate key theft | Per-use presence. A thief can submit this round's ballot once if the delegate has not yet (accepted), but cannot replace a ballot already accepted, and can revoke the entry. The owner registers again from the same account, which stops the stolen key at once (§4.6 Re-registration and key loss) |
| Coordinator key | Suspension is immediate, freeze-only and public with a reason code; every proxy payload needs at least 2 approvals, reaching 2-of-3 within the first two proxy rounds (D7). Coordinators remain fully trusted, as they already are via x/upgrade. |

**Not provided:** receipt-freeness (a delegator can prove its DC opening; pools are vote-market aggregation points, cf. LobbyFi); per-delegate censorship resistance (reveals and ballots tagged `d` are attributable; helper redundancy and multiple vote servers mitigate); and proofs for delegators that a ballot or their own shares were recorded (D1).

**Delegates' own privacy.** Vote servers can link a delegate's private votes to their public ballot unless Tor is on, so delegate mode defaults Tor on.

---

## 6. Hole register

Severity is after skeptic review. "Unverified" means not re-checked by a skeptic; these are adopted where cheap. Grouped rows share one fix.

| ID(s) | Sev. | Issue | Resolution |
|---|---|---|---|
| IDN-1 | high | Takeover or rogue-verifier recovery captures pools mid-round; removing a verifier doesn't stop it | Rev 6 removed recovery. Rev 8 re-keys by re-registration, and the new key cannot ballot in rounds created before the re-key, so no formed pool can be captured; a removed verifier key stops new re-keys (decision 10) |
| IDN-2 | high | Signer signs opaque digests; alarm reads API-written table | Signer recomputes, re-checks every post through oEmbed and makes the paid lookup with its own credentials; own budgets and daily spend cap; the refresher reads every registration post daily; signer-log reconciliation (rev 6: no separate re-verifier, decision 21) |
| IDN-4 | high | One online directory key controls handle, avatar and curated status | Vetted list and profiles pinned per round by the admin key after Valar and Vizor PR review, never by the directory key (rev 7); the directory key can only hide; proven pre-proving check of the chosen entries; handle pinning |
| CMP-1, OPS-1, IDN-6(b), LIV-10(b) | high | DRK unfreeze defeats freeze; freezes replayable | **Closed by removal (rev 6):** no freeze, unfreeze or pending change exists; only a verifier-attested re-registration from the same account undoes a `revoke` (decision 10, rev 8) |
| IDN-3 | medium | One coordinator key can install verifiers and zero delays | Trust model stated; every proxy payload needs ≥2 approvals, 2-of-3 within two proxy rounds (Q5 decided, D7); no delays or warm-up exist to configure (rev 6) |
| IDN-5 | medium | Any post or retweet with the marker binds an account | Original post, whole-template match |
| IDN-6(a,c) | medium | DRK thief blocks recovery; cancels burn change cap | **Closed by removal (rev 6):** no recovery, cancel or change cap |
| IDN-11 | medium | Lookalike precedence by registration order; empty curated list at launch; inherited successors | OOB fingerprint confirmation for every vetted entry; ASCII lookalike check against vetted handles and a reserved list; re-keyed entries keep their index and show "Key changed" (rev 8: no successor links; rev 6: no first-seen precedence or skeleton reservations, decision 43) |
| IDN-13, CMP-22 | medium | Conflicting app-store UGC controls | UGC limited to the curated vetted list, with every change reviewed by Valar and Vizor in the list's PR; report link and local Hide; one text limit; terms |
| PRV-1 | medium | OVK-keyed ARK exposes delegations to UFVK holders | DC secrets from the random per-round hotkey (rev 6, decision 14) |
| PRV-2, CMP-17 | medium | Abstain option creates solvable buckets; claims overstated | No Abstain option by default (Q2, default no) and no uncounted abstain for delegates (O5); §5 claims restated: complete ballots close the cross-proposal channel, and zero-direct options leave delegators somewhat more exposed than direct voters in small rounds (§5 Pool solvability, XR-1); exposure stated in §5, the FAQ and copy C4 rather than warned in the app (D11) |
| PRV-4, LIV-4, CMP-11, SND-8 | medium | Five cutoff rules; single-share DCs leak; hint key reuse | Vote parity (D5): one rule shared with casts (open until `vote_end_time`; single share iff `is_last_moment()` at batch planning); layout frozen per batch for its DCs, `(d, w, slot, layout)` per `van_nf`, before the first proof; same host clock as votes; authenticated timing required; timing and EA exposure stated as identical to late votes. SND-8 no longer applies: there is no recovery hint (rev 6) |
| PRV-7 | medium | Vote servers link delegate's private votes to identity | Tor default-on in delegate mode; honest copy |
| LIV-1 | medium | Helper fleet caps scale; Zodl votes also lost | Budget (16 per pre-window DC, 1 per in-window DC), metrics, more workers, Zodl non-regression gate including in-window DCs |
| LIV-3 | medium | Locked allocation strands unexecuted bundles | `release_proxy_bundle`, including bundles a chain pause rejected |
| LIV-5, CMP-10 | medium | Adding to a locked allocation was undefined; no `dc_slot` | One delegation per round, never changeable (D2); `dc_slot` persisted |
| OPS-3, CMP-7 | medium | Recovery feed had no backing store | **Closed by removal (rev 6):** no recovery feed (decision 17) |
| OPS-6 | medium | Dormant tests ignore GasUsed; Msg-service ungated | `V:state/breaking`; one const gates the Msg service too; v1.7.0 activates at a halt between rounds and is never backported, so no two binaries run the same heights and GasUsed equality is not needed (rev 6) |
| OPS-8, CMP-7 | medium | Conflicting KV layouts; false "did not vote" | One key table; one ballot key per `(round, d)`, so absence is a missing key (§4.2 Ballot absence) |
| CMP-2 | medium | "Stop accepting" had no valid backing op | **Closed by removal (rev 6):** no "Stop accepting"; a delegate retires with `revoke` |
| CMP-3 | medium | Registration shapes put X ids in tx bytes | Identity `register` op only |
| CMP-4 | medium | Wire contracts diverge across specs | `contracts-v1` with golden vectors |
| CMP-5 | medium | Phrase model not propagated; hardware delegates blocked | Phrase everywhere; DG-12 removed |
| CMP-6, SND-2, SND-3, IDN-8 | medium | Four delegate-vote digests; no chain binding | One convention with `lp8(chain_id)`; reset changes `chain_id` |
| CMP-8 | medium | PI order, packing, PRF disagree | 11-PI `Instance` order (rev 6: no bound); `dc_seed` PRF |
| CMP-12 | medium | Capability and config names disagree | One field list, two fields; no dynamic-config extension (rev 6) |
| CMP-13 | medium | Four static-config shapes; per-round pin conflict | No new static pin; one `extensions.proxy_delegation_v1` object in the dynamic config, with the vetted list pinned per round and the live directory not frozen (rev 7) |
| CMP-14 | medium | WebP vs PNG; platform codecs | WebP plus Rust decode; ban test |
| CMP-16, PRV-3 | medium | Immediate DC share links tx to delegate | No designated immediate DC share; window-wide immediacy as for votes; in-window DCs gate completion on their reveal |
| CMP-18, LIV-2 | medium | Recovery unwired; wrong "still counts" copy | COMPLETE on hand-off; new C8, which says plainly that a reinstall before hand-off loses the delegation (rev 6: no R2, decision 17) |
| CMP-19, PRV-5 | medium | Per-index lookups leak choices | Whole-document directory download and whole-list ballot reads, kept. The proof-set part is superseded in rev 5: no delegator proofs (decision 35); the pre-proving registry check is the one per-key request left (R5-2) |
| CMP-23 | medium | Audit scope too narrow | Two tranches; tranche 2 about a third smaller in rev 6 |
| CMP-25 | medium | Unlisted owner questions | §7 |
| SND-1, CF-3 | low | Slot nf missing from chain and genesis paths; rand invariant | Slot nullifier wired through ValidateBasic, ante step 4, the handler and genesis (§4.2); invariant in ZIP-PD |
| SND-6, CF-1 | low | Bound open; width and value rule | **Closed by removal (rev 6):** no `delegate_index_bound` (O1); the chain's reveal check `1 ≤ d < NextDelegateIndex` stays |
| SND-7 (unv.) | low | Mixed anchor burns a registration | Registration ⇔ anchor 0 |
| SND-4 (unv.) | low | Stolen key submits the delegate's vote irreversibly | Per-use key presence; a ballot is one-shot, so a thief cannot replace one already accepted; registering again from the same account stops the stolen key at once (§4.6 Re-registration and key loss) |
| SND-5 (unv.) | low | Suspension stops a pool from counting | Kept; owner decided (Q5, D7): immediate, freeze-only, public reason, ≥2 approvals; a suspension before the ballot leaves the pool uncounted, and a ballot already accepted stands |
| IDN-7 | low | Hot key theft; misleading claim | Per-use presence; reworded |
| IDN-9 | low | Floods via fresh signatures | One ballot tx per `(round, d)` per block in PrepareProposal; registry ops need a fresh attested key, with one registration per account per 14 days, or the entry's own key (rev 6, rev 8) |
| IDN-10 | low | Subdomain sybils | **Closed by removal (rev 6):** no DNS provider (decision 43) |
| IDN-12, PRV-8 | low | Crypto-shredding claim false; stale objects | Claim withdrawn; only the current directory and pack exist; ≤24 h expiry |
| IDN-14 | low | Phrase looks like a wallet seed | Non-BIP-39 format |
| IDN-15 (unv.) | low | Ed25519 rule mismatch | ZIP-215; torsion-free keys |
| IDN-16, CMP-15, PRV-9 | low | Index in link path; fingerprint drift; size-ordered DCs | No deep links in v1 (decision 41); 80-bit fingerprint; shuffled order; one pack |
| PRV-6 | low | Sentry spans link reveals to leaves | Span hygiene |
| LIV-6 | low | Mirror outage blocks delegation | Two mirrors with failover; search always refreshes first, and commit needs a directory verified within 10 minutes, so an outage of both mirrors pauses new delegations while voting continues (rev 6) |
| LIV-7, LIV-8 (unv.) | low | C9 misleads; cross-install surprises | C9 reworded; gov-nullifier gate reports "used from another device" (rev 6: no R2 offer) |
| LIV-9, OPS-11 (unv.) | low | Helper index check before leaf | Leaf first; 503; the index check is reachable without the bound and returns a permanent error (§4.3) |
| LIV-11, LIV-12 (unv.) | low | Delegate-vote retries; gates halting work | Any second ballot is rejected, and the client treats an identical stored ballot as success (decision 26); gates only for new allocations |
| OPS-2 | low | Genesis rejects type 3 | Genesis section; `ShareCountKey` reuse |
| OPS-4, CMP-20 | low | Beta and Zodl; enable race | Stage beta; Zodl limits; the proxy flag is set in `MsgCreateVotingSession`, so there is no enable race (rev 6, decision 45); sign-off |
| OPS-5 | low | Zakura 2.0 swap unchecked | Corpus replay gate |
| OPS-7 | low | No mid-round brake | `proxy_dc_paused`, which Vizor also reads before every commit (D9, decision 38) |
| OPS-9 | n/a | Not applicable: depended on a redesign that is not shipping | n/a |
| OPS-10 (unv.) | low | Proof-verification cap risk | No cap in v1 (decision 24) |
| OPS-12, OPS-13 (unv.) | low | `ProposalTally(0)`; routing cost | Reject 0; droplet benchmark; `max_delegates` 20,000 |
| CF-2, CF-4, CF-5 (unv.) | low | W=0 casts; key RAM; digest kind tag and split-off | Dead successor; phased proving; no kind byte needed, because 0x09 carries only DCs (rev 6); re-plan |
| CMP-9 | low | ZIP-PD leaf order reversed | Corrected |
| CMP-21, CMP-24 | low | No legal item; missing ops tooling | Legal gate; V20 |
| CMP-26..29 (unv.) | low | Vocabulary, analytics, perf, support gaps | contracts-v1 vocabulary; aggregate analytics with no per-delegate counts or totals; device and droplet benchmarks; support runbook and delegate reminders |

**Revision 2 to 6 rows** (owner changes and their reviews; XR rows come from the external review of rev 3, R5 rows from the owner's rev 5 decisions and the Astra cross-check, and R6 rows from the rev 6 simplification pass; none of these sets was re-checked by a skeptic):

| ID(s) | Sev. | Issue | Resolution |
|---|---|---|---|
| PUB-1..9 | n/a | Superseded: totals hidden in rev 3 | n/a |
| LMD-1 | owner | Delegation closed up to 6 h before voting ended | Parity (D5): open until `vote_end_time`; single share in the window; pools routed at close; no chain, helper or VK change |
| LMD-2 | medium | Layout frozen per DC only; casts in a batch re-proved across the window boundary fail `recovery_matches_draft` | Layout persisted once per 0x09 batch before the first proof. Since rev 6, 0x09 carries no casts, so remainder casts keep the vote path's own layout handling |
| LMD-3 | medium | A late proxy-only DC whose reveal misses close reports success (no designated immediate share) | In-window DC completion waits for the reveal of share 0; fails at `vote_end`; "Not counted" |
| LMD-4 | medium | Per-key share-nullifier proof counts in the decoy proof set (1 vs 16 per DC) reveal real keys and layouts | Superseded in rev 5: no delegator proofs (decision 35) |
| LMD-5 | medium | A late DC's reveal links its anonymous tx to its delegate | Accepted as parity with a late vote's reveal; stated in §5 (ii); grace window later (Q18) |
| LMD-6 | low | The votes-table delivery planner cannot hold DCs; layout inferred from payload count | Proxy planner keyed by `dc_slot`; explicit `single_share` |
| LMD-7 | low | Late `KeepForSelf` remainder needs a second tx-and-reveal cycle | Accepted in rev 6 (decision 23): 0x09 carries no casts, so the remainder is voted in a separate tx a few seconds later, and a remainder vote sent in the final minute may miss close, as any late vote may. Vizor takes the user straight from the delegation to the vote flow |
| LMD-8 | low | Missing round timing silently disables `VoteEnded` and `is_last_moment()` | `proxy::availability` requires authenticated timing for new allocations |
| LMD-9 | low | Removing the delegate deadline margin shifts risk; load framing optimistic; C9 rejection not isolated; SND-8 safe by policy only; `revealCloseTime` touches shared validation | Final-5-minute warning and 60 s confirm; pass criteria scoped to shares accepted ≥10 min before close; isolated C9 test; no recovery hint, so SND-8 no longer applies (rev 6); `revealCloseTime` deferred to v1.1 (rev 6) |
| CRD-1 | low | Under D7, incident levers (`proxy_dc_paused`, suspension) need two coordinator approvals | Two coordinators on call for every proxy round; runbook pre-drafts the pause and unpause payloads. Since rev 6 the chain pause is the only off switch (decision 38) |
| TRU-1 | high | Helpers run inside validators and see each payload's tree position and all 16 share commitments, so a coalition of at least t validators that keeps helper intake can reconstruct individual amounts, for direct votes too | Stated as the v1 trust assumption in §5 Validator trust and the normative wording (iii); label-free reveals would not fix it (decision 31); a real fix needs helpers separated from key holders, which is out of v1 scope |
| XR-1 | high | Cross-proposal pool solvability: a pool is the same unknown on every proposal its delegate routes, so zero-direct buckets and per-proposal turnout differences give equations that determine pool totals even when no delegate is alone on an option. Lone delegators are more exposed than direct voters, so claiming parity with direct voters is false | [measured] §5 Pool solvability. Rev 5 adopts b1 as complete, atomic ballots (D2, decision 34, Q19 decided), which closes the turnout channel by construction; the zero-direct-option channel remains and is disclosed in normative (i), §1 item 10, answer (c), D4, D11, decision 31, copy C4, the FAQ wording and O5; b2 is a v1.1 candidate, a and b3 are rejected, and Q20 records hidden per-option direct counts as a possible v1.1 upgrade; D11 kept |
| XR-2 | medium | An unsigned remote kill-switch flag can be flipped by whoever controls its host, and Vizor's only remote flag today, `swap-enabled.json`, is unsigned | Rev 4-5: a signed round extension's `enabled`. Rev 6: no remote flag of any kind; Vizor reads the chain pause, which needs 2 coordinator approvals (D9, decision 38) |
| XR-3 | low | A zero-nonce ChaCha20-Poly1305 hint is safe only while `(d, w, slot, layout)` stays frozen per `van_nf` (SND-8) | Rev 4-5: an AES-SIV hint. Superseded in rev 6: no recovery hint (decision 17); the freeze stays for byte-identical re-proves |
| XR-4 | medium | The VK fingerprint tests are `#[ignore]`d, so a plain `cargo test` skips the release gate | CI runs `cargo test --release -- --ignored vk_fingerprint_unchanged` for ZKP1-4 on the release commit (§4.1) |
| XR-5 | low | Several referenced hooks do not exist yet and read as existing | [new] labels and §4 Existing vs new |
| XR-6 | low | Chain-proof verification assigned to zcash_voting, which has no light client | Vizor's participation reader, extended with the registry entry keys; zcash_voting supplies key derivations and expected values (§4.4 Chain verification). Since rev 5 it serves only the pre-proving registry check and gov-nullifier non-membership |
| XR-7 | low | Routing scans every stored delegate vote regardless of how many pools received reveals | Reverted in rev 6 (decision 40): routing iterates ballots. The scan is bounded by `max_delegates` either way and the curve work is identical, so a derived counter on the reveal path, its genesis rebuild and a forced-branch test were not worth it |
| R5-1 | medium | Astra caveat: the simulation that measured b1 gave every active delegate a complete ballot; as a filter over per-proposal choices, the rule would have kept only about 12-35% of pools | One atomic ballot, validated complete at submission (§4.2 Ballot op); Vizor's ballot screen requires every answer before Submit (§4.5); decision 34 |
| R5-2 | low | Without a decoy set, the pre-proving registry proof request names the chosen delegates' keys to the RPC server | Accepted: helpers already learn each DC's delegate (§5); Tor hides the IP; ballot and directory reads stay whole-list (decision 18) |
| R5-3 | low | Ballots and share status are shown without proofs, so a lying RPC server or helper can misreport them in the app | Accepted (D1, decision 35): the chain counts the real ballot, delegates can publish their choices elsewhere, and the pre-proving registry check stays proven, so a lying server still cannot redirect a delegation |
| R5-4 | medium | A delegate who misses the ballot deadline, or is suspended before it, leaves its whole pool uncounted for the round | Accepted by the owner (D2, D7): copy "Counts only with a ballot" before commit and on PD-7; DG-8 reminder, final-5-minute warning and 60 s confirm; a ballot track record on profiles follows in v1.1 (decision 42) |
| R6-1 | low | A delegate who loses or leaks the key loses that round's pool if this happens before the ballot | Accepted (decision 10). Since rev 8, registering again from the same account keeps the index, vetted status and history; the new key votes from the next round |
| R6-2 | low | A key thief can revoke the entry | Accepted: the delegate registers again from the X or GitHub account the thief does not control, which reactivates the entry (rev 8) |
| R6-3 | low | No recovery after a reinstall or seed restore: a delegation not yet handed off is lost, and the kept remainder cannot be voted from the new install | Accepted as parity with votes (decision 17); copy C8 |
| R6-4 | low | A DC to an unregistered index is never revealed | Accepted: it only hurts its sender, and honest wallets prove the entry before proving (O1) |
| R6-5 | low | Auditing routing needs archival state, with no audit query | Accepted (decision 40) |
| R6-6 | low | Switching delegation off mid-round needs 2 coordinator approvals | Accepted (decision 38); two coordinators on call (CRD-1) |
| R6-7 | low | Without the in-circuit bound, the helper's index check and the chain's reveal check carry the "registered index" rule alone | Both stay, with tests (§4.2, §4.3) |
| R7-1 | low | The admin key that signs rounds now also pins each round's vetted list | Accepted (owner rev 7): it is already the root of trust for every round's election key, and the list also needs Valar and Vizor PR approval |
| R7-2 | low | Vetted-list additions wait for the next round | Accepted: mid-round registrants are still found by exact search (D10), and removals act at once through directory flags or suspension |
| R7-3 | low | Vetted delegates' pictures and names are X content again | Refreshed every round; hidden within 24 h when an account is deleted, suspended or protected, and when the picture changes if counsel requires; counsel signs off (§4.6 Legal) |
| R7-4 | low | Without a round's proxy entry, Vizor cannot verify the live directory or show the vetted list | The entry is published with the round; it is content, not an off switch (decision 38) |
| R8-1 | medium | The X or GitHub account is the delegate's master identity: a hijacker can re-key the entry | Accepted (owner rev 8): no formed pool can be captured; "Key changed" is shown; the 14-day cooldown can delay the owner taking it back, and the hijacker's key can vote only in rounds created after its re-key |
| R8-2 | medium | Profile pictures come from fetching public X profile pages, which X's terms prohibit without written consent | Accepted by the owner (decision 47): once per round, vetted delegates only, identicon on failure, paid lookup as fallback; registration stays on the official API; counsel informed (§4.6 Legal) |
| R8-3 | low | Deleting the registration post delists the delegate, because the free daily refresh reads it | Accepted: registering again restores the listing; the delegate guide says to keep the post up |
| R8-4 | low | Under a sustained attack, new registrations pause for the rest of the UTC day once the spend cap is reached | Accepted: spend stays capped, and each paid call needs a real marker post from an account outside its cooldown |

**Partly refuted:** PRV-2's 81% assumed that pools with no choice on a proposal map to Abstain (real figure 42-44%); OPS-4's Zodl visibility claim fails for Zodl ≥3.9.5, which shows only endorsed rounds (only ≤3.13.x throws on unparseable rounds); IDN-8's replay-after-reset (the verifier refuses reused keys); LIV-2's "COMPLETE before delivery"; IDN-6(c)'s 64-cycle exhaustion; IDN-3's "new trust" (coordinators can already push binaries); OPS-7's reveal pause (unimplementable); CMP-3's on-chain X ids (no proto field exists).

---

## 7. Owner questions

Q16 and Q17 were withdrawn in rev 3 with the published totals. Q19 was decided in rev 5. Q3, Q4 and Q21 were decided in rev 6, and Q22 in rev 7.

### 7.1 Decided

| # | Decision | Where |
|---|---|---|
| Q1 | **Answered (rev 2): delegation stays open until voting ends, like voting.** DCs in a batch planned in the last-moment window use the vote path's single-share layout and are sent at once; at close, pools are added to their delegates' ballots with every reveal that landed before `vote_end_time`. As with votes, a delegation whose reveal misses close is not counted, and Vizor says so. | D5, O3, §4.4 |
| Q5 | **Accepted (rev 2): coordinator suspension with safeguards.** Immediate, freeze-only, with a public reason code; it cannot create entries, change keys or void a ballot already accepted, and a suspension before the ballot means no ballot, so the pool counts nowhere (rev 5). Every proxy payload (params including the verifier set, the pause and suspension), and any `MsgCreateVotingSession` that sets the proxy flag, needs at least 2 vote-manager approvals regardless of the global threshold; at least 2 vote managers before activation, and 2-of-3 within the first two proxy rounds. | D7, §4.2, §8.3 |
| Q8 | **Decided (rev 3): per-delegate totals hidden.** No leaderboard and no ZEC totals on profiles or dashboards. A ballot track record ("voted in N of M rounds") follows in v1.1 (decision 42). Approximate delegation counts appear only on the delegate's own dashboard, marked approximate, never as a sort key. Totals are never published, but published per-option totals can determine some of them (§5 Pool solvability). | D4, §4.5, §5 |
| Q9 | **Decided (rev 3): vetted-only browse list.** The browse list shows only vetted delegates, uncapped, approved by Valar and Vizor reviewers in the config repo and pinned per round in the signed dynamic config (rev 7, decision 39), with its git history as the public log and OOB fingerprint confirmation for every vetted entry. Pictures and names are refreshed from X or GitHub before each round, and statements come from the delegates; unvetted delegates have no avatar or display text. | D3, §4.6 |
| Q15 | **Decided (rev 3): never.** One delegation per round; a committed allocation is never changed or added to. The release path for bundles whose batch never landed is failure recovery, not a change. | D2, §4.4 |
| Q19 | **Decided (rev 5): complete ballots (b1).** Each delegate submits one atomic, final ballot per round with a counted option for every proposal; a pool without a ballot counts nowhere; delegates have no uncounted abstain; results are decrypted once, at close. This closes the cross-proposal turnout channel by construction. [measured] Launch-size rounds exposing a lone delegator fell from 98-100% (rev 4, an upper bound) to 16-26%; large rounds stay about 0%. An independent cross-check by a second model (Astra) agreed b1 is the best available within this architecture; its caveat that the simulation assumed complete ballots is why the ballot is atomic. The remaining zero-direct-option exposure is accepted for v1 with the §5, FAQ and copy C4 disclosure; Q20 records a possible v1.1 upgrade. | D2, D7, decisions 34 and 37, §4.2, §4.5, §5 |
| O4 | **Confirmed: a separate delegate key phrase.** One delegate key per phrase since rev 6. | D6, §4.6 |
| Q3 | **Decided (rev 6): 1 ballot (0.125 ZEC) minimum per delegate,** as a fixed protocol floor with no per-round parameter. | §4.4 |
| Q4 | **Decided (rev 6): no delegator recovery after a reinstall, for any account,** as for votes. The earlier question, opt-in recovery keyed to the viewing key for hardware delegators, is moot. | Decision 17, §4.4 |
| Q21 | **Decided (rev 6): adopt the simplification pass in full,** including the three owner calls: (A) no deep links in v1, (B) a lean status view, and (C) one off switch. | Revision history, decisions 10, 14, 17, 23, 26 and 38-45 |
| Q22 | **Decided (rev 7): the vetted list lives in the dynamic config,** pinned per round by the existing admin key and approved by Valar and Vizor PR review instead of curator keys, with pictures, names and handles refreshed from X or GitHub before each round. | Decision 39, §4.6, §4.7 |
| Q23 | **Decided (rev 8): registration cost and identity.** 12-word delegate phrases. Registering again from the same account re-keys the entry, with no support process and a 14-day cooldown checked before any paid call. Registration costs a free oEmbed check first, then one paid lookup by the signer under a daily spend cap. The daily refresh is free through oEmbed. Valar's own tool fetches pictures from public profile pages once per round. Profile content stays on Valar's servers, with only hashes in git. | Decisions 10, 46-48; §4.6 |

### 7.2 Open

| # | Question | Recommended default |
|---|---|---|
| Q2 | Allow a round to include a real, counted "Abstain" option, an ordinary option for direct voters and delegates alike? With complete ballots it is the only way a delegate can abstain on one proposal and still count on the others. But an option that few direct voters pick is a channel-A equation: [measured, under rev 4's proposal-by-proposal routes] an Abstain option made 42-44% of pools exactly solvable in small rounds when turnout equations were ignored, and every abstain-bucket variant tried was worse than the baseline (§5 Abstain). Zodl would have to present it. | No. Off by default; a delegate who wants to abstain sits out the round by submitting no ballot. |
| Q6 | Verifier trust at launch | 1-of-1 Valar for registration; an independent second verifier within 6 months, added through params (k-of-n counting already exists, so no upgrade is needed) |
| Q7 | Beta venue | Stage chain. Mainnet test rounds only within old-Zodl limits (≤15 proposals, 2-8 contiguous options), never Zodl-endorsed. |
| Q10 | May other wallets consume the directory? | Registry entries (IDs and current handles) once counsel clears handle redistribution. Vetted pictures and names are X or GitHub content under the same decision; statements follow the content terms delegates accept. |
| Q11 | Who handles reports, and how fast? | Valar support, through a web form or email; 24 h for impersonation and offensive content; same-day delisting when confirmed. |
| Q12 | Per-round enablement | Opt-in per round through the `MsgCreateVotingSession` proxy flag; enable for all public rounds after one successful mainnet proxy round. |
| Q13 | Ship follow-mode v0 (directory plus signed voting guides) a round early? | Only if the verifier MVP is early and it does not touch the circuit path. Do not plan on it. |
| Q14 | X API budget. Rev 8 pays only for registrations: one post lookup with its author (about $0.015) per registration that passes the free oEmbed check, under a daily spend cap (launch about $5). Handle refresh (oEmbed) and per-round pictures (Valar's own tool) are free. Earlier analysis, kept for reference: refreshing handles with paid user lookups would cost about $63 a month at 200 delegates. Keep the cap at $5 a day? | Yes. [inference, X prices read 2026-10-06: users $0.010 and posts $0.005 per resource, deduplicated per UTC day] About $63 a month at 200 delegates, $310 at 1,000 and $3,050 at 10,000 (about $1,250 if 70% are dormant and refreshed weekly); registration checks add about $0.035 per attempt. If counsel accepts X's batch compliance jobs for the deletion duty, weekly refresh plus those jobs costs less. Re-estimate against the X price list at launch. A stale handle can only point at the delegate bound by numeric id, and handle pinning warns on renames. |
| Q18 | Post-close reveal grace window G (reveals only, accepted until `vote_end + G`, before routing and any decryption), shared by votes and delegations? It removes the timing link of late votes and DCs and lets helper backlog drain. | Not in v1. Design for v1.1 after load-lab data, as a separate coordinated upgrade, with Zodl notified (results arrive G later). The `revealCloseTime` refactor is deferred with it (rev 6, §4.2). |
| Q20 | Hide per-option direct counts in v1.1 (Astra's research path, not adopted)? An additive reveal circuit for direct votes that outputs one encrypted value per option would hide which options received no direct votes, removing channel A detection; existing verifying keys stay unchanged. Costs [inference, Astra's estimate]: about 3× helper proving per share, larger reveals, and the loss of the live per-option vote bars in the vote-sdk UI (`ui/src/App.tsx` via `vote-summary`) (§5, Possible v1.1 upgrade). | Not in v1. Revisit for v1.1 after load-lab data on the helper cost, and if mainnet proxy rounds show zero-direct options holding lone pools. |

---

## 8. Rollout and milestones

### 8.1 Work packages

Sizes: S is 1-3 days, M is 1-2 eng-weeks, L is 2-4 eng-weeks. Per-repo totals are rough [inference]; rev 6 figures come from the three simplification reviews (`archive/review/rev6-simplification.md`).

- **Specs (chain lead, spec lead).**
  - **SP1 `contracts-v1`** (M, weeks 0-3) **blocks every implementation PR.** It covers:
    - proto, JSON names, tags, REST, digests, the KV table and capabilities;
    - errors and vocabulary;
    - FRB DTOs;
    - Go and Rust golden vectors in CI.

    Rev 6 makes it smaller: two new tags, five delegate digests plus D1, and no pending-change state.
  - **SP2:**
    - ZIP-A errata limited to the hooks ZIP-PD cites (S).
    - **One ZIP, ZIP-PD** (M). Rev 6 folds ZIP-DR into it as a registry section: register, revoke, suspension, attestation counting, the five digests, the re-key rule and the coordinator trust model with the D7 approval floor. ZIP-PD also carries:
      - the ZKP4 statement, including the 3-bit slot check and no index bound, frozen by week 3;
      - the timing section (spec_rollout §1.4.11), rewritten for parity. Delete "SHOULD finish before vote_end − buffer" and "MUST NOT start after vote_end − 30 min". The wallet MUST use the vote path's last-moment predicate and `VoteEnded` check, MUST require authenticated round timing for new allocations, and MUST freeze the batch layout and `(d, w, slot, layout)` per `van_nf` before the first proof;
      - the complete-ballot and one-final-tally rules, the §5 normative wording, pool solvability and the validator trust assumption.
    - Directory, proof-template and verifier-policy formats in a document in the verifier repo (S).
    - WAPI/SUB/SETUP amendments (S).
    - Book edits only where pages become false (S-M, to week 15). One overview page and one delegate page replace `delegation-setup`, `partial-delegation`, `userflow/delegating-your-vote` and the README TODO. Add the §5 wording to the privacy page, and fix the sentence in design-principles; the reference pages follow later.
- **voting-circuits (about 7-8 eng-weeks)**, with rc.1 by week 8:
  - constants (S);
  - ZKP4 with C14 (3-bit slot check) and no index bound (M);
  - MockProver suite (M);
  - builders with the `dc_seed` PRF (M);
  - layout-1 secrets, 0x14 path and vectors (S);
  - prove, verify and fingerprints (S);
  - exports and vectors (S);
  - cross-circuit tests (S);
  - benchmarks with mobile RSS (S).
- **vote-sdk (about 12.5-14 eng-weeks):**
  - proto and dormant gating with one const (S);
  - types and digests (S-M);
  - capacity guard (S);
  - registry: register (new or re-key, with the re-key ballot rule), revoke and suspension (M);
  - ballots, with the completeness and one-shot rules (S-M);
  - ZKP4 FFI (M);
  - 0x09, cloned from the 0x06/0x07 batch code (M);
  - p=0 reveal (S);
  - the round flag in `MsgCreateVotingSession`, params and pause (S);
  - routing hook (M);
  - queries and capabilities (S);
  - genesis (S);
  - helper branch, telemetry, metrics (M);
  - upgrade and runbooks (S);
  - e2e (L);
  - determinism and droplet benchmark (S);
  - corpus replay gate (S);
  - activation (S);
  - D7 approval floor for proxy payloads (S).
  - **V20 ops (S-M):**
    - CLI and coordinator-UI support for the proxy payloads and the round flag, including a pre-drafted pause and unpause pair;
    - Prometheus metrics with an alert on permanent p=0 rejections;
    - a scripted canary delegate and delegator per proxy round (one pre-window DC and one in-window DC at about T−10 min), asserting that the canary's shares and ballot landed;
    - explorer event docs.
- **zcash_voting (about 12-13 eng-weeks):**
  - planner with sequential fill and the 8-delegate cap (S-M);
  - secrets from the hotkey, phrase and digests (S-M);
  - circuits integration and phased proving (M);
  - schema v25 (M);
  - effective-weight refactor (L);
  - batch builder for DC-only 0x09 (M);
  - submission lifecycle and split-off (M-L);
  - planner obligations and release path, including paused batches (M-L);
  - DC share delivery through the existing share kinds (M);
  - the plain status view model, plus registry key derivations and expected values for the pre-proving check (S);
  - gating on chain state, including the pause (S-M);
  - directory client with exact search, the lookalike fold and the pre-proving chain check (S-M);
  - delegate APIs, including the complete-ballot builder, preflight and identical retry, and the crate (S-M);
  - integration, vectors, mobile benchmarks (M);
  - layout choice, batch freeze and the explicit-layout share planner (S).
- **Verifier service (about 5.75-7 eng-weeks):**
  - API with the free oEmbed pre-check, the 14-day cooldown and rate limits, and the X and GitHub providers (M-L);
  - hardened signer that re-checks every post and makes the one paid lookup under the daily spend cap, plus the key ceremony (M);
  - refresher (daily oEmbed reads of registration posts, delisting on 404 within 24 h) and chain indexer (S-M);
  - publisher of the one signed `directory.json`, on two mirrors (S-M);
  - curator tool: fetch vetted pictures from public profile pages (decision 47), upload content to Valar's store, write the hashes-only list and open the PR with previews (S-M);
  - reports inbox and retention (S);
  - ops (M).

  Legal review runs externally in weeks 4-10 and gates the X provider.
- **Config repo** (S): the `extensions.proxy_delegation_v1` object, `*/delegates/`, CI signature and hash checks, and CODEOWNERS. The vote-sdk admin UI's "Sign config entry" step gains the proxy-entry payload (S). The **deeplink server** has no change in v1.
- **Vizor (about 9-10 eng-weeks).** UI on mocks from week 4, integration from week 11. It includes:
  - the vetted browse list;
  - exact search with the "Not reviewed" and "new this round" states;
  - the delegate ballot screen and confirm sheet (every question required, re-auth, one submission) (S-M);
  - Retire;
  - the plain delegation status view with the delegate's published ballot (S);
  - the paused state and release offer (S);
  - in-window DC completion gating with the "Not counted" state (S);
  - the participation-reader extension for registry entries (S);
  - the new network-role entries and service tests (S).
- **Stage, load lab and QA (about 3.5-4.25 eng-weeks); specs and book (about 3.5-4.25).**

### 8.2 Timeline and critical path

| Milestone | Weeks | Notes |
|---|---|---|
| M0 contracts-v1, ZKP4 statement, registry digests frozen | 0-3 | Critical. Every rev 6 change to wire formats lands here |
| M1 voting-circuits 0.13.0-rc.1 | 2-8 | Critical; about a week earlier than rev 5, without the index bound |
| M2a Audit tranche 1: ZKP4 (both layouts, 3-bit slot check), 0x09, p=0 reveal, ballot validation, routing and ρ, registry handlers | 8-12 | Critical; book auditors now |
| M2b Audit tranche 2: client secrets (hotkey-derived `dc_seed`, layout 1), phrase format and key derivation, the five registry and ballot digests, the re-key rule and attestation counting, signer custody, the per-round proxy entry and its signing payload, directory signing, Rust image decode, Vizor's participation-reader extension (light-client and IAVL checks of the registry entries) | 11-14 | Launch gate; about a third smaller than rev 5. Merge it into M2a if the client crypto is ready by about week 10 |
| M3 vote-sdk dormant merges plus helper | 3-11 | Registry and ballots need no circuits |
| M4 Verifier MVP (X, GitHub), with the hardened signer | 3-9 | One identity engineer can move to Vizor integration afterwards |
| M5 zcash_voting | 4-12 | Needs M1 rc |
| M6 Vizor | 4-15 | |
| M7 Config extension, legal sign-off | 6-10 | Legal covers handles and vetted delegates' X pictures and names (§4.6 Legal) |
| M8 Stage activation; T1 and T3; load lab; capacity gate | 12-16 | Critical |
| M9 Mainnet activation between rounds | 16-17 | Critical. Real influencers onboard on mainnet after M9, with registration on and no proxy round yet |
| M10 First public proxy round | 17-18 | |

- **Critical path:** M0 → M1 → M2a → release candidates (0.13.0, v1.7.0, zcash_voting, Vizor) → M8 → final tags → M9 → M10, about 17-18 weeks, set by the circuit, the audit and the stage rounds.
- M2b and the legal sign-off run on parallel paths that must close before M8 ends.
- **Total:** about 54-61 eng-weeks plus two audit tranches [inference], against rev 5's 79-84.
  - Rev 6 saves in every package.
  - Rev 7 trims about 0.5 more net: no curator keys, offline certificate or new static pin, against the new per-round proxy entry and picture pull.
  - Rev 8 adds back about 0.5-1, for the re-key op, the oEmbed pre-check and refresher, the cooldown and spend cap, and moving profile content out of git.

  Package figures:

  | Package | Rev 5 | Rev 6 |
  |---|---|---|
  | zcash_voting | 17-18 | 12-13 |
  | Vizor | 13-14 | 9-10 |
  | vote-sdk | about 20 | 12.5-14 |
  | Verifier | about 10 | 5.75-7 |
  | Circuits | 8-9 | 7-8 |
  | Specs | about 6 | 3.5-4.25 |
  | QA | about 5 | 3.5-4.25 |
  | Config repo, deeplink server | S each | S, none |

  The largest single cuts are the one-key registry (about 4-5 across packages), dropping recovery (about 3), curated profiles with one directory document (about 2-3), DC-only 0x09 (about 2-2.5) and the chain trims (about 3.5-4.5). These and the smaller cuts in `archive/review/rev6-simplification.md` overlap, so all the cuts together add up to more than the total saving.
- **Earlier estimates:** rev 1 was 80-85, rev 3-4 83-86 and rev 5 79-84 [inference].
- **Staffing:** circuits 1-2, chain 2, client 2, Vizor 2, identity 1-2, specs/QA 1.

### 8.3 Activation

1. Merge dormant PRs to main under `V:state/breaking`.
2. Confirm the voting-circuits 0.13.0 release commit passed the CI fingerprint gate, `cargo test --release -- --ignored vk_fingerprint_unchanged` for ZKP1-4 (§4.1). These tests are `#[ignore]`d, so a plain `cargo test` never runs them.
3. Run the corpus replay gate: the candidate binary's FFI verifiers check every vote tx from recent mainnet rounds (ZKP1/2/3 proofs, RedPallas, TX1 sighash) with byte-identical accept/reject results against v1.6.x. This covers the Zakura 2.0 dependency swap (OPS-5).
4. Run the coordinated v1.7.0 halt **between rounds**: all rounds FINALIZED and helper queues empty. Every later upgrade halt follows the same rule.
5. Post-checks: capabilities on (`proxy_delegation_version = 1`, and the four fingerprints match the values the gated tests pin); the registry is empty.
6. Coordinator actions:
   - Confirm at least 2 vote managers in the coordinator policy (add one with `MsgUpdateVoteManagers` if needed), because every proxy payload needs 2 approvals (D7).
   - Set params: the verifier set (Valar's key, threshold 1) and `max_delegates` 20,000.
   - Reach 2-of-3 within the first two proxy rounds.
7. Register the canary delegate and publish the live directory to both mirrors. The first proxy round's entry pins the launch vetted list, reviewed once real influencers have onboarded, before M10 (§8.4).
8. Per round: create proxy rounds with the `MsgCreateVotingSession` proxy flag (at least 2 approvals), and verify the created round's field 31. In the same config PR as the round entry, add its proxy entry: run the curator tool, get Valar and Vizor approval for the vetted list, and sign the entry with the round (§4.7).

**Mid-round off switch (D9, decision 38).** One lever: `MsgSetProxyDelegationPause {paused: true}` (at least 2 approvals, D7).
- **What the chain does:** it rejects every new 0x09 at once, at ante step 2, without spending the VAN, including batches that were already committed.
- **What Vizor does:** it reads the pause before preview and before every commit, hides the delegation entry points and shows "Paused" (§4.4 Gating). A committed batch that the chain rejects as paused is retried if the pause lifts before `vote_end_time`, or released so the weight can be voted directly (§4.4).
- **What keeps working:** the round stays active and voting is unaffected. DCs already included are still revealed and count through their delegates' ballots (D2), and delegates can still submit ballots. Lifting the pause resumes delegation.
- **What it cannot do:** it cannot undo existing delegations, which are final because the weight has left the delegator's VAN.
- A heavier "discard delegated weight this round" lever is deliberately not included in v1.

**Rollback.**
- No binary rollback after the first proxy tx.
- Incident levers: the pause above and `MsgSetDelegateSuspension`, which under D7 each need two coordinator approvals, so two coordinators are on call for every proxy round (CRD-1); and creating later rounds without the proxy flag, at the normal threshold.

### 8.4 Stage and beta

- **T1 (internal, plus 3-5 friendly external delegates).** One proxy round with:
  - Vizor (software, Keystone, Ledger), plus a current Zodl build and an old Vizor build;
  - a Keystone-signed registration;
  - an in-window DC at about T−10 min alongside a pre-window DC;
  - a delegate registered mid-round who is found by exact search, receives a delegation and submits a ballot in the same round;
  - a delegate with delegations who submits no ballot (its pool is counted nowhere);
  - a delegate who retires and registers again, and one who re-keys with a new 12-word phrase (the new key votes only in the next round);
  - a pause on/off drill.

  A scripted check confirms that per-option totals match the plaintext model.
- **T3:** the adversarial list plus the load matrix on the ten-validator lab, including:
  - a DC landing in the transition block is rejected with the VAN unspent;
  - a batch re-proved across the window boundary keeps one layout;
  - an incomplete ballot and any second ballot are rejected;
  - a re-registration inside the 14-day cooldown is refused before any paid call, and one from a different account that took a renamed handle creates a new entry instead of re-keying.
- **Real influencers** onboard on mainnet between M9 and M10, with registration on and no proxy round yet; this also seeds the launch vetted list.
- **Then** one mainnet proxy round per Q7 and Q12.

### 8.5 Zodl coordination

Send a written notice listing what Zodl will misreport:
- **M1:** per-option totals include delegated weight, and Zodl cannot show which part was delegated.
- **M2:** `VoteSummary` share counts exclude pools.
- **M3:** a seed that delegated in Vizor shows "already used" in Zodl.
- **M4:** old Vizor classifies "delegated" as "voted".
- **M5:** explorers see new event types.
- **M6:** DB v25 cannot be downgraded.
- **M7:** `VoteRound` gains field 31 (`proxy_delegation`) in proxy rounds, and `MsgCreateVotingSession` gains a proxy flag that only coordinator tooling sets. The dynamic config gains an `extensions` object.

Zodl maintainers then confirm on a current production build that `VoteRound` field 31, the new `MsgCreateVotingSession` proxy flag and the dynamic config's `extensions` object are ignored, and sign off before the first proxy round. Zodl builds 3.13.x and earlier throw on rounds they cannot parse, so mainnet test rounds must stay within that parser's limits until those builds age out. Optional Zodl copy: "Totals include votes cast by public delegates."

---

## 9. Test strategy and launch checklist

### 9.1 Tests by layer

- **Circuits:**
  - the §4.1 suite, including the layout-1 tests;
  - the C14 slot tests: slot 7 accepted, slot 8 rejected, and slot nullifiers distinct over 0..8;
  - the `d = u32::MAX` positive;
  - ZKP1-4 `vk_fingerprint_unchanged`. These are `#[ignore]`d and run in CI with `cargo test --release -- --ignored vk_fingerprint_unchanged` (release gate, §4.1);
  - `vk_fingerprints()` returns the pinned values;
  - real-proof size ≤15 KiB;
  - CI benchmarks.
- **Chain:**
  - **0x09.**
    - Slot nf duplicates: intra-message, across txs in CheckTx and RecheckTx, and genesis type 3.
    - `1 ≤ #DC ≤ 8`: a 9-DC 0x09 is rejected in ValidateBasic.
    - Registration ⇔ anchor 0, in both directions.
  - **Digests and keys.**
    - D1, ballot and registry digest golden vectors (Go = Rust = e2e `sighash.rs`).
    - Prefix-free `SVOTE_` domains.
    - ZIP-215 torsion vectors.
  - **Registry.**
    - `register` needs a threshold of distinct current verifiers and a never-seen key; a replayed attestation fails.
    - The registry is capped at `min(max_delegates, 2^30)`.
    - A mid-round `register` gets the next index at once and can submit a ballot in the running round.
    - A `register` with `delegate_index = d` re-keys entry `d`: same index, new key, ACTIVE; a suspension stays; the old key is rejected at once; the new key's ballot is rejected in rounds created before the re-key and accepted in later ones; a re-key with a different `subject_commit` is rejected.
    - A revoked entry is reactivated by a re-key.
    - A revoked or suspended delegate cannot submit a ballot, and a ballot accepted before a later revocation, suspension or re-key still counts.
  - **Pool reveals.** p=0 reveals are accepted for suspended and revoked delegates and for an index registered after round creation, and rejected for `d = 0` and `d ≥ Next`.
  - **Proposal 0 stays out of results.**
    - Proposal 0 never appears in VoteSummary, TallyResults, 0x0D partials, completeness or `SubmitTally`.
    - `ProposalTally(0)` is rejected.
    - No injected or client message carries a partial decryption of a proposal-0 entry.
  - **Ballot validation.**
    - A ballot is rejected if it has the wrong length or any `options[i] ≥ num_options(i + 1)` (including `u32::MAX`).
    - A ballot is rejected if it arrives at `blockTime ≥ vote_end_time`.
    - Any second ballot, identical or not, is rejected with `ErrDelegateBallotExists`.
  - **Routing.**
    - Every counted pool appears in every proposal's bucket sums exactly once; a revealed pool with no ballot appears in none.
    - Results are decrypted once, with no partial decryption before routing and no re-tally.
    - Routing conserves weight against a plaintext model, and gives byte-identical output under reversed iteration.
    - The identity-C1 vector.
    - `pools_routed` is set once.
    - Pools are routed before any partial decryption is accepted.
  - **Per-block filter.** At most one ballot tx per `(round, d)` per block in PrepareProposal.
  - **Pause and round flag.**
    - `proxy_dc_paused` rejects new 0x09 immediately, with the VAN unspent, while included DCs' reveals and ballots still land and count; clearing it restores acceptance.
    - A `MsgCreateVotingSession` proxy flag writes field 31.
  - **Capabilities.** Capability field 4 is 1 and field 5 matches the compiled fingerprints.
  - **Determinism and genesis.**
    - The single dormancy const gates every new path.
    - Two-node determinism across the transition block.
    - A genesis round trip with a finished and an active proxy round, with `0x1A 02` and `next_index` rebuilt.
    - The droplet routing benchmark.
  - **D7.** Proxy payloads, and a create-session action with the proxy flag, never execute below 2 approvals, even at policy threshold 1.
- **Helper:**
  - leaf-first ordering, with 503 on an absent leaf;
  - a DC to an unregistered index gets the permanent error and is dropped;
  - a layout-1 DC payload (one share, `submit_at = 0`) is scheduled at arrival and revealed;
  - the span-data key test;
  - capacity metrics.
- **e2e:**
  - register, then create a round with the proxy flag;
  - 0x09 with and without registration, and helper reveals;
  - complete ballots, a rejected incomplete ballot, and a delegate with a revealed pool and no ballot (counted nowhere);
  - per-option totals match the plaintext model;
  - split-off re-plan, and exact-tree recovery of an ambiguous 0x09 submission;
  - a Zodl-style flow in the same round;
  - a layout-1 DC landing at T−60 s is revealed and routed, and raises the pool share count by 1;
  - a pool reveal or ballot in the transition block is rejected, and routing completes;
  - a 0x09 with a registration works inside the window;
  - a delegate registered mid-round receives a delegation and submits a ballot;
  - a delegate retires, and another re-registers;
  - a mid-round pause and resume.
- **zcash_voting:**
  - **Planner.**
    - Proptests: conservation, `D ≤ #DC ≤ D + B − 1`, within-1 quota, at most 8 delegates, `dc_slot` never above 7, and no two DCs to one delegate in a bundle.
    - The sequential fill matches `prototypes/client/compare_packers.py`.
    - No API amends a committed allocation.
  - **Secrets.** `hrk` and `dc_seed` vectors for both layouts, with every DC secret derived from the hotkey.
  - **Gating.**
    - Availability refuses new allocations when the chain is paused, the round flag is off, capabilities or fingerprints mismatch, or authenticated timing is missing.
    - Committed work continues.
    - Gates never stop committed work.
  - **Phrases.** 12-word delegate phrases never validate as BIP-39, restore rejects BIP-39, and derivation matches its vectors.
  - **Release path.** The state machine, including a paused batch.
  - **Timing and layout.**
    - No designated immediate DC share.
    - `VoteEnded` at `now ≥ vote_end` while in-flight work advances.
    - Layout = 1 iff `is_last_moment()` at batch planning.
    - The batch layout is persisted before the first proof and reused across terminal-rejection re-prove and split-off re-plan, with byte-identical DCs.
  - **Share delivery.**
    - A layout-0 DC dispatched in the window plans 16 shares at `submit_at = 0`.
    - Layout-1 delivery plans exactly one payload at `submit_at = 0` with explicit `single_share`.
  - **Status and ballots.**
    - Ballots are fetched only as the whole-round list, with no per-index REST on delegator paths.
    - The status view model maps ballot presence and hand-off correctly, including "not counted" for a delegate with no ballot at close.
    - The delegate ballot builder rejects an incomplete ballot before signing, never submits a different second ballot, and treats `ErrDelegateBallotExists` with an identical stored ballot as success.
  - **Directory and registry.**
    - A search refreshes the directory before matching.
    - Commit refuses a directory older than 10 minutes.
    - The lookalike fold matches its vectors.
    - The round's proxy entry verifies against `trusted_keys`, and the vetted list and pack match their hashes; an entry signed for another round, or a list with another `round_id`, is rejected.
    - The pre-proving chain check rejects an entry whose DK or `subject_commit` differs from the directory, or that is not ACTIVE or is suspended.
  - **Migration and benchmarks.**
    - The v24→v25 migration preserves rows.
    - Mobile benchmarks with peak RSS.
- **Vizor:**
  - **Coverage.** Widget tests for every PD and DG state.
  - **Directory and search.**
    - The browse list shows only the round's pinned vetted list, and a list or proxy entry for another round is rejected.
    - Unvetted entries appear only through exact search, with handle, fingerprint, identicon and "Not reviewed", and never an avatar, display name or statement.
    - "new this round" appears for indices at or above `next_delegate_index_at_creation`.
  - **Counts and totals.** No screen except the delegate's own dashboard shows a delegation count, and none shows a ZEC total. DG-8 shows the count as "at least N".
  - **Delegation flow.**
    - The picker stops at 8 delegates.
    - `tooLate` fires only after `vote_end`, and there is no 10-minute block.
    - PD-6 waits for the in-window DC reveal and fails at `vote_end`.
    - The paused state and its release offer.
    - "Vote with the ZEC you kept" opens the vote flow after a `KeepForSelf` delegation.
  - **Status and results.**
    - PD-7 shows "Handed off", the delegate's published ballot or "No ballot yet", and after close "Counted" or "No ballot. Not counted this round.", with no reveal counter and no per-proposal status.
    - The "Counts only with a ballot" copy appears on PD-5 and PD-7.
    - PD-8 shows no delegate names.
  - **Delegate mode.**
    - DG-9 keeps Submit disabled until every question has an answer, and offers no per-question abstain.
    - DG-10 lists every choice and requires re-auth, and a submitted ballot is read-only.
    - DG-11 Retire requires re-auth and a confirm, and DG-2 "Register again" re-keys the same entry.
    - A delegate whose key changed during the current round is not offered for delegation, and shows "Changed keys. Can vote again next round."
  - **Copy.** The §4.5 copy keys, including "Not browsable" and "Retire". No string claims that a delegator's amount privacy equals a direct voter's.
  - **Images and network roles.**
    - The `Image.network`/codec ban.
    - Each avatar's hash is checked against the round's vetted list.
    - Route-table audit, including the mirror and verifier network roles.
  - **Chain checks.**
    - Availability follows the chain pause both ways, including an in-flight DC that still completes.
    - The participation reader verifies registry entries against golden key vectors and rejects modified proofs, wrong keys and stale headers.
  - **Other.** Deletion cleanup and accessibility.
- **Verifier:**
  - reply, quote, retweet and template-mismatch proofs are rejected;
  - an attempt that fails the free oEmbed check, or an account inside its 14-day cooldown, causes no paid call;
  - the signer refuses mismatched fields, re-checks the post and makes the one paid lookup itself, stops paid calls at the daily spend cap, and attests a re-key for an account that already has an entry;
  - the refresher delists a registration whose post returns 404 within 24 h, and never delists on network errors or 5xx;
  - `directory.json` carries current handles for every registered delegate, and its flags can hide but never add a vetted entry;
  - a purge regenerates the document on both mirrors;
  - the directory regenerates within about 10 minutes of a chain registry change;
  - the curator tool fetches vetted pictures once per round, falls back to an identicon on a parse failure, rejects a malformed image, re-encodes deterministically, and writes no profile content to git;
  - Vizor shows a picture, name, statement or handle only when it matches the round's pinned hash.
- **Config repo:**
  - CI rejects a proxy entry whose signature, list hash or pack hash fails, or whose list names another round;
  - CODEOWNERS block a merge without Valar and Vizor approval;
  - current Zodl and old Vizor builds parse a dynamic config that carries the extension.
- **Adversarial (stage T3):**
  - **Circuit and 0x09 abuse.**
    - ZKP4 after a partial vote; ZKP2 and ZKP4 on one VAN; one VAN twice in a batch.
    - Field-wrap inflation.
    - A ZKP4 with `dc_slot = 8`; a 9-DC 0x09.
    - A VC revealed as proposal 0, and a DC as proposal p.
    - Proposal-0 entries in 0x0D or `SubmitTally`.
    - 8-DC self-delegation to inflate a reveal count (visible only as an approximate count on that delegate's dashboard).
  - **Timing and pause.**
    - A DC landing in the transition block (rejected, VAN unspent).
    - A batch re-proved across the window boundary.
    - A 0x09 sent while paused (rejected, VAN unspent).
  - **Ballots.** Ballot replay, races, incomplete ballots, second ballots, ballots from revoked keys, and fresh-signature ballot floods.
  - **Registry and verifier.**
    - Registry spam, expired-attestation reuse, double-counted signers and replayed attestations.
    - Re-registration inside the cooldown, and a rename to dodge it (at most one paid call, then refused).
    - Retweet binding.
  - **Reveals and helpers.**
    - Reveals to unregistered indices, and DCs to unregistered indices.
    - Targeted helper censorship of one index.
  - **Encodings and capacity.** Non-canonical encodings, tree fill and an oversized batch.
  - **Directory and identity.**
    - A directory `seq` rollback, and a proxy entry or vetted list replayed for a different round.
    - An unvetted lookalike reached by exact search (lookalike warning, no avatar or display text), and a lookalike of a vetted handle.
    - Directory-key relabelling of an index or key, caught by the pre-proving check.
  - **Anchors.** Mixed-anchor front-running.
  - **Compatibility.** Zodl and old Vizor in the same round.

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

Rev 6 drops L7 (directory CDN at 50k wallets; one static file behind a CDN) and L8 (verifier spike at the daily cap; the signer budget caps it) as lab scenarios; the verifier budget gets a unit test.

Pass criteria:
- every honest share accepted by helpers at least 10 min before `vote_end_time` is revealed in time with at least 30% headroom; for later shares, the reveal-before-close rate and latency are reported by class (vote, pre-window DC, in-window DC), because no fleet can guarantee shares posted in the final seconds;
- Zodl direct-vote loss and latency do not regress against L1/L1b, with in-window DCs present;
- block time p99 is at most 3 s;
- routing EndBlock is within the budget set from the droplet benchmark;
- no app-hash divergence;
- proofs per unique reveal are measured and reported.

### 9.2 Launch checklist

- [ ] **Specs.** contracts-v1 frozen. Golden vectors pass in Go, Rust and e2e. ZIP-PD published, with its registry section, the complete-ballot rule, routing on every proposal for counted pools only, and the one-final-tally rule (Q19). Book privacy and threat pages carry the §5 wording, including pool solvability and the validator trust assumption.
- [ ] **Releases and audits.**
  - Both audit tranches closed (or the merged tranche).
  - voting-circuits 0.13.0, vote-sdk v1.7.0, zcash_voting and the Vizor store release are final.
  - The CI fingerprint gate (`cargo test --release -- --ignored vk_fingerprint_unchanged` for ZKP1-4) passed on the release commit, and fingerprints for all four circuits are published.
  - ZKP4 rows re-measured on the release circuit.
- [ ] **Corpus replay and stage.** Corpus replay gate passed. Stage T1 and T3 passed, including:
  - Zodl and old Vizor;
  - per-option totals against the plaintext model;
  - an in-window DC and a mid-round registrant;
  - a delegate who submits no ballot;
  - rejected incomplete and second ballots;
  - a retire and a re-registration;
  - a pause/resume drill.
- [ ] **Load.** Load gate passed. Helper workers set to the measured headroom.
- [ ] **Mainnet upgrade.**
  - Applied between rounds; the upgrade runbook keeps that rule for every later halt.
  - Capabilities verified (`proxy_delegation_version = 1`, fingerprints).
  - At least 2 vote managers in the coordinator policy; proxy payloads need 2 approvals.
  - Params set: verifier set at threshold 1, `max_delegates` 20,000.
- [ ] **Verifier hardening live.**
  - The free oEmbed pre-check and the 14-day cooldown run before any paid call.
  - The signer makes the one paid lookup on its own host, under the daily spend cap (about $5 at launch).
  - The refresher reads every registration post daily through oEmbed and delists on 404 within 24 h.
  - The picture tool's guardrails are in place: vetted delegates only, once per round, identicon fallback.
  - The publisher uses a read-only database role.
- [ ] **Legal.** Sign-off recorded. Purge tested end to end on both mirrors.
- [ ] **Vetted list and directory.**
  - Every launch vetted entry's fingerprint OOB-confirmed and its statement collected under the content terms; the launch list's PR approved by Valar and Vizor, and pinned by the first proxy round's signed entry.
  - Reserved-names list seeded.
  - Directory on two mirrors, regenerating within about 10 minutes of chain changes.
- [ ] **Dynamic config extension.** `extensions.proxy_delegation_v1` shape and signatures checked in CI (§4.7); CODEOWNERS require Valar and Vizor approval; current Zodl and old Vizor ignore it. No new static pin; old pins untouched; `supported_versions` unchanged.
- [ ] **Zodl.** Written notice sent and sign-off received.
- [ ] **Off switch.** `proxy_dc_paused` tested mid-round:
  - the chain rejects new DCs with the VAN unspent;
  - Vizor shows "Paused" and stops new allocations, while committed batches retry or are released;
  - included DCs are counted and ballots are accepted;
  - resume works.

  No remote flag gates proxy delegation. Two coordinators on call for each proxy round.
- [ ] **Dashboards live:** pool reveals, ballots, helper queue by class, permanent p=0 rejections, verifier signer budget and log reconciliation, X API errors, directory freshness.
- [ ] **Canary.** Delegate and delegator scheduled for the first round, with a pre-window and an in-window DC; the canary asserts that its shares and ballot landed.
- [ ] **Public docs published.**
  - **Delegator FAQ:**
    - one delegation per round, and finality;
    - delegated ZEC counts only if the delegate submits a complete ballot before voting ends;
    - last-moment behavior, which is the same as voting, including that a delegation sent in the final minute may not be counted;
    - what is public: delegate ballots and approximate delegation counts, while per-delegate totals are not shown;
    - Vizor shows the delegate's ballot as published, without proofs;
    - a delegator's amount can be computed from public results when zero-direct options determine its delegate's pool and it is the only delegator. In small rounds this is somewhat more exposure than a direct vote has; use the §5 FAQ wording and its simulation figures;
    - splitting limits any exposure to a slice;
    - device loss: a reinstall before "Handed off" loses the delegation, and the kept remainder can be voted only from the original install.
  - **Delegate guide:**
    - phrase safety, and what happens if the phrase is lost or leaked: re-registering under a new number with a new phrase;
    - Tor;
    - one complete ballot per round, which cannot be changed;
    - missing the deadline leaves delegated ZEC uncounted for the round, and sitting out means not submitting;
    - public and permanent attribution;
    - the approximate count on the dashboard;
    - vetted versus unvetted discovery, and sharing a handle or fingerprint.
  - A no-receipt-freeness disclosure.
  - A support runbook with escalation to coordinators, including the key-loss override.
- [ ] **App store.** UGC review passed (report link, hide, curated vetted-only profiles, contact, terms).
- [ ] On-call owners named for chain, helper, verifier and Vizor.
