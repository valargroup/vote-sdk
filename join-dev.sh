#!/bin/bash
# join-dev.sh — Build from source and join the Zally chain as a validator.
#
# Usage (from repo root):
#   ./join-dev.sh
#
# What it does:
#   1. Builds zallyd + create-val-tx from source (Go + Rust)
#   2. Downloads genesis.json and network config from DO
#   3. Initializes a node, generates cryptographic keys
#   4. Configures the node to connect to the existing network
#   5. Generates a start.sh script that handles sync + validator registration
#
# Requirements: Go 1.24+, Rust stable toolchain, curl, jq

set -euo pipefail

CHAIN_ID="zvote-1"
INSTALL_DIR="${ZALLY_INSTALL_DIR:-$HOME/.local/bin}"
HOME_DIR="${ZALLY_HOME:-$HOME/.zallyd}"
DO_BASE="https://vote.fra1.digitaloceanspaces.com"

# Resolve repo root (directory containing this script).
REPO_DIR="$(cd "$(dirname "$0")" && pwd)"

# ─── Preflight ────────────────────────────────────────────────────────────────

echo "=== Zally validator join (build from source) ==="
echo ""

# Activate mise if available — ensures pinned Go/Rust/Node versions from mise.toml.
if command -v mise > /dev/null 2>&1; then
  eval "$(mise activate bash --shims)"
  mise install
fi

# Fallback: check for tools manually if mise is not installed.
for cmd in go cargo curl jq; do
  if ! command -v "$cmd" > /dev/null 2>&1; then
    echo "ERROR: $cmd is required. Install it and re-run."
    exit 1
  fi
done

echo "Go:    $(go version)"
echo "Rust:  $(rustc --version)"
echo "Repo:  ${REPO_DIR}"

mkdir -p "${INSTALL_DIR}"

# Ensure install dir is on PATH for this session and the generated start.sh.
case ":${PATH}:" in
  *":${INSTALL_DIR}:"*) ;;
  *) export PATH="${INSTALL_DIR}:${PATH}" ;;
esac

# ─── Prompt for moniker ──────────────────────────────────────────────────────

if [ -n "${ZALLY_MONIKER:-}" ]; then
  MONIKER="$ZALLY_MONIKER"
else
  printf "Enter a name for your validator: "
  read -r MONIKER < /dev/tty
  if [ -z "$MONIKER" ]; then
    echo "ERROR: Moniker cannot be empty."
    exit 1
  fi
fi

# ─── Build binaries from source ──────────────────────────────────────────────

echo ""
echo "=== Building Rust circuits library ==="
cd "${REPO_DIR}/sdk"
make circuits

echo ""
echo "=== Building zallyd (Go + FFI) ==="
make build-ffi

echo ""
echo "=== Building create-val-tx ==="
go build -o create-val-tx ./scripts/create-val-tx

# Install to INSTALL_DIR.
cp zallyd create-val-tx "${INSTALL_DIR}/"
chmod +x "${INSTALL_DIR}/zallyd" "${INSTALL_DIR}/create-val-tx"

# Clear bash's command cache so the newly installed binaries are found immediately.
hash -r

echo "Installed: ${INSTALL_DIR}/zallyd, ${INSTALL_DIR}/create-val-tx"

# Return to repo root.
cd "${REPO_DIR}"

# ─── Download network config ─────────────────────────────────────────────────

echo ""
echo "=== Downloading network config ==="

NETWORK_JSON=$(curl -fsSL "${DO_BASE}/network.json")
PERSISTENT_PEERS=$(echo "$NETWORK_JSON" | jq -r '.persistent_peers')
echo "Peers: ${PERSISTENT_PEERS}"

# ─── Initialize node ─────────────────────────────────────────────────────────

echo ""
echo "=== Initializing node ==="

# Clean previous state if present.
if [ -d "${HOME_DIR}" ]; then
  echo "Removing existing ${HOME_DIR}..."
  rm -rf "${HOME_DIR}"
fi

"${INSTALL_DIR}/zallyd" init "${MONIKER}" --chain-id "${CHAIN_ID}" --home "${HOME_DIR}" > /dev/null 2>&1

# ─── Download and place genesis ───────────────────────────────────────────────

echo "Downloading genesis.json..."
curl -fsSL -o "${HOME_DIR}/config/genesis.json" "${DO_BASE}/genesis.json"
"${INSTALL_DIR}/zallyd" genesis validate-genesis --home "${HOME_DIR}" > /dev/null 2>&1
echo "Genesis validated."

# ─── Generate keys ────────────────────────────────────────────────────────────

