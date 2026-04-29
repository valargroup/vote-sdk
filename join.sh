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
#   1. Fetches voting-config from the GitHub Pages CDN (canonical source; same one wallets use)
#   2. Fetches node identity and active app version from the first vote_servers[] URL (PEX seed)
#   3. Acquires svoted + create-val-tx for the active chain version (set SVOTE_LOCAL_BINARIES=1 to use local)
#   4. Downloads genesis.json from DO Spaces (canonical file from sdk-chain-reset)
#   5. Restores the latest pruned chain-data snapshot when one is published
#   6. Generates cryptographic keys
#   7. Configures the node to connect to the existing network
#   8. Registers once with the admin join queue and installs svoted wrapper (sync + fund + bond)
#
# Optional: SVOTE_WRAPPER_SCRIPT=/path/to/svoted-wrapper.sh when join.sh is piped from curl and
# svoted-wrapper.sh is not beside join.sh (see vote-sdk/scripts/svoted-wrapper.sh).
#
# Requirements: curl. jq and lz4 are installed automatically when missing (apt/dnf/yum/apk/Homebrew).
# Dependency (binary path): release.yml must have run to upload binaries to DO Spaces.

set -euo pipefail

main() {
CHAIN_ID="svote-1"
INSTALL_DIR="${SVOTE_INSTALL_DIR:-$HOME/.local/bin}"
HOME_DIR="${SVOTE_HOME:-$HOME/.svoted}"
DO_BASE="https://vote.fra1.digitaloceanspaces.com"
SNAPSHOT_BASE_URL="${SVOTE_SNAPSHOT_BASE_URL:-https://snapshots.valargroup.org}"
# Canonical voting-config (same payload wallets fetch). Override for staging
# mirrors or fork testing; see github.com/valargroup/token-holder-voting-config.
VOTING_CONFIG_URL="${VOTING_CONFIG_URL:-https://valargroup.github.io/token-holder-voting-config/voting-config.json}"
# Admin API base — used once for POST /api/register-validator during setup and
# by the helper heartbeat after the public URL is configured. Override via
# SVOTE_ADMIN_URL when joining a non-default deployment.
DEFAULT_ADMIN_API_BASE="${DEFAULT_ADMIN_API_BASE:-https://vote-chain-primary.valargroup.org}"
SVOTE_ADMIN_URL="${SVOTE_ADMIN_URL:-${DEFAULT_ADMIN_API_BASE}}"

# Parse --domain flag for TLS hostname override.
SVOTE_DOMAIN="${SVOTE_DOMAIN:-}"
DOMAIN_MODE="auto"
if [ -n "$SVOTE_DOMAIN" ]; then
  DOMAIN_MODE="explicit"
fi
while [ $# -gt 0 ]; do
  case "$1" in
    --domain) SVOTE_DOMAIN="$2"; DOMAIN_MODE="explicit"; shift 2 ;;
    --domain=*) SVOTE_DOMAIN="${1#--domain=}"; DOMAIN_MODE="explicit"; shift ;;
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

brew_install_quiet() {
  local log_file="${SVOTE_INSTALL_LOG:-${TMPDIR:-/tmp}/shielded-vote-join-install.log}"
  local package_list="$*"

  mkdir -p "$(dirname "${log_file}")"
  echo "Installing with Homebrew: ${package_list} (details: ${log_file})"
  {
    echo ""
    echo "[$(date)] brew install ${package_list}"
  } >> "${log_file}"

  if HOMEBREW_NO_ENV_HINTS=1 brew install "$@" >> "${log_file}" 2>&1; then
    echo "Homebrew install complete: ${package_list}"
    return 0
  fi

  echo "ERROR: Homebrew install failed: ${package_list}"
  echo "  See log: ${log_file}"
  return 1
}

install_missing_tools() {
  local missing=()
  local tool

  for tool in jq lz4; do
    if ! command -v "$tool" > /dev/null 2>&1; then
      missing+=("$tool")
    fi
  done

  if [ "${#missing[@]}" -eq 0 ]; then
    return 0
  fi

  echo "Missing tools: ${missing[*]} — installing..."
  OS_NAME=$(uname -s)
  if [ "$OS_NAME" = "Linux" ]; then
    if command -v apt-get > /dev/null 2>&1; then
      export DEBIAN_FRONTEND=noninteractive NEEDRESTART_MODE=a NEEDRESTART_SUSPEND=1
      sudo -E apt-get update -qq
      sudo -E apt-get install -y "${missing[@]}"
    elif command -v dnf > /dev/null 2>&1; then
      sudo dnf install -y "${missing[@]}"
    elif command -v yum > /dev/null 2>&1; then
      sudo yum install -y "${missing[@]}"
    elif command -v apk > /dev/null 2>&1; then
      sudo apk add --no-cache "${missing[@]}"
    else
      echo "ERROR: ${missing[*]} required. No supported package manager found (apt, dnf, yum, apk)."
      exit 1
    fi
  elif [ "$OS_NAME" = "Darwin" ]; then
    if command -v brew > /dev/null 2>&1; then
      brew_install_quiet "${missing[@]}"
    else
      echo "ERROR: ${missing[*]} required. Install with: brew install ${missing[*]}"
      exit 1
    fi
  else
    echo "ERROR: ${missing[*]} required. Install them for your OS and re-run."
    exit 1
  fi
}

