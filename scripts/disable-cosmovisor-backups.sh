#!/usr/bin/env bash
# Permanently disable Cosmovisor's pre-upgrade data copy and remove existing
# data-backup-Y-M-D directories from a managed Shielded Vote validator.
#
# The published copy is rendered with an immutable shared-helper URL and hash.
set -euo pipefail

readonly BACKUP_COMMON_URL='__COMMON_URL__'
readonly BACKUP_COMMON_SHA256='__COMMON_SHA256__'
readonly BACKUP_DROPIN_NAME='zz-cosmovisor-skip-backup.conf'

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")" 2>/dev/null && pwd || true)"
COMMON_LIB=""
COMMON_TMP=""
COMMON_RENDERED=1

bootstrap_sha256_file() {
  local file="$1"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$file" | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$file" | awk '{print $1}'
  else
    echo "ERROR: sha256sum or shasum is required." >&2
    return 1
  fi
}

if [ "$BACKUP_COMMON_URL" = '__COMMON_''URL__' ] || \
   [ "$BACKUP_COMMON_SHA256" = '__COMMON_''SHA256__' ]; then
  COMMON_RENDERED=0
fi

select_local_common() {
  local candidate="$1"
  local actual_sha256

  [ -f "$candidate" ] || return 1
  if [ "$COMMON_RENDERED" = "0" ]; then
    COMMON_LIB="$candidate"
    return 0
  fi
  actual_sha256="$(bootstrap_sha256_file "$candidate")"
  if [ "$actual_sha256" = "$BACKUP_COMMON_SHA256" ]; then
    COMMON_LIB="$candidate"
    return 0
  fi
  echo "WARNING: Ignoring local shared helper with an unexpected checksum: ${candidate}." >&2
  return 1
}

if [ -n "$SCRIPT_DIR" ]; then
  select_local_common "${SCRIPT_DIR}/_chain_upgrade_common.sh" || true
fi
if [ -z "$COMMON_LIB" ]; then
  select_local_common "/opt/shielded-vote/scripts/_chain_upgrade_common.sh" || true
fi

if [ -z "$COMMON_LIB" ]; then
  if [ "$COMMON_RENDERED" = "0" ]; then
    echo "ERROR: This source copy has not been rendered for publication." >&2
    echo "Run it from a vote-sdk checkout or use the versioned operator URL." >&2
    exit 1
  fi
  printf '%s\n' "$BACKUP_COMMON_SHA256" | grep -Eq '^[0-9a-f]{64}$' \
    || { echo "ERROR: Embedded helper checksum is invalid." >&2; exit 1; }
  command -v curl >/dev/null 2>&1 \
    || { echo "ERROR: curl is required." >&2; exit 1; }
  COMMON_TMP="$(mktemp)"
  curl -fsSL --retry 3 --connect-timeout 15 "$BACKUP_COMMON_URL" -o "$COMMON_TMP" \
    || { echo "ERROR: Could not download ${BACKUP_COMMON_URL}." >&2; exit 1; }
  actual_common_sha256="$(bootstrap_sha256_file "$COMMON_TMP")"
  if [ "$actual_common_sha256" != "$BACKUP_COMMON_SHA256" ]; then
    echo "ERROR: Shared helper checksum mismatch." >&2
    echo "  Expected: ${BACKUP_COMMON_SHA256}" >&2
    echo "  Actual:   ${actual_common_sha256}" >&2
    exit 1
  fi
  COMMON_LIB="$COMMON_TMP"
fi

# shellcheck disable=SC1090,SC1091
source "$COMMON_LIB"

APPLY=0
EXPECTED_CHAIN_ID="${SVOTE_CHAIN_ID:-}"
SERVICE_INPUT="${SVOTE_SERVICE_NAME:-svoted}"
TIMEOUT_SECS="${SVOTE_BACKUP_RESTART_TIMEOUT:-120}"
DROPIN_PATH=""
DROPIN_ROLLBACK_DIR=""
DROPIN_PREVIOUSLY_EXISTED=0
DROPIN_MUTATED=0
SERVICE_RESTART_ATTEMPTED=0
BACKUP_TOTAL_KIB=0
BACKUP_REMOVED_KIB=0
BACKUP_REMOVED_COUNT=0
declare -a BACKUP_ROOTS=()
declare -a BACKUP_DIRS=()

