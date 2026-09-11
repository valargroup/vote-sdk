# REST request-body timeout contract v1

The vote API can return HTTP 408 with a non-broadcast receipt when its original
synchronous body read times out. It must return immediately without invoking
broadcast or scheduling asynchronous work. This applies to the delegation, cast,
batch, combined batch and reveal-share mutation routes. It also applies to
helper `POST /shares`: there, `dispatch: "not_started"` guarantees that this
attempt never invoked enqueue or scheduled work. Both the initial JSON decode
and the trailing-content check must finish before helper validation/enqueue.
A failure after enqueue, including a lost acceptance response, cannot emit this
receipt. Earlier attempts may already have enqueued the same share.

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
Helper regressions: `TestHelperIngressTimeoutNeverEnqueues` checks both decode
boundaries and invalid/missing tokens. `TestHelperRESTListenerStalledUploadReturnsBoundTimeout`
exercises both boundaries over TCP through the Cosmos REST listener.
`TestHelperLostAcceptanceResponseRemainsEnqueued` verifies a failed response
write cannot undo durable acceptance and an identical retry reports duplicate.
The shared `internal/restingress` writer preserves the chain receipt format.
Helper body-size, authentication, scheduling, and durable acceptance rules remain
unchanged. Roll out helper server support before enabling client recognition.

## Read deadline

New vote-sdk configurations default to a 30-second REST read deadline, increased
from the inherited 10 seconds. This gives large uploads more time to complete
through transient transport loss; it does not repair that loss. Both chain and
helper requests use this REST listener. Body-size limits remain enforced.

Existing nodes keep their explicit `app.toml` settings. To adopt the longer
window, set this value in the existing `[api]` section and restart the node:

```toml
[api]
rpc-read-timeout = 30
```

Despite the field name, this setting controls the Cosmos REST listener; it does
not change the separate consensus JSON-RPC listener. The deadline bounds request
reading, not proof generation or committed confirmation. Slow clients can hold
an upload connection longer, but the read deadline remains finite. Client-side
timeouts are independent and are not extended by this setting.
