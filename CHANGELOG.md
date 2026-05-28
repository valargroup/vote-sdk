# Changelog

All notable changes to this repository will be documented in this file.

Historical changes before commit `704b202e2088b91caeaf2290cef85e4a9a759542` are untracked.

## Unreleased

### Changed

- Confirm helper share submissions against committed chain state before marking them submitted, retrying timed-out confirmations through the normal helper backoff path.
- Retain failed share witness data until the normal purge path removes expired rounds, preserving rescue and export data after permanent failures.
