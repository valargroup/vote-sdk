# Infrastructure

## Overview

All backend services run on a single DigitalOcean droplet (`46.101.255.48`) behind a
Caddy reverse proxy that provides automatic HTTPS via Let's Encrypt + sslip.io.

The frontend is deployed to Vercel.

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
| Helper server | `https://46-101-255-48.sslip.io` | `/api/v1/status` |
| Nullifier PIR | `https://46-101-255-48.sslip.io/nullifier` | `/nullifier/` (Caddy strips prefix) |
| Frontend (UI) | `https://zally-phi.vercel.app` | — |

## Frontend env vars

```bash
VITE_CHAIN_URL=https://46-101-255-48.sslip.io
VITE_NULLIFIER_URL=https://46-101-255-48.sslip.io/nullifier
```

## Health checks

```bash
# Chain — list rounds
curl -sf https://46-101-255-48.sslip.io/zally/v1/rounds

# Helper server — status
curl -sf https://46-101-255-48.sslip.io/api/v1/status

# Nullifier PIR server
curl -sf https://46-101-255-48.sslip.io/nullifier/health
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

All deploy workflows use `appleboy/ssh-action` + `appleboy/scp-action` with secrets
`DEPLOY_HOST`, `DEPLOY_USER`, and `SSH_PASSWORD`.
