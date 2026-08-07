# Software Upgrade Runbook

The chain uses `x/upgrade` for coordinated state-breaking upgrades. The module
is not exposed directly to operators: raw `cosmos.upgrade` tx messages are not
allowlisted, and upgrade tx CLI generation is disabled. Coordinators schedule
or cancel plans through `x/vote` coordinator actions.

Routine binary swaps that preserve state still use `sdk-chain-deploy.yml`.

Validator hosts installed through `join.sh` use Cosmovisor with binary downloads
enabled and checksums required. Pre-staging through `update_chain.sh` removes the
network dependency at the halt, while the automatic path remains available for
future checksum-pinned plans.

## First rollout with x/upgrade

The first release that adds `x/upgrade` must use **Reset SDK Chain**, not a
state-preserving deploy. Adding the `upgrade` KV store is itself a store/state
change, and existing live state does not contain that store.

## Validator upgrade model

```text
Coordinator schedules plan (name + height)
        ↓
Validators run update_chain.sh --mode prepare (stage-only; no service stop)
        ↓
Validators run update_chain.sh --mode verify-prestage (PASS/FAIL checklist)
        ↓
At halt height Cosmovisor switches using upgrade-info.json
        ↓
Post-upgrade health checks
```

Fresh Linux installs bootstrap Cosmovisor by default through `join.sh`:

```bash
curl -fsSL https://setup.valargroup.org/ | bash -s -- --env prod
```

Existing direct-mode installs migrate once:

```bash
curl -fsSL https://shielded-vote.nyc3.digitaloceanspaces.com/update_chain.sh | sudo bash -s -- \
  --mode migrate --plan-name <plan-name> --tag <tag> --chain-api <chain-rest-url>
```

`--chain-api` lets migration verify scheduled and previously applied plans even
when a partial migration has left the local RPC offline. Its network ID must
match the validator's local genesis.

Existing Cosmovisor installs enable the same guarded defaults once:

```bash
curl -fsSL https://shielded-vote.nyc3.digitaloceanspaces.com/update_chain.sh | sudo bash -s -- \
  --mode configure-autodownload --plan-name <plan-name> --tag <tag> --chain-api <chain-rest-url>
```

## v1.2.0 coordinated cutover

Staging already applied `ironwood-v1`, while production did not apply the
`v1.1.0` plan. Production skips that plan and picks up the Ironwood verifier
changes together with the delegation-validation changes in `v1.2.0`. The
historical `v1`, `ironwood-v1`, and `v1.1.0` handlers remain registered but
must not be reused.

The delegation message and signing-digest changes alter which transactions a
validator accepts, so this is not a routine rolling deployment. Both networks
must switch binaries through a coordinated halt. The handlers do not migrate
stores.

Use a distinct plan for the testnet release-candidate rehearsal, then use the
final plan on both networks:

| Network | Release tag | Plan name |
|---------|-------------|-----------|
| Testnet rehearsal | `v1.2.0-rc.1` | `v1.2.0-rc.1` |
| Testnet final | `v1.2.0` | `v1.2.0` |
| Mainnet final | `v1.2.0` | `v1.2.0` |

Before scheduling the final mainnet plan, cancel any pending `v1.1.0` plan.
Do not leave both operations until the old halt height is near.

Use the tag-scoped updater to pre-stage the matching release on every validator.
`--allow-no-plan` is required while no plan is scheduled:

```bash
TAG=v1.2.0-rc.1
PLAN_NAME="${TAG}"
curl -fsSL "https://shielded-vote.nyc3.digitaloceanspaces.com/scripts/upgrade/${TAG}/update_chain.sh" | sudo bash -s -- \
  --mode prepare --plan-name "${PLAN_NAME}" --tag "${TAG}" --allow-no-plan
```

After scheduling, rerun `verify-prestage` without `--allow-no-plan` so the
staged layout is checked against the on-chain name and height.

## Held release and promotion

Set the repository variable `RELEASE_HOLD_TAG` to a stable tag before pushing
that tag when operators need to pre-stage a coordinated release. The release
workflow still publishes the GitHub release and every tag-scoped artifact, but
it does not mark the release Latest or update `version.txt` and the unversioned
installer scripts.

