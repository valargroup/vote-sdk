# Validator Upgrade Path Validation Checklist

Gated validation program for Cosmovisor pre-staging and coordinated `x/upgrade`
halts. **Do not advance to the next stop point without explicit operator
approval.**

Related runbooks: [software-upgrades.md](software-upgrades.md),
[join-chain.md](join-chain.md), [genesis-setup.md](genesis-setup.md).

## Stop Point 0 — Pre-merge / pre-release gate

**Gate:** All items PASS before merging PR and cutting a release tag.

### Repository alignment (local)

```bash
# From vote-sdk repo root on the upgrade PR branch
bash -n scripts/_chain_upgrade_common.sh scripts/update_chain.sh scripts/svoted-wrapper.sh join.sh
scripts/render-update-chain.sh \
  v0.11.0 \
  https://shielded-vote.nyc3.digitaloceanspaces.com \
  https://shielded-vote.nyc3.digitaloceanspaces.com/scripts/upgrade/v0.11.0/_chain_upgrade_common.sh \
  https://shielded-vote.nyc3.digitaloceanspaces.com/scripts/upgrade/v0.11.0/update_chain.sh \
  | bash -n
diff <(scripts/render-update-chain.sh \
         latest \
         https://shielded-vote.nyc3.digitaloceanspaces.com \
         https://shielded-vote.nyc3.digitaloceanspaces.com/scripts/_chain_upgrade_common.sh \
         https://shielded-vote.nyc3.digitaloceanspaces.com/update_chain.sh) \
  scripts/update_chain.sh
```

| Check | PASS | FAIL |
|-------|------|------|
| All `bash -n` succeed | | |
| Template substitutes to match `update_chain.sh` (diff empty) | | |
| PR CI green (join.sh smoke, unit/integration tests) | | |

### Release workflow publishes upgrade scripts

Confirm [`.github/workflows/release.yml`](../../.github/workflows/release.yml) uploads tag-scoped copies of:

- `update_chain.sh` (from template + tag substitution)
- `scripts/_chain_upgrade_common.sh`
- `prepare-upgrade-artifacts.sh`

| Check | PASS | FAIL |
|-------|------|------|
| `release.yml` `distribute` job uploads all three | | |
| Stable releases also update the shared copies | | |
| RC releases leave shared copies unchanged | | |

### join.sh upgrade modes

| Check | PASS | FAIL |
|-------|------|------|
| Linux default is `cosmovisor` when `--upgrade-mode` omitted | | |
| `--upgrade-mode direct` accepted (legacy direct-mode mimic) | | |
| macOS default remains `direct` | | |

Quick grep:

```bash
grep -A2 'SVOTE_UPGRADE_MODE' join.sh | head -20
```

### Runbook ↔ script alignment

| Check | PASS | FAIL |
|-------|------|------|
| [software-upgrades.md](software-upgrades.md) documents `prepare`, `migrate`, `verify-prestage` | | |
| Documents `--skip-cosmovisor-service` for direct-mode staging-only verify | | |
| Documents GitHub egress for Cosmovisor | | |
| Documents systemd autodetect when `update_chain.sh` run as root | | |

**STOP — operator sign-off before merge + tag.**

---

## Stop Point 1 — Merge, tag, publish

**Gate:** Published artifacts verified before any VM work.

### Actions

1. Merge upgrade PR to `main`.
2. Cut `vN.N.N` or `vN.N.N-rc.N` (move [CHANGELOG.md](../../CHANGELOG.md) Unreleased → a dated section for a stable release).
3. Wait for GitHub **Release** workflow to complete.

### Post-release artifact verification

```bash
TAG=v0.11.0   # replace with actual tag
DO_BASE="${SVOTE_DO_SPACES_BASE:-https://shielded-vote.nyc3.digitaloceanspaces.com}"
scripts/verify_upgrade_release_artifacts.sh "$TAG" "$DO_BASE"
```

| Check | PASS | FAIL |
|-------|------|------|
| GitHub Release exists for tag | | |
| Tag-scoped `update_chain.sh` fetchable; `--help` works | | |
| Tag-scoped `_chain_upgrade_common.sh` fetchable | | |
| Tag-scoped `prepare-upgrade-artifacts.sh` fetchable | | |
| `shielded-vote-${TAG}-linux-amd64.tar.gz` + `.sha256` verify | | |
| Tag-scoped `update_chain.sh` default `--tag` matches tag | | |
| Stable only: `version.txt` and shared scripts match tag | | |
| RC only: shared release pointers remain unchanged | | |

