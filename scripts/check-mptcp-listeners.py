#!/usr/bin/env python3
"""Inspect live Linux sockets without relying on MPTCP listener dumps from ss.

Requires root with permission to use pidfd_getfd on the inspected processes.
Exit 1 means MPTCP listeners remain; exit 2 means verification is incomplete.
No sockets are created, closed in the target, or otherwise modified.
"""

import argparse
import ctypes
import errno
import json
import os
from pathlib import Path
import platform
import socket
import sys


def inspect(required):
    """Return listeners and inspection errors, including missing required services."""
    # pidfd_getfd is syscall 438 on these supported Linux architectures.
    if platform.system() != "Linux" or platform.machine() not in ("x86_64", "aarch64"):
        return [], ["Requires Linux x86_64 or aarch64"]
    libc = ctypes.CDLL(None, use_errno=True)
    libc.syscall.restype = ctypes.c_long
    listeners, errors = [], []
    for proc in sorted(Path("/proc").iterdir()):
        if not proc.name.isdigit():
            continue
        try:
            name = (proc / "comm").read_text().strip()
            pidfd = os.pidfd_open(int(proc.name))
        except OSError as exc:
            if exc.errno not in (errno.ENOENT, errno.ESRCH):
                errors.append(f"PID {proc.name}: {exc}")
            continue
        try:
            for fd in (proc / "fd").iterdir():
                try:
                    if not os.readlink(fd).startswith("socket:"):
                        continue
                    duplicate = libc.syscall(438, pidfd, int(fd.name), 0)
                    if duplicate < 0:
                        error = ctypes.get_errno()
                        if error in (errno.EBADF, errno.ESRCH):
                            continue  # Process closed the descriptor during inspection.
                        raise OSError(error, os.strerror(error))
                    with socket.socket(fileno=duplicate) as sock:
                        if sock.family not in (socket.AF_INET, socket.AF_INET6):
                            continue
                        if sock.type != socket.SOCK_STREAM:
                            continue
                        if not sock.getsockopt(socket.SOL_SOCKET, socket.SO_ACCEPTCONN):
                            continue
                        listeners.append({
                            "process": name,
                            "pid": int(proc.name),
                            "address": sock.getsockname(),
                            "protocol": sock.getsockopt(socket.SOL_SOCKET, socket.SO_PROTOCOL),
                        })
                except OSError as exc:
                    if exc.errno not in (errno.ENOENT, errno.ESRCH, errno.EBADF):
                        errors.append(f"{name} PID {proc.name} FD {fd.name}: {exc}")
                        break
        except OSError as exc:
            if exc.errno not in (errno.ENOENT, errno.ESRCH):
                errors.append(f"{name} PID {proc.name}: {exc}")
        finally:
            os.close(pidfd)
    for name in required:
        if not any(item["process"] == name for item in listeners):
            errors.append(f"No listening socket found for required process {name}")
    return listeners, errors


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--require-process", action="append", default=[],
                        help="Fail if this process name has no live listener; repeat as needed")
    args = parser.parse_args()
    listeners, errors = inspect(args.require_process)
    mptcp = [item for item in listeners if item["protocol"] == 262]
    print(json.dumps({"listeners": listeners, "mptcp_listeners": len(mptcp),
                      "errors": errors}, indent=2))
    if errors:
        return 2
    return 1 if mptcp else 0


if __name__ == "__main__":
    sys.exit(main())
