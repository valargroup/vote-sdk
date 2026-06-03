# Changelog

All notable changes to this repository will be documented in this file.

Historical changes before commit `704b202e2088b91caeaf2290cef85e4a9a759542` are untracked.

## Unreleased

### Changed

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
