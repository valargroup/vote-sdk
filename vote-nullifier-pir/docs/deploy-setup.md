# Deploy setup for nf-server

This guide covers two deployment paths:

- **[Binary setup](#binary-setup-operators)** — Download a pre-built binary and run the service. No Rust toolchain or git clone required.
- **[Source setup](#source-setup-developers)** — Build from source with CI/CD-driven deployment.

---

## Hardware requirements

| Resource | Minimum | Recommended | Notes |
|----------|---------|-------------|-------|
| **CPU** | x86-64 (any) | x86-64 with AVX-512 | AVX-512 gives ~2× query throughput. Intel Ice Lake / Sapphire Rapids or newer. AMD Zen 4+. |
| **RAM** | 16 GB | 32 GB | The server loads ~6 GB of tier data and builds YPIR internal structures. Peak usage during initialization is roughly 2× the tier data size. |
| **Disk** | 20 GB free | 40 GB free | Nullifier data (~1.6 GB), PIR tier files (~6 GB), plus headroom for ingestion and re-export. |
| **OS** | Linux (x86-64) | Ubuntu 22.04+ / Debian 12+ | macOS (arm64/amd64) binaries are also published but not recommended for production serving. |
| **Network** | Outbound HTTPS | Static IP or DNS A record | Needs outbound access to a lightwalletd gRPC endpoint for ingestion. Inbound access on the serve port for clients. |

### AVX-512 note

The `serve` feature works on any x86-64 CPU. AVX-512 is an optional optimization that approximately halves PIR query latency (tier 1: ~0.5 s, tier 2: ~1.6 s per query). The pre-built `linux-amd64` release binary includes AVX-512 support; on CPUs without it the binary still runs but falls back to baseline SIMD.

---

## Binary setup (operators)

This path is for operators who want to run `nf-server` without cloning the repository or installing the Rust toolchain.

### 1. Download the binary

Grab the latest release from GitHub:

```bash
# Pick the asset for your platform
PLATFORM="linux-amd64"   # or: linux-arm64, darwin-amd64, darwin-arm64
VERSION=$(curl -s https://api.github.com/repos/valargroup/vote-nullifier-pir/releases/latest | grep tag_name | cut -d'"' -f4)

sudo mkdir -p /opt/nf-ingest
cd /opt/nf-ingest

# Download the binary and systemd unit
curl -fLO "https://github.com/valargroup/vote-nullifier-pir/releases/download/${VERSION}/nf-server-${PLATFORM}"
curl -fLO "https://github.com/valargroup/vote-nullifier-pir/releases/download/${VERSION}/nullifier-query-server.service"

sudo mv "nf-server-${PLATFORM}" nf-server
sudo chmod +x nf-server
```

### 2. Bootstrap nullifier data

Download the nullifier snapshot (first run only):

```bash
cd /opt/nf-ingest
BOOTSTRAP_URL="https://vote.fra1.digitaloceanspaces.com"

curl -fLO "${BOOTSTRAP_URL}/nullifiers.bin"
curl -fLO "${BOOTSTRAP_URL}/nullifiers.checkpoint"
curl -fLO "${BOOTSTRAP_URL}/nullifiers.tree"
```

### 3. Run the ingest + export pipeline

```bash
cd /opt/nf-ingest

# Ingest latest nullifiers from lightwalletd
./nf-server ingest --data-dir . --lwd-url https://zec.rocks:443

# Export PIR tier files (creates pir-data/ directory)
./nf-server export --data-dir . --output-dir ./pir-data
```

### 4. Install the systemd service

```bash
sudo cp /opt/nf-ingest/nullifier-query-server.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable nullifier-query-server
sudo systemctl start nullifier-query-server
```

Verify the service is running:

```bash
sudo systemctl status nullifier-query-server
curl http://localhost:3000/health
```

### 5. Periodic re-sync (cron)

Set up a cron job or systemd timer to keep nullifiers up to date:

```bash
# Example: re-sync every 6 hours
cat <<'EOF' | sudo tee /etc/cron.d/nf-resync
0 */6 * * * root cd /opt/nf-ingest && ./nf-server ingest --data-dir . --lwd-url https://zec.rocks:443 --invalidate && ./nf-server export --data-dir . --output-dir ./pir-data && systemctl restart nullifier-query-server
EOF
```

---

## Caddy reverse proxy with automatic TLS

[Caddy](https://caddyserver.com/) provides automatic HTTPS certificate provisioning via Let's Encrypt. This section sets up Caddy in front of `nf-server` so clients connect over TLS.

### Prerequisites

- A domain name with a DNS A record pointing to your server's public IP.
- Ports 80 and 443 open in your firewall (Caddy needs both for ACME HTTP-01 challenge).

### Install Caddy

```bash
# Debian / Ubuntu
sudo apt install -y debian-keyring debian-archive-keyring apt-transport-https
curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/gpg.key' | sudo gpg --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg
curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt' | sudo tee /etc/apt/sources.list.d/caddy-stable.list
sudo apt update
sudo apt install caddy
```

### Configure Caddy

Replace `pir.example.com` with your actual domain:

```bash
cat <<'EOF' | sudo tee /etc/caddy/Caddyfile
pir.example.com {
    reverse_proxy localhost:3000
}
EOF
```

### Start Caddy

```bash
sudo systemctl enable caddy
sudo systemctl restart caddy
```

Caddy will automatically obtain and renew a TLS certificate. Verify with:

```bash
curl https://pir.example.com/health
```

---

## Source setup (developers)

This path is for contributors and operators who want to build from source with CI/CD-driven deployment.

### Moving cached data to the deploy directory

The service uses flat binary files for nullifier storage. To move them into the deploy directory (default `/opt/nf-ingest`):

```bash
sudo mkdir -p /opt/nf-ingest

# Stop the service first if it is running
sudo systemctl stop nullifier-query-server || true

# Move data files
sudo mv /path/to/nullifiers.bin        /opt/nf-ingest/
sudo mv /path/to/nullifiers.checkpoint /opt/nf-ingest/
sudo mv /path/to/nullifiers.tree       /opt/nf-ingest/

# Ensure the deploy user can write (if deploy runs as a different user)
# sudo chown -R DEPLOY_USER:DEPLOY_USER /opt/nf-ingest
```

The unit file in `docs/nullifier-query-server.service` uses `/opt/nf-ingest` as the data directory by default.

### GitHub repository secrets

In the repo: **Settings -> Secrets and variables -> Actions**, add:

| Secret              | Description |
|---------------------|-------------|
| `DEPLOY_HOST`       | Remote hostname or IP (e.g. `ingest.example.com` or `192.0.2.10`). |
| `DEPLOY_USER`       | SSH user on that host (e.g. `deploy` or `ubuntu`). |
| `SSH_PASSWORD`      | SSH password for that user. |

### One-time setup on the remote host

**Directory and binaries**

- Create the deploy directory. Default in the workflow is `DEPLOY_PATH: /opt/nf-ingest`.
- Ensure the SSH user can write to that directory.
- Either bootstrap the nullifier data (`make bootstrap`) or run an initial ingest.

**Query server (PIR HTTP API)**

The `nf-server serve` subcommand starts the PIR HTTP server. It needs:

- **Nullifier data**: `nullifiers.bin` and `nullifiers.checkpoint` in the data directory.
- **PIR data**: Exported tier files in `pir-data/` (produced by `nf-server export`).
- **Port**: Configurable via `--port` (default 3000).

A systemd unit file is provided at `docs/nullifier-query-server.service`. Copy to `/etc/systemd/system/`:

```bash
sudo cp docs/nullifier-query-server.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable nullifier-query-server
sudo systemctl start nullifier-query-server
```

**Ingest (periodic sync)**

Run `nf-server ingest` periodically (cron or systemd timer) to sync new nullifiers:

```bash
/opt/nf-ingest/nf-server ingest \
    --data-dir /opt/nf-ingest \
    --lwd-url https://zec.rocks:443
```

After ingest, re-export with `nf-server export` and restart the serve process, or use the `resync.yml` workflow to do all three steps remotely.

### Changing deploy path or restart command

- **Deploy path**: Edit the `env.DEPLOY_PATH` in `.github/workflows/deploy.yml` (default `/opt/nf-ingest`).
- **Restart command**: Edit the "Install config and restart services" step in that workflow if you use a different service name.

### Manual runs

Both `deploy.yml` and `resync.yml` support `workflow_dispatch`, so you can trigger them from **Actions -> Run workflow** without pushing to `main`.

### Test locally

From the workspace root:

```bash
# Bootstrap nullifier data (first run only)
make bootstrap

# Or ingest from scratch
make ingest

# Export PIR tier files
make export-nf

# Start the server
make serve
```

Then check `http://localhost:3000/health` and `http://localhost:3000/root`.

---

## CI/CD workflows

The workflows in `.github/workflows/` handle building and deploying `nf-server`:

- **`deploy.yml`** — Builds on every push to `main` and deploys to a remote host via SSH.
- **`release.yml`** — Builds multi-platform binaries and publishes a GitHub Release on version tags.
- **`resync.yml`** — Manually triggers a nullifier resync (ingest + export + restart) on the remote host.
