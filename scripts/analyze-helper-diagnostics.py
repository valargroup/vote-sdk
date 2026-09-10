#!/usr/bin/env python3
"""Join bounded client transport diagnostics with Caddy records by random ID.

No payload, URL, or share identity is emitted. Missing correlations are counted
explicitly; server-reported timings are diagnostics, not acceptance evidence.
"""
import argparse
import json
from pathlib import Path


def distribution(values):
    values = sorted(values)
    if not values:
        return {"count":0}
    return {"count":len(values),"mean_ms":sum(values)/len(values),
            **{name:values[min(len(values)-1,int(len(values)*q))] for name,q in [('p50_ms',.5),('p95_ms',.95),('p99_ms',.99),('max_ms',1)]}}


def analyze(run_dirs, capture_dir):
    proxy = {}
    capture_errors = 0
    for file in capture_dir.glob('*.jsonl'):
        with file.open() as source:
            for line in source:
                sample = json.loads(line)
                capture_errors += len(sample.get('errors',[])) + int('capture_error' in sample)
                if 'caddy_access' in sample:
                    access = sample['caddy_access']
                    proxy[access['request_id']] = access
    durations = {name:[] for name in ['client_headers','connection_assignment','handler','unattributed','caddy','upstream_headers']}
    requests = matched = dropped = 0
    protocols = {}
    for directory in run_dirs:
        report = json.loads((directory/'round.observability.json').read_text())
        dropped += sum(report.get(name,0) for name in ['records_dropped','summary_updates_dropped','active_stages_dropped'])
        for record in report['records']:
            diagnostic = record.get('http_diagnostics')
            if not diagnostic: continue
            requests += 1
            protocol = diagnostic.get('protocol') or 'unknown'
            protocols[protocol] = protocols.get(protocol,0)+1
            for source,name in [('response_headers_us','client_headers'),('connection_acquired_us','connection_assignment'),('server_handler_us','handler'),('unattributed_wait_us','unattributed')]:
                if diagnostic.get(source) is not None: durations[name].append(diagnostic[source]/1000)
            access = proxy.get(diagnostic['request_id'])
            if access:
                matched += 1
                if isinstance(access.get('duration'),(int,float)): durations['caddy'].append(access['duration']*1000)
                try: durations['upstream_headers'].append(float(access['upstream_headers_ms']))
                except (KeyError,TypeError,ValueError): pass
    return {'requests':requests,'proxy_matched':matched,'proxy_missing':requests-matched,'capture_errors':capture_errors,
            'dropped_observations':dropped,'protocols':protocols,'timings':{name:distribution(values) for name,values in durations.items()}}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--run',type=Path,action='append',required=True)
    parser.add_argument('--capture',type=Path,required=True)
    args = parser.parse_args()
    print(json.dumps(analyze(args.run,args.capture),indent=2))


if __name__ == '__main__':
    main()
