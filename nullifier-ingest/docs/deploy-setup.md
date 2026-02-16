# Auto-deploy setup for nullifier-ingest

The workflow `.github/workflows/nullifier-ingest-deploy.yml` builds nullifier-ingest on every push to `main` (when `nullifier-ingest/**` changes) and deploys the binaries to a remote host via SSH.

## 0. Moving cached data to `/root/zally/nullifier-service`

The service uses two cached files: the SQLite DB and a sidecar tree file. To move them from the old location to `/root/zally/nullifier-service`:

```bash
# Create target directory
sudo mkdir -p /root/zally/nullifier-service

# Move the database and tree sidecar (stop the service first if it’s running)
sudo systemctl stop nullifier-query-server || true
sudo mv /root/zally/nullifier-ingest/service/nullifiers.db      /root/zally/nullifier-service/
sudo mv /root/zally/nullifier-ingest/service/nullifiers.db.tree /root/zally/nullifier-service/

# Ensure the deploy user can read them (if deploy runs as a different user)
# sudo chown -R DEPLOY_USER:DEPLOY_USER /root/zally/nullifier-service

# Restart the service using the new path (see systemd unit below)
```

Configure the service to use the new path: set `DB_PATH=/root/zally/nullifier-service/nullifiers.db`. The server will then use the sidecar at `nullifiers.db.tree` in the same directory. If you deploy binaries to the same directory (see workflow `DEPLOY_PATH`), use that path in your systemd unit as in the example below.

## 1. GitHub repository secrets

In the repo: **Settings → Secrets and variables → Actions**, add:

| Secret              | Description |
|---------------------|-------------|
| `DEPLOY_HOST`       | Remote hostname or IP (e.g. `ingest.example.com` or `192.0.2.10`). |
| `DEPLOY_USER`       | SSH user on that host (e.g. `deploy` or `ubuntu`). |
| `SSH_PASSWORD`      | SSH password for that user. |

The deploy job will copy `query-server` and `ingest-nfs` to the remote and run a restart command (see below).

## 2. One-time setup on the remote host

### Directory and binaries

- Create the deploy directory. Default in the workflow is `DEPLOY_PATH: /opt/nullifier-ingest`; you can change it to `/root/zally/nullifier-service` so binaries and data live together.
- Ensure the SSH user can write to that directory (e.g. `sudo mkdir -p /root/zally/nullifier-service && sudo chown $DEPLOY_USER /root/zally/nullifier-service`).
- Put `nullifiers.db` and `nullifiers.db.tree` in that directory (or in a separate data dir and set `DB_PATH` accordingly; see section 0).

### Query server (HTTP API)

The `query-server` binary serves the exclusion-proof API. It needs:

- **Database**: A SQLite DB of ingested nullifiers. Either copy an existing `nullifiers.db` to the host or run `ingest-nfs` first (see below).
- **Port**: Set `PORT` (default 3000) when running.

Example **systemd unit** (using data dir `/root/zally/nullifier-service`). Adjust paths if you use a different `DEPLOY_PATH`:

`/etc/systemd/system/nullifier-query-server.service`:

```ini
[Unit]
Description=Nullifier ingest query server
After=network.target

[Service]
Type=simple
User=root
WorkingDirectory=/root/zally/nullifier-service
Environment="DB_PATH=/root/zally/nullifier-service/nullifiers.db"
Environment="PORT=3000"
ExecStart=/root/zally/nullifier-service/query-server
Restart=on-failure

[Install]
WantedBy=multi-user.target
```

Then:

```bash
sudo systemctl daemon-reload
sudo systemctl enable nullifier-query-server
sudo systemctl start nullifier-query-server
```

After that, each deploy will run `sudo systemctl restart nullifier-query-server` (the workflow uses `|| true` so it won’t fail if you haven’t created the unit yet).

### Ingest (optional)

`ingest-nfs` fills the SQLite DB from the chain. Run it periodically (cron or systemd timer) on the same host, e.g.:

- `DB_PATH=/root/zally/nullifier-service/nullifiers.db LWD_URL=https://zec.rocks:443 /root/zally/nullifier-service/ingest-nfs`

No restart is run for ingest; only the binary is updated on deploy.

## 3. Changing deploy path or restart command

- **Deploy path**: Edit the `env.DEPLOY_PATH` in `.github/workflows/nullifier-ingest-deploy.yml` (default `/opt/nullifier-ingest`).
- **Restart command**: Edit the “Restart service” step in that workflow if you use a different service name or script (e.g. a custom `restart.sh`).

## 4. Manual runs

The workflow has `workflow_dispatch`, so you can run it from **Actions → Deploy nullifier-ingest → Run workflow** without pushing to `main`.

## 5. Test locally before CI

**Option A – Make target (recommended)**  
From `nullifier-ingest/`:

```bash
# Copy your cached files into the default data dir (or set DATA_DIR)
mkdir -p nullifier-service
cp /path/to/nullifiers.db nullifier-service/
cp /path/to/nullifiers.db.tree nullifier-service/

make serve-deploy
```

This builds the release binaries (same as CI) and runs `query-server` with `DB_PATH=nullifier-service/nullifiers.db`. Then open `http://localhost:3000/health` and `http://localhost:3000/root`. Override the data dir with `make serve-deploy DATA_DIR=/root/zally/nullifier-service` (or any path that contains `nullifiers.db` and `nullifiers.db.tree`).

**Option B – Run the deploy workflow locally with act**  
If you have [act](https://github.com/nektos/act) and Docker:

```bash
# List events (push to main, or workflow_dispatch)
act -n -W .github/workflows/nullifier-ingest-deploy.yml

# Run the workflow (will prompt for secrets or use .secrets file)
act push -W .github/workflows/nullifier-ingest-deploy.yml
```

Use a `.secrets` file (gitignored) with `DEPLOY_HOST`, `DEPLOY_USER`, `SSH_PASSWORD` so you don’t type them. The deploy job will run against your real host.
