# Join the Zally Network as a Validator

## Requirements

- Linux or macOS (amd64 or arm64)
- `curl` and `jq` installed
- Funded validator account (see Step 2)

## Step 1 — Run join.sh

Run the setup script. It downloads pre-built binaries, fetches the genesis and network config, initialises your node, generates cryptographic keys, and produces a ready-to-run `start.sh`.

```bash
curl -fsSL https://vote.fra1.digitaloceanspaces.com/join.sh | bash
```

You will be prompted for a **moniker** (a display name for your validator). To run non-interactively, set it as an env var:

```bash
ZALLY_MONIKER=my-validator \
  curl -fsSL https://vote.fra1.digitaloceanspaces.com/join.sh | bash
```

### Optional env vars

| Variable | Default | Purpose |
|---|---|---|
| `ZALLY_MONIKER` | *(prompted)* | Validator display name |
| `ZALLY_INSTALL_DIR` | `/usr/local/bin` | Where to install `zallyd` and `create-val-tx` |
| `ZALLY_HOME` | `~/.zallyd` | Node home directory |

When `join.sh` finishes it prints your validator address:

```
  Address:  zally1xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
```

Save this — you need it for Step 2.

## Step 2 — Fund your account

Your account must hold stake before it can register as a validator. Ask a teammate to trigger the **"Fund validator"** GitHub Action with your address from Step 1.

You can confirm the funds arrived once the node is synced (Step 3):

```bash
zallyd query bank balances <your-address> --node tcp://localhost:26657
```

## Step 3 — Start the node

`join.sh` generated a `start.sh` in your home directory. Run it:

```bash
~/.zallyd/start.sh
```

This will:
1. Start `zallyd` in the background, logging to `~/.zallyd/node.log`
2. Poll sync status and print block height until the node is caught up
3. Automatically call `create-val-tx` to register you as a validator once synced

To follow logs while the node is running (in a separate terminal):

```bash
tail -f ~/.zallyd/node.log
```

## Step 4 — Verify

Once `start.sh` reports "Validator registered", confirm you appear in the validator set:

```bash
zallyd query staking validators --node tcp://localhost:26657
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
| REST API port | `1317` (localhost only) |
| Node home | `~/.zallyd` |
