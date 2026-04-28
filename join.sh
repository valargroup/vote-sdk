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
#   2. Fetches voting-config from the GitHub Pages CDN (canonical source; same one wallets use)
#   3. Fetches node identity from the first vote_servers[] URL (PEX seed)
#   4. Downloads genesis.json from DO Spaces (canonical file from sdk-chain-reset)
#   5. Initializes a node, generates cryptographic keys
#   6. Configures the node to connect to the existing network
#   7. Starts svoted, waits for sync, registers with the admin join queue, installs svoted-join (poll + bond)
#
# Optional: SVOTE_JOIN_LOOP_SCRIPT=/path/to/join-loop.sh when join.sh is piped from curl and
# join-loop.sh is not beside join.sh (see vote-sdk/scripts/join-loop.sh).
#
# Requirements: curl. jq is installed automatically when missing (apt/dnf/yum/apk/Homebrew).
# Dependency (binary path): release.yml must have run to upload binaries to DO Spaces.

set -euo pipefail

CHAIN_ID="svote-1"
INSTALL_DIR="${SVOTE_INSTALL_DIR:-$HOME/.local/bin}"
HOME_DIR="${SVOTE_HOME:-$HOME/.svoted}"
DO_BASE="https://vote.fra1.digitaloceanspaces.com"
# Canonical voting-config (same payload wallets fetch). Override for staging
# mirrors or fork testing; see github.com/valargroup/token-holder-voting-config.
VOTING_CONFIG_URL="${VOTING_CONFIG_URL:-https://valargroup.github.io/token-holder-voting-config/voting-config.json}"
# Admin API base — POST /api/register-validator (join queue) and
# POST /api/server-heartbeat (helper liveness). Override via SVOTE_ADMIN_URL
# when joining a non-default deployment.
DEFAULT_ADMIN_API_BASE="${DEFAULT_ADMIN_API_BASE:-https://vote-chain-primary.valargroup.org}"
SVOTE_ADMIN_URL="${SVOTE_ADMIN_URL:-${DEFAULT_ADMIN_API_BASE}}"

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

if ! command -v curl > /dev/null 2>&1; then
  echo "ERROR: curl is required. Install it and re-run."
  exit 1
fi

if ! command -v jq > /dev/null 2>&1; then
  echo "jq not found — installing..."
  OS_NAME=$(uname -s)
  if [ "$OS_NAME" = "Linux" ]; then
    if command -v apt-get > /dev/null 2>&1; then
      export DEBIAN_FRONTEND=noninteractive NEEDRESTART_MODE=a
      sudo -E apt-get update -qq
      sudo -E apt-get install -y jq
    elif command -v dnf > /dev/null 2>&1; then
      sudo dnf install -y jq
    elif command -v yum > /dev/null 2>&1; then
      sudo yum install -y jq
    elif command -v apk > /dev/null 2>&1; then
      sudo apk add --no-cache jq
    else
      echo "ERROR: jq is required. No supported package manager found (apt, dnf, yum, apk)."
      exit 1
    fi
  elif [ "$OS_NAME" = "Darwin" ]; then
    if command -v brew > /dev/null 2>&1; then
      brew install jq
    else
      echo "ERROR: jq is required. Install with: brew install jq"
      exit 1
    fi
  else
    echo "ERROR: jq is required. Install jq for your OS and re-run."
    exit 1
  fi
fi

if ! command -v jq > /dev/null 2>&1; then
  echo "ERROR: jq is still not available after install attempt."
  exit 1
fi

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
  # Tarballs live under binaries/vote-sdk/ since release.yml commit b30573d8.
  # version.txt and join.sh itself stay at the bucket root.
  curl -fsSL -o /tmp/shielded-vote-release.tar.gz "${DO_BASE}/binaries/vote-sdk/shielded-vote-${VERSION}-${PLATFORM}.tar.gz"

  # Verify tarball integrity via SHA-256 checksum.
  CHECKSUM_URL="${DO_BASE}/binaries/vote-sdk/shielded-vote-${VERSION}-${PLATFORM}.tar.gz.sha256"
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

# ─── Discover network via voting-config (CDN) ───────────────────────────────
# The voting-config JSON is the same one wallets fetch from
# valargroup.github.io/token-holder-voting-config (ZIP 1244 §Vote Configuration
# Format). We use vote_servers[0] as the seed peer for P2P; SVOTE_ADMIN_URL is
# a separate base for the join queue and helper heartbeat.

echo ""
echo "=== Discovering network ==="

