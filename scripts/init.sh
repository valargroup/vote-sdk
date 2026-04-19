#!/bin/bash
set -e

# Load .env from repo root if present (local dev convenience).
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
if [ -f "$REPO_ROOT/.env" ]; then
    set -a
    # shellcheck disable=SC1091
    . "$REPO_ROOT/.env"
    set +a
fi

CHAIN_ID="svote-1"
MONIKER="validator"
HOME_DIR="${SVOTED_HOME:-$HOME/.svoted}"
BINARY="svoted"
DENOM="usvote"

echo "=== Initializing Shielded-Vote Chain ==="

# Remove existing data but preserve nullifier/PIR tier files (~6 GB).
if [ -d "$HOME_DIR" ]; then
    find "$HOME_DIR" -mindepth 1 -maxdepth 1 ! -name nullifiers -exec rm -rf {} +
else
    mkdir -p "$HOME_DIR"
fi

# Init chain
$BINARY init "$MONIKER" --chain-id "$CHAIN_ID" --home "$HOME_DIR"

# Import or generate the validator key. When VAL_PRIVKEY is set (CI/production),
# import the deterministic key so the address is known ahead of time. Otherwise
# generate a fresh key (local dev).
if [ -n "${VAL_PRIVKEY:-}" ]; then
    $BINARY keys import-hex validator "$VAL_PRIVKEY" --keyring-backend test --home "$HOME_DIR"
else
    $BINARY keys add validator --keyring-backend test --home "$HOME_DIR"
fi

VALIDATOR_ADDR=$($BINARY keys show validator -a --keyring-backend test --home "$HOME_DIR")
VALIDATOR_VALOPER=$($BINARY keys show validator --bech val -a --keyring-backend test --home "$HOME_DIR")
echo "Validator address: $VALIDATOR_ADDR"
echo "Validator valoper: $VALIDATOR_VALOPER"

# Import the bootstrap vote-manager keys. VM_PRIVKEYS is a comma-separated list
# of 64-char hex secp256k1 private keys; every derived address becomes a vote
# manager at genesis (any-of-N). The stake pool is split evenly across the set.
# shellcheck source=scripts/_vote_manager_keys_lib.sh
. "$(dirname "$0")/_vote_manager_keys_lib.sh"
parse_vm_privkeys

