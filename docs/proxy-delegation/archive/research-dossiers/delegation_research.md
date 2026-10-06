# Proxy delegation: external research dossier on private vote delegation

Scope: survey of private and public vote-delegation designs, mapped onto our shielded-voting system (VAN/VC/shares, Poseidon VCT, Pallas ElGamal with threshold decryption by validators). "Proxy delegation" means giving voting power to another person. It is distinct from today's `MsgDelegateVote`/ZKP1, which moves note weight into a VAN bound to a hotkey.

Evidence labels: **[CODE]** means what the code does today, **[DOC]** means what our specs or docs claim, **[EXT]** means an external source, and **[INF]** means my own inference.

---

## 1. Baseline facts about our system that constrain any design

| # | Fact | Evidence |
|---|---|---|
| B1 | Vote transactions skip the Cosmos SDK Tx envelope (no accounts, no fees). They are authenticated only by RedPallas signatures plus a ZKP. | [CODE] `x/vote/ante/validate.go:1-14` |
| B2 | Messages today: `DelegateVote` (ZKP1), `CastVote`/`CastVoteBatch` (ZKP2), `DelegateAndCastVoteBatch`, `RevealShare` (ZKP3), `SubmitTally`, `SubmitPartialDecryption`. | [CODE] `proto/svote/v1/tx.proto:16-22` |
| B3 | Tally state is one encrypted accumulator per (round, proposal, decision), built by `AddToTally`. The first ciphertext must be non-identity. A public share count is also kept per bucket. | [CODE] `x/vote/keeper/keeper_tally.go:33-60`, `:135-156` |
| B4 | `MaxProposals = 50` (51-bit bitmask, bit 0 is a sentinel). There are 16 shares per VC and at most 8 options per proposal. `TallyBSGSBound = 2^28` ballots per bucket. A tally timeout finalizes with empty results. | [CODE] `x/vote/types/keys.go:31,33-35,45-48,50-52,60-63` |
| B5 | 1 ballot = 0.125 ZEC (`BALLOT_DIVISOR = 12_500_000`), so splitting across 10 delegates needs at least 10 ballots. | [CODE] `voting-circuits/src/params.rs:5,14` |
| B6 | A round has a single `vote_end_time`. There is no separate delegation, delegate or override deadline. `ea_pk` is per round, so any encrypted pool is per round. | [CODE] `proto/svote/v1/types.proto:41,46` |
| B7 | Threshold decryption uses Feldman commitments with a threshold of at least 2. The ZIP says t = ceil(n/2)+1. | [CODE] `types.proto:62-67`; [DOC] ZIP `adam_voting-protocol-client-delay` L179-185 |
| B8 | The VAN nullifier is `Poseidon(vsk.nk, "vote authority spend", round, van)`. Only the hotkey holder can compute it. | [DOC] ZIP L330-333 |
| B9 | In ZKP2, `proposal_id` is public and `vote_decision` is private. The decision becomes public, per share, at reveal (ZKP3), and the chain adds the share into `agg[proposal][decision]`. So in our system **the choice is public per share but the amount is hidden and unlinkable**. This is the reverse of Kite/Helios-style encrypted unit vectors. | [DOC] ZIP L772-806, L965-970, L1140-1146 |
| B10 | Submission servers learn the proposal and decision of the shares they reveal. | [DOC] ZIP L186-190 |
| B11 | The voter chooses the ElGamal randomness `r_i` for every share, and the ciphertext is posted on chain. A voter can therefore prove a share's plaintext. **The system is not receipt-free.** Receipt-freeness and coercion resistance are not listed requirements. | [DOC] ZIP L377-395 (r_i private witness), Requirements L200-215; [INF] |
| B12 | The custody handoff already lets a funds controller create a VAN for someone else's hotkey and deliver `num_ballots` and `van_comm_rand`. This is a working "transfer full weight to another person's key" primitive. The receiver can check it, so it is also a verifiable vote-sale primitive. | [CODE] `zcash_voting/src/delegation_capability.rs:46-52, 338-363`; [DOC] `zcash_voting/docs/exporting-to-external-software.md:1-60` |
| B13 | A new VAN reuses the old VAN's address and `gov_comm_rand`. The ZIP calls this safe because those fields are never externally observable. That argument fails whenever a third party built the VAN: the custody controller today, or a proxy delegator in any VAN-transfer design. That party can recompute successor VAN commitments (only the bitmask changes) and watch the VCT to learn **which proposals** the holder voted on and when. | [DOC] ZIP L1470-1480; [INF], to be verified by the circuits/code panel |
| B14 | The ZIP and the book anticipate "partial delegation" as a VAN-to-VAN split into a delegate VAN plus a change VAN. They mark it unspecified. | [DOC] ZIP L1506-1530; `greg_shielded-voting-fixes` ZIP L1598-1603; book `delegation/delegation-setup.md:1-34`, `partial-delegation.md:1-38` |
| B15 | Shares are split 16 ways for two reasons: timing unlinkability, and to limit what a key-holding EA learns about any single voter. In the last-moment buffer a single full-weight share is used. The buffer is `min(10% of window, 3600 s)`, and `submit_at` is uniform up to `vote_end_time - buffer`. | [DOC] ZIP L1394-1404, L249-257; submission-server ZIP L277, L296 |
| B16 | Drift: the ZIP drafts still say `proposal_id in {1..15}`, but the chain allows 50. | [DOC] both shielded-voting ZIP branches; [CODE] `keys.go:48` |
| B17 | ZKP1 needs a SpendAuthSig from the Orchard key each round (a Keystone signature for hardware users). A "standing" delegation that carries across rounds is therefore impossible for hardware users without signing again each round. | [DOC] ZIP L1311-1332; [INF] |

