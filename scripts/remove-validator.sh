#!/usr/bin/env bash
# Remove a local Shielded Vote validator setup created by join.sh or the
# manual setup guide. This deletes validator identity and chain data.
set -euo pipefail

readonly CONFIRM_TEXT="REMOVE VALIDATOR"
TS="$(date -u +%Y%m%dT%H%M%SZ)"
readonly TS

SVOTE_HOME="${SVOTE_HOME:-$HOME/.svoted}"
SVOTE_INSTALL_DIR="${SVOTE_INSTALL_DIR:-$HOME/.local/bin}"

log() {
  printf '==> %s\n' "$*"
}

warn() {
  printf 'WARNING: %s\n' "$*" >&2
}

as_root() {
  if [ "${EUID:-$(id -u)}" -eq 0 ]; then
    "$@"
  elif command -v sudo >/dev/null 2>&1; then
    sudo "$@"
  else
    echo "This step requires root privileges, but sudo was not found." >&2
    return 1
  fi
}

confirm() {
  if [ "${SVOTE_REMOVE_CONFIRM:-}" = "$CONFIRM_TEXT" ] || [ "${SVOTE_REMOVE_YES:-0}" = "1" ]; then
    return 0
  fi

  if [ ! -r /dev/tty ] || [ ! -w /dev/tty ]; then
    cat >&2 <<EOF
This script deletes validator keys, local chain data, and services.
Re-run from an interactive terminal, or set:
  SVOTE_REMOVE_CONFIRM="$CONFIRM_TEXT"
EOF
    exit 1
  fi

  cat >/dev/tty <<EOF

This will permanently remove the local Shielded Vote validator setup.

It deletes:
  - validator home: ${SVOTE_HOME}
  - svoted and svoted-join services
  - local join binaries under ${SVOTE_INSTALL_DIR}
  - generated validator Caddy config when it is clearly dedicated to this node

If config/priv_validator_key.json, config/node_key.json, keyring-test/,
pallas.*, and ea.* are not backed up, this validator identity is lost. A new
join will create a new validator identity and must go through approval again.

EOF
  printf 'Type %s to continue: ' "$CONFIRM_TEXT" >/dev/tty
  local answer
  IFS= read -r answer </dev/tty || exit 1
  if [ "$answer" != "$CONFIRM_TEXT" ]; then
    echo "Aborted." >/dev/tty
    exit 1
  fi
}

remove_file() {
  local path="$1"
  if [ -e "$path" ] || [ -L "$path" ]; then
    log "Removing ${path}"
    rm -f -- "$path"
  fi
}

remove_tree() {
  local path="$1"
  if [ -e "$path" ] || [ -L "$path" ]; then
    log "Removing ${path}"
    rm -rf -- "$path"
  fi
}

remove_root_file() {
  local path="$1"
  if [ -e "$path" ] || [ -L "$path" ]; then
    log "Removing ${path}"
    as_root rm -f -- "$path"
  fi
}

remove_systemd_unit() {
  local unit="$1"
  if ! command -v systemctl >/dev/null 2>&1; then
    return 0
  fi

  log "Stopping ${unit} if present"
  as_root systemctl stop "$unit" 2>/dev/null || true
  as_root systemctl disable "$unit" 2>/dev/null || true
}

remove_launchd_agent() {
  local label="$1"
  local plist="$HOME/Library/LaunchAgents/${label}.plist"

  if ! command -v launchctl >/dev/null 2>&1; then
    return 0
  fi

  log "Unloading ${label} if present"
  launchctl bootout "gui/$(id -u)/${label}" 2>/dev/null || true
  remove_file "$plist"
}

generated_validator_caddyfile() {
  local file="$1"
  [ -f "$file" ] || return 1
  grep -Eq 'reverse_proxy[[:space:]]+(localhost|127[.]0[.]0[.]1):1317' "$file" || return 1

  local active_lines
  active_lines="$(awk 'NF && $1 !~ /^#/ { count++ } END { print count + 0 }' "$file")"
  [ "${active_lines:-999}" -le 4 ]
}

remove_linux_caddy_if_generated() {
  local caddyfile="/etc/caddy/Caddyfile"
  if ! generated_validator_caddyfile "$caddyfile"; then
    if [ -f "$caddyfile" ] && grep -Eq 'reverse_proxy[[:space:]]+(localhost|127[.]0[.]0[.]1):1317' "$caddyfile"; then
      warn "Leaving ${caddyfile}; it contains a validator proxy but does not look dedicated."
    fi
    return 0
  fi

  local backup="${caddyfile}.svote-validator-remove-${TS}"
  log "Backing up generated Caddy config to ${backup}"
  as_root cp "$caddyfile" "$backup"
  as_root rm -f "$caddyfile"

  if command -v systemctl >/dev/null 2>&1; then
    log "Stopping Caddy because the generated validator config was removed"
    as_root systemctl stop caddy 2>/dev/null || true
    as_root systemctl disable caddy 2>/dev/null || true
  fi
}

remove_macos_caddy_if_generated() {
  local caddyfile="$HOME/.config/caddy/Caddyfile"
  if ! generated_validator_caddyfile "$caddyfile"; then
    if [ -f "$caddyfile" ] && grep -Eq 'reverse_proxy[[:space:]]+(localhost|127[.]0[.]0[.]1):1317' "$caddyfile"; then
      warn "Leaving ${caddyfile}; it contains a validator proxy but does not look dedicated."
    fi
    return 0
  fi

  local backup="${caddyfile}.svote-validator-remove-${TS}"
  log "Backing up generated Caddy config to ${backup}"
  cp "$caddyfile" "$backup"
  rm -f "$caddyfile"
}

main() {
  confirm

  log "Removing validator services"
  remove_systemd_unit svoted-join
  remove_systemd_unit svoted
  remove_root_file /etc/systemd/system/svoted-join.service
  remove_root_file /etc/systemd/system/svoted.service
  remove_root_file /etc/default/svoted-join
  remove_root_file /etc/default/svoted
  if command -v systemctl >/dev/null 2>&1; then
    as_root systemctl daemon-reload 2>/dev/null || true
    as_root systemctl reset-failed svoted svoted-join 2>/dev/null || true
  fi

  remove_launchd_agent com.shielded-vote.join
  remove_launchd_agent com.shielded-vote.validator
  remove_launchd_agent com.shielded-vote.caddy

  log "Removing local validator data"
  remove_tree "$SVOTE_HOME"

  log "Removing local validator binaries"
  remove_file "${SVOTE_INSTALL_DIR}/svoted"
  remove_file "${SVOTE_INSTALL_DIR}/create-val-tx"
  remove_file "${SVOTE_INSTALL_DIR}/join-loop.sh"
  remove_file "${SVOTE_INSTALL_DIR}/svoted-wrapper.sh"

  remove_linux_caddy_if_generated
  remove_macos_caddy_if_generated

  cat <<EOF

Validator teardown complete.

Next checks:
  - Remove this validator's public URL from vote_servers[] if it was published.
  - Keep any off-host key backup only if you may need this validator identity again.
EOF
}

main "$@"
