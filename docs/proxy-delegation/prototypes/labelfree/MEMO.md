# Does each delegated share need a "delegate #17" label?

Decision memo, 2026-10-05. Every comparison is against the labeled-pool plan **with hidden totals**: the rev-2 disclosure is reverted, so pools are never decrypted. Labels: [code], [doc], [measured], [inference]. Repo paths are vote-sdk unless noted. Evidence is under `scratchpad/` in `labelfree-design/`, `labelfree/`, `zkp3r/` and `skeptic/`.

## 1. Short answer

**No. Neither correctness nor privacy needs the label. It is there to keep cost and timing simple.**

Today's plan reveals a delegator's shares *before* the delegate has voted, so the chain must park the weight until it knows the option. Each share is tagged "for delegate 17", summed into `Pool[17]`, and the pool is moved onto the delegate's choices at close (PLAN.md:23-24, 334, 404-417 [doc]).

The label buys a fixed cost (16 reveals per delegation however many proposals there are), free last-second voting for delegates (routing is one addition per proposal at close), and an unchanged ZKP3.

It costs what the owner suspected. The public sees each delegate's reveal count live, about 16 per delegation, so a lone delegation stands out (PLAN.md:29 [doc]). A coalition holding at least t EA key shares can decrypt `Pool[d]`, or sum the 16 labeled shares, and get a lone delegator's exact amount.

The label can go if each share is revealed only *after* the delegate has voted on that proposal. The reveal proves in zero knowledge "my hidden delegate voted o on p", and the share lands in bucket (p, o) like a direct-vote share. A prototype rejected every invalid input we tried. The cost moves: reveals multiply by the number of proposals the delegate votes on, and none can start before the delegate votes.

## 2. How the label-free design works

- **Unchanged:** ZKP4, the DC `Poseidon5(DOMAIN_VC, round, shares_hash, 0, d)`, 0x09, the registry and one-shot routes.
- **Route leaves.** When delegate d routes p to o, the chain appends `Poseidon5(DOMAIN_ROUTE=2, round, d, p, o)` to the existing per-round commitment tree. An explicit abstain appends nothing.
- **Reveals.** For each DC share it holds and each routed p, a helper sends an ordinary `MsgRevealShare` labeled (p, o). The wire format is unchanged (tx.proto:120-128 [code]). The message carries `C' = C_i + Enc(0; r')` and a proof of five facts:
  1. the DC is in the tree;
  2. `C_i` opens share i;
  3. `C'` re-randomizes `C_i`;
  4. a route leaf for the same hidden d is in the tree;
  5. `nf = H(DC, i, blind_i, p)`.
- **Tally.** `AddToTally(p, o)` as today. There are no pools, no routing hook, no ρ and no per-delegate counts.
- **One reveal circuit.** Every reveal in a proxy round, direct or delegated, must use one circuit, ZKP3U, with a private mode bit. A separate DC reveal type lets anyone solve per-delegate counts for 100% of delegates at D ≤ 20, P = 10 or D ≤ 80, P = 37 [measured, `labelfree/dc_type_leak.out`].
- **Soundness.** One nullifier per (DC, share, p), excluding o; one route leaf per (round, d, p). The DC's constant 0, ZKP2's p ≠ 0 gate and the chain's p ≥ 1 check (keeper_voting.go:260 [code]) stop DCs and votes being opened as each other.
- **Prototype.** The separate-VK ZKP3R is K=11, 1,016 rows, 8,352 B proof; at 4 threads 90 ms prove and 2.0 ms verify, against ZKP3's 35 ms and 1.2 ms. Its 14 MockProver tests and a ZKP4-to-ZKP3R end-to-end test pass [measured, `zkp3r_rows.log`, `zkp3r_timing.log`]. **ZKP3U itself is not built.**

## 3. Side by side