install_missing_tools

for REQUIRED_TOOL in jq lz4; do
  if ! command -v "$REQUIRED_TOOL" > /dev/null 2>&1; then
    echo "ERROR: ${REQUIRED_TOOL} is still not available after install attempt."
    exit 1
  fi
done

build_register_payload() {
  local ts="$1"
  jq -nc \
    --arg oa "${VALIDATOR_ADDR}" \
    --arg u "${VALIDATOR_URL:-}" \
    --arg m "${MONIKER}" \
    --argjson ts "${ts}" \
    '{operator_address:$oa,url:$u,moniker:$m,timestamp:$ts}'
}

build_register_body() {
  local ts="$1"
  local sig="$2"
  local pub_key="$3"
  jq -nc \
    --arg oa "${VALIDATOR_ADDR}" \
    --arg u "${VALIDATOR_URL:-}" \
    --arg m "${MONIKER}" \
    --argjson ts "${ts}" \
    --arg s "${sig}" \
    --arg pk "${pub_key}" \
    '{operator_address:$oa,url:$u,moniker:$m,timestamp:$ts,signature:$s,pub_key:$pk}'
}

JOIN_QUEUE_STATUS="not attempted"

handle_public_url_failure() {
  local message="$1"
  VALIDATOR_URL=""

  if [ "$DOMAIN_MODE" = "explicit" ] && [ "${SVOTE_ALLOW_NO_PUBLIC_URL:-0}" != "1" ]; then
    echo "ERROR: ${message}"
    echo "  You supplied a public domain, so join.sh will not continue without a working Caddy setup."
    echo "  Fix Caddy/DNS, or set SVOTE_ALLOW_NO_PUBLIC_URL=1 to register for funding without a public URL."
    exit 1
  fi

  echo "WARNING: ${message}"
  echo "  Continuing with no public URL. Vote managers can still fund this operator from the join queue."
  echo "  The validator will not be client-ready until a public HTTPS URL is configured."
}

print_join_status() {
  echo ""
  echo "Join queue: ${JOIN_QUEUE_STATUS}"
  if [ -n "${VALIDATOR_URL:-}" ]; then
    echo "Public URL: configured ${VALIDATOR_URL}"
  else
    echo "Public URL: missing"
    echo "  Vote managers can still fund the operator address from the join queue."
    echo "  Configure public HTTPS before treating this validator as client-ready."
  fi
}

systemd_env_quote() {
  local value="$1"
  value="${value//\\/\\\\}"
  value="${value//\"/\\\"}"
  printf '"%s"' "$value"
}

sha256_file() {
  local file="$1"

  if command -v sha256sum > /dev/null 2>&1; then
    sha256sum "$file" | awk '{print $1}'
  elif command -v shasum > /dev/null 2>&1; then
    shasum -a 256 "$file" | awk '{print $1}'
  else
    echo "ERROR: sha256sum or shasum is required to verify the snapshot archive."
    return 1
  fi
}

cleanup_snapshot_tmp() {
  if [ -n "${SNAPSHOT_TMP_DIR:-}" ]; then
    rm -rf "${SNAPSHOT_TMP_DIR}"
  fi
}

finish_snapshot_tmp() {
  cleanup_snapshot_tmp
  SNAPSHOT_TMP_DIR=""
  trap - EXIT
}

