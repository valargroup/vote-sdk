# Vizor wallet + vizor-deeplink-server: deep read for proxy delegation

Path prefixes used below:
- `vizor:` = `/private/tmp/claude-501/-Users-czstudio-Documents-vote-sdk--claude-worktrees-vote-delegation-planning-29b451/0cff7cc1-5f3f-4de1-be83-322a1fdf7727/scratchpad/src/vizor-wallet`
- `zv:` = `.../scratchpad/src/zcash_voting`
- `dls:` = `/Users/czstudio/Documents/vizor-deeplink-server` (local checkout at `b325f07`, 2026-09-16, branch `main`)
- `chain:` = `/Users/czstudio/Documents/vote-sdk/.claude/worktrees/vote-delegation-planning-29b451`

Terminology: "delegation" / ZKP1 / MsgDelegateVote means moving note weight into a VAN bound to a voting hotkey. The new feature is called **proxy delegation** here.

Labels: **[code]** = what the code does today. **[doc]** = what a doc says. **[inference]** = my own reasoning.

---

## 1. The voting feature end to end

### 1.1 Routes and entry points
- **[code]** Route builders live in `vizor:lib/src/features/voting/voting_routes.dart:1-24`. The routes are `/voting/poll/:roundId`, `/review`, `/status?account=`, `/submitted?account=` and `/results`.
- **[code]** Desktop GoRoutes are at `vizor:lib/app.dart:1496-1543`. They add `/voting` and `/voting/keystone/scan`. Each one is wrapped in `_guardVotingScreen` → `VotingSoftwareAccountGuard` (`app.dart:1545-1547`). Despite its name, that guard only waits for `accountProvider` to load (`voting_software_account_guard.dart:16-28`). Hardware accounts are not blocked.
- **[code]** Mobile routes are at `vizor:lib/src/core/navigation/mobile_routes.dart:417-480`. They use `CupertinoPage` and `MobileVotingAccountGuard`. Back-label strings are in `core/navigation/app_back_resolver.dart:61-67,148-161`.
- **[code]** There are three entry points:
  - Desktop sidebar "Vote" (`core/layout/app_main_sidebar.dart:562-574`). Re-tapping it requests a poll-list refresh (`:149-151`).
  - Mobile Home card "Coinholder voting" / "Help to shape the network" (`features/home/screens/mobile/mobile_home_screen.dart:1501-1513`). It is shown only when `votingHomeEntryVisibleProvider` is true.
  - Mobile Settings "Coinholder voting" (`features/settings/screens/mobile/mobile_settings_screen.dart:101-106`).
- **[code]** The UI copy is inconsistent: "Token holder voting" (`voting_proposal_detail_screen.dart:243-312`, `voting_config_settings_panel.dart:471`) versus "Coinholder voting" on mobile. The polls header carries a "Beta" label (`voting_polls_screen.dart:381-397`).

### 1.2 Screens
- **Poll list**: `features/voting/screens/voting_polls_screen.dart`.
  - Card states and labels are at `:852-913`: In progress/Resume, Active/Vote, Voted/Review, Tallying, Closed/View results.
  - Ineligible rows read "Already used for this round" or "Not eligible for this round" (`:504-507`).
- **Round detail**: `voting_proposal_detail_screen.dart` (2087 lines).
  - Shared `VotingActivePollContent` (`:551`).
  - The CTA cycles through Unavailable, Retry eligibility, Not eligible and Review answers (`:741-746`).
  - Privacy-trim notice (`:536-548`): "… is left out of this vote to keep your submission less identifiable".
  - Voted header and "Vote locked" (`:840-900`, `:1787-1893`). Pending recovery copy (`:2009-2039`).
- **Review**: `voting_review_screen.dart`, with "Review your answers" and "Confirm & submit" (`:247,287`).
- **Status / progress**: `voting_status_screen.dart` (1950 lines).
  - Step rows, Keystone panel (`:1443-1800`), memo viewer (`:1695`), Ledger panel (`:1196`).
  - Desktop copy "Don't close the window. Generating zero-knowledge proofs can take a while…" (`:966-967`).
  - Mobile equivalent is `screens/mobile/mobile_voting_submission_progress_screen.dart:84`: "Don't leave this window."
- **Submitted**: `voting_submission_confirmation_screen.dart`. On mobile it is `mobile_voting_submitted_screen.dart`.
- **Results**: `voting_results_screen.dart`.
  - Polls `getRoundTally` every 10 s while tallying (`:31-41,168-178`).
  - Shows per-option tallies, "Winner" and "Your vote" (`:734`). "Your vote" comes from the local round plan's `completedVoteDisplay`.