| | Labeled pools (hidden totals) | Label-free (ZKP3U) |
|---|---|---|
| Public, lone delegator | Live count R_d = 16 flags it. The amount is public if `Pool[d]` is the only weight on an option, which is easy to spot because BallotCount leaves out pool shares (keeper_tally.go:195-207 [code]) | Public if they are the only weight on an option, as for a direct voter. Counts stay hidden only when delegates outnumber proposals or every delegate routes everything; otherwise per-proposal cast counts (tx.proto:80 [code]) solve them and exposure equals labeled: 3.42 vs 3.42 of 3.50 lone delegators at D=5, P=37 [measured, `skeptic/unified_leak_lone.out`] |
| Public, delegator among many | Approximate following, live | Nothing, except bursts after late routes |
| Public, early DC tied to its tx | Small pools narrow the tx to a median of 4-112 candidates (PLAN.md:759 [doc]). With 10 small pools the tx is unique in 18-33% of trials [measured] | No link |
| Public, last-moment DC | The reveal links the tx to d, as a late vote's does | Same: its reveals land on d's routed options |
| Delegate | Sees R_d | Sees nothing |
| Helpers | Learn d, the DC leaf and the IP | The same. They also hold shares until `vote_end` and know which (p, o) each reveal feeds |
| EA coalition (≥t), keys only | Decrypts `Pool[d]`: every delegate's total and every lone delegator's exact amount | Sees single shares only. For whale DCs it links the exact remainder set in 10-39% of cases and the exact amount in 0% [measured, `exp_b3.out`]. Standard-coin splits bring it to direct-voter parity |
| EA coalition (≥t) that keeps helper intake | Can group every VC and DC, direct votes included | Same, so no gain |
| Reveals per DC | 16 (1 for a last-moment DC) | 16 × P_routed (P_routed for a last-moment DC) |
| L2-like round: 5k direct voters × 10 proposals + 10k delegators × 3 DCs | 1.28M reveals, 31 fleet-hours, 6.7 GB | 4.55M (3.56×), 109 fleet-hours, 36 GB [inference, `load.out`] |
| Workers for ≤0.1% loss at 20k × 3 DCs (today 20) | 16 at P=10; 26 at P=37 | 119-226 at P=10. At P=37 no fleet size works: it needs 47.8 h of full blocks [measured, `seeds_deadline.out`, `counts.out`] |
| Chain verification | A full block of 256 ZKP3 reveals takes about 0.85 s on one core | About 1.5 s against ~1.2 s blocks, and CheckTx verifies again (validate.go:137-140 [code]) |
| Timing | Routes are free until `vote_end` | Reveals wait for routes. A late route over 1,000 DCs × 10 proposals is 160k reveals, about 3.8 fleet-hours. A top delegate (22% of DCs) routing at T−1h loses 11.5% of DC weight and 100% of later direct votes at today's fleet [measured, `burst.out`] |
| A missed reveal loses | 1/16 of the DC on every proposal | 1/16 on one proposal |
| Circuits | ZKP4; ZKP3 unchanged | ZKP4, plus ZKP3U for every reveal in proxy rounds |
| Chain changes | p=0 branch, `Pool[d]`, pool counts, EndBlock routing with ρ, 0x1C bases | Route-leaf append in 0x0C, a per-round reveal VK selector, an `ea_pk` input and a nullifier-prefix query |
| Spam per registration | 256 reveals (PLAN.md:498 [doc]) | 256 × P, which is 9,472 at P=37 |
| Engineering | Baseline (smaller with disclosure reverted) | Neutral to +6 eng-weeks; ZKP3U joins the circuits critical path and audit tranche 1 [inference]. Fleet running cost 6-12× at scale |
| Top risks | Public counts, the coalition decrypting pools, routing-hook cost (OPS-13) | Late-route bursts regressing Zodl votes, which fails release gate 4 (PLAN.md:519 [doc]); the chain verification ceiling; parity that holds only conditionally; an unbuilt circuit |

**Removed by label-free:** the p=0 branches (chain and helper) and `reveal_pool_share`; `Pool[d]` and its counts; `delegate-pools`; the EndBlock routing hook and OPS-13; ρ and 0x1C; the `pools_routed` fields; `tally-breakdown`; decisions 6-8; LMD-4 padding; the "about N delegations" copy.

**Added:** a route watcher, per-(share, p) scheduling, share retention until `vote_end`, primary-helper dedupe, and a route deadline or Q18.

## 4. The skeptic's surviving problems

