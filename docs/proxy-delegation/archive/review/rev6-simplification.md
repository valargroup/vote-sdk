# Rev 6 simplification pass

The owner asked for a pass over PLAN.md rev 5: what could make it simpler to build, with the same guarantees and without giving up too many features. Three reviewers covered:
- circuits, chain and helper;
- the client library and Vizor;
- identity, the verifier, specs and rollout.

The owner adopted every recommended cut, plus the three optional ones (A, B and C below). PLAN.md rev 6 is the result, and where this file and PLAN.md disagree, PLAN.md wins. All savings are [inference] on the plan's S/M/L scale (S about 0.3, M about 1, L about 1.8 eng-weeks).

## Guarantees the reviewers had to keep

1. **Set and forget.** Up to 8 delegates with percentage splits plus an optional kept remainder; once per round; final; works for software, Keystone and Ledger accounts.
2. **Delegate ballots.** A delegate submits one complete ballot any time up to close, or never; with no ballot, the pool counts nowhere.
3. **Hidden pool totals.** Pools are never decrypted and per-delegate totals are never shown. Complete ballots and one final tally keep the cross-proposal channel closed.
4. **Last-moment parity.** Delegation stays open until `vote_end_time`, with the single-share layout in the last-moment window.
5. **Zodl and old clients unaffected.** No ZKP1/2/3 VK change and no `vote_protocol` bump.
6. **Open registration.** An automated verifier (X, plus a fallback provider), a vetted-only browse list, and exact search for everyone else.
7. **Keys, suspension and the off switch.** A separate delegate key phrase; coordinator suspension that is freeze-only with at least 2 approvals; a mid-round off switch.
8. **Spam bound.** At most 8 DCs per registration.
9. **No redirection.** A delegator cannot be redirected to the wrong delegate, and Vizor never contacts X.
10. **Liveness.** Delegated weight still counts if the app dies after hand-off.

## Adopted cuts

| Cut | What goes | What the owner gives up | Saving |
|---|---|---|---|
| One delegate key; registry ops `register` and `revoke` plus coordinator suspension | The DIK/DRK split and `key_epoch`; route-key rotation, identity change, verifier-attested recovery, cancel and self-freeze; pending changes and their effective view; delay floors, the key-change cap, verifier warm-up and the recover threshold; 6 of 11 digest domains (one new domain, SUCCESSOR, covers the off-chain successor authorization); Vizor's rotation and recovery screens and banners; the phrase restore scan | Index continuity after a key loss (a successor link and re-vetting replace it); the round's pool if the loss comes before the ballot (rev 5 lost it too for a lost phrase); a reversible self-pause | About 4-5 |
| No delegator recovery after a reinstall (DC secrets from the hotkey) | Software and hardware ARKs, the `t_ARK` ZIP domain, the AES-SIV hint and its vectors, recovery tiers R1 and R2, the recovery cursor, Vizor's R2 job, `0x1D` and its feed and genesis section | After a reinstall or seed restore, an unfinished delegation and the kept remainder are lost, exactly as for votes today | About 3 |
| Curated vetted profiles; one signed `directory.json` plus one avatar pack on two mirrors | X hydration; the automated picture pipeline (fetch, sandboxed decode, re-encode, content addressing, shards, perceptual hashing, abuse scanning, pending-review hold); the profiles doc and its purge SLA; `index.json`, the hash chain, cross-mirror equivocation checks and the third mirror | Vetted profiles no longer follow X changes automatically | About 2-3 |
| 0x09 carries only DCs (the optional registration stays) | Mixed DC and cast batches, the D1 kind byte, the 50-action cap and two capability fields, the 700 KB byte-budget packer, the combined review screen and job kind | "Delegate some, vote the rest" takes two transactions; a remainder vote in the final minute may miss close | About 2-2.5 |
| Chain trims | C15 `delegate_index_bound` and public input 11; `routing-audit`, `0x1C`, `0x1E`, `0x1F` and the dual routing path; derived registry indexes and `updated_since_height`; a separate verifier-set payload and warm-up; `attestation_id`; capability fields 4-13 shrunk to 2; tags 0x0B and 0x0C merged; no-op ballot retries; GasUsed dormancy tests; the `revealCloseTime` refactor (deferred to v1.1) | An audit endpoint (auditors replay archival state instead) | About 3.5-4.5, and rc.1 about a week earlier |
| Client trims | The exact packer (replaced by sequential fill); the local registry mirror and post-commit re-check; four new job kinds cut to two; schema tables; `min_ballots_per_delegate`; the wakelock and optional warnings; in-app report POST, PD-11, PD-12 and PD-13 | Nothing visible beyond a report link instead of an in-app sheet | About 2.5-3.5 |
| Identity and verifier trims | The DNS provider; the independent re-verifier (the signer re-fetches every proof and the refresher re-checks once); proof-post rechecks and the oEmbed fallback; the admin UI (replaced by a curator CLI); the "Stop accepting" flag; UTS #39 skeletons, first-seen precedence, reservations and perceptual hashing (replaced by an ASCII fold check) | Domain-only delegates in v1; warnings between two unvetted lookalikes | About 2-3.5 |
| Specs, stage and audit | ZIP-DR (folded into ZIP-PD); book rewrites beyond pages that become false; a separate delegate-beta stage round; L7 and L8 lab scenarios; about a third of audit tranche 2 | Fewer influencers in the stage beta | About 2-3.5 |
| Owner call A: no deep links in v1 | The `/d` route, its privacy review, Vizor's link parsing and mismatch blocking | Delegates share a handle or fingerprint instead of a link | About 0.6-0.8 |
| Owner call B: a lean status view | The k/n reveal counter and "on track" state, per-option delegate names on results, and the v1 track record (now v1.1) | Less detail until v1.1 | About 0.7-1 |
| Owner call C: one off switch | The signed per-round config extension, its `seq` rule, CI signing, audit item and per-round signing step | Switching off needs two coordinator approvals | About 1 |

