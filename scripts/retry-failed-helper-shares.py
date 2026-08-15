#!/usr/bin/env python3
"""Return a round's failed helper shares to the pending queue.

The helper keeps its runnable schedule in memory, so this script stops svoted,
updates the round's terminal rows, and restarts svoted. It is a dry run unless
--execute is supplied.
"""

from __future__ import annotations

import argparse
import base64
import binascii
import fcntl
import json
import os
from pathlib import Path
import socket
import sqlite3
import subprocess
import sys
import time
import urllib.error
import urllib.request


FAILED = 3
RECEIVED = 0
MIN_ROUND_BUFFER_SECONDS = 60


class RecoveryError(RuntimeError):
    pass


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description=(
            "Reset all failed helper shares in one active round for normal "
            "reconciliation or retry."
        )
    )
    parser.add_argument(
        "--home", required=True, type=Path, help="svoted home directory"
    )
    parser.add_argument(
        "--round-id", required=True, help="64-character voting round ID"
    )
    parser.add_argument("--expected-chain-id", required=True)
    parser.add_argument("--expected-hostname")
    parser.add_argument(
        "--db-path",
        type=Path,
        help="helper database path (default: <home>/helper.db)",
    )
    parser.add_argument(
        "--chain-api-port",
        type=int,
        default=1317,
        help="local chain API port (default: 1317)",
    )
    parser.add_argument("--service", default="svoted")
    parser.add_argument("--retry-delay-seconds", type=int, default=60)
    parser.add_argument(
        "--execute", action="store_true", help="perform the reset and restart svoted"
    )
    args = parser.parse_args()

    try:
        bytes.fromhex(args.round_id)
    except ValueError as exc:
        raise RecoveryError("round ID must be hexadecimal") from exc
    if len(args.round_id) != 64:
        raise RecoveryError("round ID must contain exactly 64 hexadecimal characters")
    args.round_id = args.round_id.lower()

    if not 1 <= args.chain_api_port <= 65535:
        raise RecoveryError("--chain-api-port must be between 1 and 65535")
    if args.retry_delay_seconds < 1:
        raise RecoveryError("--retry-delay-seconds must be positive")
    return args


def validate_host_and_chain(args: argparse.Namespace) -> None:
    if args.expected_hostname and socket.gethostname() != args.expected_hostname:
        raise RecoveryError(
            f"hostname mismatch: expected {args.expected_hostname}, got {socket.gethostname()}"
        )

    genesis_path = args.home / "config" / "genesis.json"
    try:
        chain_id = json.loads(genesis_path.read_text(encoding="utf-8"))["chain_id"]
    except (OSError, KeyError, json.JSONDecodeError) as exc:
        raise RecoveryError(f"could not read chain ID from {genesis_path}") from exc
    if chain_id != args.expected_chain_id:
        raise RecoveryError(
            f"chain ID mismatch: expected {args.expected_chain_id}, got {chain_id}"
        )


def resolve_db_path(args: argparse.Namespace) -> Path:
    if args.db_path:
        return args.db_path.expanduser().resolve()
    return (args.home / "helper.db").resolve()


def load_active_round(args: argparse.Namespace, chain_api_port: int) -> int:
    url = (
        f"http://127.0.0.1:{chain_api_port}/shielded-vote/v1/round/" f"{args.round_id}"
    )
    try:
        with urllib.request.urlopen(url, timeout=5) as response:
            round_data = json.load(response)["round"]
        returned_id = base64.b64decode(round_data["vote_round_id"], validate=True).hex()
        status = round_data["status"]
        vote_end_time = int(round_data["vote_end_time"])
    except (
        OSError,
        KeyError,
        TypeError,
        ValueError,
        binascii.Error,
        json.JSONDecodeError,
        urllib.error.URLError,
    ) as exc:
        raise RecoveryError(f"could not read voting round from {url}") from exc
    if returned_id != args.round_id:
        raise RecoveryError("local chain API returned a different voting round")
    if status not in (1, "1", "SESSION_STATUS_ACTIVE"):
        raise RecoveryError(f"voting round is not active (status {status})")
    return vote_end_time


def load_failed_shares(
    db: sqlite3.Connection, args: argparse.Namespace
) -> list[sqlite3.Row]:
    db.row_factory = sqlite3.Row
    rows = db.execute(
        """
        SELECT share_index, proposal_id, tree_position,
               state, attempts, vote_end_time, submit_at, original_submit_at,
               length(shares_hash) AS shares_hash_len,
               length(enc_share_c1) AS c1_len,
               length(enc_share_c2) AS c2_len,
               length(share_comms) AS comms_len,
               length(primary_blind) AS blind_len
          FROM shares
         WHERE round_id = ? AND state = 3
         ORDER BY share_index, proposal_id, tree_position
        """,
        (args.round_id,),
    ).fetchall()
    if not rows:
        raise RecoveryError("the round has no failed helper shares")
    return rows