usage() {
  cat <<'EOF'
usage: disable-cosmovisor-backups.sh [options]

Permanently set UNSAFE_SKIP_BACKUP=true for a managed Shielded Vote validator,
restart and verify the service when needed, then remove existing Cosmovisor
data-backup-Y-M-D directories.

Options:
  --apply                   Apply the systemd change and delete backups.
                            Without this flag, run a read-only preflight.
  --expected-chain-id ID    Required chain ID safety check.
  --service-name NAME       systemd service basename (default: svoted).
  --timeout-secs N          RPC readiness timeout after restart (default: 120).
  --help                    Show this help text.

The script supports the standard join.sh layout and explicit custom
DAEMON_HOME or DAEMON_DATA_BACKUP_DIR values from the live service. It refuses
non-systemd, non-Cosmovisor, inactive, ambiguous, or unsafe layouts.
EOF
}

format_kib() {
  local kib="$1"
  awk -v kib="$kib" 'BEGIN {
    if (kib >= 1048576) printf "%.1f GiB", kib / 1048576;
    else if (kib >= 1024) printf "%.1f MiB", kib / 1024;
    else printf "%d KiB", kib;
  }'
}

normalize_service_name() {
  SERVICE_NAME="${SERVICE_INPUT%.service}"
  case "$SERVICE_NAME" in
    ''|.*|*[!A-Za-z0-9_.-]*)
      svote_upgrade_die "Unsafe --service-name value: ${SERVICE_INPUT}."
      ;;
  esac
  SVOTE_SERVICE_NAME="$SERVICE_NAME"
  export SVOTE_SERVICE_NAME
}

parse_args() {
  while [ "$#" -gt 0 ]; do
    case "$1" in
      --apply)
        APPLY=1
        shift
        ;;
      --expected-chain-id)
        [ "$#" -ge 2 ] || svote_upgrade_die "--expected-chain-id requires a value."
        EXPECTED_CHAIN_ID="$2"
        shift 2
        ;;
      --expected-chain-id=*)
        EXPECTED_CHAIN_ID="${1#--expected-chain-id=}"
        shift
        ;;
      --service-name)
        [ "$#" -ge 2 ] || svote_upgrade_die "--service-name requires a value."
        SERVICE_INPUT="$2"
        shift 2
        ;;
      --service-name=*)
        SERVICE_INPUT="${1#--service-name=}"
        shift
        ;;
      --timeout-secs)
        [ "$#" -ge 2 ] || svote_upgrade_die "--timeout-secs requires a value."
        TIMEOUT_SECS="$2"
        shift 2
        ;;
      --timeout-secs=*)
        TIMEOUT_SECS="${1#--timeout-secs=}"
        shift
        ;;
      --help|-h)
        usage
        exit 0
        ;;
      *)
        svote_upgrade_die "Unknown option: $1"
        ;;
    esac
  done

  case "$EXPECTED_CHAIN_ID" in
    ''|*[!A-Za-z0-9._-]*)
      svote_upgrade_die "--expected-chain-id is required and must contain only letters, numbers, '.', '_', or '-'."
      ;;
  esac
  case "$TIMEOUT_SECS" in
    ''|*[!0-9]*) svote_upgrade_die "--timeout-secs must be a non-negative integer." ;;
  esac
  normalize_service_name
}