- **Keystone scan**: `keystone_voting_scan_screen.dart`. On mobile it is `mobile/mobile_keystone_voting_signing_screen.dart`.
- **Config sheet**: `widgets/voting_config_settings_panel.dart`. It covers custom static-config sources and the "Show test rounds" toggle (`:634-691`).

### 1.3 Providers and state machine
- **[code]** `VotingSessionPhase` (`providers/voting/voting_state.dart:73-89`) runs: idle → waitingForWalletSync → resolvingPir → loadingWitnesses → readyToDelegate → keystoneSigning / ledgerSigning → delegating → delegated → readyToVote → syncingVoteTree → castingVotes → submittingShares → done / error.
- **[code]** `VotingSessionState` (`:241-347`):
  - pins `accountUuid` (`:244-248`);
  - holds `roundPlan: RoundPlanView`, `eligibleWeightZatoshi` and `privacyTrimDroppedValueZatoshi`;
  - holds Keystone and Ledger signing requests and `terminalDelegationNotice`.
- **[code]** `VotingSessionNotifier` (`providers/voting/voting_session_provider.dart`, 4717 lines):
  - It is recovery-first, and every action is serialized through `_enqueue` (`:74-80`, `:3352`).
  - Concurrency caps are 3 (`:43-44`).
  - Delegation entry points are `delegatePendingBundles({mnemonic})`, `…WithKeystoneSignatures()` and `…WithLedgerSignatures()` (`:612-632`). They share `_delegatePendingBundles` (`:648-886`).
  - Casting is `castVotes` (`:1274`).
  - Session binding (`:1798-1838`) uses the same configured API servers as chain, helper and vote-tree endpoints.
- **[code]** `VotingSubmissionJobNotifier` (`providers/voting/voting_submission_job_provider.dart`, 1764 lines) is the "Submit" orchestrator.
  - Statuses: `idle, running, waitingForKeystone, waitingForLedger, complete, error` (`:28-35`).
  - `_run` (`:521-840`) does: draft load → wallet sync readiness → eligibility → `recordBallotIntents` → hardware or software branch → delegate → `_submitVotesAndShares`.
  - Load-bearing comment (`:669-676`): *"the SDK plans a bundle's delegation only while that bundle still has a vote to cast, so a round whose intents are not yet durable reports no delegation work."* **Delegation is currently driven by ballot intents.**
- **[code]** The resume plan is read from the SDK: `features/voting/voting_resume_plan.dart`.
  - `isVoteNextStepKind` is an exhaustive switch over `NextStepKind` (`:60-71`): delegate, advanceDelegation, **advanceImportedDelegation**, castVote, advanceVote, advanceVoteBatch, submitShares, confirmShare.
  - Adding a new step kind breaks the build there on purpose.
  - `RoundPlanView` (`lib/src/rust/third_party/zcash_voting/wire.dart:598-660`) carries `hotkeyBound`, `needsBundleSetup`, `delegationBundlesNeedingSigning`, etc.
  - Its doc says *"The exclusion is an imported delegation, which is already broadcast and never asks the voter for a signer"* (`wire.dart:~641`).
- **[code]** The recovery service is a thin wrapper (`voting_recovery_service.dart:10-33`, `voting_recovery_api.dart`). Dart keeps no mirror of durable rows (`rust/src/wallet/voting/README.md:161-166`).
- **[code]** Submission guard (`providers/voting/voting_submission_guard_provider.dart:44-118`): an in-memory token registry that blocks account delete/reset while a submission runs.
- **[code]** Destructive-operation drain and share-tracking registry (`voting_share_tracking_registry_provider.dart:10-80`, `beginBackgroundWork`). AGENTS.md requires all voting background work to register before its first await (`AGENTS.md:342-345`).
- **[code]** The share-tracking restorer re-arms on launch, unlock and resume (`voting_share_tracking_restorer_provider.dart:211-216`; README `:266-283`).

### 1.4 Rust bridge
- **[code]** `rust/src/api/voting_session.rs`: opaque `VotingRoundSession` over `zcash_voting::RoundExecutor` (`:223-299`).
  - Methods: `plan`, `set_ballot_intents`, `run_round`, `keystone_signing_requests`, `run_share_tracking`, `confirm_immediate_share`.
  - Signer kinds are only `Mnemonic | KeystoneStored | KeystoneProvided` (`:82-89`).
  - Ledger signatures are stored and then run as `KeystoneStored`; see `voting_session_provider.dart:770-790`.
