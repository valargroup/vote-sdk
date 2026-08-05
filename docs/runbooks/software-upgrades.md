# Software Upgrade Runbook

The chain uses `x/upgrade` for coordinated state-breaking upgrades. The module
is not exposed directly to operators: raw `cosmos.upgrade` tx messages are not
allowlisted, and upgrade tx CLI generation is disabled. Coordinators schedule
or cancel plans through `x/vote` coordinator actions.

Routine binary swaps that preserve state still use `sdk-chain-deploy.yml`.

Validator hosts installed through `join.sh` should use **Cosmovisor pre-staging**
via `update_chain.sh` so the binary switches deterministically at the scheduled
halt height without manual restart coordination.

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
  --mode migrate --plan-name <plan-name> --tag <tag>
```

## Ironwood verifier cutover

Use the `v1.1.0` plan for the production Ironwood verifier binary so the plan
matches the release tag. Staging already applied `ironwood-v1`; that handler
remains registered for staging history, while production uses `v1.1.0`.
The earlier `v1` plan has also been applied and must not be reused. The Ironwood
handlers do not migrate stores; they coordinate the consensus-sensitive verifier
switch.

The running pre-Ironwood binary schedules the plan and reaches the halt height.
Before then, stage the target release whose `svoted` binary registers the
`v1.1.0` handler. Cosmovisor starts that binary at the halt and the target
binary applies the handler.

Use the tag-scoped updater to pre-stage the final release:

```bash
TAG=v1.1.0
curl -fsSL "https://shielded-vote.nyc3.digitaloceanspaces.com/scripts/upgrade/${TAG}/update_chain.sh" | sudo bash -s -- \
  --mode prepare --plan-name v1.1.0 --tag "${TAG}"
```

## Held release and promotion

Set the repository variable `RELEASE_HOLD_TAG` to a stable tag before pushing
that tag when operators need to pre-stage a coordinated release. The release
workflow still publishes the GitHub release and every tag-scoped artifact, but
it does not mark the release Latest or update `version.txt` and the unversioned
installer scripts.

After the coordinated upgrade is complete, run the **Promote release** workflow
with that exact tag. It verifies the held stable release and its tag-scoped
artifacts, updates the mutable DigitalOcean Spaces pointers, marks the GitHub
release Latest, verifies both channels, and clears `RELEASE_HOLD_TAG`.

Promotion changes what future unversioned downloads resolve to. It does not
install binaries or restart running validators.

## Scheduling a state-breaking upgrade

To schedule the halt height, a current coordinator proposes a coordinator
action. It executes immediately when the threshold is 1, or after enough
current coordinators approve it:

```bash
svoted tx vote schedule-upgrade <name> <height> \
  --info '{"tag":"v1.2.3","notes":"state-breaking upgrade"}' \
  --from <vote-manager-key> \
  --chain-id svote-1
```

If another plan already exists, the tx is rejected unless the caller explicitly
allows replacement:

```bash
svoted tx vote schedule-upgrade <name> <height> \
  --info '{"tag":"v1.2.4"}' \
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
  no residual `svoted`/`cosmovisor` process for the same home.
- **Double-sign controls**: require `priv_validator_state.json`, print consensus
  pubkey fingerprint, and require explicit single-signer acknowledgment
  (`SVOTE_ACK_SINGLE_SIGNER=1` or interactive `YES`).
- **Plan-name strictness**: staged directory must match scheduled plan name unless
  `--allow-no-plan` is explicitly set for early staging.

Modes:

| Mode | Purpose |
|------|---------|
| `prepare` | Stage genesis + upgrade binaries only |
| `migrate` | Stage, then migrate systemd service to Cosmovisor |
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
