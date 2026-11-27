#!/usr/bin/env bash
# Runs the ignored Rust e2e tests, as make test-api does, against the running
# isolated localnet. Some tests are skipped with the reason printed:
# create_round_for_zashi reads a live lightwalletd and PIR server;
# round_activation asserts a three-validator ceremony, so it runs only when
# LOCALNET_VALIDATORS is at least 3. Arguments are passed to the libtest
# harness, for example a test-name filter.

LOCALNET_SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=scripts/localnet-isolated/common.sh
. "$LOCALNET_SCRIPT_DIR/common.sh"
localnet_load_env
localnet_any_running || localnet_die "the localnet is not running; run make localnet-isolated-start"

eval "$(bash "$LOCALNET_SCRIPT_DIR/env.sh")"

skips=(--skip create_round_for_zashi)
localnet_log "skipping create_round_for_zashi: it needs a live lightwalletd and PIR server"
if [ "$LOCALNET_VALIDATORS" -lt 3 ]; then
    skips+=(--skip round_activation)
    localnet_log "skipping round_activation: it needs LOCALNET_VALIDATORS=3 or more"
fi

localnet_log "running the e2e suite against $SVOTE_API_URL (PROXY=$PROXY, $LOCALNET_VALIDATORS validator(s))"
start_seconds="$(date +%s)"
cd "$LOCALNET_REPO_ROOT"
status=0
cargo test --release --no-fail-fast --manifest-path e2e-tests/Cargo.toml -- \
    --nocapture --ignored --test-threads=1 "${skips[@]}" "$@" || status=$?
localnet_log "e2e suite finished with status $status in $(($(date +%s) - start_seconds))s"
exit "$status"