After the coordinated upgrade is complete, run the **Promote release** workflow
with that exact tag. It verifies the held stable release and its tag-scoped
artifacts, updates the mutable DigitalOcean Spaces pointers, marks the GitHub
release Latest, and verifies both channels. `RELEASE_HOLD_TAG` can remain as an
audit marker because it affects only that exact tag; replace it before the next
coordinated release. Promotion refuses to replace a newer stable release that
has since become Latest; rerunning the current Latest tag remains safe.

Before promotion, verify only the held release's immutable artifacts:

```bash
DO_BASE="${SVOTE_DO_SPACES_BASE:-https://shielded-vote.nyc3.digitaloceanspaces.com}"
scripts/verify_upgrade_release_artifacts.sh --tag-scoped-only "$TAG" "$DO_BASE"
```

After promotion, rerun the command without `--tag-scoped-only` to verify
`version.txt` and the unversioned installer scripts too.

For an ordinary stable release, GitHub publishes the release without changing
Latest, updates and verifies the mutable Spaces pointers, and only then marks
the release Latest. Rerunning the current Latest tag preserves that status while
the pointers are republished.

Promotion changes what future unversioned downloads resolve to. It does not
install binaries or restart running validators.

## Scheduling a state-breaking upgrade

To schedule the halt height, a current coordinator proposes a coordinator
action. It executes immediately when the threshold is 1, or after enough
current coordinators approve it:

```bash
TAG=v1.2.3
RELEASE_BASE="https://github.com/valargroup/vote-sdk/releases/download/${TAG}"
AMD64_ASSET="shielded-vote-${TAG}-cosmovisor-v1-linux-amd64.tar.gz"
ARM64_ASSET="shielded-vote-${TAG}-cosmovisor-v1-linux-arm64.tar.gz"

if
  AMD64_CHECKSUM=$(curl -fsSL "${RELEASE_BASE}/${AMD64_ASSET}.sha256") &&
  ARM64_CHECKSUM=$(curl -fsSL "${RELEASE_BASE}/${ARM64_ASSET}.sha256") &&
  AMD64_SHA256=$(printf '%s\n' "$AMD64_CHECKSUM" | awk 'NR == 1 {print $1}') &&
  ARM64_SHA256=$(printf '%s\n' "$ARM64_CHECKSUM" | awk 'NR == 1 {print $1}') &&
  printf '%s\n' "$AMD64_SHA256" | grep -Eq '^[0-9a-fA-F]{64}$' &&
  printf '%s\n' "$ARM64_SHA256" | grep -Eq '^[0-9a-fA-F]{64}$' &&
  UPGRADE_INFO=$(jq -nc \
    --arg tag "$TAG" \
    --arg amd64 "${RELEASE_BASE}/${AMD64_ASSET}?checksum=sha256:${AMD64_SHA256}" \
    --arg arm64 "${RELEASE_BASE}/${ARM64_ASSET}?checksum=sha256:${ARM64_SHA256}" \
    '{tag: $tag, binaries: {"linux/amd64": $amd64, "linux/arm64": $arm64}}') &&
  printf '%s\n' "$UPGRADE_INFO" | jq -e . >/dev/null
then
  printf '%s\n' "$UPGRADE_INFO" | jq &&
    svoted tx vote schedule-upgrade <name> <height> \
      --info "$UPGRADE_INFO" \
      --from <vote-manager-key> \
      --chain-id svote-1
else
  printf 'ERROR: release checksums or upgrade info are invalid; upgrade was not scheduled.\n' >&2
  false
fi
```

Do not schedule a tag-only plan. Cosmovisor can download a missing binary only
when `info.binaries` contains the validator's platform and the URL includes its
SHA-256 checksum. The Upgrades page's **Load from release** action constructs
and validates the same entries before signing.

If another plan already exists, the tx is rejected unless the caller explicitly
allows replacement. After changing `TAG`, rebuild `UPGRADE_INFO` with the block
above before replacing the plan:

