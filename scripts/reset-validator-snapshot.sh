#!/usr/bin/env bash
# reset-validator-snapshot.sh - Restore latest chain data for an existing validator.
#
# This is for already-initialized validators that need to replace bloated or
# corrupt chain data from the latest pruned snapshot. It preserves the local
# CometBFT validator state file before swapping data/ so the node does not
# reuse a restored signing height.

set -euo pipefail

CHAIN_ID="${SVOTE_CHAIN_ID:-}"
HOME_DIR="${SVOTE_HOME:-$HOME/.svoted}"
INSTALL_DIR="${SVOTE_INSTALL_DIR:-$HOME/.local/bin}"
SNAPSHOT_BASE_URL="${SVOTE_SNAPSHOT_BASE_URL:-https://snapshots.valargroup.org}"
SERVICE_NAME="${SVOTE_SERVICE_NAME:-svoted}"
POST_RESTART_SYNC_TIMEOUT="${SVOTE_POST_RESTART_SYNC_TIMEOUT:-600}"
TMP_PARENT="${SVOTE_TMPDIR:-${TMPDIR:-/tmp}}"
SVOTED_BIN="${SVOTED_BIN:-svoted}"
LAUNCHD_LABEL="${SVOTE_LAUNCHD_LABEL:-com.shielded-vote.validator}"

SNAPSHOT_TMP_DIR=""
SERVICE_MANAGER=""

log() {
  printf '%s\n' "$*"
}

die() {
  log "ERROR: $*" >&2
  exit 1
}

cleanup() {
  if [ -n "${SNAPSHOT_TMP_DIR:-}" ]; then
    rm -rf "${SNAPSHOT_TMP_DIR}"
  fi
}
trap cleanup EXIT

require_tool() {
  local tool="$1"
  command -v "${tool}" >/dev/null 2>&1 || die "${tool} is required."
}

sha256_file() {
  local file="$1"

  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "${file}" | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "${file}" | awk '{print $1}'
  else
    die "sha256sum or shasum is required to verify the snapshot archive."
  fi
}

print_config() {
  log "=== Shielded-Vote validator snapshot reset ==="
  log "  Home:             ${HOME_DIR}"
  log "  Snapshot service: ${SNAPSHOT_BASE_URL}"
  log "  Service name:     ${SERVICE_NAME}"
  log "  Chain ID:         ${CHAIN_ID}"
  log "  Sync timeout:     ${POST_RESTART_SYNC_TIMEOUT}s"
  log "  Temp parent:      ${TMP_PARENT}"
  log ""
}

validate_timeout() {
  case "${POST_RESTART_SYNC_TIMEOUT}" in
    ''|*[!0-9]*) die "SVOTE_POST_RESTART_SYNC_TIMEOUT must be a non-negative integer." ;;
  esac
}

prepare_path() {
  case ":${PATH}:" in
    *":${INSTALL_DIR}:"*) ;;
    *) export PATH="${INSTALL_DIR}:${PATH}" ;;
  esac
}

detect_service_manager() {
  local os_name
  os_name="$(uname -s)"
  case "${os_name}" in
    Linux)
      command -v systemctl >/dev/null 2>&1 || die "systemctl is required on Linux."
      if ! systemctl cat "${SERVICE_NAME}" >/dev/null 2>&1; then
        die "systemd service '${SERVICE_NAME}' is not installed; refusing to reset data without managed service control."
      fi
      SERVICE_MANAGER="systemd"
      ;;
    Darwin)
      command -v launchctl >/dev/null 2>&1 || die "launchctl is required on macOS."
      local plist_file="${HOME}/Library/LaunchAgents/${LAUNCHD_LABEL}.plist"
      [ -f "${plist_file}" ] || die "launchd plist not found: ${plist_file}"
      SERVICE_MANAGER="launchd"
      ;;
    *)
      die "Unsupported OS: ${os_name}. Supported: Linux and Darwin."
      ;;
  esac
}

matching_svoted_processes() {
  ps axww -o pid= -o command= 2>/dev/null | awk -v home="${HOME_DIR}" '
    /[s]voted[[:space:]]+start/ &&
      (index($0, "--home " home) || index($0, "--home=" home)) {
        print $1
      }
  '
}

wait_for_no_svoted_process() {
  local pids
  local waited=0
  while true; do
    pids="$(matching_svoted_processes | tr '\n' ' ' | sed 's/[[:space:]]*$//')"
    if [ -z "${pids}" ]; then
      return 0
    fi
    if [ "${waited}" -ge 20 ]; then
      die "svoted is still running for ${HOME_DIR} after service stop (pid(s): ${pids}). Refusing to modify data."
    fi
    sleep 1
    waited=$((waited + 1))
  done
}

