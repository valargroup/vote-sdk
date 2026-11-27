#!/usr/bin/env bash
# Prints export lines for the environment the Rust e2e tests read, pointed at
# the isolated localnet. Use it as:
#   eval "$(bash scripts/localnet-isolated/env.sh)"

LOCALNET_SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=scripts/localnet-isolated/common.sh
. "$LOCALNET_SCRIPT_DIR/common.sh"
localnet_load_env

HOME1="$(localnet_home 1)"
API1="http://127.0.0.1:$(localnet_port "$LOCALNET_API_PORT" 1)"

# emit prints one shell-quoted export line.
emit() {
    printf 'export %s=%q\n' "$1" "$2"
}

# The tests run `svoted` from PATH to sign, so ours must come first.
# shellcheck disable=SC2016  # $PATH must expand when the output is eval'd
printf 'export PATH=%q:"$PATH"\n' "$LOCALNET_BIN_DIR"
emit SVOTE_API_URL "$API1"
emit HELPER_SERVER_URL "$API1"
emit SVOTE_NODE_URL "tcp://127.0.0.1:$(localnet_port "$LOCALNET_RPC_PORT" 1)"
emit SVOTE_HOME "$HOME1"
emit SVOTE_PALLAS_PK_PATH "$HOME1/pallas.pk"
emit SVOTE_CHAIN_ID "$LOCALNET_CHAIN_ID"
emit VM_PRIVKEYS "$(cat "$LOCALNET_DIR/vm_privkeys")"
emit HELPER_API_TOKEN "$LOCALNET_HELPER_API_TOKEN"
if [ "$PROXY" = "1" ]; then
    # The development verifier's public seed (common.sh), for a local attester.
    emit SVOTE_PROXY_VERIFIER_SEED "$LOCALNET_PROXY_VERIFIER_SEED_HEX"
fi