---

## 2. Survey of mechanisms

### 2.1 Kite (Nazirkhanova, Gunjur, Cruz-De Jesus, Boneh; arXiv 2501.05626v4, Mar 2026; FC 2026 preproceedings)
- **Mechanism.** The contract holds one additively homomorphic ElGamal ciphertext per registered delegate (`Ld`). To delegate, a voter posts an encrypted vector that is zero everywhere except at the chosen delegate, and the contract adds it homomorphically. The proof `R_del` shows a Merkle-proven balance and well-formedness. The circuit is linear in the number of delegates, so the implementation uses an **anonymity set** of T delegates (5, 10 or 20); T = 1 is public delegation. Delegated tokens are locked. Delegations are snapshotted (`Reid`) at election start. (Sec. 3, 3.3-3.4, 5, Alg. 3-4)
- **Delegate voting.**
  - Public mode: the contract adds the delegate's encrypted pool to the chosen option. The delegate does O(1) work regardless of how many delegators it has.
  - Private mode: the delegate posts `Rerand(pool)` in the chosen slot and `Enc(0)` elsewhere, with proof `R_vote`. This is the re-encryption plus disjunctive-proof pattern. (Sec. 3.5, App. B, eq. 2)
- **(a) Verification.** In public mode a delegator sees the delegate's vote and knows the whole pool moved. Kite argues private delegate voting "makes it harder for voters to hold their delegates to account". Remark 5 says delegators should monitor delegate activity, which is public even in private mode. The paper's future work names exactly the user's middle ground: keep the delegate's vote confidential from the public but reveal it to that delegate's delegators. (Sec. 1 L86-88, Remark 5, Sec. 7)
- **(b) Coercion.** Delegations are hidden by default but a voter can later prove how they delegated. The authors explicitly do not claim receipt-freeness or coercion resistance. (Sec. 1)
- **(c) Override.** None. A delegator's tokens are locked and they cannot vote. Undelegation re-presents `ct` (matched by hash) and is subtracted homomorphically. Only delegations at election start count. Remark 7 sketches vote changes for delegates.
- **(d) Transitivity.** None. "Each voter can only delegate their own voting power", so cycles never occur.
- **(e) Scalability.** The delegate does O(1) work. The tally is a single threshold decryption per option.
- **(f) Visibility.**
  - Public: "voter X delegated" (to someone in the anonymity set).
  - Delegate: learns nothing, including whether anyone delegated to it.
  - Per-delegate totals stay encrypted. The tally publishes **percentages only** (or only the winner) so small delegate sets do not leak totals. (Sec. 3.6, App. C.4)
  - Remark 3: over-delegation risk, mitigated by publishing DP-noised approximate powers.
  - Remark 4: split delegation by using multiple accounts.
  - Remark 6: topic-based delegation.
- **Performance.** Noir/PLONK on BabyJubJub on an M2 MacBook Air: delegation takes 7-167 s depending on T; proofs are 4,288 B; verification is about 400k gas. (Sec. 6, App. C.6)

### 2.2 Penumbra
- **Mechanism.** Validators' votes are public and act as the default for their whole delegation pool. Delegator votes are anonymous and override the delegator's portion: the delegator's power is subtracted from the validator's tally, whatever the order of votes. Eligibility is a delegation note that existed before voting started. Governance votes use a separate nullifier set, and a note can be spent and still used to vote. Clients should roll notes over so their votes on different proposals cannot be linked. [protocol.penumbra.zone/main/governance.html]
- **What a DelegatorVote reveals.** Proposal, start position, the vote, the staked value (amount and the validator-specific asset), the unbonded amount, the nullifier and rk. "The voting power ..., the vote, as well as the proposal ... is revealed." [protocol.penumbra.zone/main/governance/action/delegator_vote.html]
- The concepts page describes an older design that encrypted delegator votes to a validator threshold key. The action spec says the vote is plaintext, and I treat the action spec as authoritative. [protocol.penumbra.zone/main/concepts/governance.html]
- **(a)** Delegators verify validator votes trivially (public). **(c)** Delegator precedence, order-independent. **(d)** One level. **(e)** Validators do O(1) work. **(f)** Which account stakes to which validator is hidden ("not possible to see which accounts are staking to which validator"). Validator totals are public, and override amounts and choices are public but anonymous. [penumbra.exchange blog, delUM]

