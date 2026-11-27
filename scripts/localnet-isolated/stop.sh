#!/usr/bin/env bash
# Stops the validators start.sh launched for this localnet. Only a pid whose
# command line is our svoted for our home is signalled, so a recycled pid or
# any other svoted on the machine is never touched. Chain state is kept;
# init.sh wipes it.

LOCALNET_SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=scripts/localnet-isolated/common.sh
. "$LOCALNET_SCRIPT_DIR/common.sh"
localnet_load_env

for i in $(localnet_range 1 "$LOCALNET_VALIDATORS"); do
    pid_file="$LOCALNET_PID_DIR/val$i.pid"
    if pid="$(localnet_pid_of "$i")"; then
        localnet_log "stopping val$i (pid $pid)"
        kill -TERM "$pid" 2>/dev/null || true
        for _ in $(localnet_range 1 30); do
            localnet_pid_of "$i" >/dev/null || break
            sleep 1
        done
        if localnet_pid_of "$i" >/dev/null; then
            localnet_log "val$i ignored SIGTERM for 30s; sending SIGKILL"
            kill -KILL "$pid" 2>/dev/null || true
            sleep 1
        fi
    elif [ -f "$pid_file" ]; then
        localnet_log "val$i is not running (stale pid file)"
    fi
    rm -f "$pid_file"
done
localnet_log "stopped"
