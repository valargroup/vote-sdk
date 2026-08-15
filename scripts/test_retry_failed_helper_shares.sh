#!/usr/bin/env bash

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SCRIPT="${REPO_ROOT}/scripts/retry-failed-helper-shares.py"
TEST_DIR="$(mktemp -d)"
SERVER_PID=""
LOCK_PID=""
cleanup() {
  if [ -n "${LOCK_PID}" ]; then
    kill "${LOCK_PID}" >/dev/null 2>&1 || true
    wait "${LOCK_PID}" >/dev/null 2>&1 || true
  fi
  if [ -n "${SERVER_PID}" ]; then
    kill "${SERVER_PID}" >/dev/null 2>&1 || true
    wait "${SERVER_PID}" >/dev/null 2>&1 || true
  fi
  rm -rf "${TEST_DIR}"
}
trap cleanup EXIT

ROUND_ID="c34da31e27020fa8cf533c9593f33ec7dd2d264c0bb2dc77bd3cd80082e82404"
HOME_DIR="${TEST_DIR}/home"
DB_PATH="${HOME_DIR}/helper.db"
FAKE_BIN="${TEST_DIR}/bin"
SERVICE_STATE="${TEST_DIR}/service-state"
SYSTEMCTL_LOG="${TEST_DIR}/systemctl.log"
PORT_FILE="${TEST_DIR}/port"
ROUND_END="$(( $(date +%s) + 3600 ))"

mkdir -p "${HOME_DIR}/config" "${FAKE_BIN}"
printf '{"chain_id":"svote-1"}\n' > "${HOME_DIR}/config/genesis.json"
printf '[helper]\ndb_path = ""\n' > "${HOME_DIR}/config/app.toml"
printf 'active\n' > "${SERVICE_STATE}"

python3 - "${DB_PATH}" "${ROUND_ID}" "${ROUND_END}" <<'PY'
import sqlite3
import sys

db_path, round_id, round_end = sys.argv[1:]
with sqlite3.connect(db_path) as db:
    db.execute(
        """
        CREATE TABLE shares (
            round_id TEXT NOT NULL,
            share_index INTEGER NOT NULL,
            shares_hash TEXT NOT NULL,
            proposal_id INTEGER NOT NULL,
            vote_decision INTEGER NOT NULL,
            enc_share_c1 TEXT NOT NULL,
            enc_share_c2 TEXT NOT NULL,
            tree_position INTEGER NOT NULL,
            share_comms TEXT NOT NULL,
            primary_blind TEXT NOT NULL,
            state INTEGER NOT NULL,
            attempts INTEGER NOT NULL,
            vote_end_time INTEGER NOT NULL,
            submit_at INTEGER NOT NULL,
            original_submit_at INTEGER NOT NULL,
            received_at INTEGER NOT NULL,
            PRIMARY KEY (round_id, share_index, proposal_id, tree_position)
        )
        """
    )
    db.execute(
        """
        INSERT INTO shares VALUES (?, 12, 'hash', 2, 0, 'c1', 'c2', 69,
                                   '["comm"]', 'blind', 3, 5, ?, 100, 100, 50)
        """,
        (round_id, int(round_end)),
    )
    db.execute(
        """
        INSERT INTO shares VALUES (?, 13, 'hash2', 1, 1, 'c1b', 'c2b', 70,
                                   '["comm2"]', 'blind2', 3, 7, ?, 200, 200, 60)
        """,
        (round_id, int(round_end)),
    )
    db.execute(
        """
        INSERT INTO shares VALUES (?, 14, 'hash3', 1, 0, '', '', 71,
                                   '[]', '', 2, 0, ?, 300, 300, 70)
        """,
        (round_id, int(round_end)),
    )
    db.execute(
        """
        INSERT INTO shares VALUES (?, 15, 'hash4', 1, 0, 'c1d', 'c2d', 72,
                                   '["comm4"]', '', 3, 5, ?, 400, 400, 80)
        """,
        (round_id, int(round_end)),
    )
PY

python3 - "${PORT_FILE}" "${ROUND_ID}" "${ROUND_END}" <<'PY' &
import base64
import http.server
import json
import socketserver
import sys

port_file, round_id, round_end = sys.argv[1:]

class Handler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        body = json.dumps({"round": {
            "vote_round_id": base64.b64encode(bytes.fromhex(round_id)).decode(),
            "status": 1,
            "vote_end_time": int(round_end),
        }}).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, _format, *_args):
        pass

with socketserver.TCPServer(("127.0.0.1", 0), Handler) as server:
    with open(port_file, "w", encoding="utf-8") as output:
        output.write(str(server.server_address[1]))
    server.serve_forever()
PY
SERVER_PID="$!"
for _ in $(seq 1 50); do
  [ -s "${PORT_FILE}" ] && break
  sleep 0.1
done
[ -s "${PORT_FILE}" ]
CHAIN_API_PORT="$(cat "${PORT_FILE}")"

cat > "${FAKE_BIN}/systemctl" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "${SYSTEMCTL_LOG}"
case "${1:-}" in
  is-active)
    [ "$(cat "${SERVICE_STATE}")" = active ]
    ;;
  stop)
    printf 'inactive\n' > "${SERVICE_STATE}"
    if [ "${FAKE_STOP_FAILURE_AFTER_STOP:-0}" = 1 ]; then
      exit 1
    fi
    ;;
  start)
    printf 'active\n' > "${SERVICE_STATE}"
    ;;
  *)
    exit 1
    ;;
esac
SH
chmod +x "${FAKE_BIN}/systemctl" "${SCRIPT}" "${REPO_ROOT}/scripts/test_retry_failed_helper_shares.sh"

