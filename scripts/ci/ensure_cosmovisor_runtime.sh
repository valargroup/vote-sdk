#!/usr/bin/env bash
# ensure_cosmovisor_runtime.sh
# Post-deploy runtime guard for Cosmovisor-managed svoted services.

set -euo pipefail

svote_ci_log() {
  printf '==> %s\n' "$*"
}

svote_ci_die() {
  printf 'ERROR: %s\n' "$*" >&2
  exit 1
}

svote_ci_stage_binary_atomically() {
  local source_bin="$1"
  local target_bin="$2"
  local target_dir
  target_dir="$(dirname "$target_bin")"
  install -d -m 0755 "$target_dir"
  install -m 0755 "$source_bin" "${target_bin}.new"
  mv -f "${target_bin}.new" "$target_bin"
}

svote_ci_hash_file() {
  local path="$1"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$path" | awk '{print $1}'
  else
    shasum -a 256 "$path" | awk '{print $1}'
  fi
}

svote_ci_require_matching_hashes() {
  local expected_path="$1"
  local actual_path="$2"
  local expected_hash
  local actual_hash

  expected_hash="$(svote_ci_hash_file "$expected_path")"
  actual_hash="$(svote_ci_hash_file "$actual_path")"
  svote_ci_log "deployed hash: ${expected_hash}"
  svote_ci_log "runtime hash:  ${actual_hash}"

  [ "$expected_hash" = "$actual_hash" ]
}

svote_ci_parse_upgrade_plan_from_runtime_path() {
  local runtime_path="$1"
  local daemon_home="$2"
  local prefix="${daemon_home%/}/cosmovisor/upgrades/"
  local suffix="/bin/svoted"
  local remainder

  case "$runtime_path" in
    "${prefix}"*"${suffix}")
      remainder="${runtime_path#"$prefix"}"
      printf '%s\n' "${remainder%"$suffix"}" | cut -d/ -f1
      return 0
      ;;
    *)
      return 1
      ;;
  esac
}

svote_ci_is_cosmovisor_execstart() {
  local service_file="$1"
  if grep -Eq '^ExecStart=.*cosmovisor.*run start' "$service_file"; then
    return 0
  fi
  return 1
}

main() {
  local service_name="${SERVICE_NAME:-svoted}"
  local daemon_home="${DAEMON_HOME:-/opt/shielded-vote/.svoted}"
  local source_bin="${SOURCE_BIN:-/opt/shielded-vote/current/bin/svoted}"
  local sync_runtime="${SYNC_COSMOVISOR_RUNTIME:-false}"
  local ensure_cosmovisor="${ENSURE_COSMOVISOR_MODE:-true}"
  local service_tmp
  local runtime_link
  local runtime_target
  local runtime_pid
  local runtime_exe
  local applied_plan
  local plan_bin

  [ -x "$source_bin" ] || svote_ci_die "source binary missing or not executable: ${source_bin}"
  command -v systemctl >/dev/null 2>&1 || svote_ci_die "systemctl is required"

  service_tmp="$(mktemp)"
  trap 'rm -f "$service_tmp"' EXIT
  systemctl cat "$service_name" --no-pager > "$service_tmp"

  if ! svote_ci_is_cosmovisor_execstart "$service_tmp"; then
    if [ "$ensure_cosmovisor" = "true" ]; then
      svote_ci_die "service ${service_name} is not configured for cosmovisor ExecStart"
    fi
    svote_ci_log "service ${service_name} is not in cosmovisor mode; skipping runtime sync"
    return 0
  fi

  runtime_link="${daemon_home%/}/cosmovisor/current/bin/svoted"
  runtime_target="$(readlink -f "$runtime_link" 2>/dev/null || true)"
  [ -n "$runtime_target" ] || svote_ci_die "unable to resolve runtime target from ${runtime_link}"
  [ -e "$runtime_target" ] || svote_ci_die "runtime target path does not exist: ${runtime_target}"

  svote_ci_log "service ${service_name} is in cosmovisor mode"
  svote_ci_log "runtime target: ${runtime_target}"

  if [ "$sync_runtime" = "true" ]; then
    svote_ci_log "syncing deployed binary into cosmovisor runtime path"
    svote_ci_stage_binary_atomically "$source_bin" "$runtime_target"

    if applied_plan="$(svote_ci_parse_upgrade_plan_from_runtime_path "$runtime_target" "$daemon_home" 2>/dev/null || true)"; then
      if [ -n "$applied_plan" ]; then
        plan_bin="${daemon_home%/}/cosmovisor/upgrades/${applied_plan}/bin/svoted"
        svote_ci_log "mirroring deployed binary into applied plan path: ${plan_bin}"
        svote_ci_stage_binary_atomically "$source_bin" "$plan_bin"
      fi
    fi
  else
    svote_ci_log "SYNC_COSMOVISOR_RUNTIME=false; leaving runtime binary unchanged"
  fi

  svote_ci_log "restarting ${service_name}"
  systemctl restart "$service_name"
  systemctl is-active --quiet "$service_name" || svote_ci_die "service ${service_name} is not active after restart"

  runtime_pid="$(pgrep -f "${daemon_home%/}.*svoted start --home ${daemon_home}" | head -n 1 || true)"
  [ -n "$runtime_pid" ] || svote_ci_die "unable to find running svoted pid for ${daemon_home}"
  runtime_exe="$(readlink -f "/proc/${runtime_pid}/exe" 2>/dev/null || true)"
  [ -n "$runtime_exe" ] || svote_ci_die "unable to resolve /proc/${runtime_pid}/exe"
  case "$runtime_exe" in
    "${daemon_home%/}/cosmovisor/"*) ;;
    *) svote_ci_die "runtime executable is not under cosmovisor tree: ${runtime_exe}" ;;
  esac

  if [ "$sync_runtime" = "true" ]; then
    if ! svote_ci_require_matching_hashes "$source_bin" "$runtime_exe"; then
      svote_ci_die "runtime hash mismatch after sync (deployed != runtime)"
    fi
  fi

  svote_ci_log "cosmovisor runtime verification complete"
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
  main "$@"
fi
