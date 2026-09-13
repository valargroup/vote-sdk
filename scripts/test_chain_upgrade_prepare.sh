#!/usr/bin/env bash
# Exercise preparation with local release fixtures and a changing on-chain plan.
# The function loaded from the template consumes fixture variables and callbacks.
# shellcheck disable=SC2034,SC2329,SC2153
set -euo pipefail
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
source "${REPO_ROOT}/scripts/_chain_upgrade_common.sh"
eval "$(sed -n '/^run_stage_first() {/,/^# run_verify_prestage/{ /^# run_verify_prestage/d; p; }' "${REPO_ROOT}/scripts/update_chain.sh.template")"
root=$(mktemp -d)
trap 'rm -rf "$root"' EXIT
fail() { echo "FAIL: $*" >&2; exit 1; }

for scenario in valid repeat fresh wrong-tag wrong-checksum wrong-platform malformed cancelled applied expired changed-during-download renamed-plan identity-install-failure install-failure fresh-install-failure; do
  (
    case_root="$root/$scenario"
    DAEMON_HOME="$case_root/home"
    COSMVISOR_ROOT="$DAEMON_HOME/cosmovisor"
    GENESIS_BIN="$COSMVISOR_ROOT/genesis/bin/svoted"
    INSTALL_DIR="$case_root/bin"
    COSMOVISOR_BIN="$INSTALL_DIR/cosmovisor"
    PLAN_NAME=v1.6.0
    RELEASE_TAG=v1.6.0
    MODE=prepare
    ALLOW_NO_PLAN=0
    INSTALL_CLI_SET=1
    SERVICE_USER=''
    SVOTE_PLATFORM=linux-amd64
    target="$COSMVISOR_ROOT/upgrades/$PLAN_NAME/bin/svoted"
    identity="$(dirname "$(dirname "$target")")/prepared-artifact.json"
    mkdir -p "$DAEMON_HOME/config" "$(dirname "$GENESIS_BIN")" "$INSTALL_DIR" "$(dirname "$target")"
    printf '{"chain_id":"upgrade-test-1"}' > "$DAEMON_HOME/config/genesis.json"
    printf '#!/bin/sh\necho v1.4.0\n' > "$GENESIS_BIN"
    printf '#!/bin/sh\necho v1.6.0\n' > "$case_root/candidate"
    printf '#!/bin/sh\nexit 0\n' > "$COSMOVISOR_BIN"
    chmod +x "$GENESIS_BIN" "$case_root/candidate" "$COSMOVISOR_BIN"
    printf '#!/bin/sh\necho v1.6.0-rc.6\n' > "$target"
    printf '{"tag":"v1.6.0-rc.6"}' > "$identity"
    cp "$target" "$case_root/previous-binary"
    cp "$identity" "$case_root/previous-identity"
    case "$scenario" in fresh|fresh-install-failure) rm "$target" "$identity" ;; esac
    archive_sha=$(printf archive | shasum -a 256 | awk '{print $1}')
    jq -nc --arg sha "$archive_sha" '{name:"v1.6.0",height:"1000",info:({tag:"v1.6.0",binaries:{"linux/amd64":("https://example.test/archive?checksum=sha256:"+$sha)}}|tojson)}' > "$case_root/plan.json"
    case "$scenario" in
      wrong-tag) filter='.info |= (fromjson | .tag="v1.6.0-rc.6" | tojson)' ;;
      wrong-checksum) filter='.info |= (fromjson | .binaries["linux/amd64"]="https://example.test/archive?checksum=sha256:0000000000000000000000000000000000000000000000000000000000000000" | tojson)' ;;
      wrong-platform) filter='.info |= (fromjson | .binaries={} | tojson)' ;;
      malformed) filter='.info="invalid"' ;;
      *) filter='.' ;;
    esac
    jq "$filter" "$case_root/plan.json" > "$case_root/plan.new"
    mv "$case_root/plan.new" "$case_root/plan.json"

    # Stub external I/O, leaving validation, staging, and commit code real.
    svote_upgrade_verify_validator_identity_files() { :; }
    svote_upgrade_resolve_runtime_svoted() { echo "$GENESIS_BIN"; }
    svote_upgrade_query_upgrade_plan() { cat "$case_root/plan.json"; }
    svote_upgrade_query_applied_plan_height() {
      if [ "$scenario" = applied ] && [ -e "$case_root/downloaded" ]; then echo 20; else echo 0; fi
    }
    svote_upgrade_query_block_height() {
      if [ "$scenario" = expired ] && [ -e "$case_root/downloaded" ]; then echo 1000; else echo 10; fi
    }
    svote_upgrade_download_release_tarball() {
      touch "$case_root/downloaded"
      echo fixture
    }
    svote_upgrade_extract_svoted() { echo "$case_root/candidate"; }
    svote_upgrade_verify_cosmovisor_archive() { SVOTE_PREPARED_ARCHIVE_SHA256="$archive_sha"; }
    svote_upgrade_install_cosmovisor() {
      case "$scenario" in
        cancelled) printf null > "$case_root/plan.json" ;;
        renamed-plan)
          jq '.name="different-plan"' "$case_root/plan.json" > "$case_root/plan.new"
          command mv "$case_root/plan.new" "$case_root/plan.json" ;;
        changed-during-download)
          jq '.info |= (fromjson | .tag="v1.6.0-rc.6" | tojson)' "$case_root/plan.json" > "$case_root/plan.new"
          command mv "$case_root/plan.new" "$case_root/plan.json" ;;
      esac
    }
    svote_upgrade_has_cosmovisor_runtime_for_home() { return 0; }
    mv() {
      case "$scenario" in
        identity-install-failure)
          if [ "${*: -1}" = "$identity" ]; then return 1; fi ;;
        install-failure|fresh-install-failure)
          if [ "${*: -1}" = "$target" ]; then return 1; fi ;;
      esac
      command mv "$@"
    }

    # Do not use `if run_stage_first`: it disables errexit inside shell functions.
    set +e
    ( set -e; trap '[ -z "${TMP_DIR:-}" ] || rm -rf "$TMP_DIR"' EXIT; run_stage_first ) > "$case_root/output" 2>&1
    result=$?
    set -e
    case "$scenario" in
      valid|repeat|fresh)
        [ "$result" = 0 ] || { cat "$case_root/output"; fail "$scenario rejected"; }
        cmp "$case_root/candidate" "$target" || fail 'candidate not installed'
        svote_upgrade_verify_artifact_identity "$PLAN_NAME" "$RELEASE_TAG"
        if [ "$scenario" = repeat ]; then
          ( trap '[ -z "${TMP_DIR:-}" ] || rm -rf "$TMP_DIR"' EXIT; run_stage_first ) > "$case_root/repeat-output" 2>&1
          cmp "$case_root/candidate" "$target"
          svote_upgrade_verify_artifact_identity "$PLAN_NAME" "$RELEASE_TAG"
        fi ;;
      *)
        [ "$result" != 0 ] || fail "$scenario unexpectedly succeeded"
        if [ "$scenario" = fresh-install-failure ]; then
          [ ! -e "$target" ] && [ ! -e "$identity" ] || fail 'failed first preparation left staged files'
        else
          cmp "$case_root/previous-binary" "$target" || fail "$scenario changed staged executable"
          cmp "$case_root/previous-identity" "$identity" || fail "$scenario changed staged metadata"
        fi ;;
    esac
    [ "$("$GENESIS_BIN" version)" = v1.4.0 ] || fail 'genesis changed'
    echo "PASS: preparation $scenario"
  )
done
