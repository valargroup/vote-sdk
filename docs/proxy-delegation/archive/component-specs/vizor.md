# Vizor wallet: proxy delegation UX and integration spec (architecture B2)

Component: Vizor wallet UX + integration. Date: 2026-10-05. Read-only research; no repo was modified.

Labels: **[code]** verified in source (file:line), **[doc]** claim in a document, **[inference]** my reasoning or proposal.
Path prefixes: `vizor:` = `scratchpad/src/vizor-wallet` (origin/main snapshot); `zv:` = `scratchpad/src/zcash_voting`; `dls:` = `/Users/czstudio/Documents/vizor-deeplink-server` @ `b325f07`; `chain:` = the vote-sdk worktree; `dossier:` = `scratchpad/dossiers/*.md`.

---

## 0. Summary

1. The delegator flow lives inside the existing voting feature. One generalized submission job (today's `VotingSubmissionJobNotifier`, extended) drives ZKP1, proxy delegation (DCs), the user's own casts and helper shares from the SDK round plan. Hardware accounts still sign only ZKP1.
2. Delegation must come before the user's own votes. Vizor enforces this per round (the SDK enforces the real per-VAN `proposal_authority == MAX` rule) and explains it.
3. Discovery uses a signed, whole-download registry snapshot that Vizor searches locally. Profile pictures come from a Vizor/Valar origin by content hash, over the Tor-aware client, and are drawn with `Image.memory`. Vizor never contacts X.
4. "Did my delegate vote" uses public route data that Vizor verifies against the chain's signed header with the existing IAVL participation reader. Share-reveal progress comes from SDK share tracking, plus share-nullifier proofs at the end.
5. The delegate role is a new "Delegate profile" area. In v1 it is software accounts only. The delegate key (DK) is derived from the account seed. Public votes are DK-signed, fee-less messages with no proofs, so they are cheap on mobile.
6. Gating fails closed. All of these must hold: compile define, remote kill switch, wallet capability, signed config capability, chain capability with a matching ZKP4 VK fingerprint, and a valid registry. The beta runs on `[TEST]` rounds.

---

## 1. Terminology and naming

| Concept | Code name (new code) | User-facing |
|---|---|---|
| ZKP1 / `MsgDelegateVote` (note weight into a hotkey VAN) | unchanged (`delegatePendingBundles`, `DelegationStatusView`, ...) | **"Authorize voting"**. Rename the Ledger strings "Preparing voting delegation" / "Finishing voting delegation" (`vizor:lib/src/features/voting/screens/voting_status_screen.dart:1262-1263` [code]) to "Preparing voting authorization" / "Finishing voting authorization". The step "Proving voting authority" (`:1036`) stays. |
| Proxy delegation (weight to a person, ZKP4 + DC) | `proxy*`: `ProxyAllocation`, `ProxyDelegationStatus`, `NextStepKind.proxyDelegate` | verb "delegate", noun "delegation", list "Your delegations" |
| The person | `Delegate*`: `DelegateProfile`, `DelegateIdentity`, `DelegateKey` | "delegate" |
| Delegation commitment (DC) | `delegationCommitment` / `dc` | never shown |
| `MsgDelegateRoute` | `delegateRoute` | delegate side: "public vote"; delegator side: "@alice voted Yes" |
| Pool[d] | `delegatePool` | never shown; say "voting power delegated to you" |
| DK | `delegateKey` | "delegate key", "key fingerprint" |
| Featured tier | `featured` | "Featured" |

Rule: new code never uses bare `delegation*` for the delegator side. Existing ZKP1 names keep their meaning. Code review should reject a new symbol that collides.

---

## 2. Feature gating and rollout

`proxyDelegationGateProvider(roundId)` returns `ProxyGate {bool delegatorEnabled, bool delegateEnabled, ProxyGateReason? reason}`. It is enabled only when every row below passes.

| Gate | Source | When missing or unreachable | Effect |
|---|---|---|---|
| G1 build | `--dart-define=VIZOR_PROXY_DELEGATION_ENABLED` (default `false` until GA), new `lib/src/core/config/proxy_delegation_feature_config.dart` | false | everything hidden |
| G2 remote | `https://functions.vizor.cash/static/proxy-delegation.json`, schema `{ "<appVersion>": {"delegator": bool, "delegate": bool} }`, parsed like `parseSwapEnabledOverrideForVersion` (`vizor:lib/src/core/config/swap_remote_enable_config.dart:10-32` [code]) | Beta: treated as false (fail closed, like swap's `== true`). GA: last cached value, default true (kill-switch semantics). | hides *new* delegations and onboarding. Status views stay visible. |
| G3 wallet | `zcash_voting` `WalletCapabilities` gains `proxy_delegation: ["v1"]` (today it has `vote_server`/`vote_protocol`/`tally`/`pir`, `zv:zcash_voting/src/config/mod.rs:86-107` [code]) | build mismatch | hidden |
| G4 config | dynamic config `supported_versions.proxy_delegation` contains `v1`, and the round entry carries `features.proxy_delegation = true` | absent | hidden for that round |
| G5 chain | `GET /shielded-vote/v1/protocol-capabilities` (`chain:api/query_handler.go:71` [code]) reports `proxy_delegation_v1 = true` and `zkp4_vk_fingerprint` equals the client's compiled VK | absent or mismatch | hidden for that round (debug log only) |
| G6 registry | static config `delegate_registry` present, and the snapshot verifies (§7.8) | absent or invalid | directory read-only or unavailable; new delegation blocked |
| G7 beta | round title starts with `[TEST]` and "Show test rounds" is on (`vizor:lib/src/providers/voting/voting_round_visibility_provider.dart:12-17` [code]), or a hidden "Delegation beta" toggle in `voting_config_settings_panel.dart` | n/a | beta period only |
| G8 account | delegator: any account kind; delegate: software accounts only (v1) | n/a | delegate entry shows the blocker DG-12 |

Never-hide rule: once the local round plan has proxy allocations, the status, results and poll card show them whatever G1-G7 say.

Zodl and older Vizor builds are unaffected because the config must not bump `vote_protocol`. A bump makes every older wallet reject the config (`dossier:specs.summary.md`; `vizor:lib/src/providers/voting/voting_config_provider.dart:211` `ConfigSwitchKind.protocolChanged` [code]). See contract F1.

Rollout:
1. Ship dark (G1 false).
2. Internal builds with G1 true on stage `[TEST]` rounds.
3. Public beta on mainnet `[TEST]` rounds with G2 per version.
4. GA: G1 default true, G2 becomes a kill switch, G7 is dropped.

---

## 3. Delegator journey

### 3.1 Entry points

| Entry | Location | Behaviour |
|---|---|---|
| E1 poll detail card | `VotingActivePollContent` (`vizor:.../voting_proposal_detail_screen.dart:551` [code]), under the voting-power row, desktop and mobile (`_MobilePollSummary`) | "Delegate voting power" card, states PD-1 |
| E2 directory | Desktop: a "Delegates" toolbar button on the polls header (`voting_polls_screen.dart:300-397` area). Mobile: a nav-bar action on `MobileVotingPollsScreen`. | Opens PD-2 in browse mode. "Delegate" then asks for a round if more than one eligible active round exists (PD-13). |
| E3 deep link | `https://link.vizor.cash/d/<index>[#k=<hint>]` (§7.10) | Opens PD-3 |
| E4 Home card | no new CTA. When this account has proxy allocations, the existing card subtitle reads "Delegated" | |
| E5 review screen hint | `voting_review_screen.dart` when `proxyAvailability == available` | One line: "Want to delegate some of your voting power? Do it before you submit your votes." Link to PD-4. |

### 3.2 Discovery

**Directory (PD-2).**
- Tabs: "Featured" (default) and "All".
- Featured is ordered weighted-random. The seed is `sha256(installSalt || roundId)`, so the order is stable for one device and round and differs across devices. This follows the Agora default and limits herding (`dossier:identity_research.md:138,325` [doc]).
- "All" has a sort menu: "Random" (default), "Longest registered", "A to Z".
- Rows show: thumbnail or identicon, `@handle`, display name (secondary), `#index`, chips (Featured, New this round, Key changed, Lookalike, Proof missing), and the short fingerprint.
- In round context each row also shows "Voted on 7 of 12" for the current round (from the round route list, §3.6), and an "Add" button that adds the delegate to the allocation draft (max 10).

**Search** runs locally over the snapshot. Nothing is sent to any server.
- `@han` or `han`: case-insensitive handle prefix.
- Display-name substring, NFKC-normalized, bidi controls stripped.
- `#123` or `123`: exact index.
- A pasted `https://link.vizor.cash/d/123...` link opens that profile. This is the desktop path for links.
- An exact handle match is pinned first.
- Hidden delegates are excluded, except for an exact `#index` or a link.

**Profile (PD-3).**
- Header: 96 px avatar, `@handle`, display name, `#index`, a Featured chip ("Featured by Vizor" with an info sheet: "Featured doesn't mean Vizor agrees with their votes."), and "Registered Sep 12, 2026".
- Verification row: "Verified on X · checked 3 hours ago", or the GitHub/domain equivalent.
- Key block: fingerprint (§3.2.1), identicon, "Key details" sheet with the full DK and key history.
- Bio, at most 160 characters, plain text, no tappable links.
- "Open X profile" opens the external browser with the existing IP-linkage disclosure pattern (`vizor:lib/src/core/config/zcash_explorer.dart:11-14` [code]).
- "This round": a per-proposal list of the delegate's public votes so far ("Yes", "Not voted yet").
- "Voting record": past rounds, each "Voted on N of M" and expandable per proposal.
- If you delegated: "You delegated 12.5 ZEC to @alice in this round."
- Actions:
  - "Delegate to @alice": adds to the draft and opens PD-4.
  - "Copy link". Mobile also has "Share".
  - "Report", "Hide delegate".

#### 3.2.1 Trust signals

- **Fingerprint** [inference, contract I2]: the first 16 data characters of the DK's bech32m string, in four groups (`qxy7 3mzt 9xpa 2wqe`). Because it is literally a substring of the key in the X proof post, a user can compare it with the post by eye.
  - The identicon is a 5x5 symmetric glyph from the same bytes.
  - Screen readers spell it out in groups.
- **Lookalike warning.**
  - The authoritative source is the snapshot's `lookalike_of: [delegate_index]`, which the service computes with UTS #39 skeletons (`dossier:identity_research.md:308-319` [doc]).
  - Client defence in depth for ASCII handles (`[A-Za-z0-9_]{1,15}`):
    - Compute a skeleton: lowercase; map `0→o`, `1→l`, `i→l`; then `rn→m` and `vv→w`; strip `_`.
    - Warn when the skeleton equals, or is within Damerau distance 1 (handles of 5+ characters) of, a skeleton belonging to a Featured delegate or to any delegate registered earlier.
  - The older or Featured entry is never flagged.
  - Copy: "Looks like @zcash (Featured). Check the key fingerprint before you delegate."
  - Shown on the list row (chip), profile (banner), allocation row and review row.
- **New this round**: `registered_height > round.created_at_height` (`chain:proto/svote/v1/types.proto:49` [code]). Copy: "Registered after this round started. Make sure this is who you expect."
- **Key changed**: the key history has an entry with `effective_height > round.created_at_height`. Copy: "This delegate's key changed on Oct 2. Check the fingerprint." For a delegate you already delegated to, the status screen adds: "Votes made with the new key count for your delegation."
- **Verification state**, from the snapshot `proofs[].state`:
  - `verified`: tick.
  - `stale`: "Couldn't check recently". Informational only.
  - `removed`: "Proof post missing". Not searchable after the service's grace period; new delegation blocked.
  - `suspended`: "X account suspended". New delegation blocked.
  - `key_change_pending`: "Key change pending". New delegation blocked.
- **Chain cross-check** (blocking): the snapshot `dk_pubkey` and `status` must equal the chain record for `delegate_index`, proved via IAVL (§7.9) at review time. On mismatch: "This delegate's details don't match the voting chain. Vizor blocked delegation to keep your ZEC safe."

### 3.3 Allocation editor (PD-4)

**Inputs** come from the SDK plan: `eligibleBallots` (T, the sum of VAN `num_ballots` after the privacy trim) and `proxyAvailability`. One ballot is 0.125 ZEC (`zv:zcash_voting/src/governance.rs:11-15` [code]).

**Rows:**
- Up to 10 delegate rows. Each has avatar, handle, fingerprint, an input, the computed result "= 12.375 ZEC · 24.8%", and remove.
- A fixed last row, "Keep for myself", which is computed and read-only.
- An "Add delegate" button opens PD-2 as a picker. It is disabled at 10, with the hint "You can choose up to 10 delegates."

**Modes:**
- "Percent" (default): 0.1% steps, text field plus steppers.
- "ZEC": steppers of ±0.125 and a text field that rounds down on blur with the notice "Rounded down to 12.375 ZEC (0.125 ZEC steps)".
- Toggle label: "Show in ZEC" / "Show in percent".

**Quick actions:**
- "Split evenly".
- A "Delegate everything" switch. When it is on, percentages are treated as relative weights, normalized to 100%, and "Keep for myself" shows 0.

**Exact rounding.** The SDK `preview_proxy_allocation` is authoritative. Dart only formats. The preview is called on each edit, debounced 150 ms; it is a pure FFI call.
- Percent mode: each delegate `i` asks for basis points `p_i`, and keep `k = 10000 − Σp_i` (k = 0 when Delegate everything is on, after normalization).
  - Quotas are `q = T·p/10000`. Floors go to each delegate and to keep. The leftover ballots go one by one to the largest fractional parts. Ties go to delegates in row order, then keep.
  - Any delegate with `p_i > 0` and 0 ballots is raised to 1. The ballot is taken from keep, or else from the delegate with the most ballots (as long as it has more than 1).
  - If `T < n`: error "You need at least 0.125 ZEC of voting power for each delegate."
- ZEC mode: `b_i = floor(zat_i / 12_500_000)` and `keep = T − Σb_i`. If keep < 0: "That's more than your voting power. Lower an amount to continue."
- Each delegate gets at least 1 ballot ("Each delegate needs at least 0.125 ZEC").

**VAN packing is SDK-owned and invisible.** Delegates are assigned to the user's VANs (normally at most 2, the privacy trim at `zv:zcash_voting/src/note_bundling.rs:21-52` [code]):
- first-fit decreasing;
- each delegate in one VAN where possible, split across VANs only when needed;
- at most 10 DCs per VAN;
- the leftover per VAN is that VAN's kept weight.

The preview returns `dc_count` and `tx_count` for the progress screen only.

**Notices:**
- Privacy trim: the existing line (`voting_proposal_detail_screen.dart:542-548` [code]).
- Late round, if `now ≥ vote_end_time − 24 h`: "Voting ends in 9 hours. Your delegates may not have time to vote. Check what they've voted on so far."
- Block if `now ≥ vote_end_time − 10 min`: "It's too late to delegate in this round."

**Draft persistence.** The draft is stored like ballot drafts (`vizor:lib/src/features/voting/voting_flow_models.dart:258-330` [code]) under `zcash_voting_proxy_draft_{account}|{round}`. Contents: `[{delegateIndex, dkFingerprint, handleAtSelection, mode, value}]`. It is deleted once the SDK durably records the allocation. Dart keeps no mirror of durable rows (`vizor:rust/src/wallet/voting/README.md:161-166` [doc]).

### 3.4 Review and confirm (PD-5, PD-10)

**Standalone review** (`/voting/poll/:roundId/delegate/review`):
- Rows: avatar, handle, fingerprint, chips, ZEC and percent.
- A "Keep for myself: 12.5 ZEC" line, with "You can vote with it after delegating."
- Three fact rows, each with a "Learn more" sheet:
  - "Final for this round": §9 C1.
  - "Your delegates vote in public": C3.
  - "Your amount and identity stay private": C4.
- Checkbox (required): "I understand this delegation is final and can't be changed this round."
- Primary button: "Delegate 37.5 ZEC". Secondary: "Edit".
- If the kept weight is > 0 and the draft has answers, an extra secondary button "Vote with my remaining 12.5 ZEC too" switches to the combined path.

**Combined review** (the existing `/voting/poll/:roundId/review`, `voting_review_screen.dart:247,287` [code]):
- A "Delegations (final)" section sits above "Your votes (12.5 ZEC)".
- The same checkbox.
- The CTA becomes "Delegate and submit votes".
- Submission is one atomic batch per VAN: DCs first, then casts (baseline).

**Preflight on open:**
- Refresh the registry if older than 10 minutes.
- Prove each chosen delegate's chain record (§7.9).
- Re-run the preview.
- Any failure blocks the CTA with a specific message (§3.10).

**Hardware note** (Keystone/Ledger, only if ZKP1 is not yet signed): "Your Keystone will ask you to authorize voting for this round. It shows your full voting amount, not what you delegate. Your delegate choices are made in Vizor and aren't shown on the device."

### 3.5 Progress (PD-6)

This reuses `voting_status_screen.dart` and `mobile_voting_submission_progress_screen.dart`. Each `_StepRow` (`voting_status_screen.dart:1020-1058` [code]) shows only when its step applies.

1. "Signing with Keystone" / "Signing with Ledger" (existing panels `:1004-1017`, `:1443-1800`).
2. "Proving voting authority" (ZKP1, existing).
3. "Creating delegation proofs": 3 of 5 (ZKP4 count; includes ZKP2 count when combined: "Creating proofs 7 of 20").
4. "Submitting to the voting chain": tx i of n.
5. "Sending shares to helpers": until the immediate share is confirmed (`vizor:lib/src/features/voting/voting_resume_plan.dart:16-19` [code]).
6. "Done", then navigate to PD-7 with the banner "Delegation sent. It's final for this round."

Desktop keeps "Don't close the window..." (`:967`). Mobile keeps "Don't leave this window." Before the first broadcast: a "Cancel" button. Afterwards the button is hidden and the caption reads "Submitted. This can't be undone."

### 3.6 Post-delegation status (PD-7, `/voting/poll/:roundId/delegations`)

**Header:**
- "You delegated 37.5 ZEC to 3 delegates · Final for this round".
- When kept weight remains: "12.5 ZEC kept for your own vote" with a "Vote now" button, or "You voted with it" once done.
- Amounts are masked when privacy mode is on. The voting UI does not read `privacyModeProvider` today ([code] per `dossier:vizor.md` §2.1); add that here.

**Per delegate card:** avatar, handle, fingerprint, chips, "12.5 ZEC", a share-progress bar, then a per-proposal list:

| Condition | Label | Icon |
|---|---|---|
| Round ACTIVE, no route | "Hasn't voted yet" | clock |
| Route present (decision is an option) | "Voted Yes" + "Verified" tick once proved | check |
| Route present, option is an explicit Abstain option | "Voted Abstain" | check |
| Round TALLYING/FINALIZED, no route | "Didn't vote. Your ZEC abstains on this proposal." + "Verified" once non-membership is proved | dash |
| Delegate inactive or revoked, round ACTIVE | "Can't vote anymore. Your ZEC will abstain on proposals they haven't voted on." | warning |

**Summary line**, the "end-to-end" sentence: "Your 12.5 ZEC to @alice: Yes on P1 · No on P2 · Didn't vote on P3."

**Share progress**, from `ProxyAllocationStatusView.shares_confirmed/shares_total` (SDK share tracking; helper quorum per `zv:zcash_voting/src/share_tracking/confirmation.rs:19-28` [code]):
- All confirmed: "All shares counted".
- Partly confirmed, round ACTIVE: "9 of 16 shares counted. Helpers add the rest at random times before voting ends, so they can't be linked to you."
- Partly confirmed after the round ended: warning "14 of 16 shares reached the chain in time. Part of this delegation wasn't counted."
- At FINALIZED, Vizor proves the share-nullifier keys (§7.9) and shows "Counted (verified on chain)".

**Round ended without results** (tally timeout): reuse the existing results copy.

**Polling.** Foreground only; mobile Dart work is foreground-only (`vizor:lib/src/core/layout/app_process_work_policy.dart:4-17` [code via dossier]).
- Fetch once on open, then every 30 s while ACTIVE and visible, every 120 s while TALLYING, and stop at FINALIZED.
- Back off on errors 30, 60, 120 s, capped at 5 min.
- Use the incremental whole-round route list `GET /delegate-routes/{round_id}?from_height=` (contract C3), never a per-delegate query, so the server does not learn which delegate you care about.
- On failure: "Couldn't refresh. Showing results from 10:42." Never log URLs or keys (`vizor:docs/voting-participation.md:14-20` [doc]).

### 3.7 Coexistence with your own vote

- **Order rule (copy C5).** Delegation is offered only while `RoundPlanView.proxyAvailability == available`. The SDK reports `alreadyVoted` once any cast for the round has been submitted on any VAN. This is stricter than the per-VAN circuit rule because Vizor casts on all bundles together (ballot intents are per (round, wallet, proposal), `zv:zcash_voting/src/storage/migrations/001_init.sql:170-181` [code via dossier]).
- Ballot intents recorded by an earlier failed submission do **not** block delegation; only a submitted cast does.
- **"Delegate more."** While still available, the user can add delegates in a second batch from the successor VAN (still MAX authority), up to 10 delegates per round in total across batches. The editor shows existing delegations as locked rows.
- **Voting power display.** The voting-power row reads "Voting power 50 ZEC · 37.5 ZEC delegated · 12.5 ZEC left to vote", and proposal casting uses the kept weight.
- When everything is delegated, answers are not editable. The CTA is "View delegation status" and the poll card state is `delegated`.
- **Blocked explanation**, shown on the PD-1 card when `alreadyVoted`: "You've already voted in this round, so you can't delegate now. Delegation has to come before your own votes so the same ZEC is never counted twice."

### 3.8 Hardware accounts (Keystone, Ledger)

- Only ZKP1 needs the device. It is unchanged and signs to the account's own hotkey. The SDK memo stays "I am authorizing this hotkey managed by my wallet to vote on {round}. Amount: X ZEC." (`zv:zcash_voting/src/delegate.rs:2426-2449` [code via dossier]).
- ZKP4 and DCs are signed by the phone-held hotkey, like votes today (`dossier:vizor.md` §1.6). No firmware change.
- If ZKP1 is already confirmed (for example after an earlier "delegate more"), no device step is shown.
- Cancelling device signing keeps saved partial signatures (`vizor:AGENTS.md:727` [doc]). The allocation stays recorded and shows as "Not sent yet" with "Resume" and "Discard".
- Ledger on desktop and Keystone QR on mobile reuse the existing panels and copy, plus the §3.4 note beside the QR or memo.

### 3.9 Poll list, detail and results

- **Poll card** (`voting_polls_screen.dart:927-940` [code]):
  - Add `_PollCardState.delegated` (label "Delegated", action "View status").
  - For partial delegation, keep `active`/`voted` and add the subtitle "37.5 ZEC delegated".
  - The ineligible line "Already used for this round" (`:503-507` [code]) yields to "Delegated" when the local plan has proxy allocations.
- **Detail**: card PD-1 plus the voting-power row (§3.7).
- **Results** (`voting_results_screen.dart:734` [code]):
  - "Your vote" covers the kept weight.
  - New "Through your delegates" rows: "@alice · 12.5 ZEC · Yes", or "Didn't vote".
  - Footnote: "Results include votes delegates made for the people who delegated to them."

### 3.10 Delegator error catalogue

| Code | When | Copy | Retry | Where |
|---|---|---|---|---|
| `gateOff` | G1-G5 fail | hidden; deep link shows "Delegates aren't available in this version of Vizor." | n/a | PD-1/2/3 |
| `registryUnavailable` | no valid snapshot, never fetched | "Couldn't load delegates. Check your connection and try again." | yes | PD-2 |
| `registryStale` | snapshot older than 10 min at review and refresh failed | "The delegate list is out of date. Connect to the internet and try again." | yes | PD-5 |
| `chainMismatch` | snapshot DK/status differs from the proved chain record | "This delegate's details don't match the voting chain. Vizor blocked this delegation to keep your ZEC safe." | no | PD-3/5 |
| `delegateUnavailable` | inactive, revoked, delisted, proof removed, suspended, key change pending | "@alice isn't accepting delegations right now." | no | PD-3/4/5 |
| `tooMany` | more than 10 | "You can choose up to 10 delegates." | n/a | PD-4 |
| `belowMinimum` | an entry under 1 ballot | "Each delegate needs at least 0.125 ZEC." | n/a | PD-4 |
| `overAllocated` | Σ > T | "That's more than your voting power. Lower an amount to continue." | n/a | PD-4 |
| `notEnoughPower` | T < n or T = 0 | "You need at least 0.125 ZEC of voting power for each delegate." | n/a | PD-4 |
| `alreadyVoted` | availability changed | copy C5 | no | PD-1/4/5 |
| `tooLate` | now ≥ end − 10 min | "It's too late to delegate in this round." | no | PD-1/4/5 |
| `walletSync` | `waitingForWalletSync` | existing "Waiting for wallet sync" | auto | PD-6 |
| `notEligible` | eligibility error | existing friendly eligibility copy (`voting_proposal_detail_screen.dart:520-534`) | yes | PD-1 |
| `proofFailed` / `network` | pre-broadcast failure | `friendlyVotingErrorMessage` + "Nothing was sent. Try again." | yes | PD-6 |
| `rejected` | chain rejected a batch (e.g. VAN already spent on another install) | "The voting chain rejected this delegation. Your voting power for this round may have been used elsewhere." + diagnostic | no | PD-6/7 |
| `expired` | round ended before broadcast | "Voting ended before your delegation was sent. Nothing was delegated." | no | PD-6/7 |
| `partialCount` | round ended with unconfirmed DC shares | partial copy in §3.6 | no | PD-7 |
| `hardwareCancelled` | device signing cancelled | "Delegation not sent yet." + Resume / Discard | yes | PD-1/6 |
| `softwareSecretMissing` | software account without a readable secret | the existing "requires a software account" copy (`voting_submission_job_provider.dart:790`) | no | PD-6 |

---

## 4. Delegate journey

### 4.1 Eligibility and placement

- **Entry.** Mobile: a "Delegate profile" row in Settings, next to "Coinholder voting" (`vizor:lib/src/features/settings/screens/mobile/mobile_settings_screen.dart:98-107` [code]). Desktop: a "Delegate profile" tab in the Vote section. Both route to `/voting/delegate`, which shows DG-8 if registered and DG-1 otherwise.
- **Software accounts only in v1.** The DK is derived on demand in Rust from the account seed, which only software accounts have in Vizor (`dossier:client.md` §1 point 5).
  - Hardware accounts see DG-12: "Delegate profiles need a software account in this version. Create or switch to a software account to become a delegate."
- **One delegate identity per account.** `identity_index` starts at 0 and increments on rotation.

### 4.2 Onboarding (DG-1 to DG-7)

**DG-1 Intro.**
- "As a delegate, people can give you their voting power. Your votes as a delegate are public and linked to your X account. You won't see who delegated to you or how much."
- Bullets:
  - "You vote once per proposal. It's final."
  - "Proposals you don't vote on count as abstain for everyone who delegated to you."
  - "Your private votes stay private."
- CTA "Get started".

**DG-2 Key.**
- Rust derives the DK (§7.3) and returns the public key, bech32m string and fingerprint.
- Copy: "Your delegate key comes from this account's recovery phrase. If you lose this device, restore the account to get it back."
- Required checkbox: "I've backed up this account's recovery phrase". Link "Show recovery phrase" (existing settings flow).
- The fingerprint is displayed.

**DG-3 Proof method.**
- Choices: "X (recommended)", "GitHub", "Your website".
- Handle or locator input with validation:
  - X: `^[A-Za-z0-9_]{1,15}$`;
  - GitHub: username;
  - Domain: FQDN.
- Note: "X shows your profile photo and name in Vizor. GitHub and website profiles show a generated image."

**DG-4 Post composer.** The exact proof text comes from Rust (contract I5), ASCII, under 280 characters, with no URL:

  ```
  I accept Zcash shielded-vote delegations.
  zvote-delegate:v1:<bech32m DK>
  ```

- Actions: "Copy text"; "Open X" (`https://x.com/intent/post?text=...` in the external browser, with the IP disclosure).
- GitHub: "Create a public gist named zvote-delegate.txt with this text".
- Domain: "Add a DNS TXT record `_zvote-delegate.<domain>` with this text, or publish `https://<domain>/.well-known/zvote-delegate.json`."
- Then: "Paste the link to your post" (X post URL or gist URL; domain needs no link). Validate the URL host: `x.com`/`twitter.com` status URLs, or `gist.github.com`.

**DG-5 Verification.**
- `POST verifier /v1/proofs`, then poll `GET /v1/proofs/{id}`: every 3 s for 30 s, then every 10 s, for at most 10 minutes. Foreground only.
- States: "Waiting to check", "Checking your post", "Verified as @alice".
- Failure copy:
  - `post_not_found`: "We couldn't find that post. Check the link and that the post is public."
  - `key_not_in_post`: "The post doesn't contain your key. Copy the text again and repost."
  - `account_protected`: "Your X account is protected. Make it public while we check."
  - `account_suspended`: "This X account is suspended."
  - `author_mismatch`: "That post is from a different account than @alice."
  - `rate_limited` / `unavailable`: "Verification is busy. Try again in a few minutes."
  - `domain_record_missing`: "We couldn't find the record on your domain yet. DNS changes can take a while."
- On budget expiry: "Still checking. You can leave and come back." The state is persisted (§7.5).

**DG-6 Register on chain.**
- Rust builds `MsgRegisterDelegate` (DK public key, verifier attestation, DK-signed registration statement including `x_user_id`; contracts C6/I5), submits it, and polls `/tx/{hash}`.
- States: "Registering", "Registered as delegate #123".
- Errors:
  - `attestation_expired`: back to DG-5 with "Your verification expired. We'll check again."
  - `dk_already_registered`: "This key is already registered." Then trigger recovery (§8).
  - `capability_missing`: hidden by G5.
  - network: retry.

**DG-7 Done.**
- Profile preview.
- "Your profile appears in the directory within about an hour" (contract I2 latency).
- Share link `https://link.vizor.cash/d/123#k=<hint>`: "Copy link"; mobile also "Share".
- Suggested text: "Delegate your Zcash vote to me in Vizor: <link>".

### 4.3 Dashboard (DG-8)

- **Profile card:** avatar, handle, `#index`, fingerprint, Featured chip, verification state, plus a banner when the proof is `stale`, `removed` or `suspended` ("Your X proof is missing. Repost it to stay listed.", with the DG-4 composer).
- **Status:** "Accepting delegations", or "Not accepting delegations" when inactive.
- **Active rounds list:** each row shows "Public votes: 5 of 12", the deadline, and "Delegations so far: about 40 (estimated)" if contract C5 exists.
  - Explanation: "Counted from public share reveals, which arrive with random delays. Amounts stay hidden, even from you."
  - Recommended: own dashboard only (open question 1).
- **Past rounds:** each row shows the public votes and a "Your private vote" column.
- **Key management:** opens DG-11.

### 4.4 Public voting (DG-9, DG-10)

**DG-9.** For each proposal, two columns side by side:
- **"Your public vote (as @alice)":** either the button "Vote publicly", or "Yes · Oct 3, 14:02 · Verified".
- **"Your private vote":** comes from this account's own round plan (`completedVoteDisplay`, `vizor:lib/src/rust/third_party/zcash_voting/wire.dart:154-170` [code]) or its draft. Values: "Yes", "Not voted", or "Delegated". It carries a lock icon and the caption "Only you can see this."
- A deadline banner: "Public votes close Oct 9, 18:00. Proposals you don't vote on count as abstain for everyone who delegated to you."

**DG-10 confirm sheet.**
- Title: "Vote Yes publicly on Proposal 3?"
- Body: "This vote is public and final. Everyone can see that @alice voted Yes. It applies to all voting power delegated to you for this proposal. You can't change it."
- Primary button: "Vote Yes publicly". Secondary: "Cancel".
- Confirming requires the wallet password or biometrics, reusing the spend re-auth [inference: reuse the existing send-confirmation helper].
- Then Rust signs `MsgDelegateRoute(round, proposal, decision, delegate_index)` with the DK. Vizor submits it, polls the tx, and IAVL-verifies the route.

**DG-10 states and errors:**
- states: confirming, re-auth, signing, submitting, waitingInclusion, recorded(verified);
- `already_routed` (one-shot): show the existing vote with "You've already voted publicly on this proposal.";
- `round_closed`: "Public voting for this round has ended.";
- `key_revoked`: "Your delegate key was replaced. Update your key to vote.";
- `invalid_decision`: "This option isn't valid for this proposal. Refresh and try again.";
- network: retry, idempotent because the route is one-shot.

**Timing hint** (no enforcement): routes and the delegate's own private casts are submitted by separate jobs. A delegate who also votes privately should use Tor, so timing and IP do not link their private VAN to their public identity (§Risks).

### 4.5 Key management (DG-11)

- **Rotate key:**
  1. Explain.
  2. Derive the DK at `identity_index + 1`.
  3. New proof post (DG-4/5).
  4. `MsgRotateDelegateKey` co-signed by the old and new DK; takes effect immediately per registry policy.
  - Mid-round warning: "People who delegated to you this round will see 'Key changed'."
  - If the old key is unavailable: the registry's cooling-off path (contract I6). Copy: "Without your old key, the change takes effect after a waiting period."
- **Stop accepting delegations:** `MsgSetDelegateStatus(inactive)`. Copy: "You'll be hidden from the directory and no one can delegate to you. You can still vote publicly in rounds already in progress." Recommended semantics, contract C8.
- **Resume accepting:** `active`.
- **Retire profile:** `revoked`; irreversible; requires typing the handle.

### 4.6 Delegate error catalogue

| Code | Copy | Retry |
|---|---|---|
| `hardwareAccount` | DG-12 copy | no |
| `seedUnavailable` | "Unlock your wallet to use your delegate key." | yes |
| verifier failures | see DG-5 | yes |
| `attestationExpired` | "Your verification expired. We'll check again." | auto |
| `dkAlreadyRegistered` | "This key is already registered. Restoring your profile." | auto |
| `alreadyRouted` | "You've already voted publicly on this proposal." | no |
| `roundClosed` | "Public voting for this round has ended." | no |
| `keyRevoked` | "Your delegate key was replaced. Update your key to vote." | no |
| `network` | "Couldn't reach the voting chain. Try again." | yes |

---

## 5. Screen inventory

### 5.1 Delegator

| ID | Screen (route) | States | Data source | Actions |
|---|---|---|---|---|
| PD-1 | Delegation card in poll detail (`/voting/poll/:roundId`) | hidden (gate) · available · available with existing delegations ("Delegate more") · not sent yet · job in progress · delegated (part) · delegated (all) · blocked alreadyVoted · blocked noPower · late-round warning · blocked tooLate · eligibility error | `RoundPlanView` proxy fields, `proxyDelegationGateProvider`, `votingSubmissionJobProvider` | Choose delegates · View status · Resume · Discard · Learn more |
| PD-2 | Delegates directory (`/voting/delegates?round=`; desktop master pane, mobile page) | first load (skeleton) · Featured · All (sort) · searching · no results · stale banner ("Updated 2 h ago") · offline with cache · error without cache · picker mode (round, selected chips, 10 cap) · gate off | `delegateRegistryProvider`, `delegateThumbnailProvider`, `hiddenDelegatesProvider`, `roundDelegateRoutesProvider` (voted counts), `proxyAllocationDraftProvider` | search · tab · sort · open profile · Add/Remove (picker) · paste link · Hidden delegates |
| PD-3 | Delegate profile (`/voting/delegates/:delegateIndex?round=`) | loading · verified · Featured · New this round · Key changed · Lookalike · proof stale/removed · suspended · key change pending · delisted · inactive/revoked · chain mismatch (blocking) · unknown index · hidden by me · you delegated · key-hint mismatch (from link) · record loading/error | snapshot entry, chain record (IAVL), round route lists (cached), local plan | Delegate to @x · Copy link · Share · Open X profile · Report · Hide/Unhide · Key details |
| PD-4 | Allocation editor (`/voting/poll/:roundId/delegate`) | empty · percent · ZEC · Delegate everything · valid · overAllocated · belowMinimum · tooMany · notEnoughPower · rounding notice · privacy-trim notice · preview loading/error · late warning · tooLate/alreadyVoted (availability changed) · existing locked rows | draft, SDK preview, `RoundPlanView.eligibleBallots` | add · remove · edit · mode toggle · Split evenly · Delegate everything · Continue |
| PD-5 | Delegation review (`/voting/poll/:roundId/delegate/review`) | ready · checkbox unchecked · refreshing registry · registryStale · chainMismatch · delegateUnavailable · late warning · hardware note · starting | preview, snapshot, chain records, draft ballot | Edit · Delegate X ZEC · Vote with my remaining X ZEC too |
| PD-6 | Progress (`/voting/poll/:roundId/status?account=&kind=proxy`) | §6 states, Keystone QR and Ledger panels, cancel before broadcast, retryable error, terminal rejected/expired | `votingSubmissionJobProvider`, session phase, plan | Cancel (pre-broadcast) · Retry · Scan Keystone · Cancel Ledger |
| PD-7 | Delegation status (`/voting/poll/:roundId/delegations`) | submitted · confirming · per proposal pending/voted/abstained/verified · shares k/n · all counted · partial count · tallying · finalized (counted/verified) · no results (timeout) · key changed · delegate inactive · offline with last refresh time · privacy-masked | plan `proxyAllocations`, `roundDelegateRoutesProvider`, `delegateRouteVerificationProvider`, share tracking | Vote now (kept) · open profile · refresh · Learn more |
| PD-8 | Results additions (`/voting/poll/:roundId/results`) | with/without delegations · per delegate Voted/Didn't vote | tally-results, local plan, route list | open delegation status |
| PD-9 | Poll card (`/voting`) | + `delegated`, + "X ZEC delegated" subtitle | plan summary | View status |
| PD-10 | Combined review (`/voting/poll/:roundId/review`) | + Delegations section · checkbox | as PD-5 + ballot draft | Delegate and submit votes |
| PD-11 | Hidden delegates (`/voting/delegates/hidden`) | empty · list | `hiddenDelegatesProvider` | Unhide |
| PD-12 | Report sheet (modal) | form · sending · sent · error · rate-limited | report client | reason, details, "Also hide", Send |
| PD-13 | Round picker sheet | one round (skipped) · several · none eligible ("No round you can delegate in right now") | rounds provider, eligibility | pick round |

### 5.2 Delegate

| ID | Screen (route) | States | Data source | Actions |
|---|---|---|---|---|
| DG-1 | Intro (`/voting/delegate/start`) | default · gate off | gate | Get started |
| DG-2 | Key (`/voting/delegate/key`) | deriving · ready · seedUnavailable | Rust `delegate_key_public` | confirm backup · Show recovery phrase |
| DG-3 | Proof method (`/voting/delegate/proof`) | X/GitHub/domain · invalid handle | local | Continue |
| DG-4 | Post composer (`/voting/delegate/proof/post`) | text ready · copied · link pasted · link invalid | Rust `delegate_proof_text` | Copy · Open X · Paste link · Submit |
| DG-5 | Verification (`/voting/delegate/proof/status`) | queued · checking · verified · failed(code) · still checking (budget) | verifier API | Retry · Edit link · Leave |
| DG-6 | Registration (`/voting/delegate/register`) | submitting · waiting · registered #n · attestationExpired · dkAlreadyRegistered · network | chain tx status | Retry |
| DG-7 | Done | default | snapshot (may lag) | Copy link · Share · Go to dashboard |
| DG-8 | Dashboard (`/voting/delegate`) | registered · not yet in directory · proof stale/removed/suspended · inactive · revoked · offline | identity store, chain record, snapshot, pool activity | open round · Key management · Repost proof |
| DG-9 | Round public voting (`/voting/delegate/round/:roundId`) | per proposal not voted / submitting / voted (verified) · round closed · private column states | route list, own round plan | Vote publicly |
| DG-10 | Public vote confirm sheet | confirm · re-auth · signing · submitting · recorded · error(code) | Rust signer, chain | Vote X publicly · Cancel |
| DG-11 | Key management (`/voting/delegate/keys`) | active · inactive · rotation in progress · rotation cooling off · revoked | chain record, identity store | Rotate · Stop/Resume accepting · Retire |
| DG-12 | Software account required | default | account kind | Switch account |

Layout:
- Desktop uses master-detail panes inside the Vote section, with transparency per `vizor:AGENTS.md:1005-1009`, and `AppDialog` confirms.
- Mobile uses `CupertinoPage` routes. Sheets use `MobileBottomSafeArea(bottomPadding: token)` (`vizor:AGENTS.md:1011-1037` [doc]). The allocation editor has a bottom summary bar.
- UI branching uses `kAppFormFactor` only, never `Platform.is*` (`AGENTS.md:105-123`).

---

## 6. Proxy delegation job: state machine

Implementation: generalize `VotingSubmissionJobNotifier` (`vizor:lib/src/providers/voting/voting_submission_job_provider.dart:285+` [code]) rather than add a second job.
- Add `VotingSubmissionJobState.kind: VotingSubmissionKind {ballot, proxy, proxyAndBallot}`.
- Add `proxyStage: ProxyJobStage?`, derived from `VotingSessionPhase` plus the plan.
- Add `terminalReason: VotingSubmissionTerminalReason? {blocked, rejected, expired, notSent}`.
- Keep `VotingSubmissionJobStatus` (`:28-35`).
- Add `VotingSessionPhase.provingProxyDelegations` and `submittingProxyDelegations` between `delegated` and `readyToVote` (`vizor:lib/src/providers/voting/voting_state.dart:73-89` [code]).

```
idle --start(kind)--> preflight
preflight --gate/round/registry/chain-record fail--> BLOCKED(reason)            [terminal; retryable only for registryStale/network]
preflight --ok--> syncingWallet --ok--> checkingEligibility --ok--> recordingPlan
syncingWallet --waitingForWalletSync--> (paused; resumes on sync)            [existing behaviour :576-589]
recordingPlan --availability != available--> BLOCKED(alreadyVoted|tooLate|noPower)
recordingPlan --hw && plan.needsDelegationSigning--> awaitingKeystone | awaitingLedger
awaitingKeystone|awaitingLedger --signatures stored--> authorizing
awaitingKeystone|awaitingLedger --cancel--> NOT_SENT                         [allocation stays recorded; Resume/Discard]
recordingPlan --else--> authorizing (ZKP1 only if needed; may be composed into the same tx)
authorizing --> provingDelegations (ZKP4 x dc_count [+ ZKP2 x casts]) --> submitting (tx 1..n per VAN)
submitting --first accepted broadcast--> (POINT OF NO RETURN) confirming
submitting --chain rejects--> REJECTED(diagnostic)                           [terminal for that VAN]
confirming --included + leaf positions--> deliveringShares --immediate share confirmed--> COMPLETE
any pre-broadcast step --error--> ERROR(retryable)  --retry--> preflight (SDK plan resumes; idempotent)
any step --now >= vote_end_time before broadcast--> EXPIRED (SDK marks allocation Expired)
COMPLETE --> background: share tracking (confirmProxyShare) until all DC shares confirmed or round ends
```

| From | Event | To | Side effects |
|---|---|---|---|
| idle | `startProxyDelegation` | preflight | take `VotingSubmissionGuard` (`voting_submission_guard_provider.dart:44-118`) and `++generation` |
| preflight | ok | syncingWallet | registry refreshed to 10 min or newer; chain records proved |
| recordingPlan | ok | awaiting* / authorizing | SDK `set_proxy_allocation` (+ `set_ballot_intents` if combined) **before** the hardware/software branch, for the same reason as the comment at `:669-680` (the plan, including the ZKP1 signing need, is derived from durable intents); delete the Dart draft |
| awaiting* | signatures | authorizing | existing Keystone/Ledger handlers (`:375-470`, `:870-935`) |
| provingDelegations | progress | provingDelegations | `i/n` from SDK progress events |
| submitting | broadcast ok | confirming | Cancel hidden |
| deliveringShares | immediate share confirmed | COMPLETE | navigate to PD-7; share-tracking registry armed |
| any | `cancel` (pre-broadcast) | NOT_SENT | generation bump; guard released; partial HW signatures kept |
| NOT_SENT | Discard | idle | SDK `clear_proxy_allocation` (refused after broadcast) |

Software accounts need the mnemonic only when ZKP1 is needed (`:767-795`). ZKP4 uses the stored hotkey. `_run` changes:
- (a) Accept `draft.isEmpty && proxyAllocationRecordedOrDrafted`. Today it fails with "Choose at least one vote before submitting." (`:664`).
- (b) Record the proxy allocation first.
- (c) After delegation, call `_submitVotesAndShares` with an empty draft when `kind == proxy`, so the SDK drives DC batches and shares.

**Onboarding sub-machine** (`DelegateOnboardingNotifier`, persisted):

```
notStarted -> keyReady -> methodChosen -> awaitingPost -> verifying -> verified(attestation, expiresAt) -> registering -> registered(index)
verifying -fail(code)-> awaitingPost;  verified -expired-> verifying;  registering -reject(code)-> verified|awaitingPost
```

**Route submission** (`DelegateRouteSubmissionNotifier`, keyed `(account, round, proposal)`):

```
idle -> confirming -> reauth -> signing -> submitting -> waitingInclusion -> recorded(verified)
error: alreadyRouted (terminal, show existing), roundClosed, keyRevoked, network (retry)
```

---

## 7. Integration

### 7.1 Routes

Add builders to `vizor:lib/src/features/voting/voting_routes.dart:1-24` [code]:
- `votingProxyAllocationRoute(roundId)` = `${votingPollRoute}/delegate`
- `votingProxyReviewRoute` = `/delegate/review`
- `votingProxyStatusRoute` = `/delegations`
- `delegatesDirectoryRoute({roundId})` = `/voting/delegates`
- `delegateProfileRoute(index, {roundId, keyHint})`
- `delegateDashboardRoute` = `/voting/delegate`, plus the onboarding sub-routes in §5.2.

Register them in three places:
- desktop `GoRoute`s next to `vizor:lib/app.dart:1496-1543` (wrapped in `_guardVotingScreen`, `:1545-1547`);
- mobile `CupertinoPage` routes in `vizor:lib/src/core/navigation/mobile_routes.dart:417-480` (wrapped in `MobileVotingAccountGuard`);
- back labels in `vizor:lib/src/core/navigation/app_back_resolver.dart:61-67,148-161`: "Delegates", "Delegate", "Delegate profile", "Delegations".

### 7.2 Providers and jobs (new files under `lib/src/providers/voting/delegates/`)

- **`proxyDelegationGateProvider(roundId)`**: §2.
- **`delegateRegistryProvider`** (`AsyncNotifier<DelegateDirectoryState{snapshot, fetchedAt, stale, error}>`), plus `refresh()`.
- **`delegateThumbnailProvider(sha256)`** (`FutureProvider.family<Uint8List?>`).
- **`delegateChainRecordProvider(delegateIndex)`**: REST record plus an IAVL-proved record.
- **`delegateSearchProvider((query, tab, sort))`**.
- **`hiddenDelegatesProvider`** (Notifier; SharedPreferences `vizor_hidden_delegates_v1_{network}`).
- **`proxyAllocationDraftProvider(VotingSessionKey)`** and **`proxyAllocationPreviewProvider(VotingSessionKey)`**.
- **`votingSubmissionJobProvider(key)`** (extended), plus `VotingSubmissionJobsNotifier.startProxyDelegation(roundId, {accountUuid, bool includeBallot})`, next to `start` (`:224-252`).
- **`proxyDelegationStatusProvider(VotingSessionKey)`** (plan + routes + verification).
- **`roundDelegateRoutesProvider(roundId)`**: incremental, foreground polling (§3.6).
- **`delegateRouteVerificationProvider((roundId, delegateIndex, proposalId))`**.
- **`delegatePoolActivityProvider(roundId)`**: optional, contract C5.
- **`delegateIdentityProvider(accountUuid)`**: secure store.
- **`delegateOnboardingNotifierProvider(accountUuid)`**, **`delegateRouteSubmissionProvider(...)`** and **`delegateKeyManagementProvider(accountUuid)`**: jobs with generation guards copied from `_isCurrentJob`.
- **`delegateReportClientProvider`**.
- **`pendingDelegateLinkProvider`**: in-memory park for deep links while locked.

### 7.3 Rust bridge

**SDK-backed** (methods on the opaque `VotingRoundSession`, `vizor:rust/src/api/voting_session.rs:223-299` [code]; DTOs flat in `zcash_voting::wire` because FRB scans it, `AGENTS.md:897-901`):

```rust
pub async fn preview_proxy_allocation(&self, req: ProxyAllocationRequestView) -> Result<ProxyAllocationPreviewView, VotingErrorView>; // pure
pub async fn set_proxy_allocation(&self, req: ProxyAllocationRequestView) -> Result<RoundPlanView, VotingErrorView>;       // durable, idempotent for identical req
pub async fn clear_proxy_allocation(&self) -> Result<RoundPlanView, VotingErrorView>;                                     // refused after first broadcast
pub async fn proxy_share_nullifiers(&self) -> Result<Vec<ProxyShareNullifierView>, VotingErrorView>;                      // for IAVL verification
// run_round / run_share_tracking (:407, :513) drive proxy steps unchanged; signer kinds unchanged (:82-89).

pub struct ProxyAllocationRequestView { pub entries: Vec<ProxyAllocationEntryView>, pub mode: ProxyAllocationModeView, pub delegate_everything: bool }
pub struct ProxyAllocationEntryView { pub delegate_index: u32, pub basis_points: u32, pub zatoshi: u64, pub dk_fingerprint: String, pub host_label: String /* <=256 B, e.g. "@alice" */ }
pub enum   ProxyAllocationModeView { Percent, Amount }
pub struct ProxyAllocationPreviewView { pub eligible_ballots: u64, pub entries: Vec<ProxyDcPlanView>, pub kept_ballots: u64, pub dc_count: u32, pub tx_count: u32, pub errors: Vec<ProxyPlanIssueView>, pub notices: Vec<ProxyPlanIssueView> }
pub struct ProxyDcPlanView { pub delegate_index: u32, pub ballots: u64, pub rounded: bool }
pub enum   ProxyPlanIssueView { TooMany, BelowMinimum{delegate_index:u32}, OverAllocated, NotEnoughPower, Rounded{delegate_index:u32}, Unavailable{reason: ProxyAvailabilityView} }
pub enum   ProxyAvailabilityView { Available, AlreadyVoted, NotSupported, NoVotingPower, RoundNotActive, TooLate, DelegateLimitReached }
pub struct ProxyAllocationStatusView { pub delegate_index: u32, pub ballots: u64, pub host_label: String, pub dk_fingerprint: String, pub phase: ProxyPhaseView, pub dc_count: u32, pub shares_total: u32, pub shares_confirmed: u32, pub tx_hashes: Vec<String>, pub submitted_at: Option<u64>, pub diagnostic: Option<SubmissionDiagnosticView> }
pub enum   ProxyPhaseView { Planned, Proving, Submitted, Confirmed, Rejected, Expired }
pub struct ProxyShareNullifierView { pub delegate_index: u32, pub dc_index: u32, pub share_index: u32, pub nullifier: Vec<u8> }
```

`RoundPlanView` (`vizor:lib/src/rust/third_party/zcash_voting/wire.dart:598-700` [code]) gains:
- `proxyAvailability`
- `eligibleBallots`, `delegatedBallots`, `keptBallots`
- `proxyAllocations: List<ProxyAllocationStatusView>`
- `proxyBundlesNeedingWork: Uint32List`
- `hasUnconfirmedProxyShares` (folded into `hasUnconfirmedShares`, so the share-tracking restorer keeps working, `voting_share_tracking_restorer_provider.dart:211-216`).

**Vizor-owned** (new `rust/src/api/delegate_identity.rs`; the seed never leaves Rust, the same pattern as `rust/src/wallet/voting/signer.rs`):

```rust
pub fn delegate_key_public(mnemonic: String, network: String, account_index: u32, identity_index: u32) -> Result<DelegateKeyView, VotingErrorView>;
pub struct DelegateKeyView { pub identity_index: u32, pub dk_pubkey: Vec<u8>, pub dk_bech32: String, pub fingerprint: String, pub proof_text: String }
pub fn delegate_build_registration(mnemonic: String, network: String, account_index: u32, identity_index: u32, attestation: DelegateAttestationView) -> Result<SignedVoteChainMessageView, VotingErrorView>;
pub fn delegate_build_route(mnemonic: String, network: String, account_index: u32, identity_index: u32, delegate_index: u32, round_id: String, proposal_id: u32, decision: u32) -> Result<SignedVoteChainMessageView, VotingErrorView>;
pub fn delegate_build_rotation(mnemonic: String, network: String, account_index: u32, old_identity_index: u32, new_identity_index: u32, delegate_index: u32, attestation: DelegateAttestationView) -> Result<SignedVoteChainMessageView, VotingErrorView>;
pub fn delegate_build_status(mnemonic: String, network: String, account_index: u32, identity_index: u32, delegate_index: u32, status: DelegateStatusView) -> Result<SignedVoteChainMessageView, VotingErrorView>;
pub async fn submit_vote_chain_message(msg: SignedVoteChainMessageView) -> Result<TxSubmissionView, VotingErrorView>; // via network_clients (routed)
pub fn verify_delegate_snapshot(bytes: Vec<u8>, signing_keys: Vec<TrustedKeyView>, network: String) -> Result<DelegateSnapshotView, VotingErrorView>;
pub fn verify_vote_store_proofs(network: String, evidence: VoteStoreEvidenceView, keys: Vec<VoteStoreKeyView>) -> Result<Vec<VoteStoreProofView>, VotingErrorView>;
```

- Message encoding (canonical protobuf) should come from `zcash_voting`, so all wallets share it (contract S9). Vizor only wraps it.
- DK derivation [inference; needs crypto review; contract S9/I5]: `zip32::arbitrary` with context `"ZcashVoteDelegateKeyV1"` over `[account_index', identity_index']`; Ed25519 secret = the first 32 bytes. Move to registered derivation once a ZIP number exists (`dossier:client.md` §1).
- Add the new network role "delegate chain messages / route list / registry" to the route table and to `sdk_network_construction_stays_in_the_factory` (`vizor:rust/src/wallet/voting/README.md:311-341` [doc]).
- Regenerate the bridge with `scripts/generate-rust-bridge.sh`.
- Bump the SDK pin at `rust/Cargo.toml:129-130`.

### 7.4 SDK plan and `NextStepKind`

New kinds (contract S3): `proxyDelegate`, `advanceProxyDelegation`, `submitProxyShares`, `confirmProxyShare`.
- `isVoteNextStepKind` (`vizor:lib/src/features/voting/voting_resume_plan.dart:60-71` [code]) is exhaustive on purpose. All four return `false`, so DC shares never count as vote work in `voteCarryingBundleIndexes` (`:84-94`).
- Add an exhaustive `isProxyNextStepKind`.
- `NextStepView` gains `dcIndex` rather than overloading `proposalId = 0` and `choice = delegate_index`.
- `RoundPlanActionKind` (`wire.dart:596`) gains `proxyDelegate`.
- Poll cards treat outstanding proxy work as `inProgress` ("Resume").

### 7.5 Storage

| Data | Store | Key | Scope |
|---|---|---|---|
| Delegate profile metadata (no secret) | `AppSecureStore.writeSecretString` (session-encrypted) | `zcash_account_delegate_profile_{accountUuid}` → `{v:1, identityIndex, dkPubkey, delegateIndex?, onboarding:{state, method, locator, proofId?, attestation?, attestationExpiresAt?, registrationTxHash?}}` | account |
| Proxy allocation draft | secure storage, like ballot drafts | `zcash_voting_proxy_draft_{account}\|{round}` | account × round |
| Durable allocation, DC rows, DC share rows | `zcash_voting` VotingDb (contract S1/S7) | n/a | account × round |
| Registry snapshot | app support file, re-verified on load | `delegates/{network}/snapshot.json` + `.sig` | device |
| Thumbnails | app support files, LRU 20 MB | `delegates/{network}/thumbs/{sha256}.png` | device |
| Verified route/share proofs (immutable once present) | app support file | `delegates/{network}/verified.json` | device |
| Hidden delegates | SharedPreferences | `vizor_hidden_delegates_v1_{network}` | device |
| Install salt (directory ordering) | SharedPreferences | `vizor_delegates_order_salt_v1` | device |
| Pending deep link | memory only | n/a | n/a |

The DK secret is never stored. It is derived per use from the mnemonic, which is only readable with an unlocked session (`vizor:lib/src/core/storage/app_secure_store.dart:358-375` [code via dossier]).

### 7.6 Account deletion and reset

In `_removeAccount` (`vizor:lib/src/providers/account_provider.dart:1737-1790` [code]), add best-effort steps next to `deleteVotingHotkeysForAccount` (`:1766`):
- `_storage.deleteDelegateProfile(uuid)`;
- `votingProxyDraftPersistence.deleteForAccount(uuid)`.

SDK rows go through the existing `_deleteDurableVotingStateForAccount` → `delete_voting_account_state` (`vizor:rust/src/api/voting.rs:1029-1045`), once contract S7 makes `clear_wallet_state` cover the proxy tables.

Device-scoped caches stay on per-account delete. A full wallet reset also clears the `delegates/` directory and the two SharedPreferences keys.

Account-deletion dialog addition when a delegate profile exists: "This account is delegate @alice (#123). Deleting it removes your delegate key from this device. Restore this account's recovery phrase to get it back. Any public votes you've made stay on the voting chain."

### 7.7 Destructive-operation drain

- The proxy job inherits the submission guard (`_replaceGuard` in `_startJob`).
- DC share tracking uses the existing registry and restorer.
- Each new account-scoped async job calls `votingShareTrackingRegistry.beginBackgroundWork(accountUuid:)` before its first `await` and aborts on `null` (`vizor:lib/src/providers/voting/voting_share_tracking_registry_provider.dart:18-34` [code]; `AGENTS.md:342-345` [doc]). That covers onboarding, route submission, key management, and proxy draft writes that race deletion.
- Registry refresh and verification caches are device-scoped and need no lease.
- Report sends are fire-and-forget with no account state.

### 7.8 Networking, registry, profile pictures, caching

- **Transport.**
  - All Dart HTTP goes through `NetworkHttpClient` (`vizor:lib/src/core/network/network_http_client.dart:226-300` [code]): Tor-aware, fails closed while Tor bootstraps, GET/POST only under Tor (`:25-34`).
  - Chain queries extend `VotingApiClient` with failover (`vizor:lib/src/services/voting/voting_api_client.dart:163-252` [code]).
  - Rust submissions use `network_clients`.
- **Registry snapshot.**
  - Mirrors come from static config `delegate_registry.snapshot_urls` (two mirrors, like the voting config loader `services/voting/voting_config_loader.dart:14-90`).
  - Whole-document download (gzip). Rust verifies Ed25519 over the canonical JSON with domain `zcash-shielded-vote:delegate-directory:v1`, using `delegate_registry.signing_keys`. These keys are purpose-separated and are not `trusted_keys`, which authenticate rounds (`dossier:client.md` §7).
  - Checks: `network`/`chain_id` match; `generated_at` within the last 7 days; `registry_height` must not regress below the cached copy.
  - Refresh: on directory open if older than 10 minutes; always at review; otherwise show the stale copy with "Updated 2 h ago".
- **Profile pictures.**
  - Never `Image.network`; there is none in `lib/src` today (`dossier:vizor.md` §4). Add a test that greps `lib/` for `Image.network|NetworkImage|CachedNetworkImage` and fails if it finds one.
  - Fetch: prefer one thumbnail pack per snapshot (`thumbs_pack_sha256`) when it is 4 MB or less. Otherwise fetch thumbnails by hash in visible order, prefetching all Featured thumbnails so the fetch pattern says less about interest.
  - Validate:
    - sha256 equals the snapshot value;
    - PNG magic;
    - at most 64 KB;
    - `instantiateImageCodec` dimensions at most 256×256.
  - Then draw with `Image.memory(bytes, cacheWidth: devicePx)`.
  - Any failure, `pfp_status != approved`, or a missing hash falls back to the identicon.
- **Never contact X**, `pbs.twimg.com` or GitHub avatars from the wallet. The only exception is the user-initiated "Open X profile" or "Open X" in the external browser, with a disclosure.
- **No per-delegate queries** for status or the directory:
  - routes come as a whole-round incremental list;
  - pool activity comes as a whole-round list;
  - profile voting records come from per-round lists, cached forever after FINALIZED.

### 7.9 Verification against chain state (IAVL)

Reuse the participation reader. It verifies `abci_query` IAVL membership and non-membership against a Tendermint commit anchored to bundled validator sets (`vizor:docs/voting-participation.md:22-60` [doc]; `rust/src/wallet/voting/participation.rs:18-23,94-101` [code via dossier]). Dart fetches `/commit`, `/validators` and `/abci_query`; Rust verifies.

New key kinds (contract C4): `DelegateRoute{round_id, delegate_index, proposal_id}`, `DelegateRecord{delegate_index}`, `ShareNullifier{round_id, nf}` (share nullifiers already exist as type `0x02`, `chain:x/vote/types/keys.go:100-114`).

**Policy:**
- Route present: prove membership once and cache forever (routes are one-shot).
- Route absent after TALLYING: prove non-membership once at a height at or after the end block.
- Chain record: prove at review and on profile open, with a 10-minute cache.
- DC share nullifiers: prove at FINALIZED, or when tracking reports everything confirmed.
- Each verification batch adds the same key kinds for 3 random other delegates as decoys, so the RPC sees less about which delegate you care about. Disclose this in `docs/voting-delegates.md`; it is not PIR, which matches the existing participation caveat.

The UI shows "Verified" only on a proof success. A failure shows nothing extra; the REST data still displays, labelled "Unverified" in key details.

### 7.10 Deep links

**Wallet.**
- `vizor:lib/src/core/navigation/vizor_deep_link.dart:1-27` [code]: add `VizorDeepLinkRoute.delegateProfile`.
  - Match `^/d/(0|[1-9][0-9]{0,9})$` with value at most 4294967295 and `!uri.hasQuery`.
  - The fragment may carry `k=<16 bech32 chars>`. Any other fragment is ignored, not an error.
  - Add `static Uri delegateLink(int index, {String? keyHint})`.
- `incoming_link_dispatch.dart:56-88`: add `IncomingDelegateProfileLink(int delegateIndex, String? keyHint)`. The host-first ordering stays load-bearing (`:9-16`).
- `app.dart:1884-1904` switch:
  - if `requiresUnlock`: park in `pendingDelegateLinkProvider` and drain after unlock;
  - if `isOnboardingLocation`: drop;
  - if the gate is off: toast "Delegates aren't available in this version of Vizor.";
  - else `router.push(delegateProfileRoute(...))`.
  - An unknown index refreshes the registry, then shows "We couldn't find this delegate."
  - A key-hint mismatch shows a profile banner: "This link was made for a different key. The delegate may have changed keys. Check the fingerprint."
- [inference] Check `android/app/src/main/AndroidManifest.xml` intent-filter path patterns, which are not in this snapshot. The AASA already claims `"/": "*"` (`dls:src/associations.ts:3-20` [code]).
- **Desktop.** HTTPS links are not handled ("the desktop runners never register a handler", `incoming_link_dispatch.dart:4-7` [code]). Users paste the link into directory search (§3.2), and profiles offer "Copy link".

**Server** (`dls:`):
- `src/routes.ts:1-19`: add `DeeplinkPageKind "delegate-profile"`. `resolveDeeplinkRoute` keeps its exact `switch` plus **one** explicit regex branch with the u32 bound. This is not a wildcard (`README.md:88-90`).
- `src/page.ts`:
  - eyebrow "Delegate";
  - heading "Open this delegate in Vizor";
  - description "Vizor shows this delegate's verified profile, key fingerprint and public voting record.";
  - buttons "Get Vizor" and "Copy link". Copy uses a hash-pinned inline script; the CSP already allows `script-src` hashes (`src/handler.ts:142-143` [code]).
  - desktop line "On a computer, open Vizor, go to Vote, then Delegates, and paste this link."
- Use a generic static `og-delegate.png`, with no per-delegate data. The gateway has no database and no remote assets (`README.md:9-12`), and a spoofable per-delegate card would be a phishing aid.
- Update the README route table and tests. Needs the privacy review `README.md:171-173` requires.

### 7.11 Conventions, docs, tests

- **Copy:** sentence case (`vizor:AGENTS.md:266-287`). New widgetbook file `lib/widgetbook/voting_delegates_use_cases.dart`.
- **figma-compare scenarios** in `lib/figma_compare/figma_compare_scenarios.dart`, each desktop + `mobile-` variant:
  - `voting-delegates-featured`, `voting-delegates-search-empty`;
  - `voting-delegate-profile`, `voting-delegate-profile-lookalike`, `voting-delegate-profile-new-this-round`;
  - `voting-proxy-allocation`, `voting-proxy-allocation-invalid`;
  - `voting-proxy-review`, `voting-proxy-review-combined`;
  - `voting-proxy-progress-keystone`;
  - `voting-proxy-status-active`, `voting-proxy-status-finalized-partial`;
  - `voting-poll-card-delegated`;
  - `delegate-onboarding-post`, `delegate-onboarding-verifying`;
  - `delegate-dashboard`, `delegate-round-public-vote`, `delegate-public-vote-confirm`.
  - Run with `scripts/figma-compare.sh widget`.
- **Feature doc:** `docs/voting-delegates.md`, following `docs/voting-participation.md`.
- **Tests:**
  - rounding property tests on the SDK side, plus Dart format tests;
  - lookalike skeleton tests;
  - deep link classifier tests (host-first; rejects `/d/01`, `/d/4294967296`, query strings);
  - snapshot verify tests (bad signature, regression, wrong network);
  - pfp validation;
  - job state machine with a fake session (cancel before and after broadcast, Keystone, Ledger, rejected, expired);
  - widget tests in both form-factor lanes (`--dart-define=VIZOR_FORM_FACTOR=mobile`);
  - Rust network-construction guard.

---

## 8. Recovery

| Situation | Delegator: shown | Delegator: lost | Delegate |
|---|---|---|---|
| App restart, lock, crash mid-job | everything. The job resumes from the SDK plan; NOT_SENT allocations offer Resume/Discard | nothing | onboarding resumes from persisted state; routes are on chain |
| Reinstall or seed restore, before the job broadcast | Delegation is available again: governance nullifiers are unspent and ZKP1 never landed | the draft | n/a |
| Reinstall or seed restore, after broadcast. Per-round hotkeys are random and unrecoverable (`vizor:docs/voting-participation.md:146` [doc]; `rust/src/wallet/voting/README.md:42-50`) | participation reads "Already used for this round" (`voting_polls_screen.dart:503-507`), with the explanation "This wallet already took part in this round on another install. Vizor can't show details here." Public delegate profiles and routes stay browsable. | which delegates and amounts; DC share tracking and the "counted" check; voting the kept weight (the successor VAN needs the lost hotkey); "delegate more". **The delegation still counts**, because shares were handed to helpers at submission. | the DK is re-derived from the seed. Vizor scans `identity_index` 0..9, looks each up via `GET /delegates?dk_pubkey=` (contract C2), and restores `delegateIndex`, status and dashboard. Public votes are on chain. Pending onboarding restarts at "Paste link"; the post is still valid. |
| Hardware delegator | same as above. The device is only needed for ZKP1 | same | n/a (software-only v1) |
| Lost seed | n/a | n/a | the DK is lost. Rotation needs the registry's cooling-off path (contract I6), which delegators see as "Key changed". |

Pre-delegation disclosure (PD-5 "Learn more" and PD-7 footer): "Keep Vizor installed until voting ends. If you reinstall or restore this wallet, your delegation still counts, but Vizor can't show it."

---

## 9. Copy deck (sentence case, no em dashes)

| Key | String |
|---|---|
| C1 finality | "Delegation is final for this round. Once you confirm, you can't change it, take it back, or vote with this ZEC yourself, even if your delegate doesn't vote." |
| C2 abstain | "If your delegate doesn't vote on a proposal, the ZEC you gave them isn't counted for any option on that proposal. It's the same as not voting." |
| C3 public | "Delegates vote in public. Anyone can see how @alice voted on each proposal, so you can check how your voting power was used." |
| C4 privacy | "Your delegate can't see who you are or how much you gave them. Vizor never publishes per-delegate totals, only the final results. Your delegation gets the same protection as a private vote." |
| C4b count | "The number of delegations someone receives can be estimated from public data. Amounts and identities can't." |
| C5 order | "Delegate before you vote. Once you've voted on any proposal in this round, you can't delegate, so the same ZEC is never counted twice." |
| C6 hardware | "Your Keystone only authorizes Vizor to vote for you in this round. It shows your full voting amount. Your delegate choices are made in Vizor and aren't shown on the device." (Ledger variant: "Your Ledger ...") |
| C7 shares | "Helpers add your delegation to the count at random times before voting ends, so it can't be linked to you." |
| C8 reinstall | "Keep Vizor installed until voting ends. If you reinstall or restore this wallet, your delegation still counts, but Vizor can't show it." |
| C9 card | "Don't want to vote on every proposal? Delegate some or all of your voting power to people you trust." |
| C10 featured | "Featured by Vizor. Featured doesn't mean Vizor agrees with their votes." |
| C11 lookalike | "Looks like @zcash (Featured). Check the key fingerprint before you delegate." |
| C12 new | "Registered after this round started. Make sure this is who you expect." |
| C13 key changed | "This delegate's key changed on {date}. Check the fingerprint." |
| C14 delegate intro | "As a delegate, people can give you their voting power. Your votes as a delegate are public and linked to your X account. You won't see who delegated to you or how much." |
| C15 public vote | "This vote is public and final. Everyone can see that @alice voted {option}. It applies to all voting power delegated to you for this proposal. You can't change it." |
| C16 deadline | "Public votes close {date}. Proposals you don't vote on count as abstain for everyone who delegated to you." |
| C17 private col | "Only you can see this." |
| C18 checkbox | "I understand this delegation is final and can't be changed this round." |
| CTA | "Delegate voting power" · "Choose delegates" · "Delegate {amount} ZEC" · "Delegate and submit votes" · "Vote {option} publicly" · "View status" · "Delegate more" |

Error copy is in §3.10 and §4.6. The ZKP1 rename is in §1.

---

## 10. App-store UGC and moderation

Apple Guideline 1.2 and Google Play's UGC policy need four things: filtering, reporting, blocking, and published contact details.

- **Filtering** (server-side, contract I2/I4):
  - Pictures are re-encoded and shown only when `pfp_status == approved`. Pending or removed pictures show the identicon.
  - Display name and bio show only when `text_status == approved`; otherwise "Profile text under review" and the handle only.
  - Bio is at most 160 characters, plain text, with no links.
  - Delisted entries (`listed == false`) are not searchable. Their profile reads "This delegate is no longer listed. You can't delegate to them. Delegations already made still count."
  - Pictures of Featured delegates are reviewed before they change.
- **Report:** a sheet on every profile.
  - Reasons: "Pretending to be someone else", "Offensive photo", "Offensive name or bio", "Scam or spam", "Something else".
  - Optional details, at most 500 characters.
  - "Also hide this delegate", checked by default.
  - Sent as `POST https://functions.vizor.cash/v1/delegates/report` with `{schema:1, network, delegate_index, dk_fingerprint, reason, details?}`. No account id, no address. Goes through `NetworkHttpClient`; POST is allowed under Tor.
  - Local limits: 5 per hour and 1 per delegate per day.
  - Confirmation: "Thanks. We'll review this delegate."
  - The footer shows the support contact.
- **Block:** "Hide delegate" is local and device-wide. It removes the delegate from lists and search; the profile shows "You hid this delegate" with Unhide. Manage hidden delegates in PD-11. Delegations already made stay visible, with a "Hidden" chip; the user's own delegation is never hidden.
- **Curation:** Featured is editorial. Publish the criteria (C10).

---

## 11. Accessibility

- **Avatars:** `Semantics(label: 'Profile photo of @alice')`. Identicons are decorative, with the fingerprint text as the label. Fingerprints are spelled out in groups.
- **Status:** every chip and status has an icon and text, never colour alone, with contrast of 4.5:1 or better. Abstain, pending and voted have distinct icons.
- **Allocation editor:**
  - Each row is a labelled text field plus stepper buttons, with semantic increase and decrease actions (±1% or ±0.125 ZEC).
  - "Keep for myself" is a `liveRegion`, so screen readers hear the remainder change.
  - Validation errors are announced, and focus moves to the first invalid row.
- **Progress:** step changes are announced through a `liveRegion`. No timed auto-dismiss. Respect `MediaQuery.disableAnimations` by turning off shimmer and animated bars.
- **Text size:** dynamic type up to 200% on mobile without truncating amounts. Handles ellipsize visually, but the full handle is in semantics.
- **Desktop:** full keyboard navigation (logical Tab order, Enter activates, Esc closes sheets), visible focus rings, and search focused on open.
- **Targets:** at least 44×44 pt.
- **Text safety:** bidi controls and zero-width characters are stripped from display names. Handles render as an LTR-isolated span.
- **Confirm sheets:** they read the full consequence before the button. The button label includes the choice ("Vote Yes publicly").

---

## 12. Contracts assumed from other components

The same list appears in `cross_component_contracts`. Short ids:
- C = Chain
- S = client SDK (`zcash_voting`)
- Z = Circuits
- I = identity/registry service
- H = Helpers
- F = Config
- L = deeplink server
- B = Vizor backend

---

## 13. Interaction with the private-vote redesign

None of the delegator UX reads vote decisions from share reveals:
- status uses the route list and DC share nullifiers;
- results use tally-results.

The design therefore holds whether `DOMAIN_VC_V2` and vector ZKP3 (`voting-circuits docs/design.md`) land before or after. The only requirement is contract Z3: DC reveals keep a path with a public `delegate_index`. The own-vote "Your vote" display comes from the local plan and does not change.
