# zcash_voting client library and custody-voter: findings for proxy delegation

Scope: `zcash_voting` 5.1.1-rc.3 (origin/main snapshot at `SRC=/private/tmp/claude-501/-Users-czstudio-Documents-vote-sdk--claude-worktrees-vote-delegation-planning-29b451/0cff7cc1-5f3f-4de1-be83-322a1fdf7727/scratchpad/src`, so `ZV=$SRC/zcash_voting`), custody-voter (`$SRC/custody-voter`), plus the voting-circuits, chain and Vizor files I had to read to check client behaviour. Paths below are relative to those roots unless absolute. Each claim is labelled one of three ways: **[code]** means verified in source, **[doc]** means a document claims it, **[inference]** means my own reasoning.

Terminology: "delegation" or "MsgDelegateVote" means today's ZKP1 move of note weight into a VAN that is bound to a hotkey. **Proxy delegation** means the new feature: giving voting power to another person.

---

## 0. Headline answers

1. **Can a delegator learn whether the delegate voted with the delegated power? Yes, with today's circuits, but not from tree leaves alone. [code + inference]**
   - ZKP2 condition 7 rebuilds the successor VAN from the *same* `vpk_g_d`, `vpk_pk_d`, `num_ballots`, `round_id` and **`van_comm_rand`**. Only `proposal_authority` changes, to `old - 2^proposal_id` (`voting-circuits/src/vote_proof/circuit.rs:1104-1123`; `voting-circuits/src/vote_proof/README.md:259-267`). The ZIP draft says this reuse is deliberate (`zips/draft-valargroup-shielded-voting__greg_shielded-voting-fixes.md:1364-1374`).
   - A proxy delegator builds the VAN, so it knows the full opening. It can therefore compute every candidate successor VAN.
   - The singleton path, `MsgCastVote`, appends the new VAN to the tree (`x/vote/keeper/msg_server.go` CastVote; `proto/svote/v1/tx.proto:76-88`).
   - `MsgCastVoteBatch` appends **only the final VAN** to the tree (`x/vote/keeper/msg_server.go:253`). Its event publishes `proposal_ids` and `final_van_leaf_index` in plaintext (`msg_server.go:272-280`). Every action's `vote_authority_note_new` sits in the tx body.
   - The SDK casts up to 50 proposals per batch (`zcash_voting/src/vote.rs:34`; chain `x/vote/types/msgs.go:15`). So a delegator that only syncs tree leaves would have to guess the delegate's exact proposal subset.
   - With the cast-vote-batch **events** or **tx bodies**, the check is exact. For each batch event, compute `VAN(MAX − Σ_{p∈batch} 2^p)` from the stored opening and compare it with the leaf at `final_van_leaf_index`. Alternatively, walk the per-action `vote_authority_note_new` values.
   - What the delegator learns: whether the VAN was spent, on which proposals, and at what height.
   - What it does not learn: the choices. The vote commitment binds `shares_hash`, which is built from share commitments blinded with randomness derived from the delegate's key (`voting-circuits/src/vote_proof/builder.rs:84-128`).
   - No chain endpoint serves cast actions per round today. The REST routes are listed in §7.
   - The hash helper needed for this, `van_integrity`, is `pub(crate)` in voting-circuits (`voting-circuits/src/gadgets/mod.rs:11`). The client wrapper `plan_vote_authority_transition` requires the hotkey seed (`zcash_voting/src/zkp2.rs:33-71`). A public, address-based helper must be exported.
   - **This property breaks if a future VAN-merge or VAN-split circuit re-randomizes without publishing a tag the delegator can recognize.**

2. **The delegate can discover incoming delegations on-chain today, in principle, without an off-chain channel. [code + inference]**
   - The ZKP1 output note is a zero-value Orchard/Ironwood note *to the hotkey address* with a 512-byte memo (`zcash_voting/src/action.rs:163, 564-613`).
   - Its `enc_ciphertext` (580 bytes) and `out_ciphertext` are published in `MsgDelegateVote.tx1_effects` (`zcash_voting/src/tx1.rs:16-27, 53-58`; `proto/svote/v1/tx.proto:57-69`).
   - The hotkey holder can trial-decrypt with its IVK.
   - Today's memo is display text only ("I am authorizing this hotkey…Amount: X ZEC"; `zcash_voting/src/delegate.rs:2426-2449`). It does **not** carry `van_comm_rand`, so a delegate cannot open the VAN from the chain alone.
   - Fix options: a structured memo payload, or a protocol rule `van_comm_rand := PRF(rseed_output)`.
   - The chain keeps only `van_cmx` in state and emits only `leaf_index` (`x/vote/keeper/msg_server.go:140-171`). A tx-body or ciphertext feed endpoint is needed.

