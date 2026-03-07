#!/bin/bash
# clean-validator.sh — Remove all state from a previous join.sh run.
#
# Stops svoted, removes the data directory, and uninstalls Caddy config.
# Does NOT remove the downloaded binaries (svoted, create-val-tx) from
# ~/.local/bin — pass --purge to remove those too.
#
# Usage:
#   bash clean-validator.sh          # clean state only
#   bash clean-validator.sh --purge  # clean state + remove binaries

set -euo pipefail

HOME_DIR="${SVOTE_HOME:-$HOME/.svoted}"
INSTALL_DIR="${SVOTE_INSTALL_DIR:-$HOME/.local/bin}"
PURGE=false

for arg in "$@"; do
  case "$arg" in
    --purge) PURGE=true ;;
  esac
done

echo "=== Cleaning validator state ==="

# Stop svoted — prefer systemd if a service is installed.
if systemctl is-active --quiet svoted 2>/dev/null; then
  echo "Stopping svoted systemd service..."
  sudo systemctl stop svoted
  sudo systemctl disable svoted
  sudo rm -f /etc/systemd/system/svoted.service
  sudo systemctl daemon-reload
elif pgrep -x svoted > /dev/null 2>&1; then
  echo "Stopping svoted..."
  pkill -x svoted || true
  sleep 2
fi

# Remove data directory.
if [ -d "$HOME_DIR" ]; then
  echo "Removing ${HOME_DIR}..."
  rm -rf "$HOME_DIR"
else
  echo "No data directory at ${HOME_DIR}"
fi

# Remove Caddy config.
if [ -f /etc/caddy/Caddyfile ]; then
  echo "Removing Caddy config..."
  sudo rm -f /etc/caddy/Caddyfile
  sudo systemctl restart caddy 2>/dev/null || true
fi

# Optionally remove binaries.
if $PURGE; then
  echo "Removing binaries from ${INSTALL_DIR}..."
  rm -f "${INSTALL_DIR}/svoted" "${INSTALL_DIR}/create-val-tx"
fi

echo "Done."
