# Auto-deploy setup for SDK chain (zallyd)

The workflow `.github/workflows/sdk-chain-deploy.yml` builds zallyd (with circuits FFI) on every push to `main` (when `sdk/**` changes) and deploys to a remote host via SSH. Each deploy runs `init.sh` for a fresh chain, then restarts the systemd service.

## 1. GitHub repository secrets

In the repo: **Settings → Secrets and variables → Actions**, add (or reuse from nullifier-ingest):

| Secret              | Description |
|---------------------|-------------|
| `DEPLOY_HOST`       | Remote hostname or IP (e.g. `chain.example.com`). |
| `DEPLOY_USER`       | SSH user on that host (e.g. `deploy` or `root`). |
| `SSH_PASSWORD`      | SSH password for that user. |

## 2. One-time setup on the remote host

### Deploy directory

Create the deploy directory. Default in the workflow is `DEPLOY_PATH: /opt/zally-chain`.

```bash
sudo mkdir -p /opt/zally-chain
sudo chown $DEPLOY_USER:$DEPLOY_USER /opt/zally-chain
```

### Systemd unit

Copy the unit file from the repo and enable it:

```bash
sudo cp sdk/docs/zallyd.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable zallyd
```

The service will start automatically after the first deploy runs `init.sh` and triggers `systemctl restart zallyd`.

No pre-existing chain data is needed — the first deploy will initialize a fresh chain.

## 3. What happens on each deploy

1. **Build** (on GitHub runner): Go + Rust circuits are compiled, producing the `zallyd` binary and copying `init.sh`.
2. **Deploy**: `zallyd` and `init.sh` are SCP'd to `/opt/zally-chain` on the remote host.
3. **Init**: `init.sh` runs with `HOME=/opt/zally-chain`, so chain data lives at `/opt/zally-chain/.zallyd`. This wipes and reinitializes the chain on every deploy.
4. **Restart**: `sudo systemctl restart zallyd` starts the chain with the new binary and fresh data.

## 4. Changing deploy path or restart command

- **Deploy path**: Edit `env.DEPLOY_PATH` in `.github/workflows/sdk-chain-deploy.yml` (default `/opt/zally-chain`). Also update the systemd unit file's `ExecStart` and `WorkingDirectory`.
- **Restart command**: Edit the "Restart service" step if you use a different service name.

## 5. Manual runs

The workflow has `workflow_dispatch`, so you can run it from **Actions → Deploy SDK chain → Run workflow** without pushing to `main`.

## 6. Same host as nullifier-ingest

If the same machine is used for both nullifier-ingest and the SDK chain, that's fine — they use different deploy paths (`/opt/nullifier-ingest` vs `/opt/zally-chain`) and different systemd units.