# Total stake pool divided evenly across vote managers (preserves total supply).
TOTAL_VOTE_MANAGER_POOL=1000000000
VOTE_MANAGER_ADDRS=()
NUM_VOTE_MANAGERS=${#VM_PRIVKEY_LIST[@]}
PER_VOTE_MANAGER_STAKE=$((TOTAL_VOTE_MANAGER_POOL / NUM_VOTE_MANAGERS))
REMAINDER=$((TOTAL_VOTE_MANAGER_POOL - PER_VOTE_MANAGER_STAKE * NUM_VOTE_MANAGERS))

for i in "${!VM_PRIVKEY_LIST[@]}"; do
    key="${VM_PRIVKEY_LIST[$i]}"
    name="vote-manager-$((i + 1))"
    $BINARY keys import-hex "$name" "$key" --keyring-backend test --home "$HOME_DIR"
    addr=$($BINARY keys show "$name" -a --keyring-backend test --home "$HOME_DIR")
    VOTE_MANAGER_ADDRS+=("$addr")
    # Vote manager 1 receives any remainder from the integer division.
    if [ "$i" -eq 0 ]; then
        stake=$((PER_VOTE_MANAGER_STAKE + REMAINDER))
    else
        stake=$PER_VOTE_MANAGER_STAKE
    fi
    echo "Vote-manager ${name}:     $addr (balance: ${stake}${DENOM})"
    $BINARY genesis add-genesis-account "$addr" "${stake}${DENOM}" \
        --keyring-backend test --home "$HOME_DIR"
done

# Add validator's genesis account (needed for self-delegation).
$BINARY genesis add-genesis-account "$VALIDATOR_ADDR" "10000000${DENOM}" \
    --keyring-backend test --home "$HOME_DIR"

# Create genesis transaction (self-delegation)
$BINARY genesis gentx validator "10000000${DENOM}" \
    --chain-id "$CHAIN_ID" \
    --keyring-backend test \
    --home "$HOME_DIR"

# Collect genesis transactions
$BINARY genesis collect-gentxs --home "$HOME_DIR"

# Build the vote_manager_addresses JSON array for the genesis patch.
VOTE_MANAGER_JSON=$(printf '%s\n' "${VOTE_MANAGER_ADDRS[@]}" | jq -R . | jq -s .)

# Patch genesis: set vote_manager_addresses to the imported keys' addresses and zero
# out slashing slash fractions (no token burn). Defaults for
# signed_blocks_window (100), min_signed_per_window (0.5), and
# downtime_jail_duration (600s) are acceptable.
GENESIS="$HOME_DIR/config/genesis.json"
jq --argjson vms "$VOTE_MANAGER_JSON" '
  .app_state.vote.vote_manager_addresses = $vms
  | .app_state.slashing.params.slash_fraction_double_sign = "0.000000000000000000"
  | .app_state.slashing.params.slash_fraction_downtime = "0.000000000000000000"' \
  "$GENESIS" > "${GENESIS}.tmp" && mv "${GENESIS}.tmp" "$GENESIS"

# Validate genesis
$BINARY genesis validate-genesis --home "$HOME_DIR"

# Ensure minimum-gas-prices is set (the Go default template writes "0usvote"
# but older inits or manual edits may leave it blank, which aborts `svoted start`).
APP_TOML="$HOME_DIR/config/app.toml"
sed -i.bak 's/^minimum-gas-prices = ""/minimum-gas-prices = "0usvote"/' "$APP_TOML"

# Enable the REST API server (default: disabled).
# Use port 1318 to avoid Cursor IDE occupying 1317.
sed -i.bak '/\[api\]/,/\[.*\]/ s/enable = false/enable = true/' "$APP_TOML"
sed -i.bak 's|address = "tcp://localhost:1317"|address = "tcp://0.0.0.0:1318"|' "$APP_TOML"
# Enable CORS for dev (Vite dev server on port 5173).
sed -i.bak '/\[api\]/,/\[.*\]/ s/enabled-unsafe-cors = false/enabled-unsafe-cors = true/' "$APP_TOML"

# Move gRPC and gRPC-Web off their Cosmos defaults for the same reason we
# move the REST API off 1317: Cursor IDE's Remote-SSH auto-port-forwarding
# (and some Node.js `--inspect` tooling) listens on 9090/9091 locally, so
# the default bind fails and cascades into the errgroup, which in turn
# aborts the embedded PIR supervisor. init_multi.sh assigns per-validator
# ports (9390/9490/9590); the single-validator script uses 9190/9191 to
# match scripts/test_join_ci.sh.
sed -i.bak 's|address = "localhost:9090"|address = "localhost:9190"|' "$APP_TOML"
sed -i.bak 's|address = "localhost:9091"|address = "localhost:9191"|' "$APP_TOML"
rm -f "${APP_TOML}.bak"

# Allow long CheckTx (ZKP verification ~30–60s). Default 10s closes the RPC connection
# before the response, causing "EOF" at the API.
CONFIG_TOML="$HOME_DIR/config/config.toml"
sed -i.bak 's/^timeout_broadcast_tx_commit = .*/timeout_broadcast_tx_commit = "120s"/' "$CONFIG_TOML"
rm -f "${CONFIG_TOML}.bak"

# Generate Pallas keypair for ECIES (ceremony key distribution).
# The secret key is used by PrepareProposal to decrypt the EA key share
# and auto-inject MsgAckExecutiveAuthorityKey.
$BINARY pallas-keygen --home "$HOME_DIR"

# Update [vote] key paths in app.toml (section is auto-generated by the template).
# ea_sk_path is the parent directory for per-round ea_sk files (generated by auto-deal).
EA_SK_PATH="$HOME_DIR/ea.sk"
PALLAS_SK_PATH="$HOME_DIR/pallas.sk"
sed -i.bak "s|^ea_sk_path = .*|ea_sk_path = \"$EA_SK_PATH\"|" "$APP_TOML"
sed -i.bak "s|^pallas_sk_path = .*|pallas_sk_path = \"$PALLAS_SK_PATH\"|" "$APP_TOML"
rm -f "${APP_TOML}.bak"

# Helper defaults are privacy/dev oriented. Benchmark scripts can override them
# via environment variables before invoking this script.
HELPER_API_TOKEN="${SVOTE_HELPER_API_TOKEN:-}"
HELPER_EXPOSE_QUEUE_STATUS="${SVOTE_HELPER_EXPOSE_QUEUE_STATUS:-false}"
HELPER_MIN_DELAY="${SVOTE_HELPER_MIN_DELAY:-90}"
HELPER_PROCESS_INTERVAL="${SVOTE_HELPER_PROCESS_INTERVAL:-5}"
HELPER_MAX_CONCURRENT_PROOFS="${SVOTE_HELPER_MAX_CONCURRENT_PROOFS:-2}"
HELPER_ADMIN_URL="${SVOTE_HELPER_ADMIN_URL:-}"
HELPER_URL="${SVOTE_HELPER_URL:-}"
HELPER_SENTRY_DSN="${SVOTE_HELPER_SENTRY_DSN:-}"

# Append [helper] section (not in the default template).
cat >> "$APP_TOML" <<HELPERCFG

###############################################################################
###                         Helper Server                                   ###
###############################################################################

[helper]

# Set to true to disable the helper server.
disable = false

# Optional auth token for POST /shielded-vote/v1/shares (sent via X-Helper-Token header).
# Empty disables token auth for both share submission and queue-status polling.
api_token = "$HELPER_API_TOKEN"

# Benchmark-only queue metrics endpoint. Keep disabled by default to avoid
# exposing per-round share activity to unauthenticated observers.
expose_queue_status = $HELPER_EXPOSE_QUEUE_STATUS

# Path to the SQLite database file. Empty = default ($HOME/.svoted/helper.db).
db_path = ""

# Minimum delay floor (seconds).
min_delay = $HELPER_MIN_DELAY

# How often to check for shares ready to submit (seconds).
process_interval = $HELPER_PROCESS_INTERVAL

# Port of the chain's REST API (used for MsgRevealShare submission).
chain_api_port = 1318

# Maximum concurrent proof generation goroutines.
max_concurrent_proofs = $HELPER_MAX_CONCURRENT_PROOFS

# Admin server URL for registration and heartbeat. Empty disables (local dev default).
admin_url = "$HELPER_ADMIN_URL"

# This server's public URL. Empty disables the heartbeat (local dev default).
helper_url = "$HELPER_URL"

# Sentry DSN for error tracking. Empty disables Sentry.
# Can also be set at runtime via the SENTRY_DSN environment variable.
sentry_dsn = "$HELPER_SENTRY_DSN"
HELPERCFG

# Append [admin] section.
ADMIN_DISABLE="${SVOTE_ADMIN_DISABLE:-true}"
ADMIN_ADDRESS="${SVOTE_ADMIN_ADDRESS:-${VOTE_MANAGER_ADDRS[0]}}"
cat >> "$APP_TOML" <<ADMINCFG

###############################################################################
###                         Admin Server                                    ###
###############################################################################

[admin]

# Set to true to disable the admin server (server directory, registration,
# health monitoring).
disable = $ADMIN_DISABLE

# Path to the admin SQLite database. Empty = default (\$HOME/.svoted/admin.db).
db_path = ""

# Bootstrap admin address for approve/reject operations.
admin_address = "$ADMIN_ADDRESS"

# How often to probe vote servers for health (seconds).
probe_interval = 1800

# How often to check for stale pulses (seconds).
evict_interval = 120

# How long a server can go without a pulse before being excluded (seconds).
stale_threshold = 21600

# PIR server list (JSON array). Included in the voting-config response.
pir_servers = ""
ADMINCFG

# Append [ui] section.
cat >> "$APP_TOML" <<UICFG

###############################################################################
###                         Admin UI                                        ###
###############################################################################

[ui]

# Set to true to serve the admin UI from the chain API server.
enable = false

# Path to the built UI dist directory (output of "npm run build" in ui/).
dist_path = ""
UICFG

echo ""
echo "=== Chain initialized successfully! ==="
echo "Validator valoper: $VALIDATOR_VALOPER"
echo "Vote-manager addresses (any-of-N):"
for addr in "${VOTE_MANAGER_ADDRS[@]}"; do
    echo "  $addr"
done
echo ""
echo "Start with: $BINARY start --home $HOME_DIR"
