# Auto-deploy setup for SDK chain (svoted) — 3-validator

The workflow `.github/workflows/sdk-chain-deploy.yml` builds svoted (with circuits FFI) and the admin UI on every push to `main` and deploys a 3-validator chain to a single remote host via SSH. On `reset_chain`, the chain is fully re-initialized and validators are registered so the chain is immediately ready for use.

## Port layout

All three validators run on the same host with non-overlapping port sets:

| Validator | P2P   | RPC   | REST API | pprof |
|-----------|-------|-------|----------|-------|
| val1      | 26156 | 26157 | 1418     | 6160  |
| val2      | 26256 | 26257 | 1518     | 6260  |
| val3      | 26356 | 26357 | 1618     | 6360  |

Val1 is the genesis validator and is the primary API endpoint (reverse-proxied by Caddy). Val2 and val3 join after chain start via `MsgCreateValidatorWithPallasKey`.

## 1. GitHub repository secrets

In the repo: **Settings → Secrets and variables → Actions**, add:

| Secret             | Scope       | Description                                                          |
| ------------------ | ----------- | -------------------------------------------------------------------- |
| `DEPLOY_HOST`      | Repository  | Remote hostname or IP (e.g. `chain.example.com`).                    |
| `DEPLOY_USER`      | Repository  | SSH user on that host (e.g. `deploy` or `root`).                     |
| `SSH_PASSWORD`     | Repository  | SSH password for that user.                                          |
| `VM_PRIVKEY`       | Repository  | 64-char hex secp256k1 private key for the bootstrap vote-manager.    |
| `CEREMONY_SSH_KEY` | Environment (`production`) | Ed25519 private key for ceremony bootstrap SSH.       |

Generate `VM_PRIVKEY` with `openssl rand -hex 32`. This key is imported as the vote-manager account during chain initialization. **Never commit it to the repository** — locally it is loaded from `.env` (see `.env.example`).

The `CEREMONY_SSH_KEY` secret lives in the GitHub **production** environment (Settings → Environments → production). Generate the keypair and authorize it on the remote:

```bash
ssh-keygen -t ed25519 -C "github-actions-ceremony" -f /tmp/shielded-vote-ci-key -N ""
# Add public key to remote
cat /tmp/shielded-vote-ci-key.pub | ssh root@<DEPLOY_HOST> 'mkdir -p ~/.ssh && cat >> ~/.ssh/authorized_keys'
# Copy private key contents into the CEREMONY_SSH_KEY secret
cat /tmp/shielded-vote-ci-key
```

## 2. One-time setup on the remote host

### Deploy directory

```bash
sudo mkdir -p /opt/shielded-vote
sudo chown $DEPLOY_USER:$DEPLOY_USER /opt/shielded-vote
```

### Systemd units

Install all three validator unit files and enable them:

```bash
sudo cp sdk/docs/svoted-val1.service /etc/systemd/system/
sudo cp sdk/docs/svoted-val2.service /etc/systemd/system/
sudo cp sdk/docs/svoted-val3.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable svoted-val1 svoted-val2 svoted-val3
```

Each unit starts `svoted` with a separate `--home` directory. Val1 additionally serves the admin UI via `--serve-ui --ui-dist`:

| Unit          | Home directory                   | Notes                                     |
|---------------|----------------------------------|--------------------------------------------|
| svoted-val1   | /opt/shielded-vote/.svoted-val1  | `--serve-ui --ui-dist /opt/shielded-vote/ui/dist` |
| svoted-val2   | /opt/shielded-vote/.svoted-val2  |                                            |
| svoted-val3   | /opt/shielded-vote/.svoted-val3  |                                            |

Service files are **auto-deployed** by the CI pipeline on each deploy (copied from `docs/svoted-val*.service` in the repo to `/etc/systemd/system/` with `daemon-reload`). The manual `cp` above is only needed for the initial `systemctl enable`.

No pre-existing chain data is needed — the first deploy with `reset_chain=true` will initialize everything.

## 3. What happens on each deploy

### Binary-only update (default, `reset_chain=false`)

1. **Build**: Go + Rust circuits are compiled, producing `svoted`, `create-val-tx`, and `init_multi.sh`. The admin UI is built (`cd ui && npm install && npm run build`).
2. **Deploy**: Binaries, scripts, `ui/dist`, and `docs/svoted-val*.service` files are SCP'd to `/opt/shielded-vote`.
3. **Stop**: `svoted-val1/2/3` are stopped and ports confirmed free.
4. **Install units**: Updated service files are copied to `/etc/systemd/system/` and `systemctl daemon-reload` runs.
5. **Start**: All three services are restarted with the new binary and UI.
6. **Verify**: Val1's API (port 1418), helper server, and admin UI are checked.

### Full reset (`reset_chain=true`)

Steps 1–4 are the same, then:

