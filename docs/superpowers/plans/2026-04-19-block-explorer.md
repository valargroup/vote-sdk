# Block Explorer Implementation Plan (systemd-native)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Deploy a block explorer for svote-1 that shows the real prod chain, backed by a non-validating svoted archive node running as a 4th systemd service alongside val1/val2/val3, with a static ping-pub Vue bundle served by Caddy.

**Architecture:** Add a new `svoted-archive` systemd service that shares genesis with val1/val2/val3 and peers with val1 over p2p (pruning=nothing). Build the ping-pub SPA in GitHub Actions (Node 20 + yarn), scp the `dist/` to `/opt/shielded-vote/explorer/dist/`, and point three new Caddy subdomains (`explorer.*`, `explorer-api.*`, `explorer-rpc.*`) at the static files, archive LCD, and archive RPC respectively. Chain reset wipes the archive alongside validators.

**Tech Stack:** Bash (init scripts), systemd, Caddy 2, GitHub Actions, Node 20 + yarn (for the CI explorer build step), ping-pub/explorer (Vue 3 SPA pinned to a specific commit).

**Spec reference:** [`docs/superpowers/specs/2026-04-19-block-explorer-design.md`](../specs/2026-04-19-block-explorer-design.md)

---

## File Structure

**New (3 files):**
- `scripts/init-archive.sh` — One-shot script that initializes `$HOME/.svoted-archive` given val1 has already been inited. Called by the reset workflow after `init_multi.sh`.
- `docs/svoted-archive.service` — systemd unit for the archive node.
- `deploy/explorer/svote.json` — ping-pub chain config (endpoints, denom, bech32 prefix, pretty_name).

**Modified (3 files):**
- `.github/workflows/sdk-chain-deploy.yml` — add a parallel `build-explorer` job producing `explorer-dist.tar.gz`, include `init-archive.sh` + `svoted-archive.service` in the main artifact, scp everything.
- `.github/workflows/sdk-chain-reset.yml` — extend stop/install-units/init/start/verify phases to cover the archive.
- `deploy/Caddyfile` — three new site blocks.

**Unchanged (verified, not modified):**
- `scripts/init_multi.sh` — archive init lives in a separate script.
- `.github/workflows/caddy-deploy.yml` — already auto-deploys Caddyfile changes, no edit needed.
- All validator service files (`docs/svoted-val{1,2,3}.service`).
- Any Go code under `app/`, `x/`, `cmd/`, or `crypto/`.

---

## Task 1: ping-pub chain config

**Files:**
- Create: `deploy/explorer/svote.json`

- [ ] **Step 1.1: Create the chain config file**

Write `deploy/explorer/svote.json`:

```json
{
  "chain_name": "svote-testnet",
  "registry_name": "svote",
  "api": ["https://explorer-api.46-101-255-48.sslip.io"],
  "rpc": ["https://explorer-rpc.46-101-255-48.sslip.io"],
  "sdk_version": "0.50",
  "coin_type": "118",
  "min_tx_fee": "0",
  "addr_prefix": "sv",
  "logo": "/logos/cosmos.svg",
  "theme_color": "#2f4858",
  "assets": [
    {
      "base": "usvote",
      "symbol": "SVOTE",
      "exponent": "6",
      "coingecko_id": "",
      "logo": "/logos/cosmos.svg"
    }
  ],
  "chain_id": "svote-1",
  "pretty_name": "Zcash Vote",
  "features": ["staking", "gov"]
}
```

- [ ] **Step 1.2: Validate it's legal JSON**

Run: `jq . deploy/explorer/svote.json > /dev/null && echo ok`

Expected: `ok`.

- [ ] **Step 1.3: Commit**

```bash
git add deploy/explorer/svote.json
git commit -m "Add svote-1 ping-pub chain config

Points at explorer-api / explorer-rpc sslip.io subdomains. Display
name is 'Zcash Vote'. Consumed by the build-explorer CI step, which
drops this into ping-pub's chains/testnet/ at build time."
```

---

## Task 2: systemd unit for the archive

**Files:**
- Create: `docs/svoted-archive.service`

- [ ] **Step 2.1: Create the unit file**

Write `docs/svoted-archive.service`:

```ini
[Unit]
Description=Shielded-Vote SDK chain - Archive node (svoted-archive)
After=svoted-val1.service
Requires=svoted-val1.service

[Service]
Type=simple
User=root
WorkingDirectory=/opt/shielded-vote
ExecStart=/opt/shielded-vote/svoted start --home /opt/shielded-vote/.svoted-archive
Restart=on-failure

[Install]
WantedBy=multi-user.target
```

