# YPIR Private Information Retrieval Audit Scope

**Date:** 2026-02-27

**Repository:** https://github.com/valargroup/Shielded-Vote

**Commit:** `eef9904f48693aaccf6c6eda5ce81a448196556f` (branch: `main`) TODO: Update this

---

## 1. Overview

Shielded Vote is a private voting protocol where voters cast ballots using zero-knowledge proofs. Each vote requires proving nullifier non-membership in an Indexed Merkle Tree (IMT) — this prevents double-voting without revealing the voter's identity. To prevent the server from learning *which* nullifier is being queried, we use **YPIR** — a single-server Private Information Retrieval scheme with silent preprocessing — to let the client fetch its Merkle authentication path without revealing which leaf it needs.

Specifically, we use the **YPIR+SP variant** (YPIR applied to SimplePIR), which retrieves large records (entire database column) rather than single elements. The implementation uses LWE-to-RLWE packing (CDKS-style) in the response path to reduce response size and client/server bandwidth costs.

This audit covers the **full PIR subsystem**: the upstream YPIR library, our server-side integration, and our client-side integration. The primary security goal is **client privacy** — the server must not be able to learn the queried nullifier from any observable behavior.

### Cryptographic Context

This request asks the auditor to validate the cryptographic assumptions and implementation details in the pinned `ypir` / `spiral-rs` versions. At a high level, the implementation uses:

- **RLWE/LWE-based constructions** used by YPIR and SimplePIR components
- **Packing/key-switching operations** used in the LWE-to-RLWE response packing path

The scheme uses two independent secret keys with different parameters:

| Parameter         | SimplePIR (Level 1) | Packing (Level 2)                         |
| ----------------- | ------------------- | ----------------------------------------- |
| Ring dimension    | d₁ = 1024           | d₂ = 2048                                 |
| Modulus           | q₁ = 2³²            | q₂ ≈ 2⁵⁶ (two 28-bit NTT-friendly primes) |
| Plaintext modulus | N = 2⁸              | p = 2¹⁴                                   |

## 2. Threat Model & Security Goals

### 2.1 Primary Goal: Client Privacy (Critical)

**It is absolutely unacceptable for the client to leak the queried nullifier to the server.** This is the single most important property of the system.

Client privacy must hold even if the server deviates from the protocol or actively attempts to extract the queried index. Privacy leakage could occur through:

- **Cryptographic weakness** in the YPIR scheme itself (query reveals row index)
- **Implementation bugs** in query generation or parameter setup (client-side)
- **Side channels** in the wire format, request timing, or request size that correlate with the queried index

### 2.2 Secondary Goal: Server-Side Correctness (Important)

The server should return correct data. Incorrect responses would cause proof generation to fail (detected downstream), so this is less critical than privacy, but still in scope:

- **Response correctness** — YPIR decryption should yield the exact row bytes
- **Data integrity** — tier export should faithfully represent the Merkle tree
- **Alignment / packing bugs** — the server uses `YServer::<u16>` with 14-bit packing; misuse corrupts decoded data

### 2.3 Out of Scope

- The zero-knowledge circuit that consumes the Merkle path (separate audit)
- The Cosmos SDK chain logic (session management, round lifecycle)
- The nullifier ingestion pipeline (`lightwalletd` sync into `nullifiers.bin` / checkpoint files)
- Denial-of-service resilience on the HTTP layer
- The iOS/Swift application layer (it calls Rust via FFI; the FFI boundary is not in scope)

## 3. System Architecture

```
┌───────────────┐        HTTPS         ┌────────────────┐
│  pir-client   │ ◄──────────────────► │  pir-server    │
│  (on device)  │                      │  (remote)      │
└───────┬───────┘                      └───────┬────────┘
        │                                      │
        │ uses ypir client API                 │ uses ypir server API
        │ (default-features=false)             │ (features=["server"])
        │                                      │
        ▼                                      ▼
┌────────────────────────────────────────────────────┐
│              ypir crate (artifact branch)          │
│  https://github.com/menonsamir/ypir @ b980152      │
├────────────────────────┬───────────────────────────┤
│  Client API            │  Server API (feat=server) │
│  - query generation    │  - YServer setup          │
│  - response decoding   │  - query answering        │
│  - key generation      │  - offline preprocessing  │
└────────────────────────┴───────────────────────────┘
```

### Tier Structure (11 + 7 + 8 Layers)

The nullifier tree is a sorted Indexed Merkle Tree (IMT) with ~50 million leaves at depth 26 (extended to depth 29 for the ZK circuit). To retrieve a full 26-sibling authentication path privately, the tree is split into three tiers. Each tier is a self-contained subtree that the client can binary-search locally after retrieval.

| Tier | Depth Range | Rows          | Row Contents                                                        | Row Size | Total Size | Retrieval         |
| ---- | ----------- | ------------- | ------------------------------------------------------------------- | -------- | ---------- | ----------------- |
| 0    | 0–10        | 1 (flat blob) | 2,047 internal node hashes + 2,048 subtree records (hash + min_key) | 192 KB   | 192 KB     | **Plaintext** GET |
| 1    | 11–17       | 2,048         | 126 internal nodes + 128 leaf records (hash + min_key) per row      | ~12 KB   | ~24 MB     | **YPIR** POST     |
| 2    | 18–25       | 262,144       | 254 internal nodes + 256 leaf records (key + value) per row         | ~24 KB   | ~6 GB      | **YPIR** POST     |