### 2.3 Cosmos SDK x/gov (and AtomOne)
- "If a delegator does not vote, it will inherit its validator vote. If the delegator votes before its validator, it will not inherit ... after its validator, it will override." Weighted split votes are supported (ADR-037). Since v0.53 the tally function is pluggable. [docs.cosmos.network/sdk/latest/modules/gov/README.md]
- Everything is public. Verification is trivial. Override is order-independent with delegator precedence. Validators are not punished for not voting, and if a validator does not vote, its non-voting delegators count for nothing.
- AtomOne removed inheritance entirely: validators vote only with self-stake. This is a counterpoint showing concern about concentrated delegate power. [allinbits.com/blog/atomone-v4-technical-reference; pkg.go.dev atomone x/gov]

### 2.4 Namada
- "Delegates will be able to vote only for 2/3 of the total voting period, while delegators can vote until the end." A delegator's divergent vote is subtracted from its delegate. [specs.namada.net/modules/governance/on-chain]
- **This is the cleanest "see, then override" schedule.** Delegators learn the delegate's vote before their own deadline.

### 2.5 Snapshot ("ERC-20 Votes With Override") and Shutter
- If the delegate votes after the delegator, the delegator's balance is deducted from the delegate's power. A delegator always keeps access to their own balance. The rule is order-independent with delegator precedence. Everything is public. [discuss.ens.domains/t/.../11385]
- Shutter shielded voting on Snapshot threshold-encrypts ballots to keypers until the end, then they are decrypted and counted (temporary privacy). Shutter's announced "permanent shielded voting" moves to threshold-homomorphic ElGamal plus ZK. Neither addresses delegation; Snapshot delegation strategies run over plaintext. [shutternetwork.discourse.group/t/.../761; decrypt.co/105201]

### 2.6 MACI
- Anti-collusion comes from secret key-change messages processed in reverse order by a trusted coordinator. A bribed voter can secretly invalidate a vote, so receipt-freeness holds except toward the coordinator. MACI has no delegation, and the coordinator is a trust point our threshold-validator model does not have. [maci.pse.dev/docs/core-concepts/spec]

### 2.7 Cicada (Glaeser, Seres, Zhu, Bonneau; ePrint 2023/1473; a16z)
- Homomorphic time-lock puzzles give running-tally privacy, and ballot privacy only for the duration of the vote. There is no delegation. It cites Juels et al.: coercion resistance is impossible in a permissionless setting without trusted hardware. [a16zcrypto.com/posts/article/building-cicada-...]

### 2.8 Vocdoni / DAVINCI
- zkSNARKs plus threshold ElGamal. Silent vote overwrite ("last vote counts") gives anti-coercion, and sequencers re-encrypt ballots for receipt-freeness. [blog.vocdoni.io/davinci-universal-voting-protocol; hackmd.io/@vocdoni/BJY8EXQy1x]
- Vocdoni App 2.3 "vote delegation" is a representative-mandate feature, and "a dedicated delegation workflow will be introduced in a future update". It is not a cryptographic private-delegation design. [vocdoni.io blog 2.3]

### 2.9 Aztec/Aragon Nouns private voting
- Storage proofs establish ownership or delegation. Delegates vote with delegated Nouns, and delegation is either/or: delegators cannot vote independently. Ballots use timelock encryption. The report lists "non-deterministic proof" (anti vote buying) and "vote recast" (anti-coercion) as future work. [research.aragon.org/nouns-tech.html; aztec.network/blog/the-time-nounsdao-got-private-voting]

### 2.10 Academic delegation schemes
- **Treasury system (Zhang, Oliynykov, Balogun; NDSS 2019).**
  - Each voter posts an encrypted unit vector of length m+3: m expert slots plus Yes/No/Abstain. Experts post encrypted votes.
  - The committee **decrypts the per-expert delegated totals** D_i, then raises each expert's encrypted vote vector to D_i (scalar multiplication by a public plaintext) and adds direct votes.
  - Result: the expert's vote stays private, the per-expert totals are public, and voter stake is public. This is the inverse of Kite's public-vote mode. [ndss-symposium.org ... Zhang_paper.pdf, Fig. 4, 5, 7]
- **Statement voting (Zhang and Zhou; FC 2019; ePrint 2017/616).** Ballots are conditional statements resolved at tally. Liquid democracy (transitive delegation) is a special case. Full secrecy, with per-ballot tally work. [eprint.iacr.org/2017/616]
- **Kovalchuk et al. 2025 (ePrint 2025/803).** UC-secure on-chain quadratic voting with liquid democracy. Per Kite, these schemes process every ballot individually at tally. [eprint.iacr.org/2025/803]
- **Kulyk et al., "Introducing Proxy Voting to Helios" (ARES 2016).**
  - Delegation tokens are forwarded anonymously to proxies and validated only at tally after mixing, so a proxy cannot prove how many valid delegations it holds.
  - A voter can cancel by voting directly at any time before tally (delegator precedence).
  - The proxy has to submit every token, so its work is O(n). [publikationen.bibliothek.kit.edu/1000081973]
- **Kulyk et al., "Coercion-resistant proxy voting" (IFIP SEC 2016 / Computers & Security 2017).**
  - Fake delegation credentials (JCJ-style).
  - Up to T prioritized delegations; only the highest-priority one counts, so there is a backup order.
  - Cancellation by issuing the highest-priority unused credential. [publikationen.bibliothek.kit.edu/1000081964]
