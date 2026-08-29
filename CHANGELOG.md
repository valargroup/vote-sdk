# Changelog

All notable changes to this repository will be documented in this file.

Historical changes before commit `704b202e2088b91caeaf2290cef85e4a9a759542` are untracked.

Instructions on coordinated upgrades can be found [here](https://setup.valargroup.org/#coordinated-upgrade).

## Unreleased

- Prepare atomic cast-vote batches behind a coordinated activation gate, with
  one batch-wide authorization digest,
  chained unchanged ZKP #2 proofs, deterministic wire encoding, and recovery
  events that retain every vote commitment while appending only the final VAN.
- Preserve CometBFT transaction event attributes exactly in transaction status
  responses instead of interpreting Base64-like plain text as encoded data.
- Close idle helper REST and local CometBFT RPC connections before their server
  timeouts, and reconcile interrupted broadcasts before retrying, so scheduled
  reveal submissions do not race stale pooled connections.
- Stop reporting reveal-share proofs as invalid when CheckTx sees a commitment
  anchor ahead of locally committed state, while keeping the transaction
  rejected and retaining alerts for block execution and proof verifier failures.
- Let coordinators review pending action payloads as decoded, copyable JSON in
  the approvals UI.

## v1.4.1

- Pause helper ingress and queued share processing until the local Comet node
  is caught up and its latest block is fresh.
- Default validator circuits to Zakura's optimized cryptography backend while
  preserving an explicit upstream Zcash mode for compatibility testing.
- Group share failures emitted by the configured helper instance by round,
  processing stage, and queue action, with separate round-close summaries.

## v1.4.0

- Let Shielded Vote Creator import its JSON exports as new editable drafts,
  with schema validation and fresh local IDs.
- Show production share queues only when a current vote-manager wallet is
  connected, while keeping the staging monitor visible to everyone.
- Keep at most 256 unique vote share submissions in each block, and reject
  proposals that bypass round-scoped deduplication or the submission cap.
- Honor Comet's final proposal byte budget after injected transactions, and
  reduce the consensus maximum block size to 5 MiB for fresh chains and the
  `v1.4.0` upgrade.
- Serialize first-round helper queue metadata writes so concurrent submissions
  are not rejected by SQLite writer contention. Use a new helper
  proof-concurrency key that defaults validator-hosted helpers to one worker and
  ignores the earlier value, so the coordinated upgrade does not require manual
  edits on every validator.
- Route default production and staging voting-config reads through the
  GitHub-primary `voting.valargroup.dev` gateway with its Cloudflare fallback.
- Make new Linux Cosmovisor validator services skip local pre-upgrade chain data
  copies while retaining the existing external identity backup requirement.
- Reject malformed, round-invalid, and internally inconsistent helper shares
  before queueing, while keeping validator and infrastructure failures alertable.
- Limit helper share queue selection to active rounds, and use a lightweight
  current-round query for Vote Status, Share Queues, and Attest Round. Keep
  completed history collapsed and load it only when requested.
- Exponentially back off repeated helper share checks at an unchanged committed
  height to two minutes while retaining urgent retries near vote close.

## v1.3.1

- Add a `v1.3.1` coordinated upgrade handler so testnet can rehearse the patch
  release and mainnet can skip the unapplied `v1.3.0` plan.
- Add a checksum-pinned validator maintenance script that persists
  `UNSAFE_SKIP_BACKUP=true`, verifies the restarted Cosmovisor service, and
  removes only validated `data-backup-Y-M-D` directories.
- After validator restarts, wait for CheckTx to receive a block time before
  generating another reveal proof, and prevent an unset time from being
  interpreted as an expired round. Limit each share to one outbound submission
  per locally committed height so a stalled chain cannot exhaust its retry
  budget. Add a dry-run-first validator recovery script that requeues every
  retained failed share in an active round for reconciliation.
- Generate voter-throughput delegation proofs for the round ID emitted by the
  live chain and remove the incompatible shared proof fixtures.

## v1.3.0

- Publish the standalone Linux AMD64 `voting-config` verifier and checksum with
  each release so config repositories can pin an immutable auth-v2-capable tool.
- Add `v1.3.0-rc.1` and `v1.3.0` coordinated upgrade handlers and align the
  verifier and end-to-end test stack on the new voting-crate release candidates.
- Bind dynamic round attestations to the round ID, election-authority key, and
  `pir_layout` (including nested YPIR `poly_len`) with domain-separated
  `auth_version: 2` signatures, preventing cross-round and parameter replay.
  The admin UI lists only active rounds for attestation and intentionally fixes
  the current `19/12/7` layout and `poly_len` 4096 at the authorization point
  to avoid an additional network dependency. This matches the wallet-side
  verification in
  [zcash_voting#172](https://github.com/valargroup/zcash_voting/pull/172).
- Upgrade CometBFT to v0.38.21 for its patched consensus implementation.
- Derive safe validator snapshot resets from the local genesis chain ID and
  reject conflicting overrides before stopping the service.
- Raise the maximum accepted Halo2 proof size and matching proof-generation
  buffer from 8 KiB to 15 KiB for the new 11,328-byte delegation and
  11,040-byte vote proofs.
- Exclude shares first received at or after a round's close time from close-time
  unsubmitted-share alerts.
- Preserve reveal-share witnesses after CheckTx acceptance while bounded retries
  check for the committed nullifier.

## v1.2.0

- Keep reveal-share witness data queued when CometBFT broadcast and transaction
  status retries cannot determine whether the transaction was accepted.
- Let the admin UI and snapshot-data API accept both legacy three-tier and
  runtime two-tier PIR snapshots, and compare PIR fleet roots using semantic
  root names. Preserve and validate the negotiated PIR layout when automated
  config PRs add signed rounds.
- Accept compact, versioned Ironwood transaction effects in delegation messages
  and derive the RedPallas signing digest directly instead of receiving it as a
  separate message field.
- Add `v1.2.0-rc.1` and `v1.2.0` coordinated upgrade handlers. Testnet can
  rehearse the release candidate after its historical `ironwood-v1` upgrade,
  while testnet and mainnet use `v1.2.0` for the final cutover. Mainnet can
  skip the unapplied `v1.1.0` plan and pick up its Ironwood changes in the same
  upgrade.
- Publish checksum-pinned Cosmovisor archives for both Linux architectures,
  require the upgrade scheduler to load them, and show the exact upgrade info
  before signing. New and migrated validators enable checksum-required
  auto-downloads, recover only chain-confirmed stale upgrade markers, and
  refuse migrations that leave an unmanaged or duplicate signer process.

## v1.1.0

- Use Ironwood commitment and nullifier roots with the Ironwood voting proof
  verifier. Staging uses Zcash Testnet, production uses Mainnet, and release
  candidates no longer replace stable release pointers. The production
  coordinated upgrade plan matches the `v1.1.0` release tag; the already-applied
  staging `ironwood-v1` plan remains supported. Stable releases become Latest
  only after their mutable release pointers are verified.

## v1.0.3

- Observability improvements and operational script fixes. No changes to the node.

## v1.0.2

### Changed

- Helper endpoints now return `503` when the local node has not produced a block for more than 3 minutes (based on local Comet `/status` `latest_block_time`), preventing share ingestion on stale nodes.

## v1.0.1

Operational migration script updates. Does not touch chain code.

- Ensure managed systemd services load `/etc/default/svoted` after Cosmovisor migration so deploy-time environment variables remain available.

## v1.0.0

This is the coordinated upgrade intended to be applied on mainnet.
Previously, we had state-breaks across minor versions that we would
apply to stage nodes directly. However, production is still running v0.9.x
up until v1.0.0.

### Added

- Add a `v1` x/upgrade handler in `app.RegisterUpgradeHandlers` so coordinated upgrades can use the same plan name across `svote-1`, `zvote-1`, and test chains.

## v0.11.0 - 2026-06-03

### Added

- Validator coordinated upgrade tooling: `update_chain.sh`, `_chain_upgrade_common.sh`, and `prepare-upgrade-artifacts.sh` for Cosmovisor pre-staging with stage-first safety guardrails.
- `join.sh` defaults to `--upgrade-mode cosmovisor` on Linux (`direct` on macOS); use `--upgrade-mode direct` to run svoted without Cosmovisor.
- Gated upgrade validation checklist and verification scripts for release and isolated-network testing.
- Let coordinator policy updates set `min_ceremony_validators` through
  `MsgUpdateVoteManagers`, expose the current value on the vote-managers query,
  and add propose/current-policy controls on the admin Approvals page.

### Changed

- Harden validator upgrade scripts: Cosmovisor from official GitHub releases with checksum verification, systemd path autodetection when run as root, cosmovisor tree ownership fixup, fail-closed wrapper in cosmovisor mode, defensive upgrade-plan JSON parsing, and split staging vs service checks in `verify-prestage`.

## v0.10.0 - 2026-06-03

### Changed

- Update Orchard to v0.14
- Raise TSS threshold to two thirds
- Reject Halo2 proofs with trailing unread transcript bytes at the Rust FFI
  verifier boundary, and update the Rust voting dependencies to the published
  `voting-circuits` 0.7.0 / `zcash_voting` 0.10.2 releases.
- Preserve DKG coefficient files through speculative ceremony ack proposals so
  validators can retry `MsgAckExecutiveAuthorityKey` if CometBFT advances to a
  later round before their proposal commits. Coefficients are now cleaned up only
  after committed state moves the round past the ceremony ack window.
- Keep helper reveal shares pending when local submit, round status, tree readiness, or Merkle path failures look like system readiness or transport problems instead of spending failed-share attempts.

## v0.9.7 - 2026-05-28

### Changed

- Enable the explorer uptime view in the deployed and local Ping.pub explorer configuration.
- Hide the governance view from the deployed and local Ping.pub explorer configuration.
- Retain failed share witness data until the normal purge path removes expired rounds, preserving rescue and export data after permanent failures.
