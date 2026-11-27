#!/usr/bin/env bash
# Builds svoted into $LOCALNET_DIR/bin and initializes an isolated localnet of
# LOCALNET_VALIDATORS validators under $LOCALNET_DIR, with every port moved off
# the defaults. Genesis follows scripts/init.sh (one validator) and
# scripts/init_multi.sh (several): vote managers from VM_PRIVKEYS, the
# vote_funding pool, Pallas keys, the per-round EA key path and the helper
# section. With PROXY=1, genesis also opens delegate registration under the
# development verifier key in common.sh. start.sh registers validators 2..N
# once the chain runs, as CI does.
#
# Settings are documented in common.sh and docs/localnet-isolated.md.

LOCALNET_SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=scripts/localnet-isolated/common.sh
. "$LOCALNET_SCRIPT_DIR/common.sh"

PROXY="${PROXY:-0}"
LOCALNET_VALIDATORS="${LOCALNET_VALIDATORS:-1}"
LOCALNET_CHAIN_ID="${LOCALNET_CHAIN_ID:-svote-1}"
LOCALNET_API_PORT="${LOCALNET_API_PORT:-21317}"
LOCALNET_RPC_PORT="${LOCALNET_RPC_PORT:-36657}"
LOCALNET_P2P_PORT="${LOCALNET_P2P_PORT:-36656}"
LOCALNET_ABCI_PORT="${LOCALNET_ABCI_PORT:-36658}"
LOCALNET_PROMETHEUS_PORT="${LOCALNET_PROMETHEUS_PORT:-36660}"
LOCALNET_GRPC_PORT="${LOCALNET_GRPC_PORT:-19090}"
LOCALNET_GRPC_WEB_PORT="${LOCALNET_GRPC_WEB_PORT:-19091}"
LOCALNET_PPROF_PORT="${LOCALNET_PPROF_PORT:-16060}"
LOCALNET_PORT_STRIDE="${LOCALNET_PORT_STRIDE:-100}"
HELPER_API_TOKEN="${SVOTE_HELPER_API_TOKEN:-}"
HELPER_EXPOSE_QUEUE_STATUS="${SVOTE_HELPER_EXPOSE_QUEUE_STATUS:-true}"
HELPER_MAX_CONCURRENT_PROOFS_V3="${SVOTE_HELPER_MAX_CONCURRENT_PROOFS_V3:-2}"

case "$PROXY" in
    0 | 1) ;;
    *) localnet_die "PROXY must be 0 or 1, got '$PROXY'" ;;
esac
case "$LOCALNET_VALIDATORS" in
    [1-9]) ;;
    *) localnet_die "LOCALNET_VALIDATORS must be 1 to 9, got '$LOCALNET_VALIDATORS'" ;;
esac
for setting in LOCALNET_API_PORT LOCALNET_RPC_PORT LOCALNET_P2P_PORT LOCALNET_ABCI_PORT \
    LOCALNET_PROMETHEUS_PORT LOCALNET_GRPC_PORT LOCALNET_GRPC_WEB_PORT LOCALNET_PPROF_PORT \
    LOCALNET_PORT_STRIDE; do
    value="${!setting}"
    case "$value" in
        '' | *[!0-9]*) localnet_die "$setting must be a number, got '$value'" ;;
    esac
done

localnet_require_safe_dir
if [ -f "$LOCALNET_ENV_FILE" ]; then
    # Check with the recorded settings: a running chain may use other ports.
    if (localnet_load_env && localnet_any_running); then
        localnet_die "the localnet in $LOCALNET_DIR is running; run make localnet-isolated-stop first"
    fi
fi

mkdir -p "$LOCALNET_DIR"
touch "$LOCALNET_MARKER"
find "$LOCALNET_DIR" -mindepth 1 -maxdepth 1 ! -name bin ! -name "$(basename "$LOCALNET_MARKER")" \
    -exec rm -rf {} +
mkdir -p "$LOCALNET_BIN_DIR" "$LOCALNET_LOG_DIR" "$LOCALNET_PID_DIR"
INIT_LOG="$LOCALNET_LOG_DIR/init.log"
: >"$INIT_LOG"