canonical_directory() {
  local path="$1"
  local label="$2"
  local canonical normalized

  case "$path" in
    /*) ;;
    *) svote_upgrade_die "${label} must be an absolute path: ${path:-<empty>}." ;;
  esac
  [ -d "$path" ] || svote_upgrade_die "${label} is not a directory: ${path}."
  [ ! -L "$path" ] || svote_upgrade_die "${label} must not be a symlink: ${path}."
  canonical="$(readlink -f -- "$path" 2>/dev/null || true)"
  [ -n "$canonical" ] || svote_upgrade_die "Could not resolve ${label}: ${path}."
  normalized="${path%/}"
  [ -n "$normalized" ] || normalized="/"
  [ "$canonical" = "$normalized" ] \
    || svote_upgrade_die "${label} contains a symlink or non-canonical component: ${path} -> ${canonical}."
  printf '%s\n' "$canonical"
}

assert_service_layout() {
  local unit="${SERVICE_NAME}.service"
  local main_pid effective_mode effective_daemon_name effective_home effective_svote_home
  local runtime_mode runtime_daemon_name runtime_home

  systemctl cat "$unit" >/dev/null 2>&1 \
    || svote_upgrade_die "systemd service ${unit} is not installed."
  systemctl is-active --quiet "$unit" 2>/dev/null \
    || svote_upgrade_die "${unit} must be active before changing its backup policy."

  effective_mode="$(svote_upgrade_systemd_effective_env_value "SVOTE_UPGRADE_MODE" || true)"
  effective_daemon_name="$(svote_upgrade_systemd_effective_env_value "DAEMON_NAME" || true)"
  effective_home="$(svote_upgrade_systemd_effective_env_value "DAEMON_HOME" || true)"
  effective_svote_home="$(svote_upgrade_systemd_effective_env_value "SVOTE_HOME" || true)"
  [ "$effective_mode" = "cosmovisor" ] \
    || svote_upgrade_die "${unit} has SVOTE_UPGRADE_MODE=${effective_mode:-<unset>}; expected cosmovisor."
  [ "$effective_daemon_name" = "$SVOTE_DAEMON_NAME" ] \
    || svote_upgrade_die "${unit} has DAEMON_NAME=${effective_daemon_name:-<unset>}; expected ${SVOTE_DAEMON_NAME}."
  [ -n "$effective_home" ] || svote_upgrade_die "${unit} has no effective DAEMON_HOME."
  if [ -n "$effective_svote_home" ] && [ "$effective_svote_home" != "$effective_home" ]; then
    svote_upgrade_die "${unit} has inconsistent DAEMON_HOME (${effective_home}) and SVOTE_HOME (${effective_svote_home})."
  fi

  main_pid="$(systemctl show "$unit" -p MainPID --value 2>/dev/null || true)"
  case "$main_pid" in
    ''|0|*[!0-9]*) svote_upgrade_die "${unit} has no live MainPID." ;;
  esac
  runtime_mode="$(svote_upgrade_process_env_value "$main_pid" "SVOTE_UPGRADE_MODE" || true)"
  runtime_daemon_name="$(svote_upgrade_process_env_value "$main_pid" "DAEMON_NAME" || true)"
  runtime_home="$(svote_upgrade_process_env_value "$main_pid" "DAEMON_HOME" || true)"
  [ "$runtime_mode" = "cosmovisor" ] \
    || svote_upgrade_die "${unit} runtime has SVOTE_UPGRADE_MODE=${runtime_mode:-<unset>}; expected cosmovisor."
  [ "$runtime_daemon_name" = "$SVOTE_DAEMON_NAME" ] \
    || svote_upgrade_die "${unit} runtime has DAEMON_NAME=${runtime_daemon_name:-<unset>}; expected ${SVOTE_DAEMON_NAME}."
  [ -n "$runtime_home" ] || svote_upgrade_die "${unit} runtime has no DAEMON_HOME."

  effective_home="$(canonical_directory "$effective_home" "systemd DAEMON_HOME")"
  runtime_home="$(canonical_directory "$runtime_home" "runtime DAEMON_HOME")"
  [ "$effective_home" = "$runtime_home" ] \
    || svote_upgrade_die "systemd and runtime DAEMON_HOME differ (${effective_home} != ${runtime_home})."

  DAEMON_HOME="$runtime_home"
  SVOTE_HOME="$runtime_home"
  COSMVISOR_ROOT="${DAEMON_HOME}/cosmovisor"
  GENESIS_BIN_DIR="${COSMVISOR_ROOT}/genesis/bin"
  GENESIS_BIN="${GENESIS_BIN_DIR}/${SVOTE_DAEMON_NAME}"
  export DAEMON_HOME SVOTE_HOME COSMVISOR_ROOT GENESIS_BIN_DIR GENESIS_BIN

  svote_upgrade_assert_single_managed_signer
}

assert_validator_home() {
  local genesis_file="${DAEMON_HOME}/config/genesis.json"
  local current_link="${COSMVISOR_ROOT}/current"
  local canonical_root current_target chain_id

  [ "$DAEMON_HOME" != "/" ] || svote_upgrade_die "Refusing DAEMON_HOME=/."
  [ -f "$genesis_file" ] || svote_upgrade_die "Missing ${genesis_file}."
  [ -d "${DAEMON_HOME}/data" ] || svote_upgrade_die "Missing ${DAEMON_HOME}/data."
  [ -d "$COSMVISOR_ROOT" ] || svote_upgrade_die "Missing ${COSMVISOR_ROOT}."
  svote_upgrade_verify_validator_identity_files

  [ -L "$current_link" ] || svote_upgrade_die "Cosmovisor current path is not a symlink: ${current_link}."
  canonical_root="$(readlink -f -- "$COSMVISOR_ROOT" 2>/dev/null || true)"
  current_target="$(readlink -f -- "$current_link" 2>/dev/null || true)"
  [ -n "$canonical_root" ] && [ -n "$current_target" ] \
    || svote_upgrade_die "Could not resolve ${current_link}."
  case "$current_target" in
    "${canonical_root}/genesis") ;;
    "${canonical_root}/upgrades/"*)
      [ "$current_target" != "${canonical_root}/upgrades" ] \
        || svote_upgrade_die "Cosmovisor current points at the upgrades container, not an upgrade: ${current_target}."
      ;;
    *) svote_upgrade_die "Cosmovisor current points outside the expected tree: ${current_target}." ;;
  esac

  chain_id="$(jq -er '.chain_id | select(type == "string" and length > 0)' "$genesis_file" 2>/dev/null || true)"
  [ -n "$chain_id" ] || svote_upgrade_die "Could not read chain_id from ${genesis_file}."
  [ "$chain_id" = "$EXPECTED_CHAIN_ID" ] \
    || svote_upgrade_die "Chain ID mismatch: expected ${EXPECTED_CHAIN_ID}, local genesis has ${chain_id}."
}

add_backup_root() {
  local candidate="$1"
  local canonical data_root cosmovisor_root existing

  canonical="$(canonical_directory "$candidate" "Cosmovisor backup root")"
  data_root="$(readlink -f -- "${DAEMON_HOME}/data" 2>/dev/null || true)"
  cosmovisor_root="$(readlink -f -- "$COSMVISOR_ROOT" 2>/dev/null || true)"
  case "$canonical" in
    /|/bin|/boot|/dev|/etc|/lib|/lib64|/proc|/run|/sbin|/sys|/usr|/var)
      svote_upgrade_die "Refusing broad Cosmovisor backup root: ${canonical}."
      ;;
    "$data_root"|"$data_root/"*)
      svote_upgrade_die "Cosmovisor backup root overlaps active chain data: ${canonical}."
      ;;
    "$cosmovisor_root"|"$cosmovisor_root/"*)
      svote_upgrade_die "Cosmovisor backup root overlaps the Cosmovisor binary tree: ${canonical}."
      ;;
  esac

  for existing in "${BACKUP_ROOTS[@]:-}"; do
    [ "$existing" = "$canonical" ] && return 0
  done
  BACKUP_ROOTS+=("$canonical")
}

resolve_backup_roots() {
  local main_pid custom_root
  BACKUP_ROOTS=()

  # Always include DAEMON_HOME so old default-location backups are removed even
  # when the service now has a custom DAEMON_DATA_BACKUP_DIR.
  add_backup_root "$DAEMON_HOME"

  main_pid="$(systemctl show "${SERVICE_NAME}.service" -p MainPID --value 2>/dev/null || true)"
  custom_root="$(svote_upgrade_process_env_value "$main_pid" "DAEMON_DATA_BACKUP_DIR" || true)"
  if [ -n "$custom_root" ]; then
    add_backup_root "$custom_root"
  fi
}

validate_backup_candidate() {
  local path="$1"
  local expected_root="$2"
  local basename canonical parent

  [ -e "$path" ] || svote_upgrade_die "Backup candidate disappeared before cleanup: ${path}."
  [ ! -L "$path" ] || svote_upgrade_die "Refusing symlink backup candidate: ${path}."
  [ -d "$path" ] || svote_upgrade_die "Refusing non-directory backup candidate: ${path}."
  basename="${path##*/}"
  [[ "$basename" =~ ^data-backup-[0-9]{4}-[0-9]{1,2}-[0-9]{1,2}$ ]] \
    || svote_upgrade_die "Refusing malformed Cosmovisor backup name: ${basename}."
  canonical="$(readlink -f -- "$path" 2>/dev/null || true)"
  parent="$(dirname "$canonical")"
  [ -n "$canonical" ] && [ "$canonical" = "$path" ] \
    || svote_upgrade_die "Backup candidate is not canonical: ${path} -> ${canonical:-<unresolved>}."
  [ "$parent" = "$expected_root" ] \
    || svote_upgrade_die "Backup candidate escaped its expected root: ${path}."
}