echo "Fetching voting-config from ${VOTING_CONFIG_URL}..."
if ! VOTING_CONFIG=$(curl -fsSL --connect-timeout 15 --max-time 60 "${VOTING_CONFIG_URL}"); then
  echo "ERROR: Could not fetch voting-config from ${VOTING_CONFIG_URL}"
  echo "  Set VOTING_CONFIG_URL to a reachable mirror, or fix network access."
  exit 1
fi

SEED_URL=$(echo "$VOTING_CONFIG" | jq -r '.vote_servers[0].url // empty')

if [ -z "$SEED_URL" ] || [ "$SEED_URL" = "null" ]; then
  echo "ERROR: No vote_servers[0].url in voting-config (upstream returned an empty list)."
  echo "  The maintainers of token-holder-voting-config need to publish at least one validator URL."
  exit 1
fi

echo "Seed node: ${SEED_URL}"
echo "Admin / join API base: ${SVOTE_ADMIN_URL}"

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

# Clean previous state if present.
if [ -d "${HOME_DIR}" ]; then
  echo "Removing existing ${HOME_DIR}..."
  rm -rf "${HOME_DIR}"
fi

# Do not silence stderr: with set -e, a failed init would otherwise exit with no explanation.
if ! svoted init "${MONIKER}" --chain-id "${CHAIN_ID}" --home "${HOME_DIR}" > /dev/null; then
  echo "ERROR: svoted init failed. Typical causes: missing dynamic libraries (ldd on the svoted binary), disk full, or invalid moniker."
  exit 1
fi

# ─── Fetch genesis from DO Spaces ────────────────────────────────────────────
# sdk-chain-reset.yml's upload-genesis job writes the canonical genesis to
# s3://vote/genesis.json after every reset. That's the single source of truth
# that reset-join.sh and reset-archive.sh already consume, so join.sh matches.

GENESIS_URL="${DO_BASE}/genesis.json"
echo "Fetching genesis.json from ${GENESIS_URL}..."
curl -fsSL -o "${HOME_DIR}/config/genesis.json" "${GENESIS_URL}"
if ! svoted genesis validate-genesis --home "${HOME_DIR}"; then
  echo "ERROR: genesis.json failed validation against this svoted build."
  exit 1
fi
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

# Optional auth token for POST /shielded-vote/v1/shares (sent via X-Helper-Token header).
# Empty disables token auth.
api_token = ""

# Path to the SQLite database file. Empty = default (\$HOME/.svoted/helper.db).
db_path = ""

# How often to check for shares ready to submit (seconds).
process_interval = 5

# Port of the chain's REST API (used for MsgRevealShare submission).
chain_api_port = 1317

# Maximum concurrent proof generation goroutines.
max_concurrent_proofs = 2

# Admin server base URL — used for POST /api/register-validator on startup
# and POST /api/server-heartbeat every 2h. Empty disables the heartbeat.
admin_url = "${SVOTE_ADMIN_URL}"

# This server's public URL as seen by clients (set after Caddy TLS setup).
# Empty disables the heartbeat.
helper_url = ""
HELPERCFG

echo "Node configured."

# ─── TLS reverse proxy (Caddy) ──────────────────────────────────────────────
# Sets up Caddy as a TLS reverse proxy in front of the chain REST API (port 1317).
# Caddy auto-provisions Let's Encrypt certificates.
#
# Set SVOTE_SKIP_CADDY=1 to skip Caddy setup entirely (e.g. Docker/CI environments
# without a public IP or systemd, or when TLS is handled externally).

echo ""
echo "=== Setting up TLS reverse proxy ==="

if [ "${SVOTE_SKIP_CADDY:-0}" = "1" ]; then
  echo "SVOTE_SKIP_CADDY=1: skipping Caddy setup."
  VALIDATOR_URL=""
elif [ -z "$SVOTE_DOMAIN" ]; then
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
# the admin API so operators appear in the Join queue UI.