No `--serve-ui`, `--serve-pir`, `--serve-admin` — the archive only answers queries. `Requires=svoted-val1.service` ties the archive's lifecycle to val1.

- [ ] **Step 2.2: Syntax-check if systemd-analyze is available**

Run: `command -v systemd-analyze >/dev/null && systemd-analyze verify docs/svoted-archive.service 2>&1 || echo "systemd-analyze not available locally — skipping; unit is validated at deploy time"`

Expected: either no output (passes) or the fallback message. Unit files that reference paths that don't exist on the local machine (`/opt/shielded-vote/svoted`) may warn — that's fine, this is a deploy-time artifact.

- [ ] **Step 2.3: Commit**

```bash
git add docs/svoted-archive.service
git commit -m "Add svoted-archive systemd unit

Runs svoted against /opt/shielded-vote/.svoted-archive as a
non-validating full node. Requires=svoted-val1.service so the archive
always cycles with val1. Serves LCD/RPC for the block explorer."
```

---

## Task 3: archive init script

**Files:**
- Create: `scripts/init-archive.sh`

This is the meat of the prod change. The script reads val1's finalized genesis and wires an archive home directory pointing at a dedicated port set.

- [ ] **Step 3.1: Create the init script**

Write `scripts/init-archive.sh`:

```bash
#!/bin/bash
# init-archive.sh — Initialize an archive (non-validator) svoted node.
#
# Must run AFTER scripts/init_multi.sh (requires val1's genesis to exist).
# Creates $HOME/.svoted-archive configured to peer with val1 via p2p on
# 127.0.0.1:26156 and serve LCD/RPC for the block explorer.
#
# Usage:
#   bash scripts/init-archive.sh
#
# Env: $HOME must be the prefix where .svoted-val1 lives (same convention
# as init_multi.sh — local dev uses the real $HOME, CI exports
# HOME=/opt/shielded-vote).
set -e

BINARY="svoted"
CHAIN_ID="svote-1"

HOME_VAL1="$HOME/.svoted-val1"
HOME_ARCHIVE="$HOME/.svoted-archive"

# Archive port allocation (+100 pattern matching init_multi.sh).
#                        Val1    Val2    Val3    Archive
# CometBFT P2P:         26156   26256   26356   26456
# CometBFT RPC:         26157   26257   26357   26457
# gRPC:                  9390    9490    9590    9690
# gRPC-web:              9391    9491    9591    9691
# REST API:              1418    1518    1618    1718
# pprof:                 6160    6260    6360    6460
P2P_PORT=26456
RPC_PORT=26457
API_PORT=1718
GRPC_PORT=9690
GRPC_WEB_PORT=9691
PPROF_PORT=6460

# ---------------------------------------------------------------------------
# Pre-flight: val1's genesis must exist.
# ---------------------------------------------------------------------------
if [ ! -f "$HOME_VAL1/config/genesis.json" ]; then
    echo "ERROR: $HOME_VAL1/config/genesis.json not found."
    echo "       Run scripts/init_multi.sh (or --ci) before init-archive.sh."
    exit 1
fi

# ---------------------------------------------------------------------------
# Cleanup — always start from a clean archive home.
# ---------------------------------------------------------------------------
echo "=== Cleaning up previous archive data ==="
rm -rf "$HOME_ARCHIVE"

# ---------------------------------------------------------------------------
# Initialize the archive node
# ---------------------------------------------------------------------------
echo ""
echo "=== Initializing archive node ==="
$BINARY init archive --chain-id "$CHAIN_ID" --home "$HOME_ARCHIVE"

# Generate a Pallas keypair. Archive doesn't use it — but app.toml validates
# pallas_sk_path at startup and svoted refuses to boot if the path is empty
# or unreadable. The file exists and is ignored.
$BINARY pallas-keygen --home "$HOME_ARCHIVE"

# Copy val1's finalized genesis — THIS is what makes the archive part of the
# same chain as val1/2/3. Same genesis hash = same chain.
cp "$HOME_VAL1/config/genesis.json" "$HOME_ARCHIVE/config/genesis.json"

# ---------------------------------------------------------------------------
# Configure config.toml (ports, relaxed addr_book, persistent_peers)
# ---------------------------------------------------------------------------
CONFIG_TOML="$HOME_ARCHIVE/config/config.toml"

sed -i.bak "s|laddr = \"tcp://0.0.0.0:26656\"|laddr = \"tcp://0.0.0.0:${P2P_PORT}\"|" "$CONFIG_TOML"
sed -i.bak "s|laddr = \"tcp://127.0.0.1:26657\"|laddr = \"tcp://127.0.0.1:${RPC_PORT}\"|" "$CONFIG_TOML"
sed -i.bak "s|pprof_laddr = \"localhost:6060\"|pprof_laddr = \"localhost:${PPROF_PORT}\"|" "$CONFIG_TOML"

# All validators run on 127.0.0.1 — same relaxed addr_book as init_multi.sh.
sed -i.bak 's/^addr_book_strict = true/addr_book_strict = false/' "$CONFIG_TOML"
sed -i.bak 's/^allow_duplicate_ip = false/allow_duplicate_ip = true/' "$CONFIG_TOML"

# Peer with val1 at its p2p port (26156).
VAL1_NODE_ID=$($BINARY comet show-node-id --home "$HOME_VAL1")
VAL1_PEER="${VAL1_NODE_ID}@127.0.0.1:26156"
sed -i.bak "s|persistent_peers = \"\"|persistent_peers = \"${VAL1_PEER}\"|" "$CONFIG_TOML"
echo "Archive peer: $VAL1_PEER"

rm -f "${CONFIG_TOML}.bak"

# ---------------------------------------------------------------------------
# Configure app.toml (API, CORS, ports, vote-module paths, pruning=nothing)
# ---------------------------------------------------------------------------
APP_TOML="$HOME_ARCHIVE/config/app.toml"

# Enable REST API, bind 0.0.0.0 so Caddy can reach it, CORS on for browser.
sed -i.bak '/\[api\]/,/\[.*\]/ s/enable = false/enable = true/' "$APP_TOML"
sed -i.bak "s|address = \"tcp://localhost:1317\"|address = \"tcp://0.0.0.0:${API_PORT}\"|" "$APP_TOML"
sed -i.bak '/\[api\]/,/\[.*\]/ s/enabled-unsafe-cors = false/enabled-unsafe-cors = true/' "$APP_TOML"

# gRPC / gRPC-web ports (not exposed publicly, just avoiding collision).
sed -i.bak "s|address = \"localhost:9090\"|address = \"localhost:${GRPC_PORT}\"|" "$APP_TOML"
sed -i.bak "s|address = \"localhost:9091\"|address = \"localhost:${GRPC_WEB_PORT}\"|" "$APP_TOML"

# Vote-module key paths. Archive doesn't use these; setting them to local
# paths satisfies app.toml's startup validation.
EA_SK_PATH="$HOME_ARCHIVE/ea.sk"
PALLAS_SK_PATH="$HOME_ARCHIVE/pallas.sk"
sed -i.bak "s|^ea_sk_path = .*|ea_sk_path = \"$EA_SK_PATH\"|" "$APP_TOML"
sed -i.bak "s|^pallas_sk_path = .*|pallas_sk_path = \"$PALLAS_SK_PATH\"|" "$APP_TOML"
sed -i.bak "s|^comet_rpc = .*|comet_rpc = \"http://localhost:${RPC_PORT}\"|" "$APP_TOML"

# Full-history pruning: keep every block forever. min-retain-blocks=0
# disables the retention cap entirely.
sed -i.bak 's|^pruning = ".*"|pruning = "nothing"|' "$APP_TOML"
sed -i.bak 's|^min-retain-blocks = .*|min-retain-blocks = 0|' "$APP_TOML"

rm -f "${APP_TOML}.bak"

# ---------------------------------------------------------------------------
# Summary
# ---------------------------------------------------------------------------
echo ""
echo "=========================================="
echo "=== Archive Node Initialized OK        ==="
echo "=========================================="
echo "  Home:    $HOME_ARCHIVE"
echo "  Chain:   $CHAIN_ID (genesis copied from val1)"
echo "  RPC:     http://127.0.0.1:${RPC_PORT}"
echo "  API:     http://127.0.0.1:${API_PORT}"
echo "  P2P:     ${P2P_PORT} (peer: $VAL1_PEER)"
echo "  Pruning: nothing (full history retained)"
echo ""
echo "Start with: sudo systemctl start svoted-archive"
```