| Severity | Problem | Fix |
|---|---|---|
| High | **Parity is conditional.** Cast txs publish `proposal_id`, so per-proposal DC reveal totals are public. When there are not many more delegates than proposals and their participation varies, per-delegate counts are solvable, and lone-delegator exposure equals labeled. | State the guarantee with its conditions. Full hiding needs zero-value reveals for unrouted (d, p), which means null-route leaves plus Q18 and more load. Alternatively wait for V2, which removes per-option counts. |
| High | **The owner's coalition baseline does not hold in deployment.** Helpers run inside validators (join-chain.md:173 [doc]), t = 7 of 10 (keeper_ceremony.go:53-63 [code]), and each share goes to 5 helpers (zcash_voting server_order.rs:51-53 [code]). A t-coalition that keeps helper intake can group every VC and DC, in both designs. | Tell the owner. Label-free only helps against a coalition that has keys but no helper data. Real protection needs helpers separated from key holders, or a different delivery model. |
| High | **Routes inside the last-moment window starve later direct votes** and reveal an approximate following. | Choose one: a consensus route deadline at the start of the last-moment window (strains D5 for delegates); a priority queue for direct shares (reverses decision 29); or burst pacing with accepted loss. Add this scenario to release gate 4. |
| Medium | **The load fix and the coalition fix conflict.** A standard-coin split at k=4 shares loses a median of 16.2% and up to 60% of the weight; at k=16 the median is 0% and the maximum 2.9% [measured, `skeptic/m1_vs_k.out`]. | Choose explicitly: keep 16 standard-coin shares at full load, or re-measure coalition linking at k=8/4/1 before quoting the 1.36× figure. |
| Medium | **The chain verification ceiling.** 256 reveals per block probably cannot verify in time on 2-4 vCPU droplets. | Benchmark full ZKP3U blocks, including CheckTx. Use batch verification or a lower cap, then redo the load tables. |
| Medium | **The measured circuit is not the one we would ship.** | Build ZKP3U. Golden-vector its mode-0 nullifier against ZKP3 (share_reveal/circuit.rs:19-24 [code]). Profile the helper's 1.7 s per share. |
| Medium | **All five holders of a share prove it at the same moment** under the deterministic schedule. | Make a deterministic primary helper per (share, p) a prerequisite, and quote load at dup 2-5. |
| Medium | **Spam amplification is 16× a direct voter.** | Raise `min_ballots_per_delegate`, lower the slot cap, or bind unused ZKP4 slots to a null commitment. |
| Low | **Misstatements:** D7 is unchanged (posted routes survive in both designs, PLAN.md:332 [doc]); ZKP3R is K=11, not K=12. | Corrected here. |
| Low | **Helpers know the (p, o) each reveal feeds**, so censorship is precise. | Document it, or use the ZKP3R-V2 shape. |

## 5. Owner decisions under label-free

- **D1 public routes, D2 final delegation, D3 open registry, D6 delegate phrase:** unchanged.
- **D4 hidden totals:** labeled keeps a `Pool[d]` ciphertext that is never decrypted but could be. Label-free has nothing to decrypt.
- **D5 last-moment parity:** delegators keep it (an in-window DC is one immediate share); delegates' routes need a deadline or Q18. This is the main product cost.
- **D7 suspension:** unchanged; "an unrouted pool abstains" becomes "unrouted proposals abstain".

## 6. Recommendation

**Do not swap the v1 plan yet. Run a gated spike (2-3 weeks [inference]) before the M1 circuits freeze.** Adopt label-free only if all four gates pass:

1. **Circuit.** ZKP3U is built with K ≤ 11 and a proof of at most 15,360 B, and its mode-0 nullifier matches ZKP3's golden vectors.
2. **Helpers.** The measured ZKP3U pipeline, with primary-helper dedupe (dup ≤ 2), carries the projected first-year round with at most 0.1% loss. The simulations (dup 1) suggest:
   - 16-35 workers (today 20) at 2k × 3 DCs, P=10;
   - 119-226 workers (12-23 per operator) at 20k × 3, P=10;
   - automatic failure at 20k × 3, P ≥ 37.
3. **Chain.** A full block of 256 ZKP3U reveals verifies within block time on the 2-4 vCPU droplets, CheckTx included. If not, lower the cap and redo gate 2.
4. **Owner sign-off** on three points:
   - delegate routes close at the start of the last-moment window, or Q18 is adopted with G sized to drain the largest following;
   - the conditional parity wording;
   - no counts, followings or leaderboard by ZEC.

**If all four pass, adopt label-free as a package:**
- ZKP3U for every reveal in proxy rounds;
- route leaves in the round tree;
- 16 standard-coin shares per DC (rounding loss median 0%, maximum 3% [measured, `m1_loss.out`]);
- independent per-(share, p) times;
- spam bounds.

Never ship the separate-VK ZKP3R. Do not adopt a per-delegator share budget without re-measuring coalition linking.

**If any gate fails, which I expect at L2-like scale:**
- Keep labels for v1 with hidden totals.
- Correct §5 to say that a coalition with keys alone can group a lone delegator's shares, and that approximate followings are public live.
- Make label-free the delegation path of private-vote V2: the reveal circuit changes there anyway, ZKP3R-V2 fits K=11 (1,867 rows, 100.6 ms at 4 threads [measured]), and V2 removes the per-option counts behind the parity gap.

**On either path,** tell the owner plainly: against a coalition that also keeps helper intake, neither design, and not today's direct votes either, meets "nobody learns a single person's amount".