3. **A seed-derived, stable delegate identity is technically easy.**
   - The hotkey "stored secret" is 64 bytes and is used as a ZIP-32 seed: `UnifiedSpendingKey::from_seed(network, secret, account 0)`, address index 0 (`zcash_voting/src/hotkey.rs:41-105`).
   - `zcash_voting` already depends on `zip32 0.2` (`zcash_voting/Cargo.toml:82`; `Cargo.lock:4557-4558`). That version exposes `registered::cryptovalue_from_subpath(context, seed, zip_number, subpath) -> [u8; 64]` (`~/.cargo/registry/src/index.crates.io-*/zip32-0.2.1/src/registered.rs:225-262`), which is exactly the right size.
   - Blocker: registered derivation needs a ZIP number, and every voting ZIP draft is "ZIP: Unassigned" (`zips/*.md:1-2`).
   - Policy conflict: the docs explicitly say the hotkey "is not wallet seed or mnemonic-derived material" (`zcash_voting/src/types.rs:510-515`).
   - What breaks is covered in §1.

4. **Splitting "X% to each of up to 10 delegates" is not possible inside one bundle with the current circuits. [code]**
   - One ZKP1 bundle (at most 5 notes) makes exactly one VAN and one TX1 action (`zcash_voting/src/governance.rs:17-22`; `tx1.rs:16-27`).
   - The default bundle policy trims to at most 2 bundles (`note_bundling.rs:21-52`).
   - Bundle-granular assignment therefore gives most wallets at most 1 or 2 independently assignable weight units.
   - A circuit change is needed. Candidates are a multi-output ZKP1 or a post-delegation VAN-split proof; see §3.

5. **A popular delegate's workload scales as (incoming VANs) × (proposals) proofs, plus 16 helper shares per proof. [code + doc]** Mitigations: a merge circuit, a minimum delegation size, desktop or server delegate tooling.

---

## 1. Hotkey generation, derivation, persistence and round scoping

### What the code does today [code]

- **Constants** (`zcash_voting/src/hotkey.rs:11-19`):
  - `VOTING_HOTKEY_STORED_SECRET_LEN = 64`
  - `VOTING_HOTKEY_ACCOUNT_INDEX = 0`
  - `VOTING_HOTKEY_ADDRESS_INDEX = 0`
- **Generation.** `generate_random_voting_hotkey(network)` fills 64 bytes from `OsRng` (`hotkey.rs:35-39`).
  - Its doc says to generate "once per local voting identity and round" and store it in platform secure storage.
  - It is "not deterministic across fresh app installs unless the stored hotkey secret is restored" (`hotkey.rs:21-30`).
- **Reconstruction.** `voting_hotkey_from_stored_secret` requires exactly 64 bytes, then derives the Orchard spending key with `UnifiedSpendingKey::from_seed(&network, secret, AccountId(0))` and takes the external address at index 0 (`hotkey.rs:41-105`).
  - The same secret gives different addresses per network (`hotkey.rs:126-132`).
  - In other words, the stored secret *is* a ZIP-32 seed for a throwaway account.
- **`VotingHotkey`** (`types.rs:500-574`) holds the secret, the 43-byte raw address, `address_index` and `network`.
  - `delegation_target()` debug-asserts address index 0 (`types.rs:566-573`).
  - `from_stored_secret` docs: "not wallet seed or mnemonic-derived material" (`types.rs:510-522`).
- **`VotingHotkeyTarget`** (`types.rs:385-441`) is the public, secret-free recipient: raw Orchard bytes plus network.
  - V1 always uses address index 0 (`types.rs:396-424`).
  - Raw receiver bytes do not encode a network (`types.rs:385-388`).
- **`RoundBoundVotingHotkeyTarget`** (`types.rs:443-495`) binds the target to a chain ID and round ID.
  - It can only be constructed through `VotingHotkeyTargetV1::validate_for` (`from_validated_parts` is `pub(crate)`, `types.rs:455-466`).
  - Delegation rejects use against another round (`types.rs:481-494`).
- **Deterministic VAN blinding for the local-hotkey path** (`van_blinding.rs:14-93`):
  - `VanBlindingKey = BLAKE2b-512(key = hotkey.stored_secret, "zcash_voting/van-blinding-key/v1")`.
  - Per bundle, the blinding is derived from network, round ID, snapshot height, `ea_pk`, `nc_root`, `nullifier_imt_root`, `bundle_index`, and the sorted `(position, cmx, value)` of each real note.
  - It is wired in through `DelegationKeys::with_voting_hotkey` (`delegate.rs:184-197`).
  - The public-target path, `with_round_bound_voting_target` (`delegate.rs:199-226`), has no secret. Its blinding "remains randomly sampled and must be retained" (`delegate.rs:209-210`; random fallback at `action.rs:494`).
