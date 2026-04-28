# Production Deployment Guide

## Architecture

```
               ┌────────────────┐
               │  Cloudflare    │
               │  DNS           │
               └───┬────────┬──┘
                   │        │
          ┌────────▼──┐  ┌──▼──────────┐
          │ Droplet 1  │  │ Droplet 2    │
          │ vote-      │  │ vote-        │
          │ primary    │  │ secondary    │
          │            │  │              │
          │ • svoted   │  │ • svoted     │
          │   start    │  │   start      │
          │ • caddy    │  │ • caddy      │
          └─────┬──────┘  └──────┬───────┘
                └────P2P :26656──┘
```

Production uses dedicated DigitalOcean Droplets in the same region + VPC, running native binaries under systemd (no Docker):

- **vote-primary** (`vote-chain-primary.<domain>`): 4 vCPU / 16 GB RAM / 100 GB NVMe. Bootstrap validator — creates genesis via `scripts/init.sh` with `SVOTE_ADMIN_DISABLE=false` so the admin module is enabled. Serves the admin UI at `https://vote-chain-primary.<domain>/` via `--serve-ui --ui-dist` (systemd drop-in). Secondaries keep `[admin] disable = true` from `svoted init`.
- **vote-secondary** (`vote-chain-secondary.<domain>`): 2 vCPU / 8 GB RAM / 50 GB NVMe. Joining validator — fetches genesis, syncs, and self-registers via `scripts/reset-join.sh`.
- **vote-snapshot** (`snapshots.<domain>`): 4 vCPU / 16 GB RAM / 100 GB volume by default. Pruned non-validator node — publishes daily `data/` snapshots and metadata to `s3://vote/snapshots/svote-1/`, while Caddy serves the public snapshot page.

Both nodes run the same `svoted` binary. Caddy on each host terminates TLS via Let's Encrypt.

## Fee and Reward Distribution

`vote-sdk` is operated as a no-fee chain: minimum gas prices default to `0usvote`,
distribution messages are not registered, and validator rewards, commission, and
community-pool accounting are not part of the protocol. The standard Cosmos
`x/distribution` module is intentionally omitted; staking runs without
distribution hooks.

## Prerequisites

- DigitalOcean account with API token
- Cloudflare account with the domain's zone
- Terraform >= 1.5 installed locally (or in CI)
- A published release tag on DO Spaces (produced by `release.yml`)

## Provisioning with Terraform

```bash
cd terraform/
cp terraform.tfvars.example terraform.tfvars
# Edit terraform.tfvars with real values

terraform init
terraform plan
terraform apply
```

This creates:
- VPC in the chosen region
- Two Droplets with block volumes attached
- Firewalls (SSH from admin IPs, P2P, HTTPS)
- Cloudflare DNS records for both hosts

Cloud-init on each Droplet automatically:
1. Installs Caddy from the official apt repo
2. Writes the systemd unit, Caddyfile, and environment file
3. Mounts the block volume
4. Downloads + installs the release binary via `install-release.sh`
5. Enables and starts `svoted` + `caddy`

## Binary Install Flow

Each host runs `install-release.sh` to manage binary upgrades:

```
/opt/shielded-vote/
├── current -> releases/v1.2.3     # symlink, swapped atomically
├── releases/
│   ├── v1.2.3/
│   │   └── bin/svoted
│   └── v1.2.2/
│       └── bin/svoted
├── .svoted/                        # chain data (block volume)
└── install-release.sh
```

The systemd unit `ExecStart` points at `/opt/shielded-vote/current/bin/svoted`, so tag swaps are atomic and require only a `systemctl restart svoted`.

## Workflows

### Deploy (restart binaries)

**`.github/workflows/sdk-chain-deploy.yml`** — manual `workflow_dispatch` with a `tag` input.

1. SSHes to production hosts and runs `install-release.sh --tag <tag>`
2. Verifies chain REST APIs and public frontends respond

Use this for routine upgrades. Chain state is preserved; only the binary is swapped.

```bash
# Or manually on each host:
/opt/shielded-vote/install-release.sh --tag v1.2.3
```

### Reset (from genesis)

**`.github/workflows/sdk-chain-reset.yml`** — manual `workflow_dispatch` with a `tag` input.