TAGS="$(localnet_build_tags)"
BUILD_ENV="$LOCALNET_BIN_DIR/build.env"
if [ "${LOCALNET_SKIP_BUILD:-0}" = "1" ]; then
    [ -x "$LOCALNET_SVOTED" ] || localnet_die "LOCALNET_SKIP_BUILD=1 but $LOCALNET_SVOTED is missing"
    grep -qx "PROXY=$PROXY" "$BUILD_ENV" 2>/dev/null \
        || localnet_die "bin/ was built for another PROXY mode; rebuild without LOCALNET_SKIP_BUILD"
    if [ "$LOCALNET_VALIDATORS" -gt 1 ] && [ ! -x "$LOCALNET_CREATE_VAL_TX" ]; then
        localnet_die "LOCALNET_SKIP_BUILD=1 but $LOCALNET_CREATE_VAL_TX is missing"
    fi
    localnet_log "reusing $LOCALNET_SVOTED (tags $TAGS)"
else
    localnet_log "building circuits and svoted (tags $TAGS) into $LOCALNET_BIN_DIR"
    # The Makefile's build-ffi recipe, writing to our bin instead of ./svoted.
    # It never runs go install, so ~/go/bin is left alone.
    rm -f "$BUILD_ENV"
    make -C "$LOCALNET_REPO_ROOT" --no-print-directory build-ffi \
        BINARY="$LOCALNET_SVOTED" FFI_TAGS="$TAGS" BUILD_TAGS="$TAGS"
    (cd "$LOCALNET_REPO_ROOT" && go build -o "$LOCALNET_CREATE_VAL_TX" ./scripts/create-val-tx)
    printf 'PROXY=%s\nTAGS=%s\nCOMMIT=%s\n' "$PROXY" "$TAGS" \
        "$(git -C "$LOCALNET_REPO_ROOT" rev-parse HEAD 2>/dev/null || echo unknown)" >"$BUILD_ENV"
fi

# Every svoted below is ours and runs with a --home under LOCALNET_DIR, so
# none writes its default config to ~/.svoted. scripts/_genesis_accounts_lib.sh
# reads BINARY and GENESIS_LIB_HOME, and create-val-tx and the e2e tests find
# svoted on PATH.
BINARY="$LOCALNET_SVOTED"
GENESIS_LIB_HOME="$(localnet_home 1)"
export PATH="$LOCALNET_BIN_DIR:$PATH"
# shellcheck source=scripts/_genesis_accounts_lib.sh
. "$LOCALNET_REPO_ROOT/scripts/_genesis_accounts_lib.sh"
# shellcheck source=scripts/_vote_manager_keys_lib.sh
. "$LOCALNET_REPO_ROOT/scripts/_vote_manager_keys_lib.sh"

# run_logged runs one setup command with its output in logs/init.log, and
# shows the log tail when it fails.
run_logged() {
    if ! "$@" >>"$INIT_LOG" 2>&1; then
        tail -n 40 "$INIT_LOG" >&2
        localnet_die "command failed: $*"
    fi
}

# patch_file applies one sed expression to FILE in place, BSD and GNU alike.
patch_file() {
    sed -i.bak "$2" "$1"
    rm -f "$1.bak"
}