- **Vizor persistence.** Hotkeys are stored per `(accountUuid, roundId)` in secure storage (`$SRC/vizor-wallet/lib/src/core/storage/voting_hotkey_store.dart:1-80`). Vizor uses `recoverable_bundle_policy_v1()` (`$SRC/vizor-wallet/rust/src/wallet/voting/participation.rs:555, 943, 1015`).
- **custody-voter persistence.**
  - One random hotkey per (profile, round), kept in the OS Keychain (`$SRC/custody-voter/src-tauri/src/voter.rs:106-108`; README:66).
  - Backed up as an age-encrypted envelope containing hotkeys and the database (`docs/architecture.md:81-94`).
  - custody-voter pins `zcash_voting = "=3.0.0-rc.3"` (`src-tauri/Cargo.toml:52`).

### Can a long-lived delegate identity key be derived from the wallet seed? [inference, grounded in code]

- **Mechanically, yes.**
  - Proposed derivation: `stored_secret = zip32::registered::cryptovalue_from_subpath(ctx, wallet_seed, ZIP_N, [coin_type', account', "delegate-identity" tag, identity_index'])`. This yields 64 bytes, so `VotingHotkey::from_stored_secret` and every existing proving and signing path work unchanged.
  - Do **not** pass the raw BIP-39 seed. That is technically 64 bytes too, but it would make the "hotkey" equal the wallet's real account-0 Orchard spending key.
  - Until a ZIP number exists, `zip32::arbitrary` with a unique context string is the stopgap. It gives no registration guarantee.
- **Stable address across rounds.** The address stays fixed at index 0, so the delegate gets one publishable address that is recoverable from the seed. The influencer UX needs exactly this.

What breaks or needs attention:

1. **On-chain linkability across rounds: none found for public observers. [inference]**
   - The VAN is a blinded Poseidon commitment.
   - The governance output note is encrypted with a fresh ephemeral key, and `cmx_new` uses a fresh `rcm`.
   - The ZKP2 address is a private witness, `r_vpk` is rerandomized, and the VAN nullifier is domain-separated per round (`vote_proof/README.md`, conditions 3-5).
   - Anyone who knows the delegate's IVK can link everything. That is the delegate.
2. **Delegators can trace the delegate's VAN chain** for each VAN they created, through randomness reuse (§0.1). The ZIP's "safe because never externally observable" rationale (`zips/...greg_shielded-voting-fixes.md:1364-1374`) assumes only the owner knows the opening. That no longer holds for proxy delegation, and already does not hold for the custody capability handoff.
3. **Key exposure grows.** A long-lived key that leaks lets an attacker vote all future delegated weight and read every incoming memo. The design needs rotation (`identity_index`) and directory revocation.
4. **Nonce and PRF reuse across rounds looks safe. [inference from code]**
   - `VanBlindingKey` binds round and bundle identity (`van_blinding.rs:68-86`).
   - The share PRF binds `sk`, `round_id`, `proposal_id` and the VAN commitment (`zkp2.rs:83-92` doc; `voting-circuits/src/vote_proof/builder.rs:84-128`).
   - The VAN nullifier includes `round_id`.
5. **Hardware-wallet accounts have no seed in Vizor.** Keystone and Ledger signers return only SpendAuth signatures (`delegation_pipeline/mod.rs:17-20`). These accounts cannot derive a seed-based delegate key. They need a random key plus backup, or device support for ZIP-32 registered derivation.
6. **Storage shape.** Vizor's hotkey store is keyed per round (`voting_hotkey_store.dart`). A per-account "delegate identity" slot is needed.
7. **Do not mix roles in one scope.** If the same key is used as the user's personal round hotkey *and* their delegate identity, own bundles and imported incoming VANs share one `wallet_id` scope. `ensure_bundles_with_policy` requires the persisted bundle count to equal the planned count (`round/mod.rs:727-797`), and capability import requires an empty or exactly matching bundle set (`delegation_capability.rs:432-447`). Use a separate `wallet_id` scope for incoming delegations.
8. **The address format must change.** `VotingHotkeyTargetV1` is round-bound: it carries `vote_chain_id` and `vote_round_id` (`wire.rs:151-171`). A stable directory entry needs a new round-free format, such as `DelegateIdentityV1 {network, raw_orchard_address or Orchard-only UA}`, plus a public way to bind it to a round, producing a `RoundBoundVotingHotkeyTarget`. Today that constructor is not public.
9. **Proof of control of an address.** An Orchard address carries no `ak`, so a SpendAuth signature cannot be checked against it. Because `pk_d = [ivk]·g_d` (`zips/...greg_shielded-voting-fixes.md:542-544`), a domain-separated Schnorr proof of knowledge with base `g_d` and public key `pk_d` *can* be checked from the address alone. **[inference]** This is a candidate for the X-post claim signature, and needs crypto review.

