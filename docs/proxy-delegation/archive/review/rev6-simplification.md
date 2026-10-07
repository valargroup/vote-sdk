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

## Follow-up in rev 7 (owner decision)

The owner moved the vetted list into the dynamic config that wallets already fetch.
- **Signed proxy entry.** Each round gets a proxy entry beside its round entry, signed by the same admin key (`trusted_keys`). The entry pins the vetted list's hash and names the live directory's signing key.
- **Approval.** A Valar reviewer and a Vizor reviewer approve each list in a config-repo PR. This replaces rev 6's 2-of-3 curator keys.
- **Pre-round refresh.** Before each round, a curator tool refreshes the vetted delegates' pictures, names and handles from X or GitHub by account id.
- **Fixed per round.** Additions wait for the next round. Removals still act at once through directory flags or suspension.
- **What it removes:** the curator keys, the vetted section's own `seq`, the directory's offline-key certificate and the new static pin.
- **Net effect:** about 0.5 eng-weeks saved after adding the per-round picture pull [inference]. Vetted pictures and names are X content again, and counsel signs off on that (PLAN.md §4.6 Legal).

## Follow-up in rev 8 (owner decisions)

- **12-word phrases.** Delegate phrases are 12 words.
- **Re-registration.** An account can register again to replace its key. The verifier attests that it is the same account, and the entry is re-keyed: same number, new key.
  - The old key stops at once.
  - The new key votes only in rounds created after the change.
  - The verifier allows one registration per account per 14 days, checked before any paid call.
  - This replaces the successor authorization and the support override.
- **Registration cost.** A free check through X's oEmbed endpoint comes first: post text, current handle and display name. Then the signer makes one paid post lookup, for the numeric id and the account's age, under a daily spend cap.
- **Free refresh.** The daily handle refresh reads each registration post through oEmbed. Paid calls are now only for registrations, about $0.015 each.
- **Pictures.** Vetted delegates' pictures come from their public X profile pages, fetched once per round by Valar's own tool. The owner accepted this terms-of-service risk.
- **Storage.** Profile content moves out of git to Valar's store, because X's 24-hour deletion rule cannot be met in git history. The per-round list pins only salted hashes.

## Follow-up in rev 9 (owner decision)

A new key from registering again counts only for rounds created at least 2 days after it.
- **Before that,** the previous key keeps working, including in rounds already running. A hijack therefore never touches a running or imminent round.
- **Replacing a key early.** A new key replaced before its 2 days are up never counts, so an owner who recovers the account within 2 days cancels a hijacker's key.
- **Manual backstop.** For a hijack more than 2 days before a round, coordinators suspend the delegate until the owner registers again.
- **Stolen keys.** A stolen key is revoked at once with "Retire" from the owner's own copy.
- **Rate limit.** The 14-day cooldown is replaced by one paid check per account per day.
- **Chain state.** Each entry keeps its last 4 keys with timestamps, and the ballot check uses the round's effective key.

## Follow-up in rev 11 (final review)

Astra, at extra-high reasoning, and an independent reviewer both found holes in rev 9's key rule and a few rev 7-8 details. Their raw reports are not kept here; PLAN.md rev 11 records the outcome. The fixes, all owner-approved:
- **Key history.** A first registration counts in every round. A pending key replaced before it settles is deleted, and settled keys are kept. The effective key is the newest with `settles_at ≤ round.created_at_time`.
- **Saved attestations.** Attestations bind the entry's newest key.
- **Key freeze.** Coordinators freeze one key instead of suspending the whole delegate.
- **Several keys.** The apps handle more than one key per delegate.
- **Spend cap.** Part of it is reserved for owners re-registering.
- **Relisting.** Re-posting the marker relists a delegate without a key change.
- **Pictures.** There is no picture pack in git.
- **Freshness.** The directory's age is judged by its signed time.
- **Proxy entries.** Each carries a version number.
- **Approvals.** A required CI check enforces one Valar and one Vizor approval.

Simplifications adopted:
- One VAN-weight loader.
- Proven entry bytes decoded in zcash_voting.
- The proxy entry in its own PR.
- Stage auto-merge.
- No per-IP tracking.
- No paid picture fallback.

Not adopted:
- A verifier-signed handle binding for unvetted delegates.
- Salted picture hashes.
- Rewording the "Counted" status.

## Follow-up in rev 12 (last hole hunt)

Astra, at extra-high reasoning, and an independent reviewer looked for remaining holes before implementation. Their raw reports are not kept here; PLAN.md rev 12 records the outcome (R12-1 to R12-8). The owner approved eight must-fix items:
- **Config tooling.** vote-sdk's config tooling keeps unknown fields, CI protects proxy entries, and the gateway, Pages and stage auto-merge handle `delegates/`.
- **Trust anchor.** Vizor's bundled validator set is refreshed at launch and monitored, with a clear failure state.
- **Batch expiry.** 0x09 carries an expiry height, so a released batch can never land later, and a released registration is reused without new device signatures.
- **Paid checks.** One paid check per post, cached for any submitter, replaces rev 11's signature check before payment, which could not work. The owner asked why a signature was needed at all; the cache stops the attack without one.
- **Relisting.** A new post is confirmed against the numeric account.
- **Round time.** The signed proxy entry carries the round's creation time.
- **Delegation window.** The proxy entry is prepared right after round creation, with clear states before it lands and after a first vote.
- **Consensus.** No key pruning in v1, and genesis validates the registry.

Smaller findings the owner chose not to apply in rev 12 (revisit while drafting contracts-v1 or later):
- Randomizing delegate order before the sequential fill, so packing does not hint at allocation order.
- Keeping an old phrase on the delegate's device until its successor key has settled.
- Separating a delegate's "retired" display status from per-round eligibility, including a "Revoke old key" action.
- Making the re-registration spend reserve best effort, including renamed accounts.
- Richer status states and a lost-key flag.
- Hijack runbooks and verifier compromise at scale.
- Publisher failover and directory-key rotation details, such as an admin-signed expiry on the directory key.
- Generation-specific inputs for the VAN-weight loader.
- Testing the oldest supported Zodl builds.
- Tor wording in copy C7.
- Stale-text cleanup.
