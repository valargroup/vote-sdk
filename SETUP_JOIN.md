# Join the Zally Network as a Validator

There are two paths to join: **binary** (no repo needed) or **source** (for developers with the repo).

## Path A — Binary (no repo, no mise)

### Requirements

- Linux or macOS (amd64 or arm64)
- `curl` and `jq` installed
- Funded validator account (see Step 2)
- **Pre-built binaries on DO Spaces** — the `release.yml` GitHub Action must have run at least once to upload `zallyd` and `create-val-tx` to `vote.fra1.digitaloceanspaces.com`. If `join.sh` fails to download binaries, trigger a release first.
- **At least one validator registered in Edge Config** — the bootstrap operator must have registered a validator's public URL in the admin UI so that `join.sh` can discover the network.

### Step 1 — Run join.sh

```bash
curl -fsSL https://vote.fra1.digitaloceanspaces.com/join.sh | bash
```

You will be prompted for a **moniker** (a display name for your validator). To run non-interactively:

```bash
ZALLY_MONIKER=my-validator \
  curl -fsSL https://vote.fra1.digitaloceanspaces.com/join.sh | bash
```

#### What join.sh does

1. Downloads pre-built binaries from DO Spaces (or uses local if already in PATH)
2. Queries the Vercel voting-config API to discover a live validator
3. Fetches `genesis.json` from the discovered validator's `/zally/v1/genesis` endpoint
4. Fetches the validator's P2P node identity from `/cosmos/base/tendermint/v1beta1/node_info`
5. Initializes the node, generates keys, configures CometBFT with the discovered peer
6. Produces `~/.zallyd/start.sh` for starting the node

#### Optional env vars

| Variable | Default | Purpose |
|---|---|---|
| `ZALLY_MONIKER` | *(prompted)* | Validator display name |
| `ZALLY_INSTALL_DIR` | `~/.local/bin` | Where to install `zallyd` and `create-val-tx` |
| `ZALLY_HOME` | `~/.zallyd` | Node home directory |
| `VOTING_CONFIG_URL` | `https://zally-phi.vercel.app` | Vercel app URL for network discovery |

When `join.sh` finishes it prints your validator address. Save it for Step 2.

### Step 2 — Fund your account

Your account must hold stake before it can register as a validator. Ask the bootstrap operator to fund your address using the **admin UI** (Validators → Fund validator).

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
mise run validator:join    # builds from source, discovers network, generates start.sh
```

This runs `mise run build:install` (builds `zallyd` + `create-val-tx` from source), then `join.sh` which detects the local binaries, fetches the network config via Vercel, and produces `~/.zallyd/start.sh`.

### Step 2 — Fund your account

Same as Path A — ask the bootstrap operator to fund your address in the admin UI.

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

The EA key ceremony is automatic. When a new voting round is created, your validator is included in the ceremony if it is bonded and has a registered Pallas key (done automatically by `join.sh`). The block proposer handles dealing and acking via `PrepareProposal` — no manual steps required.

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