---

## 2. VotingDb schema, round state, recovery, tree sync, confirmation, phases, session

### Schema [code] (`zcash_voting/src/storage/migrations/001_init.sql`)

- **`rounds`** (lines 1-14): PK `(round_id, wallet_id)`. Holds round params, `phase`, `session_json`, `bundle_policy_json`. **No hotkey or target column.**
- **`bundles`** (lines 16-44): PK `(round_id, wallet_id, bundle_index)`. One VAN per bundle.
  - `van_comm_rand`, `gov_comm` (the VAN), `total_note_value` (quantized when imported), `address_index`, `van_leaf_position` (current successor VAN position, advanced by votes), `delegation_tx_hash`.
  - Plus construction fields: `note_positions_blob`, `rho_signed`, `nf_signed`, `cmx_new`, `rseed_output`, `alpha`, `rk`, `gov_nullifiers_blob`, `tx1_effects`, `delegation_pczt`, and others.
  - **No target address column.** The target is supplied at each step and checked against persisted `cmx_new` and `gov_comm` by `validate_hotkey_address_for_bundle` (`storage/operations.rs:151-230`).
- **`votes`**: `UNIQUE(round, wallet, bundle, proposal)` (lines 80-94).
- **`share_delegations`**: per `(bundle, proposal, share)` (lines 138-155).
- **`ballot_intent`**: per `(round, wallet, proposal)`, **not per bundle** (lines 170-181).
- **`chain_submissions`** (lines 201-245): `kind CHECK IN ('delegation','vote','vote_batch','delegate_and_cast_vote_batch')`, `proposal_id BETWEEN 1 AND 50`. Adding a new kind (for example a VAN split) needs a table rebuild, like `006_delegate_cast.sql`.
- **`delegate_cast_recovery`** and **`combined_cast_rejections`** (lines ~296-340).
- **Migrations**: `CURRENT_VERSION = 24`, `LAUNCH_VERSION = 13`. Every change must be an in-place, row-preserving migration plus an `001_init.sql` update (`storage/migrations.rs:5-25`).
- **`RoundPhase`**: `Initialized`, `HotkeyGenerated`, `DelegationConstructed`, `DelegationProved`, `VoteReady` (`storage/mod.rs:24-30`).
- **`DelegationPhase`**: `Prepared`, `PcztBuilt`, `Proved`, `Submitted`, `SubmissionManaged`, `SubmittedWithoutHash`, `SubmissionRejected`, `Confirmed` (`phases.rs:75-92`).

### Recovery after reinstall [code + doc]

- `recovery.rs` is a **read-only snapshot API**, not a rebuild (`recovery.rs:1-4, 229-275`).
- Rebuild after database loss:
  - Restore the hotkey secret.
  - Use `recoverable_bundle_policy_v1()`, the frozen v1 planner (`note_bundling.rs:325-343`).
  - Deterministic blinding then reproduces the same VAN (CHANGELOG v3.1.0-rc.16, `CHANGELOG.md:265-272`).
  - Exact-tree recovery (`chain_submission/recovery.rs`) streams `/commitment-tree/{round}/leaves` to find the exact generation layout and confirm with `confirmation_source='tree'` (`001_init.sql:217, 239-244`).