collect_backup_dirs() {
  local root path size_kib existing
  BACKUP_DIRS=()
  BACKUP_TOTAL_KIB=0

  for root in "${BACKUP_ROOTS[@]}"; do
    while IFS= read -r -d '' path; do
      validate_backup_candidate "$path" "$root"
      for existing in "${BACKUP_DIRS[@]:-}"; do
        [ "$existing" = "$path" ] && continue 2
      done
      BACKUP_DIRS+=("$path")
      size_kib="$(du -sk -- "$path" 2>/dev/null | awk '{print $1}' || true)"
      case "$size_kib" in
        ''|*[!0-9]*) svote_upgrade_die "Could not measure backup directory: ${path}." ;;
      esac
      BACKUP_TOTAL_KIB=$((BACKUP_TOTAL_KIB + size_kib))
    done < <(find -P "$root" -mindepth 1 -maxdepth 1 -name 'data-backup-*' -print0)
  done
}

print_preflight() {
  local root path runtime_skip
  runtime_skip="$(runtime_skip_backup_value || true)"

  echo "=== Cosmovisor backup maintenance preflight ==="
  echo "  Service:                  ${SERVICE_NAME}.service"
  echo "  Chain ID:                 ${EXPECTED_CHAIN_ID}"
  echo "  Validator home:           ${DAEMON_HOME}"
  echo "  Runtime skip-backup:      ${runtime_skip:-<unset>}"
  echo "  Backup roots:"
  for root in "${BACKUP_ROOTS[@]}"; do
    echo "    - ${root}"
  done
  echo "  Backup directories:       ${#BACKUP_DIRS[@]}"
  for path in "${BACKUP_DIRS[@]}"; do
    echo "    - ${path}"
  done
  echo "  Estimated space:          $(format_kib "$BACKUP_TOTAL_KIB")"
  echo
}