- [ ] **Step 3.2: Make it executable**

Run: `chmod +x scripts/init-archive.sh`

- [ ] **Step 3.3: Syntax-check**

Run: `bash -n scripts/init-archive.sh && echo ok`

Expected: `ok`.

- [ ] **Step 3.4: Commit**

```bash
git add scripts/init-archive.sh
git commit -m "Add scripts/init-archive.sh

Initializes an archive (non-validator) svoted home sharing val1's
genesis. Peers with val1 over p2p at 127.0.0.1:26156; serves LCD on
:1718 and RPC on :26457; pruning=nothing so the explorer sees full
history back to genesis.

Run after scripts/init_multi.sh. CI invokes it from
sdk-chain-reset.yml as part of the chain-init phase."
```

---

## Task 4: Caddy routes

**Files:**
- Modify: `deploy/Caddyfile` — append three new site blocks after the last existing validator block.

- [ ] **Step 4.1: Read the tail of the current Caddyfile**

Run: `tail -30 deploy/Caddyfile`

Expected: shows the last `valN.46-101-255-48.sslip.io { ... }` block. New blocks append after whatever is currently last.

- [ ] **Step 4.2: Append the three new site blocks**

Add exactly this to the end of `deploy/Caddyfile` (tab-indented to match existing style):