restore_latest_snapshot() {
  if [ "${SVOTE_SKIP_SNAPSHOT:-0}" = "1" ]; then
    echo "SVOTE_SKIP_SNAPSHOT=1: skipping snapshot restore; node will sync from genesis."
    return 0
  fi

  echo ""
  echo "=== Restoring latest chain snapshot ==="

  local metadata_url="${SNAPSHOT_BASE_URL%/}/latest.json"
  local metadata_file
  local archive_file
  local listing_file
  local validator_state_file
  local snapshot_chain_id
  local snapshot_url
  local snapshot_checksum
  local snapshot_height
  local snapshot_date
  local expected_checksum
  local actual_checksum

  SNAPSHOT_TMP_DIR=$(mktemp -d)
  trap cleanup_snapshot_tmp EXIT

  metadata_file="${SNAPSHOT_TMP_DIR}/latest.json"
  archive_file="${SNAPSHOT_TMP_DIR}/snapshot.tar.lz4"
  listing_file="${SNAPSHOT_TMP_DIR}/snapshot.files"
  validator_state_file="${SNAPSHOT_TMP_DIR}/priv_validator_state.json"

  echo "Fetching snapshot metadata from ${metadata_url}..."
  if ! curl -fsSL --connect-timeout 15 --max-time 60 -o "${metadata_file}" "${metadata_url}"; then
    echo "WARNING: No snapshot metadata is available from ${metadata_url}."
    echo "  Continuing without snapshot; node will sync from genesis."
    finish_snapshot_tmp
    return 0
  fi

  if ! jq empty "${metadata_file}" > /dev/null 2>&1; then
    echo "WARNING: Snapshot metadata is not valid JSON."
    echo "  Continuing without snapshot; node will sync from genesis."
    finish_snapshot_tmp
    return 0
  fi

  snapshot_chain_id=$(jq -r '.chain_id // empty' "${metadata_file}")
  snapshot_url=$(jq -r '.url // empty' "${metadata_file}")
  snapshot_checksum=$(jq -r '.checksum // empty' "${metadata_file}")
  snapshot_height=$(jq -r '.height // empty' "${metadata_file}")
  snapshot_date=$(jq -r '.date // empty' "${metadata_file}")

  if [ "${snapshot_chain_id}" != "${CHAIN_ID}" ]; then
    echo "WARNING: Snapshot chain_id mismatch. Expected ${CHAIN_ID}, got ${snapshot_chain_id:-<empty>}."
    echo "  Continuing without snapshot; node will sync from genesis."
    finish_snapshot_tmp
    return 0
  fi

  case "${snapshot_url}" in
    http://*|https://*) ;;
    *)
      echo "WARNING: Snapshot metadata does not contain a valid archive URL."
      echo "  Continuing without snapshot; node will sync from genesis."
      finish_snapshot_tmp
      return 0
      ;;
  esac

  if ! printf '%s\n' "${snapshot_checksum}" | grep -Eq '^[0-9a-fA-F]{64}$'; then
    echo "WARNING: Snapshot metadata does not contain a valid SHA-256 checksum."
    echo "  Continuing without snapshot; node will sync from genesis."
    finish_snapshot_tmp
    return 0
  fi

  echo "Latest snapshot: height ${snapshot_height:-unknown} (${snapshot_date:-unknown date})"
  echo "Downloading ${snapshot_url}..."
  if ! curl -fsSL --retry 3 --connect-timeout 15 -o "${archive_file}" "${snapshot_url}"; then
    echo "ERROR: Could not download snapshot archive."
    exit 1
  fi

  expected_checksum=$(printf '%s' "${snapshot_checksum}" | tr 'A-F' 'a-f')
  actual_checksum=$(sha256_file "${archive_file}" | tr 'A-F' 'a-f')
  if [ "${actual_checksum}" != "${expected_checksum}" ]; then
    echo "ERROR: Snapshot checksum mismatch."
    echo "  Expected: ${expected_checksum}"
    echo "  Actual:   ${actual_checksum}"
    exit 1
  fi
  echo "Snapshot checksum verified."

  if ! lz4 -dc "${archive_file}" | tar -tf - > "${listing_file}"; then
    echo "ERROR: Snapshot archive is not readable by lz4 + tar."
    exit 1
  fi

  if ! awk 'BEGIN { ok=1 } !/^data(\/|$)/ || /(^|\/)\.\.(\/|$)/ { print; ok=0 } END { exit ok ? 0 : 1 }' "${listing_file}" > /dev/null; then
    echo "ERROR: Snapshot archive contains unsafe paths."
    exit 1
  fi

  if [ ! -f "${HOME_DIR}/data/priv_validator_state.json" ]; then
    echo "ERROR: ${HOME_DIR}/data/priv_validator_state.json is missing before snapshot restore."
    exit 1
  fi
  cp "${HOME_DIR}/data/priv_validator_state.json" "${validator_state_file}"

  echo "Extracting snapshot data into ${HOME_DIR}/data..."
  rm -rf "${HOME_DIR}/data"
  if ! lz4 -dc "${archive_file}" | tar -C "${HOME_DIR}" -xf -; then
    echo "ERROR: Snapshot extraction failed."
    exit 1
  fi

  if [ ! -d "${HOME_DIR}/data" ]; then
    echo "ERROR: Snapshot archive did not restore ${HOME_DIR}/data."
    exit 1
  fi

  cp "${validator_state_file}" "${HOME_DIR}/data/priv_validator_state.json"
  rm -rf "${HOME_DIR}/data/cs.wal"

  echo "Snapshot restored. Preserved local validator state and removed restored consensus WAL."
  finish_snapshot_tmp
}

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

# ─── Discover network via voting-config (CDN) ───────────────────────────────
# The voting-config JSON is the same one wallets fetch from
# valargroup.github.io/token-holder-voting-config (ZIP 1244 §Vote Configuration
# Format). We use vote_servers[0] as the seed peer for P2P; SVOTE_ADMIN_URL is
# a separate base for one-time join registration and helper heartbeat.

echo ""
echo "=== Discovering network ==="

echo "Fetching voting-config from ${VOTING_CONFIG_URL}..."
if ! VOTING_CONFIG=$(curl -fsSL --retry 5 --retry-delay 2 --retry-max-time 60 --connect-timeout 15 --max-time 60 "${VOTING_CONFIG_URL}"); then
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

# Fetch the node's P2P identity and active app version. The app version, not
# the latest published release marker, is the binary version that can replay
# new blocks without app-hash divergence.
NODE_INFO_URL="${SEED_URL%/}/cosmos/base/tendermint/v1beta1/node_info"
if ! NODE_INFO=$(curl -fsSL --retry 5 --retry-delay 2 --retry-max-time 60 --connect-timeout 15 --max-time 30 "${NODE_INFO_URL}"); then
  echo "ERROR: Could not fetch node_info from ${NODE_INFO_URL}"
  echo "  The seed node may be restarting; retry join.sh in a minute."
  exit 1
fi
NODE_ID=$(echo "$NODE_INFO" | jq -r '.default_node_info.default_node_id // .default_node_info.id // empty')
LISTEN_ADDR=$(echo "$NODE_INFO" | jq -r '.default_node_info.listen_addr // empty')
CHAIN_BINARY_VERSION=$(echo "$NODE_INFO" | jq -r '.application_version.version // empty')

