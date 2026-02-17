# Handoff: E2E Testing (`greg/e2e-test` branch)

## Goal

Get the full voting flow working end-to-end: chain → delegation (ZKP #1) → cast-vote (ZKP #2) → helper server (ZKP #3) → tally → finalize.

## What's been done this session

### Validated (passing)

- **Chain + ZKP #1 + #2 + #3 (direct) all work.** The old `voting_flow_librustvoting_path` test passed fully (489s, debug mode). Delegation, tree sync, VAN witness, vote commitment, cast-vote signing, share reveal, and tally all succeed against a local chain.

### Code changes (uncommitted, on `greg/e2e-test`)

Three files modified to add helper server integration to the e2e test:

1. **`e2e-tests/src/api.rs`** — Added `helper_server_url()` (defaults to `http://127.0.0.1:9091`) and `post_helper_json()` for POSTing to the helper server with retry logic.

2. **`e2e-tests/src/payloads.rs`** — Added `helper_share_payload()` that converts librustvoting's raw byte share payloads to the helper server's wire format (base64 for binary fields, hex for vote_round_id).

3. **`e2e-tests/tests/voting_flow_librustvoting.rs`** — Rewrote to be the **canonical e2e test**:
   - Steps 1-9: Same as before (create session, delegate, cast vote, build share payloads)
   - Step 10: **Now sends all 4 shares to the helper server** instead of generating ZKP #3 directly
   - Steps 11-14: **Added tally verification** (verify ciphertext, wait for TALLYING, wait for FINALIZED, query tally results)
   - Removed direct ZKP #3 path (that's still covered by `voting_flow.rs`)
   - Removed unused imports (`orchard::share_reveal::*`, etc.)

### What failed and why

The updated test failed at step 10 with a **connection refused** error to `http://localhost:9090/api/v1/shares`. The helper server was not running when the test reached that step. Steps 1-9 all passed cleanly.

## How to run

### Prerequisites — three services needed

```bash
# Terminal 1: Chain (port 1318)
cd sdk && make init && make start

# Terminal 2: Helper server (port 9091) — MUST use --release for ZKP #3 perf
cd helper-server && cargo run --release --bin helper-server -- \
  --tree-node http://127.0.0.1:1318 \
  --chain-submit http://127.0.0.1:1318 \
  --min-delay 1 --max-delay 3 \
  --db-path :memory:

# Terminal 3: Run the test
cargo test --release --manifest-path e2e-tests/Cargo.toml \
  voting_flow_librustvoting_path -- --nocapture --ignored
```

### Environment variables

| Variable | Default | Purpose |
|----------|---------|---------|
| `ZALLY_API_URL` | `http://127.0.0.1:1318` | Chain REST API URL |
| `HELPER_SERVER_URL` | `http://127.0.0.1:9091` | Helper server URL |
| `ZALLY_EA_PK_PATH` | `$HOME/.zallyd/ea.pk` | EA public key (only for `voting_flow.rs`) |

## What needs to happen next

### Immediate: Get the helper server e2e test passing

1. Start all three services (chain, helper server in release mode, then test)
2. Run the test and debug any issues
3. The most likely issues:
   - **Helper server tree sync**: The helper server needs to sync the commitment tree from the chain. With `--tree-node http://127.0.0.1:1318`, it pulls from `/zally/v1/commitment-tree/leaves`. Verify the helper server logs show successful tree sync after cast-vote commits.
   - **Share payload wire format**: The helper server expects `vote_round_id` as hex (64 chars), `shares_hash`/`c1`/`c2` as base64. The `helper_share_payload()` function in payloads.rs handles this conversion. If the helper server rejects payloads, check its logs for validation errors.
   - **ZKP #3 proof generation**: The helper server generates ZKP #3 for each share. In release mode this takes ~30-60s per share. The test has a 300s timeout waiting for tally to appear. With `--min-delay 1 --max-delay 3`, all 4 shares should be processed within ~5 minutes.
   - **Auto-tally**: After all shares are submitted and the voting window expires, the chain's `PrepareProposal` auto-injects `MsgSubmitTally`. The EA secret key (`~/.zallyd/ea.sk`) must exist. The voting window is ~120s (set by `create_voting_session_payload` with `expires_in_sec: 120`).

### After test passes: Commit and update handoff

- Commit the changes on `greg/e2e-test`
- The test serves as the **canonical reference implementation** for the Zashi voting flow
- The only shortcuts vs production: mocked nullifier service (synthetic in-memory IMT) and test-generated EA keypair

### Then: Zashi integration

The e2e test mirrors what Zashi needs to do. Key integration points for the iOS app:

1. **Helper server URL config** — `VotingAPIClientLiveKey.swift` currently POSTs shares directly to `/zally/v1/reveal-share`. Needs to POST to the helper server's `/api/v1/shares` instead.
2. **Wire format** — The helper server expects a different JSON shape than the chain endpoint. See `helper_share_payload()` in payloads.rs for the exact format.
3. **Round ID** — `VotingStore.swift` needs `vote_round_id` populated before the flow starts. Currently a config gap (blocker B1 in the ZKP #2 handoff doc).
4. **`vcTreePosition`** — After cast-vote TX, the app needs to read the commitment tree to get the new leaf position for the share payloads.

## Key files

| File | Role |
|------|------|
| `e2e-tests/tests/voting_flow_librustvoting.rs` | **Canonical e2e test** (this is what we're getting working) |
| `e2e-tests/tests/voting_flow.rs` | Low-level circuit test (direct ZKP #3, no helper server) |
| `e2e-tests/src/api.rs` | HTTP client helpers (chain + helper server) |
| `e2e-tests/src/payloads.rs` | JSON payload builders for all endpoints |
| `e2e-tests/src/setup.rs` | Delegation bundle builder (synthetic, no real chain data needed) |
| `helper-server/src/processor.rs` | Helper server ZKP #3 pipeline |
| `helper-server/src/types.rs` | Helper server wire format (SharePayload, EncryptedShareWire) |
| `docs/handoff-zkp2-tx2-wiring.md` | Previous handoff (ZKP #2 + delegation wiring) |

## Architecture diagram

```
E2E Test                    Chain (1318)           Helper Server (9091)
────────                    ────────────           ──────────────────
create-voting-session ───► POST /create-voting-session
delegate-vote (ZKP#1) ───► POST /delegate-vote
                           ◄── tree updated (VAN leaf)
sync tree ────────────────► GET /commitment-tree/leaves
build VAN witness
build vote commitment (ZKP#2)
sign cast-vote
cast-vote ────────────────► POST /cast-vote
                           ◄── tree updated (+2 leaves)
build share payloads
POST 4 shares ──────────────────────────────────► POST /api/v1/shares
                                                  │ sync tree from chain
                                                  │ generate VC witness
                                                  │ derive share nullifier
                                                  │ generate ZKP #3 (30-60s)
                           ◄── POST /reveal-share ┘
                           (×4 shares, with delay)
wait for tally ───────────► GET /tally/{round}/1
wait for TALLYING ────────► GET /round/{id}
wait for FINALIZED ───────► GET /round/{id}
                           (auto-tally via PrepareProposal)
verify results ───────────► GET /tally-results/{id}
```