1. **reset-primary**: installs tag, stops svoted, wipes chain state, runs `init.sh` (imports `PRIMARY_VAL_PRIVKEY`) to create fresh genesis, starts svoted
2. **upload-genesis**: fetches genesis from primary's REST API, uploads to DO Spaces (`s3://vote/genesis.json`)
3. **fund-secondary**: derives the secondary address from `SECONDARY_VAL_PRIVKEY`, sends 100M usvote from a vote manager on the primary
4. **reset-snapshot**: installs tag, runs `reset-snapshot.sh`, starts a pruned non-validator node, and enables `snapshot.timer`
5. **reset-secondary**: installs tag, runs `reset-join.sh` (imports `SECONDARY_VAL_PRIVKEY`), syncs, verifies funding, registers as validator
6. **verify**: checks validators, archive, and snapshot frontend

Required secrets: `PRIMARY_HOST`, `SECONDARY_HOST`, `SNAPSHOT_HOST`, `DEPLOY_USER`, `SSH_PRIVATE_KEY`, `VM_PRIVKEYS`, `PRIMARY_VAL_PRIVKEY`, `SECONDARY_VAL_PRIVKEY`, `DOMAIN`, `DO_ACCESS_KEY`, `DO_SECRET_KEY`, `SLACK_WEBHOOK_URL`.

## First-Time Bootstrap

After `terraform apply`, run the Reset workflow to initialize the chain:

1. Add GitHub secrets: `PRIMARY_VAL_PRIVKEY` and `SECONDARY_VAL_PRIVKEY` (64-char hex private keys for each validator)
2. Trigger "Reset SDK Chain" with the desired release tag
3. The workflow bootstraps genesis on primary, uploads it, funds the secondary from a vote manager, and joins secondary
4. Verify: `DOMAIN=<your-domain> bash scripts/healthcheck.sh`

## Manual Operations

### Initialize primary from scratch

```bash
ssh root@<primary-ip>
export PATH="/opt/shielded-vote/current/bin:$PATH"
export SVOTED_HOME=/opt/shielded-vote/.svoted
export SVOTE_ADMIN_DISABLE=false
export VM_PRIVKEYS=<64-char-hex>  # comma-separated for multi-VM
export VAL_PRIVKEY=<64-char-hex>  # omit to generate a fresh key
systemctl stop svoted
rm -rf /opt/shielded-vote/.svoted/*
# init.sh sources scripts/_vote_manager_keys_lib.sh; both must be
# present in the same directory (run from a repo checkout, or copy both
# files side-by-side onto the host first).
bash scripts/init.sh
systemctl start svoted
```

### Join secondary manually

```bash
ssh root@<secondary-ip>
export PATH="/opt/shielded-vote/current/bin:$PATH"
export GENESIS_URL=https://vote.fra1.digitaloceanspaces.com/genesis.json
export PRIMARY_REST_URL=https://vote-chain-primary.<domain>
export VAL_PRIVKEY=<64-char-hex>
export HOME_DIR=/opt/shielded-vote/.svoted
bash scripts/reset-join.sh
```

### Roll back to a previous release

```bash
ln -sfn /opt/shielded-vote/releases/v1.2.2 /opt/shielded-vote/current.new
mv -Tf /opt/shielded-vote/current.new /opt/shielded-vote/current
systemctl restart svoted
```

## Useful Commands

```bash
# View service logs
journalctl -u svoted -f
journalctl -u caddy -f

# Check service status
systemctl status svoted
systemctl status caddy

# Check which release is active
ls -la /opt/shielded-vote/current

# Check validator set
svoted query staking validators --home /opt/shielded-vote/.svoted --output json | jq '.validators[].description.moniker'
```

## Failover Runbook

### Primary is down

1. Secondary continues producing blocks if it has enough voting power, but single-validator chains halt without the bootstrap node.
2. SSH to primary: `journalctl -u svoted -f`
3. Common causes: disk full, OOM, consensus panic.
4. Restart: `systemctl restart svoted`.

### Secondary is down

1. Primary continues producing blocks alone.
2. SSH to secondary: `journalctl -u svoted -f`
3. Restart: `systemctl restart svoted`.

## Infrastructure Costs

| Component | SKU | Monthly |
|-----------|-----|---------|
| vote-primary | `s-4vcpu-16gb-amd` | ~$68 |
| vote-secondary | `s-2vcpu-8gb-amd` | ~$36 |
| Block volumes (100 + 50 GB) | DO Volumes | ~$15 |
| **Total** | | **~$119/mo** |