- **Nejadgholi, Yang, Clark, "Ballot Secrecy for Liquid Democracy" (FC Workshops 2021).**
  - Under full secrecy, three problems arise: cycles go undetected, delegates cannot see incoming weight, and delegates are unaccountable.
  - Fixing any of these with an oracle (cycle detection, incoming-weight count, voting-action visibility) **breaks coercion resistance**. For example, a coercer delegates to Alice and checks the cycle oracle, or watches the weight oracle tick up by one. Noise does not help against repeated queries.
  - Coercion resistance, no cycles, knowledge of incoming weight, and delegate accountability cannot all be had together.
  - Their alternative is a multi-round design: you may delegate only to someone who has already voted publicly. [pulpspy.com/papers/2021_voting.pdf, Sec. 4-5]
- **Li and Pournaras, "Resilient Liquid Democracy" (arXiv 2607.01730, Jul 2026).**
  - Delegations are sealed with decentralized timed-release encryption while they form, then revealed for tally.
  - Voters choose **ranked backup delegates plus a personal fallback ballot**.
  - In their experiments, votes lost to delegate failure fell from 26% to about 3%. [arxiv.org/abs/2607.01730]
- **Utke and Schmidt-Kraepelin (NeurIPS 2023).** Social-choice rules for ranked or fractional multi-delegation. Useful if "split among 10" becomes a ranked backup list. [research.tue.nl ...]
- **Karoukis (arXiv 2302.14421).** A Merkle-chained commitment scheme for reversible delegation with secret voting power. [arxiv.org/abs/2302.14421]

### 2.11 DAO practice and delegation markets
- **Optimism/Agora "Alligator" advanced delegation.** Partial delegation to multiple delegates, plus sub-delegation chains with absolute or relative allowances, max-redelegation rules and allowlists. It shipped an allowance-calculation bug that under-counted power when absolute and relative chains intersected; Governor Update Proposal 2 fixed it. **Split and transitive accounting is bug-prone even in plaintext.** [gov.optimism.io/t/.../8164]
- **Cardano CIP-1694 DReps.** DRep votes and delegations are public. There are predefined "Abstain" and "No Confidence" DReps. Under the `drepActivity` rule, an inactive DRep's delegated stake drops out of active stake. [cardano.org/glossary/drep-activity-period; forum.cardano.org]
- **Polkadot OpenGov.** Delegation is per track, and you cannot vote directly on a track while delegating it (undelegate first). [support.polkadot.network]
- **LobbyFi (Arbitrum).** Holders delegate to LobbyFi, which sells the pooled voting power per proposal by instant buy or auction. Example: about 19.3M ARB of votes bought for 5 ETH. **When delegate behavior is public and verifiable, delegation pools become vote-market aggregation points.** [forum.arbitrum.foundation/t/.../28934; defillama.com research]
- **Dark DAOs.** Daian, Kell, Miers, Juels (2018) [hackingdistributed.com 2018/07/02]. Austgen et al. (2023) built a TEE prototype that buys votes while hiding the cartel. [arxiv.org/abs/2311.03530]
- **Vitalik, "Moving beyond coin voting governance" (2021).** Vote buying is "the deep fundamental vulnerability" of coin voting. Delegation helps small-holder apathy but not vote buying. [vitalik.eth.limo/general/2021/08/16/voting3.html]

### 2.12 Zcash coinholder voting today
- Zodl 3.5.0 shipped beta shielded coinholder polling, built with the Valar team. Keystone signs on device. Only results are public. [zodl.com/zodl-3-5-0-vote-with-your-zec]
- The NU7 poll ran Aug 25 to Sep 14, 2026, with about 2.4M of about 3.6M eligible ZEC participating. Exact per-option totals are published at 0.125 ZEC precision, e.g. 2,375,932.375 vs 22,384.875 ZEC. [unchainedcrypto.com; cryptoslate.com]
- Existing Zcash polling has no delegation to people. The only "delegation" is to a hotkey or a custody voter (B12).
- [INF] Because exact totals are published, small buckets can expose pool sizes in any design where delegate routing is public (see 4.2).

### 2.13 Summary matrix

| System | Delegator sees delegate's vote? | Override | Transitive | Delegate per-delegation work | Delegate learns amounts | Public sees per-delegate totals |
|---|---|---|---|---|---|---|
| Kite (public mode) | Yes (public) | No (undelegate before snapshot) | No | O(1) | No | No (encrypted; percent tally) |
| Kite (private mode) | Participation only | No | No | O(1) + OR-proof | No | No |
| Penumbra | Yes (public) | Yes, delegator precedence | No | O(1) | Validator stake public | Yes (stake) |
| Cosmos x/gov | Yes | Yes, delegator precedence | No | O(1) | Yes (public) | Yes |
| Namada | Yes, before own deadline | Yes, until end; delegates stop at 2/3 | No | O(1) | Public | Yes |
| Snapshot override | Yes | Yes, order-independent | No | O(1) | Public | Yes |
| Treasury (NDSS19) | No (expert vote private) | No | No | O(1) | No individual amounts | **Yes (decrypted)** |
| Kulyk Helios proxy | No | Yes, any time before tally | No | O(n) tokens | Cannot verify count | No |
| Aragon/Aztec Nouns | Public delegation | No (either/or) | No | O(1) | Public | Public |
| MACI / Cicada / DAVINCI | n/a (no crypto delegation) | n/a | n/a | n/a | n/a | n/a |