- **[code]** `rust/src/api/voting.rs`: stage-level functions.
  - `generate_voting_hotkey` (`:555`), `setup_delegation_bundles` (`:637`), `check_voting_eligibility` (`:656`).
  - `precompute_*`, Keystone request and store functions (`:804-895`).
  - `get_round_plan` (`:1084`), `delete_voting_account_state` (`:1029-1045`), config resolution (`:1150-1220`).
- **[code]** `rust/src/wallet/voting/delegation.rs:86-139` `open_pipeline(inputs, hotkey: Option<VotingHotkey>)` binds the SDK `DelegationPipeline` to the **wallet's own hotkey**. Ledger accounts get `with_ledger_output_review` (`:131-138`).
- **[code]** FRB scans `zcash_voting::wire` directly (`rust_input: crate::api,zcash_voting::wire`, README `:293-303`). The API surface must stay flat structs (`AGENTS.md:897-901`). Regenerate with `scripts/generate-rust-bridge.sh` (`AGENTS.md:24-26`).
- **[code]** Pinned SDK: `rust/Cargo.toml:129-130` `zcash_voting = "=5.1.1-rc.3"`.
- **[code]** Vizor exposes **none** of the SDK's capability-handoff API: no `export/import_delegation_capability`, `prepare_delegation_bundle_for_target` or `VotingHotkeyTargetV1` in its bridge. Only the `advanceImportedDelegation` enum value reaches Dart. A Rust test does exercise an imported submitted delegation (`voting_session.rs:967-990`).

### 1.5 Keystone, Ledger and software accounts
- **[code]** Software accounts:
  - Delegation needs the mnemonic: `getSoftwareWalletSecretForAccount` plus a Linux secret guard (`voting_submission_job_provider.dart:767-795`).
  - The seed stays inside `rust/src/wallet/voting/signer.rs`, which returns only the detached SpendAuth signature.
- **[code]** Keystone accounts: `zcash-sign-batch` QR of redacted PCZTs.
  - Up to 40 messages per QR round with a 200-character fragment length (`voting_submission_job_provider.dart:1711-1712`).
  - The phone shows each bundle's `displayMemo` next to the QR (`voting_status_screen.dart:1695-1800`).
  - The SDK builds the memo text as *"I am authorizing this hotkey managed by my wallet to vote on {round}.\nAmount: X ZEC."* (`zv:zcash_voting/src/delegate.rs:2426-2449`).
  - The memo goes into the delegation PCZT output to the hotkey address (`zv:zcash_voting/src/action.rs:565-578`).
  - Skip-remaining-bundles exists (`voting_state.dart:375-385`, `delete_skipped_bundles`).
- **[code]** Ledger accounts:
  - Signing goes through `ledgerVotingPcztSignerProvider` (`voting_submission_job_provider.dart:872-935`).
  - The device reviews an ASCII memo (`zv:delegate.rs:2452-2464`).
- **[doc]** Vote cancellation must preserve saved partial signatures (`AGENTS.md:727`).
- **[doc]** Hardware QR codes must be black-on-white with a quiet zone (`AGENTS.md:253-264`).

### 1.6 Hotkey storage
- **[code]** Key: `zcash_account_voting_hotkey_{accountUuid}_{roundId}` (`core/storage/app_secure_store.dart:59,729-738`).
  - Stored with `writeSecretString`, which is app-encrypted under the session password.
  - Reads require an unlocked session (`:358-375,418-428`).
  - Bulk delete happens on account removal (`:441-457`; `providers/account_provider.dart:1765-1769`).
- **[code]** `VotingHotkeyStore.getOrCreate(allowCreation:)` never replaces a key once the round plan says `hotkeyBound` (`core/storage/voting_hotkey_store.dart:50-110`; `voting_session_provider.dart:2213-2241`).
- **[doc]** One random hotkey per account per round, with no seed-derived recovery (`rust/src/wallet/voting/README.md:42-50`). *"Lost voting hotkey secrets cannot be recreated from a wallet seed or Keystone UFVK"* (`docs/voting-participation.md:146`).
- **[code]** The hotkey is always phone-held, even for Keystone and Ledger accounts. Vote signing never touches the hardware device.
- **[code]** Wallet-link does not appear to transfer hotkeys: there is no hotkey reference in `features/wallet_link/*`.
- **[code]** Ballot drafts are stored in plain secure storage under `zcash_voting_draft_votes_{account}|{round}` (`features/voting/voting_flow_models.dart:272-330`).