# configure_node moves validator INDEX's listeners onto its ports, points the
# vote API and helper at this node, and adds the helper and UI sections that
# scripts/init.sh adds. PEERS is the persistent_peers value.
configure_node() {
    local index="$1"
    local peers="$2"
    local home
    home="$(localnet_home "$index")"
    local config_toml="$home/config/config.toml"
    local app_toml="$home/config/app.toml"
    local api rpc p2p abci prometheus grpc grpc_web pprof
    api="$(localnet_port "$LOCALNET_API_PORT" "$index")"
    rpc="$(localnet_port "$LOCALNET_RPC_PORT" "$index")"
    p2p="$(localnet_port "$LOCALNET_P2P_PORT" "$index")"
    abci="$(localnet_port "$LOCALNET_ABCI_PORT" "$index")"
    prometheus="$(localnet_port "$LOCALNET_PROMETHEUS_PORT" "$index")"
    grpc="$(localnet_port "$LOCALNET_GRPC_PORT" "$index")"
    grpc_web="$(localnet_port "$LOCALNET_GRPC_WEB_PORT" "$index")"
    pprof="$(localnet_port "$LOCALNET_PPROF_PORT" "$index")"

    # CometBFT listeners, bound to loopback. PEX stays off so these nodes only
    # ever dial each other.
    patch_file "$config_toml" "s|^laddr = \"tcp://127.0.0.1:26657\"|laddr = \"tcp://127.0.0.1:${rpc}\"|"
    patch_file "$config_toml" "s|^laddr = \"tcp://0.0.0.0:26656\"|laddr = \"tcp://127.0.0.1:${p2p}\"|"
    patch_file "$config_toml" "s|^proxy_app = \"tcp://127.0.0.1:26658\"|proxy_app = \"tcp://127.0.0.1:${abci}\"|"
    patch_file "$config_toml" "s|^prometheus_listen_addr = \":26660\"|prometheus_listen_addr = \"127.0.0.1:${prometheus}\"|"
    patch_file "$config_toml" "s|^pprof_laddr = \"localhost:6060\"|pprof_laddr = \"localhost:${pprof}\"|"
    patch_file "$config_toml" "s|^pex = true|pex = false|"
    patch_file "$config_toml" "s|^persistent_peers = \".*\"|persistent_peers = \"${peers}\"|"
    if [ "$LOCALNET_VALIDATORS" -gt 1 ]; then
        # As scripts/init_multi.sh: every node shares 127.0.0.1.
        patch_file "$config_toml" 's/^addr_book_strict = true/addr_book_strict = false/'
        patch_file "$config_toml" 's/^allow_duplicate_ip = false/allow_duplicate_ip = true/'
    fi

    # app.toml as scripts/init.sh writes it, on this node's ports.
    patch_file "$app_toml" 's/^minimum-gas-prices = ""/minimum-gas-prices = "0usvote"/'
    patch_file "$app_toml" '/\[api\]/,/\[.*\]/ s/enable = false/enable = true/'
    patch_file "$app_toml" "s|^address = \"tcp://[a-z0-9.]*:1317\"|address = \"tcp://127.0.0.1:${api}\"|"
    patch_file "$app_toml" '/\[api\]/,/\[.*\]/ s/enabled-unsafe-cors = false/enabled-unsafe-cors = true/'
    patch_file "$app_toml" "s|^address = \"localhost:9090\"|address = \"localhost:${grpc}\"|"
    patch_file "$app_toml" "s|^address = \"localhost:9091\"|address = \"localhost:${grpc_web}\"|"
    patch_file "$app_toml" "s|^ea_sk_path = .*|ea_sk_path = \"$home/ea.sk\"|"
    patch_file "$app_toml" "s|^pallas_sk_path = .*|pallas_sk_path = \"$home/pallas.sk\"|"
    # The vote API broadcasts through comet_rpc, whose default is 26657.
    patch_file "$app_toml" "s|^comet_rpc = .*|comet_rpc = \"http://127.0.0.1:${rpc}\"|"
    # Keep [admin] disabled, and point its config fetch at a closed loopback
    # port so nothing can reach the production voting config.
    patch_file "$app_toml" '/^\[admin\]/,/^\[.*\]/ s/^disable = .*/disable = true/'
    patch_file "$app_toml" 's#^config_url = .*$#config_url = "http://127.0.0.1:9/"#'

    cat >>"$app_toml" <<HELPERCFG

###############################################################################
###                         Helper Server                                   ###
###############################################################################

[helper]

# Set to true to disable the helper server.
disable = false

# Optional auth token for POST /shielded-vote/v1/shares (sent via X-Helper-Token header).
# Empty disables token auth for both share submission and queue-status polling.
api_token = "$HELPER_API_TOKEN"

# Queue metrics endpoint polled by the e2e voter_throughput test.
expose_queue_status = $HELPER_EXPOSE_QUEUE_STATUS

# Public coarse round-level queue histogram endpoint.
expose_queue_summary = true

# Path to the SQLite database file. Empty = default (\$home/helper.db).
db_path = ""

# Port of this node's REST API (used for MsgRevealShare submission).
chain_api_port = ${api}

# Maximum concurrent proof generation goroutines.
max_concurrent_proofs_v3 = $HELPER_MAX_CONCURRENT_PROOFS_V3

# Sentry stays off on the isolated localnet.
sentry_dsn = ""
HELPERCFG

    cat >>"$app_toml" <<UICFG

###############################################################################
###                         Admin UI                                        ###
###############################################################################

[ui]

# Set to true to serve the admin UI from the chain API server.
enable = false

# Path to the built UI dist directory (output of "npm run build" in ui/).
dist_path = ""
UICFG

    # No default listener may survive: a missed one would collide with, or
    # broadcast into, a chain started from scripts/init.sh.
    if grep -HnE '^[^#]*:(1317|26656|26657|26658|26660|9090|9091|6060)"' \
        "$config_toml" "$app_toml"; then
        localnet_die "validator $index still has a default port; see the lines above"
    fi
}

