# Sync a New Node and Create a Validator

## Current Chain State

The genesis validator is already running:

- Chain ID: `zvote-1`
- P2P: `0.0.0.0:26656` (externally accessible)
- RPC: `127.0.0.1:26657` (local only — see note in Step 6)
- REST API: port `1318`
- Home: `~/.zallyd`
- Binary: `/root/go/bin/zallyd` (not in `$PATH` — prefix commands with full path or add to PATH)

## Step 0 — Prerequisites on the New Node

The `zallyd` binary must be built with the `halo2` and `redpallas` FFI tags. Clone the repo and install:

```bash
export GOPATH=$HOME/go
export PATH=$PATH:$GOPATH/bin

git clone https://github.com/z-cale/zally
cd zally/sdk
make install-ffi   # builds with -tags "halo2,redpallas"
```

This places `zallyd` at `~/go/bin/zallyd`.

## Step 1 — Initialize the Node

```bash
MONIKER=new-validator    # choose a name

zallyd init $MONIKER --chain-id zvote-1
```

## Step 2 — Copy genesis.json from the Genesis Validator

From **this host** (or expose the file via HTTP/SCP from the genesis node):

```bash
# On the genesis node, the genesis file is at:
cat ~/.zallyd/config/genesis.json

# On the new node, copy it:
scp root@164.92.137.124:~/.zallyd/config/genesis.json ~/.zallyd/config/genesis.json

Or if copy-pasting,
nano genesis.json
mv genesis.json ~/.zallyd/config/genesis.json
```

Validate the genesis file:
```bash
zallyd genesis validate-genesis --home ~/.zallyd
```

## Step 3 — Generate Cryptographic Keys

```bash
zallyd init-validator-keys
```

This generates a Cosmos account key (keyring name `validator`, backend `test`), a Pallas keypair, and an EA keypair in one step. Use `--key-name`, `--keyring-backend`, or `--home` to override defaults.

After running, save the printed address:

```bash
NEW_VAL_ADDR=$(zallyd keys show validator -a --keyring-backend test)
echo "New validator address: $NEW_VAL_ADDR"
```

## Step 4 — Configure config.toml

Edit `~/.zallyd/config/config.toml`:

TODO: FIX
```bash
# Set persistent peer to the genesis validator
sed -i 's|persistent_peers = ""|persistent_peers = "1a96912d840d4886dd1039187bf246b206ea7901@164.92.137.124:26656"|' ~/.zallyd/config/config.toml
```

## Step 5 — Configure app.toml

Append the vote module config to `~/.zallyd/config/app.toml`:

```bash
cat >> ~/.zallyd/config/app.toml <<EOF

[vote]
ea_sk_path = "$HOME/.zallyd/ea.sk"
pallas_sk_path = "$HOME/.zallyd/pallas.sk"
comet_rpc = "http://localhost:26657"
EOF
```

Also enable the REST API if needed:

```bash
sed -i '/\[api\]/,/\[.*\]/ s/enable = false/enable = true/' ~/.zallyd/config/app.toml
sed -i 's|address = "tcp://localhost:1317"|address = "tcp://0.0.0.0:1518"|' ~/.zallyd/config/app.toml
```

## Step 6 — Fund the New Validator Account

The new account must be funded before it can create a validator. From the **genesis validator** (on this host), send stake:

```bash
# On your machine
zallyd keys show validator -a --keyring-backend test

# On genesis validator machine. Set previous output to NEW_VAL_ADDR
zallyd tx bank send validator $NEW_VAL_ADDR 20000000stake \
  --keyring-backend test \
  --chain-id zvote-1 \
  --home ~/.zallyd \
  --node tcp://127.0.0.1:26657 \
  --yes
```

## Step 7 — Start the New Node and Wait for Sync

```bash
zallyd start

# Monitor sync status
watch -n2 'zallyd status --home $HOME 2>/dev/null | python3 -c "import sys,json; s=json.load(sys.stdin)[\"sync_info\"]; print(\"catching_up:\", s[\"catching_up\"], \"height:\", s[\"latest_block_height\"])"'
```

Wait until `catching_up: False` before proceeding.

## Step 8 — Register as Validator via CreateValidatorWithPallasKey

Use the `create-val-tx` helper tool (located in `sdk/scripts/create-val-tx`). From the repo:

```bash
cd /root/zally/sdk    # or wherever the repo is on the new node

go run ./scripts/create-val-tx --moniker $MONIKER --amount 10000000stake --rpc-url tcp://localhost:26657
```

This will:

1. Read `priv_validator_key.json` from the new home for the consensus pubkey
2. Read `pallas.pk` from the new home
3. Build, sign, and broadcast `MsgCreateValidatorWithPallasKey` to the chain

## Step 9 — Verify

```bash
# Check the new validator appears in the validator set
zallyd query staking validators
```

## Useful Commands

- `make clean` - reset the chain home directory