### 1.7 Config loading, helpers and participation
- **[code]** Static config is hash-pinned with mirrors: `voting.valargroup.dev/pins/...` and `raw.githubusercontent.com/valargroup/token-holder-voting-config/...` (`services/voting/voting_config_loader.dart:14-90`). Dynamic config is signature-verified in Rust.
- **[code]** `ResolvedVotingConfig.supportedVersions.voteProtocol` (`rust/third_party/zcash_voting/config.dart:212-224`). The SDK accepts `vote_protocol ∈ {v0, v1}` (`zv:zcash_voting/src/config/mod.rs:82,102`).
- **[code]** Helper health is a per-session `HelperHealth` in Rust (`voting_session.rs:232,262`; `wallet/voting/network_clients.rs:22-33`). The SDK owns delivery and confirmation; Dart does not see helper observations (README `:203-253`).
- **[code]** Participation reader (`rust/src/wallet/voting/participation.rs`; doc `docs/voting-participation.md`):
  - It is a Tendermint commit and IAVL proof verifier anchored to bundled validator sets (`participation.rs:18-23`).
  - It queries vote-store keys `01 00 || round_id || gov_nullifier` (`participation.rs:94-101`), i.e. governance nullifiers only.
  - The chain's nullifier store also holds type `0x01` VAN nullifiers and type `0x02` share nullifiers (`chain:x/vote/types/keys.go:100-114`).
  - Its outcome is "Already used for this round" when notes were used elsewhere. The doc stresses this *"does not prove every proposal was voted on"* (`voting-participation.md:141-146`).
  - The SDK-side note filter is not enforced on this branch (`:152-174`).
- **[code]** Home discovery probes `functions.vizor.cash/v1/voting/discovery/{prod,stage}` (`docs/voting-home-discovery.md:17-44`).

---

## 2. Where proxy delegation would plug in

### 2.1 Delegator path ("Delegate my vote")
**Entry CTA:**
- **[inference]** Add it next to "Review answers" in `VotingActivePollContent` (`voting_proposal_detail_screen.dart:551-800`, CTA logic `:741-746`) and in the mobile `_MobilePollSummary` (`:1191`).
- **[inference]** The poll card needs a new "Delegated" state (`voting_polls_screen.dart:852-913`).
- **[inference]** "Already used for this round" (`:506`) must become "Delegated to …" when the local record exists. Otherwise a proxy-delegated wallet reads as "already used".

**New routes:**
- **[inference]** e.g. `/voting/poll/:roundId/delegate`, `/delegate/review` and `/delegate/status`, added in `voting_routes.dart`, `app.dart:1496-1543`, `mobile_routes.dart:417-480` and `app_back_resolver.dart:61-67`.

**New screens (desktop + `screens/mobile/` twins):**
- delegate search/picker with directory list, avatar and handle;
- allocation editor for up to N delegates plus a "keep for myself" share;
- review;
- signing/status.
- These can reuse `VotingStatusContent`, `_KeystoneSigningPanel` and `LedgerVotingSigningPanel` (`voting_status_screen.dart`).

**New provider/job:**
- **[inference]** A `VotingProxyDelegationJob` modeled on `VotingSubmissionJobNotifier`, with the same software / Keystone / Ledger branches and generation guards.
- The existing `_run` cannot be reused unchanged:
  - it requires a non-empty draft or recovery (`:657-665`, "Choose at least one vote before submitting.");
  - it records ballot intents before delegation (`:669-708`).

**Rust:**
- **[inference]** `open_pipeline` needs a target-bound variant: a `VotingHotkeyTargetV1` / `RoundBoundVotingHotkeyTarget` per delegate instead of `Option<VotingHotkey>` (`delegation.rs:86-139`).
- **[inference]** Session and API need new flat DTOs for the target and allocation.
- **[inference]** If the SDK adds a step kind such as `ProxyDelegate`, it must be handled in `voting_resume_plan.dart:60-71`.
- **[inference]** The SDK's `RoundPlanView` must expose outbound proxy delegations so Dart does not mirror rows (README `:161-166` forbids Dart-side mirrors).

**Keystone and Ledger:**
- **[inference]** The device-visible memo must change, e.g. *"I am delegating X ZEC of voting power on {round} to {handle}/{address}"* (SDK `delegate.rs:2426`, Ledger escape path `:2452`).
- **[inference]** The PCZT output recipient becomes the delegate's address, which the hardware device can display.