COMMON_ARGS=(
  --home "${HOME_DIR}"
  --round-id "${ROUND_ID}"
  --expected-chain-id svote-1
  --chain-api-port "${CHAIN_API_PORT}"
  --retry-delay-seconds 60
)

if "${SCRIPT}" "${COMMON_ARGS[@]}" > "${TEST_DIR}/incomplete.out" 2>&1; then
  echo "recovery unexpectedly accepted an incomplete failed witness" >&2
  exit 1
fi
grep -q 'failed share witness is incomplete' "${TEST_DIR}/incomplete.out"
python3 - "${DB_PATH}" <<'PY'
import sqlite3
import sys
with sqlite3.connect(sys.argv[1]) as db:
    assert db.execute(
        "SELECT state, attempts, submit_at FROM shares WHERE share_index = 12"
    ).fetchone() == (3, 5, 100)
    db.execute("DELETE FROM shares WHERE share_index = 15")
PY

"${SCRIPT}" "${COMMON_ARGS[@]}" > "${TEST_DIR}/dry-run.out"
grep -q 'DRY RUN: no changes made' "${TEST_DIR}/dry-run.out"

before="$(python3 - "${DB_PATH}" <<'PY'
import sqlite3
import sys
with sqlite3.connect(sys.argv[1]) as db:
    print(db.execute("SELECT state, attempts, submit_at FROM shares ORDER BY share_index").fetchall())
PY
)"
[ "${before}" = '[(3, 5, 100), (3, 7, 200), (2, 0, 300)]' ]

if PATH="${FAKE_BIN}:${PATH}" \
  SERVICE_STATE="${SERVICE_STATE}" \
  SYSTEMCTL_LOG="${SYSTEMCTL_LOG}" \
  FAKE_STOP_FAILURE_AFTER_STOP=1 \
  "${SCRIPT}" "${COMMON_ARGS[@]}" --execute > "${TEST_DIR}/stop-failure.out" 2>&1; then
  echo "recovery unexpectedly continued after a service stop error" >&2
  exit 1
fi
grep -q "returned non-zero exit status" "${TEST_DIR}/stop-failure.out"
[ "$(cat "${SERVICE_STATE}")" = active ]

LOCK_READY="${TEST_DIR}/lock-ready"
python3 - "${DB_PATH}.lock" "${LOCK_READY}" <<'PY' &
import fcntl
import pathlib
import sys
import time

with open(sys.argv[1], "a+", encoding="utf-8") as lock_file:
    fcntl.flock(lock_file, fcntl.LOCK_EX)
    pathlib.Path(sys.argv[2]).touch()
    time.sleep(60)
PY
LOCK_PID="$!"
for _ in $(seq 1 50); do
  [ -f "${LOCK_READY}" ] && break
  sleep 0.1
done
[ -f "${LOCK_READY}" ]

if PATH="${FAKE_BIN}:${PATH}" \
  SERVICE_STATE="${SERVICE_STATE}" \
  SYSTEMCTL_LOG="${SYSTEMCTL_LOG}" \
  "${SCRIPT}" "${COMMON_ARGS[@]}" --execute > "${TEST_DIR}/locked.out" 2>&1; then
  echo "recovery unexpectedly modified a locked helper database" >&2
  exit 1
fi
grep -q 'helper DB is still locked' "${TEST_DIR}/locked.out" || {
  cat "${TEST_DIR}/locked.out" >&2
  exit 1
}
[ "$(cat "${SERVICE_STATE}")" = active ]
kill "${LOCK_PID}"
wait "${LOCK_PID}" >/dev/null 2>&1 || true
LOCK_PID=""

PATH="${FAKE_BIN}:${PATH}" \
  SERVICE_STATE="${SERVICE_STATE}" \
  SYSTEMCTL_LOG="${SYSTEMCTL_LOG}" \
  "${SCRIPT}" "${COMMON_ARGS[@]}" --execute > "${TEST_DIR}/execute.out"

python3 - "${DB_PATH}" <<'PY'
import sqlite3
import sys
import time

with sqlite3.connect(sys.argv[1]) as db:
    rows = db.execute(
        """
        SELECT share_index, state, attempts, submit_at, original_submit_at,
               enc_share_c1, enc_share_c2, share_comms, primary_blind
          FROM shares
         ORDER BY share_index
        """
    ).fetchall()
assert rows[0][0:3] == (12, 0, 0), rows
assert rows[1][0:3] == (13, 0, 0), rows
assert int(time.time()) + 30 <= rows[0][3] <= int(time.time()) + 90, rows
assert rows[0][3] == rows[1][3], rows
assert rows[0][4:] == (100, 'c1', 'c2', '["comm"]', 'blind'), rows
assert rows[1][4:] == (200, 'c1b', 'c2b', '["comm2"]', 'blind2'), rows
assert rows[2] == (14, 2, 0, 300, 300, '', '', '[]', ''), rows
PY

[ "$(grep -c '^stop svoted$' "${SYSTEMCTL_LOG}")" -eq 3 ]
[ "$(grep -c '^start svoted$' "${SYSTEMCTL_LOG}")" -eq 3 ]
grep -q 'Scheduled 2 failed shares and restarted svoted successfully' "${TEST_DIR}/execute.out"
[ "$(cat "${SERVICE_STATE}")" = active ]

if "${SCRIPT}" "${COMMON_ARGS[@]}" > "${TEST_DIR}/second.out" 2>&1; then
  echo "second recovery unexpectedly succeeded" >&2
  exit 1
fi
grep -q 'the round has no failed helper shares' "${TEST_DIR}/second.out"

printf 'retry-failed-helper-shares tests passed\n'
