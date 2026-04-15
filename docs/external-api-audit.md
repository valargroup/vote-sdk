# External API Call Audit (ZCA-40)

This document audits outbound API/client calls across:

- `valargroup/vote-sdk` (this repository)
- `valargroup/librustvoting` (audited from upstream source)

Focus: identify where data leaves the process boundary and confirm whether private data is transmitted.

## Method

- Searched for outbound HTTP/gRPC usage in Go, TypeScript, and Rust code.
- Reviewed request payloads, endpoint paths, and call intent.
- Separated production/runtime paths from test-only harness calls.

---

## 1) vote-sdk outbound calls

### 1.1 Snapshot pipeline (`api/snapshot.go`)

| Call | Endpoint | Purpose | Data sent | Privacy note |
|---|---|---|---|---|
| PIR root query | `GET {PIRServiceURL}/root` (default `http://localhost:3000/root`) | Fetch nullifier IMT root + synced height for session snapshot | No body | No private data sent |
| Lightwalletd tree state | gRPC `CompactTxStreamer/GetTreeState` on configured lightwalletd URLs | Fetch block hash + Orchard frontier for `nc_root` derivation | Block height only | No private data sent |

Notes:
- Lightwalletd fallback defaults include public servers (for example `zec.rocks`).
- Orchard frontier is fetched for root derivation, not for secret export.

### 1.2 Chain broadcast/query paths (`api/handler.go`, `internal/helper/submit.go`)

| Call | Endpoint | Purpose | Data sent | Privacy note |
|---|---|---|---|---|
| Comet broadcast | `POST {CometRPCEndpoint}` JSON-RPC `broadcast_tx_sync` | Submit vote tx bytes | Protocol tx bytes | Intended on-chain payloads |
| Comet tx query | `POST {CometRPCEndpoint}` JSON-RPC `tx` | Confirm tx inclusion/status | Tx hash | No private data |
| Helper reveal submit | `POST {chainApi}/shielded-vote/v1/reveal-share` | Submit `MsgRevealShare` | share nullifier, encrypted share, proof, round metadata | Protocol reveal payload; no key/seed material |
| Round metadata query | `GET {chainApi}/shielded-vote/v1/round/{id}` | Read vote end time before helper submission | Round ID | No private data |

### 1.3 Admin/helper coordination (`internal/helper/pulse.go`, `internal/admin/monitor.go`)

| Call | Endpoint | Purpose | Data sent | Privacy note |
|---|---|---|---|---|
| Validator register | `POST {AdminURL}/api/register-validator` | Register helper server | Operator address, URL, moniker, timestamp, signature, pubkey | Identity/liveness metadata only |
| Heartbeat pulse | `POST {AdminURL}/api/server-heartbeat` | Keep approved server active | Same signed heartbeat payload | No private keys transmitted |
| Health probe | `GET {helperURL}/shielded-vote/v1/status` | Remove dead approved servers | No body | No private data |

### 1.4 UI/browser outbound calls (`ui/src/...`)

| Call group | Endpoint family | Purpose | Privacy note |
|---|---|---|---|
| Chain/UI queries | `/shielded-vote/*`, `/cosmos/*` | Chain state, ceremony, rounds, tx status | Standard protocol/admin reads |
| Tx broadcast from browser | `/cosmos/tx/v1beta1/txs` and follow-up tx polling | Submit and confirm signed tx bytes | Signing is client-side; private keys are not posted |
| Admin APIs | `/api/*` | Voting config + registration approvals/removals | Signed admin payloads, not key material |
| Nullifier APIs | `/nullifier/*` | Snapshot status + root queries | No private data |
| Blockchair stats | `https://api.blockchair.com/zcash/stats` | Estimate latest Zcash height/time in UI | No private data |

### 1.5 Telemetry

| Call | Endpoint | Purpose | Privacy note |
|---|---|---|---|
| Sentry SDK | DSN-configured host (when enabled) | Error/event reporting | No explicit secret-key transmission in audited code; operational errors/tags may include runtime context |

### 1.6 Test/tooling-only calls in vote-sdk

- `e2e-tests` and scripts make HTTP/grpcurl calls to local/dev services and optionally configured external services.
- These paths are non-production harness logic.

---

## 2) librustvoting outbound calls

### 2.1 Vote commitment tree sync client

| Call | Endpoint | Purpose | Data sent | Privacy note |
|---|---|---|---|---|
| Latest tree | `GET /shielded-vote/v1/commitment-tree/{round_id}/latest` | Sync VC tree state | Round ID | No private data |
| Root at height | `GET /shielded-vote/v1/commitment-tree/{round_id}/{height}` | Query historical root | Round ID + height | No private data |
| Leaves range | `GET /shielded-vote/v1/commitment-tree/{round_id}/leaves?...` | Incremental leaf sync | Round ID + height range | No private data |

Implementation: `vote-commitment-tree-client/src/http_sync_api.rs` (`HttpTreeSyncApi`, `reqwest::blocking`).

### 2.2 PIR proof fetch for ZKP1

| Call | Endpoint | Purpose | Data sent | Privacy note |
|---|---|---|---|---|
| PIR proof query | configured PIR server via `pir_client::PirClientBlocking` | Fetch nullifier exclusion proofs for delegation proving | Note nullifiers/proof query inputs | Protocol identifiers; no private key/seed material |

Implementation: `librustvoting/src/storage/operations.rs` (`build_and_prove_delegation`).

### 2.3 Helper payload handling in core crate

- `librustvoting` core builds helper payload structs (`SharePayload`) and stores delegation tracking metadata.
- Core crate does **not** directly POST helper share payloads at audited call sites; sending is handled by integrating apps/services.

### 2.4 Test/CLI-only paths in librustvoting

- `vote-commitment-tree-client` CLI/tests perform HTTP sync calls.
- `create_round_for_zashi` e2e test uses `grpcurl` to lightwalletd and HTTP to IMT server.
- These are non-production harness paths.

---

## 3) Concern check: VCT sync and frontier behavior

- VCT sync (`HttpTreeSyncApi`) performs state/leaves retrieval only.
- Snapshot path fetches Orchard frontier (`orchardTree`) from lightwalletd to compute `nc_root`.
- No audited path sends wallet private keys, seeds, or secret share randomness to external APIs.

This matches the expected model where sync logic updates tree/frontier state rather than exporting secrets.

---

## 4) Conclusion

- No direct evidence in audited production call paths of private key/seed exfiltration.
- Outbound traffic consists primarily of:
  - protocol state queries,
  - protocol tx payloads intended for chain processing,
  - signed validator/admin metadata,
  - PIR proof queries,
  - optional telemetry (Sentry),
  - optional external stats query (Blockchair UI helper).
