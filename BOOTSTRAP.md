# Bootstrap Playbook

End-to-end guide for standing up a new Zally network from scratch. Each step gives the exact command; detailed explanations live in the linked docs.

## Prerequisites

- **Server**: Linux (amd64 or arm64) with root/sudo access, public IP, ports 443 (HTTPS), 26656 (P2P) open
- **Domain**: A domain or sslip.io address pointing to the server (for Caddy HTTPS)
- **GitHub**: Repository access with permissions to set Actions secrets and create tags
- **Vercel**: Project linked to the repo for the admin UI, with Edge Config store provisioned
- **Local tools**: `git`, `gh` CLI, SSH access to the server

## Phase 1: Infrastructure

### 1.1 Provision server and install dependencies

```bash
ssh root@<SERVER_IP>
apt update && apt install -y caddy jq
```

### 1.2 Configure Caddy

Edit the hostname in `sdk/deploy/Caddyfile` (replace `46-101-255-48.sslip.io` with your domain), then:

```bash
scp sdk/deploy/Caddyfile root@<SERVER_IP>:/etc/caddy/Caddyfile
ssh root@<SERVER_IP> "systemctl restart caddy"
```

See: [`sdk/deploy/Caddyfile`](sdk/deploy/Caddyfile)

### 1.3 Install systemd units

```bash
scp sdk/docs/zallyd-val{1,2,3}.service root@<SERVER_IP>:/etc/systemd/system/
scp nullifier-ingest/docs/nullifier-query-server.service root@<SERVER_IP>:/etc/systemd/system/
ssh root@<SERVER_IP> "systemctl daemon-reload && systemctl enable zallyd-val1 zallyd-val2 zallyd-val3 nullifier-query-server"
```

See: [`sdk/docs/deploy-setup.md` section 2](sdk/docs/deploy-setup.md)

### 1.4 Configure GitHub Actions secrets

In **Settings > Secrets and variables > Actions**, add:

| Secret | Value |
|--------|-------|
| `DEPLOY_HOST` | Server hostname or IP |
| `DEPLOY_USER` | SSH user (e.g. `root`) |
| `SSH_PASSWORD` | SSH password |
| `DO_ACCESS_KEY` | DigitalOcean Spaces access key |
| `DO_SECRET_KEY` | DigitalOcean Spaces secret key |

Create a **production** environment and add `CEREMONY_SSH_KEY` (ed25519 private key):

```bash
ssh-keygen -t ed25519 -C "github-actions" -f /tmp/zally-ci-key -N ""
ssh-copy-id -i /tmp/zally-ci-key.pub root@<SERVER_IP>
# Paste contents of /tmp/zally-ci-key into the CEREMONY_SSH_KEY secret
```

See: [`sdk/docs/deploy-setup.md` section 1](sdk/docs/deploy-setup.md)

### 1.5 Configure Vercel environment variables

In the Vercel project settings, set:

| Variable | Value |
|----------|-------|
| `EDGE_CONFIG` | Edge Config connection string |
| `EDGE_CONFIG_ID` | Edge Config store ID |
| `VERCEL_API_TOKEN` | Vercel API token (for Edge Config updates) |

## Phase 2: Genesis Chain

### 2.1 Build and deploy

The first deploy with `reset_chain=true` builds everything and initializes the 3-validator chain:

1. Go to **Actions > Deploy SDK chain > Run workflow**
2. Check **Reset chain state**
3. Run — this builds `zallyd` + `create-val-tx`, runs `init_multi.sh --ci`, starts all 3 validators, and registers val2/val3

### 2.2 Verify chain is running

```bash
ssh root@<SERVER_IP>
systemctl status zallyd-val1 zallyd-val2 zallyd-val3
curl -s http://localhost:1418/zally/v1/commitment-tree/latest | jq .
```

### 2.3 Register validator URL in Edge Config

Open the admin UI and register the validator's public URL (e.g. `https://your-domain.com`) so that `join.sh` and iOS clients can discover the network.

## Phase 3: Nullifier Service

### 3.1 Create data directory

```bash
ssh root@<SERVER_IP>
mkdir -p /opt/nullifier-ingest
```

### 3.2 Bootstrap nullifiers

Build and deploy the nullifier service. The `nullifier-ingest-deploy.yml` workflow handles this on pushes to `main` under `nullifier-ingest/**`. For the initial bootstrap:

```bash
# On the server, after binaries are deployed:
cd /opt/nullifier-ingest
DB_PATH=/opt/nullifier-ingest/nullifiers.db LWD_URL=https://zec.rocks:443 ./nf-server ingest
./nf-server export
systemctl start nullifier-query-server
```