if [ -z "$NODE_ID" ]; then
  echo "ERROR: Could not fetch node_id from ${SEED_URL}"
  exit 1
fi

if [ -n "${SVOTE_RELEASE_VERSION:-}" ]; then
  echo "Using SVOTE_RELEASE_VERSION override: ${SVOTE_RELEASE_VERSION}"
  CHAIN_BINARY_VERSION="${SVOTE_RELEASE_VERSION}"
elif [ -z "$CHAIN_BINARY_VERSION" ] || [ "$CHAIN_BINARY_VERSION" = "null" ]; then
  echo "WARNING: Could not read active chain app version from ${NODE_INFO_URL}."
  echo "  Falling back to ${DO_BASE}/version.txt."
  CHAIN_BINARY_VERSION=$(curl -fsSL "${DO_BASE}/version.txt" | tr -d '[:space:]')
fi

if [ -z "$CHAIN_BINARY_VERSION" ]; then
  echo "ERROR: Could not resolve a release version for this chain."
  exit 1
fi

if ! printf '%s\n' "$CHAIN_BINARY_VERSION" | grep -Eq '^v[0-9]+(\.[0-9]+)*([._-][A-Za-z0-9]+)*$'; then
  echo "ERROR: Active chain app version is not a valid release tag: ${CHAIN_BINARY_VERSION}"
  exit 1
fi

# Extract the host from the seed URL and the P2P port from the node's listen address.
SEED_HOST=$(echo "$SEED_URL" | sed -E 's|^https?://||; s|:[0-9]+$||; s|/.*||')
P2P_PORT=$(echo "$LISTEN_ADDR" | sed -E 's|.*:([0-9]+)$|\1|')
P2P_PORT="${P2P_PORT:-26656}"
PERSISTENT_PEERS="${NODE_ID}@${SEED_HOST}:${P2P_PORT}"
echo "Chain binary version: ${CHAIN_BINARY_VERSION}"
echo "Peers: ${PERSISTENT_PEERS}"

# ─── Acquire binaries ────────────────────────────────────────────────────────
# Download the binary release that the active chain is running. Source
# developers can skip the download by setting SVOTE_LOCAL_BINARIES=1 before
# running the script; that local svoted must report the active chain version.

if [ "${SVOTE_LOCAL_BINARIES:-0}" = "1" ] && command -v svoted > /dev/null 2>&1 && command -v create-val-tx > /dev/null 2>&1; then
  LOCAL_SVOTED_VERSION=$(svoted version 2>/dev/null | tr -d '[:space:]' || true)
  if [ "$LOCAL_SVOTED_VERSION" != "$CHAIN_BINARY_VERSION" ] && [ "${SVOTE_ALLOW_VERSION_MISMATCH:-0}" != "1" ]; then
    echo "ERROR: Local svoted version ${LOCAL_SVOTED_VERSION:-<unknown>} does not match active chain ${CHAIN_BINARY_VERSION}."
    echo "  Unset SVOTE_LOCAL_BINARIES to download the matching release, or set SVOTE_ALLOW_VERSION_MISMATCH=1 if you are intentionally testing a fork."
    exit 1
  fi
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

  VERSION="${CHAIN_BINARY_VERSION}"

  echo "Version: ${VERSION}"
  echo "Platform: ${PLATFORM}"
  # Tarballs live under binaries/vote-sdk/ since release.yml commit b30573d8.
  # version.txt and join.sh itself stay at the bucket root.
  RELEASE_URL="${DO_BASE}/binaries/vote-sdk/shielded-vote-${VERSION}-${PLATFORM}.tar.gz"
  echo "Downloading release tarball..."
  echo "  ${RELEASE_URL}"
  DOWNLOAD_SIZE=$(curl -fsSLI "${RELEASE_URL}" 2>/dev/null | awk 'BEGIN { IGNORECASE=1 } /^content-length:/ { gsub("\r", "", $2); size=$2 } END { print size }' || true)
  if [ -n "${DOWNLOAD_SIZE}" ] && [ "${DOWNLOAD_SIZE}" -gt 0 ] 2>/dev/null; then
    DOWNLOAD_SIZE_LABEL=$(awk -v bytes="${DOWNLOAD_SIZE}" 'BEGIN { printf "%.1f MB", bytes / 1048576 }')
    echo "Size: ${DOWNLOAD_SIZE_LABEL}"
  else
    DOWNLOAD_SIZE=""
  fi

  DOWNLOAD_ATTEMPT=1
  while true; do
    rm -f /tmp/shielded-vote-release.tar.gz

    curl -fsSL -o /tmp/shielded-vote-release.tar.gz "${RELEASE_URL}" &
    CURL_PID=$!
    LAST_PERCENT=0
    LAST_MB=0

    while kill -0 "${CURL_PID}" 2>/dev/null; do
      if [ -f /tmp/shielded-vote-release.tar.gz ]; then
        DOWNLOADED_BYTES=$(wc -c < /tmp/shielded-vote-release.tar.gz | tr -d '[:space:]')
        if [ -n "${DOWNLOAD_SIZE}" ] && [ "${DOWNLOAD_SIZE}" -gt 0 ] 2>/dev/null; then
          PERCENT=$((DOWNLOADED_BYTES * 100 / DOWNLOAD_SIZE))
          PROGRESS_STEP=$((PERCENT / 10 * 10))
          if [ "${PROGRESS_STEP}" -gt "${LAST_PERCENT}" ] && [ "${PROGRESS_STEP}" -lt 100 ]; then
            echo "Download progress: ${PROGRESS_STEP}%"
            LAST_PERCENT="${PROGRESS_STEP}"
          fi
        else
          DOWNLOADED_MB=$((DOWNLOADED_BYTES / 1048576))
          if [ "${DOWNLOADED_MB}" -ge $((LAST_MB + 25)) ]; then
            echo "Download progress: ${DOWNLOADED_MB} MB"
            LAST_MB="${DOWNLOADED_MB}"
          fi
        fi
      fi
      sleep 2
    done

    if wait "${CURL_PID}"; then
      echo "Download progress: 100%"
      break
    fi

    if [ "${DOWNLOAD_ATTEMPT}" -ge 3 ]; then
      echo "ERROR: Failed to download release tarball after 3 attempts."
      exit 1
    fi

    DOWNLOAD_ATTEMPT=$((DOWNLOAD_ATTEMPT + 1))
    echo "Download failed; retrying (${DOWNLOAD_ATTEMPT}/3)..."
    sleep 2
  done

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