echo ""
echo "=== Generating cryptographic keys ==="

"${INSTALL_DIR}/zallyd" init-validator-keys --home "${HOME_DIR}"

VALIDATOR_ADDR=$("${INSTALL_DIR}/zallyd" keys show validator -a --keyring-backend test --home "${HOME_DIR}")

# ─── Configure config.toml ───────────────────────────────────────────────────

echo ""
echo "=== Configuring node ==="

CONFIG_TOML="${HOME_DIR}/config/config.toml"

# Set persistent peers.
sed -i.bak "s|persistent_peers = \"\"|persistent_peers = \"${PERSISTENT_PEERS}\"|" "${CONFIG_TOML}"

# Increase broadcast timeout for ZKP verification (~30-60s).
sed -i.bak 's/^timeout_broadcast_tx_commit = .*/timeout_broadcast_tx_commit = "120s"/' "${CONFIG_TOML}"

rm -f "${CONFIG_TOML}.bak"

# ─── Configure app.toml ──────────────────────────────────────────────────────

APP_TOML="${HOME_DIR}/config/app.toml"

# Enable REST API with CORS.
sed -i.bak '/\[api\]/,/\[.*\]/ s/enable = false/enable = true/' "${APP_TOML}"
sed -i.bak '/\[api\]/,/\[.*\]/ s/enabled-unsafe-cors = false/enabled-unsafe-cors = true/' "${APP_TOML}"

# Fix [vote] paths (template uses literal $HOME, replace with actual).
sed -i.bak "s|\\\$HOME/.zallyd|${HOME_DIR}|g" "${APP_TOML}"

rm -f "${APP_TOML}.bak"

# Append [helper] section (not in the default template).
cat >> "${APP_TOML}" <<HELPERCFG

###############################################################################
###                         Helper Server                                   ###
###############################################################################

[helper]

# Set to true to disable the helper server.
disable = false

# Optional auth token for POST /api/v1/shares (sent via X-Helper-Token header).
# Empty disables token auth.
api_token = ""

# Path to the SQLite database file. Empty = default (\$HOME/.zallyd/helper.db).
db_path = ""

# Mean of the exponential delay distribution (seconds).
# Shares are delayed by Exp(1/mean) for temporal unlinkability, capped at vote end time.
# Use a short value for testing; production default is 43200 (12 hours).
mean_delay = 60

# How often to check for shares ready to submit (seconds).
process_interval = 5

# Port of the chain's REST API (used for MsgRevealShare submission).
chain_api_port = 1317

# Maximum concurrent proof generation goroutines.
max_concurrent_proofs = 2
HELPERCFG

echo "Node configured."

# ─── Generate start.sh ───────────────────────────────────────────────────────

START_SCRIPT="${HOME_DIR}/start.sh"

cat > "${START_SCRIPT}" <<STARTEOF
#!/bin/bash
# start.sh — Start the node, wait for sync, and register as a validator.
# Generated by join-dev.sh for moniker "${MONIKER}".
set -euo pipefail

HOME_DIR="${HOME_DIR}"
INSTALL_DIR="${INSTALL_DIR}"
MONIKER="${MONIKER}"
VALIDATOR_ADDR="${VALIDATOR_ADDR}"
LOG_FILE="\${HOME_DIR}/node.log"

# Ensure binaries are on PATH.
case ":\${PATH}:" in
  *":\${INSTALL_DIR}:"*) ;;
  *) export PATH="\${INSTALL_DIR}:\${PATH}" ;;
esac

echo "Starting zallyd..."
echo "Logs: \${LOG_FILE}"
zallyd start --home "\${HOME_DIR}" >> "\${LOG_FILE}" 2>&1 &
ZALLYD_PID=\$!

trap "echo ''; echo 'zallyd is still running in the background (PID: \${ZALLYD_PID}).'; echo \"Stop it with: kill \${ZALLYD_PID}\"; echo \"Logs: \${LOG_FILE}\"; exit 0" INT TERM

# Give the node a moment to start up.
sleep 5

echo "Waiting for node to sync..."
echo "  (follow logs with: tail -f \${LOG_FILE})"
while true; do
  STATUS=\$(zallyd status --home "\${HOME_DIR}" 2>/dev/null || echo "")
  if [ -z "\$STATUS" ]; then
    sleep 2
    continue
  fi

  CATCHING_UP=\$(echo "\$STATUS" | jq -r '.sync_info.catching_up' 2>/dev/null || echo "true")
  HEIGHT=\$(echo "\$STATUS" | jq -r '.sync_info.latest_block_height' 2>/dev/null || echo "0")
  echo "  height: \${HEIGHT}, catching_up: \${CATCHING_UP}"

  if [ "\$CATCHING_UP" = "false" ]; then
    echo "Node is synced."
    break
  fi
  sleep 5