def validate_failed_shares(
    rows: list[sqlite3.Row], vote_end_time: int, submit_at: int
) -> None:
    if vote_end_time <= submit_at + MIN_ROUND_BUFFER_SECONDS:
        raise RecoveryError("round ends too soon to retry this share safely")
    for row in rows:
        if row["state"] != FAILED or row["attempts"] < 1:
            raise RecoveryError("failed share has an invalid state or attempt count")
        if row["vote_end_time"] != vote_end_time:
            raise RecoveryError(
                "helper row vote end time does not match the active round"
            )
        required_lengths = {
            "shares_hash": (row["shares_hash_len"], 1),
            "enc_share_c1": (row["c1_len"], 1),
            "enc_share_c2": (row["c2_len"], 1),
            "share_comms": (row["comms_len"], 3),
            "primary_blind": (row["blind_len"], 1),
        }
        missing = [
            name
            for name, (length, minimum) in required_lengths.items()
            if length is None or length < minimum
        ]
        if missing:
            identity = (
                f"share_index={row['share_index']} proposal_id={row['proposal_id']} "
                f"tree_position={row['tree_position']}"
            )
            raise RecoveryError(
                f"failed share witness is incomplete ({identity}): {', '.join(missing)}"
            )


def print_plan(
    db_path: Path,
    rows: list[sqlite3.Row],
    args: argparse.Namespace,
    submit_at: int,
) -> None:
    print("Helper share recovery plan")
    print(f"  host:              {socket.gethostname()}")
    print(f"  chain:             {args.expected_chain_id}")
    print(f"  database:          {db_path}")
    print(f"  round:             {args.round_id}")
    print(f"  failed shares:     {len(rows)}")
    print(f"  new state:         Received ({RECEIVED})")
    print("  new attempts:      0")
    print(f"  new submit_at:     {submit_at}")
    print("  witness fields and original_submit_at remain unchanged")
    print("  rows:")
    for row in rows:
        print(
            f"    share_index={row['share_index']} proposal_id={row['proposal_id']} "
            f"tree_position={row['tree_position']} attempts={row['attempts']}"
        )


def systemctl(*arguments: str, check: bool = True) -> subprocess.CompletedProcess[str]:
    return subprocess.run(
        ["systemctl", *arguments],
        check=check,
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
    )


def service_is_active(service: str) -> bool:
    return systemctl("is-active", "--quiet", service, check=False).returncode == 0


def reset_failed_shares(
    db_path: Path,
    args: argparse.Namespace,
    vote_end_time: int,
    submit_at: int,
    expected_rows: list[tuple[object, ...]],
) -> None:
    lock_fd = os.open(f"{db_path}.lock", os.O_RDWR | os.O_CREAT, 0o600)
    try:
        try:
            fcntl.flock(lock_fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError as exc:
            raise RecoveryError(
                "helper DB is still locked; refusing to modify it"
            ) from exc

        with sqlite3.connect(db_path, timeout=5, isolation_level=None) as db:
            db.execute("BEGIN IMMEDIATE")
            try:
                rows = load_failed_shares(db, args)
                validate_failed_shares(rows, vote_end_time, submit_at)
                if [tuple(row) for row in rows] != expected_rows:
                    raise RecoveryError(
                        "failed share set changed during recovery; no update was committed"
                    )
                result = db.execute(
                    """
                    UPDATE shares
                       SET state = 0, attempts = 0, submit_at = ?
                     WHERE round_id = ? AND state = 3
                    """,
                    (submit_at, args.round_id),
                )
                if result.rowcount != len(rows):
                    raise RecoveryError(
                        "not every failed share was reset; rolling back"
                    )
                db.execute("COMMIT")
            except BaseException:
                db.execute("ROLLBACK")
                raise
    finally:
        os.close(lock_fd)


def execute_recovery(
    db_path: Path,
    args: argparse.Namespace,
    vote_end_time: int,
    submit_at: int,
    expected_rows: list[tuple[object, ...]],
) -> None:
    if not service_is_active(args.service):
        raise RecoveryError(f"systemd service {args.service} is not active")

    operation_error: BaseException | None = None
    restart_required = False
    try:
        restart_required = True
        systemctl("stop", args.service)
        if service_is_active(args.service):
            raise RecoveryError(f"systemd service {args.service} did not stop")
        reset_failed_shares(db_path, args, vote_end_time, submit_at, expected_rows)
    except BaseException as exc:
        operation_error = exc
    finally:
        if restart_required:
            start_result = systemctl("start", args.service, check=False)
            if start_result.returncode != 0:
                message = start_result.stderr.strip() or "unknown systemctl error"
                raise RecoveryError(
                    f"failed to restart {args.service} after recovery attempt: {message}"
                ) from operation_error

    if operation_error is not None:
        raise operation_error
    if not service_is_active(args.service):
        raise RecoveryError(
            f"systemd service {args.service} is not active after restart"
        )


def main() -> int:
    try:
        args = parse_args()
        validate_host_and_chain(args)
        db_path = resolve_db_path(args)
        if not db_path.is_file():
            raise RecoveryError(f"helper DB does not exist: {db_path}")

        submit_at = int(time.time()) + args.retry_delay_seconds
        vote_end_time = load_active_round(args, args.chain_api_port)
        read_uri = f"{db_path.as_uri()}?mode=ro"
        with sqlite3.connect(read_uri, uri=True, timeout=5) as db:
            rows = load_failed_shares(db, args)
            validate_failed_shares(rows, vote_end_time, submit_at)
            print_plan(db_path, rows, args, submit_at)
            expected_rows = [tuple(row) for row in rows]

        if not args.execute:
            print("DRY RUN: no changes made; pass --execute to apply this plan")
            return 0

        execute_recovery(db_path, args, vote_end_time, submit_at, expected_rows)
        print(
            f"Scheduled {len(rows)} failed shares and restarted {args.service} successfully"
        )
        return 0
    except (RecoveryError, OSError, sqlite3.Error, subprocess.SubprocessError) as exc:
        print(f"ERROR: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
