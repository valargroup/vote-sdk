#!/usr/bin/env python3
"""Prepare/apply a reversible, staging-only runtime Caddy diagnostic overlay.

Run on the staging host with its matching Caddyfile snippet. No Terraform,
service restart, listener changes, or persistent Caddyfile changes are made.
Backups may contain sensitive existing config and are stored mode 0600.
"""
import argparse
import copy
import json
import os
from pathlib import Path
import socket
import subprocess
import time
import urllib.request


def overlay(current, adapted):
    candidate = copy.deepcopy(current)
    servers = candidate["apps"]["http"]["servers"]
    sample = next(iter(adapted["apps"]["http"]["servers"].values()))
    for server in servers.values():
        if server.get("logs"):
            raise ValueError("existing HTTP logging must be reviewed before adding this overlay")
        server["logs"] = {"default_logger_name": "helper_diagnostics"}
        routes = sample["routes"][0]["handle"][0]["routes"]
        server["routes"] = copy.deepcopy(routes) + server.get("routes", [])
    candidate["apps"]["http"]["metrics"] = {}
    logs = candidate.setdefault("logging", {}).setdefault("logs", {})
    for name, config in adapted["logging"]["logs"].items():
        if name == "default":
            logs.setdefault("default", {}).setdefault("exclude", []).extend(config.get("exclude", []))
        else:
            if name in logs:
                raise ValueError("diagnostic logger already configured")
            logs[name] = config
    return candidate


def write_private(path, content):
    with os.fdopen(os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600), "w") as out:
        json.dump(content, out)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--snippet", type=Path, required=True)
    parser.add_argument("--apply", action="store_true")
    parser.add_argument("--out", type=Path, default=Path("/var/lib/caddy/helper-diagnostics"))
    args = parser.parse_args()
    if socket.gethostname() not in ("vote-primary-stage", "vote-secondary-stage"):
        parser.error("this runtime overlay is restricted to the two staging helpers")
    current = json.load(urllib.request.urlopen("http://127.0.0.1:2019/config/", timeout=10))
    adapted = json.loads(subprocess.check_output(["caddy", "adapt", "--adapter", "caddyfile", "--config", str(args.snippet)], stderr=subprocess.PIPE))
    candidate = overlay(current, adapted)
    args.out.mkdir(parents=True, exist_ok=True, mode=0o700)
    stamp = str(time.time_ns())
    backup = args.out / (stamp + "-before.json")
    target = args.out / (stamp + "-candidate.json")
    write_private(backup, current)
    write_private(target, candidate)
    subprocess.run(["caddy", "validate", "--config", str(target)], check=True, stdout=subprocess.DEVNULL)
    if args.apply:
        # Refuse to overwrite a concurrently changed runtime configuration.
        latest = json.load(urllib.request.urlopen("http://127.0.0.1:2019/config/", timeout=10))
        if latest != current:
            raise RuntimeError("Caddy config changed during validation; rerun")
        request = urllib.request.Request("http://127.0.0.1:2019/load", json.dumps(candidate).encode(), {"Content-Type": "application/json"})
        with urllib.request.urlopen(request, timeout=20) as response:
            response.read()
    print(json.dumps({"applied": args.apply, "candidate": str(target), "rollback": str(backup)}))


if __name__ == "__main__":
    main()
