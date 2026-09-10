# Staging helper latency diagnostics

These diagnostics are optional and restricted to staging. They do not certify
multi-voter capacity. No multi-client load runner is included.

## Capture boundaries

With `SENTRY_ENVIRONMENT=staging` and `SVOTE_HELPER_DIAGNOSTICS=1`,
observed HTTPS POSTs to the two exact staging helper hostnames in `zcash_voting` generate an independent random
128-bit `X-Vote-Request-ID`. The `helper.http.transport` observation carries
`http_diagnostics`, an additive optional object; old observation JSON remains
readable. Unobserved requests do not acquire this header. Cancellation, deadline,
dispatch classification, and payload handling remain unchanged.

| Diagnostic | Boundary |
|---|---|
| `connection_acquired_us` | Handoff until Hyper assigns a connection, including pool wait and new connection setup. Missing if headers win the observation race. |
| `connection_setup_us` | Assigned connection's connector call through establishment, including DNS/TCP/TLS; excludes pool/readiness wait. Custom connectors without timing metadata leave this absent. |
| `connection_predates_request` | Whether the connection was established before this request's handoff. This identifies already-established connections without recording addresses. |
| `response_headers_us` | Client handoff until response headers; includes transport and server waiting. |
| `server_started_unix_us` | Server route entry before the Sentry wrapper, from the matching response. |
| `server_handler_us` | Server route entry through first response-header write, not socket delivery. |
| `unattributed_wait_us` | Client header duration minus matching server duration. Not a pure network measurement. Absent for missing/inconsistent timing. |

Server response timings are untrusted diagnostic inputs. They never determine
acceptance, retry, or health. The server echoes and logs only a valid 32-hex
token. Logs contain the fixed route `shares`, method, protocol, status, and
timings; no payload, raw URL, cookie, API token, or voter identity is added.

The Caddy overlay records request duration and reverse-proxy header latency,
using the same ID. Caddy's `ts` is completion time; subtract `duration` to
estimate its start. Comparing absolute client and server times requires checking
clock offset. Comparing durations does not.

## Queue and runtime instrumentation

Set both `SENTRY_ENVIRONMENT=staging` and `SVOTE_HELPER_DIAGNOSTICS=1`
on the helper service before starting it. Both conditions are required. Outside
this configuration, no new correlation headers/logs or store metric families
are enabled, and no queue metric scans run. Configuration is read at construction;
restart the helper through the normal staging operations workflow to change it.

| Metric | Meaning |
|---|---|
| `svote_helper_store_lock_wait_seconds{operation}` | Wait for the store mutex. |
| `svote_helper_store_lock_held_seconds{operation}` | Time inside the critical section. |
| `svote_helper_store_sql_write_seconds{statement,result}` | Nontransactional SQL write time, including connection-pool wait; statements are bounded insert/update/delete/other classes. |
| `svote_helper_scheduler_scan_seconds{operation}` | Next-due scan or ready candidate selection, excluding mutex wait. |
| `svote_helper_queue_depth{state}` | Authoritative ready, not-yet-due, processing, and aggregate pending counts; terminal rows excluded. |
| `svote_helper_queue_oldest_ready_age_seconds` | Age of the oldest effective queued due time; retry backoff is respected. |
| `svote_helper_enqueue_to_confirmation_seconds` | Receipt through observed chain confirmation, including intentional future delay. Missing/invalid legacy receipt timestamps are omitted. |

Queue collection performs one O(pending) in-memory scan per scrape under the
store lock and does no SQL. Account for that cost in large-backlog experiments.
Confirmation timing uses persisted receipt time across restart and is observed
only after a successful terminal transition; repeated completion cannot double
count. No timestamp changes queue ordering or share eligibility.

Set `SVOTE_HELPER_DIAGNOSTIC_PROFILES=1` on an explicitly staged diagnostic
process with the two switches above to sample one in 100 mutex contention
events and approximately one blocking event per 10 ms blocked. Both are off by
default. The existing localhost pprof listener serves the profiles. CPU
profiling is activated only by an explicit profile request.

## Staging procedure

1. Build/test the instrumented client and helper. Deploy the helper changes
   through the normal release/deploy workflow. Caddy capture works with older
   helpers, but their per-request server headers and new queue metrics are absent.
2. Copy `deploy/helper-diagnostics.Caddyfile` and
   `scripts/helper-diagnostics-caddy.py` to each staging helper. Run the script
   with `--snippet <path>` to prepare and validate a candidate, then add `--apply`.
   It restricts itself to `vote-primary-stage` and `vote-secondary-stage`, keeps
   listeners/backends unchanged, and refuses to replace existing access logging.
   The script prints protected candidate and rollback paths, never config contents.