**Durable outbound record:**
- **[inference]** Persist which delegate (handle, target, weight, tx hash) per account and round in the SDK sidecar. This is needed for status, recovery and the "Delegated" label.
- **[inference]** Add it to account deletion cleanup (`account_provider.dart:1745-1790`, Rust `delete_voting_account_state` `voting.rs:1029-1045`).

**Privacy mode:**
- **[code]** The voting UI does not read `privacyModeProvider` today. **[inference]** Decide whether delegated amounts are masked.

### 2.2 Status tracking ("did my delegate vote?")
- **[code]** Today Vizor can verify only governance-nullifier membership for its own notes (`participation.rs:94-101`).
- **[code]** VAN nullifiers live at `01 01 || round || van_nf` (`chain:x/vote/types/keys.go:100-114`).
- **[inference]** The verifier could prove membership of a type-0x01 key with the same code path, but only if the delegator can compute the delegate's VAN nullifier. That requires the delegate's `nk`, so it is impossible today.
- **[inference]** Any "did they vote" answer needs either a protocol change (a delegator-computable tag on spend) or a delegate-supplied attestation.
- **[inference]** Status polling must follow the participation pattern: foreground only, backoff, never logging keys or URLs (`voting-participation.md:14-20,94-101`).
- **[code]** There are no push notifications (no FCM/APNs plugin in `pubspec.yaml`), so "your delegate voted" alerts can only appear on in-app refresh.

### 2.3 Delegate-side dashboard
**Setup and key:**
- **[inference]** "Become a delegate" needs a new section, e.g. in the voting header's settings area (`voting_polls_screen.dart:300-380`) or Settings.
- **[inference]** It needs a long-lived delegate key with a backup/export story. Today's per-round random hotkey cannot be pre-published, and losing it loses the round's delegated weight.
- **[code]** The SDK's public target `VotingHotkeyTargetV1` **binds `vote_round_id`** and fixed `address_index = 0` (`zv:zcash_voting/src/wire.rs:158-171`; `docs/exporting-to-external-software.md:38-60`). With the current SDK, a delegate must mint and publish a fresh target for every round after the round exists.

**Inbox:**
- **[inference]** Delegated VAN openings (num_ballots, van_comm_rand, tx hash) must reach the delegate somehow.
- **[code]** The SDK v1 capability package is privacy-sensitive and must use a confidential, authenticated channel (`exporting-to-external-software.md:74-95`).
- **[code]** Vizor already has an encrypted relay pattern in wallet-link: AES-256-GCM envelopes, package create/status/revoke, and build-time backend URL `VIZOR_WALLET_LINK_BACKEND_URL` defaulting to `http://localhost:3000` (`features/wallet_link/wallet_link_config.dart:1-6`, `services/wallet_link_api_client.dart`, `services/wallet_link_completion_crypto.dart:9-30`).

**Import conflicts:**
- **[code]** `import_delegation_capability` allows **one complete package per (round, wallet_id)**. It rejects locally constructed or conflicting bundle state (`zv:zcash_voting/src/delegation_capability.rs:380-457`).
- **[code]** Vizor's `wallet_id` is the account (`db::open_voting_db(db_path, account_uuid)`, `voting_session.rs:259-260`).
- **[inference]** As shipped, a Vizor account cannot import a second delegator's package, or import any package while also voting its own notes in the same round. The N:1 influencer case needs SDK schema work, such as a per-delegation scope.

**Voting with delegated weight:**
- **[inference]** Reuse `castVotes`, but bypass own-eligibility gating. Today eligibility failure turns into "Not eligible" UI (`voting_state.dart:147-150`; `voting_submission_job_provider.dart:590-640`). An influencer with zero shielded ZEC must still be able to vote received weight.

**Totals:**
- **[inference]** Total delegated weight is the sum of imported `num_ballots × BALLOT_DIVISOR` (0.125 ZEC, `zv:zcash_voting/src/governance.rs:15`).

**Scale:**
- **[code]** Vote work is round-serialized. One 37-proposal round with 3 bundles took about 58 s of drive plus about 523 s of helper delivery work. The per-ZKP2 p50 is 0.117 to 0.214 s (`zv:stage-bench/README.md:198-227`).
- **[inference]** 1,000 delegated VANs × 37 proposals is about 37k ZKP2s plus about 590k helper shares on one phone in the foreground. That is not feasible without VAN consolidation or merge in the protocol.

