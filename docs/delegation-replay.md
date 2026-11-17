# Replaying rejected delegation proofs

Rejected transactions do not enter chain history. Optional local capture retains
the proof and the exact named inputs passed to the delegation verifier, including
the note-tree and nullifier-tree roots loaded from round state. This lets an
operator reproduce a rejection without contacting a node or recovering a wallet.

## Enable capture

Use a real Halo2 build. Capture is disabled by default, including for existing
configuration files that omit the setting. To enable it on the selected node,
set this in the existing `[vote]` section of `<node-home>/config/app.toml`:

```toml
[vote]
delegation_replay_capture = true
```

On the next start, capture writes under `<node-home>/debug/`. The node home is
the directory selected by `--home` (normally `~/.svoted`), so the default location
is `~/.svoted/debug/`. There is no separate capture-directory flag.

The directory is created with mode `0700`; an existing directory must already
have that mode. Bundles have mode `0600`. A process lock prevents two writers.
Do not share the directory with other data or add files to it. An unknown file,
symlink, or non-private file causes capture storage to refuse writes. Disabling
capture requires setting `delegation_replay_capture = false` and restarting the
node. No deployment is enabled by this change.

Only delegation proof failures reached after signature validation are eligible,
including the delegation inside a combined delegation/cast transaction. Successful
proofs, signature failures, and lightweight mempool rechecks are not captured.
CheckTx and FinalizeBlock observations are deduplicated by the proof, exact named
inputs, schema, and executable hash, independently of transaction signatures.

Fixed limits are eight queued bundles, one new admission per minute with a burst
of five, 64 KiB per bundle, 100 stored bundles, 10 MiB of bundle storage including
the temporary write, and 24-hour retention. Oldest files are evicted before a new
write. Retention runs at startup, before writes, and every minute while enabled;
expiry can lag by one cleanup interval. If the node is stopped or capture is
disabled, files remain until capture resumes or an operator deletes them.

Admission uses a nonblocking lock and queue. Queue pressure, contention, write
errors, and limits drop diagnostics without changing the original validation
error, gas accounting, events, or chain state. A background worker writes a
temporary file and atomically renames it. These files are best-effort diagnostics,
not a durable transaction journal; a crash or shutdown can lose pending evidence.

## Bundle contents and handling

The versioned JSON bundle contains base64 proof bytes and the named Go verifier
inputs (byte slices are base64), capture time, execution phase, chain ID, state
height, transaction hash when available, original verifier error, binary version,
commit, Go build information, and SHA-256 of the executable. The executable hash
also identifies its statically linked Rust verifier. Preserve that executable
for exact replay; source-equivalent rebuilds are explicitly comparisons.

The bundle does not retain the complete transaction, signature, request headers,
client identifiers, or private circuit inputs. Verifier-visible inputs can still
link voting activity: keep bundles restricted and do not attach them to Sentry or
public issues. Logs expose only capture IDs and fixed status messages. The checksum
detects accidental modification; it is not authentication against a malicious editor.

`svote_delegation_replay_total{outcome="..."}` reports queued, written, duplicate,
busy, rate_limited, queue_full, invalid_or_oversize, stopped, write_error, and
cleanup_error outcomes. These are diagnostic outcomes, not unique-user counts.

## Offline replay

Copy a bundle and the original executable to a restricted machine of the same
OS/architecture. No chain home, RPC endpoint, wallet, or signing key is needed:

```sh
svoted debug replay-delegation /restricted/capture.json
```

The command checks the schema, size, checksums, and executable identity before
calling the real Go-to-Rust verifier with the saved inputs. Mock builds refuse to
run. Results distinguish the same rejection, a different rejection, and unexpected
verification success. The latter two return an error exit status.

To deliberately test another binary:

```sh
svoted debug replay-delegation /restricted/capture.json --compare
```

Different-executable results are prefixed `comparison:`. Replay does not recreate
the full signature, round-liveness, or nullifier checks and does not name a failed
private circuit condition. It answers whether this exact proof verifies with the
captured server inputs through that binary's conversion and verifier path.