---

## 3. Cross-cutting lessons for questions (a) to (f)

- **(a) Verification.** Every deployed system that lets delegators check their delegate does it the same way: **the delegate's vote is public and attributable, and the delegate's whole pool moves atomically** (Penumbra, Cosmos, Namada, Snapshot, Kite public mode). No deployed or published scheme achieves "visible only to my delegators" (Kite lists it as open). [INF] Any such scheme is visible to anyone who delegates the minimum (one ballot, 0.125 ZEC), so in practice it is public.
- **(b) Coercion and vote buying.**
  - Literature: accountability and coercion resistance conflict (Clark et al.; Ford via Clark). With public delegate votes, a briber can check a bribed delegate.
  - Our system: there is **no receipt-freeness to lose** (B11), and full-weight VAN transfer to a buyer is already possible (B12).
  - Hiding the delegate's choice does not stop a briber. The delegate knows its own randomness and can prove its choice, unless a third party re-randomizes (as DAVINCI sequencers do). Hiding it only blinds the delegators.
  - [INF] For public figures, public delegate votes are therefore the better trade. The remaining risk is market aggregation (LobbyFi) and concentration (Kite Remark 3, AtomOne).
- **(c) Override.**
  - Two patterns exist: delegator precedence that is order-independent (Penumbra, Cosmos, Snapshot), and the Namada schedule (delegate deadline, then a delegator window).
  - Kite and Polkadot forbid override. Kulyk allows cancellation any time before tally.
  - In a private system, override means proving "I am withdrawing my contribution from pool D for proposal p" without double withdrawal. That needs a per-(contribution, proposal) nullifier.
- **(d) Transitivity.** Practical systems are one level deep. Transitive systems (LiquidFeedback, Alligator) hit cycles, accounting bugs and secrecy problems (Clark et al.). VAN transfer is transitive by nature but cannot form cycles, because weight moves rather than pointing.
- **(e) Scalability.** Homomorphic per-delegate accumulators (Kite) or chain-side tallies (Cosmos, Penumbra) give O(1) delegate work. Note- or token-forwarding (Kulyk, VAN transfer) gives O(n) delegate work.
- **(f) Visibility.** The privacy knobs are independent of each other:
  1. Who delegated (hidden in our system by VAN anonymity).
  2. To whom (tagged, anonymity set, or fully hidden).
  3. How much (encrypted).
  4. How many delegators each delegate has (a count oracle; Clark warns it enables coercion checks).
  5. Each delegate's total (Treasury reveals it; Kite hides it and reports percentages).
  6. The delegate's choice (public or encrypted).

---

## 4. Candidate architectures mapped onto our system

### Architecture A: VAN split and transfer ("bearer delegation", the book's extension)
- **Mechanics.**
  - A new hotkey-signed ZKP spends the delegator's VAN and outputs up to k delegate VANs, each bound to a delegate's published voting address with `num_ballots = w_j`, plus a change VAN. The bitmask is inherited.
  - The delegate must learn each VAN's opening. That needs either an on-chain encrypted note (a new ciphertext plus trial decryption, like Orchard) or off-chain delivery as in the custody handoff.
  - Before voting the delegate either votes each VAN separately (P × (ZKP2 + 16 ZKP3) per delegator) or runs a new **merge** circuit (m VANs to 1). Merge needs identical bitmasks, so the delegate keeps one merged VAN per bitmask class.
- **Privacy.** Strongest toward the public: delegations look like ordinary spends, and per-delegate totals and counts are invisible. The delegate learns every amount (it opens the VANs), but not the delegator's identity unless delivery is off-chain.
- **Accountability.** Essentially none.
  - The delegate's ZKP2 votes look like anyone else's, and the delegator cannot compute the delegate's VAN nullifier (B8).
  - Via B13, the delegator can still track successor VAN commitments and learn which proposals were voted, but not the decision. That is an accidental participation leak, and also a receipt.
  - Once the delegate merges VANs, tracking breaks.
  - The delegate can **selectively drop** individual delegators' VANs.
- **Override and revocation.** Impossible, because the VAN is a bearer asset. A two-key VAN gives only first-come-first-served races, not delegator precedence.
- **Transitivity.** Free, with no cycles. The delegate can re-split or resell VANs, so this is also the cleanest vote-market primitive (already possible via B12).
- **Scalability.** Poor. With 5,000 delegators and 10 proposals, the delegate faces about 50k ZKP2 plus 800k ZKP3 without merging, or about 700 merge proofs plus 10 ZKP2 plus 160 ZKP3 with 8-way merges. Plausible for a server-run delegate, not for a phone.
- **Chain delta.** Small: a new tx type that reuses the VCT and VAN nullifiers, plus a note-ciphertext field.

