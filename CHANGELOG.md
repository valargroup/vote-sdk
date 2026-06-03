# Changelog

All notable changes to this repository will be documented in this file.

Historical changes before commit `704b202e2088b91caeaf2290cef85e4a9a759542` are untracked.

## Unreleased

- Preserve DKG coefficient files through speculative ceremony ack proposals so
  validators can retry `MsgAckExecutiveAuthorityKey` if CometBFT advances to a
  later round before their proposal commits. Coefficients are now cleaned up only
  after committed state moves the round past the ceremony ack window.

## v0.9.7 - 2026-05-28

### Changed

- Enable the explorer uptime view in the deployed and local Ping.pub explorer configuration.
- Hide the governance view from the deployed and local Ping.pub explorer configuration.
- Retain failed share witness data until the normal purge path removes expired rounds, preserving rescue and export data after permanent failures.
