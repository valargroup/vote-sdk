#!/usr/bin/env bash
# Unit tests for disable-cosmovisor-backups.sh safety and cleanup helpers.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SCRIPT="${REPO_ROOT}/scripts/disable-cosmovisor-backups.sh"

# macOS CI exercises the procfs logic through SVOTE_PROC_ROOT fixtures. Its rm
# lacks GNU --one-file-system, so strip only that flag inside this test process.
if [ "$(uname -s)" = "Darwin" ]; then
  rm() {
    local arg
    local -a filtered=()
    for arg in "$@"; do
      [ "$arg" = "--one-file-system" ] && continue
      filtered+=("$arg")
    done
    command /bin/rm "${filtered[@]}"
  }
fi

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

expect_failure() {
  local label="$1"
  shift
  if ("$@") >/dev/null 2>&1; then
    fail "${label}: expected failure"
  fi
}

# shellcheck source=scripts/disable-cosmovisor-backups.sh
source "$SCRIPT"

TMP_ROOT="$(readlink -f -- "$(mktemp -d)")"
trap 'rm -rf "$TMP_ROOT"' EXIT
TEST_HOME="${TMP_ROOT}/validator-home"
CUSTOM_BACKUPS="${TMP_ROOT}/custom-backups"
SVOTE_PROC_ROOT="${TMP_ROOT}/proc"
export SVOTE_PROC_ROOT

mkdir -p \
  "${TEST_HOME}/config" \
  "${TEST_HOME}/data" \
  "${TEST_HOME}/cosmovisor/genesis/bin" \
  "$CUSTOM_BACKUPS" \
  "${TMP_ROOT}/bin" \
  "${SVOTE_PROC_ROOT}/1001" \
  "${SVOTE_PROC_ROOT}/1002" \
  "${SVOTE_PROC_ROOT}/1003"
printf '{"chain_id":"zvote-1"}\n' > "${TEST_HOME}/config/genesis.json"
printf '{"pub_key":{"value":"test-consensus-key"}}\n' > "${TEST_HOME}/config/priv_validator_key.json"
printf '{"height":"10","round":0,"step":0}\n' > "${TEST_HOME}/data/priv_validator_state.json"
printf '#!/usr/bin/env bash\n' > "${TEST_HOME}/cosmovisor/genesis/bin/svoted"
chmod +x "${TEST_HOME}/cosmovisor/genesis/bin/svoted"
ln -s "${TEST_HOME}/cosmovisor/genesis" "${TEST_HOME}/cosmovisor/current"

printf '#!/usr/bin/env bash\n' > "${TMP_ROOT}/bin/cosmovisor"
printf '#!/usr/bin/env bash\n' > "${TMP_ROOT}/bin/svoted"
chmod +x "${TMP_ROOT}/bin/cosmovisor" "${TMP_ROOT}/bin/svoted"
ln -s /bin/bash "${SVOTE_PROC_ROOT}/1001/exe"
ln -s "${TMP_ROOT}/bin/cosmovisor" "${SVOTE_PROC_ROOT}/1002/exe"
ln -s "${TMP_ROOT}/bin/svoted" "${SVOTE_PROC_ROOT}/1003/exe"
printf '%s\0' "${TMP_ROOT}/bin/svoted-wrapper.sh" > "${SVOTE_PROC_ROOT}/1001/cmdline"
printf '%s\0' "${TMP_ROOT}/bin/cosmovisor" run start --home "$TEST_HOME" > "${SVOTE_PROC_ROOT}/1002/cmdline"
printf '%s\0' "${TMP_ROOT}/bin/svoted" start --home "$TEST_HOME" > "${SVOTE_PROC_ROOT}/1003/cmdline"
printf '0::/system.slice/svoted.service\n' > "${SVOTE_PROC_ROOT}/1001/cgroup"
printf '0::/system.slice/svoted.service\n' > "${SVOTE_PROC_ROOT}/1002/cgroup"
printf '0::/system.slice/svoted.service\n' > "${SVOTE_PROC_ROOT}/1003/cgroup"

