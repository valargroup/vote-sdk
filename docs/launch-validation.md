# Launch Validation Harness

The launch-validation harness lives in `e2e-tests` and owns deterministic run
math, strict gate evaluation, and the self-contained HTML report for pre-launch
Shielded Vote scale runs.

## Generate a Report

Expected-only report:

```bash
cargo run --release --manifest-path e2e-tests/Cargo.toml --bin launch_validation -- \
  --spec e2e-tests/examples/launch-validation.loadtest.json \
  --output-dir artifacts/launch-validation
```

Report render smoke test with simulated observations:

```bash
cargo run --release --manifest-path e2e-tests/Cargo.toml --bin launch_validation -- \
  --spec e2e-tests/examples/launch-validation.loadtest.json \
  --simulate \
  --output-dir artifacts/launch-validation-simulated
```

Collect observations from a completed live run:

```bash
SVOTE_CHAIN_ID=svote-loadtest-1 \
cargo run --release --manifest-path e2e-tests/Cargo.toml --bin launch_validation -- \
  --spec e2e-tests/examples/launch-validation.loadtest.json \
  --collect \
  --round-id <hex-round-id>
```

Run the live launch executor:

```bash
SVOTE_CHAIN_ID=svote-loadtest-1 \
SVOTE_NODE_URL=tcp://localhost:26657 \
SVOTE_SSH_HOST=root@vote-chain-loadtest-primary.valargroup.org \
SVOTE_HOME=/root/.svoted \
VM_PRIVKEYS=<comma-separated-vote-manager-privkeys> \
HELPER_API_TOKEN=<loadtest-helper-token> \
cargo run --release --manifest-path e2e-tests/Cargo.toml --bin launch_validation -- \
  --spec e2e-tests/examples/launch-validation.loadtest.json \
  --execute \
  --yes \
  --output-dir artifacts/launch-validation-loadtest
```

Use `--dry-run-execute` before touching the network. It prints the bundle/vote
counts, expected tree delta, vote start/end timestamps, and helper acceptance
plan without creating a round or running SSH chaos. For the full 1K run, leave a
large setup buffer because the round id is only known after the create tx, and
ZKP #1/#2 proofs must bind to that emitted id:

```bash
cargo run --release --manifest-path e2e-tests/Cargo.toml --bin launch_validation -- \
  --spec e2e-tests/examples/launch-validation.loadtest.json \
  --dry-run-execute
```

Run the first live test with the smoke spec, then run the full 1K/4h spec after
the smoke report passes:

```bash
SVOTE_CHAIN_ID=svote-loadtest-1 \
SVOTE_NODE_URL=tcp://localhost:26657 \
SVOTE_SSH_HOST=root@vote-chain-loadtest-primary.valargroup.org \
SVOTE_HOME=/root/.svoted \
VM_PRIVKEYS=<comma-separated-vote-manager-privkeys> \
HELPER_API_TOKEN=<loadtest-helper-token> \
cargo run --release --manifest-path e2e-tests/Cargo.toml --bin launch_validation -- \
  --spec e2e-tests/examples/launch-validation.smoke.json \
  --execute \
  --yes \
  --output-dir artifacts/launch-validation-smoke
```

Actual run observations can also be written as `RunObservation` JSON and passed
with `--observations`. The harness then emits:

- `report.html` — self-contained semi-interactive report with tabs, sortable
  tables, filters, and raw JSON.
- `summary.md` — compact fallback for GitHub or Slack.
- `run.json` — full machine-readable report, including expected model, observed
  data, gates, and final status.

## Launch-Gate Defaults

The included loadtest spec targets:

- 5 validators and helper target count 3.
- 1,000 synthetic wallets over a 4-hour round.
- A mixed two-proposal ballot: one binary proposal and one 3-option proposal
  including Abstain.
- Tiered wallets covering dust drops, exact-threshold bundles, 5-note bundles,
  multi-bundle wallets, and a whale cohort.
- Burst-heavy timing with a final last-moment cohort.

