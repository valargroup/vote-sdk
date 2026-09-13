# Coordinated upgrade validation: production v1.4.0 → v1.6.0

Validate locally before publishing a candidate. This checklist does not authorize
production or staging preparation, migration, scheduling, resets, or deployment.
Use the existing `v1.6.0` handler; candidate release tags remain `v1.6.0-rc.N`.

## Baseline and artifacts

Record live versions through read-only REST queries, and pin the SDK and
vote-infrastructure commits used for the test. At the September 12, 2026 check,
production was v1.4.0; both staging validators were v1.6.0-rc.5 and had already
applied `v1.6.0`. Neither chain had a pending plan. Do not schedule that plan again
on staging.

For each candidate:

- Run the upgrade shell tests, including `test_chain_upgrade_artifacts.sh`.
- Verify the rendered updater matches its template and embedded helper digest.
- Run `python3 scripts/test-static-pages.py` in vote-infrastructure.
- Build real FFI binaries. Mock verifiers are insufficient for acceptance.
- Verify full-release and Cosmovisor archives contain identical executables.

## Local Linux rehearsal

The dedicated runner uses Docker Compose, real systemd and Cosmovisor, two
validators with equal voting power, observer nodes installed by `join-full.sh`,
and a fresh validator installed by `join.sh` after cutover.
The chain ID is `upgrade-test-1`; keys, genesis, voting configuration, and snapshots
are generated locally. Runtime containers have an internal-only Docker network.
Downloads of published artifacts and build dependencies happen before isolation.

Start Docker and build the runtime. Use Ubuntu 24.04 for ARM64 (existing published
ARM64 releases require it); use Ubuntu 22.04 for AMD64 production compatibility.

```bash
docker build -t svote-upgrade-systemd:local docker/upgrade
# On native Linux AMD64 instead:
# docker build --build-arg UBUNTU_VERSION=22.04 -t svote-upgrade-systemd:local docker/upgrade

# Control: published application binaries, current scripts, no new release.
python3 scripts/test_upgrade_localnet.py \
  --infra ../vote-infrastructure \
  --output /tmp/upgrade-control
```

Build a candidate and the existing real-proof test executable. The version below
is a local build label, not a request to create a Git tag:

```bash
docker build --platform linux/arm64 --target e2e-export \
  -f docker/upgrade/Dockerfile.build \
  --build-arg VERSION=v1.6.0-rc.999 \
  --build-arg COMMIT="$(git rev-parse HEAD)" \
  --output type=local,dest=/tmp/upgrade-candidate .

python3 scripts/test_upgrade_localnet.py \
  --infra ../vote-infrastructure \
  --candidate-tag v1.6.0-rc.999 \
  --candidate-binary /tmp/upgrade-candidate/svoted \
  --proof-binary /tmp/upgrade-candidate/atomic-delegate-cast \
  --scenario prestage --output /tmp/upgrade-prestage

python3 scripts/test_upgrade_localnet.py \
  --infra ../vote-infrastructure \
  --candidate-tag v1.6.0-rc.999 \
  --candidate-binary /tmp/upgrade-candidate/svoted \
  --proof-binary /tmp/upgrade-candidate/atomic-delegate-cast \
  --scenario autodownload --output /tmp/upgrade-autodownload
```

Use `--platform linux/amd64` with an AMD64 runtime image and binary for the
production-platform run. The build supports cross-compiling the node binary;
real-proof test executables run on their build platform. Native AMD64 CI is
available through vote-infrastructure’s **Test coordinated upgrade locally**,
with an explicit matching SDK ref. Running in the private infrastructure repository
lets the workflow read its own source without adding cross-repository secrets. That workflow has read-only repository permissions and
no publishing or deployment steps.

The runner requires a new output directory and records `result.json` plus logs.
It removes only its own containers by default. `--keep` retains that run for
inspection; the generated Compose project name identifies its resources.

## Required outcomes

- Repeated preparation does not restart services or change the active binary.
- Legacy migration restarts the old binary, retains service configuration, and
  produces exactly one managed supervisor and one child per node.
- Verification rejects altered binaries, wrong plan/tag/platform/checksum,
  unavailable plan queries, and expired heights. `--allow-no-plan` accepts only
  a successful query showing no plan.
- A coordinator transaction creates state before the cutover, and that state
  survives it on every node.
- After scheduling, all nodes pass verification without `--allow-no-plan`.
- The pre-staged case resumes with the artifact server stopped at the halt.
- The download case starts with no target directory and obtains the pinned
  archive automatically. An existing partial target directory is a separate
  failure case: Cosmovisor refuses to overwrite it.
- All nodes record the same applied height, execute the intended binary hash,
  agree on app hashes at a common height, and produce at least 20 more blocks.
- Restarting the services preserves progress. A new observer restores a locally
  generated post-upgrade snapshot and catches up using Cosmovisor.
- The real-proof atomic delegation/cast/helper/tally test passes on the candidate.
- A fresh validator appears in the join queue, receives coordinator funding, and
  bonds through the actual installed wrapper. Locally built releases must include
  `create-val-tx` beside `svoted`; the supplied Docker build exports both.
  Use a post-upgrade snapshot with the new binary. Replaying from genesis requires
  the historical binary sequence; a new binary alone cannot cross the old halt.

Retain failure evidence. Never make a test pass by resetting signer state,
starting a second signer, skipping checksums, or contacting a live chain.

## Optional published prerelease smoke test

Only after the local gates pass, publish the next unused `v1.6.0-rc.N` from the
validated commit through the artifact-only Release workflow. Its commit must
satisfy the existing release-branch policy.

Verify tag-scoped scripts and archives, then repeat the local rehearsal using
those published binaries. Verify GitHub Latest, `version.txt`, and unversioned
scripts remain unchanged. Do not publish stable v1.6.0, promote a release, merge
infrastructure changes that auto-publish setup pages, or prepare a live node as
part of this validation phase.

## Subsequent rollout

Live rollout requires a separate operator decision after reviewing the evidence.
Production has one Valargroup primary and external operators, not a Valargroup
secondary. Inventory every validator and snapshot/archive node before choosing
an eventual halt height. Stage already applied `v1.6.0`; further stage binary
validation must respect that history rather than reuse its applied plan.
