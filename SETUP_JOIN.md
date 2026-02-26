# Join the Zally Network as a Validator

There are two paths to join: **binary** (no repo needed) or **source** (for developers with the repo).

## Path A — Binary (no repo, no mise)

### Requirements

- Linux or macOS (amd64 or arm64)
- `curl` and `jq` installed
- Funded validator account (see Step 2)
- **Pre-built binaries on DO Spaces** — the `release.yml` GitHub Action must have run at least once to upload `zallyd` and `create-val-tx` to `vote.fra1.digitaloceanspaces.com`. If `join.sh` fails to download binaries, trigger a release first.

### Step 1 — Run join.sh

```bash
curl -fsSL https://vote.fra1.digitaloceanspaces.com/join.sh | bash
```

You will be prompted for a **moniker** (a display name for your validator). To run non-interactively:

```bash
ZALLY_MONIKER=my-validator \
  curl -fsSL https://vote.fra1.digitaloceanspaces.com/join.sh | bash
```

#### Optional env vars

| Variable | Default | Purpose |
|---|---|---|
| `ZALLY_MONIKER` | *(prompted)* | Validator display name |
| `ZALLY_INSTALL_DIR` | `~/.local/bin` | Where to install `zallyd` and `create-val-tx` |
| `ZALLY_HOME` | `~/.zallyd` | Node home directory |

When `join.sh` finishes it prints your validator address. Save it for Step 2.

### Step 2 — Fund your account

Your account must hold stake before it can register as a validator. Ask a teammate to trigger the **"Fund validator"** GitHub Action with your address from Step 1.

### Step 3 — Start the node

```bash
~/.zallyd/start.sh
```

This starts zallyd, waits for sync, and registers you as a validator automatically.

---

## Path B — Source (has repo, uses mise)

### Step 1 — Build and join

```bash
cd zally
mise install              # pin Go/Rust/Node versions
mise run validator:join    # builds from source, downloads genesis, generates start.sh
```

This runs `join-dev.sh` which builds `zallyd` + `create-val-tx` from source, fetches the genesis and network config, and produces `~/.zallyd/start.sh`.

### Step 2 — Fund your account

Same as Path A — trigger the **"Fund validator"** GitHub Action with your address.

### Step 3 — Start the node

```bash
~/.zallyd/start.sh
```

---

## Verify

Once `start.sh` reports "Validator registered", confirm you appear in the validator set:

```bash
zallyd query staking validators --node tcp://localhost:26657
```

## Ceremony Participation

The EA key ceremony is automatic. When a new voting round is created, your validator is included in the ceremony if it is bonded and has a registered Pallas key (done automatically by `join.sh` / `join-dev.sh`). The block proposer handles dealing and acking via `PrepareProposal` — no manual steps required.

If your validator fails to ack in 3 consecutive ceremonies, it will be jailed. Any bonded validator can unjail a jailed validator using the admin UI (click the "Unjail" button on the jailed validator's card). Unjailing also resets the ceremony miss counter.

Check ceremony status:

```bash
mise status
# or
curl -s localhost:1318/zally/v1/rounds | jq
```

## Useful commands

```bash
# Check sync status
zallyd status --home ~/.zallyd | jq '.sync_info'

# Follow node logs
tail -f ~/.zallyd/node.log

# Stop the node
pkill zallyd

# Restart the node (after stopping)
~/.zallyd/start.sh
```

## Chain info

| | |
|---|---|
| Chain ID | `zvote-1` |
| P2P port | `26656` |
| RPC port | `26657` (localhost only) |
| REST API port | `1318` (localhost only) |
| Node home | `~/.zallyd` |