done

# Check if already a validator.
IS_VALIDATOR=\$(zallyd query staking validators --home "\${HOME_DIR}" --output json 2>/dev/null \
  | jq -r ".validators[] | select(.description.moniker == \"\${MONIKER}\") | .operator_address" 2>/dev/null || echo "")

if [ -n "\$IS_VALIDATOR" ]; then
  echo "Already registered as validator: \${IS_VALIDATOR}"
else
  # Wait for the account to be funded before attempting registration.
  echo "Waiting for account \${VALIDATOR_ADDR} to be funded..."
  echo "  (trigger the 'Fund validator' GitHub Action if you haven't already)"
  while true; do
    BALANCE=\$(zallyd query bank balances "\${VALIDATOR_ADDR}" --home "\${HOME_DIR}" --output json 2>/dev/null \
      | jq -r '.balances[] | select(.denom == "stake") | .amount' 2>/dev/null || echo "")
    if [ -n "\$BALANCE" ] && [ "\$BALANCE" != "0" ]; then
      echo "  Account funded (\${BALANCE} stake)."
      break
    fi
    sleep 5
  done

  echo "Registering as validator..."
  if ! create-val-tx --moniker "\${MONIKER}" --amount 200000stake --home "\${HOME_DIR}" --rpc-url tcp://localhost:26657; then
    echo ""
    echo "ERROR: create-val-tx exited with a non-zero status." >&2
    echo "  Check node logs for details: \${LOG_FILE}" >&2
    exit 1
  fi

  # Verify the validator actually appeared on-chain rather than assuming success.
  echo "Verifying registration on-chain (waiting ~6s for block commit)..."
  sleep 6
  IS_NOW_VALIDATOR=\$(zallyd query staking validators --home "\${HOME_DIR}" --output json 2>/dev/null \
    | jq -r ".validators[] | select(.description.moniker == \"\${MONIKER}\") | .operator_address" 2>/dev/null || echo "")
  if [ -z "\${IS_NOW_VALIDATOR}" ]; then
    echo ""
    echo "ERROR: Validator registration failed — '\${MONIKER}' not found in the validator set." >&2
    echo "  Check node logs for details: \${LOG_FILE}" >&2
    exit 1
  fi
  echo "Validator registered: \${IS_NOW_VALIDATOR}"
fi

echo ""
echo "Node is running (PID: \${ZALLYD_PID}). Logs: \${LOG_FILE}"
echo "Press Ctrl+C to detach (node keeps running). To stop: kill \${ZALLYD_PID}"
wait \$ZALLYD_PID
STARTEOF

chmod +x "${START_SCRIPT}"

# ─── Summary ──────────────────────────────────────────────────────────────────

echo ""
echo "============================================="
echo "       Node initialized successfully"
echo "============================================="
echo ""
echo "  Moniker:  ${MONIKER}"
echo "  Home:     ${HOME_DIR}"
echo "  Address:  ${VALIDATOR_ADDR}"
echo ""
# Warn if install dir isn't in the user's shell profile PATH.
SHELL_PROFILE=""
if [ -f "$HOME/.zshrc" ]; then
  SHELL_PROFILE="$HOME/.zshrc"
elif [ -f "$HOME/.bashrc" ]; then
  SHELL_PROFILE="$HOME/.bashrc"
elif [ -f "$HOME/.bash_profile" ]; then
  SHELL_PROFILE="$HOME/.bash_profile"
fi

if [ -n "$SHELL_PROFILE" ] && ! grep -q "${INSTALL_DIR}" "$SHELL_PROFILE" 2>/dev/null; then
  if [ "${INSTALL_DIR}" != "/usr/local/bin" ] && [ "${INSTALL_DIR}" != "/usr/bin" ]; then
    echo "  NOTE: Add ${INSTALL_DIR} to your PATH permanently:"
    echo "    echo 'export PATH=\"${INSTALL_DIR}:\$PATH\"' >> ${SHELL_PROFILE}"
    echo ""
  fi
fi

echo "=== Next steps ==="
echo ""
echo "1. Fund your account. Ask a teammate to trigger the"
echo "   'Fund validator' GitHub Action with your address:"
echo "   ${VALIDATOR_ADDR}"
echo ""
echo "2. Once funded, start your node (syncs and registers automatically):"
echo "   ${START_SCRIPT}"
echo ""
echo "   Then follow node logs with:"
echo "   tail -f ${HOME_DIR}/node.log"
echo ""