```bash
svoted tx vote schedule-upgrade <name> <height> \
  --info "$UPGRADE_INFO" \
  --replace-existing \
  --from <vote-manager-key> \
  --chain-id svote-1
```

There is no extra lead-time guard in `x/vote`. The underlying `x/upgrade`
keeper rejects past heights. Scheduling for the current block is accepted by
the keeper but effectively halts on the next preblock because the current
preblock has already run.

Inspect the scheduled plan through the query path:

```bash
svoted query upgrade plan --home ~/.svoted
```

Cancel the current plan through the same coordinator action flow:

```bash
svoted tx vote cancel-upgrade \
  --from <vote-manager-key> \
  --chain-id svote-1
```

## Implementing the future binary

For each future state-breaking release:

1. Add a named handler in `app.RegisterUpgradeHandlers`.
2. Use the exact same name in the vote-manager scheduled plan.
3. If stores are added, renamed, or deleted, read the dumped upgrade-info file
   before `app.Load` and install an `UpgradeStoreLoader` for that plan.
4. Release the new binary.
5. Schedule the plan from the old binary.
6. Validators pre-stage the new binary under
   `~/.svoted/cosmovisor/upgrades/<plan-name>/bin/svoted`.
7. At the halt height, Cosmovisor switches automatically. Nodes without the
   matching handler halt with the `UPGRADE "<name>" NEEDED` message.

The completed handler records the applied plan in `x/upgrade`, so later queries
can confirm the upgrade height:

```bash
svoted query upgrade applied <plan-name> --home ~/.svoted
```

## Validator operator checklist

### T-24h to T-1h (pre-stage)

1. Confirm scheduled plan name and height:

   ```bash
   svoted query upgrade plan --home ~/.svoted
   ```

2. Stage binaries without stopping the running validator:

   ```bash
   curl -fsSL https://shielded-vote.nyc3.digitaloceanspaces.com/update_chain.sh | sudo bash -s -- \
     --mode prepare \
     --plan-name <plan-name> \
     --tag <release-tag>
   ```

   Or from a repo checkout:

   ```bash
   sudo scripts/update_chain.sh --mode prepare --plan-name <plan-name> --tag <release-tag>
   ```

3. Verify pre-stage readiness (staging + service checks):

   ```bash
   sudo scripts/update_chain.sh --mode verify-prestage \
     --plan-name <plan-name> --tag <release-tag>
   ```

   For direct-mode hosts that staged but have not migrated yet, use
   `--skip-cosmovisor-service` to validate binaries only.

4. Confirm Cosmovisor layout:

   ```bash
   ls -l ~/.svoted/cosmovisor/genesis/bin/svoted
   ls -l ~/.svoted/cosmovisor/upgrades/<plan-name>/bin/svoted
   ```

### One-time migration from direct join.sh service

Run only once per host to move from direct `svoted` wrapper service to
Cosmovisor-managed startup:

```bash
export SVOTE_ACK_SINGLE_SIGNER=1   # required for non-interactive runs
curl -fsSL https://shielded-vote.nyc3.digitaloceanspaces.com/update_chain.sh | sudo bash -s -- \
  --mode migrate \
  --plan-name <plan-name> \
  --tag <release-tag>
```

Migration requires the target binary to be staged and validated first. It then
stops the service and switches the host to Cosmovisor.

### At/after halt height

1. Watch logs:

   ```bash
   journalctl -u svoted -f
   ```

2. Confirm node resumes and applied upgrade:

   ```bash
   svoted status --home ~/.svoted
   svoted query upgrade applied <plan-name> --home ~/.svoted
   ```

3. Confirm validator remains bonded:

   ```bash
   svoted query staking validators --home ~/.svoted --output json | jq '.validators[] | select(.status=="BOND_STATUS_BONDED") | .description.moniker'
   ```

### Post-upgrade

- Share `verify-prestage` PASS output with coordinators if requested.
- Keep staged upgrade directory until the plan is applied.
- Do not restore old `priv_validator_state.json` backups.

## Script guardrails

