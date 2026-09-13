# Local upgrade readiness evidence — 2026-09-13

Scope: production v1.4.0 → the existing `v1.6.0` application upgrade handler.
All stateful tests used isolated `upgrade-test-1` Docker localnets. No live node
was prepared, migrated, restarted, reset, or upgraded. No tag, release, workflow,
setup page, or mutable release pointer was published.

## Observed live baseline (read-only)

| Environment | Application | Commit | Pending plan |
| --- | --- | --- | --- |
| Production primary, `zvote-1` | v1.4.0 | `6a10c073baae3c2003ada402d7c5c513ee6b90ce` | None |
| Staging primary and secondary, `svote-1` | v1.6.0-rc.5 | `ce91e16f6f0f6ab57b3a8f751666a8ff8a098196` | None |

Staging previously applied `v1.6.0` at height 8721300. The updater now explicitly
rejects preparation of an already-applied plan, including with `--allow-no-plan`.

## Candidate and infrastructure

The real FFI application was built from main commit
`9c462421eb315a9d931960d33283e025d1fdf594`, with `halo2,redpallas` enabled.
`v1.6.0-rc.6` was used only as a local build label; no such tag was created by
this work. The infrastructure tree tested here is committed as `cc5808b`.
The JSON records preserve the original base commits and hashes of the changed
SDK scripts used by each run.

| Platform | Application SHA-256 | Build result |
| --- | --- | --- |
| Linux ARM64 | `459418c83288283811d332981f57de05d7ad758b5fc4a889b5bc7a799f7b99b6` | Passed |
| Linux AMD64 | `447b4dbe8e7966dc4c96d73a55944eeb1392687c1c34fcbaa3502a98e71c3052` | Passed |

Both builds export `svoted` and `create-val-tx`. Full and Cosmovisor archives are
checksum-verified and required to contain the same application executable.

## Final clean acceptance runs

Both scenarios passed on native Linux ARM64 under real systemd and Cosmovisor.
They use two equal-power validators, an observer, a fresh snapshot full node,
and a fresh validator. There were no manual runtime repairs in these final runs.

| Scenario | Result | Applied height | Passing checks |
| --- | --- | --- | --- |
| prestage | Passed | 97 | 15 |
| autodownload | Passed | 97 | 15 |

Each run verifies repeat preparation without restart, preservation of legacy
settings (including deliberately conflicting environment files), stage-only
installation, strict scheduled-plan checks, halt/switch/resume, identical
executable and app hashes, funded-state continuity, service restart, snapshot
restoration, real-proof delegation/cast/helper/tally, and validator
registration/funding/bonding. The pre-staged case stops the artifact server at
the halt; the download case fetches the checksum-pinned archive at the halt.

Machine-readable results: [prestage](upgrade-evidence/2026-09-13/prestage.json)
and [autodownload](upgrade-evidence/2026-09-13/autodownload.json). Full local logs
remain in `/tmp/svote-upgrade-final-prestage` and
`/tmp/svote-upgrade-final-autodownload`. All test containers were removed after
collecting evidence; the existing unrelated user build container was retained.

## Regression coverage

The offline suites passed for artifact identity/tampering, mismatched
plan/tag/platform, already-applied and expired plans, unavailable/malformed plan
APIs, pinned bootstrap helpers, runtime configuration, stale-marker recovery,
multiple-signer detection, backup policy maintenance, snapshot cleanup, release
channels, and validator wrapper behavior.

Infrastructure tests passed for repeated stage-only installation, missing and
incorrect checksums, conflicting existing releases, legacy-home refusal, snapshot
runtime-version metadata, production/staging page rendering, and copied-command
shell syntax. Actionlint and Terraform formatting checks passed. No Terraform
plan or apply was run.

## Issues found and corrected locally

- Migration previously replaced service settings and operator drop-ins. It now
  preserves them and uses a dedicated launch/runtime override.
- Existing environment files could override download settings. Dedicated runtime
  environment files now enforce the selected checksum/download policy.
- Snapshot cleanup leaked a RETURN trap into later installer steps.
- The validator installer and shared helper reused `SERVICE_PATH` for different
  purposes, producing an invalid executable search path in the service.
- macOS tar metadata polluted Cosmovisor archives; packaging now uses ustar
  without copyfile metadata.
- A download rehearsal must start without the target upgrade directory:
  Cosmovisor refuses to overwrite a partial directory. This is distinct from
  successful pre-staging, which works with the artifact server offline.
- A fresh validator using the post-upgrade binary needs the post-upgrade snapshot;
  replaying from genesis requires the historical binary sequence. The local
  primary must also explicitly enable its otherwise-disabled admin join queue.

Earlier diagnostic runs are retained separately and are not counted as clean
acceptance runs.

## Remaining publication gate

Native AMD64 runtime acceptance remains outstanding. On this ARM64 Mac, the
user-mode emulator crashed even when executing the published v1.4.0 baseline
with a Go `lfstack.push` error. This does not establish whether AMD64 upgrades
pass or fail. No runtime checks were weakened to bypass that failure.

The private vote-infrastructure repository now contains a manual **Test
coordinated upgrade locally** workflow for native Ubuntu AMD64, with an explicit
SDK ref, read-only repository permissions, and no publishing/deployment steps.
Run it against the reviewed commits before publishing a test prerelease. Then
verify the published artifacts and repeat the localnet with those exact files.
