#!/usr/bin/env bash
# Unit tests for stale-plan recovery and managed-signer enforcement.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
COMMON="${REPO_ROOT}/scripts/_chain_upgrade_common.sh"

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

# shellcheck source=scripts/_chain_upgrade_common.sh
source "$COMMON"

"${REPO_ROOT}/scripts/update_chain.sh" --help | grep -q 'recover-active-upgrade' \
  || fail "update_chain.sh help does not list recover-active-upgrade"

TMP_ROOT="$(mktemp -d)"
trap 'rm -rf "$TMP_ROOT"' EXIT

DAEMON_HOME="${TMP_ROOT}/home"
COSMVISOR_ROOT="${DAEMON_HOME}/cosmovisor"
GENESIS_BIN="${COSMVISOR_ROOT}/genesis/bin/svoted"
SERVICE_NAME="svoted"
mkdir -p "${DAEMON_HOME}/data" "$(dirname "$GENESIS_BIN")"
printf '#!/usr/bin/env bash\n' > "$GENESIS_BIN"
chmod +x "$GENESIS_BIN"

svote_upgrade_query_applied_plan_height() {
  case "$1" in
    v1) printf '802461\n' ;;
    v1.2.0) printf '4883196\n' ;;
    *) fail "unexpected applied-plan query: $1" ;;
  esac
}

make_versioned_binary() {
  local path="$1"
  local version="$2"
  mkdir -p "$(dirname "$path")"
  # The generated fixture needs a literal positional parameter.
  # shellcheck disable=SC2016
  printf '#!/usr/bin/env bash\n[ "${1:-}" = version ] && printf "%%s\\n" %q\n' "$version" > "$path"
  chmod +x "$path"
}

echo "=== stale recovery: archive an exactly matched applied marker ==="
printf '{"name":"v1","height":802461}\n' > "${DAEMON_HOME}/data/upgrade-info.json"
mkdir -p "${COSMVISOR_ROOT}/upgrades"
ln -s "${COSMVISOR_ROOT}/upgrades/v1" "${COSMVISOR_ROOT}/current"
svote_upgrade_prepare_stale_plan_recovery "v1.1.0"
[ "$SVOTE_STALE_PLAN_NAME" = "v1" ] || fail "stale plan name was not captured"
[ "$SVOTE_STALE_PLAN_HEIGHT" = "802461" ] || fail "stale plan height was not captured"
svote_upgrade_archive_stale_plan_marker
[ ! -e "${DAEMON_HOME}/data/upgrade-info.json" ] || fail "stale marker remained in watched location"
[ "$(readlink "${COSMVISOR_ROOT}/current")" = "${COSMVISOR_ROOT}/genesis" ] \
  || fail "current did not point to genesis after recovery"
[ -f "$SVOTE_STALE_UPGRADE_INFO_ARCHIVE" ] || fail "stale marker audit archive is missing"
grep -q '"name":"v1"' "$SVOTE_STALE_UPGRADE_INFO_ARCHIVE" || fail "archive contents changed"

echo "=== active recovery: exact applied marker and prior applied current are required ==="
OLD_UPGRADE_BIN="${COSMVISOR_ROOT}/upgrades/v1/bin/svoted"
TARGET_UPGRADE_BIN="${COSMVISOR_ROOT}/upgrades/v1.2.0/bin/svoted"
make_versioned_binary "$OLD_UPGRADE_BIN" "v1.0.0"
make_versioned_binary "$TARGET_UPGRADE_BIN" "v1.2.0"
printf '{"name":"v1.2.0","height":4883196}\n' > "${DAEMON_HOME}/data/upgrade-info.json"
ln -sfn "${COSMVISOR_ROOT}/upgrades/v1" "${COSMVISOR_ROOT}/current"

svote_upgrade_validate_active_plan_recovery "v1.2.0"
[ "$SVOTE_RECOVERY_PLAN_NAME" = "v1.2.0" ] || fail "active recovery plan name was not captured"
[ "$SVOTE_RECOVERY_PLAN_HEIGHT" = "4883196" ] || fail "active recovery plan height was not captured"
svote_upgrade_validate_recovery_current_link "v1.2.0"

printf '{"name":"v1.2.0","height":4883195}\n' > "${DAEMON_HOME}/data/upgrade-info.json"
if (svote_upgrade_validate_active_plan_recovery "v1.2.0") >/dev/null 2>&1; then
  fail "mismatched active recovery height was accepted"
fi
printf '{"name":"v1.2.0","height":4883196}\n' > "${DAEMON_HOME}/data/upgrade-info.json"

ln -sfn "${TMP_ROOT}/unexpected" "${COSMVISOR_ROOT}/current"
if (svote_upgrade_validate_recovery_current_link "v1.2.0") >/dev/null 2>&1; then
  fail "unexpected Cosmovisor current target was accepted"