runtime_skip_backup_value() {
  local main_pid
  main_pid="$(systemctl show "${SERVICE_NAME}.service" -p MainPID --value 2>/dev/null || true)"
  case "$main_pid" in
    ''|0|*[!0-9]*) return 1 ;;
  esac
  svote_upgrade_process_env_value "$main_pid" "UNSAFE_SKIP_BACKUP"
}

assert_skip_backup_active() {
  local effective runtime
  effective="$(svote_upgrade_systemd_effective_env_value "UNSAFE_SKIP_BACKUP" || true)"
  runtime="$(runtime_skip_backup_value || true)"
  [ "$effective" = "true" ] \
    || svote_upgrade_die "systemd effective UNSAFE_SKIP_BACKUP is ${effective:-<unset>} (expected true)."
  [ "$runtime" = "true" ] \
    || svote_upgrade_die "${SERVICE_NAME}.service runtime UNSAFE_SKIP_BACKUP is ${runtime:-<unset>} (expected true)."
}

write_skip_backup_dropin() {
  local dropin_dir expected_file
  dropin_dir="/etc/systemd/system/${SERVICE_NAME}.service.d"
  DROPIN_PATH="${dropin_dir}/${BACKUP_DROPIN_NAME}"
  DROPIN_ROLLBACK_DIR="$(mktemp -d)"
  DROPIN_PREVIOUSLY_EXISTED=0

  [ ! -L "$DROPIN_PATH" ] || svote_upgrade_die "Refusing symlink systemd drop-in: ${DROPIN_PATH}."
  if [ -e "$DROPIN_PATH" ]; then
    [ -f "$DROPIN_PATH" ] || svote_upgrade_die "Systemd drop-in is not a regular file: ${DROPIN_PATH}."
    cp -p "$DROPIN_PATH" "${DROPIN_ROLLBACK_DIR}/previous.conf"
    DROPIN_PREVIOUSLY_EXISTED=1
  fi

  expected_file="${DROPIN_ROLLBACK_DIR}/expected.conf"
  {
    printf '[Service]\n'
    printf 'Environment="UNSAFE_SKIP_BACKUP=true"\n'
  } > "$expected_file"
  chmod 0644 "$expected_file"

  if [ -f "$DROPIN_PATH" ] && cmp -s "$expected_file" "$DROPIN_PATH"; then
    svote_upgrade_log "Persistent skip-backup drop-in is already current: ${DROPIN_PATH}"
    return 0
  fi

  install -d -m 0755 "$dropin_dir"
  install -m 0644 "$expected_file" "${DROPIN_PATH}.new.$$"
  mv -f "${DROPIN_PATH}.new.$$" "$DROPIN_PATH"
  DROPIN_MUTATED=1
  svote_upgrade_log "Set UNSAFE_SKIP_BACKUP=true in ${DROPIN_PATH}"
}