### Architecture B: public delegate pools with public delegate routing (Kite public mode, adapted)
- **State.**
  - A delegate registry: a long-lived identity (the X-handle link belongs to the identity panel) and a per-round RedPallas route key. Chain txs are RedPallas+ZKP authenticated (B1).
  - Per round, per delegate: `Pool[d]`, an ElGamal ciphertext under that round's `ea_pk`.
  - `Withdrawn[d][p]`, needed only if override is supported.
- **Proxy-delegation proof (new, hotkey-signed, a ZKP2 sibling). It:**
  - spends the VAN (same nullifier, same VCT membership);
  - requires `proposal_authority == FULL` (see the rules below);
  - outputs a change VAN of `num_ballots - Σw_j` with the same bitmask;
  - outputs k = 10 fixed slots of `(delegate_index_j, Enc(w_j))`, with each `w_j` range-checked and Σw_j bounded by num_ballots;
  - optionally outputs k **contribution notes** in the VCT under a new domain tag, needed only for override or revocation.

  Unused slots carry `Enc(0)` to random registered delegates as cover. The chain adds each ciphertext to `Pool[d_j]`. [INF] Cost is comparable to ZKP2, which already does 16 in-circuit encryptions at K=11 (`keys.go:65-70`).
- **Delegate vote.** `MsgRouteDelegatePool{round, delegate, proposal, decision, sig}`, signed with the delegate key. It costs O(1) per proposal and needs no ZKP. At tally the chain computes `agg[p][route(d,p)] += Pool[d] - Withdrawn[d][p]`.
- **(a) Verification.** Strong. The delegator knows its ciphertext was added to `Pool[d]` in the same tx, the pool moves atomically (the delegate cannot drop individuals), and the route is public.
- **(f) Visibility.**
  - Hidden: the delegator's identity and amount.
  - The delegate learns nothing, not even its own total, unless you add dual encryption to the delegate's key or a threshold decryption of pool totals.
  - Public: that anonymous VAN(s) delegated to D (a count, blurred by dummy slots), and D's choices.
- **Override (optional), delegator precedence.**
  - An override proof is a ZKP2 variant whose input is a contribution note instead of a VAN.
  - It publishes `nf = H(contrib_nk, p)` and a ciphertext provably equal to the contribution's plaintext, which the chain adds to `Withdrawn[d][p]`.
  - It outputs a normal VC with 16 shares summing to w.
  - Order-independent by construction. It reveals D, as Penumbra does, unless you use an anonymity-set vector.
  - Simpler alternative: revoke only before the delegation close (Kite).
- **Timeline.** Namada-style: proxy-delegation close, then delegate-route deadline T1, then delegators see routes and may override until `vote_end_time - last_moment_buffer`. Today the round has only `vote_end_time` (B6).
- **Transitivity.** Recommend none in v1. If added, routes are public, so cycle detection is trivial.
- **Leaks.**
  - Delegation count per delegate (the Clark incoming-weight oracle), mitigated by dummy slots or anonymity sets.
  - Pool size inferable from exact bucket totals, especially when a large delegate is the only router to an option, and by differencing across up to 50 proposals.
  - With a colluding threshold of validators, each unsplit contribution amount, which loses the "EA sees only 1/16" property (B15).
  - Long-term: harvest-now-decrypt-later on permanent ciphertexts.

### Architecture C: public delegate pools with a privately proven route (Kite private mode); variant C' is Treasury-style
- **C.** For proposal p the delegate posts a vector `E_1..E_m` (m ≤ 8) with a proof that exactly one `E_x = Pool[d] + Enc(0; r_x)` and the others are `Enc(0)`. This is re-encryption plus an OR-proof, which [INF] is cheap in Halo2 because `Pool[d]` is a public input. The chain adds `E_j` into every `agg[p][j]`.
  - Delegators learn participation only.
  - **Bribery is not prevented**, because the delegate knows `r_x`. You would need validators or a sequencer to re-randomize the vector, which is a new protocol role.
  - It breaks B9's "decision public per ciphertext" invariant and the meaning of the per-bucket ShareCount (B3).
- **C'.** Validators threshold-decrypt each `Pool[d]` after the delegation close, the delegate posts an encrypted unit vector with an OR-proof, and the chain scales it by the public total (Treasury, NDSS 2019).
  - Per-delegate totals are public, and the delegate's choice is private.
  - Each `Pool[d]` decryption adds threshold-decryption load.

### Architecture D: "Follow" or mirror voting (client-level, no protocol change)
- **Mechanics.** An influencer publishes a signed voting guide, which may be on-chain via a public route message without pools. The delegator's Vizor casts the delegator's own ZKP2 and ZKP3s copying it, either automatically or on a reminder.
- **Properties.**
  - Identical privacy to direct voting.
  - The delegator trivially "knows" its weight was used.
  - No vote-market aggregation point.
- **Costs.**
  - The device must be online after the influencer decides, and mobile background execution is unreliable (that is why reveal is server-delegated, ZIP rationale L1461).
  - Full proving cost per proposal.
  - The influencer has no measurable power, and nothing on chain proves the copying.
  - A server-side auto-follower would need the hotkey, which hands over full vote control.