- **Re-derivable from seed plus chains:** notes and snapshot selection, the bundle plan under the v1 policy, governance nullifiers (Vizor's participation check derives them from the FVK alone; `$SRC/vizor-wallet/docs/voting-participation.md:9-12`), and VAN positions, *if the VAN can be recomputed*.
- **Lost:**
  - The random hotkey, unless secure storage or a backup survived. Without it the round's weight is unusable, because the governance nullifiers are already spent.
  - Public-target (custody) VAN blinding, which is random. Recovery depends on the provider's outbox (`docs/exporting-to-external-software.md:68-72, 115-118, 172-195`).
  - Ballot intents.
  - Helper-share recovery JSON for votes cast but not yet delivered.
- **For proxy delegation, the delegator needs** (a) deterministic blinding keyed by delegator-owned, recoverable material, and (b) a recoverable record of *which delegate and how many ballots*.
  - Candidate: set the account OVK on proxy outputs. The code already supports this for Ledger review (`action.rs:606`; `delegate.rs:140-148`; CHANGELOG v5.1.0).
  - The delegator can then recover the delegate address and memo from published `tx1_effects`. **[inference]**

### Tree sync [code]

- `VoteTreeSync` keeps an in-memory `TreeClient` per round (`tree_sync.rs:362-367`) and downloads **every leaf** ("no trial decryption needed since all vote-tree leaves are public"; `vote-commitment-tree/src/client.rs:8-9, 320`).
- It marks known VAN positions and, before a bundle's first vote, verifies that the leaf at the confirmed position equals the stored delegation VAN (`tree_sync.rs:441-559`).
- **Leaves are commitments only.** Tree sync cannot discover *incoming* delegations; it can only locate or verify *known* commitments.
- A cheap extension: a leaf-visitor hook in `TreeClient::sync` (`client.rs:312-347`) that matches a set of expected successor VANs.
  - This is enough for singleton casts.
  - It is not enough for batch casts, which only append the final VAN. Those need an event or tx feed (§0.1).

### Confirmation [code]

- `confirmation.rs` is private lifecycle projection. `apply_delegation_confirmation_with_conn` stores the tx hash and VAN position; votes advance `van_leaf_position` (`confirmation.rs:1-8, 44-83`).
- Imported capabilities confirm through `ChainSubmissionClient::advance_imported_delegation` (`chain_submission/client.rs:565`), which polls the stored tx hash and records the `delegate_vote` event's `leaf_index`.

### Session and planner [code]

- `RoundPlan` (`session.rs:717-830`) is the resume plan. Relevant constraints:
  - Ballot intents must exactly cover the roster before any cast is planned (`round_planning/classify.rs:205-210`).
  - **An imported-capability round blocks every new vote until all imported bundles are `Confirmed`** (`classify.rs:212-221`; doc `exporting-to-external-software.md:157-160`).
  - `hotkey_bound` tells the host the round requires the same hotkey.
- `RoundExecutor`/`RoundBinding` binds **one** hotkey secret per round (`vote_work/mod.rs:45-57`).
- `DelegationPipeline` binds **one** hotkey per account per round (`delegation_pipeline/mod.rs:45-55`).

---

## 3. Bundling and how a proxy split maps onto VANs

### Code today [code]

- **Bundle shape.** At most 5 real notes per bundle (`BUNDLE_NOTE_SLOTS = 5`, `governance.rs:17-22`).
- **Algorithm** (`note_bundling.rs:577-742`):
  - Sort notes by value, descending; fill sequentially.
  - Start a new bundle past the 25,000 ZEC threshold (v1).
  - Drop bundles below 1 ballot.
  - Sort bundles by value, descending.
  - Privacy trim down to `DEFAULT_MAX_PRIVACY_BUNDLES = 2` within a budget of 1% or 1,000 ZEC (`note_bundling.rs:21-52`).
- **Quantization.** Each bundle VAN commits to `num_ballots = floor(Σvalue / 12,500,000)` (`governance.rs:11-15, 85-110`).
  - Minimum 1 ballot (0.125 ZEC).
  - Ceiling 2^30 ballots (delegation README §8, `voting-circuits/src/delegation/README.md:339-356`).
  - The circuit admits a one-ballot under-claim for about 34% of values (README:357-369; `params.rs:3-14`).
- **Proposal authority.** ZKP1 hardcodes `MAX_PROPOSAL_AUTHORITY` (`delegation/circuit.rs:235-247`; README §7). A delegated VAN therefore always carries full authority for all 50 proposals.
- **One target per job.** One TX1 holds exactly one Ironwood action, and the chain validates an 821-byte framing (`tx1.rs:16-27`; chain `x/vote/types/tx1.go`). The custody doc says the controller "MUST use the same target for every bundle in that delegation job" (`exporting-to-external-software.md:56-57`).

### Mapping "X% to each of up to 10 delegates" [inference]

Worked example. Notes of 600, 300, 50, 30, 15, 4 and 1 ZEC give:

- B0 = {600, 300, 50, 30, 15} = 995 ZEC = 7,960 ballots
- B1 = {4, 1} = 5 ZEC = 40 ballots

A split of 50% to A, 30% to B and 20% kept is **impossible** at bundle granularity, because B0 holds 99.5% of the weight. Most wallets will have 1 or 2 bundles.

| Option | Circuit change | Granularity | Notes |
|---|---|---|---|
| A. Assign whole bundles | None | ≤ #bundles | Nearly useless for "up to 10". Could raise bundle count with smaller `max_real_notes_per_bundle`, but that depends on how many notes the wallet has. |
| B. Multi-output ZKP1 (k VANs and k encrypted outputs per bundle) | ZKP1 + TX1 multi-action + chain tx1 validation + Keystone/Ledger review | ballot | Hardware signer reviews k recipients. The chain's fixed 821-byte TX1 framing changes. |
| C. VAN split after delegating to own hotkey (book's "VAN split", `shielded-vote-book/delegation/partial-delegation.md`) | New split circuit and Msg | ballot | No ZKP1, TX1 or hardware-wallet change. Signed by the delegator's hotkey on the vote chain. Delegator can postpone the choice and keep a remainder. Needs a new per-output ciphertext for discovery. |

Allocation rules the library should own whatever the option:

- Convert percentages into **integer ballots** over the eligible weight (largest remainder).
- At least 1 ballot per output.
- Each split's outputs must sum exactly to the source VAN's `num_ballots`.
- Minimise VAN count: assign whole bundles first, split only the remainder. The VAN count is at most #bundles + #delegates − 1.
- Report exact ballots per delegate in the UI.
- Under C, outputs inherit the source VAN's proposal authority. Votes the delegator already cast are excluded from the delegate's authority, which prevents double counting.

---

## 4. Today's external delegation (custody handoff) and what to reuse

### How it works [code + doc]

- **Voter side.** Creates a fresh hotkey and sends `VotingHotkeyTargetV1` JSON. The target is round- and chain-bound, address index 0 (`wire.rs:151-171`; doc:38-57).
- **Controller side.**
  - Validates with `validate_for`, then builds with `prepare_delegation_bundle_for_target` or `DelegationKeys::with_round_bound_voting_target`. Blinding is random.
  - Proves, signs and persists the signed txs.
  - Calls `export_delegation_capability` (`delegation_capability.rs:274-376`). This requires locally prepared and proven bundles and refuses rounds with combined delegate-and-cast (`:283-290`). It persists the result, then broadcasts and delivers.
- **Package format.** `DelegationCapabilityV1` is canonical JSON: `{format_version, vote_chain_id, network, vote_round_id, address_index, raw_orchard_address, bundles: [{bundle_index, num_ballots, van_comm_rand, delegation_tx_hash}]}` (`:39-79`).
  - Strict codec, at most 4,096 bundles and 1 MiB (`:27-37, 140-232`).
  - The package is privacy-sensitive but grants no voting authority.
- **Voter import** (`import_delegation_capability`, `:379-457`):
  - Recomputes every VAN from the voter's own hotkey address.
  - Inserts all bundle rows atomically. Exact re-import is a no-op; any partial or conflicting state is rejected (`:432-447`).
  - Sets `DelegationProved`.
- **Confirmation.** Uses `advance_imported_delegation`. All bundles must be confirmed before the first vote (doc:145-166; `classify.rs:212-221`).
- **custody-voter app** (Tauri desktop):
  - Flow: generate target, then import capability, then confirm each tx, then vote (`README.md:63-90`; `src-tauri/src/voter.rs:106-276`; `docs/architecture.md:57-71`).
  - Pinned, authenticated config. Keychain hotkeys. Age-encrypted backups.

### Guarantees and limits [code + doc]

- **Guarantees:**
  - No seed, spending key or hotkey secret crosses the boundary (doc:18-19).
  - The voter verifies VAN correctness against its own key, and verifies the leaf during sync (`tree_sync.rs:441-454`).
  - Holding the package alone gives no voting authority (doc:168-170).
- **Limits:**
  - Full weight only, one target per job.
  - "One funds controller and one fresh hotkey per voter and round" (doc:34-36).
  - Complete, contiguous batch from index 0 only; no incremental import.
  - All-bundle confirmation barrier.
  - Off-chain authenticated channel required. No public-chain recovery; both parties must retain state (doc:115-118, 172-195).
  - Address index fixed at 0.
  - The controller host, not `VotingDb`, must retain the target (doc:68-72).

### Reusable for proxy delegation [inference]

- **Delegator side:** `RoundBoundVotingHotkeyTarget` and `with_round_bound_voting_target` are the "send to someone else's key" path. Needed additions:
  - Per-bundle or per-output targets.
  - Persisting the target in `VotingDb`.
  - Deterministic delegator-side blinding.
- **Delegate side:** the importer's validation core (recompute VAN from own key, verify against public `van_cmx`, poll-only confirmation, leaf verification in tree sync) is reusable. Needed additions:
  - An **incremental, per-delegation** import keyed by `van_cmx` or tx hash, with allocated bundle indices.
  - Per-bundle readiness instead of the round-wide barrier.
- **Payload:** the capability bundle fields (`num_ballots`, `van_comm_rand`, `delegation_tx_hash`) are the right content for a proxy payload, whether it arrives in a memo, by relay or as a QR code.
- **Codec:** the canonical-JSON style of `VotingHotkeyTargetV1` suits a round-free `DelegateIdentityV1`.

---

## 5. Share sizing for a high-weight delegate

- **Per-VAN limits [code]:**
  - `num_ballots ≤ 2^30` (delegation condition 8). For scale, 21M ZEC is about 168M ballots.
  - 16 shares per vote commitment (`share_policy/server_order.rs:8`; `vote_proof/builder.rs:39`).
  - Each share `< 2^30` (`params.rs:16-23`).
  - Denomination split: greedy fill with {10M, 1M, …, 1} ballots across at most 9 slots, the remainder spread over the remaining 7 or more (`builder.rs:53-128`).
  - A merged VAN holding all the delegated weight would still fit within 2^30. **[inference]**
- **Privacy [doc + inference]:**
  - Holders of the complete EA secret key can decrypt individual shares (`voting-circuits/docs/design.md`).
  - Large denominations (1M or 10M ballots) are rare. If a delegate merges incoming VANs, its shares reveal whale-scale weight to EA-key holders.
  - Without a merge, each delegated VAN is voted separately with normal-sized shares. That costs more proofs but resembles ordinary voters.
- **Workload [code + inference]:**
  - Per VAN per proposal: 1 ZKP2, 16 helper shares, 16 ZKP3 reveals made by helpers on chain.
  - Batches merge proposals per VAN into one tx (`MAX_VOTE_BATCH_ACTIONS = 50`, `vote.rs:34`) but do not reduce the proof count.
  - Example: 1,000 delegators × 10 proposals = 10,000 ZKP2 and 160,000 shares and reveals for one influencer.
- **Immediate share** [code]: the round's immediate share is designated on the lowest-value bundle and the lowest voted proposal (`share_policy/initial_placement.rs:21-38`). That is fine for delegates.
- **No delegate-specific share code is needed** unless a merge circuit is added.

## 6. Proof timing and memory

- **Circuit sizes [doc]:** ZKP1 K=12 (4,096 rows), ZKP2 K=11, ZKP3 K=10 (`voting-circuits/README.md:109-114`).
- **Bundle pipeline benchmark** (Apple M4 Max, 2026-09-09; `docs/bundle_pipeline_benchmark.md:39-55`) [doc]:
  - 6 bundles × 2 proposals = 12 real ZKP2.
  - 22.3 s SDK wall time at concurrency 1, 13.5 s at concurrency 5.
  - About 23.5 CPU-seconds in total, so roughly **2 CPU-seconds per ZKP2 including overhead**.
  - Peak RSS 180-330 MiB.
  - ZKP1 not measured.
- **Design doc** (`voting-circuits/docs/design.md:27-30, 465-479`) [doc]: a consolidated ZKP2 prototype took 1.1 s on M4 Max, "more than two seconds on mobile". A proposed split ZKP 1.5 took 531 ms and the cast 81 ms; peak RSS about 0.9 GiB. That is a **proposal**, not current code.
- **Book** [doc, older]: "Register takes ~30s … Each vote ZKP also takes ~15s" (`shielded-vote-book/overview/comparison-to-today.md:37-39`).
- **Runtime** [code]: Rayon prover workers with 64 MiB stacks, worker count equal to available parallelism by default (`proving_runtime/mod.rs:16-31, 79`).
- **Helper delivery** [doc]: 3 bundles × 37 proposals gives 111 proofs and 1,776 shares, with about 556 s of accumulated request time, idealized to 17-35 s of wall time (`docs/helper_delivery_benchmark.md:22-27, 161-163`). A staging run took 6.5 minutes of confirmation for 144 shares (`stage-bench/README.md:95-113`).
- **No mobile numbers exist in these repos.** [finding]

## 7. Config and service discovery

- **Static config** [code]: hash-pinned, ed25519 `trusted_keys`, dynamic URLs (`config/mod.rs:414-449, 852-873`).
- **Dynamic config** [code]: `vote_servers`, `pir_endpoints`, `pir_layout`, `supported_versions`, and signed `rounds` (`config/mod.rs:875-884`). Only round entries are signed: `RoundAuthPayloadV2 = domain || round_id || ea_pk || pir layout` (`round_auth.rs:8-50`).
- **Forward compatibility** [code]: none of the config wire structs uses `deny_unknown_fields`. A new optional `delegate_directory_endpoints: [ServiceEndpoint]` field can be added without breaking older clients. **[inference]**
- **Do not put a directory key in `trusted_keys`** [inference]. Every trusted key authenticates rounds (`config/mod.rs:457-470`), so a directory key there could also sign rounds. Use a separate purpose-tagged key list, or a new signed payload domain, for example `zcash-shielded-vote:delegate-directory:v1`.
- **`WalletCapabilities`** advertises `vote_protocol` v0 and v1 (`config/mod.rs:84-106`). A proxy-delegation protocol version should be gated here.
- **No chain endpoint exists for discovery or tracking.** The chain REST routes are delegate-vote, cast-vote, cast-vote-batch, delegate-and-cast, tx/{hash}, commitment-tree latest/leaves/height, shares, share-status, rounds, tally and similar (grep of `x/` in the vote-sdk worktree). None lists delegation tx bodies, ciphertexts or cast actions per round.

## 8. APIs the library would need

### (a) Delegator

- `DelegateIdentityV1` parse and verify, plus `bind_to_round(chain_id, &VotingRoundParams) -> RoundBoundVotingHotkeyTarget`.
- `plan_proxy_split(eligible plan, targets: [(DelegateIdentity, Share)], keep_self) -> ProxySplitPlan`. Deterministic, integer ballots, enforces at most 10 delegates, minimum ballots, minimum VAN count.
- Pipeline support for per-output targets:
  - Option A: per-bundle `DelegationKeys`.
  - Options B and C: split proof building and submission. This adds a new `chain_submissions` kind.
- Persistence: new tables keyed `(round, wallet, bundle, output)` holding the target address, delegate identity reference, `num_ballots`, `van_comm_rand`, VAN, tx hash and status.
- Deterministic blinding from delegator-owned recoverable material, plus OVK-enabled outputs.
- Structured memo `ProxyDelegationPayloadV1`: `num_ballots` (quantized, not raw zatoshi, which `display_memo` exposes today), `van_comm_rand`, round, human text including `@handle` for on-device review. It must stay printable ASCII to avoid Ledger's memo-hash path (`delegate.rs:2451-2463`).
- Planner and `RoundPlan`: delegated-away bundles and outputs are **not** cast obligations. Expose kept versus delegated weight, and per-output delegation and vote status. Vizor's participation check must tell "delegated" apart from "voted" (`voting-participation.md`).
- `track_proxy_delegations(db, round, feed) -> [ProxyStatus {delegate, ballots, delegation_confirmed, voted_proposals, heights}]`. Requires a public successor-VAN helper in voting-circuits and a chain feed of cast actions or events.
- `recover_proxy_delegations(seed-derived keys, round, feed)`: rebuild from chain data through OVK decryption or deterministic recomputation.

### (b) Delegate

- `derive_delegate_identity(seed, network, account, identity_index) -> VotingHotkey`, using registered ZIP-32 derivation; a random variant plus backup for hardware accounts.
- `DelegateIdentityV1::to_json`, and `sign_identity_claim(handle, nonce)` / `verify_identity_claim`. Candidate scheme: a Schnorr proof of knowledge of `ivk` with base `g_d`, public key `pk_d`.
- `scan_incoming_delegations(identity, round, feed) -> [IncomingDelegation]`:
  - Trial-decrypt the `tx1_effects` `enc_ciphertext` (or the future split ciphertexts) with the hotkey IVK.
  - Parse the payload, recompute the VAN, and require it to equal the public `van_cmx`.
  - Take the leaf index from the event, or find it by an exact tree scan.
- `import_incoming_delegation(scope, incoming)`: idempotent on `van_cmx`, allocates the next bundle index in a dedicated `wallet_id` scope, per-bundle readiness with no round-wide barrier.
- Voting with consolidated weight: reuse `RoundExecutor` over the incoming scope with shared ballot intents. Optionally `plan/prove_van_merge` if a merge circuit is adopted; then decide how delegators observe a merge.
- `recover_delegate_round(seed, round, feed)`:
  - Re-derive the key and rescan.
  - Detect already-spent VANs: the delegate can compute VAN nullifiers itself and check them with Vizor's verified `abci_query` path (`voting-participation.md:22-31`), or walk successors.
  - Resume.
- Policy and estimation: `estimate_delegate_workload`, and `min_accepted_ballots` (ignoring dust delegations; this must be disclosed to delegators).