stop_service() {
  log "Stopping validator service..."
  case "${SERVICE_MANAGER}" in
    systemd)
      sudo systemctl stop "${SERVICE_NAME}"
      ;;
    launchd)
      launchctl bootout "gui/$(id -u)/${LAUNCHD_LABEL}" 2>/dev/null || true
      ;;
    *)
      die "service manager was not detected"
      ;;
  esac
  wait_for_no_svoted_process
}

start_service() {
  log "Starting validator service..."
  case "${SERVICE_MANAGER}" in
    systemd)
      sudo systemctl start "${SERVICE_NAME}"
      ;;
    launchd)
      launchctl bootstrap "gui/$(id -u)" "${HOME}/Library/LaunchAgents/${LAUNCHD_LABEL}.plist"
      ;;
    *)
      die "service manager was not detected"
      ;;
  esac
}

preflight_local_home() {
  [ -d "${HOME_DIR}" ] || die "SVOTE_HOME does not exist: ${HOME_DIR}"
  [ -f "${HOME_DIR}/config/genesis.json" ] || die "${HOME_DIR}/config/genesis.json is missing."
  [ -f "${HOME_DIR}/data/priv_validator_state.json" ] || die "${HOME_DIR}/data/priv_validator_state.json is missing."
}

resolve_chain_id() {
  local genesis_file="${HOME_DIR}/config/genesis.json"
  local genesis_chain_id

  genesis_chain_id="$(jq -er '.chain_id | select(type == "string" and length > 0)' "${genesis_file}" 2>/dev/null || true)"
  [ -n "${genesis_chain_id}" ] \
    || die "Could not read chain_id from ${genesis_file}."

  if [ -n "${CHAIN_ID}" ] && [ "${CHAIN_ID}" != "${genesis_chain_id}" ]; then
    die "SVOTE_CHAIN_ID mismatch. Local genesis has ${genesis_chain_id}, override requested ${CHAIN_ID}."
  fi
  CHAIN_ID="${genesis_chain_id}"
}

validate_snapshot_listing() {
  local listing_file="$1"

  if ! awk 'BEGIN { ok=1 } !/^data(\/|$)/ || /(^|\/)\.\.(\/|$)/ { print; ok=0 } END { exit ok ? 0 : 1 }' "${listing_file}" >/dev/null; then
    die "Snapshot archive contains unsafe paths; expected only data/ entries."
  fi
}

stage_latest_snapshot() {
  log "Fetching latest snapshot metadata..."

  local metadata_url="${SNAPSHOT_BASE_URL%/}/latest.json"
  local metadata_file="${SNAPSHOT_TMP_DIR}/latest.json"
  local archive_file="${SNAPSHOT_TMP_DIR}/snapshot.tar.lz4"
  local listing_file="${SNAPSHOT_TMP_DIR}/snapshot.files"
  local extract_dir="${SNAPSHOT_TMP_DIR}/extract"
  local snapshot_chain_id
  local snapshot_url
  local snapshot_checksum
  local snapshot_height
  local snapshot_date
  local snapshot_type
  local expected_checksum
  local actual_checksum

  curl -fsSL --connect-timeout 15 --max-time 60 -o "${metadata_file}" "${metadata_url}" \
    || die "No snapshot metadata is available from ${metadata_url}."

  jq empty "${metadata_file}" >/dev/null 2>&1 || die "Snapshot metadata is not valid JSON."

  snapshot_chain_id="$(jq -r '.chain_id // empty' "${metadata_file}")"
  snapshot_url="$(jq -r '.url // empty' "${metadata_file}")"
  snapshot_checksum="$(jq -r '.checksum // empty' "${metadata_file}")"
  snapshot_height="$(jq -r '.height // empty' "${metadata_file}")"
  snapshot_date="$(jq -r '.date // empty' "${metadata_file}")"
  snapshot_type="$(jq -r '.type // empty' "${metadata_file}")"

  [ "${snapshot_chain_id}" = "${CHAIN_ID}" ] \
    || die "Snapshot chain_id mismatch. Expected ${CHAIN_ID}, got ${snapshot_chain_id:-<empty>}."
  case "${snapshot_url}" in
    http://*|https://*) ;;
    *) die "Snapshot metadata does not contain a valid http(s) archive URL." ;;
  esac
  printf '%s\n' "${snapshot_checksum}" | grep -Eq '^[0-9a-fA-F]{64}$' \
    || die "Snapshot metadata does not contain a valid SHA-256 checksum."
  if [ -n "${snapshot_type}" ] && [ "${snapshot_type}" != "pruned" ]; then
    die "Latest snapshot type is '${snapshot_type}', expected 'pruned'."
  fi

  log "Latest snapshot: height ${snapshot_height:-unknown} (${snapshot_date:-unknown date})"
  log "Downloading ${snapshot_url}..."
  curl -fsSL --retry 3 --connect-timeout 15 -o "${archive_file}" "${snapshot_url}" \
    || die "Could not download snapshot archive."

  expected_checksum="$(printf '%s' "${snapshot_checksum}" | tr 'A-F' 'a-f')"
  actual_checksum="$(sha256_file "${archive_file}" | tr 'A-F' 'a-f')"
  [ "${actual_checksum}" = "${expected_checksum}" ] || {
    log "  Expected: ${expected_checksum}" >&2
    log "  Actual:   ${actual_checksum}" >&2
    die "Snapshot checksum mismatch."
  }
  log "Snapshot checksum verified."

  lz4 -dc "${archive_file}" | tar -tf - > "${listing_file}" \
    || die "Snapshot archive is not readable by lz4 + tar."
  validate_snapshot_listing "${listing_file}"

  mkdir -p "${extract_dir}"
  lz4 -dc "${archive_file}" | tar -C "${extract_dir}" -xf - \
    || die "Snapshot extraction failed during staging."
  [ -d "${extract_dir}/data" ] || die "Snapshot archive did not contain data/."

  log "Snapshot staged and verified."
}