**STOP — operator sign-off before provisioning isolated network.**

---

## Stop Point 2 — Separate-network bring-up

**Goal:** Two fresh DO instances, isolated from stage/prod endpoints. Secondary
uses **direct** join mode to mimic legacy validators.

### Hardware

Production target per [join-chain.md](join-chain.md): `linux-amd64`, 4 vCPU,
8 GB RAM, 120 GB NVMe.

### Primary (genesis + local admin)

Bootstrap from genesis on a **new chain ID** (e.g. `upgrade-test-1`) — do not
reuse `svote-1` or `zvote-1` voting-config URLs.

Suggested local-dev path (single host or primary VM):

```bash
# On primary — from repo checkout at release tag
CHAIN_ID=upgrade-test-1 SVOTE_HOME=~/.svoted-primary ./scripts/init.sh
# Enable admin UI in systemd unit (see genesis-setup.md primary.conf pattern)
```

Record for joiners:

- Primary REST: `http://<primary-ip>:1317` or TLS URL if Caddy configured
- P2P: `<node_id>@<primary-ip>:26656`
- Admin API: `http://<primary-ip>:1317` (in-process admin on primary)
- Genesis URL: `http://<primary-ip>:1317/shielded-vote/v1/genesis`

Publish a **local-only** voting-config JSON (file or temporary host) with
`vote_servers[0].url` pointing at the primary REST base — **not**
`voting.valargroup.org`.

### Secondary (direct-mode join)

Export the isolated-network settings before running the tag-scoped join script.
`SVOTE_RELEASE_VERSION` is what pins the binary; the script path alone does not.

```bash
export SVOTE_CHAIN_ID=upgrade-test-1
export VOTING_CONFIG_URL=https://<your-isolated-config>/dynamic-voting-config.json
export SVOTE_ADMIN_URL=http://<primary-ip>:1317
export SVOTE_DO_SPACES_BASE="${DO_BASE}"
export SVOTE_RELEASE_VERSION="${TAG}"
export SVOTE_SKIP_SNAPSHOT=1
export SVOTE_MONIKER=upgrade-test-secondary

curl -fsSL "${DO_BASE}/scripts/join/${TAG}/join.sh" | bash -s -- \
  --upgrade-mode direct \
  --tls-mode skip \
  --env prod
```

Pre-flight isolation check on secondary **before** join:

```bash
scripts/verify_isolated_join_env.sh \
  --voting-config-url "$VOTING_CONFIG_URL" \
  --admin-url "$SVOTE_ADMIN_URL" \
  --chain-id "$SVOTE_CHAIN_ID"
```

| Check | PASS | FAIL |
|-------|------|------|
| `verify_isolated_join_env.sh` reports no stage/prod coupling | | |
| Secondary `systemctl cat svoted` shows `SVOTE_UPGRADE_MODE=direct` | | |
| Secondary syncs (`catching_up=false`) | | |
| Secondary appears in primary admin Join queue | | |

### Third-node join probe (optional)

From a third short-lived VM, repeat join with same isolated overrides and a
different moniker. Confirms discoverability is not hard-coded to stage/prod.

| Check | PASS | FAIL |
|-------|------|------|
| Third node reaches sync against isolated primary | | |

**STOP — operator sign-off before admin approval and upgrade rehearsal.**

---

## Stop Point 3 — Separate-network upgrade rehearsal

**Prerequisite:** The target release includes a registered upgrade handler for
the test plan name (see `app/upgrades.go`). The running binary schedules the
plan; the target binary applies the handler after Cosmovisor switches.

### 3a — Admin approval (operator)

1. Open primary admin UI (local).
2. Approve/fund secondary validator from Join queue.
3. Confirm secondary bonds (`BOND_STATUS_BONDED`).

### 3b — Pre-stage both nodes

On **each** validator (primary if also validating, secondary):

