#!/bin/bash
# join.sh — Join the Shielded-Vote chain as a validator.
#
# Binary-only (no repo):
#   curl -fsSL https://vote.fra1.digitaloceanspaces.com/join.sh | bash
#
# Source developer (has repo + mise):
#   mise run build:install   # builds svoted + create-val-tx → $HOME/go/bin
#   SVOTE_LOCAL_BINARIES=1 ./join.sh   # uses local binaries, skips download
#
# What it does:
#   1. Acquires svoted + create-val-tx (always downloads latest; set SVOTE_LOCAL_BINARIES=1 to use local)
#   2. Discovers the network via the Vercel API (voting-config endpoint)
#   3. Fetches genesis.json + node identity from a live validator
#   4. Initializes a node, generates cryptographic keys
#   5. Configures the node to connect to the existing network
#   6. Starts the node, waits for sync + funding, and registers as validator
#
# Requirements: curl, jq
# Dependency (binary path): release.yml must have run to upload binaries to DO Spaces.

set -euo pipefail

CHAIN_ID="svote-1"
INSTALL_DIR="${SVOTE_INSTALL_DIR:-$HOME/.local/bin}"
HOME_DIR="${SVOTE_HOME:-$HOME/.svoted}"
DO_BASE="https://vote.fra1.digitaloceanspaces.com"
VOTING_CONFIG_URL="${VOTING_CONFIG_URL:-https://shielded-vote.vercel.app}"

# Parse --domain flag for TLS hostname override.
SVOTE_DOMAIN="${SVOTE_DOMAIN:-}"
while [ $# -gt 0 ]; do
  case "$1" in
    --domain) SVOTE_DOMAIN="$2"; shift 2 ;;
    --domain=*) SVOTE_DOMAIN="${1#--domain=}"; shift ;;
    *) shift ;;
  esac
done

# ─── Preflight ────────────────────────────────────────────────────────────────

echo "=== Shielded-Vote validator join ==="
echo ""

for cmd in curl jq; do
  if ! command -v "$cmd" > /dev/null 2>&1; then
    echo "ERROR: $cmd is required. Install it and re-run."
    exit 1
  fi
done

# ─── Prompt for moniker ──────────────────────────────────────────────────────

if [ -n "${SVOTE_MONIKER:-}" ]; then
  MONIKER="$SVOTE_MONIKER"
else
  printf "Enter a name for your validator: "
  read -r MONIKER < /dev/tty
  if [ -z "$MONIKER" ]; then
    echo "ERROR: Moniker cannot be empty."
    exit 1
  fi
fi

# ─── Acquire binaries ────────────────────────────────────────────────────────
# Always download the latest release binaries from DO Spaces to avoid version
# mismatches. Source developers who built from source can skip the download by
# setting SVOTE_LOCAL_BINARIES=1 before running the script.

if [ "${SVOTE_LOCAL_BINARIES:-0}" = "1" ] && command -v svoted > /dev/null 2>&1 && command -v create-val-tx > /dev/null 2>&1; then
  echo "Using local binaries (SVOTE_LOCAL_BINARIES=1):"
  echo "  svoted:         $(command -v svoted)"
  echo "  create-val-tx:  $(command -v create-val-tx)"