The rows overlap, so they add up to more than the total.

**Total.** About 53-61 eng-weeks with every cut including A, B and C, against rev 5's 79-84. The critical path goes from about 19 weeks to about 17-18, because it runs through the circuit, the audit and the stage rounds.

## Checked and rejected

- **Per-bundle Hamilton allocation.** It made 44% more DCs and helper reveals [measured, `prototypes/client/compare_packers.out`], lost the within-one-ballot quota, and still needed a global floor fix-up. Sequential fill matches the exact packer (2.845 against 2.834 DCs per delegator; identical with one bundle).
- **Taking the registration out of 0x09.** It saves about 1 eng-week, but first-time last-moment delegators would wait an extra block and a tree sync before proving, which breaks last-moment parity.
- **An unproven REST registry check.** A thief of the directory key could relabel an index or key, and only a proven chain read catches that.
- **Platform image decoders for curated avatars.** Hash-pinned bytes would still give a curator or directory key thief a decoder-exploit path (the libwebp CVE-2023-4863 class). The Rust `image-webp` decode costs 2-4 days.
- **Verifier-submitted registrations from an endorser-style account.** This moves tx submission into the verifier while revoke and ballots still need a custom tag, so it saves nothing.
- **RedPallas delegate keys.** The verifier and a Phase 2 CLI would need RedPallas.
- **Dropping these mechanisms:** the routing re-randomizer ρ, the per-round enable snapshot, the slot nullifier, the layout freeze, the release path, the ZKP2 `proposal_id ≠ 0` gate, the tree-capacity guard and the corpus replay gate. Each blocks a measured attack or carries a guarantee.

## Evidence the reviewers relied on (spot-checked)

- **Votes have no recovery today.**
  - Vizor generates a random hotkey per account and round, and "v2 does not try to recover deterministic hotkeys from the wallet seed" (Vizor `rust/src/wallet/voting/README.md:42-48`).
  - "Lost voting hotkey secrets cannot be recreated from a wallet seed or Keystone UFVK" (Vizor `docs/voting-participation.md:146`).
  - Re-importing a wallet mints a new account UUID.
- **Hotkey-derived DC secrets match votes.** Vote share randomness is derived from the hotkey, the round context and the VAN commitment (zcash_voting `zcash_voting/src/zkp2.rs`, doc comment near line 89).
- **The weight refactor stays essential.** `total_note_value` has 72 non-test uses across 10 files in zcash_voting.
- **Proposal ids are always `1..n`** (`x/vote/keeper/keeper_voting.go:255-263`), so a ballot can be a dense options array.
- **The batch code exists to copy:**
  - `x/vote/types/msgs.go:167` (`MsgCastVoteBatch.validateBasic`);
  - `x/vote/ante/validate.go:185, 383`;
  - `x/vote/keeper/msg_server.go:221`;
  - `x/vote/types/sighash.go:77`.
- **The D7 floor's home** is the coordinator approval count check (`x/vote/keeper/msg_server_coordinator_actions.go:170-176`).
- **Key uniqueness pattern.** The forward and reverse key index follows the Pallas-key registry (`x/vote/keeper/keeper_pallas_registry.go:33-48`).
- **Genesis already exports pools.** Tally accumulators and share counts, including proposal-0 pools, are exported in full (`x/vote/keeper/genesis.go:267-279`).
- **ρ stays.** `AddToTally` rejects identity ciphertexts per reveal (`x/vote/keeper/keeper_tally.go:45-81`), but nothing can be rejected in EndBlock routing.
- **No in-circuit bound is needed for u32 indices.** `MsgRevealShare.vote_decision` is a u32 (`proto/svote/v1/tx.proto:124`), so a DC to an index of 2^32 or more can never be revealed.
- **X API prices** (read 2026-10-06): users $0.010 and posts $0.005 per resource, deduplicated per UTC day. Handle refresh for every registered delegate costs about:

  | Delegates | Monthly cost |
  |---|---|
  | 200 | $63 |
  | 1,000 | $310 |
  | 10,000 | $3,050 (about $1,250 with weekly refresh of dormant delegates) |

  Rev 5 spent about the same, because hydration rode on the same user lookup. The savings are engineering, legal and ops.
