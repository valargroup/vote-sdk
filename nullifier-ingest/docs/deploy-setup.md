# Auto-deploy setup for nullifier-ingest

The workflow `.github/workflows/nullifier-ingest-deploy.yml` builds nullifier-ingest on every push to `main` (when `nullifier-ingest/**` changes) and deploys the binaries to a remote host via SSH.

## 0. Moving cached data to the deploy directory

The service uses two cached files: the SQLite DB and a sidecar tree file. To move them into the deploy directory (default `/opt/nullifier-ingest`):

```bash
# Create target directory (default matches workflow DEPLOY_PATH)
sudo mkdir -p /opt/nullifier-ingest

# Move the database and tree sidecar (stop the service first if it’s running)
sudo systemctl stop nullifier-query-server || true
sudo mv /path/to/nullifiers.db      /opt/nullifier-ingest/
sudo mv /path/to/nullifiers.db.tree /opt/nullifier-ingest/

# Ensure the deploy user can write (if deploy runs as a different user)
# sudo chown -R DEPLOY_USER:DEPLOY_USER /opt/nullifier-ingest

# Restart the service (see systemd unit below)
```

Configure the service to use that path: set `DB_PATH=/opt/nullifier-ingest/nullifiers.db`. The server will then use the sidecar at `nullifiers.db.tree` in the same directory. The unit file in `docs/nullifier-query-server.service` uses this path by default.

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

- Create the deploy directory. Default in the workflow is `DEPLOY_PATH: /opt/nullifier-ingest`.
- Ensure the SSH user can write to that directory (e.g. `sudo mkdir -p /opt/nullifier-ingest && sudo chown $DEPLOY_USER /opt/nullifier-ingest`).
- Put `nullifiers.db` and `nullifiers.db.tree` in that directory (or in a separate data dir and set `DB_PATH` accordingly; see section 0).

### Query server (HTTP API)

The `query-server` binary serves the exclusion-proof API. It needs:

- **Database**: A SQLite DB of ingested nullifiers. Either copy an existing `nullifiers.db` to the host or run `ingest-nfs` first (see below).
- **Port**: Set `PORT` (default 3000) when running.

Example **systemd unit** (using default deploy path `/opt/nullifier-ingest`, matching the workflow `DEPLOY_PATH`). A copyable unit file is in `docs/nullifier-query-server.service`; copy to `/etc/systemd/system/` and adjust paths if you use a different `DEPLOY_PATH`:

```bash
sudo cp nullifier-ingest/docs/nullifier-query-server.service /etc/systemd/system/
```

Or create `/etc/systemd/system/nullifier-query-server.service` with:

```ini
[Unit]
Description=Nullifier ingest query server
After=network.target

[Service]
Type=simple
User=root
WorkingDirectory=/opt/nullifier-ingest
Environment="DB_PATH=/opt/nullifier-ingest/nullifiers.db"
Environment="PORT=3000"
ExecStart=/opt/nullifier-ingest/query-server
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

- `DB_PATH=/opt/nullifier-ingest/nullifiers.db LWD_URL=https://zec.rocks:443 /opt/nullifier-ingest/ingest-nfs`

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

This builds the release binaries (same as CI) and runs `query-server` with `DB_PATH=nullifier-service/nullifiers.db`. Then open `http://localhost:3000/health` and `http://localhost:3000/root`. Override the data dir with `make serve-deploy DATA_DIR=/opt/nullifier-ingest` (or any path that contains `nullifiers.db` and `nullifiers.db.tree`).

**Option B – Run the deploy workflow locally with act**  
If you have [act](https://github.com/nektos/act) and Docker:

```bash
# List events (push to main, or workflow_dispatch)
act -n -W .github/workflows/nullifier-ingest-deploy.yml

# Run the workflow (will prompt for secrets or use .secrets file)
act push -W .github/workflows/nullifier-ingest-deploy.yml
```

Use a `.secrets` file (gitignored) with `DEPLOY_HOST`, `DEPLOY_USER`, `SSH_PASSWORD` so you don’t type them. The deploy job will run against your real host.