### 2.4 Bundles cannot carry percentage splits
- **[code]** Bundle note slots are 5 (`governance.rs:22`). The privacy trim aims for at most 2 bundles per holder and drops up to 1% (cap 1,000 ZEC) of weight to get there (`zv:zcash_voting/src/note_bundling.rs:23-52`). Vizor surfaces this trim in UI (`voting_proposal_detail_screen.dart:536-548`).
- **[inference]** Splitting across up to 10 delegates by assigning bundles would:
  - force at least 10 delegation submissions, an outlier fingerprint;
  - be impossible for wallets with few notes;
  - only allow note-granular splits.
- **[inference]** Percentage splits need a VAN-split circuit, as the book suggests (`shielded-vote-book/delegation/partial-delegation.md`). The book's successive splits each consume the current VAN, so N delegates means N sequential chain transactions.
- **[code]** The SDK can also submit delegation atomically with the vote batch. `MsgDelegateAndCastVoteBatch` uses a synthetic single-leaf anchor (`chain:proto/svote/v1/tx.proto:105-113`), and the SDK uses it (`zv:zcash_voting/src/delegate_and_vote_batch/`).
- **[inference]** A proxy delegation has no vote batch to pair with, so it is a standalone `MsgDelegateVote` path that the planner currently never schedules without intents.

---

## 3. Platform, background limits, timing and notifications
- **[code/doc]** Vizor ships on five platforms: iOS, Android, macOS, Windows and Linux.
  - `incoming_link_dispatch.dart:4-5`: ZIP-321 "all five platforms".
  - Windows and Linux updater providers (`providers/windows_update_provider.dart`, `linux_update_provider.dart`). Desktop release notes cover Windows, Linux and macOS (`AGENTS.md:291-295`).
  - App Store id6776591245 and Play `com.keplr.vizor` (`dls:src/config.ts:15-28`). F-Droid/direct APK via `VIZOR_DEGOOGLED` (`AGENTS.md:125-133`).
  - The public README still says releases "focus on signed and notarized macOS DMGs" (`README.md:9-10`). `AGENTS.md:303` says "Supports iOS, Android, and macOS", which is stale.
- **[code]** UI tokens are chosen at build time: `VIZOR_FORM_FACTOR=desktop|mobile`, read via `kAppFormFactor` (`core/layout/app_form_factor.dart`; rules `AGENTS.md:38-123`).
- **[code]** Background work:
  - Mobile Dart work is foreground-only; desktop continues while windows are hidden (`core/layout/app_process_work_policy.dart:4-17`).
  - Mobile auto-locks after 15 minutes in the background (`core/security/background_auto_lock_host.dart:14`; `AGENTS.md:748-750`).
  - Hotkey reads need an unlocked session.
  - There is no OS background task for voting. The only iOS `BGContinuedProcessingTask` and local notification belong to Ironwood migration (`AGENTS.md:563-622,664-709`).
  - Voting screens tell users to keep the app open (`voting_status_screen.dart:966-967`; mobile progress screen `:84`).
- **[code]** Proof timing: Vizor proves 3 at a time (`voting_session_provider.dart:43-44`). ZKP2 p50 is about 0.12 s. There is no ZKP1 timing figure in Vizor or the bench (`zv:docs/bundle_pipeline_benchmark.md:8-9`).
- **[code]** Notifications: no push SDK, and no Dart local-notification plugin in `pubspec.yaml`.

---