**Tier 0** is public and identical for all clients — it contains the top 11 layers of the tree. The client binary-searches the 2,048 subtree records to find which depth-11 subtree contains their nullifier, and extracts 11 sibling hashes directly from the internal nodes.

**Tier 1** rows each contain a complete 7-layer subtree rooted at depth 11. After decrypting the YPIR response, the client binary-searches the 128 leaf records within the row to identify the depth-18 sub-subtree, extracting 7 more sibling hashes from the internal nodes.

**Tier 2** rows each contain a complete 8-layer subtree rooted at depth 18. The client binary-searches 256 leaf records to locate the target key, extracts the final 8 sibling hashes, and computes exactly one hash (the sibling leaf) — the only client-side hash in the entire retrieval.

**Sentinel partitioning:** Seventeen sentinel nullifiers are inserted at positions k × 2²⁵⁰ (k = 0..16) to ensure all gap widths satisfy the ZK circuit's range constraints.

### Privacy Model

- **Tier 0** is identical for all clients (CDN-cacheable). The key distribution across 2,048 subtrees is public via the min_key fields, but this does not reveal individual queries.
- **Tiers 1 and 2** are retrieved via YPIR encrypted queries — the server cannot learn which row the client requested.
- The two YPIR queries are **sequential**: the Tier 2 row index depends on the Tier 1 result. The auditor should assess whether the combination of Tier 0 information and observable Tier 1/2 request properties (timing, size) allows narrowing beyond the 1-of-2,048 subtree granularity.

### Query Flow

1. Client fetches Tier 0 (plaintext, 192 KB) → binary-searches subtree records → identifies depth-11 subtree index `s1` → extracts 11 sibling hashes
2. Client generates YPIR query for Tier 1 row `s1` → server returns encrypted response → client decrypts and binary-searches 256 leaf records → identifies sub-subtree index `s2` → extracts 8 sibling hashes
3. Client generates YPIR query for Tier 2 row `(s1 × 256 + s2)` → server returns encrypted response → client decrypts and binary-searches 128 leaf records → locates target key → extracts 7 sibling hashes + computes 1 hash (sibling leaf)
4. All 26 siblings concatenated → extended to depth 29 with empty hashes → fed to ZK circuit

| Tier      | Binary Searches | Hashes Computed | Sibling Hashes Extracted |
| --------- | --------------- | --------------- | ------------------------ |
| 0         | over 2,048 keys | 0               | 11                       |
| 1         | over 256 keys   | 0               | 8                        |
| 2         | over 128 keys   | 1               | 7                        |
| **Total** | —               | **1**           | **26**                   |

## 4. YPIR Dependency

| Field           | Value                                                                        |
| --------------- | ---------------------------------------------------------------------------- |
| Package         | `ypir`                                                                       |
| Git URL         | https://github.com/menonsamir/ypir.git                                       |
| Branch          | `artifact`                                                                   |
| Pinned commit   | `b9801521301f34502496d694b2ac034857104ebc`                                   |
| Also depends on | `spiral-rs` from https://github.com/menonsamir/spiral-rs.git @ rev `f2c23c7` |

**Client-side features:** `default-features = false` (pure-Rust fallback, no AVX-512, stable toolchain)
**Server-side features:** `features = ["server"]`, optionally `explicit_avx512` for production

The entire `ypir` crate (and its `spiral-rs` dependency) is in scope. The auditor should review the YPIR library's cryptographic implementation, not only our usage of it.

## 5. Files in Scope

### 5.1 YPIR Upstream Library (External Dependency)

| Component  | Location                                            | Notes                                                 |
| ---------- | --------------------------------------------------- | ----------------------------------------------------- |
| ypir crate | https://github.com/menonsamir/ypir @ `b980152`      | Full crate — crypto, query gen, server answer, decode |
| spiral-rs  | https://github.com/menonsamir/spiral-rs @ `f2c23c7` | Lattice-based PIR primitives used by ypir             |

### 5.2 Client-Side Code (Privacy-Critical)

| File                                     | Description                                                                |
| ---------------------------------------- | -------------------------------------------------------------------------- |
| `nullifier-ingest/pir-client/src/lib.rs` | YPIR query generation, HTTP transport, response decryption, proof assembly |

This is the **most critical file** for the audit. Key areas:
- `generate_query_simplepir()` — how row index is encoded into the YPIR query
- `decode_response_simplepir()` — how the encrypted response is decoded
- Wire format construction (`[8: len][packed_query_row][pub_params]`)
- Whether request size, timing, or structure varies with the queried row index

### 5.3 Server-Side Code

| File                                      | Description                                                                    |
| ----------------------------------------- | ------------------------------------------------------------------------------ |
| `nullifier-ingest/pir-server/src/lib.rs`  | `TierServer` wrapping `YServer::<u16>`, `Aligned64` allocator, query answering |
| `nullifier-ingest/pir-server/src/main.rs` | Axum HTTP handlers, timing headers, endpoint routing                           |