else
  echo "=== Downloading binaries ==="

  OS_RAW=$(uname -s)
  ARCH_RAW=$(uname -m)

  case "$OS_RAW" in
    Linux)  OS="linux" ;;
    Darwin) OS="darwin" ;;
    *)
      echo "ERROR: Unsupported OS: ${OS_RAW}. Supported: Linux, Darwin (macOS)."
      exit 1
      ;;
  esac

  case "$ARCH_RAW" in
    x86_64)          ARCH="amd64" ;;
    aarch64|arm64)   ARCH="arm64" ;;
    *)
      echo "ERROR: Unsupported architecture: ${ARCH_RAW}. Supported: x86_64, arm64/aarch64."
      exit 1
      ;;
  esac

  PLATFORM="${OS}-${ARCH}"

  mkdir -p "${INSTALL_DIR}"

  VERSION=$(curl -fsSL "${DO_BASE}/version.txt" | tr -d '[:space:]')
  if [ -z "$VERSION" ]; then
    echo "ERROR: Could not fetch version from ${DO_BASE}/version.txt"
    exit 1
  fi

  echo "Version: ${VERSION}"
  echo "Platform: ${PLATFORM}"
  curl -fsSL -o /tmp/shielded-vote-release.tar.gz "${DO_BASE}/shielded-vote-${VERSION}-${PLATFORM}.tar.gz"

  # Verify tarball integrity via SHA-256 checksum.
  CHECKSUM_URL="${DO_BASE}/shielded-vote-${VERSION}-${PLATFORM}.tar.gz.sha256"
  if curl -fsSL -o /tmp/shielded-vote-release.tar.gz.sha256 "${CHECKSUM_URL}" 2>/dev/null; then
    EXPECTED=$(awk '{print $1}' /tmp/shielded-vote-release.tar.gz.sha256)
    if command -v sha256sum > /dev/null 2>&1; then
      ACTUAL=$(sha256sum /tmp/shielded-vote-release.tar.gz | awk '{print $1}')
    elif command -v shasum > /dev/null 2>&1; then
      ACTUAL=$(shasum -a 256 /tmp/shielded-vote-release.tar.gz | awk '{print $1}')
    else
      echo "WARNING: Neither sha256sum nor shasum found — skipping checksum verification."
      ACTUAL="$EXPECTED"
    fi
    if [ "$ACTUAL" != "$EXPECTED" ]; then
      echo "ERROR: Checksum mismatch!"
      echo "  Expected: ${EXPECTED}"
      echo "  Actual:   ${ACTUAL}"
      echo "  The downloaded tarball may be corrupted or tampered with."
      rm -f /tmp/shielded-vote-release.tar.gz /tmp/shielded-vote-release.tar.gz.sha256
      exit 1
    fi
    echo "Checksum verified."
    rm -f /tmp/shielded-vote-release.tar.gz.sha256
  else
    echo "WARNING: Checksum file not available — skipping verification."
  fi

  TARBALL_DIR="shielded-vote-${VERSION}-${PLATFORM}"
  tar xzf /tmp/shielded-vote-release.tar.gz -C /tmp "${TARBALL_DIR}/bin/svoted" "${TARBALL_DIR}/bin/create-val-tx"

  # Stop running service before overwriting (avoids "Text file busy").
  OS_NAME=$(uname -s)
  if [ "$OS_NAME" = "Darwin" ]; then
    PLIST_LABEL="com.shielded-vote.validator"
    if launchctl print "gui/$(id -u)/${PLIST_LABEL}" >/dev/null 2>&1; then
      echo "Stopping running ${PLIST_LABEL} service before upgrading..."
      launchctl bootout "gui/$(id -u)/${PLIST_LABEL}" 2>/dev/null || true
    fi
  else
    if systemctl is-active --quiet svoted 2>/dev/null; then
      echo "Stopping running svoted service before upgrading..."
      systemctl stop svoted
    fi
  fi

  cp "/tmp/${TARBALL_DIR}/bin/svoted" "${INSTALL_DIR}/svoted"
  cp "/tmp/${TARBALL_DIR}/bin/create-val-tx" "${INSTALL_DIR}/create-val-tx"
  chmod +x "${INSTALL_DIR}/svoted" "${INSTALL_DIR}/create-val-tx"
  rm -rf /tmp/shielded-vote-release.tar.gz "/tmp/${TARBALL_DIR}"

  hash -r
  echo "Installed: ${INSTALL_DIR}/svoted, ${INSTALL_DIR}/create-val-tx"
fi

# Ensure install dir is on PATH for this session.
case ":${PATH}:" in
  *":${INSTALL_DIR}:"*) ;;
  *) export PATH="${INSTALL_DIR}:${PATH}" ;;
esac

# ─── Discover network via Vercel API ─────────────────────────────────────────
# The voting-config endpoint returns the same data iOS clients use for service
# discovery. We pick a vote_server, fetch its node identity, and use it as our
# initial peer. CometBFT's PEX handles further peer discovery.