- **Variant.** Clark et al.'s multi-round idea: "delegate only to someone who already voted". Allow proxy delegation into pools **after** the delegate's routes are public. The delegator then knows the choices before committing, and B reduces to a one-proof "vote like D on everything" shortcut.

### 4.1 Comparison

| Property | A: VAN transfer (+merge) | B: Public pool, public route | C: Pool, private route | D: Follow |
|---|---|---|---|---|
| Delegator learns delegate's choice | No | Yes | No (participation only) | Yes (by construction) |
| Delegate can drop individual delegators | Yes | No (atomic pool) | No | n/a |
| Delegate learns amounts | Yes (each) | No (optional) | No | n/a |
| Public learns per-delegate count / total | No / No | Count (blurrable) / inferable | Count / inferable | No |
| Override | Impossible (bearer) | Yes, with a withdrawal accumulator | Yes, same | Trivial |
| Transitivity | Native, no cycles | Optional, public cycle check | Optional | n/a |
| Delegate work | O(n) proofs or merges | O(1) signature per proposal | O(1) OR-proof per proposal | n/a |
| Delegator work | 1 proof | 1 proof (+1 per override) | 1 proof | P × (ZKP2 + 16 ZKP3), online |
| Vote-sale primitive | Perfect (bearer VAN) | Delegate-level, visible | Delegate-level, invisible to delegators | Same as today |
| New circuits | Split, merge, note encryption | Proxy-delegate (+override) | + route OR-proof | None |
| Chain changes | Small | Registry, pools, route msg, tally hook, deadlines | + vector accumulate | None (optional guide msg) |

### 4.2 Deep dive: public delegate pool vs VAN transfer plus merge
- **Atomicity and censorship.** B applies the pool atomically. A lets a delegate cherry-pick which delegators' weight to use, or resell VANs. Neither is visible to delegators.
- **Accountability.** B gives exact, public, per-proposal accountability. A gives none, except an accidental participation leak (B13) that is also a receipt.
- **Amount privacy against the delegate.** A leaks every amount to the delegate. B hides amounts from the delegate, and also from the public unless bucket totals are small.
- **Amount privacy against colluding validators.** B is weaker than today unless each contribution is split into several ciphertexts. A keeps the 16-share protection.
- **Override.** Natural in B (an encrypted withdrawal accumulator with a per-(contribution, proposal) nullifier). Impossible in A.
- **Liveness risk.**
  - B: an unsound withdrawal or route proof can push a bucket negative or above `2^28`, so BSGS fails and the tally times out with **empty results** (B4).
  - A: inherits today's range-checked shares.
- **Complexity.**
  - A: little chain work, heavy delegate infrastructure (inbox, scanning, merge prover, bitmask classes).
  - B: modest circuits, real chain work (registry, pools, tally hook, deadlines).

---

## 5. Answer to the user: "If I delegate to an influencer, can I find out if they voted with it?"

Realistic options, with the privacy cost of each:

1. **Yes, per proposal (B, public routing).** You see on chain that D routed the pool containing your encrypted contribution to option X on proposal p. You also see it before your override deadline if the Namada schedule is used.
   - Kept private: who you are and how much you gave.
   - Made public: D's choices (expected for public figures), how many anonymous delegations D received (blurrable), and an approximate pool size.
   - Coercion cost: none beyond today, because the system already allows receipts (B11, B12).
2. **Participation only (C, or A with nullifier tracking).** You learn that D used the pool on proposal p, not how.
   - D's choice is hidden from everyone except bribers, since D can still prove its choice.
   - Costs: more circuits, possibly a re-randomizing party, and the loss of the accountability that motivates delegating to a public figure.
3. **"Only my delegators can see."** Not achievable in practice: anyone can become a delegator for 0.125 ZEC. Kite lists it as open.
4. **No (A without extras).** You cannot tell. Worse, the delegate learns your amount and can skip or resell your weight.
5. **Follow mode (D).** You "know" because your own wallet cast the vote. The cost is liveness and proving, not privacy.

Addendum: "Can I act on it?" needs either a delegate deadline plus an override window (Namada) or revocation before a delegation close (Kite). Otherwise verification is only retrospective ("fire them next round").

---

## 6. Recommended direction and rules that close holes ([INF])

