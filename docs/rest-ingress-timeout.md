# REST request-body timeout contract v1

The vote API can return HTTP 408 with a non-broadcast receipt when its original
synchronous body read times out. It must return immediately without invoking
broadcast or scheduling asynchronous work. This applies to the delegation, cast,
batch, combined batch and reveal-share mutation routes, not helper placement.

Clients opt in with one `X-Vote-Ingress-Attempt-V1` header containing a fresh
32-byte lowercase-hex token for each HTTP attempt. A valid receipt is:

```json
{"error":{"version":1,"code":"request_body_timeout","dispatch":"not_started","attempt":"<exact request token>"}}
```

Responses have JSON content type and `Cache-Control: no-store`. Old clients,
missing/duplicate/invalid tokens, and non-timeout read errors retain the generic
error response. This receipt proves nothing about previous requests. Clients
must preserve prior uncertainty, signed payloads, retry budgets and backoff.
Unknown schemas, generic 408s, incomplete responses and connection failures
remain uncertain. The token is unrelated to the staging diagnostic request ID
and is not an idempotency key. Do not use it as a metric label.

This is a contract with the configured trusted HTTPS ingress. A token prevents
stale response reuse; it does not stop a malicious gateway from lying. Proxies
must pass through these responses, not synthesize them after upstream dispatch,
cache them, or automatically replay mutation POSTs. Keep proxy retries disabled
for these routes. Check the deployed Caddy configuration before rollout.

## REST listener dependency

The Cosmos fork change in [cosmos-sdk #5](https://github.com/valargroup/cosmos-sdk/pull/5)
disables JSON-RPC batch preprocessing on the REST API listener. This repository
pins its immutable commit through the existing Cosmos SDK replacement in
`go.mod`; no local patch or workspace override is required. The listener still
applies MaxBytesReader and configured read/write deadlines. Consensus JSON-RPC
batch limits remain unchanged.

`TestIngressTimeoutNeverBroadcasts` covers all five mutation routes, token
validation, and non-timeout read failures.
`TestRESTListenerStalledUploadReturnsBoundTimeout` sends a partial body over a
real TCP connection through Cosmos API.Server.Start, waits for the configured
read deadline, requires the matching 408, and asserts zero broadcasts. It also
checks that oversized uploads still hit the body-size limit.

Roll out server support first. Verify a controlled incomplete upload through
Caddy returns the complete matching receipt with zero broadcast calls; verify
normal requests and generic proxy errors. Then release client recognition.
No timeout extension or concurrency change is part of this contract.