restore_previous_dropin() {
  if [ "$DROPIN_MUTATED" = "1" ]; then
    svote_upgrade_warn "Restoring the previous Cosmovisor backup policy after a failed activation."
    if [ "$DROPIN_PREVIOUSLY_EXISTED" = "1" ]; then
      install -m 0644 "${DROPIN_ROLLBACK_DIR}/previous.conf" "$DROPIN_PATH"
    else
      rm -f -- "$DROPIN_PATH"
    fi
    systemctl daemon-reload || true
  fi
  if [ "$SERVICE_RESTART_ATTEMPTED" = "1" ]; then
    if ! systemctl restart "${SERVICE_NAME}.service"; then
      svote_upgrade_warn "CRITICAL: could not restart ${SERVICE_NAME}.service after restoring its previous configuration."
      return 1
    fi
    if ! systemctl is-active --quiet "${SERVICE_NAME}.service" 2>/dev/null; then
      svote_upgrade_warn "CRITICAL: ${SERVICE_NAME}.service is not active after rollback."
      return 1
    fi
  fi
  DROPIN_MUTATED=0
  SERVICE_RESTART_ATTEMPTED=0
}

activate_skip_backup() {
  local runtime_before
  runtime_before="$(runtime_skip_backup_value || true)"

  write_skip_backup_dropin
  systemctl daemon-reload
  [ "$(svote_upgrade_systemd_effective_env_value "UNSAFE_SKIP_BACKUP" || true)" = "true" ] \
    || svote_upgrade_die "A later systemd override prevents UNSAFE_SKIP_BACKUP=true from taking effect."

  if [ "$runtime_before" != "true" ]; then
    svote_upgrade_log "Restarting ${SERVICE_NAME}.service to activate the backup policy."
    SERVICE_RESTART_ATTEMPTED=1
    systemctl restart "${SERVICE_NAME}.service"
    svote_upgrade_wait_for_rpc "$TIMEOUT_SECS" 1
  else
    svote_upgrade_log "The running service already has UNSAFE_SKIP_BACKUP=true; restart not needed."
  fi

  assert_skip_backup_active
  svote_upgrade_assert_single_managed_signer
  DROPIN_MUTATED=0
  SERVICE_RESTART_ATTEMPTED=0
  svote_upgrade_log "UNSAFE_SKIP_BACKUP=true is persistent and active."
}