See: [`nullifier-ingest/docs/deploy-setup.md`](nullifier-ingest/docs/deploy-setup.md)

### 3.3 Verify nullifier service

```bash
curl -s http://localhost:3000/health
curl -s http://localhost:3000/root | jq .
```

### 3.4 Register PIR URL in Edge Config

In the admin UI, register the nullifier service URL (e.g. `https://your-domain.com/nullifier`) so iOS clients can discover the PIR endpoint.

## Phase 4: First Voting Round

1. Open the admin UI (Vercel deployment URL)
2. Create a new voting round with desired parameters
3. The ceremony runs automatically via PrepareProposal (auto-deal + auto-ack)
4. Verify the round transitions from **PENDING** to **ACTIVE**:

```bash
curl -s http://localhost:1418/zally/v1/sessions | jq '.[0].status'
```

## Phase 5: Onboard Validators

Share the join URL with new validators:

```
curl -fsSL https://vote.fra1.digitaloceanspaces.com/join.sh | bash
```

After they run `join.sh` and report their address:

1. Fund their account via the admin UI (send stake tokens)
2. They run the generated `start.sh` which syncs and auto-registers

Verify new validators appear:

```bash
curl -s http://localhost:1418/cosmos/staking/v1beta1/validators | jq '.validators[].description.moniker'
```

## Phase 6: Release Pipeline

### 6.1 Tag a release

```bash
git tag v0.1.0
git push origin v0.1.0
```

The `release.yml` workflow builds all 4 platform tarballs (linux-amd64, linux-arm64, darwin-amd64, darwin-arm64), generates SHA-256 checksums, creates a GitHub Release, uploads to DO Spaces, and distributes to the server.

### 6.2 Verify

```bash
# DO Spaces
curl -sf https://vote.fra1.digitaloceanspaces.com/version.txt
# GitHub Release
gh release view v0.1.0
# join.sh works end-to-end
curl -fsSL https://vote.fra1.digitaloceanspaces.com/join.sh | bash
```

## Verification Checklist

Run from the server to confirm everything is healthy:

```bash
# Chain validators
for svc in zallyd-val1 zallyd-val2 zallyd-val3; do
  echo "$svc: $(systemctl is-active $svc)"
done

# Chain API
curl -sf http://localhost:1418/zally/v1/commitment-tree/latest > /dev/null && echo "Chain API: OK"

# Helper server
curl -sf http://localhost:1418/api/v1/status > /dev/null && echo "Helper server: OK"

# Nullifier query server
curl -sf http://localhost:3000/health > /dev/null && echo "Nullifier service: OK"

# HTTPS (external)
curl -sf https://<YOUR_DOMAIN>/zally/v1/commitment-tree/latest > /dev/null && echo "HTTPS proxy: OK"

# Voting sessions
curl -s http://localhost:1418/zally/v1/sessions | jq '.[0].status'
```

## Troubleshooting

**Chain won't start / validators not connecting**
```bash
journalctl -u zallyd-val1 --no-pager -n 50
# Check persistent_peers in config.toml, ensure P2P port 26656 is open
```

**Helper server not responding**
```bash
# Verify [helper] section exists in val1's app.toml
grep -A5 '\[helper\]' /opt/zally-chain/.zallyd-val1/config/app.toml
```

**Nullifier service unhealthy**
```bash
journalctl -u nullifier-query-server --no-pager -n 50
# Check DB_PATH and DATA_DIR point to valid data
ls -la /opt/nullifier-ingest/nullifiers.db
```

**Caddy HTTPS not working**
```bash
journalctl -u caddy --no-pager -n 50
# Verify domain resolves and port 443 is open
caddy validate --config /etc/caddy/Caddyfile
```

**Ceremony stuck (round stays in PENDING)**
```bash
# Check validator count — need enough ack'ing validators
curl -s http://localhost:1418/cosmos/staking/v1beta1/validators | jq '[.validators[] | select(.status == "BOND_STATUS_BONDED")] | length'
# Check val1 logs for ceremony errors
journalctl -u zallyd-val1 --no-pager | grep -i ceremony
```

**join.sh checksum failure**
```bash
# Re-run the release workflow to regenerate checksums, or download manually:
curl -fsSL -o /tmp/zally.tar.gz "https://vote.fra1.digitaloceanspaces.com/zally-<VERSION>-<PLATFORM>.tar.gz"
sha256sum /tmp/zally.tar.gz
```
