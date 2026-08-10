# Changelog

All notable changes to this repository will be documented in this file.

Historical changes before commit `704b202e2088b91caeaf2290cef85e4a9a759542` are untracked.

Instructions on coordinated upgrades can be found [here](https://setup.valargroup.org/#coordinated-upgrade).

## Unreleased

- Derive safe validator snapshot resets from the local genesis chain ID and
  reject conflicting overrides before stopping the service.

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
