#!/usr/bin/env bash
# join-full.sh — Join the Shielded-Vote chain as a regular full node.
#
#   curl -fsSL https://shielded-vote.nyc3.digitaloceanspaces.com/join-full.sh | bash
#   curl -fsSL https://shielded-vote.nyc3.digitaloceanspaces.com/join-full.sh | bash -s -- --env stage
#
# This observer-only installer does not generate validator keys, register with
# the join queue, expose a helper server, or configure public TLS.

set -euo pipefail

SVOTE_ENV="${SVOTE_ENV:-prod}"
SVOTE_JOIN_COMMON_VERSION="${SVOTE_JOIN_COMMON_VERSION:-}"
while [ $# -gt 0 ]; do
  case "$1" in
    --env)
      if [ $# -lt 2 ]; then
        echo "ERROR: --env requires one of: prod, stage." >&2
        exit 1
      fi
      SVOTE_ENV="$2"
      shift 2
      ;;
    --env=*)
      SVOTE_ENV="${1#--env=}"
      shift
      ;;
    *)
      echo "ERROR: unknown argument: $1" >&2
      exit 1
      ;;
  esac
done

svote_bootstrap_do_base() {
  local do_base_override="${SVOTE_DO_SPACES_BASE:-${DO_SPACES_BASE:-}}"
  local do_bucket="${SVOTE_DO_SPACES_BUCKET:-${DO_SPACES_BUCKET:-}}"
  local do_region="${SVOTE_DO_SPACES_REGION:-${DO_SPACES_REGION:-nyc3}}"
  if [ -n "${do_base_override}" ]; then
    printf '%s\n' "${do_base_override%/}"
  elif [ -n "${do_bucket}" ]; then
    printf 'https://%s.%s.digitaloceanspaces.com\n' "${do_bucket}" "${do_region}"
  else
    printf '%s\n' "https://shielded-vote.nyc3.digitaloceanspaces.com"
  fi
}

svote_source_common() {
  local common=""
  if [ -n "${SVOTE_JOIN_COMMON:-}" ] && [ -f "${SVOTE_JOIN_COMMON}" ]; then
    common="${SVOTE_JOIN_COMMON}"
  elif [ -n "${BASH_SOURCE[0]:-}" ] && [ "${BASH_SOURCE[0]}" != "bash" ]; then
    local script_dir
    script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
    if [ -f "${script_dir}/_join_common.sh" ]; then
      common="${script_dir}/_join_common.sh"
    elif [ -f "${script_dir}/scripts/_join_common.sh" ]; then
      common="${script_dir}/scripts/_join_common.sh"
    fi
  fi

  if [ -z "${common}" ]; then
    local tmp
    tmp=$(mktemp)
    if [ -n "${SVOTE_JOIN_COMMON_URL:-}" ]; then
      curl -fsSL "${SVOTE_JOIN_COMMON_URL}" -o "${tmp}"
    elif [ -n "${SVOTE_JOIN_COMMON_VERSION}" ]; then
      curl -fsSL "$(svote_bootstrap_do_base)/scripts/join-common/${SVOTE_JOIN_COMMON_VERSION}/_join_common.sh" -o "${tmp}"
    else
      curl -fsSL "$(svote_bootstrap_do_base)/scripts/_join_common.sh" -o "${tmp}"
    fi
    common="${tmp}"
  fi

  # shellcheck source=scripts/_join_common.sh
  . "${common}"
}

svote_source_common
svote_resolve_home
svote_resolve_do_base

INSTALL_DIR="${SVOTE_INSTALL_DIR:-$HOME/.local/bin}"
HOME_DIR="${SVOTE_HOME:-$HOME/.svoted-full}"
MONIKER="${SVOTE_MONIKER:-$(hostname -s 2>/dev/null || hostname 2>/dev/null || echo full-node)}"
SERVICE_NAME="${SVOTE_SERVICE_NAME:-svoted-full}"
ORIGINAL_PATH="${PATH}"

echo "=== Shielded-Vote full-node join ==="
echo ""

if ! command -v curl >/dev/null 2>&1; then
  echo "ERROR: curl is required. Install it and re-run." >&2
  exit 1
fi

svote_install_missing_tools jq lz4

echo ""
echo "=== Discovering network ==="
svote_discover_network
echo "Seed node: ${SEED_URL}"
echo "Chain ID: ${CHAIN_ID}"
echo "Chain binary version: ${CHAIN_BINARY_VERSION}"
echo "Snapshot metadata base: ${SNAPSHOT_BASE_URL}"
echo "Peers: ${PERSISTENT_PEERS}"

echo ""
echo "=== Installing svoted ==="
svote_acquire_binaries 0

case ":${PATH}:" in
  *":${INSTALL_DIR}:"*) ;;
  *) export PATH="${INSTALL_DIR}:${PATH}" ;;