3. Collect around the benchmark from this repository:

   ```sh
   python3 scripts/collect-helper-diagnostics.py --seconds 180 --out /tmp/helper-capture
   ```

   Run this alongside the benchmark. Add `--profiles` for CPU, goroutine, mutex,
   and block profiles. SSH stays open during collection; metrics and `/proc`
   counters are sampled on the hosts each second. Collection time and errors
   are recorded. No public metrics or profiling port is opened. Caddy journal
   records for the capture window are projected to a fixed safe field list.
4. In the active `zcash_voting` checkout, use the normal staging credentials:

   ```sh
   infisical run --projectId=40862c6d-a089-4355-b405-0477be0ee3b1 --env=staging -- \
     env SENTRY_ENVIRONMENT=staging SVOTE_HELPER_DIAGNOSTICS=1 \
     make stage-bench STAGE_BENCH_ARGS='run --proposals 37 --helpers 2'
   ```

   Compare the default with `--separate-helper-pool`, `--http1-only`, and
   `--warm-helper-connections`. Every choice is persisted in `run-config.json`.
   Run three alternating repetitions per 32/50 admission setting, using pinned
   binaries built from otherwise identical source. Do not infer protocol from
   a flag: use the observed negotiated protocol.
5. Join the request records:

   ```sh
   python3 scripts/analyze-helper-diagnostics.py \
     --run /path/to/client/run --capture /tmp/helper-capture
   ```

   Missing proxy matches, failed scrapes, dropped observations, and missing
   server timings are explicit. Never subtract population percentiles to infer
   a phase duration; use matched requests or separate distributions.
6. Restore Caddy when the experiment ends:

   ```sh
   curl --fail --silent --show-error -H 'Content-Type: application/json' \
     --data-binary @/protected/path/from/rollback-output.json http://127.0.0.1:2019/load
   ```

The overlay is a runtime diagnostic change, not a persistent Caddyfile or
Terraform change. A normal service restart restores its configured Caddyfile.
The filtered records use journald retention. Keep captures private and retain
the configuration/revision and background queue load with each result.

## Expanded application and chain capture

The staging request-ID wrapper covers helper shares and status, chain delegate/cast
submissions (including batch routes), and transaction-status lookups. It records
router entry, handler entry, body completion, first response headers, response
writes, and local CometBFT broadcast/status calls. `body_read_us` is elapsed time
from router entry until the body is consumed, not accumulated time inside Read.
All diagnostics still require both staging environment switches and a valid ID.

The collector includes `server_timing` and `server_phase` events projected from JSON or console
journals to fixed safe fields. It does not export other application log messages,
request bodies, transaction hashes, URLs, or error text. The analyzer reports
application correlation coverage and phase distributions alongside proxy timing.

To replace an existing diagnostic Caddy overlay, supply both
`--replace-candidate <previous-candidate.json>` and
`--base-config <original-before-overlay.json>`. The script refuses replacement
unless live configuration exactly matches that candidate, validates the new
configuration, and saves the currently active overlay as the rollback target.

## Correlated report and coverage

The collector exports only whitelisted structured `vote HTTP timing` and
`vote HTTP phase` records from the `svoted` journal. Configure the diagnostic
server with JSON logging. If it emits text logs, the collector counts those
unstructured diagnostic records but does not copy their contents or attempt
unsafe free-text parsing. Server redeployment and the two staging diagnostic
environment flags are required for these records. Caddy configuration is separate.

The analyzer includes every `*.observability.json` invocation in each `--run`
directory, including immediate-share confirmation. It joins proxy and server
records by random request ID, reports missing correlations and duplicate records,
and groups request timings by normalized route, negotiated protocol, and whether
the connection predates the request. IDs, raw paths and payloads are not included
in the summary. Repeated `--run` arguments support multiple benchmark directories.

`runtime-lag.json` is written by the client on normal completion. Missing files,
dropped runtime samples, malformed capture lines, scrape errors and dropped
observations are explicit in the report. A missing `capture.json` indicates the
collector did not write its completion summary; wait for collection to finish
before analyzing. Journal records are exported after the sampling window, so an
interrupted collector may have metrics but no request records.

Times are elapsed durations on each component's own clock. The per-request
`headers_outside_upstream` residual compares client response-header time against
Caddy upstream-header time. It includes connection acquisition, proxy and
transport work outside that upstream interval; it must not be labeled network
latency. Negative differences are counted as incompatible timing boundaries.
Application response-write duration measures writes to the HTTP implementation,
not receipt by the peer. The report does not prove clock synchronization or
attribute a latency spike merely because it overlaps a chain transaction.

Run the hermetic tooling checks with:

```sh
python3 -m unittest discover -s scripts -p test_helper_diagnostics.py
```
