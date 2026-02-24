# Bootstrap the Genesis Validator

## Current Chain State

After completing these steps the genesis node will be reachable at:

- Chain ID: `zvote-1`
- Home: `~/.zallyd`
- Binary: `~/go/bin/zallyd` (add `$HOME/go/bin` to `$PATH`)
- P2P: `0.0.0.0:26656` (externally accessible — open this port in your firewall)
- RPC: `127.0.0.1:26657` (local only)
- REST API: `0.0.0.0:1318`

## Step 0 — Prerequisites

```bash
# Go 1.24.1+ (1.24.0 has a known loader incompatibility)
# Download from https://go.dev/dl/ and add to PATH:
export GOPATH=$HOME/go
export PATH=$PATH:$GOPATH/bin

# Rust stable (1.83+)
curl --proto '=https' --tlsv1.2 -sSf https://sh.rustup.rs | sh
source "$HOME/.cargo/env"

# C toolchain
apt install -y build-essential
```

## Step 1 — Clone and Install the Binary

The `zallyd` binary must be built with the `halo2` and `redpallas` FFI tags, which requires the Rust circuits to be compiled first.

```bash
git clone https://github.com/z-cale/zally
cd zally/sdk
make install-ffi   # builds Rust circuits, then: go install -tags "halo2,redpallas"
```

This places `zallyd` at `~/go/bin/zallyd`.

## Step 2 — Initialize the Chain

`make init` wipes any existing chain data, then runs `scripts/init.sh` which:

1. Runs `zallyd init validator --chain-id zvote-1`
2. Creates a `validator` Cosmos key and a `manager` key (deterministic, used by vote-module tests)
3. Adds both accounts to genesis with initial balances
4. Creates and collects the genesis transaction (10 000 000 stake self-delegation)
5. Validates `genesis.json`
6. Enables the REST API on port `1318` with CORS
7. Sets `timeout_broadcast_tx_commit = 120s` (required for ZKP verification ≈ 30–60 s)
8. Generates an EA (Election Authority) ElGamal keypair → `~/.zallyd/ea.sk` / `ea.pk`
9. Generates a Pallas keypair for ECIES ceremony key distribution → `~/.zallyd/pallas.sk` / `pallas.pk`
10. Writes the `[vote]` and `[helper]` sections into `~/.zallyd/config/app.toml`

```bash
cd zally/sdk
make init
```

To inspect what was created:

```bash
# Validator and manager addresses
zallyd keys list --keyring-backend test --home ~/.zallyd

# Confirm genesis is valid
zallyd genesis validate-genesis --home ~/.zallyd
```

## Step 3 — Open the P2P Port

CometBFT binds P2P to `0.0.0.0:26656` by default. Make sure your firewall/security group allows inbound TCP on that port so joining validators can connect.

```bash
# UFW example
ufw allow 26656/tcp

# Or iptables
iptables -A INPUT -p tcp --dport 26656 -j ACCEPT
```

The RPC port (`26657`) and REST API port (`1318`) do **not** need to be publicly reachable unless you want remote CLI access or are exposing the API. For HTTPS exposure of the REST API, see the optional Caddy step below.

## Step 4 — Start the Chain

```bash
# Foreground (for initial testing)
zallyd start --home ~/.zallyd

# Or detached, logging to file
nohup zallyd start --home ~/.zallyd > ~/.zallyd/node.log 2>&1 &
```

Wait for the first block to be produced before proceeding:

```bash
watch -n2 'zallyd status --home ~/.zallyd 2>/dev/null | python3 -c \
  "import sys,json; s=json.load(sys.stdin)[\"sync_info\"]; \
   print(\"height:\", s[\"latest_block_height\"], \"catching_up:\", s[\"catching_up\"])"'
```

## Step 5 — Record the Node Identity

Validators who want to join the chain (see JOIN.md) need the node ID and public IP. Print both now:

```bash
# Node ID (derived from priv_validator_key.json)
zallyd tendermint show-node-id --home ~/.zallyd

# Confirm the P2P address they should use
echo "persistent_peers = \"$(zallyd tendermint show-node-id --home ~/.zallyd)@$(curl -s ifconfig.me):26656\""
```

Share the following with joining validators:
- The `persistent_peers` string above
- The `genesis.json` file at `~/.zallyd/config/genesis.json`

## Step 6 — EA Key Ceremony

The EA key ceremony is now **automatic per voting round**. When a round is created, eligible validators (bonded + registered Pallas key) are snapshotted. The block proposer auto-deals and auto-acks via `PrepareProposal`. No manual ceremony steps are needed.

To register the genesis validator's Pallas key and create the first round:

```bash
cd sdk && make ceremony
```

## Useful Commands

- `make clean` - reset the chain home directory