## 4. Identity and social features today
- **Profiles and avatars [code]:** 15 bundled PNG avatars (`core/profile_pictures.dart:1-71`), rendered with `Image.asset` (`core/widgets/app_profile_picture.dart:41-46`). Account name and `profilePictureId` live on `AccountInfo` (`providers/account_models.dart:31-60`).
- **No remote images [code]:** Vizor loads no remote images anywhere. A search for `Image.network`, `NetworkImage` and `CachedNetworkImage` in `lib/src` finds nothing. Swap and chain icons are bundled assets (`pubspec.yaml` assets).
- **No image proxy [code]:** none exists.
- **[inference]** `Image.network` would use `dart:io` `HttpClient` directly and bypass the Tor policy. Avatars must be fetched as bytes through `NetworkHttpClient` (GET/POST only under Tor, `core/network/network_http_client.dart:25-34,420-422`) and drawn with `Image.memory`. Better still, use a Vizor-hosted proxy or a directory that serves avatars from its own origin, so the client never contacts `pbs.twimg.com`.
- **Address book [code]:** multi-chain contacts with label, network, address and a bundled avatar id (`features/address_book/models/address_book_contact.dart`). Stored as JSON in secure storage under `zcash_address_book_contacts_v1` (`providers/address_book_provider.dart:12-57`). There is no name resolution.
- **No social integration [code]:** no X/Twitter integration or name service of any kind (no matches for twitter, x.com, tweet or twimg in `lib`, `rust` or `docs`).
- **External links [code]:** always open in the external browser (`launchUrl(..., LaunchMode.externalApplication)`, e.g. `widgets/voting_metadata_widgets.dart:67`). An IP-linkage disclosure pattern exists for explorers (`core/config/zcash_explorer.dart:11-14`).
- **Wallet deep links [code]:**
  - A single trusted origin `VIZOR_DEEPLINK_BASE_URL`, defaulting to `https://link.vizor.cash` (`core/navigation/vizor_deep_link.dart:1-60`).
  - The exact route allowlist is just `/` and `/payment-links/open` (`:18-27`).
  - The classifier is `classifyIncomingLink` (`core/navigation/incoming_link_dispatch.dart:56-88`). Host checks run first because Gift Card fragments carry mnemonics.
  - **HTTPS links are handled only on iOS and Android; "the desktop runners never register a handler for them"** (`:4-7`).
  - Gift Card links are now v3 (`docs/compact-gift-links.md`).
- **vizor-deeplink-server [code/doc]:**
  - It is an AWS Lambda behind CloudFront (`dls:pulumi/index.ts`, `Pulumi.production.yaml`).
  - It is stateless by charter: *"no database, analytics, external browser code, or remote assets"* (`README.md:9-12`).
  - Its exact route registry has `/` and `/payment-links/open`; everything else gets a branded 404 (`src/routes.ts:10-19`; README `:70-90`).
  - *"A wildcard server handler … is not an acceptable substitute"* (`:88-90`).
  - The CSP is strict: `img-src 'self'`, `connect-src 'none'` (`src/handler.ts:15-24`).
  - OG and Twitter card tags exist only for the Gift Card page, with a static PNG (`src/page.ts:226-243`).
  - AASA claims all paths (`"/": "*"`) and Android uses `handle_all_urls` (`src/associations.ts:3-33`). New paths need no OS association change, only both allowlists.
  - Caution: the local checkout's fallback accepts only `#v1=` and `#v2=` (`src/page.ts:132`), but the wallet documents v3. The checkout may be stale.
- **[inference]** A delegate profile URL such as `link.vizor.cash/d/<handle>` needs:
  - a parameterized route on the server, which needs a policy exception;
  - server-side directory data for a per-delegate OG card and avatar, which breaks the no-DB/no-remote rule unless signed data is baked into the path or fragment;
  - desktop fallback behavior, since desktop cannot open the app from the link.

---

## 5. Networking and privacy posture
- **Tor [code]:** embedded arti (`zcash_client_backend::tor`, `rust/src/network_privacy.rs:1-30`). It is opt-in and defaults to off (`providers/network_privacy_provider.dart:18,79-80`). It fails closed while bootstrapping or broken (`AGENTS.md:624-651`).
- **Voting HTTP [code]:**
  - `DartIoVotingHttpClient` wraps `NetworkHttpClient`, which is policy-aware (`services/voting/voting_http.dart:56-125`).
  - `VotingApiClient` covers `/shielded-vote/v1/rounds`, `rounds/active`, `round/{id}`, `tally-results/{id}` and `tally/{id}/{pid}`, with failover across vote servers (`services/voting/voting_api_client.dart:39-176,220-252`).
  - SDK network clients are built only in `rust/src/wallet/voting/network_clients.rs`. The route table and audit rule for new network roles are in the README (`:311-341`).
- **Hosts contacted [code]:**
  - lightwalletd presets such as zec.rocks and stardust;
  - `enhance-pir.valargroup.dev` (`rust/src/wallet/sync_engine/enhancement/mod.rs:39`);
  - `voting.valargroup.dev` and `raw.githubusercontent.com` for config pins;
  - the dynamic config's vote servers and PIR, e.g. `vote-rpc-primary.valargroup.org` (`docs/voting-participation.md:42`);
  - `functions.vizor.cash` for voting discovery, `swap-enabled.json` and the 1Click proxy (`voting_discovery_client.dart:6-12`, `swap_remote_enable_config.dart:10-11`, `swap_provider_config.dart:7`);
  - `api.coingecko.com` or a proxy (`zec_price_change_provider.dart:15`);
  - `1click.chaindefuser.com`;
  - `download.z.cash` for Sapling params (`send/services/sapling_params.dart:10`);
  - the wallet-link backend;
  - GitHub releases for desktop updates (`core/config/app_version_config.dart:21-29`).
  - The Vizor-operated backend (`functions.vizor.cash`, "lambda-server", `docs/voting-home-discovery.md:243-245`) is a natural home for a delegate directory or avatar proxy. It is not in this snapshot.