fi
ln -sfn "${COSMVISOR_ROOT}/upgrades/v1" "${COSMVISOR_ROOT}/current"

echo "=== active recovery: Cosmovisor config is accepted without a live MainPID ==="
systemctl() {
  case "$*" in
    "show svoted -p Environment --value")
      printf 'SVOTE_UPGRADE_MODE=cosmovisor DAEMON_HOME=%s\n' "$DAEMON_HOME"
      ;;
    "show svoted -p ExecStart --value")
      printf '{ path=/root/.local/bin/cosmovisor ; argv[]=/root/.local/bin/cosmovisor run start --home %s ; }\n' "$DAEMON_HOME"
      ;;
    *) return 1 ;;
  esac
}
svote_upgrade_assert_cosmovisor_service_config
unset -f systemctl

systemctl() {
  case "$*" in
    "show svoted -p ActiveState --value") printf 'inactive\n' ;;
    *) return 1 ;;
  esac
}
ln -sfn "${COSMVISOR_ROOT}/genesis" "${COSMVISOR_ROOT}/current"
if (svote_upgrade_activate_recovery_plan "v1.2.0" "v1.2.0") >/dev/null 2>&1; then
  fail "Cosmovisor current change after validation was accepted"
fi
ln -sfn "${COSMVISOR_ROOT}/upgrades/v1" "${COSMVISOR_ROOT}/current"
svote_upgrade_activate_recovery_plan "v1.2.0" "v1.2.0"
[ "$(readlink "${COSMVISOR_ROOT}/current")" = "${COSMVISOR_ROOT}/upgrades/v1.2.0" ] \
  || fail "active recovery did not select the target plan"
svote_upgrade_assert_current_upgrade "v1.2.0" "v1.2.0"
unset -f systemctl

echo "=== signer stop: activating systemd units are stopped unconditionally ==="
STOP_LOG="${TMP_ROOT}/systemctl-stop.log"
SYSTEMD_ACTIVE_STATE="activating"
systemctl() {
  case "$*" in
    "stop svoted")
      printf 'stop\n' >> "$STOP_LOG"
      SYSTEMD_ACTIVE_STATE="inactive"
      ;;
    "show svoted -p ActiveState --value") printf '%s\n' "$SYSTEMD_ACTIVE_STATE" ;;
    *) return 1 ;;
  esac
}
svote_upgrade_stop_validator_service
grep -qx 'stop' "$STOP_LOG" || fail "activating service was not stopped"
unset -f systemctl

echo "=== auto-download: common helper writes checksum-required drop-in ==="
SERVICE_PATH="${TMP_ROOT}/systemd/svoted.service"
mkdir -p "$(dirname "$SERVICE_PATH")"
printf '[Service]\nExecStart=/root/.local/bin/cosmovisor run start --home %s\n' "$DAEMON_HOME" > "$SERVICE_PATH"
LEGACY_RUNTIME_DROPIN="${TMP_ROOT}/systemd/svoted.service.d/99-cosmovisor-runtime.conf"
mkdir -p "$(dirname "$LEGACY_RUNTIME_DROPIN")"
printf '[Service]\nEnvironment="DAEMON_ALLOW_DOWNLOAD_BINARIES=false"\n' > "$LEGACY_RUNTIME_DROPIN"
svote_upgrade_configure_autodownload_dropin
COMMON_AUTODOWNLOAD_DROPIN="${TMP_ROOT}/systemd/svoted.service.d/zz-cosmovisor-autodownload.conf"
grep -q 'DAEMON_ALLOW_DOWNLOAD_BINARIES=true' "$COMMON_AUTODOWNLOAD_DROPIN" || fail "common drop-in did not enable downloads"
grep -q 'DAEMON_DOWNLOAD_MUST_HAVE_CHECKSUM=true' "$COMMON_AUTODOWNLOAD_DROPIN" || fail "common drop-in did not require checksums"
[ "$(printf '%s\n' "$(basename "$LEGACY_RUNTIME_DROPIN")" "$(basename "$COMMON_AUTODOWNLOAD_DROPIN")" | LC_ALL=C sort | tail -n 1)" = "$(basename "$COMMON_AUTODOWNLOAD_DROPIN")" ] \
  || fail "auto-download drop-in does not override the legacy runtime drop-in"

AUTODOWNLOAD_EFFECTIVE_ENV='DAEMON_ALLOW_DOWNLOAD_BINARIES=false DAEMON_DOWNLOAD_MUST_HAVE_CHECKSUM=false DAEMON_ALLOW_DOWNLOAD_BINARIES=true DAEMON_DOWNLOAD_MUST_HAVE_CHECKSUM=true'
systemctl() {
  case "$*" in
    "show svoted -p Environment --value") printf '%s\n' "$AUTODOWNLOAD_EFFECTIVE_ENV" ;;
    *) return 1 ;;
  esac
}
svote_upgrade_assert_autodownload_enabled
AUTODOWNLOAD_EFFECTIVE_ENV='DAEMON_ALLOW_DOWNLOAD_BINARIES=true DAEMON_DOWNLOAD_MUST_HAVE_CHECKSUM=false'
if (svote_upgrade_assert_autodownload_enabled) >/dev/null 2>&1; then
  fail "invalid effective auto-download settings were accepted"
