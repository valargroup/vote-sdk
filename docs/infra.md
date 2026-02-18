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
| Nullifier query server (IMT) | `query-server` | 3000 | `nullifier-query-server.service` | `/opt/nullifier-ingest` |

## External URLs

Caddy terminates TLS and routes by path:

| Service | External URL | Example endpoint |
|---|---|---|
| Chain REST API | `https://46-101-255-48.sslip.io` | `/zally/v1/rounds` |
| Helper server | `https://46-101-255-48.sslip.io` | `/api/v1/status` |
| Nullifier / IMT | `https://46-101-255-48.sslip.io/nullifier` | `/nullifier/` (Caddy strips prefix) |
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

# Nullifier / IMT service
curl -sf https://46-101-255-48.sslip.io/nullifier/
```

## Remote Ceremony Bootstrap

After a fresh deploy (or chain reset), the DKG ceremony needs to be bootstrapped
before voting sessions can be created. The `scripts/remote-ceremony.sh` script
runs the ceremony from your local machine, using SSH to execute `zallyd` sign and
broadcast commands on the server (which has the vote module types registered).

### Prerequisites

- SSH access to the server (e.g. `zally` host alias in `~/.ssh/config`)
- The chain must be running and producing blocks

### Usage

```bash
# Bootstrap the ceremony (fetches key files on first run)
./scripts/remote-ceremony.sh zally

# With a custom local key cache directory
./scripts/remote-ceremony.sh zally /tmp/zally-ceremony

# Override the API URL (if not using sslip.io pattern)
ZALLY_API_URL=https://custom-domain.com ./scripts/remote-ceremony.sh zally
```

### How it works

The script fetches `ea.sk`, `ea.pk`, and `pallas.pk` from the server via SCP
(cached locally for reruns), then runs the `ceremony_bootstrap` e2e test with:

- REST queries (register Pallas key, deal EA key, poll status) hit the public HTTPS URL from your local machine
- Transaction signing and broadcasting execute on the remote via SSH (`/opt/zally-chain/zallyd`)
- ECIES encryption of the EA secret key runs locally in Rust

### Verify

```bash
curl -s https://46-101-255-48.sslip.io/zally/v1/ceremony | jq .ceremony.status
# Should return "3" (CEREMONY_STATUS_CONFIRMED)
```

## CI / CD

| Workflow | Trigger | What it does |
|---|---|---|
| `sdk-chain-deploy.yml` | push to `main` (paths: `sdk/**`) | Builds `zallyd` with Rust FFI, deploys to droplet, restarts `zallyd.service` |
| `nullifier-ingest-deploy.yml` | push to `main` (paths: `nullifier-ingest/**`, `sdk/deploy/Caddyfile`) | Builds `query-server` + `ingest-nfs`, deploys to droplet, restarts `nullifier-query-server.service`, reloads Caddy |
| `nullifier-ingest-resync.yml` | manual (`workflow_dispatch`) | SSHes into droplet and runs `ingest-nfs` to incrementally resync the nullifier snapshot from lightwalletd |

All deploy workflows use `appleboy/ssh-action` + `appleboy/scp-action` with secrets
`DEPLOY_HOST`, `DEPLOY_USER`, and `SSH_PASSWORD`.
