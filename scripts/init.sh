#!/bin/bash
set -e

CHAIN_ID="zvote-1"
MONIKER="validator"
HOME_DIR="$HOME/.zallyd"
BINARY="zallyd"
DENOM="stake"

echo "=== Initializing Zally Chain ==="

# Remove existing data
rm -rf "$HOME_DIR"

# Init chain
$BINARY init "$MONIKER" --chain-id "$CHAIN_ID" --home "$HOME_DIR"

# Create a validator key
$BINARY keys add validator --keyring-backend test --home "$HOME_DIR"

# Get the validator address
VALIDATOR_ADDR=$($BINARY keys show validator -a --keyring-backend test --home "$HOME_DIR")
VALIDATOR_VALOPER=$($BINARY keys show validator --bech val -a --keyring-backend test --home "$HOME_DIR")
echo "Validator address: $VALIDATOR_ADDR"
echo "Validator valoper: $VALIDATOR_VALOPER"

# Import the deterministic vote-manager key (matches E2E test constant).
VM_PRIVKEY="b7e910eded435dd4e19c581b9a0b8e65104dcc4ebca8a1d55aa5c803e72ba2ee"
$BINARY keys import-hex manager "$VM_PRIVKEY" --keyring-backend test --home "$HOME_DIR"
MANAGER_ADDR=$($BINARY keys show manager -a --keyring-backend test --home "$HOME_DIR")
echo "Manager address:   $MANAGER_ADDR"

# Add genesis accounts with tokens
$BINARY genesis add-genesis-account "$VALIDATOR_ADDR" "100000000${DENOM}" \
    --keyring-backend test --home "$HOME_DIR"
$BINARY genesis add-genesis-account "$MANAGER_ADDR" "10000000${DENOM}" \
    --keyring-backend test --home "$HOME_DIR"

# Create genesis transaction (self-delegation)
$BINARY genesis gentx validator "10000000${DENOM}" \
    --chain-id "$CHAIN_ID" \
    --keyring-backend test \
    --home "$HOME_DIR"

# Collect genesis transactions
$BINARY genesis collect-gentxs --home "$HOME_DIR"

# Validate genesis
$BINARY genesis validate-genesis --home "$HOME_DIR"

# Enable the REST API server (default: disabled).
# Use port 1318 to avoid Cursor IDE occupying 1317.
APP_TOML="$HOME_DIR/config/app.toml"
sed -i.bak '/\[api\]/,/\[.*\]/ s/enable = false/enable = true/' "$APP_TOML"
sed -i.bak 's|address = "tcp://localhost:1317"|address = "tcp://localhost:1318"|' "$APP_TOML"
# Enable CORS for dev (Vite dev server on port 5173).
sed -i.bak '/\[api\]/,/\[.*\]/ s/enabled-unsafe-cors = false/enabled-unsafe-cors = true/' "$APP_TOML"
rm -f "${APP_TOML}.bak"

# Allow long CheckTx (ZKP verification ~30–60s). Default 10s closes the RPC connection
# before the response, causing "EOF" at the API.
CONFIG_TOML="$HOME_DIR/config/config.toml"
sed -i.bak 's/^timeout_broadcast_tx_commit = .*/timeout_broadcast_tx_commit = "120s"/' "$CONFIG_TOML"
rm -f "${CONFIG_TOML}.bak"

# Generate EA (Election Authority) ElGamal keypair for auto-tally.
# The secret key is used by PrepareProposal to decrypt tallies.
$BINARY ea-keygen --home "$HOME_DIR"

# Generate Pallas keypair for ECIES (ceremony key distribution).
# The secret key is used by PrepareProposal to decrypt the EA key share
# and auto-inject MsgAckExecutiveAuthorityKey.
$BINARY pallas-keygen --home "$HOME_DIR"

# Configure key paths in app.toml for PrepareProposal auto-injection.
EA_SK_PATH="$HOME_DIR/ea.sk"
PALLAS_SK_PATH="$HOME_DIR/pallas.sk"
cat >> "$APP_TOML" <<EACFG

###############################################################################
###                          Vote Module                                    ###
###############################################################################

[vote]

# Path to the Election Authority secret key file (32 bytes).
# Used by PrepareProposal to decrypt tallies and auto-inject MsgSubmitTally.
ea_sk_path = "$EA_SK_PATH"

# Path to the Pallas secret key file (32 bytes).
# Used by PrepareProposal to ECIES-decrypt the EA key share during ceremony
# and auto-inject MsgAckExecutiveAuthorityKey.
pallas_sk_path = "$PALLAS_SK_PATH"

###############################################################################
###                         Helper Server                                   ###
###############################################################################

[helper]

# Set to true to disable the helper server.
disable = false

# Optional auth token for POST /api/v1/shares (sent via X-Helper-Token header).
# Empty disables token auth.
api_token = ""

# Path to the SQLite database file. Empty = default ($HOME/.zallyd/helper.db).
db_path = ""

# Minimum random delay before submitting a share (seconds).
min_delay = 10

# Maximum random delay before submitting a share (seconds).
max_delay = 300

# How often to check for shares ready to submit (seconds).
process_interval = 5

# Port of the chain's REST API (used for MsgRevealShare submission).
chain_api_port = 1318

# Maximum concurrent proof generation goroutines.
max_concurrent_proofs = 2
EACFG

echo ""
echo "=== Chain initialized successfully! ==="
echo "Validator valoper: $VALIDATOR_VALOPER"
echo "Manager address:   $MANAGER_ADDR"
echo ""
echo "Start with: $BINARY start --home $HOME_DIR"
