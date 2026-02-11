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

echo ""
echo "=== Chain initialized successfully! ==="
echo "Start with: $BINARY start --home $HOME_DIR"
