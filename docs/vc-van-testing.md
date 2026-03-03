# VC/VAN Determinism and Real-Proof Validation

This document describes the combined test strategy for VC (Vote Commitment) and
VAN (Vote Authority Note) sync safety in the Cosmos SDK implementation.

## Quick Start (Single Commands)

### Run a live-chain real-proof sync stress test locally

```bash
mise run test:sync-stress-real:local
```

This command will:
- start the local 3-validator chain if not already running
- wait for helper/API readiness on `http://localhost:1418`
- run `sync_stress` with real proofs against the local chain

### Run the pre-merge VC/VAN suite locally

```bash
mise run test:vc-van:premerge
```

This command runs:
- Tier 1 deterministic Go stress tests
- real-proof `sync_stress`
- Tier 2 real-proof consistency checks (mixed load: `sync_stress` + VC flow)
- Tier 3 real-proof crash/recovery checks (mixed load: `sync_stress` + VC flow)

Optional soak in same command:

```bash
RUN_SOAK=1 DURATION_MINUTES=60 mise run test:vc-van:premerge
```

When `RUN_SOAK=1`, premerge uses mixed Tier 4 load as well (`sync_stress` + VC flow).

## Local Setup Prerequisites

- `mise install`
- Rust toolchain and Go toolchain available via mise
- `zallyd` / `create-val-tx` built by `multi:start-chain` automatically

## Modes

- **Deterministic mode (fast signal):** high-signal consensus/restart/load checks.
- **Real-proof mode (production realism):** uses real ZKP delegation load via
  `e2e-tests/tests/sync_stress.rs` and validates multi-validator convergence.

## Edge-Case Coverage Matrix

| Edge case                              | Primary test(s)                                                                  | VC/VAN surface                                        |
| -------------------------------------- | -------------------------------------------------------------------------------- | ----------------------------------------------------- |
| High-volume append pressure            | `TestTreeStress_HighVolume`                                                      | VAN + VC append accounting (`+1` delegate, `+2` cast) |
| Same-seed determinism across runs      | `TestTreeStress_OrderingDeterminism`, `TestTreeStress_ReproducibilityAcrossRuns` | Root/`next_index` determinism                         |
| Gov-nullifier conflict race            | `TestTreeStress_NullifierRace`                                                   | VAN delegation nullifier spend rules                  |
| VAN-nullifier conflict race            | `TestTreeStress_NullifierRace`                                                   | VC path spend gate on VAN nullifier                   |
| Recheck churn after winner commit      | `TestTreeStress_RecheckTxChurn`                                                  | CheckTx/RecheckTx consistency for nullifier conflicts |
| No partial append on rejected tx       | `TestTreeStress_AnchorStaleness`, `TestTreeStress_RecheckTxChurn`                | Tree atomicity under failure                          |
| Stale/invalid/future anchor handling   | `TestTreeStress_AnchorStaleness`                                                 | VC cast anchor validation                             |
| Empty block snapshot behavior          | `TestTreeStress_EmptyBlocks`                                                     | Root-at-height correctness                            |
| Cold start tree-handle reset + replay  | `TestTreeStress_ColdStartRebuild`                                                | Restart root reconstruction                           |
| Long interleaved delegate/cast churn   | `TestTreeStress_Interleaved`                                                     | Continuous VAN consume/recreate + VC append           |
| Multi-validator per-height convergence | `test_tree_consistency.sh`                                                       | `(root,next_index,app_hash)` convergence across nodes |
| Crash/restart replay consistency       | `test_tree_crash_recovery.sh`                                                    | Post-restart catch-up determinism                     |
| Soak with rolling restarts             | `test_tree_soak.sh`                                                              | Drift detection under sustained load                  |
| Real-proof concurrent sync clients     | `sync_stress.rs`                                                                 | Incremental sync convergence + witness verification   |
| Real-proof VC cast/reveal/tally        | `voting_flow_librustvoting.rs`                                                   | Full VC lifecycle with helper and final tally         |

## Deterministic Mode Commands

### Tier 1 (in-process)

```bash
mise run test:tree-stress
```

### Tier 2 (multi-validator consistency)

```bash
TREE_LOAD_CMD="<load command>" mise run test:tree-consistency
```

### Tier 3 (crash/restart)

```bash
TREE_LOAD_CMD="<load command>" mise run test:tree-crash-recovery
```

### Tier 4 (soak)

```bash
TREE_LOAD_CMD="<load command>" mise run test:tree-soak
```