echo ""
echo "=== Discovering network ==="

VOTING_CONFIG=$(curl -fsSL "${VOTING_CONFIG_URL}/api/voting-config")
SEED_URL=$(echo "$VOTING_CONFIG" | jq -r '.vote_servers[0].url')

if [ -z "$SEED_URL" ] || [ "$SEED_URL" = "null" ]; then
  echo "ERROR: No vote_servers found in voting-config at ${VOTING_CONFIG_URL}/api/voting-config"
  echo "  The bootstrap operator needs to register at least one validator URL in the admin UI."
  exit 1
fi

echo "Seed node: ${SEED_URL}"

# Fetch the node's P2P identity.
NODE_INFO=$(curl -fsSL "${SEED_URL}/cosmos/base/tendermint/v1beta1/node_info")
NODE_ID=$(echo "$NODE_INFO" | jq -r '.default_node_info.default_node_id // .default_node_info.id // empty')
LISTEN_ADDR=$(echo "$NODE_INFO" | jq -r '.default_node_info.listen_addr // empty')

if [ -z "$NODE_ID" ]; then
  echo "ERROR: Could not fetch node_id from ${SEED_URL}"
  exit 1
fi

# Extract the host from the seed URL and the P2P port from the node's listen address.
SEED_HOST=$(echo "$SEED_URL" | sed -E 's|^https?://||; s|:[0-9]+$||; s|/.*||')
P2P_PORT=$(echo "$LISTEN_ADDR" | sed -E 's|.*:([0-9]+)$|\1|')
P2P_PORT="${P2P_PORT:-26656}"
PERSISTENT_PEERS="${NODE_ID}@${SEED_HOST}:${P2P_PORT}"
echo "Peers: ${PERSISTENT_PEERS}"

# ─── Initialize node ─────────────────────────────────────────────────────────

echo ""
echo "=== Initializing node ==="

# Clean previous state if present.
if [ -d "${HOME_DIR}" ]; then
  echo "Removing existing ${HOME_DIR}..."
  rm -rf "${HOME_DIR}"
fi

svoted init "${MONIKER}" --chain-id "${CHAIN_ID}" --home "${HOME_DIR}" > /dev/null 2>&1

# ─── Fetch genesis from the seed node ────────────────────────────────────────

echo "Fetching genesis.json from ${SEED_URL}..."
curl -fsSL -o "${HOME_DIR}/config/genesis.json" "${SEED_URL}/shielded-vote/v1/genesis"
svoted genesis validate-genesis --home "${HOME_DIR}" > /dev/null 2>&1
echo "Genesis validated."

# ─── Generate keys ────────────────────────────────────────────────────────────

echo ""
echo "=== Generating cryptographic keys ==="

svoted init-validator-keys --home "${HOME_DIR}"

VALIDATOR_ADDR=$(svoted keys show validator -a --keyring-backend test --home "${HOME_DIR}")

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
sed -i.bak "s|\\\$HOME/.svoted|${HOME_DIR}|g" "${APP_TOML}"

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

# Path to the SQLite database file. Empty = default (\$HOME/.svoted/helper.db).
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

# Heartbeat pulse URL (Vercel base URL for server-heartbeat endpoint).
# Empty disables the heartbeat.
pulse_url = "${VOTING_CONFIG_URL}"

# This server's public URL as seen by clients (set after Caddy TLS setup).
# Empty disables the heartbeat.
helper_url = ""
HELPERCFG

echo "Node configured."

# ─── TLS reverse proxy (Caddy) ──────────────────────────────────────────────
# Sets up Caddy as a TLS reverse proxy in front of the chain REST API (port 1317).
# Caddy auto-provisions Let's Encrypt certificates.

echo ""
echo "=== Setting up TLS reverse proxy ==="