```
# Block explorer frontend — static ping-pub Vue bundle served from disk.
explorer.46-101-255-48.sslip.io {
	root * /opt/shielded-vote/explorer/dist
	file_server
	try_files {path} /index.html
}

# Archive node LCD — ping-pub's browser calls land here.
explorer-api.46-101-255-48.sslip.io {
	reverse_proxy localhost:1718
}

# Archive node Tendermint RPC — also called directly by the ping-pub frontend.
explorer-rpc.46-101-255-48.sslip.io {
	reverse_proxy localhost:26457
}
```

`try_files {path} /index.html` is Caddy's SPA fallback: unknown paths serve `index.html` so the Vue Router's HTML5 history mode works.

No CORS injection: the archive's `app.toml` sets `enabled-unsafe-cors = true`, so the LCD serves its own `Access-Control-*` headers.

- [ ] **Step 4.3: Validate the Caddyfile**

Run: `command -v caddy >/dev/null && caddy validate --config deploy/Caddyfile --adapter caddyfile || echo "caddy not installed locally — skipping; caddy-deploy.yml validates on deploy"`

Expected: `Valid configuration` (if caddy is installed) or the fallback message.

- [ ] **Step 4.4: Commit**

```bash
git add deploy/Caddyfile
git commit -m "Add Caddy routes for block explorer

explorer.*       -> file_server /opt/shielded-vote/explorer/dist
explorer-api.*   -> archive LCD :1718
explorer-rpc.*   -> archive Tendermint RPC :26457

Caddy auto-reloads via caddy-deploy.yml when this file changes. The
explorer-api/-rpc routes serve 502 until svoted-archive is running
on the box."
```

---

## Task 5: sdk-chain-deploy.yml — build + ship archive + explorer

