# Auto-deploy setup for SDK chain (svoted) — 3-validator

The workflow `.github/workflows/sdk-chain-deploy.yml` builds svoted (with circuits FFI) on every push to `main` (when `sdk/**` changes) and deploys a 3-validator chain to a single remote host via SSH. On `reset_chain`, the chain is fully re-initialized and the EA key ceremony is bootstrapped so the chain is immediately ready for use.

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

| Secret         | Scope       | Description                                       |
| -------------- | ----------- | ------------------------------------------------- |
| `DEPLOY_HOST`  | Repository  | Remote hostname or IP (e.g. `chain.example.com`). |
| `DEPLOY_USER`  | Repository  | SSH user on that host (e.g. `deploy` or `root`).  |
| `SSH_PASSWORD` | Repository  | SSH password for that user.                       |
| `CEREMONY_SSH_KEY` | Environment (`production`) | Ed25519 private key for ceremony bootstrap SSH. |

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

Each unit starts `svoted` with a separate `--home` directory:

| Unit          | Home directory                        |
|---------------|---------------------------------------|
| svoted-val1   | /opt/shielded-vote/.svoted-val1         |
| svoted-val2   | /opt/shielded-vote/.svoted-val2         |
| svoted-val3   | /opt/shielded-vote/.svoted-val3         |

No pre-existing chain data is needed — the first deploy with `reset_chain=true` will initialize everything.

## 3. What happens on each deploy

### Binary-only update (default, `reset_chain=false`)

1. **Build**: Go + Rust circuits are compiled, producing `svoted`, `create-val-tx`, and `init_multi.sh`.
2. **Deploy**: Binaries and scripts are SCP'd to `/opt/shielded-vote`.
3. **Stop**: `svoted-val1/2/3` are stopped and ports confirmed free.
4. **Start**: All three services are restarted with the new binary.
5. **Verify**: Val1's API (port 1418) and helper server are checked.
6. **Ceremony**: Runs against val1; skipped if ceremony is already confirmed.

### Full reset (`reset_chain=true`)

Steps 1–2 are the same, then:

3. **Stop**: All three services stopped.
4. **Init**: `init_multi.sh --ci` runs with `HOME=/opt/shielded-vote`, initializing fresh home directories for all three validators. Val2 and val3 get their genesis, keys, and port config; val1 also gets the helper server configured.
5. **Start**: All three services started.
6. **Register**: `create-val-tx` registers val2 and val3 as post-genesis validators via val1's REST API.
7. **Verify**: Service health + chain API + helper server checked.
8. **Ceremony**: EA key ceremony bootstrapped on val1.

## 4. Caddy reverse proxy

Caddy proxies HTTPS traffic to val1's REST API (port 1418). Update and reload:

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
| `api_token`             | `""`    | Optional token for `POST /api/v1/shares` (`X-Helper-Token` header).                                       |
| `db_path`               | `""`    | Path to SQLite database. Empty = `$HOME/.svoted-val1/helper.db`.                                          |
| `process_interval`      | `5`     | How often to check for ready shares (seconds).                                                            |
| `chain_api_port`        | `1418`  | Port of val1's REST API (for `MsgRevealShare` submission).                                                 |
| `max_concurrent_proofs` | `2`     | Maximum parallel proof generation goroutines (~500MB RAM each).                                           |

## 7. Deploy health checks

After services are started, the workflow verifies:
1. All three systemd services (`svoted-val1/2/3`) are active
2. Val1's chain API responds at `http://localhost:1418/shielded-vote/v1/commitment-tree/latest`
3. Val1's helper server responds at `http://localhost:1418/api/v1/status`

If any check fails, the deploy step fails with `journalctl` output for debugging.

## 8. Checking logs on the remote

```bash
# Val1 (primary — chain API, helper server)
sudo journalctl -u svoted-val1 -f

# Val2 / Val3
sudo journalctl -u svoted-val2 -f
sudo journalctl -u svoted-val3 -f

# Or tail log files directly
tail -f /opt/shielded-vote/.svoted-val1/node.log
```

## 9. Same host as nullifier-ingest

If the same machine is used for both nullifier-ingest and the SDK chain, that's fine — they use different deploy paths (`/opt/nullifier-ingest` vs `/opt/shielded-vote`) and different systemd units.
