#!/usr/bin/env bash
# Starts every validator of the isolated localnet in the background, logging
# to $LOCALNET_DIR/logs and recording pids in $LOCALNET_DIR/pids, and waits
# for blocks. On the first start of a multi-validator localnet it registers
# validators 2..N with create-val-tx, as CI does, and waits until all are
# bonded with Pallas keys. stop.sh stops exactly the processes started here.

LOCALNET_SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=scripts/localnet-isolated/common.sh
. "$LOCALNET_SCRIPT_DIR/common.sh"
localnet_load_env
export PATH="$LOCALNET_BIN_DIR:$PATH"

API1="$(localnet_port "$LOCALNET_API_PORT" 1)"
RPC1="$(localnet_port "$LOCALNET_RPC_PORT" 1)"
REGISTERED_MARKER="$LOCALNET_DIR/.validators-registered"

if localnet_any_running; then
    localnet_die "the localnet in $LOCALNET_DIR is already running"
fi
for i in $(localnet_range 1 "$LOCALNET_VALIDATORS"); do
    for port in $(localnet_validator_ports "$i"); do
        if localnet_port_listening "$port"; then
            localnet_die "port $port is already in use; stop its owner or init with other LOCALNET_*_PORT values"
        fi
    done
done

# wait_for_block waits up to SECONDS for val1 to serve a block at or above
# HEIGHT, failing early when a validator process exits.
wait_for_block() {
    local height="$1" seconds="$2" i latest
    for _ in $(localnet_range 1 "$seconds"); do
        latest="$(curl -sf "http://127.0.0.1:$API1/cosmos/base/tendermint/v1beta1/blocks/latest" 2>/dev/null \
            | jq -r '.block.header.height // empty' 2>/dev/null || true)"
        if [ -n "$latest" ] && [ "$latest" -ge "$height" ]; then
            return 0
        fi
        for i in $(localnet_range 1 "$LOCALNET_VALIDATORS"); do
            if ! localnet_pid_of "$i" >/dev/null; then
                tail -n 40 "$LOCALNET_LOG_DIR/val$i.log" >&2 || true
                localnet_die "validator $i exited; see $LOCALNET_LOG_DIR/val$i.log"
            fi
        done
        sleep 1
    done
    localnet_die "val1 served no block at height $height within ${seconds}s"
}

# bonded_with_pallas_keys prints how many bonded validators have a
# registered Pallas key.
bonded_with_pallas_keys() {
    local bonded registered
    bonded="$(curl -sf "http://127.0.0.1:$API1/cosmos/staking/v1beta1/validators?status=BOND_STATUS_BONDED&pagination.limit=200" \
        | jq -r '.validators[]?.operator_address' | sort)" || bonded=""
    registered="$(curl -sf "http://127.0.0.1:$API1/shielded-vote/v1/pallas-keys" \
        | jq -r '.validators[]?.validator_address' | sort)" || registered=""
    comm -12 <(printf '%s\n' "$bonded") <(printf '%s\n' "$registered") | sed '/^$/d' | wc -l | tr -d ' '
}

for i in $(localnet_range 1 "$LOCALNET_VALIDATORS"); do
    home="$(localnet_home "$i")"
    log="$LOCALNET_LOG_DIR/val$i.log"
    # Unset everything that could point svoted at a live service: Sentry,
    # lightwalletd and the production voting config. env execs svoted, so
    # the recorded pid is svoted's own.
    nohup env -u SENTRY_DSN -u SENTRY_ENVIRONMENT -u SVOTE_LWD_URLS -u SVOTE_ZCASH_NETWORK \
        -u SVOTE_CONFIG_URL -u SVOTE_PRECOMPUTED_BASE_URL -u SVOTE_CONFIG_PR_GITHUB_TOKEN \
        SVOTE_PIR_URL=disabled \
        "$LOCALNET_SVOTED" start --home "$home" </dev/null >>"$log" 2>&1 &
    echo "$!" >"$LOCALNET_PID_DIR/val$i.pid"
    localnet_log "started val$i (pid $!, REST $(localnet_port "$LOCALNET_API_PORT" "$i"), RPC $(localnet_port "$LOCALNET_RPC_PORT" "$i"), log $log)"
done

wait_for_block 2 180
localnet_log "val1 is producing blocks"

if [ "$LOCALNET_VALIDATORS" -gt 1 ] && [ ! -f "$REGISTERED_MARKER" ]; then
    for i in $(localnet_range 2 "$LOCALNET_VALIDATORS"); do
        localnet_log "registering val$i"
        if ! "$LOCALNET_CREATE_VAL_TX" --home "$(localnet_home "$i")" --moniker "val$i" \
            --amount "10000000$LOCALNET_DENOM" --chain-id "$LOCALNET_CHAIN_ID" \
            --rpc-url "tcp://127.0.0.1:$RPC1" >>"$LOCALNET_LOG_DIR/register.log" 2>&1; then
            tail -n 20 "$LOCALNET_LOG_DIR/register.log" >&2
            localnet_die "create-val-tx failed for val$i"
        fi
        # One block between registrations keeps the joiners' txs ordered.
        sleep 6
    done
    for _ in $(localnet_range 1 60); do
        count="$(bonded_with_pallas_keys)"
        if [ "$count" -ge "$LOCALNET_VALIDATORS" ]; then
            break
        fi
        sleep 2
    done
    [ "$count" -ge "$LOCALNET_VALIDATORS" ] \
        || localnet_die "only $count of $LOCALNET_VALIDATORS validators are bonded with Pallas keys"
    touch "$REGISTERED_MARKER"
    localnet_log "all $LOCALNET_VALIDATORS validators are bonded with Pallas keys"
fi

localnet_log "running (PROXY=$PROXY). Test env: eval \"\$(LOCALNET_DIR=$LOCALNET_DIR bash $LOCALNET_SCRIPT_DIR/env.sh)\""