localnet_log "initializing $LOCALNET_VALIDATORS validator(s), chain $LOCALNET_CHAIN_ID, in $LOCALNET_DIR (PROXY=$PROXY)"

if [ -z "${VM_PRIVKEYS:-}" ]; then
    if [ "$PROXY" = "1" ]; then
        # A proxy coordinator action executes only with two approvals
        # (contracts-v1 §5.5), so a proxy localnet gets two vote managers.
        VM_PRIVKEYS="$(openssl rand -hex 32),$(openssl rand -hex 32)"
        localnet_log "generated two vote-manager keys (VM_PRIVKEYS was unset)"
    else
        VM_PRIVKEYS="$(openssl rand -hex 32)"
        localnet_log "generated a vote-manager key (VM_PRIVKEYS was unset)"
    fi
fi
export VM_PRIVKEYS
(umask 077 && printf '%s\n' "$VM_PRIVKEYS" >"$LOCALNET_DIR/vm_privkeys")
parse_vm_privkeys

if [ "$LOCALNET_VALIDATORS" -gt 1 ]; then
    # scripts/init_multi.sh's local stakes: val1 holds enough that it and any
    # one other validator keep more than two thirds of the power.
    VAL1_STAKE="20000000${LOCALNET_DENOM}"
else
    VAL1_STAKE="10000000${LOCALNET_DENOM}"
fi
JOINER_BALANCE="10000000${LOCALNET_DENOM}"

for i in $(localnet_range 1 "$LOCALNET_VALIDATORS"); do
    home="$(localnet_home "$i")"
    run_logged "$BINARY" init "val$i" --chain-id "$LOCALNET_CHAIN_ID" --home "$home"
    run_logged "$BINARY" keys add validator --keyring-backend test --home "$home"
done

HOME1="$(localnet_home 1)"
GENESIS="$HOME1/config/genesis.json"
VAL1_ADDR="$("$BINARY" keys show validator -a --keyring-backend test --home "$HOME1")"
VAL1_VALOPER="$("$BINARY" keys show validator --bech val -a --keyring-backend test --home "$HOME1")"

VOTE_MANAGER_ADDRS=()
for i in "${!VM_PRIVKEY_LIST[@]}"; do
    name="vote-manager-$((i + 1))"
    run_logged "$BINARY" keys import-hex "$name" "${VM_PRIVKEY_LIST[$i]}" \
        --keyring-backend test --home "$HOME1"
    addr="$("$BINARY" keys show "$name" -a --keyring-backend test --home "$HOME1")"
    VOTE_MANAGER_ADDRS+=("$addr")
    genesis_add_auth_base_account "$GENESIS" "$addr"
done

run_logged "$BINARY" genesis add-genesis-account "$VAL1_ADDR" "$VAL1_STAKE" \
    --keyring-backend test --home "$HOME1"
for i in $(localnet_range 2 "$LOCALNET_VALIDATORS"); do
    addr="$("$BINARY" keys show validator -a --keyring-backend test --home "$(localnet_home "$i")")"
    run_logged "$BINARY" genesis add-genesis-account "$addr" "$JOINER_BALANCE" \
        --keyring-backend test --home "$HOME1"
done
run_logged genesis_add_module_funding_account "$GENESIS" vote_funding "$LOCALNET_DENOM" \
    "1000000000${LOCALNET_DENOM}"

run_logged "$BINARY" genesis gentx validator "$VAL1_STAKE" --chain-id "$LOCALNET_CHAIN_ID" \
    --keyring-backend test --home "$HOME1"
run_logged "$BINARY" genesis collect-gentxs --home "$HOME1"

