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

Install [mise](https://mise.jdx.dev) and a C compiler:

```bash
curl https://mise.run | sh       # install mise
xcode-select --install           # macOS — or: apt install build-essential (Linux)
```

Then from the repo root:

```bash
mise install   # pins Go 1.24.0, Rust stable, Node 22
```

<details><summary>Without mise (manual install)</summary>

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

</details>

## Step 1 — Clone and Install the Binary

The `zallyd` binary must be built with the `halo2` and `redpallas` FFI tags, which requires the Rust circuits to be compiled first.

```bash
git clone https://github.com/z-cale/zally
cd zally
mise run build:install   # builds Rust circuits, then: go install -tags "halo2,redpallas"
```

This places `zallyd` at `~/go/bin/zallyd`.

<details><summary>Without mise</summary>

```bash
cd zally/sdk
make install-ffi
```

</details>

## Step 2 — Initialize the Chain

`mise run chain:init` wipes any existing chain data, then runs `scripts/init.sh` which:

1. Runs `zallyd init validator --chain-id zvote-1`
2. Creates a `validator` Cosmos key and a `manager` key (deterministic, used by vote-module tests)
3. Adds both accounts to genesis with initial balances
4. Creates and collects the genesis transaction (10 000 000 stake self-delegation)
5. Validates `genesis.json`
6. Enables the REST API on port `1318` with CORS
7. Sets `timeout_broadcast_tx_commit = 120s` (required for ZKP verification ≈ 30–60 s)
8. Generates a Pallas keypair for ECIES ceremony key distribution → `~/.zallyd/pallas.sk` / `pallas.pk`
9. Sets `ea_sk_path` as a directory placeholder — the actual EA key is generated per-round by auto-deal
10. Writes the `[vote]` and `[helper]` sections into `~/.zallyd/config/app.toml`

```bash
mise run chain:init
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
mise run chain:start

# Or detached, logging to file
nohup zallyd start --home ~/.zallyd > ~/.zallyd/node.log 2>&1 &
```

Wait for the first block to be produced before proceeding:

```bash
watch -n2 'zallyd status --home ~/.zallyd 2>/dev/null | python3 -c \
  "import sys,json; s=json.load(sys.stdin)[\"sync_info\"]; \
   print(\"height:\", s[\"latest_block_height\"], \"catching_up:\", s[\"catching_up\"])"'
```

## Step 5 — Register in Edge Config

Every node serves its own `genesis.json` at `/zally/v1/genesis`, so manual upload is no longer needed. Instead, register the genesis node's public URL in Edge Config so that joining validators can discover it.

1. Open the admin UI (`mise ui` or `https://zally-phi.vercel.app`)
2. Navigate to **Validators**
3. On the genesis validator's card, click **Register public URL**
4. Enter the validator's public HTTPS endpoint (e.g. `https://46-101-255-48.sslip.io`)
5. Optionally check "Also register as PIR server" if this node runs the nullifier PIR server

This writes to the `voting-config` Edge Config key, which iOS clients and `join.sh` use for service discovery.

## Step 6 — EA Key Ceremony

The EA key ceremony is now **automatic per voting round**. When a round is created, eligible validators (bonded + registered Pallas key) are snapshotted. The block proposer auto-deals and auto-acks via `PrepareProposal`. No manual ceremony steps are needed.

To register the genesis validator's Pallas key and create the first round:

```bash
mise run chain:ceremony
```

<details><summary>Without mise</summary>

```bash
cd sdk && make ceremony
```

</details>

## Useful Commands

| Command | Description |
|---|---|
| `mise run chain:clean` | Reset the chain home directory |
| `mise status` | Show service health + voting round state |
| `mise tasks` | List all available tasks |
