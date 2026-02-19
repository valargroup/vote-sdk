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

## Step 6 — Bootstrap the EA Key Ceremony

The Election Authority (EA) key ceremony must run once on a live chain before any votes can be created. Its purpose is to distribute the EA secret key to all validators so that each node can independently decrypt vote tallies via `PrepareProposal`.

### Ceremony state machine

```
REGISTERING  →  DEALT  →  CONFIRMED
     ↑              |
     └──────────────┘  (timeout with < 2/3 acks: full reset)
```

- **REGISTERING** — validators submit their Pallas public keys. No timeout; persists until a deal is submitted.
- **DEALT** — the dealer has published `ea_pk` and one ECIES-encrypted `ea_sk` share per validator. Each validator's node must decrypt its share and auto-ack within 30 seconds (`phase_timeout`). If fewer than 2/3 ack in time, the state resets to REGISTERING and the ceremony must be restarted.
- **CONFIRMED** — all validators have acknowledged. The chain is ready for voting.

### Phase 1 — Register Pallas keys

Each validator submits their Pallas public key (`~/.zallyd/pallas.pk`). This key is used in Phase 2 to ECIES-encrypt that validator's copy of `ea_sk`.

```bash
zallyd tx vote register-pallas-key \
  --from validator \
  --keyring-backend test \
  --home ~/.zallyd \
  --chain-id zvote-1 \
  --node tcp://localhost:26657 \
  --yes
```

Wait ~6 seconds for the block to commit, then confirm registration:

```bash
curl -s http://localhost:1318/zally/v1/ceremony | jq '.ceremony.validators | length'
# Should print 1 (or however many validators have registered)
```

### Phase 2 — Deal the EA key

Once all validators have registered, the genesis validator (dealer) encrypts `ea_sk` for each registered validator and broadcasts the deal.

**2a. Produce `payloads.json`** — queries the chain for all registered Pallas keys and ECIES-encrypts `ea_sk` for each:

```bash
zallyd encrypt-ea-key ~/.zallyd/ea.sk \
  --node http://localhost:1318 \
  --output /tmp/payloads.json
```

Each entry in `payloads.json` contains the recipient's `validator_address`, an `ephemeral_pk` (random per-recipient Pallas point), and a `ciphertext` (32-byte `ea_sk` + 16-byte Poly1305 tag, encrypted with ChaCha20-Poly1305 using an ECDH-derived key).

**2b. Submit the deal** — publishes `ea_pk` on-chain alongside the encrypted payloads. Transitions `REGISTERING → DEALT` and starts the 30-second ack window:

```bash
EA_PK_HEX=$(xxd -p ~/.zallyd/ea.pk | tr -d '\n')

zallyd tx vote deal-ea-key "$EA_PK_HEX" /tmp/payloads.json \
  --from validator \
  --keyring-backend test \
  --chain-id zvote-1 \
  --node tcp://localhost:26657 \
  --yes
```

### Phase 3 — Auto-ack (automatic)

No manual action required. On every block while the ceremony is `DEALT`, each node's `PrepareProposal` handler:

1. Decrypts its payload from the chain using `~/.zallyd/pallas.sk`
2. Verifies that `ea_sk * G == ea_pk` (integrity check)
3. Injects a `MsgAckExecutiveAuthorityKey` into the block it proposes
4. Writes `ea_sk` to `~/.zallyd/ea.sk` for use by the auto-tally system

When all validators have acked, the ceremony transitions `DEALT → CONFIRMED`.

**Prerequisite:** `vote.pallas_sk_path` must be set in `~/.zallyd/config/app.toml` (done automatically by `make init`):

```bash
grep pallas_sk_path ~/.zallyd/config/app.toml
# vote.pallas_sk_path = "/root/.zallyd/pallas.sk"
```

### Verify completion

```bash
zallyd q vote ceremony-state
# status: CEREMONY_STATUS_CONFIRMED
```