write_main_process_env() {
  local skip_value="${1:-}"
  {
    printf 'SVOTE_UPGRADE_MODE=cosmovisor\0'
    printf 'DAEMON_NAME=svoted\0'
    printf 'DAEMON_HOME=%s\0' "$TEST_HOME"
    printf 'SVOTE_HOME=%s\0' "$TEST_HOME"
    printf 'DAEMON_DATA_BACKUP_DIR=%s\0' "$CUSTOM_BACKUPS"
    if [ -n "$skip_value" ]; then
      printf 'UNSAFE_SKIP_BACKUP=%s\0' "$skip_value"
    fi
  } > "${SVOTE_PROC_ROOT}/1001/environ"
  cp "${SVOTE_PROC_ROOT}/1001/environ" "${SVOTE_PROC_ROOT}/1002/environ"
  cp "${SVOTE_PROC_ROOT}/1001/environ" "${SVOTE_PROC_ROOT}/1003/environ"
}

FAKE_EFFECTIVE_SKIP=""
FAKE_SERVICE_ACTIVE=1
FAKE_RESTARTS=0
systemctl() {
  local command="${1:-}"
  shift || true
  case "$command" in
    cat) return 0 ;;
    is-active)
      [ "$FAKE_SERVICE_ACTIVE" = "1" ]
      ;;
    show)
      case " $* " in
        *" -p MainPID "*) printf '1001\n' ;;
        *" -p ControlGroup "*) printf '/system.slice/svoted.service\n' ;;
        *" -p Environment "*)
          printf 'SVOTE_UPGRADE_MODE=cosmovisor DAEMON_NAME=svoted DAEMON_HOME=%s SVOTE_HOME=%s' "$TEST_HOME" "$TEST_HOME"
          if [ -n "$FAKE_EFFECTIVE_SKIP" ]; then
            printf ' UNSAFE_SKIP_BACKUP=%s' "$FAKE_EFFECTIVE_SKIP"
          fi
          printf '\n'
          ;;
        *) return 1 ;;
      esac
      ;;
    daemon-reload) return 0 ;;
    restart)
      FAKE_RESTARTS=$((FAKE_RESTARTS + 1))
      return 0
      ;;
    *) return 1 ;;
  esac
}

SERVICE_NAME="svoted"
EXPECTED_CHAIN_ID="zvote-1"
write_main_process_env

echo "=== standard managed service preflight ==="
assert_service_layout
assert_validator_home
[ "$DAEMON_HOME" = "$TEST_HOME" ] || fail "unexpected daemon home: ${DAEMON_HOME}"

echo "=== default and custom backup roots ==="
mkdir -p \
  "${TEST_HOME}/data-backup-2026-8-13" \
  "${CUSTOM_BACKUPS}/data-backup-2026-08-14"
resolve_backup_roots
[ "${#BACKUP_ROOTS[@]}" -eq 2 ] || fail "expected two backup roots"
collect_backup_dirs
[ "${#BACKUP_DIRS[@]}" -eq 2 ] || fail "expected two backup directories"

echo "=== malformed and symlink candidates are rejected ==="
mkdir -p "${TEST_HOME}/data-backup-latest"
expect_failure "malformed backup" collect_backup_dirs
rm -rf "${TEST_HOME}/data-backup-latest"
ln -s "${TEST_HOME}/data-backup-2026-8-13" "${TEST_HOME}/data-backup-2026-8-15"
expect_failure "symlink backup" collect_backup_dirs
rm -f "${TEST_HOME}/data-backup-2026-8-15"