if [ -n "$VALIDATOR_URL" ]; then
  echo ""
  echo "=== Registering with vote network ==="

  TIMESTAMP=$(date +%s)
  REG_PAYLOAD="{\"operator_address\":\"${VALIDATOR_ADDR}\",\"url\":\"${VALIDATOR_URL}\",\"moniker\":\"${MONIKER}\",\"timestamp\":${TIMESTAMP}}"

  if SIG_JSON=$(svoted sign-arbitrary "$REG_PAYLOAD" --from validator --keyring-backend test --home "${HOME_DIR}" 2>/dev/null); then
    SIG=$(echo "$SIG_JSON" | jq -r '.signature')
    PUB_KEY=$(echo "$SIG_JSON" | jq -r '.pub_key')

    REG_BODY="{\"operator_address\":\"${VALIDATOR_ADDR}\",\"url\":\"${VALIDATOR_URL}\",\"moniker\":\"${MONIKER}\",\"timestamp\":${TIMESTAMP},\"signature\":\"${SIG}\",\"pub_key\":\"${PUB_KEY}\"}"

    REG_RESULT=$(curl -fsSL -X POST "${SVOTE_ADMIN_URL%/}/api/register-validator" \
      -H "Content-Type: application/json" \
      -d "$REG_BODY" 2>/dev/null || echo "")

    if [ -n "$REG_RESULT" ]; then
      REG_STATUS=$(echo "$REG_RESULT" | jq -r '.status // empty' 2>/dev/null || echo "")
      if [ "$REG_STATUS" = "pending" ] || [ "$REG_STATUS" = "registered" ]; then
        echo "Registered (${REG_STATUS}). The admin will see your request."
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
#
# Set SVOTE_SKIP_SERVICE=1 to skip service installation and the sync wait.
# Useful for Docker-based smoke tests and CI environments where systemd/launchd
# is not available. The script exits 0 after printing the node summary.

if [ "${SVOTE_SKIP_SERVICE:-0}" = "1" ]; then
  echo ""
  echo "============================================="
  echo "  Node configured (SVOTE_SKIP_SERVICE=1)"
  echo "  Service install and sync wait skipped."
  echo "============================================="
  echo ""
  echo "  Operator address: ${VALIDATOR_ADDR}"
  echo "  Home dir:         ${HOME_DIR}"
  echo "  Config:           ${CONFIG_TOML}"
  echo "  App config:       ${APP_TOML}"
  exit 0
fi

LOG_FILE="${HOME_DIR}/node.log"
SVOTED_BIN=$(command -v svoted)
SERVICE_NAME="svoted"

# Re-register with the admin join queue (idempotent).
register_url() {
  if [ -z "${VALIDATOR_URL}" ] || [ -z "${SVOTE_ADMIN_URL}" ]; then
    return 0
  fi
  local ts=$(date +%s)
  local payload="{\"operator_address\":\"${VALIDATOR_ADDR}\",\"url\":\"${VALIDATOR_URL}\",\"moniker\":\"${MONIKER}\",\"timestamp\":${ts}}"
  local sig_json
  sig_json=$(svoted sign-arbitrary "$payload" --from validator --keyring-backend test --home "${HOME_DIR}" 2>/dev/null) || return 0
  local sig=$(echo "$sig_json" | jq -r '.signature')
  local pub_key=$(echo "$sig_json" | jq -r '.pub_key')
  local body="{\"operator_address\":\"${VALIDATOR_ADDR}\",\"url\":\"${VALIDATOR_URL}\",\"moniker\":\"${MONIKER}\",\"timestamp\":${ts},\"signature\":\"${sig}\",\"pub_key\":\"${pub_key}\"}"
  curl -fsSL -X POST "${SVOTE_ADMIN_URL%/}/api/register-validator" \
    -H "Content-Type: application/json" \
    -d "$body" > /dev/null 2>&1 || true
}

# Install join-loop.sh next to svoted/create-val-tx.
JOIN_LOOP_BIN="${INSTALL_DIR}/join-loop.sh"
if [ -n "${SVOTE_JOIN_LOOP_SCRIPT:-}" ] && [ -f "${SVOTE_JOIN_LOOP_SCRIPT}" ]; then
  cp "${SVOTE_JOIN_LOOP_SCRIPT}" "${JOIN_LOOP_BIN}"
elif [ -n "${BASH_SOURCE[0]:-}" ] && [ "${BASH_SOURCE[0]}" != "bash" ] && [ -f "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/scripts/join-loop.sh" ]; then
  cp "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/scripts/join-loop.sh" "${JOIN_LOOP_BIN}"
elif curl -fsSL "${DO_BASE}/join-loop.sh" -o "${JOIN_LOOP_BIN}" 2>/dev/null; then
  :
else
  echo "ERROR: join-loop.sh not found. Clone vote-sdk and run ./join.sh from the repo, set SVOTE_JOIN_LOOP_SCRIPT, or publish join-loop.sh to ${DO_BASE}/join-loop.sh" >&2
  exit 1