restore_staged_snapshot() {
  local staged_data="${SNAPSHOT_TMP_DIR}/extract/data"
  local validator_state_file="${SNAPSHOT_TMP_DIR}/priv_validator_state.json"
  local old_data_backup="${HOME_DIR}/data.before-validator-reset.$$"

  [ -d "${staged_data}" ] || die "Staged snapshot data is missing."
  cp -p "${HOME_DIR}/data/priv_validator_state.json" "${validator_state_file}"

  log "Replacing ${HOME_DIR}/data with staged snapshot data..."
  mv "${HOME_DIR}/data" "${old_data_backup}"
  if ! mv "${staged_data}" "${HOME_DIR}/data"; then
    mv "${old_data_backup}" "${HOME_DIR}/data" 2>/dev/null || true
    die "Could not move staged snapshot data into ${HOME_DIR}/data; restored the previous data directory."
  fi
  cp -p "${validator_state_file}" "${HOME_DIR}/data/priv_validator_state.json"
  rm -rf "${HOME_DIR}/data/cs.wal"
  rm -rf "${old_data_backup}"
  log "Snapshot restored. Preserved local validator state and removed restored consensus WAL."
}

wait_for_sync() {
  local timeout="${POST_RESTART_SYNC_TIMEOUT}"
  local waited=0
  local status
  local catching_up
  local height

  log "Waiting for svoted status after restart..."
  while true; do
    status="$("${SVOTED_BIN}" status --home "${HOME_DIR}" 2>/dev/null || echo "")"
    if [ -n "${status}" ]; then
      catching_up="$(printf '%s\n' "${status}" | jq -r '.sync_info.catching_up' 2>/dev/null || echo "true")"
      height="$(printf '%s\n' "${status}" | jq -r '.sync_info.latest_block_height // "0"' 2>/dev/null || echo "0")"
      if [ -z "${catching_up}" ] || [ "${catching_up}" = "null" ]; then
        catching_up="true"
      fi
      if [ -z "${height}" ] || [ "${height}" = "null" ]; then
        height="0"
      fi
      log "  height: ${height}, catching_up: ${catching_up}"
      if [ "${catching_up}" = "false" ] && [ "${height}" != "0" ] && [ "${height}" != "null" ]; then
        log "Validator node is synced."
        return 0
      fi
    else
      log "  waiting for svoted status..."
    fi

    if [ "${waited}" -ge "${timeout}" ]; then
      die "Timed out waiting for svoted to report catching_up=false after restart."
    fi
    sleep 5
    waited=$((waited + 5))
  done
}

main() {
  validate_timeout
  prepare_path
  require_tool curl
  require_tool jq
  require_tool lz4
  require_tool tar
  require_tool "${SVOTED_BIN}"
  preflight_local_home
  resolve_chain_id
  print_config
  detect_service_manager

  mkdir -p "${TMP_PARENT}"
  SNAPSHOT_TMP_DIR="$(mktemp -d "${TMP_PARENT%/}/svote-validator-reset.XXXXXXXXXX")"

  stage_latest_snapshot
  stop_service
  restore_staged_snapshot
  start_service
  wait_for_sync

  log ""
  log "=== Validator snapshot reset complete ==="
}

main "$@"