fi
unset -f systemctl

echo "=== stale recovery: current and mismatched markers fail closed ==="
printf '{"name":"v1.1.0","height":4890179}\n' > "${DAEMON_HOME}/data/upgrade-info.json"
if (svote_upgrade_prepare_stale_plan_recovery "v1.1.0") >/dev/null 2>&1; then
  fail "current plan marker should not be archived"
fi

printf '{"name":"v1","height":802460}\n' > "${DAEMON_HOME}/data/upgrade-info.json"
if (svote_upgrade_prepare_stale_plan_recovery "v1.1.0") >/dev/null 2>&1; then
  fail "mismatched applied height should not be archived"
fi

echo "=== signer guard: exactly one managed Cosmovisor/svoted pair ==="
SVOTE_PROC_ROOT="${TMP_ROOT}/proc"
export SVOTE_PROC_ROOT
mkdir -p "${SVOTE_PROC_ROOT}"

make_process() {
  local pid="$1"
  local executable_name="$2"
  local cmdline="$3"
  local cgroup="$4"
  local -a argv=()
  local process_dir="${SVOTE_PROC_ROOT}/${pid}"
  local executable="${TMP_ROOT}/bin/${executable_name}"
  mkdir -p "$process_dir" "$(dirname "$executable")"
  : > "$executable"
  ln -s "$executable" "${process_dir}/exe"
  read -r -a argv <<< "$cmdline"
  printf '%s\0' "${argv[@]}" > "${process_dir}/cmdline"
  printf 'DAEMON_HOME=%s\0' "$DAEMON_HOME" > "${process_dir}/environ"
  printf 'Uid:\t%s\t%s\t%s\t%s\n' "$(id -u)" "$(id -u)" "$(id -u)" "$(id -u)" > "${process_dir}/status"
  printf '0::%s\n' "$cgroup" > "${process_dir}/cgroup"
}

make_process 91001 cosmovisor "/usr/local/bin/cosmovisor run start --home ${DAEMON_HOME}" "/system.slice/svoted.service"
make_process 91002 svoted "${COSMVISOR_ROOT}/genesis/bin/svoted start --home ${DAEMON_HOME}" "/system.slice/svoted.service"
rm "${SVOTE_PROC_ROOT}/91002/exe"
ln -s "${TMP_ROOT}/bin/svoted (deleted)" "${SVOTE_PROC_ROOT}/91002/exe"

systemctl() {
  case "$*" in
    "show svoted -p MainPID --value") printf '91001\n' ;;
    "show svoted -p ControlGroup --value") printf '/system.slice/svoted.service\n' ;;
    *) return 1 ;;
  esac
}

svote_upgrade_check_single_managed_signer \
  || fail "valid managed signer pair was rejected: ${SVOTE_MANAGED_SIGNER_ERROR}"

echo "=== signer guard: unmanaged second signer is rejected ==="
make_process 91003 svoted "/usr/local/bin/svoted start --home ${DAEMON_HOME}" "/user.slice/session.scope"
if svote_upgrade_check_single_managed_signer; then
  fail "unmanaged second signer was accepted"
fi
case "$SVOTE_MANAGED_SIGNER_ERROR" in
  *"outside svoted cgroup"*) ;;
  *) fail "unexpected unmanaged signer error: ${SVOTE_MANAGED_SIGNER_ERROR}" ;;
esac

echo "=== signer guard: join.sh wrapper supervision is accepted ==="
SVOTE_PROC_ROOT="${TMP_ROOT}/proc-wrapper"
mkdir -p "$SVOTE_PROC_ROOT"
make_process 92001 bash "/usr/bin/bash /root/.local/bin/svoted-wrapper.sh" "/system.slice/svoted.service"
make_process 92002 cosmovisor "/root/.local/bin/cosmovisor run start --home ${DAEMON_HOME}" "/system.slice/svoted.service"
make_process 92003 svoted "${COSMVISOR_ROOT}/genesis/bin/svoted start --home ${DAEMON_HOME}" "/system.slice/svoted.service"
systemctl() {
  case "$*" in
    "show svoted -p MainPID --value") printf '92001\n' ;;
    "show svoted -p ControlGroup --value") printf '/system.slice/svoted.service\n' ;;
    *) return 1 ;;
  esac
}
svote_upgrade_check_single_managed_signer \
  || fail "wrapper-managed signer pair was rejected: ${SVOTE_MANAGED_SIGNER_ERROR}"

echo "=== PASS: chain upgrade recovery tests ==="
