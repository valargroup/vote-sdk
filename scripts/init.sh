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
echo "Validator address: $VALIDATOR_ADDR"

# Add genesis account with tokens
$BINARY genesis add-genesis-account "$VALIDATOR_ADDR" "100000000${DENOM}" \
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
rm -f "${APP_TOML}.bak"

# Allow long CheckTx (ZKP verification ~30–60s). Default 10s closes the RPC connection
# before the response, causing "EOF" at the API.
CONFIG_TOML="$HOME_DIR/config/config.toml"
sed -i.bak 's/^timeout_broadcast_tx_commit = .*/timeout_broadcast_tx_commit = "120s"/' "$CONFIG_TOML"
rm -f "${CONFIG_TOML}.bak"

# Generate EA (Election Authority) ElGamal keypair for auto-tally.
# The secret key is used by PrepareProposal to decrypt tallies.
$BINARY ea-keygen --home "$HOME_DIR"

# Configure the EA secret key path in app.toml so the node can auto-tally.
EA_SK_PATH="$HOME_DIR/ea.sk"
cat >> "$APP_TOML" <<EACFG

###############################################################################
###                          Vote Module                                    ###
###############################################################################

[vote]

# Path to the Election Authority secret key file (32 bytes).
# Used by PrepareProposal to decrypt tallies and auto-inject MsgSubmitTally.
ea_sk_path = "$EA_SK_PATH"
EACFG

echo ""
echo "=== Chain initialized successfully! ==="
echo "Start with: $BINARY start --home $HOME_DIR"
