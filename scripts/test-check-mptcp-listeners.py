#!/usr/bin/env python3
"""Run as root inside `unshare --net` on Linux; only loopback is enabled.

Tests the verifier against real TCP/MPTCP listeners without enabling MPTCP in
the host namespace. Other namespaces must have no MPTCP listeners for the
final host-wide check to succeed.
"""

import json
import os
from pathlib import Path
import subprocess
import sys


def main():
    if os.readlink("/proc/self/ns/net") == os.readlink("/proc/1/ns/net"):
        raise SystemExit("Run inside unshare --net; do not enable MPTCP on the host")
    subprocess.run(["ip", "link", "set", "lo", "up"], check=True)
    subprocess.run(["sysctl", "-w", "net.mptcp.enabled=1"], check=True)
    checker = Path(__file__).with_name("check-mptcp-listeners.py")
    fixture = """
import ctypes, socket, sys
ctypes.CDLL(None).prctl(15, b'mptcp-test', 0, 0, 0)
tcp = socket.socket(socket.AF_INET, socket.SOCK_STREAM, socket.IPPROTO_TCP)
mptcp = socket.socket(socket.AF_INET, socket.SOCK_STREAM, 262)
for sock in (tcp, mptcp):
    sock.bind(('127.0.0.1', 0))
    sock.listen()
print('ready', flush=True)
sys.stdin.readline()
mptcp.close()
print('tcp only', flush=True)
sys.stdin.readline()
"""
    child = subprocess.Popen([sys.executable, "-c", fixture], stdin=subprocess.PIPE,
                             stdout=subprocess.PIPE, text=True)
    try:
        assert child.stdout.readline().strip() == "ready"

        def check(required, expected):
            result = subprocess.run([sys.executable, str(checker), "--require-process", required],
                                    capture_output=True, text=True, timeout=30)
            assert result.returncode == expected, result.stdout + result.stderr
            return json.loads(result.stdout)

        result = check("mptcp-test", 1)
        assert not result["errors"]
        protocols = {item["protocol"] for item in result["listeners"]
                     if item["pid"] == child.pid}
        assert protocols == {6, 262}, protocols
        child.stdin.write("close MPTCP\n")
        child.stdin.flush()
        assert child.stdout.readline().strip() == "tcp only"
        result = check("mptcp-test", 0)
        assert result["mptcp_listeners"] == 0 and not result["errors"]
        result = check("missing-process", 2)
        assert any("missing-process" in error for error in result["errors"])
        print("PASS: detects MPTCP, accepts TCP, rejects a missing required process")
    finally:
        child.terminate()
        child.wait(timeout=5)


if __name__ == "__main__":
    main()
