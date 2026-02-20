# Demo Setup — Feb 20

PR: [#121](https://github.com/z-cale/Shielded-Vote/pull/121) (branch: `adam/demo-infra`). Revert this PR to undo all changes below.

## What's ready

- [x] Prod chain running at `46.101.255.48` (3 validators, ceremony CONFIRMED)
- [x] Release workflow (`.github/workflows/release.yml`) — builds binaries on `v*` tag
- [x] Fund-validator workflow (`.github/workflows/fund-validator.yml`) — sends 5 stake via GH Actions UI
- [x] `join.sh` — one-command join script for new validators

## Before the demo (checklist)

These must be done **before** Jason runs `join.sh`:

Genesis and network config are already uploaded to DO Spaces:
- https://vote.fra1.digitaloceanspaces.com/genesis.json
- https://vote.fra1.digitaloceanspaces.com/network.json

### 1. Merge PR and tag a release

```bash
# After PR is merged to main:
git tag v0.0.1
git push origin v0.0.1
```

Wait for the Release workflow to complete (~10 min). Verify:
```bash
gh release view v0.0.1
# Should show a zally-v0.0.1-linux-amd64.tar.gz asset
```

## Jason's experience

### Step 1 — Run the join script

On a fresh Ubuntu server (requires a GitHub PAT with `repo` scope since the repo is private):

```bash
export GITHUB_TOKEN=ghp_xxx
curl -fsSL -H "Authorization: token $GITHUB_TOKEN" https://raw.githubusercontent.com/z-cale/zally/main/join.sh | bash
```

Prompts for a validator name, then downloads binaries, genesis, and configures everything. At the end it prints the validator address and path to `start.sh`.

### Step 2 — Get funded

A teammate goes to GitHub → Actions → **Fund validator** → Run workflow → paste Jason's address.

### Step 3 — Start

```bash
~/.zallyd/start.sh
```

This starts the node, waits for sync, checks if already registered, and if not, registers as a validator automatically. One command.
