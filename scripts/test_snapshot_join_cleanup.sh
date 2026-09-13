#!/usr/bin/env bash
# Regression: snapshot cleanup must not leak a RETURN trap into later sourcing.
set -euo pipefail
source "$(dirname "$0")/_join_common.sh"
TEST_ROOT=$(mktemp -d)
trap 'rm -rf "$TEST_ROOT"' EXIT
HOME_DIR="$TEST_ROOT/node"
CHAIN_ID=upgrade-test-1
SNAPSHOT_BASE_URL=https://snapshot.example.test
mkdir -p "$HOME_DIR/data" "$TEST_ROOT/snapshot/data/blockstore.db"
printf '{"height":"0"}' > "$HOME_DIR/data/priv_validator_state.json"
printf '{"height":"999"}' > "$TEST_ROOT/snapshot/data/priv_validator_state.json"
printf 'block fixture' > "$TEST_ROOT/snapshot/data/blockstore.db/CURRENT"
COPYFILE_DISABLE=1 tar -cf "$TEST_ROOT/snapshot.tar" -C "$TEST_ROOT/snapshot" data
checksum=$(svote_sha256_file "$TEST_ROOT/snapshot.tar")
jq -nc --arg checksum "$checksum" '{chain_id:"upgrade-test-1",height:"999",url:"https://snapshot.example.test/snapshot",checksum:$checksum}' > "$TEST_ROOT/latest.json"
curl() {
  local target='' arg source="$TEST_ROOT/latest.json"
  while [ "$#" -gt 0 ]; do
    arg="$1"; shift
    case "$arg" in
      -o) target="$1"; shift ;;
      https://snapshot.example.test/snapshot) source="$TEST_ROOT/snapshot.tar" ;;
    esac
  done
  cp "$source" "$target"
}
lz4() { cat "$2"; }
svote_restore_latest_snapshot
[ "$(jq -r .height "$HOME_DIR/data/priv_validator_state.json")" = 0 ]
[ -f "$HOME_DIR/data/blockstore.db/CURRENT" ]
[ -z "$(trap -p RETURN)" ]
printf ':\n' > "$TEST_ROOT/next-helper.sh"
source "$TEST_ROOT/next-helper.sh"
echo 'PASS: snapshot keeps fresh signer state and does not leak cleanup traps'