Key areas:
- `u16` packing with `FilePtIter` (14-bit value packing)
- `Aligned64` custom allocator for AVX-512 alignment (64-byte)
- Whether any server-side logging or response metadata leaks query information
- Response timing side channels

### 5.4 Tier Export / Data Preparation

| File                                       | Description                                   |
| ------------------------------------------ | --------------------------------------------- |
| `nullifier-ingest/pir-export/src/lib.rs`   | Tree building, root extension (depth 26 → 29) |
| `nullifier-ingest/pir-export/src/tier0.rs` | Tier 0 export and binary search               |
| `nullifier-ingest/pir-export/src/tier1.rs` | Tier 1 row layout and export                  |
| `nullifier-ingest/pir-export/src/tier2.rs` | Tier 2 row layout and export                  |
| `nullifier-ingest/pir-export/src/main.rs`  | CLI entry point                               |

Key areas:
- Correctness of row layout (internal nodes + leaf records per row)
- Root extension from depth-26 to depth-29
- Whether row sizes are uniform (non-uniform sizes would leak information)

### 5.5 Integration / Host Binary

| File                                           | Description                                                  |
| ---------------------------------------------- | ------------------------------------------------------------ |
| `nullifier-ingest/nf-server/src/cmd_serve.rs`  | Production serve command (wraps pir-server, handles rebuild) |
| `nullifier-ingest/nf-server/src/cmd_export.rs` | Production export command                                    |

### 5.6 FFI Integration (Context Only)

These files show how `pir-client` is called from the wallet-side Rust code. Included for context, not primary audit targets:

| File                                      | Description                                 |
| ----------------------------------------- | ------------------------------------------- |
| `librustvoting/src/zkp1.rs`               | `PirImtProvider`, `convert_pir_proof()`     |
| `librustvoting/src/storage/operations.rs` | `PirClientBlocking::connect()`, batch fetch |

### 5.7 Tests

| File                                              | Description                                            |
| ------------------------------------------------- | ------------------------------------------------------ |
| `nullifier-ingest/pir-test/src/main.rs`           | E2E test harness (small, local, server, compare modes) |
| `nullifier-ingest/pir-export/tests/round_trip.rs` | Unit/integration tests for export + proof construction |

### 5.8 Cargo Manifests and Lockfiles (Dependency/Feature Pinning)

These files define enabled features and exact dependency resolution (including pinned `ypir` / `spiral-rs` revisions in the lock graph):

| File                                     | Description                                           |
| ---------------------------------------- | ----------------------------------------------------- |
| `nullifier-ingest/pir-client/Cargo.toml` | Client crate dependencies and feature configuration   |
| `nullifier-ingest/pir-client/Cargo.lock` | Resolved dependency graph for `pir-client`            |
| `nullifier-ingest/pir-server/Cargo.toml` | Server crate dependencies and feature configuration   |
| `nullifier-ingest/pir-server/Cargo.lock` | Resolved dependency graph for `pir-server`            |
| `nullifier-ingest/pir-export/Cargo.toml` | Export crate dependencies                             |
| `nullifier-ingest/pir-export/Cargo.lock` | Resolved dependency graph for `pir-export`            |
| `nullifier-ingest/nf-server/Cargo.toml`  | Production host binary dependencies and feature gates |
| `nullifier-ingest/nf-server/Cargo.lock`  | Resolved dependency graph for `nf-server`             |

### Estimated Total Lines of Shielded Vote Code

~6,500 lines of Rust in this in-repo scope (excluding Cargo manifests/lockfiles and excluding upstream `ypir` / `spiral-rs`).

## 6. Specific Areas of Concern

We request the auditor pay special attention to:

TODO: We should add any specific questions we have under the respective section here, or add another section if needed.

### 6.1 Query Indistinguishability (Critical)

- Our client currently constructs a fresh `YPIRClient` (and thus fresh keys s₁, s₂, packing key) for every individual query — Tier 1 and Tier 2 each get independent keys. Please verify that this is sufficient for privacy, and confirm there is no state leakage across queries via the `YPIRClient` construction or the underlying `spiral-rs` parameter setup.

### 6.2 YPIR Cryptographic Correctness (High)

### 6.3 Data Integrity (Medium)

## 7. Reference Material

- **Tier design spec:** "PIR-Efficient Merkle Path Retrieval" — our internal specification for the 3-tier architecture (https://valargroup.gitbook.io/shielded-vote-docs/appendices/pir-efficient-merkle-path-retrieval)
- **YPIR paper:** Menon & Wu, "YPIR: High-Throughput Single-Server PIR with Silent Preprocessing", USENIX Security 2024 (https://eprint.iacr.org/2024/270)
- **SimplePIR paper:** Henzinger et al., "One Server for the Price of Two: Simple and Fast Single-Server Private Information Retrieval" (https://eprint.iacr.org/2022/949)
- **spiral-rs:** Lattice-based PIR library underlying YPIR (https://github.com/menonsamir/spiral-rs)