delete_backup_dirs() {
  local path root remaining

  BACKUP_REMOVED_COUNT="${#BACKUP_DIRS[@]}"
  BACKUP_REMOVED_KIB="$BACKUP_TOTAL_KIB"

  for path in "${BACKUP_DIRS[@]}"; do
    root="$(dirname "$path")"
    validate_backup_candidate "$path" "$root"
    if command -v mountpoint >/dev/null 2>&1 && mountpoint -q -- "$path"; then
      svote_upgrade_die "Refusing mounted backup directory: ${path}."
    fi
    svote_upgrade_log "Removing ${path}"
    rm -rf --one-file-system -- "$path"
    if [ -e "$path" ] || [ -L "$path" ]; then
      svote_upgrade_die "Backup directory remains after cleanup: ${path}."
    fi
  done

  collect_backup_dirs
  remaining="${#BACKUP_DIRS[@]}"
  [ "$remaining" -eq 0 ] \
    || svote_upgrade_die "${remaining} Cosmovisor backup directories remain after cleanup."
}

cleanup_on_exit() {
  local status=$?
  trap - EXIT
  if [ "$status" -ne 0 ] && { [ "$DROPIN_MUTATED" = "1" ] || [ "$SERVICE_RESTART_ATTEMPTED" = "1" ]; }; then
    restore_previous_dropin || true
  fi
  if [ -n "$DROPIN_ROLLBACK_DIR" ] && [ -d "$DROPIN_ROLLBACK_DIR" ]; then
    rm -rf -- "$DROPIN_ROLLBACK_DIR"
  fi
  if [ -n "$COMMON_TMP" ] && [ -f "$COMMON_TMP" ]; then
    rm -f -- "$COMMON_TMP"
  fi
  exit "$status"
}

main() {
  trap cleanup_on_exit EXIT
  parse_args "$@"
  svote_upgrade_require_linux_systemd_root
  svote_upgrade_require_tools jq find readlink du awk grep sed cmp install rm
  svote_upgrade_resolve_paths
  assert_service_layout
  assert_validator_home
  resolve_backup_roots
  collect_backup_dirs
  print_preflight

  if [ "$APPLY" != "1" ]; then
    echo "Dry run only. Re-run with --apply to persist the setting, restart when needed, and delete the listed backups."
    return 0
  fi

  activate_skip_backup
  delete_backup_dirs
  systemctl is-active --quiet "${SERVICE_NAME}.service" 2>/dev/null \
    || svote_upgrade_die "${SERVICE_NAME}.service is not active after cleanup."
  svote_upgrade_assert_single_managed_signer

  echo
  echo "=== Cosmovisor backup maintenance complete ==="
  echo "  Service:            ${SERVICE_NAME}.service (active)"
  echo "  Chain ID:           ${EXPECTED_CHAIN_ID}"
  echo "  Validator home:     ${DAEMON_HOME}"
  echo "  Backup policy:      UNSAFE_SKIP_BACKUP=true"
  echo "  Removed:            ${BACKUP_REMOVED_COUNT} directories ($(format_kib "$BACKUP_REMOVED_KIB"))"
  echo "  Backup directories: 0"
}

if [ "${BASH_SOURCE[0]:-$0}" = "$0" ]; then
  main "$@"
fi