if [ -z "$SVOTE_DOMAIN" ]; then
  # Auto-detect public IP and use sslip.io for a valid TLS hostname.
  PUBLIC_IP=$(curl -fsSL --connect-timeout 5 https://ifconfig.me 2>/dev/null || echo "")
  if [ -z "$PUBLIC_IP" ]; then
    echo "WARNING: Could not detect public IP. Skipping Caddy setup."
    echo "  Re-run with --domain <hostname> or set SVOTE_DOMAIN to configure TLS."
    VALIDATOR_URL=""
  else
    SVOTE_DOMAIN="$(echo "$PUBLIC_IP" | tr '.' '-').sslip.io"
    echo "Detected public IP: ${PUBLIC_IP}"
    echo "Using sslip.io domain: ${SVOTE_DOMAIN}"
  fi
fi

if [ -n "$SVOTE_DOMAIN" ]; then
  VALIDATOR_URL="https://${SVOTE_DOMAIN}"

  # Install Caddy if not present.
  if ! command -v caddy > /dev/null 2>&1; then
    OS_NAME=$(uname -s)
    if [ "$OS_NAME" = "Linux" ]; then
      echo "Installing Caddy..."
      if command -v apt-get > /dev/null 2>&1; then
        export DEBIAN_FRONTEND=noninteractive NEEDRESTART_MODE=a
        sudo -E apt-get install -y debian-keyring debian-archive-keyring apt-transport-https curl > /dev/null 2>&1
        curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/gpg.key' | sudo gpg --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg 2>/dev/null
        curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt' | sudo tee /etc/apt/sources.list.d/caddy-stable.list > /dev/null
        sudo -E apt-get update > /dev/null 2>&1
        sudo -E apt-get install -y caddy > /dev/null 2>&1
      else
        echo "WARNING: apt not found. Install Caddy manually: https://caddyserver.com/docs/install"
        VALIDATOR_URL=""
      fi
    elif [ "$OS_NAME" = "Darwin" ]; then
      if command -v brew > /dev/null 2>&1; then
        echo "Installing Caddy via Homebrew..."
        brew install caddy > /dev/null 2>&1
      else
        echo "WARNING: Homebrew not found. Install Caddy manually:"
        echo "  brew install caddy   (after installing Homebrew from https://brew.sh)"
        echo "  Then re-run join.sh."
        VALIDATOR_URL=""
      fi
    else
      echo "WARNING: Automatic Caddy installation is only supported on Linux (apt) and macOS (Homebrew)."
      echo "  Install Caddy manually: https://caddyserver.com/docs/install"
      echo "  Then re-run join.sh."
      VALIDATOR_URL=""
    fi
  fi
fi

if [ -n "$VALIDATOR_URL" ] && command -v caddy > /dev/null 2>&1; then
  OS_NAME=$(uname -s)
  if [ "$OS_NAME" = "Darwin" ]; then
    CADDY_DIR="${HOME}/.config/caddy"
    CADDYFILE="${CADDY_DIR}/Caddyfile"
    mkdir -p "${CADDY_DIR}"
  else
    CADDY_DIR="/etc/caddy"
    CADDYFILE="${CADDY_DIR}/Caddyfile"
  fi

  echo "Configuring Caddy for ${SVOTE_DOMAIN} → localhost:1317..."

  if [ "$OS_NAME" = "Darwin" ]; then
    cat > "${CADDYFILE}" <<CADDYEOF
${SVOTE_DOMAIN} {
    reverse_proxy localhost:1317
}
CADDYEOF
  else
    sudo tee "${CADDYFILE}" > /dev/null <<CADDYEOF
${SVOTE_DOMAIN} {
    reverse_proxy localhost:1317
}
CADDYEOF
  fi

  if [ "$OS_NAME" = "Darwin" ]; then
    # On macOS, Caddy is managed as part of the launchd plist (see below).
    :
  else
    sudo systemctl restart caddy 2>/dev/null || sudo caddy reload --config "${CADDYFILE}" 2>/dev/null || true
  fi
  echo "Caddy configured: ${VALIDATOR_URL} → localhost:1317"
else
  VALIDATOR_URL=""
fi

# Patch [helper] helper_url now that VALIDATOR_URL is known.
if [ -n "$VALIDATOR_URL" ]; then
  sed -i.bak "s|^helper_url = \"\"$|helper_url = \"${VALIDATOR_URL}\"|" "${APP_TOML}"
  rm -f "${APP_TOML}.bak"
fi

# ─── Phase 1: Register as pending validator ─────────────────────────────────
# The validator exists (keys generated) but isn't bonded yet. Register with
# the Vercel API so the vote-manager can see and approve it.

if [ -n "$VALIDATOR_URL" ]; then
  echo ""
  echo "=== Registering with vote network ==="

  TIMESTAMP=$(date +%s)
  REG_PAYLOAD="{\"operator_address\":\"${VALIDATOR_ADDR}\",\"url\":\"${VALIDATOR_URL}\",\"moniker\":\"${MONIKER}\",\"timestamp\":${TIMESTAMP}}"

  if SIG_JSON=$(svoted sign-arbitrary "$REG_PAYLOAD" --from validator --keyring-backend test --home "${HOME_DIR}" 2>/dev/null); then
    SIG=$(echo "$SIG_JSON" | jq -r '.signature')
    PUB_KEY=$(echo "$SIG_JSON" | jq -r '.pub_key')

    REG_BODY="{\"operator_address\":\"${VALIDATOR_ADDR}\",\"url\":\"${VALIDATOR_URL}\",\"moniker\":\"${MONIKER}\",\"timestamp\":${TIMESTAMP},\"signature\":\"${SIG}\",\"pub_key\":\"${PUB_KEY}\"}"

    REG_RESULT=$(curl -fsSL -X POST "${VOTING_CONFIG_URL}/api/register-validator" \
      -H "Content-Type: application/json" \
      -d "$REG_BODY" 2>/dev/null || echo "")

    if [ -n "$REG_RESULT" ]; then
      REG_STATUS=$(echo "$REG_RESULT" | jq -r '.status // empty' 2>/dev/null || echo "")
      if [ "$REG_STATUS" = "pending" ] || [ "$REG_STATUS" = "registered" ]; then
        echo "Registered (${REG_STATUS}). The vote-manager will see your request."
      else
        echo "WARNING: Registration response: ${REG_RESULT}"
      fi
    else
      echo "WARNING: Could not reach registration API. You can register manually later:"
      echo "  svoted sign-arbitrary '<payload>' --from validator --keyring-backend test --home ${HOME_DIR}"
    fi
  else
    echo "WARNING: Could not sign registration payload. You can register manually later."
  fi
fi

# ─── Service installation ─────────────────────────────────────────────────────
# Install a persistent service so svoted survives terminal closes and reboots.
# Uses systemd on Linux and launchd on macOS.

LOG_FILE="${HOME_DIR}/node.log"
SVOTED_BIN=$(command -v svoted)
SERVICE_NAME="svoted"

# Re-register URL with the vote network (idempotent).
# Once bonded, this promotes the pending entry to vote_servers directly.
register_url() {
  if [ -z "${VALIDATOR_URL}" ] || [ -z "${VOTING_CONFIG_URL}" ]; then
    return 0
  fi
  local ts=$(date +%s)
  local payload="{\"operator_address\":\"${VALIDATOR_ADDR}\",\"url\":\"${VALIDATOR_URL}\",\"moniker\":\"${MONIKER}\",\"timestamp\":${ts}}"
  local sig_json
  sig_json=$(svoted sign-arbitrary "$payload" --from validator --keyring-backend test --home "${HOME_DIR}" 2>/dev/null) || return 0
  local sig=$(echo "$sig_json" | jq -r '.signature')
  local pub_key=$(echo "$sig_json" | jq -r '.pub_key')
  local body="{\"operator_address\":\"${VALIDATOR_ADDR}\",\"url\":\"${VALIDATOR_URL}\",\"moniker\":\"${MONIKER}\",\"timestamp\":${ts},\"signature\":\"${sig}\",\"pub_key\":\"${pub_key}\"}"
  curl -fsSL -X POST "${VOTING_CONFIG_URL}/api/register-validator" \
    -H "Content-Type: application/json" \
    -d "$body" > /dev/null 2>&1 || true
}

echo ""
OS_NAME=$(uname -s)

if [ "$OS_NAME" = "Darwin" ]; then
  # ── macOS: launchd ──────────────────────────────────────────────────────────
  echo "=== Installing launchd service ==="

  PLIST_LABEL="com.shielded-vote.validator"
  PLIST_DIR="${HOME}/Library/LaunchAgents"
  PLIST_FILE="${PLIST_DIR}/${PLIST_LABEL}.plist"
  mkdir -p "${PLIST_DIR}"

  # Unload existing service if present.
  launchctl bootout "gui/$(id -u)/${PLIST_LABEL}" 2>/dev/null || true

  cat > "${PLIST_FILE}" <<PLISTEOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>${PLIST_LABEL}</string>
    <key>ProgramArguments</key>
    <array>
        <string>${SVOTED_BIN}</string>
        <string>start</string>
        <string>--home</string>
        <string>${HOME_DIR}</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <dict>
        <key>SuccessfulExit</key>
        <false/>
    </dict>
    <key>StandardOutPath</key>
    <string>${LOG_FILE}</string>
    <key>StandardErrorPath</key>
    <string>${LOG_FILE}</string>
    <key>EnvironmentVariables</key>
    <dict>
        <key>PATH</key>
        <string>${INSTALL_DIR}:/usr/local/bin:/usr/bin:/bin</string>
    </dict>
</dict>
</plist>
PLISTEOF

  launchctl bootstrap "gui/$(id -u)" "${PLIST_FILE}"
  echo "Service ${PLIST_LABEL} started (survives terminal close and reboots)."
  echo "Logs: ${LOG_FILE}"

  # Start Caddy as a launchd service if configured.
  if [ -n "${VALIDATOR_URL:-}" ] && command -v caddy > /dev/null 2>&1; then
    CADDY_LABEL="com.shielded-vote.caddy"
    CADDY_PLIST="${PLIST_DIR}/${CADDY_LABEL}.plist"
    CADDY_LOG="${HOME_DIR}/caddy.log"
    CADDY_BIN=$(command -v caddy)

    launchctl bootout "gui/$(id -u)/${CADDY_LABEL}" 2>/dev/null || true

    cat > "${CADDY_PLIST}" <<CADDYPLISTEOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>${CADDY_LABEL}</string>
    <key>ProgramArguments</key>
    <array>
        <string>${CADDY_BIN}</string>
        <string>run</string>
        <string>--config</string>
        <string>${HOME}/.config/caddy/Caddyfile</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>StandardOutPath</key>
    <string>${CADDY_LOG}</string>
    <key>StandardErrorPath</key>
    <string>${CADDY_LOG}</string>
</dict>
</plist>
CADDYPLISTEOF

    launchctl bootstrap "gui/$(id -u)" "${CADDY_PLIST}"
    echo "Caddy service started: ${VALIDATOR_URL} → localhost:1317"
  fi

else
  # ── Linux: systemd ──────────────────────────────────────────────────────────
  echo "=== Installing systemd service ==="

  sudo tee /etc/systemd/system/${SERVICE_NAME}.service > /dev/null <<SVCEOF
[Unit]
Description=Shielded-Vote validator (${MONIKER})
After=network.target

[Service]
Type=simple
User=$(whoami)
ExecStart=${SVOTED_BIN} start --home ${HOME_DIR}
Restart=on-failure
RestartSec=5
StandardOutput=append:${LOG_FILE}
StandardError=append:${LOG_FILE}

[Install]
WantedBy=multi-user.target
SVCEOF

  sudo systemctl daemon-reload
  sudo systemctl enable ${SERVICE_NAME}
  sudo systemctl start ${SERVICE_NAME}
  echo "Service ${SERVICE_NAME} started (survives SSH disconnect and reboots)."
  echo "Logs: ${LOG_FILE}"
fi

# Give the node a moment to start up.
sleep 5

echo "Waiting for node to sync..."
echo "  (follow logs with: tail -f ${LOG_FILE})"
while true; do
  STATUS=$(svoted status --home "${HOME_DIR}" 2>/dev/null || echo "")
  if [ -z "$STATUS" ]; then
    sleep 2
    continue
  fi

  CATCHING_UP=$(echo "$STATUS" | jq -r '.sync_info.catching_up' 2>/dev/null || echo "true")
  HEIGHT=$(echo "$STATUS" | jq -r '.sync_info.latest_block_height' 2>/dev/null || echo "0")
  echo "  height: ${HEIGHT}, catching_up: ${CATCHING_UP}"

  if [ "$CATCHING_UP" = "false" ]; then
    echo "Node is synced."
    break
  fi
  sleep 5
done

# Check if already a validator (e.g. re-running after a restart).
IS_VALIDATOR=$(svoted query staking validators --home "${HOME_DIR}" --output json 2>/dev/null \
  | jq -r ".validators[] | select(.description.moniker == \"${MONIKER}\") | .operator_address" 2>/dev/null || echo "")

if [ -n "$IS_VALIDATOR" ]; then
  echo "Already registered as validator: ${IS_VALIDATOR}"
  register_url
else
  # Wait for the account to be funded before attempting registration.
  echo "Waiting for account ${VALIDATOR_ADDR} to be funded..."
  echo "  (ask the bootstrap operator to fund your address in the admin UI)"
  while true; do
    BALANCE=$(svoted query bank balances "${VALIDATOR_ADDR}" --home "${HOME_DIR}" --output json 2>/dev/null \
      | jq -r '.balances[] | select(.denom == "usvote") | .amount' 2>/dev/null || echo "")
    if [ -n "$BALANCE" ] && [ "$BALANCE" != "0" ]; then
      echo "Account funded (${BALANCE} usvote)."
      break
    fi
    sleep 5
  done

  echo "Registering as validator..."
  if ! create-val-tx --moniker "${MONIKER}" --amount 10000000usvote --home "${HOME_DIR}" --rpc-url tcp://localhost:26657; then
    echo ""
    echo "ERROR: create-val-tx exited with a non-zero status." >&2
    echo "  Check node logs for details: ${LOG_FILE}" >&2
    exit 1
  fi

  # Verify the validator actually appeared on-chain.
  echo "Verifying registration on-chain (waiting ~6s for block commit)..."
  sleep 6
  IS_NOW_VALIDATOR=$(svoted query staking validators --home "${HOME_DIR}" --output json 2>/dev/null \
    | jq -r ".validators[] | select(.description.moniker == \"${MONIKER}\") | .operator_address" 2>/dev/null || echo "")
  if [ -z "${IS_NOW_VALIDATOR}" ]; then
    echo ""
    echo "ERROR: Validator registration failed — '${MONIKER}' not found in the validator set." >&2
    echo "  Check node logs for details: ${LOG_FILE}" >&2
    exit 1
  fi
  echo "Validator registered: ${IS_NOW_VALIDATOR}"
  # Re-register now that we're bonded — promotes to vote_servers.
  register_url
fi

echo ""
echo "============================================="
echo "  Validator is running"
echo "============================================="
echo ""
if [ "$(uname -s)" = "Darwin" ]; then
  echo "  Service:  ${PLIST_LABEL} (launchd)"
  echo "  Logs:     tail -f ${LOG_FILE}"
  echo "  Status:   launchctl print gui/$(id -u)/${PLIST_LABEL}"
  echo "  Stop:     launchctl bootout gui/$(id -u)/${PLIST_LABEL}"
  echo "  Restart:  launchctl kickstart -k gui/$(id -u)/${PLIST_LABEL}"
else
  echo "  Service:  ${SERVICE_NAME} (systemd)"
  echo "  Logs:     journalctl -u ${SERVICE_NAME} -f"
  echo "  Status:   sudo systemctl status ${SERVICE_NAME}"
  echo "  Stop:     sudo systemctl stop ${SERVICE_NAME}"
  echo "  Restart:  sudo systemctl restart ${SERVICE_NAME}"
fi