`update_chain.sh` auto-detects `SVOTE_HOME`, `SVOTE_INSTALL_DIR`, and service
`User=` from `/etc/systemd/system/svoted.service` when run with `sudo`. Explicit
`--home` / `--install-dir` override detection.

Cosmovisor is downloaded only from the official
[cosmos-sdk Cosmovisor GitHub releases](https://github.com/cosmos/cosmos-sdk/releases?q=cosmovisor&expanded=true)
with SHA256SUMS verification. Validators need outbound HTTPS to `github.com` at
join and upgrade time.

`update_chain.sh` enforces:

- **Stage-first no-touch policy**: download/checksum/layout validation completes
  before any `systemctl stop`.
- **Fail-closed**: staging or config failure aborts with a clear error and leaves
  the running validator untouched.
- **Signer-stop gate** (migrate mode only): stop service, verify inactive, verify
  no residual `svoted`/`cosmovisor` process for the same home, including a
  manually started `svoted` that relies on its default home.
- **Double-sign controls**: require `priv_validator_state.json`, then require one
  Cosmovisor supervisor and one `svoted` child in the service cgroup. Any signer
  outside that cgroup fails the migration and leaves the service stopped.
- **Plan-name strictness**: staged directory must match scheduled plan name unless
  `--allow-no-plan` is explicitly set for early staging.
- **Stale-plan recovery**: an old `data/upgrade-info.json` is archived only when
  its name and height exactly match the chain's applied-plan response. Current,
  malformed, unknown, or mismatched markers fail closed.
- **Checksum-required auto-download**: migrated services set both
  `DAEMON_ALLOW_DOWNLOAD_BINARIES=true` and
  `DAEMON_DOWNLOAD_MUST_HAVE_CHECKSUM=true`.

Modes:

| Mode | Purpose |
|------|---------|
| `prepare` | Stage genesis + upgrade binaries only |
| `migrate` | Migrate a previously staged direct systemd service to Cosmovisor |
| `configure-autodownload` | Guard the current signer, enable checksum-required downloads, and restart |
| `verify-prestage` | Read-only PASS/FAIL checklist (staging + service sections) |

Use `--skip-cosmovisor-service` with `verify-prestage` to validate staged
binaries on hosts that have not run `--mode migrate` yet.

## Validation program

Before rolling coordinated upgrades to production validators, follow the gated
checklist in [upgrade-validation-checklist.md](upgrade-validation-checklist.md).
Post-release artifact smoke checks: `scripts/verify_upgrade_release_artifacts.sh`.

## Troubleshooting

| Symptom | Likely cause | Action |
|---------|--------------|--------|
| `UPGRADE "<name>" NEEDED at height ...` persists | Missing/incorrect staged binary | Re-run `--mode prepare` with exact plan name |
| `Scheduled plan name mismatch` | Wrong `--plan-name` | Match `svoted query upgrade plan` exactly |
| `priv_validator_state.json is missing` | Data dir incomplete | Restore from backup or snapshot reset script; do not proceed |
| Service restart loop after migrate | Bad unit env or cosmovisor path | Check `journalctl -u svoted`; restore unit backup under `/etc/systemd/system/svoted.service.bak.*` |
| Cosmovisor requests an old plan such as `v1` | Direct-mode history left a stale applied-plan marker | Re-run the current migrate instructions with `--chain-api`; do not delete the marker or start `svoted` manually |
| Checksum mismatch | Corrupted download | Retry; verify tag exists in Spaces/GitHub release |

## Rollback policy

- Do **not** attempt automatic chain-state rollback from the updater script.
- Service/unit rollback is local-process safe only (restore `.bak` unit file).
- State-breaking failures require coordinated cancel/new plan from vote managers.

## Current Testnet Funding Migration

The module-funded authorized-send rollout adds one testnet only upgrade handler
named `stage-vote-funding-module`. On `svote-1`, it moves existing native
`usvote` balances from the current vote-manager addresses into the shared
`vote_funding` module account, up to the fresh genesis funding pool size. It
does not mint new supply. On any other chain ID, the handler is a no-op.