echo "=== chain mismatch and current-link escape are rejected ==="
EXPECTED_CHAIN_ID="svote-1"
expect_failure "wrong chain" assert_validator_home
EXPECTED_CHAIN_ID="zvote-1"
rm "${TEST_HOME}/cosmovisor/current"
ln -s "$TMP_ROOT" "${TEST_HOME}/cosmovisor/current"
expect_failure "escaped current link" assert_validator_home
rm "${TEST_HOME}/cosmovisor/current"
ln -s "${TEST_HOME}/cosmovisor/genesis" "${TEST_HOME}/cosmovisor/current"
assert_validator_home

echo "=== active setting requires systemd and runtime agreement ==="
FAKE_EFFECTIVE_SKIP="true"
write_main_process_env true
assert_skip_backup_active
write_main_process_env false
expect_failure "runtime setting mismatch" assert_skip_backup_active
write_main_process_env true

echo "=== rollback restores the previous drop-in ==="
DROPIN_ROLLBACK_DIR="${TMP_ROOT}/rollback"
DROPIN_PATH="${TMP_ROOT}/dropin.conf"
mkdir -p "$DROPIN_ROLLBACK_DIR"
printf '[Service]\nEnvironment="UNSAFE_SKIP_BACKUP=false"\n' > "${DROPIN_ROLLBACK_DIR}/previous.conf"
printf '[Service]\nEnvironment="UNSAFE_SKIP_BACKUP=true"\n' > "$DROPIN_PATH"
DROPIN_PREVIOUSLY_EXISTED=1
DROPIN_MUTATED=1
restore_previous_dropin
cmp "$DROPIN_PATH" "${DROPIN_ROLLBACK_DIR}/previous.conf" \
  || fail "rollback did not restore previous drop-in"
[ "$DROPIN_MUTATED" = "0" ] || fail "rollback mutation marker was not cleared"

echo "=== rendered entrypoint rejects an unpinned local helper ==="
ARTIFACT_ROOT="${TMP_ROOT}/artifact-root"
RENDERED_ROOT="${TMP_ROOT}/rendered-root"
mkdir -p \
  "${ARTIFACT_ROOT}/scripts/disable-cosmovisor-backups/v1" \
  "$RENDERED_ROOT"
cp "${REPO_ROOT}/scripts/_chain_upgrade_common.sh" \
  "${ARTIFACT_ROOT}/scripts/disable-cosmovisor-backups/v1/_chain_upgrade_common.sh"
"${REPO_ROOT}/scripts/render-disable-cosmovisor-backups.sh" \
  v1 \
  "file://${ARTIFACT_ROOT}" \
  > "${RENDERED_ROOT}/disable-cosmovisor-backups.sh"
printf '# unexpected local helper\n' > "${RENDERED_ROOT}/_chain_upgrade_common.sh"
rendered_help="$(bash "${RENDERED_ROOT}/disable-cosmovisor-backups.sh" --help 2>&1)" \
  || fail "rendered entrypoint did not recover from an unpinned local helper"
grep -Fq 'Ignoring local shared helper with an unexpected checksum' <<< "$rendered_help" \
  || fail "rendered entrypoint did not report the rejected local helper"
grep -Fq 'usage: disable-cosmovisor-backups.sh' <<< "$rendered_help" \
  || fail "rendered entrypoint did not load the pinned helper"

echo "=== exact backup cleanup leaves active state and Cosmovisor tree ==="
collect_backup_dirs
removed_count="${#BACKUP_DIRS[@]}"
[ "$removed_count" -eq 2 ] || fail "expected two backups before deletion"
mountpoint() { return 1; }
delete_backup_dirs
[ -d "${TEST_HOME}/data" ] || fail "active data was removed"
[ -d "${TEST_HOME}/cosmovisor" ] || fail "Cosmovisor tree was removed"
[ -f "${TEST_HOME}/config/priv_validator_key.json" ] || fail "validator key was removed"
[ "${#BACKUP_DIRS[@]}" -eq 0 ] || fail "backup directories remain"

echo "=== PASS: disable Cosmovisor backup tests ==="