The expected model also emits a per-bundle execution manifest in `run.json`:
wallet id, tier, note positions, proposal id, chosen option, timing cohort,
submit offset, global bundle index, eligible ZEC, and expected unique share
count. That manifest is the source of truth for comparing the live run against
the report.

Strict gates fail on finalization failure, tally timeout, validator-count
mismatch, commitment-tree delta mismatch, expected-vs-actual tally mismatch,
unexpected helper permanent failures, or unexpected run errors. Latency and
queue depth are reported but not gate-failing for the first launch gate.

The `--collect` path reads:

- `primary_api_url` for round status, commitment-tree size, finalized tally
  results, and vote-summary share counts.
- Every `vote_servers[].url` for private helper queue-status.
- `helper_api_token` from the spec, with `HELPER_API_TOKEN` as a local override.

## Isolated Infra And Reset

Use the `vote-infrastructure` `loadtest` environment for the 5-validator
side-network. The important Terraform inputs are:

```hcl
environment                  = "loadtest"
validator_count              = 5
full_stack                   = false
snapshot_spaces_bucket       = "vote-loadtest"
pir_voting_config_url        = "https://vote-loadtest.fra1.digitaloceanspaces.com/voting-config-loadtest.json"
pir_precomputed_base_url     = "https://vote.fra1.digitaloceanspaces.com"
pir_bootstrap_snapshot_height = 3317500
helper_api_token             = "loadtest-helper-token"
helper_expose_queue_status   = true
helper_max_concurrent_proofs = 16
```

Use a clean Terraform state for the side-network. The safe local pattern is:

```bash
cd vote-infrastructure
tofu init
tofu plan \
  -var-file=loadtest.private.tfvars \
  -state=loadtest.tfstate
tofu apply \
  -var-file=loadtest.private.tfvars \
  -state=loadtest.tfstate
```

Review the plan for only `vote-loadtest-*` DigitalOcean resources and
`*-loadtest` Cloudflare records before applying. Do not reuse a state file that
already manages pre-prod/prod.

`snapshot_spaces_bucket` can be a newly-created DigitalOcean Space, so genesis
and reset artifacts do not mix with pre-prod artifacts. With
`full_stack=false`, the loadtest Terraform stack omits snapshot and
explorer/archive hosts because they are not needed for vote, helper, tally, or
PIR query validation. Set `full_stack=true` only for a full production-shape
rehearsal that also validates snapshot publishing and archive/explorer
surfaces.

PIR hosts are part of the loadtest Terraform stack. For the first launch gate,
it is reasonable to let them bootstrap from the existing production/pre-prod
precomputed snapshot origin via `pir_precomputed_base_url`; that makes the
loadtest PIR servers query-ready without copying the heavy PIR snapshot. If we
want complete data isolation, create a second precomputed PIR Space and point
that variable at it. Keep `pir_voting_config_url` pointed at a loadtest-only
config so the PIR servers discover the loadtest vote servers and active round,
not pre-prod. Keep `pir_bootstrap_snapshot_height` and the launch spec
`snapshot_height` aligned with a published precomputed snapshot. Use a height
that also has `nullifiers.bin` if the run will include `pir-test load`; `3317500`
currently has both nullifiers and PIR tiers at
`https://vote.fra1.digitaloceanspaces.com/snapshots/3317500/manifest.json`.

The easy repeatable path is to publish that config into the `vote-loadtest`
Space:

```bash
cd vote-infrastructure
scripts/publish-loadtest-voting-config.sh \
  --domain valargroup.org \
  --bucket vote-loadtest \
  --region fra1 \
  --tfvars loadtest.private.tfvars
```

The script publishes a public
`https://vote-loadtest.fra1.digitaloceanspaces.com/voting-config-loadtest.json`
object with the five loadtest vote servers and PIR endpoints. Re-run it only if
loadtest DNS names, bucket, or region change. The Spaces key in
`loadtest.private.tfvars` must have write access to `vote-loadtest`; if the
upload returns `AccessDenied`, grant that key access to the Space or replace it
with a key from the same DigitalOcean team.

Run `Reset SDK Chain` from `vote-sdk` against the loadtest stack by passing a
single `network_config_json` override. The workflow keeps only two manual
inputs (`tag` and this JSON blob) so it stays under GitHub's
`workflow_dispatch` input limit:

