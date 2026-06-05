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

svote_ci_find_running_svoted_pid() {
  local daemon_home="$1"
  local line
  local pid
  local cmd
  while IFS= read -r line; do
    [ -n "$line" ] || continue
    pid="${line%% *}"
    cmd="${line#"$pid "}"
    case "$cmd" in
      *"svoted start"* )
        if printf '%s\n' "$cmd" | grep -Fq -- " --home ${daemon_home}"; then
          printf '%s\n' "$pid"
          return 0
        fi
        if printf '%s\n' "$cmd" | grep -Fq -- "--home=${daemon_home}"; then
          printf '%s\n' "$pid"
          return 0
        fi
        ;;
    esac
  done < <(pgrep -af svoted 2>/dev/null || true)
  return 1
}

svote_ci_hash_file() {
  local path="$1"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$path" | awk '{print $1}'
  else
    shasum -a 256 "$path" | awk '{print $1}'
  fi
}

svote_ci_download_and_install_cosmovisor() {
  local target_bin="$1"
  local version="${COSMOVISOR_VERSION:-v1.6.0}"
  local os="linux"
  local arch
  local platform
  local asset
  local release_base
  local tmp_dir
  local archive
  local sums_file
  local expected
  local actual
  local found

  case "$(uname -m)" in
    x86_64) arch="amd64" ;;
    aarch64|arm64) arch="arm64" ;;
    *) svote_ci_die "unsupported architecture for cosmovisor install: $(uname -m)" ;;
  esac
  platform="${os}-${arch}"
  asset="cosmovisor-${version}-${platform}.tar.gz"
  release_base="https://github.com/cosmos/cosmos-sdk/releases/download/cosmovisor%2F${version}"

  tmp_dir="$(mktemp -d)"
  archive="${tmp_dir}/cosmovisor.tar.gz"
  sums_file="${tmp_dir}/SHA256SUMS.txt"
  trap 'rm -rf "$tmp_dir"' RETURN

  curl -fsSL "${release_base}/${asset}" -o "$archive"
  curl -fsSL "${release_base}/SHA256SUMS-cosmovisor-${version}.txt" -o "$sums_file"

  expected="$(awk -v name="$asset" '$2 == name {print $1}' "$sums_file" | tr -d '\r' | head -n 1)"
  [ -n "$expected" ] || svote_ci_die "unable to resolve expected checksum for ${asset}"
  actual="$(svote_ci_hash_file "$archive")"
  [ "$expected" = "$actual" ] || svote_ci_die "cosmovisor checksum mismatch (expected=${expected}, actual=${actual})"

  tar xzf "$archive" -C "$tmp_dir"
  found="$(find "$tmp_dir" -type f -name cosmovisor | head -n 1 || true)"
  [ -n "$found" ] || svote_ci_die "cosmovisor archive did not contain binary"
  install -d -m 0755 "$(dirname "$target_bin")"
  install -m 0755 "$found" "$target_bin"
}

