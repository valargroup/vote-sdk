# Infrastructure

## Overview

All backend services run on a single DigitalOcean droplet (`46.101.255.48`) behind a
Caddy reverse proxy that provides automatic HTTPS via Let's Encrypt + sslip.io.

The frontend is deployed to Vercel.

## Network Discovery

Vercel Edge Config is the single entry point for network discovery. The `voting-config` key stores the list of public validator URLs and PIR servers. This is the same data iOS clients and `join.sh` use.

Edge Config is managed through the admin UI. When the bootstrap operator onboards a new validator, they can register its public URL in the admin UI (Validators → Register public URL), which writes to Edge Config. Registration is optional — validators participate in consensus and ceremonies regardless. They just won't be listed as public entry points.

Every validator node serves its own `genesis.json` at `/zally/v1/genesis`. Joining validators fetch genesis from the first discovered node. `network.json` is eliminated — CometBFT's peer exchange (PEX) handles peer discovery after the initial connection.

DO Spaces is used only for binary distribution (automated by `release.yml`).

## Services

| Service | Process | Internal port | Systemd unit | Deploy path |
|---|---|---|---|---|
| Zally chain (REST API) | `zallyd` | 1318 | `zallyd.service` | `/opt/zally-chain` |
| Helper server | embedded in `zallyd` | 1318 | (same as above) | (same as above) |
| Nullifier PIR server | `nf-server` | 3000 | `nullifier-query-server.service` | `/opt/nullifier-ingest` |

## External URLs

Caddy terminates TLS and routes by path:

| Service | External URL | Example endpoint |
|---|---|---|
| Chain REST API | `https://46-101-255-48.sslip.io` | `/zally/v1/rounds` |
| Genesis | `https://46-101-255-48.sslip.io` | `/zally/v1/genesis` |
| Helper server | `https://46-101-255-48.sslip.io` | `/api/v1/status` |
| Nullifier PIR | `https://46-101-255-48.sslip.io/nullifier` | `/nullifier/` (Caddy strips prefix) |
| Frontend (UI) | `https://zally-phi.vercel.app` | — |
| Voting config | `https://zally-phi.vercel.app` | `/api/voting-config` |

## Frontend env vars

```bash
VITE_CHAIN_URL=https://46-101-255-48.sslip.io
VITE_NULLIFIER_URL=https://46-101-255-48.sslip.io/nullifier
```

### Edge Config env vars (Vercel project settings)

```bash
VERCEL_API_TOKEN=...     # Vercel REST API token with Edge Config write access
EDGE_CONFIG_ID=ecfg_...  # ID of the Edge Config store
CHAIN_API_URL=https://46-101-255-48.sslip.io  # For vote-manager verification
```

## Health checks

```bash
# Chain — list rounds
curl -sf https://46-101-255-48.sslip.io/zally/v1/rounds

# Genesis
curl -sf https://46-101-255-48.sslip.io/zally/v1/genesis | jq .chain_id

# Helper server — status
curl -sf https://46-101-255-48.sslip.io/api/v1/status

# Nullifier PIR server
curl -sf https://46-101-255-48.sslip.io/nullifier/health

# Voting config (Edge Config)
curl -sf https://zally-phi.vercel.app/api/voting-config
```

## Ceremony

The EA key ceremony is automatic. When a voting round is created (via the admin UI
or `MsgCreateVotingSession`), the per-round ceremony runs via PrepareProposal:
auto-deal distributes ECIES-encrypted EA key shares to all validators, then
auto-ack confirms once enough validators have acknowledged. No manual bootstrap
step is needed — the round transitions from PENDING to ACTIVE on its own.

## CI / CD

| Workflow | Trigger | What it does |
|---|---|---|
| `sdk-chain-deploy.yml` | push to `main` (paths: `sdk/**`) | Builds `zallyd` with Rust FFI, deploys to droplet, restarts `zallyd.service`, verifies health |
| `nullifier-ingest-deploy.yml` | push to `main` (paths: `nullifier-ingest/**`, `sdk/deploy/Caddyfile`) | Builds `nf-server`, deploys to droplet, restarts `nullifier-query-server.service`, reloads Caddy |
| `nullifier-ingest-resync.yml` | manual (`workflow_dispatch`) | SSHes into droplet and runs the full `ingest → export → restart` pipeline to resync the nullifier snapshot |
| `release.yml` | push tag `v*` | Builds cross-platform binaries, uploads to DO Spaces along with `join.sh` and `version.txt` |

All deploy workflows use `appleboy/ssh-action` + `appleboy/scp-action` with secrets
`DEPLOY_HOST`, `DEPLOY_USER`, and `SSH_PASSWORD`.