- **Direction.** Architecture B with public routing, optional override via a withdrawal accumulator, and D as a zero-protocol v0 or fallback. Keep A only if the product rejects public delegate votes; it is the book's model but fails accountability and scalability for influencers.
- **Rules (each closes a hole):**
  1. Proxy delegation requires a VAN with a full bitmask. Otherwise weight already voted on p would also flow through the pool for p (double count).
  2. Use fixed-arity k = 10 slots, with dummy `Enc(0)` slots to random registered delegates.
  3. Range-check each `w_j`, and require Σw_j ≤ num_ballots.
  4. Freeze the delegate registry for the round before proxy delegation opens. Delegates cannot deregister mid-round.
  5. Pool route messages: the decision must be valid for p (validated at route time, not deferred as in ZIP L965). Decide whether last write before T1 wins.
  6. Order-independent tally math: `Pool - Withdrawn`. Skip identity ciphertexts in `AddToTally` (B3). Do not increment the per-bucket ShareCount for pools.
  7. Add round fields for the proxy close, T1, and the override window, and bind them into round parameters. Today only `vote_end_time` exists and feeds `vote_round_id` (`tx.proto` MsgCreateVotingSession comment). Re-derive the submission timing bounds (B15).
  8. Derive contribution secrets deterministically from the hotkey seed, so override survives a reinstall.
  9. No transitivity in v1.
  10. A delegate that does not route on p contributes nothing to p. Show this in the UX, or add a Li-Pournaras-style fallback (backup delegate or self) via the override window.
  11. Gate delegate registration against spam, since there are no fee-paying accounts (B1). Tie it to the identity attestation.
  12. Protect the delegate key (rotation, last-write-wins until T1). A stolen key controls a whole pool.
  13. Decide whether to split each contribution, to keep EA amount privacy (B15).

---

## 7. Sources

**External:**
- Kite: arxiv.org/abs/2501.05626 (v4 PDF read in full); fc26.ifca.ai/preproceedings/112.pdf
- Penumbra: protocol.penumbra.zone/main/governance.html; protocol.penumbra.zone/main/governance/action/delegator_vote.html; protocol.penumbra.zone/main/concepts/governance.html; penumbra.exchange/blog/governing-penumbra-private-staking-delum-and-private-governance
- Cosmos: docs.cosmos.network/sdk/latest/modules/gov/README.md; github.com/cosmos/cosmos-sdk x/gov README
- AtomOne: allinbits.com/blog/atomone-v4-technical-reference
- Namada: specs.namada.net/modules/governance/on-chain; chainflow.io/namada-governance
- Snapshot: discuss.ens.domains/t/possible-new-snapshot-strategy-for-social-proposals/11385
- Shutter: shutternetwork.discourse.group/t/permanent-shielded-voting-is-coming-to-snapshot/761; decrypt.co/105201
- MACI: maci.pse.dev/docs/core-concepts/spec
- Cicada: a16zcrypto.com/posts/article/building-cicada-private-on-chain-voting-using-time-lock-puzzles; eprint.iacr.org/2023/1473
- Vocdoni: blog.vocdoni.io/davinci-universal-voting-protocol; vocdoni.io/en/blog/vocdoni-app-2-3-...
- Aragon/Aztec: research.aragon.org/nouns-tech.html
- Academic:
  - Treasury: ndss-symposium.org/wp-content/uploads/2019/02/ndss2019_02A-2_Zhang_paper.pdf
  - Statement voting: eprint.iacr.org/2017/616
  - Kovalchuk et al.: eprint.iacr.org/2025/803
  - Kulyk: publikationen.bibliothek.kit.edu/1000081973 (Helios proxy); publikationen.bibliothek.kit.edu/1000081964 (coercion-resistant proxy)
  - Nejadgholi, Yang, Clark: pulpspy.com/papers/2021_voting.pdf
  - Li and Pournaras: arxiv.org/abs/2607.01730
  - Karoukis: arxiv.org/abs/2302.14421
  - Utke and Schmidt-Kraepelin: research.tue.nl/en/publications/anonymous-and-copy-robust-delegations-for-liquid-democracy
  - Ford: arxiv.org/abs/2003.12393
- DAO practice and markets:
  - Optimism: gov.optimism.io/t/final-governor-update-proposal-2-.../8164
  - Cardano: cardano.org/glossary/drep-activity-period
  - Polkadot: support.polkadot.network (OpenGov delegation)
  - LobbyFi: forum.arbitrum.foundation/t/dao-discussion-vote-buying-services/28934; defillama.com/research/report/an-analysis-of-the-role-of-vote-buying-in-dao-governance-report
  - Dark DAOs: arxiv.org/abs/2311.03530; hackingdistributed.com/2018/07/02/on-chain-vote-buying
  - Vitalik: vitalik.eth.limo/general/2021/08/16/voting3.html
- Zcash: zodl.com/zodl-3-5-0-vote-with-your-zec; unchainedcrypto.com (NU7 vote); cryptoslate.com/zcash-holders-vote-to-preserve-halvings-...
- ICNS (for the identity panel): Twitter OAuth plus a threshold of oracle verifiers relaying to a Registrar contract (polkachu.com/gov_proposals/29-30; hackmd.io/@shanev/H1voH1coj)

**Local, read-only:**
- vote-sdk worktree: `x/vote/types/keys.go`, `x/vote/keeper/keeper_tally.go`, `x/vote/ante/validate.go`, `proto/svote/v1/{tx,types}.proto`
- voting-circuits: `src/params.rs`
- zcash_voting: `src/delegation_capability.rs`, `docs/exporting-to-external-software.md`
- ZIP drafts: `draft-valargroup-shielded-voting__adam_voting-protocol-client-delay.md`, `__greg_shielded-voting-fixes.md`, `draft-valargroup-submission-server__adam_submission-server-client-delay.md`
- shielded-vote-book: `delegation/*.md`, `userflow/delegating-your-vote.md`
