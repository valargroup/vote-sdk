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

CHAIN_ID="${CHAIN_ID:-svote-1}"
MONIKER="valarg-genesis"
HOME_DIR="${SVOTED_HOME:-$HOME/.svoted}"
BINARY="svoted"
DENOM="usvote"
# Production (`zvote-1`) should use the vote-manager set baked into the
# x/vote module's DefaultGenesis. Staging/local chains keep the VM_PRIVKEYS
# override so test coordinators can be generated from deploy secrets.
USE_DEFAULT_GENESIS_VOTE_MANAGERS="${SVOTE_USE_DEFAULT_GENESIS_VOTE_MANAGERS:-}"
EXPECTED_PRODUCTION_VOTE_MANAGER="${SVOTE_EXPECTED_PRODUCTION_VOTE_MANAGER:-sv1wyf8tuys2ussdqwc6ugnvq0x273j8wq8fm3jrj}"
if [ -z "$USE_DEFAULT_GENESIS_VOTE_MANAGERS" ]; then
    if [ "$CHAIN_ID" = "zvote-1" ]; then
        USE_DEFAULT_GENESIS_VOTE_MANAGERS=true
    else
        USE_DEFAULT_GENESIS_VOTE_MANAGERS=false
    fi
fi

echo "=== Initializing Shielded-Vote Chain ==="

# Remove existing data but preserve nullifier/PIR tier files (~6 GB).
if [ -d "$HOME_DIR" ]; then
    find "$HOME_DIR" -mindepth 1 -maxdepth 1 ! -name nullifiers -exec rm -rf {} +
else
    mkdir -p "$HOME_DIR"
fi

# Init chain
$BINARY init "$MONIKER" --chain-id "$CHAIN_ID" --home "$HOME_DIR"
GENESIS="$HOME_DIR/config/genesis.json"

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

# Total stake pool divided evenly across vote managers (preserves total supply).
TOTAL_VOTE_MANAGER_POOL=1000000000
VOTE_MANAGER_ADDRS=()