5. **Init**: `init_multi.sh --ci` runs with `HOME=/opt/shielded-vote`, initializing fresh home directories for all three validators. Val2 and val3 get their genesis, keys, and port config; val1 also gets the helper and admin servers configured.
6. **Start**: All three services started.
7. **Register**: `create-val-tx` registers val2 and val3 as post-genesis validators via val1's REST API. A 6-second wait follows the last registration to ensure the tx is committed before restarts.
8. **Restart**: All three services are restarted (staggered, 5s apart) so helpers re-register with bonded validators.
9. **Verify**: Service health + chain API + helper server + admin UI checked.

## 4. Caddy reverse proxy

Caddy proxies HTTPS traffic to val1's REST API (port 1418), which also serves the admin UI. Update and reload:

```bash
make caddy   # from the sdk/ directory
```

Or manually:

```bash
sudo cp deploy/Caddyfile /etc/caddy/Caddyfile && sudo systemctl restart caddy
```

## 5. Manual runs

The workflow has `workflow_dispatch`, so you can run it from **Actions → Deploy SDK chain → Run workflow** without pushing to `main`. Enable `reset_chain` to wipe and reinitialize the chain.

## 6. Helper server configuration

The helper server runs inside `svoted` on **val1 only** and shares val1's REST API port (1418). It is configured in `/opt/shielded-vote/.svoted-val1/config/app.toml` under `[helper]` (written by `init_multi.sh --ci`):

| Key                     | Default | Description                                                                                               |
| ----------------------- | ------- | --------------------------------------------------------------------------------------------------------- |
| `disable`               | `false` | Set to `true` to disable the helper server entirely.                                                      |
| `api_token`             | `""`    | Optional token for `POST /shielded-vote/v1/shares` (`X-Helper-Token` header).                              |
| `db_path`               | `""`    | Path to SQLite database. Empty = `$HOME/.svoted-val1/helper.db`.                                          |
| `process_interval`      | `5`     | How often to check for ready shares (seconds).                                                            |
| `chain_api_port`        | `1418`  | Port of val1's REST API (for `MsgRevealShare` submission).                                                 |
| `max_concurrent_proofs` | `2`     | Maximum parallel proof generation goroutines (~500MB RAM each).                                           |

## 7. Admin UI

The admin UI is a React SPA (Vite + TypeScript) that lives in `ui/` and is built during CI. It is served **in-process** by `svoted` on val1's REST API port (1418) via the `--serve-ui --ui-dist` flags. Caddy reverse-proxies it at `https://46-101-255-48.sslip.io/`.

The UI uses same-origin relative paths for all API calls (`/cosmos/...`, `/shielded-vote/...`, `/api/...`), so it works without any hardcoded server URLs. A `[ui]` section exists in `app.toml` (`enable`, `dist_path`) but is superseded by the CLI flags on the systemd unit.

To build and test locally:

```bash
make start-admin   # builds UI then starts svoted with --serve-ui
```

## 8. Admin server configuration

The admin server is a thin proxy that fetches the voting-config JSON from the GitHub Pages CDN (`valargroup/token-holder-voting-config`). Server registration, approval, and removal happen via PRs on that config repo — no write endpoints here.

It runs inside `svoted` on **val1 only** and shares val1's REST API port. It is configured in `app.toml` under `[admin]` (written by `init_multi.sh --ci`):

| Key          | Default  | Description                                                                                |
| ------------ | -------- | ------------------------------------------------------------------------------------------ |
| `disable`    | `true`   | Set to `false` to enable the config proxy on this validator.                               |
| `config_url` | staging  | GitHub Pages CDN URL for the voting-config JSON. Defaults to the staging environment.      |

A single read-only endpoint is served: `GET /api/voting-config`.

## 9. Deploy health checks

After services are started, the workflow verifies:
1. All three systemd services (`svoted-val1/2/3`) are active
2. Val1's chain API responds at `http://localhost:1418/shielded-vote/v1/rounds`
3. Val1's helper server responds at `http://localhost:1418/shielded-vote/v1/status`
4. Val1's admin UI responds at `http://localhost:1418/` (contains `<div id="root">`)
5. Val1's admin API responds at `http://localhost:1418/api/voting-config`

If any check fails, the deploy step fails with `journalctl` output for debugging.

## 10. Checking logs on the remote

```bash
# Val1 (primary — chain API, helper server)
sudo journalctl -u svoted-val1 -f

# Val2 / Val3
sudo journalctl -u svoted-val2 -f
sudo journalctl -u svoted-val3 -f

# Or tail log files directly
tail -f /opt/shielded-vote/.svoted-val1/node.log
```

## 11. PIR server (nf-server)

The `nf-server` binary from [`vote-nullifier-pir`](https://github.com/valargroup/vote-nullifier-pir) runs as a separate service (not embedded in `svoted`). See that repo for build, data bootstrap, and systemd setup instructions.

`svoted` communicates with `nf-server` over HTTP via the `SVOTE_PIR_URL` environment variable. The val1 systemd unit sets `SVOTE_PIR_URL=http://localhost:3000` by default. If `nf-server` runs on a different host or port, update the variable accordingly.

## 12. Same host as nullifier-ingest

If the same machine is used for both nullifier-ingest and the SDK chain, that's fine — they use different deploy paths (`/opt/nullifier-ingest` vs `/opt/shielded-vote`) and different systemd units.
