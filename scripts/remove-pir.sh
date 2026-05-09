#!/usr/bin/env bash
# Remove a local Shielded Vote PIR server setup created by start_pir.sh or the
# manual setup guide. This deletes local PIR data and service configuration.
set -euo pipefail

readonly CONFIRM_TEXT="REMOVE PIR"
TS="$(date -u +%Y%m%dT%H%M%SZ)"
readonly TS

PIR_INSTALL_ROOT="${PIR_INSTALL_ROOT:-/opt/nf-ingest}"
PIR_SERVICE_NAME="${PIR_SERVICE_NAME:-nullifier-query-server}"

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
  if [ "${SVOTE_PIR_REMOVE_CONFIRM:-}" = "$CONFIRM_TEXT" ] || [ "${SVOTE_REMOVE_YES:-0}" = "1" ]; then
    return 0
  fi

  if [ ! -r /dev/tty ] || [ ! -w /dev/tty ]; then
    cat >&2 <<EOF
This script deletes the PIR service, configuration, and local PIR data.
Re-run from an interactive terminal, or set:
  SVOTE_PIR_REMOVE_CONFIRM="$CONFIRM_TEXT"
EOF
    exit 1
  fi

  cat >/dev/tty <<EOF

This will permanently remove the local Shielded Vote PIR server setup.

It deletes:
  - PIR install root: ${PIR_INSTALL_ROOT}
  - ${PIR_SERVICE_NAME} systemd service
  - /etc/default/nf-server
  - /usr/local/bin/nf-server when it points at ${PIR_INSTALL_ROOT}/nf-server
  - generated PIR Caddy config when it is clearly dedicated to this node

For a bootstrapped serve-only PIR host, the tier files can be downloaded again.
If you used synced mode or kept local-only nullifier artifacts here, back them
up before continuing.

EOF
  printf 'Type %s to continue: ' "$CONFIRM_TEXT" >/dev/tty
  local answer
  IFS= read -r answer </dev/tty || exit 1
  if [ "$answer" != "$CONFIRM_TEXT" ]; then
    echo "Aborted." >/dev/tty
    exit 1
  fi
}

remove_root_file() {
  local path="$1"
  if [ -e "$path" ] || [ -L "$path" ]; then
    log "Removing ${path}"
    as_root rm -f -- "$path"
  fi
}

remove_root_tree() {
  local path="$1"
  if [ -e "$path" ] || [ -L "$path" ]; then
    log "Removing ${path}"
    as_root rm -rf -- "$path"
  fi
}

clear_root_dir() {
  local path="$1"
  [ -d "$path" ] || return 0
  log "Deleting contents of ${path}"
  as_root find "$path" -mindepth 1 -maxdepth 1 -exec rm -rf -- {} +
}

remove_fstab_entries() {
  [ -f /etc/fstab ] || return 0
  if ! grep -Eq '[[:space:]](/opt/nf-ingest|/mnt/pir-data)[[:space:]]' /etc/fstab; then
    return 0
  fi

  local backup="/etc/fstab.svote-pir-remove-${TS}"
  log "Backing up /etc/fstab to ${backup}"
  as_root cp /etc/fstab "$backup"
  as_root sed -i \
    -e '\@[[:space:]]/opt/nf-ingest[[:space:]]@d' \
    -e '\@[[:space:]]/mnt/pir-data[[:space:]]@d' \
    /etc/fstab
}

remove_symlink_if_owned() {
  local path="/usr/local/bin/nf-server"
  if [ -L "$path" ] && [ "$(readlink "$path")" = "${PIR_INSTALL_ROOT}/nf-server" ]; then
    remove_root_file "$path"
  elif [ -e "$path" ]; then
    warn "Leaving ${path}; it does not point at ${PIR_INSTALL_ROOT}/nf-server."
  fi
}

generated_pir_caddyfile() {
  local file="$1"
  [ -f "$file" ] || return 1
  grep -Eq 'reverse_proxy[[:space:]]+(localhost|127[.]0[.]0[.]1):3000' "$file" || return 1

  local active_lines
  active_lines="$(awk 'NF && $1 !~ /^#/ { count++ } END { print count + 0 }' "$file")"
  [ "${active_lines:-999}" -le 10 ]
}

remove_caddy_if_generated() {
  local caddyfile="/etc/caddy/Caddyfile"
  if ! generated_pir_caddyfile "$caddyfile"; then
    if [ -f "$caddyfile" ] && grep -Eq 'reverse_proxy[[:space:]]+(localhost|127[.]0[.]0[.]1):3000' "$caddyfile"; then
      warn "Leaving ${caddyfile}; it contains a PIR proxy but does not look dedicated."
    fi
    return 0
  fi

  local backup="${caddyfile}.svote-pir-remove-${TS}"
  log "Backing up generated Caddy config to ${backup}"
  as_root cp "$caddyfile" "$backup"
  as_root rm -f "$caddyfile"

  if command -v systemctl >/dev/null 2>&1; then
    log "Stopping Caddy because the generated PIR config was removed"
    as_root systemctl stop caddy 2>/dev/null || true
    as_root systemctl disable caddy 2>/dev/null || true
  fi
}

main() {
  confirm

  if command -v systemctl >/dev/null 2>&1; then
    log "Stopping ${PIR_SERVICE_NAME} if present"
    as_root systemctl stop "${PIR_SERVICE_NAME}" 2>/dev/null || true
    as_root systemctl disable "${PIR_SERVICE_NAME}" 2>/dev/null || true
  fi

  remove_root_file "/etc/systemd/system/${PIR_SERVICE_NAME}.service"
  remove_root_file /etc/default/nf-server
  remove_symlink_if_owned

  if command -v mountpoint >/dev/null 2>&1 && mountpoint -q "$PIR_INSTALL_ROOT"; then
    clear_root_dir "$PIR_INSTALL_ROOT"
    log "Unmounting ${PIR_INSTALL_ROOT}"
    if as_root umount "$PIR_INSTALL_ROOT" 2>/dev/null; then
      remove_root_tree "$PIR_INSTALL_ROOT"
    else
      warn "Could not unmount ${PIR_INSTALL_ROOT}; deleted its contents but left the mount point in place."
    fi
  else
    remove_root_tree "$PIR_INSTALL_ROOT"
  fi

  if [ -d /mnt/pir-data ]; then
    if command -v mountpoint >/dev/null 2>&1 && mountpoint -q /mnt/pir-data; then
      clear_root_dir /mnt/pir-data
      log "Unmounting /mnt/pir-data"
      if as_root umount /mnt/pir-data 2>/dev/null; then
        remove_root_tree /mnt/pir-data
      else
        warn "Could not unmount /mnt/pir-data; deleted its contents but left the mount point in place."
      fi
    else
      remove_root_tree /mnt/pir-data
    fi
  fi

  remove_fstab_entries
  remove_caddy_if_generated

  if command -v systemctl >/dev/null 2>&1; then
    as_root systemctl daemon-reload 2>/dev/null || true
    as_root systemctl reset-failed "${PIR_SERVICE_NAME}" 2>/dev/null || true
  fi

  cat <<EOF

PIR teardown complete.

Next checks:
  - Remove this PIR server's public URL from pir_endpoints[] if it was published.
  - If this host used a dedicated volume, detach or destroy the volume separately.
EOF
}

main "$@"
