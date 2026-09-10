#!/usr/bin/env python3
"""Capture one-second staging metrics over SSH without opening public ports.

Run concurrently with stage-bench. Files contain aggregate metrics and host
counters only. --profiles also captures a 15-second CPU profile and goroutine
stacks on each helper; profiling is opt-in because it adds runtime overhead.
"""
import argparse
import concurrent.futures
import json
import os
from pathlib import Path
import subprocess
import time

REMOTE = r'''
import json, pathlib, sys, time, urllib.request, subprocess, re
duration = int(sys.argv[1])
prefixes = ('svote_helper_', 'svote_vote_tx_', 'go_', 'process_', 'caddy_http_', 'caddy_reverse_proxy_', 'cometbft_consensus_', 'tendermint_consensus_')
wall_started = time.time()
started = time.monotonic()
for tick in range(duration):
    sample = {'unix_ns':time.time_ns(), 'tick':tick, 'metrics':{}, 'host':{}, 'errors':[]}
    for name, url in [('svoted','http://127.0.0.1:1317/metrics'),('caddy','http://127.0.0.1:2019/metrics')]:
        try:
            raw = urllib.request.urlopen(url, timeout=0.8).read().decode()
            sample['metrics'][name] = [line for line in raw.splitlines() if line.startswith(prefixes)]
        except Exception as error: sample['errors'].append(name+':'+type(error).__name__)
    for name in ['stat','meminfo','diskstats','net/snmp','net/netstat','pressure/cpu','pressure/io','pressure/memory']:
        try: sample['host'][name] = pathlib.Path('/proc',name).read_text()
        except OSError: pass
    sample['collection_ms'] = (time.monotonic() - started - tick)*1000
    print(json.dumps(sample), flush=True)
    time.sleep(max(0, started+tick+1-time.monotonic()))
# Journal access records were filtered before storage by the Caddy encoder.
# Project again to a fixed whitelist before transporting them off the host.
fields = ('ts','request_id','route','method','protocol','duration','status','upstream_duration_ms','upstream_headers_ms')
try:
    logs = subprocess.check_output(['journalctl','-u','caddy','--since','@'+str(int(wall_started)),'--no-pager','-o','json'],text=True)
    for line in logs.splitlines():
        entry = json.loads(line)
        message = entry.get('MESSAGE','')
        if not isinstance(message,str): continue
        try: access = json.loads(message)
        except ValueError: continue
        if access.get('logger') == 'http.log.access.helper_diagnostics' and re.fullmatch('[0-9a-f]{32}',access.get('request_id','')):
            print(json.dumps({'caddy_access':{key:access[key] for key in fields if key in access}}),flush=True)
except Exception as error:
    print(json.dumps({'capture_error':'journal:'+type(error).__name__}),flush=True)
'''


def private_file(path):
    return os.fdopen(os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600), "wb")


def capture(role, out, seconds):
    host = f"root@stage.vote-chain-{role}.valargroup.org"
    with private_file(out / f"{role}.jsonl") as output:
        result = subprocess.run(["ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10", host,
                                 f"python3 - {seconds}"], input=REMOTE.encode(), stdout=output, stderr=subprocess.PIPE)
    return {"role":role,"exit_code":result.returncode}


def profiles(role, out):
    host = f"root@stage.vote-chain-{role}.valargroup.org"
    for name, route in [("cpu.pprof","profile?seconds=15"),("goroutine.txt","goroutine?debug=1"),("mutex.pprof","mutex"),("block.pprof","block")]:
        with private_file(out / f"{role}-{name}") as output:
            result = subprocess.run(["ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10", host,
                f"curl --fail --silent --max-time 20 'http://127.0.0.1:6060/debug/pprof/{route}'"], stdout=output, stderr=subprocess.PIPE)
        if result.returncode:
            return {"role":role,"profile_error":name,"exit_code":result.returncode}
    return {"role":role,"profiles":"captured"}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--seconds",type=int,default=120)
    parser.add_argument("--out",type=Path,required=True)
    parser.add_argument("--profiles",action="store_true")
    args = parser.parse_args()
    if not 1 <= args.seconds <= 86400:
        parser.error("seconds must be between 1 and 86400")
    args.out.mkdir(parents=True,exist_ok=False,mode=0o700)
    with concurrent.futures.ThreadPoolExecutor(max_workers=4) as pool:
        tasks = [pool.submit(capture,role,args.out,args.seconds) for role in ['primary','secondary']]
        if args.profiles:
            tasks += [pool.submit(profiles,role,args.out) for role in ['primary','secondary']]
        results = [task.result() for task in tasks]
    summary = {"started_by":"collect-helper-diagnostics", "completed_unix_ns":time.time_ns(), "seconds":args.seconds,"results":results}
    with private_file(args.out/'capture.json') as output:
        output.write(json.dumps(summary).encode())
    print(json.dumps(summary))
    if any(item.get('exit_code',0) for item in results):
        raise SystemExit(1)


if __name__ == '__main__':
    main()
