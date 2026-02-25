# Nullifier Service Setup

> **Developer shortcut:** If you have the full repo and mise, `mise start` handles nullifier bootstrap + ingestion + query server automatically. This guide is for standalone or manual setup.

The nullifier service ingests Orchard nullifiers from the Zcash chain and serves exclusion proofs to voters. It must be running before any votes can be constructed — the voting UI and e2e tests query it to generate non-inclusion proofs that prevent double-voting.

## What it does

- **`ingest-nfs`** — incrementally fetches 32-byte Orchard nullifiers from a lightwallet server (`zec.rocks` by default) into flat binary files (`nullifiers.bin`, `nullifiers.checkpoint`, `nullifiers.tree`).
- **`query-server`** — reads those files and exposes an HTTP API (default port `3000`) that returns Merkle non-inclusion proofs for a given nullifier.

Both binaries are built from `nullifier-ingest/service/` (Rust, release mode).

## Requirements

- Linux or macOS (amd64 or arm64)
- Internet access to download the nullifier snapshot and sync from the lightwallet server
- `./nullifier.sh run` installs all other dependencies automatically

## Quick start

From the repo root, run the fully automated setup:

```bash
./nullifier.sh run
```

This performs three steps in sequence:

1. **Install dependencies** — installs system packages (`build-essential`, `wget`, `pkg-config`, `libssl-dev`) and Rust/cargo via `rustup` if not already present.
2. **Bootstrap** — downloads a pre-built nullifier snapshot (`nullifiers.bin`, `nullifiers.checkpoint`, `nullifiers.tree`) from the Zally bootstrap server so you don't have to sync from block 0.
3. **Serve** — compiles `query-server` in release mode and starts it. The first build takes a few minutes; subsequent starts are fast.

The server runs in the foreground. Press `Ctrl+C` to stop it.

To run in the background and log output to a file:

```bash
nohup ./nullifier.sh serve > nullifier-serve.log 2>&1 &
echo "PID: $!"
```

## Step-by-step

If you prefer to run each phase separately:

### Step 1 — Install dependencies

```bash
./nullifier.sh install-deps
```

Installs:
- Linux: `build-essential`, `wget`, `pkg-config`, `libssl-dev`, `curl` via `apt-get`
- macOS: `wget` via Homebrew (if not already present)
- Rust stable toolchain via `rustup` (skipped if `cargo` is already in `PATH`)

### Step 2 — Bootstrap nullifier snapshot

```bash
./nullifier.sh bootstrap
```

Downloads the pre-built snapshot files into `nullifier-ingest/` if they are not already present:

| File | Description |
|---|---|
| `nullifiers.bin` | Append-only flat file of 32-byte nullifier blobs |
| `nullifiers.checkpoint` | 16-byte crash-recovery marker (last synced height + file offset) |
| `nullifiers.tree` | Cached full Merkle tree sidecar |

If the checkpoint file already exists the download is skipped (idempotent).

### Step 3 — Start the query server

```bash
./nullifier.sh serve
```

Compiles and starts `query-server`. On the first run Cargo downloads and compiles all dependencies — this typically takes 3–5 minutes. Subsequent starts compile only changed code.

Once running, the server listens on `http://localhost:3000` and accepts exclusion proof requests from the voting UI and e2e tests.

## Check status

```bash
./nullifier.sh status
```

Prints:
- Number of ingested nullifiers and file size
- Last synced block height from the checkpoint
- Tree sidecar presence and size
- Whether `query-server` is currently running

## Ingesting new nullifiers

After the initial bootstrap the snapshot may be behind the chain tip. To sync new nullifiers incrementally (safe to run while the server is stopped):

```bash
mise run nullifier:ingest
# or: make -C nullifier-ingest ingest
```

Or with a specific upper bound (must be a multiple of 10):

```bash
SYNC_HEIGHT=2600000 mise run nullifier:ingest
# or: make -C nullifier-ingest ingest SYNC_HEIGHT=2600000
```

After ingesting, restart the server so it picks up the new data:

```bash
./nullifier.sh serve
```

## Environment overrides

All defaults can be overridden via environment variables:

| Variable | Default | Description |
|---|---|---|
| `LWD_URL` | `https://zec.rocks:443` | Lightwallet server to fetch nullifiers from |
| `DATA_DIR` | `nullifier-ingest` | Directory for `nullifiers.bin` / `.checkpoint` / `.tree` |
| `PORT` | `3000` | Port for the exclusion proof query server |
| `BOOTSTRAP_URL` | `https://vote.fra1.digitaloceanspaces.com` | Base URL for snapshot file downloads |

Example — use a custom lightwallet server and data directory:

```bash
LWD_URL=https://my-node:443 DATA_DIR=/data/nullifiers ./nullifier.sh run
```

## Useful commands

```bash
# Check how many nullifiers have been ingested
mise run nullifier:status
# or: make -C nullifier-ingest status

# Run unit tests for the nullifier-tree and service crates
mise run test:nullifier
# or: make -C nullifier-ingest test

# Verify exclusion proofs against the ingested data
mise run test:proof
# or: make -C nullifier-ingest test-proof

# Stop the query server
pkill query-server

# Remove all build artifacts and data files (destructive)
mise run nullifier:clean
# or: make -C nullifier-ingest clean
```