fi
chmod +x "${JOIN_LOOP_BIN}"

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

  # Join loop: poll admin + fund + create-val-tx until bonded.
  JOIN_LOG="${HOME_DIR}/join.log"
  JOIN_LABEL="com.shielded-vote.join"
  JOIN_PLIST="${PLIST_DIR}/${JOIN_LABEL}.plist"
  launchctl bootout "gui/$(id -u)/${JOIN_LABEL}" 2>/dev/null || true
  cat > "${JOIN_PLIST}" <<JOINPLISTEOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>${JOIN_LABEL}</string>
    <key>ProgramArguments</key>
    <array>
        <string>${JOIN_LOOP_BIN}</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <dict>
        <key>SuccessfulExit</key>
        <false/>
    </dict>
    <key>StandardOutPath</key>
    <string>${JOIN_LOG}</string>
    <key>StandardErrorPath</key>
    <string>${JOIN_LOG}</string>
    <key>EnvironmentVariables</key>
    <dict>
        <key>PATH</key>
        <string>${INSTALL_DIR}:/usr/local/bin:/usr/bin:/bin</string>
        <key>SVOTE_HOME</key>
        <string>${HOME_DIR}</string>
        <key>VALIDATOR_ADDR</key>
        <string>${VALIDATOR_ADDR}</string>
        <key>MONIKER</key>
        <string>${MONIKER}</string>
        <key>VALIDATOR_URL</key>
        <string>${VALIDATOR_URL}</string>
        <key>SVOTE_ADMIN_URL</key>
        <string>${SVOTE_ADMIN_URL}</string>
        <key>SVOTE_INSTALL_DIR</key>
        <string>${INSTALL_DIR}</string>
    </dict>
</dict>
</plist>
JOINPLISTEOF
  launchctl bootstrap "gui/$(id -u)" "${JOIN_PLIST}"
  echo "Join loop service started: ${JOIN_LABEL} (logs: ${JOIN_LOG})"

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

  JOIN_LOG="${HOME_DIR}/join.log"
  sudo tee /etc/default/svoted-join > /dev/null <<ENVEOF
SVOTE_HOME=${HOME_DIR}
VALIDATOR_ADDR=${VALIDATOR_ADDR}
MONIKER=${MONIKER}
VALIDATOR_URL=${VALIDATOR_URL}
SVOTE_ADMIN_URL=${SVOTE_ADMIN_URL}
SVOTE_INSTALL_DIR=${INSTALL_DIR}
ENVEOF

  sudo tee /etc/systemd/system/svoted-join.service > /dev/null <<JOINUNIT
[Unit]
Description=Shielded-Vote validator join loop (${MONIKER})
After=network.target ${SERVICE_NAME}.service
Requires=${SERVICE_NAME}.service

[Service]
Type=simple
User=$(whoami)
EnvironmentFile=/etc/default/svoted-join
ExecStart=${JOIN_LOOP_BIN}
Restart=on-failure
RestartSec=30
SuccessExitStatus=0
StandardOutput=append:${JOIN_LOG}
StandardError=append:${JOIN_LOG}

[Install]
WantedBy=multi-user.target
JOINUNIT

  sudo systemctl daemon-reload
  sudo systemctl enable svoted-join
  sudo systemctl start svoted-join
  echo "Join loop svoted-join started (logs: ${JOIN_LOG})"
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

echo ""
echo "============================================="
echo "  Validator node is running; join loop handles bonding"
echo "============================================="
echo ""
echo "  Operator address (fund this in the admin Join queue UI):"
echo "    ${VALIDATOR_ADDR}"
echo ""
echo "  After bonding, add your public URL to vote_servers via a PR:"
echo "    https://github.com/valargroup/token-holder-voting-config"
echo "  Suggested JSON entry:"
echo "    {\"url\":\"${VALIDATOR_URL}\",\"label\":\"${MONIKER}\"}"
echo ""
if [ "$(uname -s)" = "Darwin" ]; then
  echo "  Chain service:  ${PLIST_LABEL} (launchd)"
  echo "  Chain logs:     tail -f ${LOG_FILE}"
  echo "  Join loop:      com.shielded-vote.join (launchd)"
  echo "  Join logs:      tail -f ${HOME_DIR}/join.log"
  echo ""
  echo "  svoted-join exits automatically after bonding; it stays enabled and is a"
  echo "  no-op on reboot. To remove it:"
  echo "    launchctl bootout gui/$(id -u)/com.shielded-vote.join"
else
  echo "  Chain service:  ${SERVICE_NAME} (systemd)"
  echo "  Chain logs:     journalctl -u ${SERVICE_NAME} -f"
  echo "  Join loop:      svoted-join (systemd)"
  echo "  Join logs:      tail -f ${HOME_DIR}/join.log"
  echo ""
  echo "  svoted-join exits automatically after bonding; it stays enabled and is a"
  echo "  no-op on reboot. To remove it:"
  echo "    sudo systemctl disable svoted-join"
fi