# ─── Initialize node ─────────────────────────────────────────────────────────

# Clean previous state if present.
if [ -d "${HOME_DIR}" ]; then
  echo "Removing existing ${HOME_DIR}..."
  rm -rf "${HOME_DIR}"
fi

# Suppress successful init output; replay stderr only when init fails.
if ! INIT_ERR=$(svoted init "${MONIKER}" --chain-id "${CHAIN_ID}" --home "${HOME_DIR}" 2>&1 > /dev/null); then
  if [ -n "$INIT_ERR" ]; then
    printf '%s\n' "$INIT_ERR"
  fi
  echo "ERROR: svoted init failed. Typical causes: missing dynamic libraries (ldd on the svoted binary), disk full, or invalid moniker."
  exit 1
fi

# ─── Fetch genesis ───────────────────────────────────────────────────────────
# sdk-chain-reset.yml's upload-genesis job writes the canonical genesis to
# s3://vote/genesis.json after every reset. As a guard against a stale bucket
# object during manual ops, also compare it with the live seed's genesis endpoint
# and prefer the live seed copy if they differ.

GENESIS_URL="${DO_BASE}/genesis.json"
LIVE_GENESIS_URL="${SEED_URL%/}/shielded-vote/v1/genesis"
echo "Fetching genesis.json from ${GENESIS_URL}..."
GENESIS_TMP=$(mktemp)
LIVE_GENESIS_TMP=$(mktemp)
cleanup_genesis_tmp() {
  rm -f "${GENESIS_TMP}" "${LIVE_GENESIS_TMP}"
}
trap cleanup_genesis_tmp EXIT

curl -fsSL -o "${GENESIS_TMP}" "${GENESIS_URL}"
if curl -fsSL -o "${LIVE_GENESIS_TMP}" "${LIVE_GENESIS_URL}" 2>/dev/null; then
  if ! cmp -s "${GENESIS_TMP}" "${LIVE_GENESIS_TMP}"; then
    echo "WARNING: ${GENESIS_URL} does not match live seed genesis."
    echo "  Using live seed genesis from ${LIVE_GENESIS_URL}."
    cp "${LIVE_GENESIS_TMP}" "${GENESIS_TMP}"
  fi
else
  echo "WARNING: Could not fetch live seed genesis from ${LIVE_GENESIS_URL}; using ${GENESIS_URL}."
fi
cp "${GENESIS_TMP}" "${HOME_DIR}/config/genesis.json"
if ! svoted genesis validate-genesis --home "${HOME_DIR}"; then
  echo "ERROR: genesis.json failed validation against this svoted build."
  exit 1
fi
echo "Genesis validated."
cleanup_genesis_tmp
trap - EXIT

# ─── Restore latest chain snapshot ───────────────────────────────────────────

restore_latest_snapshot

# ─── Generate keys ────────────────────────────────────────────────────────────

echo ""
echo "=== Generating cryptographic keys ==="

svoted init-validator-keys --home "${HOME_DIR}"

VALIDATOR_ADDR=$(svoted keys show validator -a --keyring-backend test --home "${HOME_DIR}")
VALIDATOR_VALOPER=$(svoted keys show validator --bech val -a --keyring-backend test --home "${HOME_DIR}")

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

# Admin server base URL — used by the helper for POST /api/server-heartbeat
# every 2h after helper_url is set. Empty disables the heartbeat.
admin_url = "${SVOTE_ADMIN_URL}"

# This server's public URL as seen by clients (set after Caddy TLS setup).
# Empty disables the heartbeat.
helper_url = ""
HELPERCFG

echo "Node configured."

# ─── TLS reverse proxy (Caddy) ──────────────────────────────────────────────
# Optionally sets up Caddy as a TLS reverse proxy in front of the chain REST API
# (port 1317). Caddy auto-provisions Let's Encrypt certificates.
#
# By default, interactive runs prompt for the TLS mode and non-interactive runs
# skip Caddy. Set SVOTE_DOMAIN or pass --domain to install Caddy for a static DNS
# name, or choose auto sslip.io from the interactive prompt for trial installs.