esac

stop_existing_full_node_service() {
  case "$(uname -s)" in
    Linux)
      if command -v systemctl >/dev/null 2>&1 && systemctl is-active --quiet "${SERVICE_NAME}" 2>/dev/null; then
        sudo systemctl stop "${SERVICE_NAME}"
      fi
      ;;
    Darwin)
      launchctl bootout "gui/$(id -u)/com.shielded-vote.full" 2>/dev/null || true
      ;;
  esac
}

echo ""
echo "=== Initializing full node ==="
stop_existing_full_node_service
if [ -d "${HOME_DIR}" ]; then
  if [ "${SVOTE_FORCE_RESET:-0}" != "1" ]; then
    echo "ERROR: ${HOME_DIR} already exists." >&2
    echo "  Re-run with SVOTE_FORCE_RESET=1 only if you intend to delete and recreate this full-node state." >&2
    exit 1
  fi
  echo "SVOTE_FORCE_RESET=1 set; removing ${HOME_DIR}."
  rm -rf "${HOME_DIR}"
fi

svoted init "${MONIKER}" --chain-id "${CHAIN_ID}" --home "${HOME_DIR}" >/dev/null
svote_fetch_genesis
svote_restore_latest_snapshot

CONFIG_TOML="${HOME_DIR}/config/config.toml"
APP_TOML="${HOME_DIR}/config/app.toml"

sed -i.bak "s|persistent_peers = \"\"|persistent_peers = \"${PERSISTENT_PEERS}\"|" "${CONFIG_TOML}"
sed -i.bak "s|\\\$HOME/.svoted|${HOME_DIR}|g" "${APP_TOML}"
rm -f "${CONFIG_TOML}.bak" "${APP_TOML}.bak"

echo "Node configured at ${HOME_DIR}."

if [ "${SVOTE_SKIP_SERVICE:-0}" = "1" ]; then
  echo ""
  echo "Node configured (SVOTE_SKIP_SERVICE=1)."
  echo "Start manually with:"
  echo "  svoted start --home ${HOME_DIR}"
  exit 0
fi

SVOTED_BIN=$(command -v svoted)
LOG_FILE="${HOME_DIR}/node.log"
LOG_FOLLOW_COMMAND=""

echo ""
case "$(uname -s)" in
  Darwin)
    echo "=== Installing launchd service ==="
    PLIST_LABEL="com.shielded-vote.full"
    PLIST_DIR="${HOME}/Library/LaunchAgents"
    PLIST_FILE="${PLIST_DIR}/${PLIST_LABEL}.plist"
    mkdir -p "${PLIST_DIR}"
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
        <string>${INSTALL_DIR}:/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin</string>
        <key>SVOTE_CHAIN_ID</key>
        <string>${CHAIN_ID}</string>
    </dict>
</dict>
</plist>
PLISTEOF
    launchctl bootstrap "gui/$(id -u)" "${PLIST_FILE}"
    LOG_FOLLOW_COMMAND="tail -f ${LOG_FILE}"
    ;;
  Linux)
    echo "=== Installing systemd service ==="
    sudo tee "/etc/systemd/system/${SERVICE_NAME}.service" >/dev/null <<SVCEOF
[Unit]
Description=Shielded-Vote full node (${MONIKER})
After=network.target

[Service]
Type=simple
User=$(whoami)
Environment="PATH=${INSTALL_DIR}:/usr/local/bin:/usr/bin:/bin" "SVOTE_CHAIN_ID=${CHAIN_ID}"
ExecStart=${SVOTED_BIN} start --home ${HOME_DIR}
Restart=on-failure
RestartSec=5
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=multi-user.target
SVCEOF
    sudo systemctl daemon-reload
    sudo systemctl enable "${SERVICE_NAME}"
    sudo systemctl restart "${SERVICE_NAME}"
    LOG_FOLLOW_COMMAND="journalctl -u ${SERVICE_NAME} -f"
    ;;
  *)
    echo "ERROR: Unsupported OS: $(uname -s)." >&2
    exit 1
    ;;
esac

sleep 5
echo "Waiting for node to sync..."
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
    break
  fi
  sleep 5
done

echo ""
echo "============================================="
echo "  Full node synced"
echo "============================================="
echo ""
echo "Home dir:       ${HOME_DIR}"
echo "Chain status:   svoted status --home ${HOME_DIR}"
echo "Logs:           ${LOG_FOLLOW_COMMAND}"
if [ "${ORIGINAL_PATH}" = "${PATH}" ]; then
  :
else
  echo "Current PATH includes ${INSTALL_DIR}; add it to your shell profile for future terminals."
fi
echo ""
echo "Verify a finalized tally with:"
echo "  svoted query vote verify-tally <round-id-hex> --node tcp://localhost:26657"