if [ "$USE_DEFAULT_GENESIS_VOTE_MANAGERS" = "true" ]; then
    echo "Using default vote-manager addresses from svoted genesis."
    while IFS= read -r addr; do
        [ -n "$addr" ] && VOTE_MANAGER_ADDRS+=("$addr")
    done < <(jq -r '.app_state.vote.vote_manager_addresses[]?' "$GENESIS")
    if [ ${#VOTE_MANAGER_ADDRS[@]} -eq 0 ]; then
        echo "ERROR: default genesis contains no vote_manager_addresses."
        exit 1
    fi
    if [ "$CHAIN_ID" = "zvote-1" ]; then
        if [ ${#VOTE_MANAGER_ADDRS[@]} -ne 1 ] || [ "${VOTE_MANAGER_ADDRS[0]}" != "$EXPECTED_PRODUCTION_VOTE_MANAGER" ]; then
            echo "ERROR: production default vote-manager mismatch."
            echo "  Expected: $EXPECTED_PRODUCTION_VOTE_MANAGER"
            echo "  Actual:   ${VOTE_MANAGER_ADDRS[*]}"
            echo "  Rebuild/release svoted with the intended x/vote DefaultGenesis before resetting production."
            exit 1
        fi
    fi
else
    # Import the bootstrap vote-manager keys. VM_PRIVKEYS is a comma-separated
    # list of 64-char hex secp256k1 private keys; every derived address becomes
    # a vote manager at genesis (any-of-N).
    # shellcheck source=scripts/_vote_manager_keys_lib.sh
    . "$(dirname "$0")/_vote_manager_keys_lib.sh"
    parse_vm_privkeys
    for i in "${!VM_PRIVKEY_LIST[@]}"; do
        key="${VM_PRIVKEY_LIST[$i]}"
        name="vote-manager-$((i + 1))"
        $BINARY keys import-hex "$name" "$key" --keyring-backend test --home "$HOME_DIR"
        addr=$($BINARY keys show "$name" -a --keyring-backend test --home "$HOME_DIR")
        VOTE_MANAGER_ADDRS+=("$addr")
    done
fi

NUM_VOTE_MANAGERS=${#VOTE_MANAGER_ADDRS[@]}
PER_VOTE_MANAGER_STAKE=$((TOTAL_VOTE_MANAGER_POOL / NUM_VOTE_MANAGERS))
REMAINDER=$((TOTAL_VOTE_MANAGER_POOL - PER_VOTE_MANAGER_STAKE * NUM_VOTE_MANAGERS))

for i in "${!VOTE_MANAGER_ADDRS[@]}"; do
    addr="${VOTE_MANAGER_ADDRS[$i]}"
    name="vote-manager-$((i + 1))"
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

# Generate Pallas keypair for ECIES (ceremony key distribution).
# The genesis validator is already bonded via gentx, so its Pallas public key
# must be seeded into vote module genesis state.
$BINARY pallas-keygen --home "$HOME_DIR"
PALLAS_PK_B64=$(base64 < "$HOME_DIR/pallas.pk" | tr -d '\n')

# Build the vote_manager_addresses JSON array for the genesis patch.
VOTE_MANAGER_JSON=$(printf '%s\n' "${VOTE_MANAGER_ADDRS[@]}" | jq -R . | jq -s .)

# Patch genesis: preserve the selected vote-manager set, register the genesis
# validator's Pallas key for EA ceremonies,
# disable staking historical-info retention, configure downtime jailing, and
# zero out slashing slash fractions (no token burn). The signed block window
# mirrors Osmosis's wall-clock window adjusted for svoted's observed block time.
jq --argjson vms "$VOTE_MANAGER_JSON" \
  --arg validator "$VALIDATOR_VALOPER" \
  --arg pallasPk "$PALLAS_PK_B64" '
  .app_state.vote.vote_manager_addresses = $vms
  | .app_state.vote.pallas_keys = [{validator_address: $validator, pallas_pk: $pallasPk}]
  | .app_state.staking.params.historical_entries = 0
  | .app_state.slashing.params.signed_blocks_window = "72800"
  | .app_state.slashing.params.min_signed_per_window = "0.800000000000000000"
  | .app_state.slashing.params.downtime_jail_duration = "300s"
  | .app_state.slashing.params.slash_fraction_double_sign = "0.000000000000000000"
  | .app_state.slashing.params.slash_fraction_downtime = "0.000000000000000000"' \
  "$GENESIS" > "${GENESIS}.tmp" && mv "${GENESIS}.tmp" "$GENESIS"

# Validate genesis
$BINARY genesis validate-genesis --home "$HOME_DIR"

# Ensure minimum-gas-prices is set (the Go default template writes "0usvote"
# but older inits or manual edits may leave it blank, which aborts `svoted start`).
APP_TOML="$HOME_DIR/config/app.toml"
sed -i.bak 's/^minimum-gas-prices = ""/minimum-gas-prices = "0usvote"/' "$APP_TOML"

# Enable the REST API server (default: disabled) and bind on all interfaces.
sed -i.bak '/\[api\]/,/\[.*\]/ s/enable = false/enable = true/' "$APP_TOML"
sed -i.bak 's|address = "tcp://localhost:1317"|address = "tcp://0.0.0.0:1317"|' "$APP_TOML"
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

# Update [vote] key paths in app.toml (section is auto-generated by the template).
# ea_sk_path is the parent directory for per-round ea_sk files (generated by auto-deal).
EA_SK_PATH="$HOME_DIR/ea.sk"
PALLAS_SK_PATH="$HOME_DIR/pallas.sk"
sed -i.bak "s|^ea_sk_path = .*|ea_sk_path = \"$EA_SK_PATH\"|" "$APP_TOML"
sed -i.bak "s|^pallas_sk_path = .*|pallas_sk_path = \"$PALLAS_SK_PATH\"|" "$APP_TOML"
rm -f "${APP_TOML}.bak"

# Helper defaults are dev oriented. Benchmark scripts can override them
# via environment variables before invoking this script.
HELPER_API_TOKEN="${SVOTE_HELPER_API_TOKEN:-}"
HELPER_EXPOSE_QUEUE_STATUS="${SVOTE_HELPER_EXPOSE_QUEUE_STATUS:-false}"
HELPER_MAX_CONCURRENT_PROOFS="${SVOTE_HELPER_MAX_CONCURRENT_PROOFS:-8}"
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

# Port of the chain's REST API (used for MsgRevealShare submission).
chain_api_port = 1317

# Maximum concurrent proof generation goroutines.
max_concurrent_proofs = $HELPER_MAX_CONCURRENT_PROOFS

# Sentry DSN for error tracking. Empty disables Sentry.
# Can also be set at runtime via the SENTRY_DSN environment variable.
sentry_dsn = "$HELPER_SENTRY_DSN"
HELPERCFG

# Patch [admin] from svoted init template (do not append a second [admin] table).
# Defaults to disabled. Set SVOTE_ADMIN_DISABLE=false when this node is the sole
# admin/UI host (production primary — see sdk-chain-reset.yml — or local dev).
ADMIN_DISABLE="${SVOTE_ADMIN_DISABLE:-true}"
# svoted init emits [admin] with disable = true; only that line matches here ([helper] uses disable = false).
sed -i.bak "s/^disable = true\$/disable = ${ADMIN_DISABLE}/" "$APP_TOML"
rm -f "${APP_TOML}.bak"

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