These scripts now require an effective load command by default. For deterministic
mode, provide `TREE_LOAD_CMD` explicitly. For real-proof mode, use the `:real`
tasks (they auto-resolve a load command). For snapshot-only debugging, you can
override this safety check with `ALLOW_EMPTY_LOAD=1`.

## Real-Proof Mode Commands

### Standalone real-proof sync stress

```bash
mise run test:sync-stress-real
```

If you want this against a local chain without manual prep, use:

```bash
mise run test:sync-stress-real:local
```

Optional knobs:

- `ZALLY_STRESS_DELEGATION_COUNT` (default `5`)
- `ZALLY_E2E_VOTE_WINDOW_SECS` (default `420`)
- `ZALLY_API_URL` (default `http://localhost:1418`)
- `HELPER_SERVER_URL` (default `http://localhost:1418`)

### Tier 2/3/4 with real-proof load

```bash
mise run test:tree-consistency:real
mise run test:tree-crash-recovery:real
mise run test:tree-soak:real
```

Mixed real-proof load variants (run `sync_stress` + `voting_flow_librustvoting`
in each load phase):

```bash
mise run test:tree-consistency:real:mixed
mise run test:tree-crash-recovery:real:mixed
mise run test:tree-soak:real:mixed
```

By default, the Tier 2/3/4 `:real` tasks use `sync_stress` as the load generator,
which is delegation-heavy and exercises VAN append/sync paths under real proofs.
For explicit real-proof VC (cast/reveal/tally) coverage, run:

```bash
cargo test --release --manifest-path e2e-tests/Cargo.toml --test voting_flow_librustvoting -- --nocapture --ignored --test-threads=1
```

Script-level knobs:

- `LOAD_MODE=real-proof` (set by the `:real` tasks)
- `REAL_PROOF_DELEGATION_COUNT` (default `5`)
- `REAL_PROOF_VOTE_WINDOW_SECS` (default `420`)
- `REAL_PROOF_API_URL` / `REAL_PROOF_HELPER_URL` (default `http://localhost:1418`)
- `ZALLY_HOME` / `ZALLY_NODE_URL` / `ZALLY_PALLAS_PK_PATH` for Cosmos tx signing
  context (auto-defaults to `val1` local paths when unset and `~/.zallyd-val1` exists)
- `REAL_PROOF_INCLUDE_VC_FLOW` (`0`/`1`, default `0`) to append `voting_flow_librustvoting`
  after `sync_stress` in each load phase
- `FAIL_ON_LOAD_ERROR` (`0`/`1`, default `1`) for soak behavior on load command failures

`TREE_LOAD_CMD` remains an explicit override for custom load generators.

## Current Scope Limits

- Tier 2/3/4 `:real` defaults are delegation-heavy (`sync_stress`) unless you set
  `REAL_PROOF_INCLUDE_VC_FLOW=1`, override `TREE_LOAD_CMD`, or run
  `voting_flow_librustvoting` explicitly.
- Multi-validator realism checks compare chain-exposed commitment-tree state and `app_hash`; they do not inject adversarial network partitions.
- These suites validate consensus/state determinism and sync correctness; they are not benchmark/perf throughput gates.

## Expected Artifacts

- Tier 2: `artifacts/tree-consistency/<timestamp>/per_height_diff.txt`
- Tier 2: `artifacts/tree-consistency/<timestamp>/per_height_snapshot.tsv`
- Tier 3: `artifacts/tree-crash-recovery/<timestamp>/replay_consistency.txt`
- Tier 3: `artifacts/tree-crash-recovery/<timestamp>/per_height_snapshot.tsv`
- Tier 4: `artifacts/tree-soak/<timestamp>/summary.json`
- Tier 4: `artifacts/tree-soak/<timestamp>/snapshots.tsv`

## Pass/Fail Criteria

The suite passes only when all of the following hold:

1. No `(height, root, next_index, app_hash)` mismatches across validators for sampled heights.
2. No partial append/nullifier writes on rejected tx paths (Tier 1 invariants).
3. No divergence after restart and catch-up at crash boundaries.
4. No nondeterministic failures in repeated seeded Tier 1 runs.
5. No consistency drift during soak with rolling restarts.
6. In real-proof mode: concurrent sync clients converge and witnesses verify.

## CI Intent

- **PR path:** deterministic VC/VAN stress (`test:tree-stress`) for fast signal.
- **main/workflow_dispatch/schedule:** real-proof sync stress plus real-proof VC flow
  (`voting_flow_librustvoting`), followed by multi-validator realism checks.