```json
{
  "domain": "valargroup.org",
  "full_stack": false,
  "chain_id": "svote-loadtest-1",
  "spaces_bucket": "vote-loadtest",
  "spaces_region": "fra1",
  "genesis_object_key": "genesis.json",
  "primary_host": "vote-chain-loadtest-primary.valargroup.org",
  "secondary_host": "vote-chain-loadtest-secondary.valargroup.org",
  "primary_api_url": "https://vote-chain-loadtest-primary.valargroup.org",
  "secondary_api_url": "https://vote-chain-loadtest-secondary.valargroup.org",
  "extra_validator_hosts": "vote-chain-loadtest-val3.valargroup.org,vote-chain-loadtest-val4.valargroup.org,vote-chain-loadtest-val5.valargroup.org",
  "extra_validator_api_urls": "https://vote-chain-loadtest-val3.valargroup.org,https://vote-chain-loadtest-val4.valargroup.org,https://vote-chain-loadtest-val5.valargroup.org"
}
```

When `full_stack=false`, the workflow no-ops snapshot/archive quiesce, reset,
and verification jobs. For a full-stack rehearsal, set `full_stack=true` and add
`snapshot_host`, `snapshot_url`, `explorer_host`, and `explorer_api_url` fields.

The release binaries still come from the existing public release bucket. That is
separate from chain state, genesis, snapshots, and launch-test artifacts.

For local harness execution, source an ignored `.env.loadtest` file containing
the loadtest vote-manager key and remote signing settings. The reset workflow
uses `LOADTEST_VM_PRIVKEYS` only when `spaces_bucket` is `vote-loadtest`; prod
resets still use `VM_PRIVKEYS`.

## PIR Query Load

The launch harness validates the vote, helper, tally, queue, and validator
chaos path. It does not itself generate wallet-style PIR query traffic. Include
a separate PIR load run against the loadtest PIR fleet when PIR performance is
part of the launch evidence.

The repeatable CI path is the `vote-nullifier-pir` `Load test PIR` workflow with
the loadtest voting config:

```text
voting_config_url: https://vote-loadtest.fra1.digitaloceanspaces.com/voting-config-loadtest.json
snapshots_base: https://vote.fra1.digitaloceanspaces.com/snapshots
snapshot_height: 3317500
target: primary or backup
duration: 15m
concurrency: 8
```

For a local run, build `pir-test`, download the matching `nullifiers.bin`, and
target the loadtest alias directly:

```bash
cd vote-nullifier-pir
cargo build --release -p pir-test
curl -fL -o nullifiers.bin \
  https://vote.fra1.digitaloceanspaces.com/snapshots/3317500/nullifiers.bin
./target/release/pir-test load \
  --url https://pir-loadtest.valargroup.org \
  --nullifiers nullifiers.bin \
  --concurrency 8 \
  --duration 15m \
  --json-out pir-loadtest-summary.json
```

## Synthetic Notes

The launch harness does not fund 1,000 mobile wallets or depend on zodl for note
creation. It creates protocol-correct synthetic Orchard notes locally, one
delegation per eligible note bundle:

- Notes are generated from fresh spending keys.
- Each bundle contains at most 5 notes.
- Bundles below `12_500_000` zatoshi are dust and are excluded.
- Bundle weight is quantized down to the real `0.125 ZEC` divisor.
- A shared note-commitment tree is built from exactly those notes.
- The voting round is created with that tree's `nc_root`.
- Delegation proofs are built after the live chain emits the actual round id.

This gives us deterministic expected tally math without needing faucet activity
or TestFlight wallets. The live user-facing zodl flow remains covered by the
manual pre-production tests; this harness is specifically the launch-scale
protocol and fleet validation path.

The live executor creates one delegation per eligible bundle, submits all
delegations first, then casts proposals in proposal-id stages. That staging is
intentional: after the first proposal, the next proof for the same bundle must
spend the newly-created proposal-authority note, not the original delegation
leaf. Each stage uses a common on-chain tree anchor and preserves deterministic
tree positions for helper-share payloads.