echo ""
echo "=== Setting up TLS reverse proxy ==="

prompt_tls_mode() {
  if ! { : < /dev/tty; } 2>/dev/null; then
    echo "INFO: No TTY for the TLS prompt; defaulting to skip Caddy."
    echo "  Set SVOTE_DOMAIN=<host> for Caddy + custom domain, or run interactively to pick sslip.io."
    DOMAIN_MODE="skip"
    SVOTE_SKIP_CADDY=1
    return 0
  fi

  echo ""
  echo "How would you like to expose this validator over HTTPS?"
  echo "  1) Skip Caddy                     (terminate TLS upstream yourself)   [default]"
  echo "  2) Custom domain + Caddy          (production; static DNS record required)"
  echo "  3) Auto: <ip>.sslip.io + Caddy    (trial / smoke; static IP required)"
  printf "Choose [1-3] (default 1): "

  local choice
  read -r -t "${SVOTE_TLS_PROMPT_TIMEOUT:-30}" choice < /dev/tty || choice=""
  case "$choice" in
    ""|1)
      DOMAIN_MODE="skip"
      SVOTE_SKIP_CADDY=1
      ;;
    2)
      echo "Configure DNS before continuing:"
      echo "  val.example.org.  A  <your-server-public-IPv4>"
      printf "Domain (e.g. val.example.org): "
      read -r SVOTE_DOMAIN < /dev/tty
      DOMAIN_MODE="explicit"
      ;;
    3) ;;
    *)
      echo "Unrecognized choice '${choice}'; defaulting to skip Caddy."
      DOMAIN_MODE="skip"
      SVOTE_SKIP_CADDY=1
      ;;
  esac
}

if [ "${SVOTE_SKIP_CADDY:-0}" != "1" ] && [ -z "${SVOTE_DOMAIN}" ]; then
  prompt_tls_mode
fi

if [ "${SVOTE_SKIP_CADDY:-0}" = "1" ]; then
  DOMAIN_MODE="skip"
  echo "SVOTE_SKIP_CADDY=1: skipping Caddy setup."
  handle_public_url_failure "Caddy setup skipped by SVOTE_SKIP_CADDY=1."
  VALIDATOR_URL=""