This task adds the explorer build job, extends the main artifact, and extends the scp step. No change to the restart job (that's Task 6).

**Files:**
- Modify: `.github/workflows/sdk-chain-deploy.yml`

- [ ] **Step 5.1: Extend the `build` job to include the archive init script and service unit**

In `.github/workflows/sdk-chain-deploy.yml`, find the existing "Copy init script to build root" step (around line 70-71). Replace:

```yaml
      - name: Copy init script to build root
        run: cp scripts/init_multi.sh init_multi.sh
```

with:

```yaml
      - name: Copy init scripts to build root
        run: |
          cp scripts/init_multi.sh init_multi.sh
          cp scripts/init-archive.sh init-archive.sh
```

- [ ] **Step 5.2: Extend the `build` job's artifact to include the new files**

In the same file, find the "Upload artifacts" step (around line 72-83). Replace:

```yaml
      - name: Upload artifacts
        uses: actions/upload-artifact@v4
        with:
          name: sdk-chain-binaries
          path: |
            svoted
            create-val-tx
            init_multi.sh
            ui/dist
            docs/svoted-val1.service
            docs/svoted-val2.service
            docs/svoted-val3.service
```

with:

```yaml
      - name: Upload artifacts
        uses: actions/upload-artifact@v4
        with:
          name: sdk-chain-binaries
          path: |
            svoted
            create-val-tx
            init_multi.sh
            init-archive.sh
            ui/dist
            docs/svoted-val1.service
            docs/svoted-val2.service
            docs/svoted-val3.service
            docs/svoted-archive.service
```

- [ ] **Step 5.3: Add a new `build-explorer` job in parallel with `build`**

In the same file, add this job immediately after the `build:` job's closing line (after the existing `name: sdk-chain-binaries` upload — i.e., before the `deploy:` job starts). Insert:

```yaml
  build-explorer:
    name: Build explorer
    runs-on: ubuntu-latest
    steps:
      - name: Checkout
        uses: actions/checkout@v4
        with:
          sparse-checkout: deploy/explorer
          sparse-checkout-cone-mode: false
      - name: Setup Node
        uses: actions/setup-node@v4
        with:
          node-version: '20'
      - name: Enable corepack (yarn 1.x)
        run: corepack enable
      - name: Cache yarn packages
        uses: actions/cache@v4
        with:
          path: ~/.cache/yarn
          key: yarn-pingpub-ee1ca5ee54d7bd2dc4c5b4df8f3b93595440e9c8
      - name: Clone ping-pub and build
        run: |
          git clone https://github.com/ping-pub/explorer.git /tmp/ping-pub
          cd /tmp/ping-pub
          git checkout ee1ca5ee54d7bd2dc4c5b4df8f3b93595440e9c8
          cp "$GITHUB_WORKSPACE/deploy/explorer/svote.json" chains/testnet/svote.json
          yarn install
          yarn build
          tar czf "$GITHUB_WORKSPACE/explorer-dist.tar.gz" -C dist .
      - name: Upload explorer artifact
        uses: actions/upload-artifact@v4
        with:
          name: sdk-explorer-dist
          path: explorer-dist.tar.gz
```

- [ ] **Step 5.4: Extend the `deploy` job to download + ship the explorer and archive files**

In the same file, find the `deploy:` job (starts around line 85). Update its `needs:` line:

Change:
```yaml
  deploy:
    name: Deploy binaries
    runs-on: ubuntu-latest
    needs: build
```

to:
```yaml
  deploy:
    name: Deploy binaries
    runs-on: ubuntu-latest
    needs: [build, build-explorer]
```

Find the "Download artifacts" step (around line 90-93). Replace:

```yaml
      - name: Download artifacts
        uses: actions/download-artifact@v4
        with:
          name: sdk-chain-binaries
```

with:

```yaml
      - name: Download chain artifacts
        uses: actions/download-artifact@v4
        with:
          name: sdk-chain-binaries
      - name: Download explorer artifact
        uses: actions/download-artifact@v4
        with:
          name: sdk-explorer-dist
```

Find the "Deploy via SCP" step (around line 103-110). Replace its `source:` line:

```yaml
          source: "svoted,create-val-tx,init_multi.sh,ui/dist,docs/svoted-val1.service,docs/svoted-val2.service,docs/svoted-val3.service"
```

with:

```yaml
          source: "svoted,create-val-tx,init_multi.sh,init-archive.sh,ui/dist,explorer-dist.tar.gz,docs/svoted-val1.service,docs/svoted-val2.service,docs/svoted-val3.service,docs/svoted-archive.service"
```

- [ ] **Step 5.5: Add a new remote step to extract the explorer tarball**

In the same file, find the `deploy:` job's last step ("Deploy via SCP"). Add this step immediately after it (before the `restart:` job starts):

```yaml
      - name: Install explorer bundle on host
        uses: appleboy/ssh-action@v1.0.3
        with:
          host: ${{ secrets.DEPLOY_HOST }}
          username: ${{ secrets.DEPLOY_USER }}
          password: ${{ secrets.SSH_PASSWORD }}
          script: |
            sudo mkdir -p /opt/shielded-vote/explorer/dist
            sudo rm -rf /opt/shielded-vote/explorer/dist/*
            sudo tar xzf /opt/shielded-vote/explorer-dist.tar.gz -C /opt/shielded-vote/explorer/dist
            sudo rm -f /opt/shielded-vote/explorer-dist.tar.gz
```

- [ ] **Step 5.6: Update the Slack failure-notification to include the new job**

In the same file, find `notify-slack:` near the bottom. Update its `needs:` line:

Change:
```yaml
    needs: [build, deploy, restart]
```

to:
```yaml
    needs: [build, build-explorer, deploy, restart]
```

And update the "Job Results" block text (around line 150):

Change:
```yaml
                  "text": { "type": "mrkdwn", "text": "*Job Results:*\nBuild: `${{ needs.build.result }}` | Deploy: `${{ needs.deploy.result }}` | Restart: `${{ needs.restart.result }}`" }
```

to:
```yaml
                  "text": { "type": "mrkdwn", "text": "*Job Results:*\nBuild: `${{ needs.build.result }}` | Build-Explorer: `${{ needs.build-explorer.result }}` | Deploy: `${{ needs.deploy.result }}` | Restart: `${{ needs.restart.result }}`" }
```

- [ ] **Step 5.7: Validate the YAML parses**

Run: `python3 -c "import yaml; yaml.safe_load(open('.github/workflows/sdk-chain-deploy.yml'))" && echo ok`

Expected: `ok`.

If Python isn't available locally but `yq` is: `yq eval '.' .github/workflows/sdk-chain-deploy.yml > /dev/null && echo ok`.

Either way, the real validation happens when GitHub parses the workflow on push.

- [ ] **Step 5.8: Commit**

```bash
git add .github/workflows/sdk-chain-deploy.yml
git commit -m "Deploy archive node and explorer bundle

- New build-explorer job: clones ping-pub at pinned SHA
  ee1ca5ee54d7bd2dc4c5b4df8f3b93595440e9c8, bakes
  deploy/explorer/svote.json as chains/testnet/svote.json, yarn build,
  tars dist/ into sdk-explorer-dist artifact.
- build job: includes init-archive.sh and svoted-archive.service in
  sdk-chain-binaries.
- deploy job: downloads both artifacts, scps all files, untars the
  explorer bundle into /opt/shielded-vote/explorer/dist/.

svoted-archive.service is installed and started by sdk-chain-reset.yml
(next commit); this PR just ships the files."
```

---

## Task 6: sdk-chain-reset.yml — wire the archive into stop/init/start/verify

**Files:**
- Modify: `.github/workflows/sdk-chain-reset.yml`

- [ ] **Step 6.1: Extend the "Stop services" step to cover the archive**

In `.github/workflows/sdk-chain-reset.yml`, find the "Stop services" step (around lines 30-52). Replace this block:

```yaml
          script: |
            for svc in svoted-val1 svoted-val2 svoted-val3; do
              sudo systemctl stop "$svc" || true
            done
            sudo pkill -9 -x svoted || true
            for i in $(seq 1 20); do
              if ! ss -tlnp | grep -qE ':26156|:26256|:26356|:6160|:6260|:6360|:3000'; then
                echo "Ports freed"
                break
              fi
              if [ "$i" = "20" ]; then
                echo "WARN: ports still bound after 20s, forcing..."
                sudo fuser -k 26156/tcp 26256/tcp 26356/tcp 6160/tcp 6260/tcp 6360/tcp 3000/tcp || true
                sleep 1
              fi
              sleep 1
            done
```

with:

```yaml
          script: |
            for svc in svoted-archive svoted-val1 svoted-val2 svoted-val3; do
              sudo systemctl stop "$svc" || true
            done
            sudo pkill -9 -x svoted || true
            for i in $(seq 1 20); do
              if ! ss -tlnp | grep -qE ':26156|:26256|:26356|:26456|:6160|:6260|:6360|:6460|:3000'; then
                echo "Ports freed"
                break
              fi
              if [ "$i" = "20" ]; then
                echo "WARN: ports still bound after 20s, forcing..."
                sudo fuser -k 26156/tcp 26256/tcp 26356/tcp 26456/tcp 6160/tcp 6260/tcp 6360/tcp 6460/tcp 3000/tcp || true
                sleep 1
              fi
              sleep 1
            done
```

Changes: archive added to the stop loop (first, so it stops before val1 — systemd would stop it automatically via Requires=, but explicit is cleaner), p2p port 26456 and pprof 6460 added to the port-check grep and fuser fallback.

- [ ] **Step 6.2: Extend the "Install updated systemd units" step**

Find the step (around lines 54-67). Replace:

```yaml
          script: |
            for i in 1 2 3; do
              src="${{ env.DEPLOY_PATH }}/docs/svoted-val${i}.service"
              if [ -f "$src" ]; then
                sudo cp "$src" /etc/systemd/system/svoted-val${i}.service
              fi
            done
            sudo systemctl daemon-reload
```

with:

```yaml
          script: |
            for i in 1 2 3; do
              src="${{ env.DEPLOY_PATH }}/docs/svoted-val${i}.service"
              if [ -f "$src" ]; then
                sudo cp "$src" /etc/systemd/system/svoted-val${i}.service
              fi
            done
            if [ -f "${{ env.DEPLOY_PATH }}/docs/svoted-archive.service" ]; then
              sudo cp "${{ env.DEPLOY_PATH }}/docs/svoted-archive.service" /etc/systemd/system/svoted-archive.service
              sudo systemctl enable svoted-archive
            fi
            sudo systemctl daemon-reload
```

`systemctl enable` makes the archive start automatically on boot alongside the validators.

- [ ] **Step 6.3: Extend the "Reinitialize all validators" step to also init the archive**

Find the step (around lines 69-85). Replace the script body:

```yaml
          script: |
            export HOME=${{ env.DEPLOY_PATH }} PATH=${{ env.DEPLOY_PATH }}:$PATH
            export VM_PRIVKEY="$VM_PRIVKEY"
            export SVOTE_HELPER_SENTRY_DSN="$SVOTE_HELPER_SENTRY_DSN"
            chmod +x ${{ env.DEPLOY_PATH }}/svoted ${{ env.DEPLOY_PATH }}/create-val-tx
            bash ${{ env.DEPLOY_PATH }}/init_multi.sh --ci
```

with:

```yaml
          script: |
            export HOME=${{ env.DEPLOY_PATH }} PATH=${{ env.DEPLOY_PATH }}:$PATH
            export VM_PRIVKEY="$VM_PRIVKEY"
            export SVOTE_HELPER_SENTRY_DSN="$SVOTE_HELPER_SENTRY_DSN"
            chmod +x ${{ env.DEPLOY_PATH }}/svoted ${{ env.DEPLOY_PATH }}/create-val-tx
            bash ${{ env.DEPLOY_PATH }}/init_multi.sh --ci
            bash ${{ env.DEPLOY_PATH }}/init-archive.sh
```

The archive init runs AFTER init_multi.sh, because it needs val1's finalized genesis to copy.

- [ ] **Step 6.4: Extend the "Start services" step**

Find the step (around lines 87-97). Replace the script body:

```yaml
          script: |
            for svc in svoted-val1 svoted-val2 svoted-val3; do
              sudo systemctl start "$svc"
            done
```

with:

```yaml
          script: |
            for svc in svoted-val1 svoted-val2 svoted-val3; do
              sudo systemctl start "$svc"
            done
            if [ -d "${{ env.DEPLOY_PATH }}/.svoted-archive" ]; then
              sudo systemctl start svoted-archive
            else
              echo "NOTE: ${{ env.DEPLOY_PATH }}/.svoted-archive does not exist yet — skipping archive start."
              echo "      Run this workflow with reset_chain=true to initialize the archive."
            fi
```

Archive is started last. `Requires=svoted-val1.service` means it'll wait for val1 to be active before coming up. The home-dir check handles the first-rollout case where `sdk-chain-deploy.yml` ships the new unit file before anyone has run a reset: the archive simply stays stopped until a `reset_chain=true` run initializes it.

- [ ] **Step 6.5: Extend the "Verify services healthy" step**

Find the step (around lines 157-216). Replace this portion of the script:

```yaml
            sleep 5
            for svc in svoted-val1 svoted-val2 svoted-val3; do
              if ! sudo systemctl is-active --quiet "$svc"; then
                echo "FAIL: $svc not active"
                sudo systemctl status "$svc" --no-pager
                since_ts=$(systemctl show -p InactiveExitTimestamp --value "$svc.service")
                if [ -n "$since_ts" ]; then
                  sudo journalctl -u "$svc" --no-pager --since "$since_ts"
                else
                  sudo journalctl -u "$svc" --no-pager -n 500
                fi
                exit 1
              fi
            done
```

with:

```yaml
            sleep 5
            services_to_check="svoted-val1 svoted-val2 svoted-val3"
            if [ -d "${{ env.DEPLOY_PATH }}/.svoted-archive" ]; then
              services_to_check="$services_to_check svoted-archive"
            fi
            for svc in $services_to_check; do
              if ! sudo systemctl is-active --quiet "$svc"; then
                echo "FAIL: $svc not active"
                sudo systemctl status "$svc" --no-pager
                since_ts=$(systemctl show -p InactiveExitTimestamp --value "$svc.service")
                if [ -n "$since_ts" ]; then
                  sudo journalctl -u "$svc" --no-pager --since "$since_ts"
                else
                  sudo journalctl -u "$svc" --no-pager -n 500
                fi
                exit 1
              fi
            done
```

Then, after the existing "Helper server healthy" curl block, add a new check for the archive. Find:

```yaml
            for i in $(seq 1 15); do
              if curl -sf http://127.0.0.1:1418/shielded-vote/v1/status > /dev/null 2>&1; then
                echo "Helper server healthy"
                break
              fi
              if [ "$i" = "15" ]; then
                echo "FAIL: Helper server /shielded-vote/v1/status not responding after 30s"
                dump_val1_diag
                exit 1
              fi
              sleep 2
            done
            echo "Chain + helper server both healthy"
```

Replace it with:

```yaml
            for i in $(seq 1 15); do
              if curl -sf http://127.0.0.1:1418/shielded-vote/v1/status > /dev/null 2>&1; then
                echo "Helper server healthy"
                break
              fi
              if [ "$i" = "15" ]; then
                echo "FAIL: Helper server /shielded-vote/v1/status not responding after 30s"
                dump_val1_diag
                exit 1
              fi
              sleep 2
            done
            if [ -d "${{ env.DEPLOY_PATH }}/.svoted-archive" ]; then
              for i in $(seq 1 30); do
                if curl -sf http://127.0.0.1:1718/cosmos/base/tendermint/v1beta1/blocks/latest > /dev/null 2>&1; then
                  echo "Archive LCD healthy (port 1718)"
                  break
                fi
                if [ "$i" = "30" ]; then
                  echo "FAIL: Archive LCD not responding on 1718 after 60s"
                  echo "--- systemctl status svoted-archive ---"
                  sudo systemctl status svoted-archive --no-pager
                  since_ts=$(systemctl show -p InactiveExitTimestamp --value svoted-archive.service)
                  if [ -n "$since_ts" ]; then
                    sudo journalctl -u svoted-archive --no-pager --since "$since_ts"
                  else
                    sudo journalctl -u svoted-archive --no-pager -n 500
                  fi
                  exit 1
                fi
                sleep 2
              done
              echo "Chain + helper server + archive all healthy"
            else
              echo "Chain + helper server healthy (archive not initialized — run reset_chain=true)"
            fi
```

- [ ] **Step 6.6: Validate the YAML parses**

Run: `python3 -c "import yaml; yaml.safe_load(open('.github/workflows/sdk-chain-reset.yml'))" && echo ok`

Expected: `ok`.

- [ ] **Step 6.7: Commit**

```bash
git add .github/workflows/sdk-chain-reset.yml
git commit -m "Wire archive into chain stop/init/start/verify

- Stop: include svoted-archive (it stops before val1 since systemd
  would cycle it anyway via Requires=).
- Install units: cp + enable svoted-archive.service if present.
- Reinit (reset_chain=true): run init-archive.sh after init_multi.sh.
- Start: start svoted-archive after val1/2/3.
- Verify: include archive in the is-active check; curl archive LCD at
  :1718 to confirm it's serving.

Archive is idempotent — safe to re-run init-archive.sh and
systemctl start svoted-archive on every workflow run."
```

---

## Task 7: Post-deploy smoke tests (manual, after first successful deploy to main)

No file changes; this documents what to verify once the branch lands and the two workflows run.

- [ ] **Step 7.1: Confirm the build-explorer job produced an artifact**

After merge to main, the `Deploy SDK chain` workflow run (triggered by the merge) should show a green `build-explorer` job. The Actions UI's "Artifacts" section should list `sdk-explorer-dist`.

- [ ] **Step 7.2: Confirm the archive is running on the box**

If you have SSH access to the deploy host:

```bash
ssh "$DEPLOY_HOST" "sudo systemctl is-active svoted-archive"
```

Expected: `active`.

- [ ] **Step 7.3: Confirm the archive is synced**

```bash
curl -s https://explorer-api.46-101-255-48.sslip.io/cosmos/base/tendermint/v1beta1/blocks/latest | jq '.block.header.height'
```

Expected: a quoted height string > `"0"`.

- [ ] **Step 7.4: Confirm the Tendermint RPC is reachable**

```bash
curl -s https://explorer-rpc.46-101-255-48.sslip.io/status | jq '.result.node_info.moniker'
```

Expected: `"archive"`.

- [ ] **Step 7.5: Confirm the explorer frontend serves**

```bash
curl -sI https://explorer.46-101-255-48.sslip.io/ | head -1
```

Expected: `HTTP/2 200` (Caddy runs HTTP/2 by default over HTTPS).

Open the URL in a browser: the ping-pub Vue app should render, show the latest block height, and let you navigate to validators/accounts/proposals.

- [ ] **Step 7.6: Confirm a chain reset leaves everything working**

Trigger `Reset SDK chain` workflow from the Actions UI with `reset_chain: true`. When it finishes:
- All four systemd services should be `active` again.
- The explorer should show height starting near 0 (chain just reset) and climbing as val1 produces blocks.

If any of 7.1-7.6 fail, treat the failure as a blocker for the PR — don't close as "done."

---

## Final notes

**What we did NOT build:**
- Custom `x/vote` tx render pages in ping-pub. Vote txs display as raw JSON. Follow-up.
- Local-dev archive target (e.g. `mise run multi:start-with-archive`). Out of scope — CI is the source of truth.
- Monitoring / alerts on archive sync lag.

**Rollback / kill-switch:**
If the explorer needs to be taken down fast without a full revert:
1. Comment out the three new Caddyfile blocks and push — `caddy-deploy.yml` reloads Caddy, subdomains start returning 404s.
2. On the box: `sudo systemctl stop svoted-archive && sudo systemctl disable svoted-archive`.
3. The validators are untouched by either action.

**PINGPUB_SHA bump:**
Change the SHA in three places when upgrading ping-pub:
- `.github/workflows/sdk-chain-deploy.yml` — `Cache yarn packages` key
- `.github/workflows/sdk-chain-deploy.yml` — `git checkout ...` in the Clone step
- `docs/superpowers/specs/2026-04-19-block-explorer-design.md` — reference note (optional, for discoverability)

Then push; CI rebuilds and ships.