- **Privacy rules [doc]:**
  - Discovery sends no account identifiers (`voting-home-discovery.md:38-42`).
  - Participation queries are correlatable, not PIR, and must not be logged (`voting-participation.md:14-20`).
  - Observability is debug-only and prints no URLs or addresses (`rust/src/wallet/voting/observability.rs:1-30`).

---

## 6. Feature flags, remote config, release and telemetry
- **Compile-time dart-defines [code]:** `VIZOR_FORM_FACTOR`, `ZCASH_DEFAULT_NETWORK`, `VIZOR_DEEPLINK_BASE_URL`, `VIZOR_VOTING_DISCOVERY_URL`, `VIZOR_VOTING_DISCOVERY_STAGE_URL`, `VIZOR_WALLET_LINK_BACKEND_URL`, `VIZOR_PAYMENT_LINK_REGTEST_ENABLED`, `VIZOR_DEGOOGLED` and others (grep of `EnvKey =` in `lib`).
- **Remote kill switch [code]:** the only generic precedent is a version-keyed JSON override for iOS swap (`core/config/swap_remote_enable_config.dart:10-31`; `swap_feature_config.dart:20-49`).
- **Network gating [code]:** features are gated per network (swap mainnet-only `swap_feature_config.dart:28-30`; donation `app.dart:~1422-1430`).
- **Voting-specific rollout levers [code]:**
  1. `[TEST]`-prefixed authenticated rounds are hidden unless "Show test rounds" is on (`providers/voting/voting_round_visibility_provider.dart:12-17`; `voting_config_settings_panel.dart:634-691`).
  2. The dynamic config's `supported_versions.vote_protocol` gate: the SDK accepts v0 and v1 only (`zv:config/mod.rs:82,102`). A new protocol version is automatically refused by old builds, with `ConfigSwitchKind.protocolChanged` handling in `providers/voting/voting_config_provider.dart:211`.
  3. Custom static config sources (`voting_config_settings_panel.dart:471-582`).
- **Telemetry [code]:** none. There is no analytics or crash SDK in `pubspec.yaml`. Voting observability is `cfg!(debug_assertions)` (`observability.rs:30`). The deeplink gateway forbids analytics, remote assets and redirects without a privacy review (`dls:README.md:171-173`).

---

## 7. Conventions a delegation UI must follow
- **Copy:** sentence case everywhere except Figma display headings and sidebar/screen titles (`AGENTS.md:266-287`). Update widgetbook fixtures and tests that assert copy. Copy-review CSVs live at the repo root and are not in this snapshot.
- **Figma:**
  - Implement and verify via deterministic scenarios in `lib/figma_compare/figma_compare_scenarios.dart`, which already has voting scenarios at `:706-760,1633-1690`, plus widgetbook `lib/widgetbook/voting_use_cases.dart`.
  - Use `scripts/figma-compare.sh widget` (`AGENTS.md:187-251,977-1009`).
  - Ignore OS-chrome layers.
  - `design_suggestion` (referenced at `mobile_settings_screen.dart:279-281`) is a repo-root list of UI with no Figma frame; it is not in the snapshot.
- **Layout:**
  - Use `kAppFormFactor` for UI branching, never `Platform.is*` (`AGENTS.md:105-123`).
  - Mobile sheets use `MobileBottomSafeArea` (`:1011-1037`).
  - iOS modal corners: `docs/design/ios-modal-corners.md`.
  - Desktop panes keep transparency unless intentionally opaque (`AGENTS.md:1005-1009`).
- **Architecture:**
  - Riverpod providers; session pinning to the account (README `:59-66`).
  - Destructive-operation drain registration (`AGENTS.md:342-352`).
  - FRB flat DTOs; a docs page per feature, e.g. `docs/voting-participation.md`.
  - Strict log hygiene.
- **Security:** a seed or mnemonic never leaves the Rust boundary. Hardware QR codes are scan-optimized.