elif [ -z "$SVOTE_DOMAIN" ]; then
  # Auto-detect public IPv4 and use sslip.io for a valid TLS hostname. Avoid
  # IPv6 here: a raw IPv6 address contains colons, which cannot appear in a
  # DNS name and causes ACME certificate issuance to retry forever.
  PUBLIC_IP=$(curl -4 -fsSL --connect-timeout 5 https://ifconfig.me 2>/dev/null || \
    curl -4 -fsSL --connect-timeout 5 https://api.ipify.org 2>/dev/null || echo "")
  if ! echo "$PUBLIC_IP" | jq -eR 'test("^([0-9]{1,3}\\.){3}[0-9]{1,3}$")' > /dev/null 2>&1; then
    handle_public_url_failure "Could not detect a public IPv4 address for sslip.io. Detected value: ${PUBLIC_IP:-<empty>}. Re-run with --domain <hostname>, set SVOTE_DOMAIN, or set SVOTE_SKIP_CADDY=1."
  else
    SVOTE_DOMAIN="$(echo "$PUBLIC_IP" | tr '.' '-').sslip.io"
    echo "Detected public IP: ${PUBLIC_IP}"
    echo "Using sslip.io domain: ${SVOTE_DOMAIN}"
  fi
fi

if [ "$DOMAIN_MODE" != "skip" ] && [ -n "$SVOTE_DOMAIN" ] && echo "$SVOTE_DOMAIN" | jq -eR 'contains(":") or test("\\s")' > /dev/null 2>&1; then
  handle_public_url_failure "Invalid TLS hostname '${SVOTE_DOMAIN}'. Use a DNS hostname without colons/spaces, or set SVOTE_SKIP_CADDY=1."
  SVOTE_DOMAIN=""
fi

if [ "$DOMAIN_MODE" != "skip" ] && [ -n "$SVOTE_DOMAIN" ]; then
  VALIDATOR_URL="https://${SVOTE_DOMAIN}"

  # Install Caddy if not present.
  if ! command -v caddy > /dev/null 2>&1; then
    OS_NAME=$(uname -s)
    if [ "$OS_NAME" = "Linux" ]; then
      echo "Installing Caddy..."
      if command -v apt-get > /dev/null 2>&1; then
        export DEBIAN_FRONTEND=noninteractive NEEDRESTART_MODE=a
        if ! {
          sudo -E apt-get install -y debian-keyring debian-archive-keyring apt-transport-https curl &&
          curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/gpg.key' | sudo gpg --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg &&
          curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt' | sudo tee /etc/apt/sources.list.d/caddy-stable.list > /dev/null &&
          sudo -E apt-get update &&
          sudo -E apt-get install -y caddy
        }; then
          handle_public_url_failure "Caddy installation failed."
        fi
      else
        handle_public_url_failure "apt not found; automatic Caddy installation is unavailable. Install Caddy manually: https://caddyserver.com/docs/install"
      fi
    elif [ "$OS_NAME" = "Darwin" ]; then
      if command -v brew > /dev/null 2>&1; then
        if ! brew_install_quiet caddy; then
          handle_public_url_failure "Caddy installation via Homebrew failed."
        fi
      else
        handle_public_url_failure "Homebrew not found. Install Caddy with 'brew install caddy' after installing Homebrew from https://brew.sh."
      fi
    else
      handle_public_url_failure "Automatic Caddy installation is only supported on Linux (apt) and macOS (Homebrew). Install Caddy manually: https://caddyserver.com/docs/install"
    fi
  fi
fi

if [ -n "$VALIDATOR_URL" ] && ! command -v caddy > /dev/null 2>&1; then
  handle_public_url_failure "Caddy is not available after installation."
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
    if ! CADDY_VALIDATE_OUTPUT=$(caddy validate --config "${CADDYFILE}" 2>&1); then
      printf '%s\n' "${CADDY_VALIDATE_OUTPUT}"
      handle_public_url_failure "Caddy config validation failed for ${CADDYFILE}."
    fi
    # On macOS, Caddy is managed as part of the launchd plist (see below).
    if [ -n "$VALIDATOR_URL" ]; then
      echo "Caddy config validated: ${VALIDATOR_URL} → localhost:1317"
    fi
  else
    if ! CADDY_VALIDATE_OUTPUT=$(sudo caddy validate --config "${CADDYFILE}" 2>&1); then
      printf '%s\n' "${CADDY_VALIDATE_OUTPUT}"
      handle_public_url_failure "Caddy config validation failed for ${CADDYFILE}."
    fi
    if [ -n "$VALIDATOR_URL" ]; then
      if command -v systemctl > /dev/null 2>&1; then
        if ! sudo systemctl restart caddy; then
          if ! sudo caddy reload --config "${CADDYFILE}"; then
            handle_public_url_failure "Caddy restart/reload failed for ${CADDYFILE}."
          fi
        fi
      elif ! sudo caddy reload --config "${CADDYFILE}"; then
        handle_public_url_failure "Caddy reload failed for ${CADDYFILE}."
      fi
      if [ -n "$VALIDATOR_URL" ]; then
        echo "Caddy configured: ${VALIDATOR_URL} → localhost:1317"
      fi
    fi
  fi
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

if [ -n "$SVOTE_ADMIN_URL" ]; then
  echo ""
  echo "=== Registering with vote network ==="

  TIMESTAMP=$(date +%s)
  REG_PAYLOAD=$(build_register_payload "${TIMESTAMP}")

  if SIG_JSON=$(svoted sign-arbitrary "$REG_PAYLOAD" --from validator --keyring-backend test --home "${HOME_DIR}" 2>/dev/null); then
    SIG=$(echo "$SIG_JSON" | jq -r '.signature')
    PUB_KEY=$(echo "$SIG_JSON" | jq -r '.pub_key')

    REG_BODY=$(build_register_body "${TIMESTAMP}" "${SIG}" "${PUB_KEY}")

    REG_RESULT=$(curl -fsSL -X POST "${SVOTE_ADMIN_URL%/}/api/register-validator" \
      -H "Content-Type: application/json" \
      -d "$REG_BODY" 2>/dev/null || echo "")

    if [ -n "$REG_RESULT" ]; then
      REG_STATUS=$(echo "$REG_RESULT" | jq -r '.status // empty' 2>/dev/null || echo "")
      if [ "$REG_STATUS" = "pending" ] || [ "$REG_STATUS" = "registered" ] || [ "$REG_STATUS" = "bonded" ]; then
        JOIN_QUEUE_STATUS="${REG_STATUS}"
        echo "Registered (${REG_STATUS}). The admin will see your request."
      else
        JOIN_QUEUE_STATUS="unexpected response"
        echo "WARNING: Registration response: ${REG_RESULT}"
      fi
    else
      JOIN_QUEUE_STATUS="registration API unreachable"
      echo "WARNING: Could not reach registration API. You can register manually later:"
      echo "  svoted sign-arbitrary '<payload>' --from validator --keyring-backend test --home ${HOME_DIR}"
    fi
  else
    JOIN_QUEUE_STATUS="signature failed"
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
  print_join_status
  exit 0
fi

LOG_FILE="${HOME_DIR}/node.log"
SVOTED_BIN=$(command -v svoted)
WRAPPER_BIN="${INSTALL_DIR}/svoted-wrapper.sh"
SERVICE_NAME="svoted"
SERVICE_PATH="${INSTALL_DIR}:/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin"

# Install svoted-wrapper.sh next to svoted/create-val-tx.
if [ -n "${SVOTE_WRAPPER_SCRIPT:-}" ] && [ -f "${SVOTE_WRAPPER_SCRIPT}" ]; then
  cp "${SVOTE_WRAPPER_SCRIPT}" "${WRAPPER_BIN}"
elif [ -n "${BASH_SOURCE[0]:-}" ] && [ "${BASH_SOURCE[0]}" != "bash" ] && [ -f "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/scripts/svoted-wrapper.sh" ]; then
  cp "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/scripts/svoted-wrapper.sh" "${WRAPPER_BIN}"
elif curl -fsSL "${DO_BASE}/svoted-wrapper.sh" -o "${WRAPPER_BIN}" 2>/dev/null; then
  :
else
  echo "ERROR: svoted-wrapper.sh not found. Clone vote-sdk and run ./join.sh from the repo, set SVOTE_WRAPPER_SCRIPT, or publish svoted-wrapper.sh to ${DO_BASE}/svoted-wrapper.sh" >&2
  exit 1
fi
chmod +x "${WRAPPER_BIN}"

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
  launchctl bootout "gui/$(id -u)/com.shielded-vote.join" 2>/dev/null || true
  rm -f "${PLIST_DIR}/com.shielded-vote.join.plist"

  cat > "${PLIST_FILE}" <<PLISTEOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>${PLIST_LABEL}</string>
    <key>ProgramArguments</key>
    <array>
        <string>${WRAPPER_BIN}</string>
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
        <string>${SERVICE_PATH}</string>
        <key>SVOTE_HOME</key>
        <string>${HOME_DIR}</string>
        <key>VALIDATOR_ADDR</key>
        <string>${VALIDATOR_ADDR}</string>
        <key>VALIDATOR_VALOPER</key>
        <string>${VALIDATOR_VALOPER}</string>
        <key>MONIKER</key>
        <string>${MONIKER}</string>
        <key>SVOTE_INSTALL_DIR</key>
        <string>${INSTALL_DIR}</string>
        <key>SVOTED_BIN</key>
        <string>${SVOTED_BIN}</string>
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

  echo "Validator wrapper will complete bonding after funding (logs: ${LOG_FILE})"

else
  # ── Linux: systemd ──────────────────────────────────────────────────────────
  echo "=== Installing systemd service ==="

  SYSTEMD_PATH=$(systemd_env_quote "PATH=${SERVICE_PATH}")
  SYSTEMD_HOME=$(systemd_env_quote "SVOTE_HOME=${HOME_DIR}")
  SYSTEMD_ADDR=$(systemd_env_quote "VALIDATOR_ADDR=${VALIDATOR_ADDR}")
  SYSTEMD_VALOPER=$(systemd_env_quote "VALIDATOR_VALOPER=${VALIDATOR_VALOPER}")
  SYSTEMD_MONIKER=$(systemd_env_quote "MONIKER=${MONIKER}")
  SYSTEMD_INSTALL=$(systemd_env_quote "SVOTE_INSTALL_DIR=${INSTALL_DIR}")
  SYSTEMD_SVOTED=$(systemd_env_quote "SVOTED_BIN=${SVOTED_BIN}")

  sudo tee /etc/systemd/system/${SERVICE_NAME}.service > /dev/null <<SVCEOF
[Unit]
Description=Shielded-Vote validator (${MONIKER})
After=network.target

[Service]
Type=simple
User=$(whoami)
Environment=${SYSTEMD_PATH} ${SYSTEMD_HOME} ${SYSTEMD_ADDR} ${SYSTEMD_VALOPER} ${SYSTEMD_MONIKER} ${SYSTEMD_INSTALL} ${SYSTEMD_SVOTED}
ExecStart=${WRAPPER_BIN}
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
  echo "Validator wrapper will complete bonding after funding."
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
echo "  Congratulations, your node is synced"
echo "============================================="
echo ""
echo "  Operator address (fund this in the admin Join queue UI):"
echo "    ${VALIDATOR_ADDR}"
echo ""
print_join_status
echo ""
echo "How to monitor:"
if [ "$(uname -s)" = "Darwin" ]; then
  echo "  Chain logs:     tail -f ${LOG_FILE}"
  SERVICE_WATCHER_NAME="com.shielded-vote.validator (launchd)"
  SERVICE_WATCHER_REMOVE="launchctl bootout gui/$(id -u)/com.shielded-vote.validator"
else
  echo "  Chain logs:     journalctl -u ${SERVICE_NAME} -f"
  SERVICE_WATCHER_NAME="svoted (systemd)"
  SERVICE_WATCHER_REMOVE="sudo systemctl stop svoted"
fi
echo ""
echo "Next step: message the voting admin and ask them to approve you as a validator."
echo "Send this message:"
echo ""
echo "  Please approve my Shielded-Vote validator."
echo "  Name: ${MONIKER}"
echo "  Validator address: ${VALIDATOR_ADDR}"
echo "  Public URL: ${VALIDATOR_URL:-not configured}"
echo ""
echo "Validator service:"
echo "  ${SERVICE_WATCHER_NAME} is checking local funding and will bond automatically."
echo "  After bonding it writes ${HOME_DIR}/join-complete and skips join logic on future restarts."
echo "  To stop it manually: ${SERVICE_WATCHER_REMOVE}"
echo ""
if [ -n "${VALIDATOR_URL:-}" ]; then
  echo "After bonding, add your public URL to vote_servers via a PR:"
  echo "  https://github.com/valargroup/token-holder-voting-config"
  echo "  Suggested JSON entry:"
  echo "    $(jq -nc --arg url "${VALIDATOR_URL}" --arg label "${MONIKER}" '{url:$url,label:$label}')"
else
  echo "After bonding, configure a public HTTPS URL before adding this validator to vote_servers:"
  echo "  https://github.com/valargroup/token-holder-voting-config"
fi
}

main "$@"