# The genesis validator is bonded at genesis, so its Pallas key is seeded into
# vote genesis; validators 2..N register theirs through create-val-tx.
run_logged "$BINARY" pallas-keygen --home "$HOME1"
PALLAS_PK_B64="$(base64 <"$HOME1/pallas.pk" | tr -d '\n')"
VOTE_MANAGER_JSON="$(printf '%s\n' "${VOTE_MANAGER_ADDRS[@]}" | jq -R . | jq -s .)"
jq --argjson vms "$VOTE_MANAGER_JSON" \
    --arg validator "$VAL1_VALOPER" \
    --arg pallasPk "$PALLAS_PK_B64" '
  .app_state.vote.vote_manager_addresses = $vms
  | .app_state.vote.pallas_keys = [{validator_address: $validator, pallas_pk: $pallasPk}]
  | .app_state.staking.params.historical_entries = 0
  | .app_state.slashing.params.signed_blocks_window = "72800"
  | .app_state.slashing.params.min_signed_per_window = "0.800000000000000000"
  | .app_state.slashing.params.downtime_jail_duration = "300s"
  | .app_state.slashing.params.slash_fraction_double_sign = "0.000000000000000000"
  | .app_state.slashing.params.slash_fraction_downtime = "0.000000000000000000"' \
    "$GENESIS" >"$GENESIS.tmp" && mv "$GENESIS.tmp" "$GENESIS"
if [ "$PROXY" = "1" ]; then
    # Open delegate registration (contracts-v1 §5.3 register steps 1 and 5):
    # the development verifier at threshold 1, and room for delegates. Every
    # other proxy genesis field keeps its default; a PROXY=0 genesis gets no
    # proxy_params at all.
    jq --argjson id "$LOCALNET_PROXY_VERIFIER_ID" \
        --arg pubkey "$LOCALNET_PROXY_VERIFIER_PUBKEY_B64" \
        --argjson maxDelegates "$LOCALNET_PROXY_MAX_DELEGATES" '
  .app_state.vote.proxy_params = {
    verifiers: [{verifier_id: $id, pubkey: $pubkey}],
    verifier_threshold: 1,
    max_delegates: $maxDelegates
  }' "$GENESIS" >"$GENESIS.tmp" && mv "$GENESIS.tmp" "$GENESIS"
fi
run_logged "$BINARY" genesis validate-genesis --home "$HOME1"

NODE_IDS=()
for i in $(localnet_range 1 "$LOCALNET_VALIDATORS"); do
    NODE_IDS+=("$("$BINARY" comet show-node-id --home "$(localnet_home "$i")")")
done
for i in $(localnet_range 1 "$LOCALNET_VALIDATORS"); do
    home="$(localnet_home "$i")"
    if [ "$i" -gt 1 ]; then
        cp "$GENESIS" "$home/config/genesis.json"
        run_logged "$BINARY" pallas-keygen --home "$home"
    fi
    peers=""
    for j in $(localnet_range 1 "$LOCALNET_VALIDATORS"); do
        [ "$j" -eq "$i" ] && continue
        peer="${NODE_IDS[$((j - 1))]}@127.0.0.1:$(localnet_port "$LOCALNET_P2P_PORT" "$j")"
        peers="${peers:+$peers,}$peer"
    done
    configure_node "$i" "$peers"
done

cat >"$LOCALNET_ENV_FILE" <<ENV
# Written by scripts/localnet-isolated/init.sh; start, stop, env and test read it.
PROXY=$PROXY
LOCALNET_VALIDATORS=$LOCALNET_VALIDATORS
LOCALNET_CHAIN_ID=$LOCALNET_CHAIN_ID
LOCALNET_API_PORT=$LOCALNET_API_PORT
LOCALNET_RPC_PORT=$LOCALNET_RPC_PORT
LOCALNET_P2P_PORT=$LOCALNET_P2P_PORT
LOCALNET_ABCI_PORT=$LOCALNET_ABCI_PORT
LOCALNET_PROMETHEUS_PORT=$LOCALNET_PROMETHEUS_PORT
LOCALNET_GRPC_PORT=$LOCALNET_GRPC_PORT
LOCALNET_GRPC_WEB_PORT=$LOCALNET_GRPC_WEB_PORT
LOCALNET_PPROF_PORT=$LOCALNET_PPROF_PORT
LOCALNET_PORT_STRIDE=$LOCALNET_PORT_STRIDE
LOCALNET_HELPER_API_TOKEN=$HELPER_API_TOKEN
ENV

localnet_log "initialized; logs in $LOCALNET_LOG_DIR. Next: make localnet-isolated-start"