svote_ci_resolve_cosmovisor_binary() {
  local explicit="${COSMOVISOR_BIN:-}"
  local daemon_home="$1"
  local source_bin="$2"
  local install_dir
  local candidate

  if [ -n "$explicit" ] && [ -x "$explicit" ]; then
    printf '%s\n' "$explicit"
    return 0
  fi

  for candidate in "/root/.local/bin/cosmovisor" "/opt/shielded-vote/cosmovisor"; do
    if [ -x "$candidate" ]; then
      printf '%s\n' "$candidate"
      return 0
    fi
  done

  install_dir="$(dirname "$source_bin")"
  candidate="${install_dir}/cosmovisor"
  if [ -x "$candidate" ]; then
    printf '%s\n' "$candidate"
    return 0
  fi

  # Last resort for hosts that were never migrated: install a pinned cosmovisor binary.
  candidate="/opt/shielded-vote/cosmovisor"
  svote_ci_log "cosmovisor not found; installing pinned binary at ${candidate}"
  svote_ci_download_and_install_cosmovisor "$candidate"
  [ -x "$candidate" ] || svote_ci_die "failed to install cosmovisor at ${candidate}"
  printf '%s\n' "$candidate"
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

svote_ci_read_applied_plan_from_upgrade_info() {
  local daemon_home="$1"
  local upgrade_info="${daemon_home%/}/data/upgrade-info.json"
  local applied_plan

  [ -f "$upgrade_info" ] || return 1
  applied_plan="$(sed -n 's/.*"name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$upgrade_info" | head -n 1)"
  [ -n "$applied_plan" ] || return 1

  # Keep path operations safe; plan names are expected to be filesystem-safe identifiers.
  if ! printf '%s\n' "$applied_plan" | grep -Eq '^[A-Za-z0-9._-]+$'; then
    printf '==> ignoring unsafe applied plan name from %s: %s\n' "$upgrade_info" "$applied_plan" >&2
    return 1
  fi

  printf '%s\n' "$applied_plan"
}

svote_ci_is_cosmovisor_execstart() {
  local service_file="$1"
  if grep -Eq '^ExecStart=.*cosmovisor.*run start' "$service_file"; then
    return 0
  fi
  return 1
}

svote_ci_migrate_direct_service_to_cosmovisor() {
  local service_name="$1"
  local daemon_home="$2"
  local source_bin="$3"
  local cosmovisor_bin
  local systemd_unit_dir="${SYSTEMD_UNIT_DIR:-/etc/systemd/system}"
  local dropin_dir="${systemd_unit_dir%/}/${service_name}.service.d"
  local dropin_path="${dropin_dir}/99-cosmovisor-runtime.conf"
  local genesis_bin="${daemon_home%/}/cosmovisor/genesis/bin/svoted"
  local applied_plan=""
  local applied_plan_bin=""
  local current_target=""

  cosmovisor_bin="$(svote_ci_resolve_cosmovisor_binary "$daemon_home" "$source_bin")"

  svote_ci_log "migrating ${service_name} from direct mode to cosmovisor"
  svote_ci_stage_binary_atomically "$source_bin" "$genesis_bin"
  if applied_plan="$(svote_ci_read_applied_plan_from_upgrade_info "$daemon_home" 2>/dev/null || true)"; then
    if [ -n "$applied_plan" ]; then
      applied_plan_bin="${daemon_home%/}/cosmovisor/upgrades/${applied_plan}/bin/svoted"
      svote_ci_log "seeding applied plan runtime binary: ${applied_plan_bin}"
      svote_ci_stage_binary_atomically "$source_bin" "$applied_plan_bin"
      current_target="${daemon_home%/}/cosmovisor/upgrades/${applied_plan}"
    fi
  fi
  if [ -z "$current_target" ]; then
    current_target="${daemon_home%/}/cosmovisor/genesis"
  fi
  ln -sfn "$current_target" "${daemon_home%/}/cosmovisor/current"

  mkdir -p "$dropin_dir"
  cat > "$dropin_path" <<EOF
[Service]
ExecStart=
ExecStart=${cosmovisor_bin} run start --home ${daemon_home}
Environment="SVOTE_UPGRADE_MODE=cosmovisor"
Environment="DAEMON_HOME=${daemon_home}"
Environment="SVOTE_HOME=${daemon_home}"
Environment="DAEMON_NAME=svoted"
Environment="DAEMON_ALLOW_DOWNLOAD_BINARIES=false"
Environment="COSMOVISOR_BIN=${cosmovisor_bin}"
Environment="SVOTED_BIN=${source_bin}"
EOF
}

main() {
  local service_name="${SERVICE_NAME:-svoted}"
  local daemon_home="${DAEMON_HOME:-/opt/shielded-vote/.svoted}"
  local source_bin="${SOURCE_BIN:-/opt/shielded-vote/current/bin/svoted}"
  local ensure_cosmovisor="${ENSURE_COSMOVISOR_MODE:-true}"
  local migrate_if_direct="${MIGRATE_TO_COSMOVISOR_IF_DIRECT:-false}"
  local service_tmp
  local runtime_link
  local runtime_target
  local runtime_pid
  local runtime_cmd
  local applied_plan
  local plan_bin

  [ -x "$source_bin" ] || svote_ci_die "source binary missing or not executable: ${source_bin}"
  command -v systemctl >/dev/null 2>&1 || svote_ci_die "systemctl is required"

  service_tmp="$(mktemp)"
  trap 'rm -f "${service_tmp:-}"' EXIT
  systemctl cat "$service_name" --no-pager > "$service_tmp"

  if ! svote_ci_is_cosmovisor_execstart "$service_tmp"; then
    if [ "$migrate_if_direct" = "true" ]; then
      svote_ci_migrate_direct_service_to_cosmovisor "$service_name" "$daemon_home" "$source_bin"
      systemctl daemon-reload
      systemctl cat "$service_name" --no-pager > "$service_tmp"
    fi
    if [ "$ensure_cosmovisor" = "true" ]; then
      if ! svote_ci_is_cosmovisor_execstart "$service_tmp"; then
        svote_ci_die "service ${service_name} is not configured for cosmovisor ExecStart"
      fi
    fi
    if ! svote_ci_is_cosmovisor_execstart "$service_tmp"; then
      svote_ci_log "service ${service_name} is not in cosmovisor mode; skipping runtime sync"
      return 0
    fi
  fi

  runtime_link="${daemon_home%/}/cosmovisor/current/bin/svoted"
  runtime_target="$(readlink -f "$runtime_link" 2>/dev/null || true)"
  [ -n "$runtime_target" ] || svote_ci_die "unable to resolve runtime target from ${runtime_link}"
  [ -e "$runtime_target" ] || svote_ci_die "runtime target path does not exist: ${runtime_target}"

  svote_ci_log "service ${service_name} is in cosmovisor mode"
  svote_ci_log "runtime target: ${runtime_target}"

  svote_ci_log "syncing deployed binary into cosmovisor runtime path"
  svote_ci_stage_binary_atomically "$source_bin" "$runtime_target"

  if applied_plan="$(svote_ci_parse_upgrade_plan_from_runtime_path "$runtime_target" "$daemon_home" 2>/dev/null || true)"; then
    if [ -n "$applied_plan" ]; then
      plan_bin="${daemon_home%/}/cosmovisor/upgrades/${applied_plan}/bin/svoted"
      svote_ci_log "mirroring deployed binary into applied plan path: ${plan_bin}"
      svote_ci_stage_binary_atomically "$source_bin" "$plan_bin"
    fi
  fi

  svote_ci_log "restarting ${service_name}"
  systemctl restart "$service_name"
  systemctl is-active --quiet "$service_name" || svote_ci_die "service ${service_name} is not active after restart"

  runtime_pid="$(pgrep -f "cosmovisor.*run start --home ${daemon_home}" | head -n 1 || true)"
  [ -n "$runtime_pid" ] || svote_ci_die "unable to find running cosmovisor process for ${daemon_home}"
  runtime_cmd="$(ps -o args= -p "$runtime_pid" 2>/dev/null || true)"
  printf '%s\n' "$runtime_cmd" | grep -Fq -- "run start --home ${daemon_home}" \
    || svote_ci_die "cosmovisor process command does not match expected home"

  if ! svote_ci_require_matching_hashes "$source_bin" "$runtime_target"; then
    svote_ci_die "runtime hash mismatch after sync (deployed != runtime)"
  fi

  svote_ci_log "cosmovisor runtime verification complete"
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
  main "$@"
fi
