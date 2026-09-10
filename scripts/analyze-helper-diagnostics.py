#!/usr/bin/env python3
"""Join bounded client transport diagnostics with Caddy records by random ID.

No payload, URL, or share identity is emitted. Missing correlations are counted
explicitly; server-reported timings are diagnostics, not acceptance evidence.
"""
import argparse
import json
import math
from collections import Counter, defaultdict
from pathlib import Path


def distribution(values):
    values = sorted(values)
    if not values:
        return {"count":0}
    return {"count":len(values),"mean_ms":sum(values)/len(values),
            **{name:values[min(len(values)-1,int(len(values)*q))] for name,q in [('p50_ms',.5),('p95_ms',.95),('p99_ms',.99),('max_ms',1)]}}


def milliseconds(value, scale=1):
    """Accept only finite nonnegative timing values, including proxy strings."""
    try:
        number = float(value) * scale
        return number if math.isfinite(number) and number >= 0 else None
    except (TypeError, ValueError):
        return None


def analyze(run_dirs, capture_dir):
    proxy, server = {}, {}
    phases = defaultdict(list)
    coverage = Counter()
    for file in sorted(capture_dir.glob('*.jsonl')):
        for line in file.read_text().splitlines():
            try:
                sample = json.loads(line)
            except ValueError:
                coverage['malformed_capture_lines'] += 1
                continue
            coverage['capture_errors'] += len(sample.get('errors', [])) + int('capture_error' in sample)
            coverage['unstructured_server_records'] += sample.get('server_journal_coverage', {}).get('unstructured', 0)
            for key, index in [('caddy_access', proxy), ('server_timing', server)]:
                if key in sample:
                    record = sample[key]
                    request_id = record.get('request_id')
                    if request_id:
                        coverage[key + '_duplicates'] += int(request_id in index)
                        index[request_id] = record
            if 'server_phase' in sample:
                record = sample['server_phase']
                phases[record.get('request_id')].append(record)
    durations = defaultdict(list)
    groups = defaultdict(lambda: defaultdict(list))
    protocols, outcomes, group_counts = Counter(), Counter(), Counter()
    requests = matched = server_matched = dropped = slow = 0
    runtime_missing = runtime_dropped = 0
    routes = {'shares', 'helper_status', 'delegate_vote', 'cast_vote', 'cast_vote_batch', 'delegate_and_cast_vote_batch', 'chain_status'}
    for directory in dict.fromkeys(run_dirs):
        runtime_file = directory / 'runtime-lag.json'
        if runtime_file.exists():
            runtime = json.loads(runtime_file.read_text())
            runtime_dropped += runtime.get('dropped', 0)
            durations['runtime_lag'].extend(value for item in runtime.get('samples', []) if (value := milliseconds(item.get('lag_us'), .001)) is not None)
        else:
            runtime_missing += 1
        # Include the designated immediate-share confirmation invocation too.
        for file in sorted(directory.glob('*.observability.json')):
            report = json.loads(file.read_text())
            dropped += sum(report.get(name, 0) for name in ['records_dropped', 'summary_updates_dropped', 'active_stages_dropped'])
            for record in report.get('records', []):
                diagnostic = record.get('http_diagnostics')
                if not diagnostic:
                    continue
                requests += 1
                protocol = diagnostic.get('protocol')
                protocol = protocol if protocol in ('h2', 'http/1.1', 'http/1.0', 'other', 'HTTP/1.1', 'HTTP/2.0', 'h1') else 'unknown'
                protocols[protocol] += 1
                route = diagnostic.get('route')
                route = route if route in routes else 'unknown'
                reused = diagnostic.get('connection_predates_request')
                connection = 'existing' if reused is True else 'new' if reused is False else 'unknown'
                group = '/'.join((route, protocol, connection))
                group_counts[group] += 1
                outcome = diagnostic.get('transport_error') or diagnostic.get('phase') or 'unknown'
                outcomes[outcome if outcome in ('connect', 'request', 'body_read', 'future_dropped', 'complete', 'awaiting_connection', 'awaiting_headers', 'reading_body', 'body_error') else 'unknown'] += 1
                timings = {}
                for source, name in [('response_headers_us','client_headers'), ('response_body_complete_us','client_complete'), ('elapsed_us','client_elapsed'), ('connection_acquired_us','connection_assignment'), ('connection_setup_us','connection_setup'), ('server_handler_us','handler'), ('server_body_read_us','body_read'), ('unattributed_wait_us','unattributed')]:
                    value = milliseconds(diagnostic.get(source), .001)
                    if value is not None:
                        timings[name] = value
                slow += int(timings.get('client_headers', 0) >= 2000)
                access = proxy.get(diagnostic.get('request_id'))
                if access:
                    matched += 1
                    for source, name, scale in [('duration','caddy',1000), ('upstream_headers_ms','upstream_headers',1), ('upstream_duration_ms','upstream',1)]:
                        value = milliseconds(access.get(source), scale)
                        if value is not None:
                            timings[name] = value
                ingress = server.get(diagnostic.get('request_id'))
                if ingress:
                    server_matched += 1
                    for source in ('duration_us', 'headers_us', 'body_read_us', 'response_write_us'):
                        value = milliseconds(ingress.get(source), .001)
                        if value is not None:
                            timings['server_' + source[:-3]] = value
                for phase in phases.get(diagnostic.get('request_id'), []):
                    name = phase.get('phase')
                    value = milliseconds(phase.get('duration_us'), .001)
                    if name in ('broadcast', 'status_lookup') and value is not None:
                        durations['rpc_' + name].append(value)
                # Compare matching requests only. These intervals have different
                # boundaries; a residual is not proof of network delay.
                if 'client_headers' in timings and 'upstream_headers' in timings:
                    difference = timings['client_headers'] - timings['upstream_headers']
                    if difference >= 0:
                        timings['headers_outside_upstream'] = difference
                    else:
                        coverage['incompatible_timing_boundaries'] += 1
                for name, value in timings.items():
                    durations[name].append(value)
                    groups[group][name].append(value)
    return {'requests':requests, 'proxy_matched':matched, 'proxy_missing':requests-matched,
            'server_matched':server_matched, 'server_missing':requests-server_matched,
            'capture_errors':coverage['capture_errors'], 'capture_summary_present':(capture_dir/'capture.json').exists(), 'coverage':dict(coverage),
            'dropped_observations':dropped, 'runtime_missing_runs':runtime_missing,
            'runtime_dropped_samples':runtime_dropped, 'headers_at_least_2s':slow,
            'protocols':dict(protocols), 'outcomes':dict(outcomes),
            'timings':{name:distribution(values) for name,values in sorted(durations.items())},
            'groups':{name:{'requests':group_counts[name], 'timings':{key:distribution(values) for key,values in timings.items()}} for name,timings in sorted(groups.items())},
            'interpretation':'Durations are measured on their own clocks. Residuals include uninstrumented client, proxy, and transport work; they do not isolate network latency. Missing or dropped records limit attribution.'}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--run',type=Path,action='append',required=True)
    parser.add_argument('--capture',type=Path,required=True)
    args = parser.parse_args()
    print(json.dumps(analyze(args.run,args.capture),indent=2))


if __name__ == '__main__':
    main()