```bash
TAG=<release-tag>
PLAN=<upgrade-plan-name>
DO_BASE=https://shielded-vote.nyc3.digitaloceanspaces.com
UPDATER_URL="${DO_BASE}/scripts/upgrade/${TAG}/update_chain.sh"

curl -fsSL "${UPDATER_URL}" | sudo bash -s -- \
  --mode prepare \
  --plan-name "${PLAN}" \
  --tag "${TAG}"
```

Secondary still on **direct** mode — verify staging only first:

```bash
curl -fsSL "${UPDATER_URL}" | sudo bash -s -- \
  --mode verify-prestage \
  --plan-name "${PLAN}" \
  --tag "${TAG}" \
  --skip-cosmovisor-service
```

Migrate secondary to Cosmovisor before halt (one-time):

```bash
export SVOTE_ACK_SINGLE_SIGNER=1
curl -fsSL "${UPDATER_URL}" | sudo bash -s -- \
  --mode migrate \
  --plan-name "${PLAN}" \
  --tag "${TAG}"
```

Full verify (service checks enabled):

```bash
curl -fsSL "${UPDATER_URL}" | sudo bash -s -- \
  --mode verify-prestage \
  --plan-name "${PLAN}" \
  --tag "${TAG}"
```

| Check | PASS | FAIL |
|-------|------|------|
| `prepare` completes without stopping running validator | | |
| `verify-prestage` staging section PASS on both nodes | | |
| After migrate, service section PASS (`SVOTE_UPGRADE_MODE=cosmovisor`) | | |
| Layout: `~/.svoted/cosmovisor/upgrades/${PLAN}/bin/svoted` exists | | |

### 3c — Schedule test upgrade (coordinator / admin UI)

Schedule plan via coordinator action or admin UI. Record plan name and halt height.

```bash
svoted query upgrade plan --home ~/.svoted
```

### 3d — Halt height

```bash
journalctl -u svoted -f
# at height:
svoted query upgrade applied "${PLAN}" --home ~/.svoted
svoted status --home ~/.svoted
```

| Check | PASS | FAIL |
|-------|------|------|
| Cosmovisor switch visible in logs | | |
| `query upgrade applied` succeeds | | |
| Nodes resume block production | | |
| Validators remain bonded (if applicable) | | |

**STOP — capture findings; fix code/scripts before stage rehearsal.**

---

## Stop Point 4 — Real stage rehearsal

Repeat Stop Point 3 commands on **stage** validators using real `svote-1`
endpoints. Compare outputs to isolated-network baseline.

| Check | PASS | FAIL |
|-------|------|------|
| Stage `prepare` + `verify-prestage` PASS on test validator(s) | | |
| Scheduled stage plan executes at halt height | | |
| No environment-specific failures vs isolated baseline | | |

**STOP — operator sign-off before production.**

---

## Stop Point 5 — Production rollout

Run only the proven sequence on prod primary + secondary.

**Abort criteria (do not proceed):**

- Any `verify-prestage` FAIL
- Plan name mismatch vs chain
- Missing `priv_validator_state.json`
- Checksum mismatch on staged binary

Sequence:

1. `prepare` on each prod validator
2. `verify-prestage` (with migrate first if still direct-mode)
3. Coordinator-scheduled halt height
4. Post-upgrade: `query upgrade applied`, bonded status, REST health

| Check | PASS | FAIL |
|-------|------|------|
| Prod primary pre-stage PASS | | |
| Prod secondary pre-stage PASS | | |
| Halt-height switch successful | | |
| Post-upgrade health checks PASS | | |

**STOP — final sign-off.**

---

## Risk mitigations

| Risk | Mitigation |
|------|------------|
| New `join.sh` diverges from old direct behavior | Secondary uses `--upgrade-mode direct`; record `systemctl cat svoted` and wrapper logs |
| Accidental stage/prod coupling on isolated net | Run `verify_isolated_join_env.sh` before join |
| Stale or wrong published scripts | Run `verify_upgrade_release_artifacts.sh` after every tag |
| Skipping human gates | Do not start next stop point without explicit approval |

## Direct-mode vs cosmovisor matrix (validation)

| Node role | Join mode | Before halt | verify-prestage flags |
|-----------|-----------|-------------|------------------------|
| Legacy mimic (secondary) | `direct` | `prepare` only | `--skip-cosmovisor-service` until migrated |
| Production target | `cosmovisor` (Linux default) | `prepare` | full checks |
| Post-migrate | `cosmovisor` | `prepare` | full checks |
