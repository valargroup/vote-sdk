# Private Merkle-Path Retrieval via PIR

**Version:** 0.4
**Date:** 2026-03-09

How a client privately retrieves a 26-hash Merkle authentication path from a
sorted nullifier tree using two PIR queries — without revealing which key
it is looking up.

**Contents**

- [Background](#background)
- [Problem Statement](#problem-statement)
- [PIR Scheme: YPIR (SimplePIR)](#pir-scheme-ypir-simplepir)
- [Constants and Sizes](#constants-and-sizes)
- [Architecture: 11 + 7 + 8](#architecture-11--7--8)
- [Tier 0: Plaintext (Depths 0–11)](#tier-0-plaintext-depths-011)
- [Tier 1: PIR Query 1 (Depths 11–18)](#tier-1-pir-query-1-depths-1118)
- [Tier 2: PIR Query 2 (Depths 18–26)](#tier-2-pir-query-2-depths-1826)
- [Storage Summary](#storage-summary)
- [Bandwidth Summary](#bandwidth-summary)
- [Client Computation Summary](#client-computation-summary)
- [Row Serialization](#row-serialization)
- [Security Properties](#security-properties)
- [Open Questions](#open-questions)

---

## Background

This system is part of the [Zcash Shielded Voting](https://github.com/zcash/zips/pull/1198)
protocol. To cast a shielded vote, a client must prove that its note has
**not** been spent — i.e., the note's nullifier does not appear in the on-chain
nullifier set. This is a nullifier **non-membership** proof.

The server maintains an Indexed Merkle Tree (see [`imt-tree/`](../imt-tree/))
over ~51 million Zcash Orchard nullifiers. Each leaf commits to a **gap range**
between adjacent sorted nullifiers: `leaf = Poseidon(low, high)`. To prove
non-membership, the client shows its nullifier falls inside one of these gaps.

The proof is verified inside a zero-knowledge circuit (the delegation circuit),
which requires a 26-hash Merkle authentication path from leaf to root.
Downloading the entire tree (~6 GB) to find one path is impractical. Instead,
the client uses **Private Information Retrieval (PIR)** to fetch exactly the
path it needs, without the server learning which nullifier is being queried.

### Sentinel invariant

The circuit verifies gap widths using a 250-bit range check. To guarantee
every gap is less than 2²⁵⁰ wide, the tree includes **17 sentinel nullifiers**
at positions `k × 2²⁵⁰` for k = 0, 1, …, 16. These partition the Pallas
field (~2²⁵⁴) into segments that satisfy the constraint. The export process
injects these sentinels before building ranges and the tree. See
[`imt-tree/README.md`](../imt-tree/README.md) for details on the circuit
integration.

---

## Problem Statement

A server holds a Merkle tree over **N ≈ 51 million leaves** (≤ 2²⁶), each
committing to a gap range between sorted nullifiers. A client wants to
privately retrieve the Merkle authentication path for a given key inside a
key-range — the 26 sibling hashes needed to verify the leaf against the root —
without revealing which key it is querying.

The client could download all 51 million nullifiers and hash them locally, but
that would transfer ~6 GB and require ~100 million Poseidon hashes.
PIR reduces this to two small queries and negligible client-side hashing.

We use Poseidon as the hash function because authentication paths must be
verified inside a ZKP. It is relatively slow compared to SHA-256, so
minimising client hash count is a design goal. We achieve **1 hash** on the
client during the PIR phase (the ZKP circuit handles the remaining 26).

### Design target

Use **2 sequential PIR queries** plus a small plaintext payload to retrieve a
full 26-hash authentication path. No hash-map or ORAM overhead.

---

## PIR Scheme: YPIR (SimplePIR)

We use the SimplePIR mode of [YPIR](https://github.com/menonsamir/ypir)
(Menon & Wu, [ePrint 2024/270](https://eprint.iacr.org/2024/270.pdf),
USENIX Security 2024).

**Why YPIR+SP?** Classic SimplePIR is fast but requires a large database hint
that the client must download once per session. YPIR eliminates this hint via
silent preprocessing while retaining SimplePIR's low per-query bandwidth and
sub-second server processing. Our data regime (6 GB database, 12–24 KB
records) falls squarely into the "large record" setting from Section 4.6 of
the paper.

We evaluated InsPIR(e) but chose YPIR+SP for its lower implementation
complexity and better match to our record-size profile.

| Parameter | Value |
| --------- | ----- |
| Tier 1 server processing | ~0.5 s per query (AVX-512) |
| Tier 2 server processing | ~1.6 s per query (AVX-512) |
| Row payload (Tier 1) | 12,224 bytes |
| Row payload (Tier 2) | 24,512 bytes |

See [`docs/params.md`](params.md) for full YPIR lattice parameter derivation.

---

## Constants and Sizes

| Symbol | Value | Description |
| ------ | ----- | ----------- |
| K | 32 bytes | Key size (Pallas field element) |
| V | 32 bytes | Value size (Pallas field element) |
| H | 32 bytes | Hash output size (Poseidon) |
| L | 64 bytes | Leaf record: 32-byte key ‖ 32-byte value |
| D | 26 | Tree depth (root at 0, leaves at 26) |

A **leaf** is a 64-byte record: 32-byte key ‖ 32-byte value. The leaf hash is
`Poseidon(key, value)` and is not stored separately.

An **internal node** is a 32-byte hash: `Poseidon(left_child, right_child)`.

### Raw tree size

| Component | Count | Size each | Total |
| --------- | ----- | --------- | ----- |
| Leaves | 2²⁶ = 67,108,864 | 64 bytes | 4.00 GB |
| Internal nodes | 2²⁶ − 1 = 67,108,863 | 32 bytes | 2.00 GB |
| **Total** | | | **≈ 6.00 GB** |

---

## Architecture: 11 + 7 + 8

The 26-layer tree is split into three tiers:

```
Depth 0  ──────────────  root
  │
  │   TIER 0: Plaintext (11 layers)
  │   Depths 0–11
  │
Depth 11 ──────────────  2,048 subtree roots
  │
  │   TIER 1: PIR Query 1 (7 layers)
  │   Depths 11–18
  │
Depth 18 ──────────────  262,144 subtree roots
  │
  │   TIER 2: PIR Query 2 (8 layers)
  │   Depths 18–26
  │
Depth 26 ──────────────  leaves (up to 67,108,864)
```

Authentication path coverage:

| Tier | Siblings provided | Depths |
| ---- | ----------------- | ------ |
| Tier 0 (plaintext) | 11 | 1–11 |
| Tier 1 (PIR query) | 7 | 12–18 |
| Tier 2 (PIR query) | 8 | 19–26 |
| **Total** | **26** | **1–26** |

At each tier the client must learn:
- The sibling hashes along the path to the queried key
- The index to query at the next tier

For the leaf tier (Tier 2), the client also needs the full leaf data and its
sibling's data to compute the sibling hash locally.

---

## Tier 0: Plaintext (Depths 0–11)

### Payload

The server publishes a single binary blob containing two sections:

**Section 1 — Internal hashes (depths 0–10):**

All internal nodes from the root down to depth 10, in breadth-first order.

Count: 2⁰ + 2¹ + ⋯ + 2¹⁰ = 2¹¹ − 1 = 2,047 hashes × 32 bytes = **65,504 bytes**

**Section 2 — Subtree records at depth 11:**

Each record is an interleaved pair: 32-byte `hash` ‖ 32-byte `min_key`.

| Field | Size | Purpose |
| ----- | ---- | ------- |
| `hash` | 32 bytes | Merkle hash of the subtree rooted here |
| `min_key` | 32 bytes | Smallest key in this subtree (for binary search) |

Count: 2¹¹ = 2,048 records × 64 bytes = **131,072 bytes**

**Total Tier 0 payload: 65,504 + 131,072 = 196,576 bytes (≈ 192 KB)**

### Client procedure

1. **Binary search** the 2,048 `min_key` values in Section 2 to find subtree
   index `S₁ ∈ [0, 2047]` such that `min_key[S₁] ≤ target_key < min_key[S₁+1]`.

2. **Read 11 sibling hashes** directly from the blob:
   - Depth 11 sibling: read `hash` from Section 2 at index `S₁ XOR 1`.
   - Depths 1–10 siblings: walk the path determined by `S₁` upward through
     the BFS-indexed internal nodes in Section 1.

   Client hashing cost: **0** — all hashes are already in plaintext.

### Caching

This payload is identical for all clients and independent of the queried key.
It changes only when the tree is rebuilt (once per governance round). At 192 KB,
it can be served via CDN, cached locally, or even bundled in application source.

---

## Tier 1: PIR Query 1 (Depths 11–18)

### Database layout

| Property | Value | Derivation |
| -------- | ----- | ---------- |
| Rows | 2,048 | One per depth-11 subtree |
| Layers per row | 7 | Relative depths 1–7 |
| Content per row | Complete 7-layer subtree | See below |

Each row contains a complete subtree spanning depths 11–18. The subtree root
(the depth-11 node) is **not** included — the client already has it from Tier 0.

**Internal nodes** (relative depths 1–6, absolute depths 12–17):

| Relative depth | Count | Cumulative |
| -------------- | ----- | ---------- |
| 1 | 2 | 2 |
| 2 | 4 | 6 |
| 3 | 8 | 14 |
| 4 | 16 | 30 |
| 5 | 32 | 62 |
| 6 | 64 | 126 |
| **Total** | **126** | 2⁷ − 2 = 126 |

Internal node storage: 126 × 32 bytes = **4,032 bytes**

**Leaf records** (relative depth 7, absolute depth 18):

These are roots of Tier 2 subtrees. Each record is: 32-byte `hash` ‖ 32-byte
`min_key`.

Leaf count: 2⁷ = 128
Leaf storage: 128 × 64 = **8,192 bytes**

**Row total: 4,032 + 8,192 = 12,224 bytes**

### Database size

| Metric | Derivation | Result |
| ------ | ---------- | ------ |
| Raw | 2,048 × 12,224 | ≈ **23.9 MB** |

### Client procedure

1. Issue PIR query for **row S₁** (the subtree index from Tier 0).

2. **Binary search** the 128 `min_key` values at the bottom of the row to find
   sub-subtree index `S₂ ∈ [0, 127]`. Records are interleaved `(hash, min_key)`,
   so the search steps by stride 64 and reads `min_key` at byte offset
   `4,032 + i × 64 + 32`.

3. **Read 7 sibling hashes** directly from the row:
   - 1 sibling at depth 18: the leaf record at index `S₂ XOR 1` (its `hash` field)
   - 6 siblings at depths 12–17: walk the 126 internal nodes from position S₂
     upward, reading the sibling at each level

---

## Tier 2: PIR Query 2 (Depths 18–26)

### Database layout

| Property | Value | Derivation |
| -------- | ----- | ---------- |
| Rows | 262,144 | One per depth-18 subtree |
| Layers per row | 8 | Relative depths 1–8 |
| Content per row | Complete 8-layer subtree | See below |

The subtree root (depth-18 node) is **not** included — the client has it from
Tier 1.

**Internal nodes** (relative depths 1–7, absolute depths 19–25):

| Relative depth | Count | Cumulative |
| -------------- | ----- | ---------- |
| 1 | 2 | 2 |
| 2 | 4 | 6 |
| 3 | 8 | 14 |
| 4 | 16 | 30 |
| 5 | 32 | 62 |
| 6 | 64 | 126 |
| 7 | 128 | 254 |
| **Total** | **254** | 2⁸ − 2 = 254 |

Internal node storage: 254 × 32 bytes = **8,128 bytes**

**Leaf records** (relative depth 8, absolute depth 26 — the actual tree leaves):

Each record is: 32-byte `key` ‖ 32-byte `value`. No separate hash field; the
leaf hash is computed as `Poseidon(key, value)`.

Leaf count: 2⁸ = 256
Leaf storage: 256 × 64 = **16,384 bytes**

**Row total: 8,128 + 16,384 = 24,512 bytes**

### Empty-leaf padding

Partially-filled rows pad remaining entries with `key = p − 1` (the maximum
Pallas field element) and `value = 0`. Using the max field element ensures
padding sorts after all real leaves, preserving the sorted invariant for
binary search. Padding with `key = 0` would break the search because zero
sorts before real keys.

### Database size

| Metric | Derivation | Result |
| ------ | ---------- | ------ |
| Raw | 262,144 × 24,512 | ≈ **5.97 GB** |
| — leaf data portion | 262,144 × 16,384 | 4.00 GB (= all 2²⁶ leaves × 64 B) |
| — internal node portion | 262,144 × 8,128 | ≈ 1.97 GB (depths 19–25) |

### Client procedure

1. Compute the Tier 2 row index: `S₁ × 128 + S₂` (the absolute depth-18
   subtree index).

2. Issue PIR query for this row.

3. **Binary search** the 256 leaf keys to find the target key and retrieve its
   value. Records are interleaved `(key, value)`, so the search steps by
   stride 64 and reads `key` at byte offset `8,128 + i × 64`.

4. **Read 8 sibling hashes** from the row:
   - 1 sibling at depth 26: the leaf at index `target_position XOR 1`.
     Compute its hash as `Poseidon(key ‖ value)` — this is the **only hash
     the client computes** during the PIR phase.
   - 7 siblings at depths 19–25: read from the 254 internal nodes by walking
     upward from the target leaf position.

---

## Storage Summary

### Data stored across all tiers

| Data | Location | Size | Derivation |
| ---- | -------- | ---- | ---------- |
| Depths 0–10 internal hashes | Tier 0 | 65,504 B | (2¹¹ − 1) × 32 |
| Depth-11 hashes + keys | Tier 0 | 131,072 B | 2¹¹ × 64 |
| Depths 12–17 internal hashes | Tier 1 rows | 8,257,536 B | 2,048 × 126 × 32 |
| Depth-18 hashes + keys | Tier 1 rows | 16,777,216 B | 2,048 × 128 × 64 |
| Depths 19–25 internal hashes | Tier 2 rows | 2,130,706,432 B | 262,144 × 254 × 32 |
| Depth-26 leaves (key + value) | Tier 2 rows | 4,294,967,296 B | 262,144 × 256 × 64 |
| **Total** | | **≈ 6.02 GB** | |

The slight excess over the 6.00 GB raw tree comes from auxiliary `min_key`
fields stored alongside hashes in Tier 0 and Tier 1 for binary search.

### Server storage

| Database | Raw size | Notes |
| -------- | -------- | ----- |
| Tier 0 (plaintext) | 192 KB | Cacheable, public |
| Tier 1 (PIR) | 23.9 MB | Small enough for any PIR scheme |
| Tier 2 (PIR) | 5.97 GB | Binding constraint for scheme selection |
| **Total (raw)** | **≈ 6.02 GB** | |

Whether the server stores raw or padded rows depends on the PIR scheme. YPIR
operates on raw data and handles alignment internally via `FilePtIter` packing.

---

## Bandwidth Summary

Bandwidth is scheme-dependent. With YPIR+SP, the client downloads a
per-database hint once per session, then each query involves an encrypted
request and response. The dominant cost is YPIR ciphertext overhead, not the
plaintext row payload.

| Component | Direction | Size |
| --------- | --------- | ---- |
| Tier 0 payload | Server → Client | 192 KB |
| Tier 1 hint (one-time) | Server → Client | scheme-dependent |
| Tier 2 hint (one-time) | Server → Client | scheme-dependent |
| PIR Query 1 (round trip) | Both | scheme-dependent |
| PIR Query 2 (round trip) | Both | scheme-dependent |

Tier 0 can be cached across sessions since it only changes when the tree is
rebuilt.

---

## Client Computation Summary

| Step | Binary search | Hashes computed | Sibling hashes read |
| ---- | ------------- | --------------- | ------------------- |
| Tier 0 | Over 2,048 keys | 0 | 11 |
| Tier 1 | Over 128 keys | 0 | 7 |
| Tier 2 | Over 256 keys | 1 (sibling leaf) | 8 |
| **Total** | | **1** | **26** |

All internal node hashes are pre-computed and served directly: Tier 0 sends
depths 0–10 as plaintext; Tier 1 and Tier 2 rows contain pre-computed internal
hashes. The client computes exactly **1 Poseidon hash** total: the sibling
leaf hash in Tier 2, computed as `Poseidon(key, value)`. The ZKP circuit
verifies the full 26-hash authentication path against the public root.

---

## Row Serialization

### Tier 0 layout (196,576 bytes)

```
Bytes 0–65,503:        internal_nodes           2,047 × 32 B = 65,504 B
                       (BFS: 1 at depth 0, 2 at depth 1, ..., 1024 at depth 10)
Bytes 65,504–196,575:  subtree_records[0..2047]  2,048 × 64 B = 131,072 B
                       (each: 32-byte hash ‖ 32-byte min_key)
```

**Indexing:**

- Internal node at depth `d`, index `i`: byte offset = `((2^d − 1) + i) × 32`
- Subtree record at index `s`: byte offset = `65,504 + s × 64`
  - `hash` at `+0`, `min_key` at `+32`

### Tier 1 row layout (12,224 bytes)

Internal nodes in breadth-first order (depth 1 left-to-right, then depth 2,
…, depth 6), followed by interleaved leaf records:

```
Bytes 0–4,031:         internal_nodes[0..125]    126 × 32 B = 4,032 B
                       (BFS: 2 at depth 1, 4 at depth 2, ..., 64 at depth 6)
Bytes 4,032–12,223:    leaf_records[0..127]      128 × 64 B = 8,192 B
                       (each: 32-byte hash ‖ 32-byte min_key)
```

**BFS indexing** — node at relative depth `d`, position `p` (0-indexed):

- Internal node byte offset: `((2^d − 2) + p) × 32` for d ∈ [1, 6], p ∈ [0, 2^d)
- Leaf record byte offset: `4,032 + p × 64` for p ∈ [0, 128)
  - `hash` at `+0`, `min_key` at `+32`
- Sibling of position p: position `p XOR 1`
- Parent of position p: position `p >> 1` at depth `d − 1`

### Tier 2 row layout (24,512 bytes)

```
Bytes 0–8,127:         internal_nodes[0..253]    254 × 32 B = 8,128 B
                       (BFS: 2 at depth 1, 4 at depth 2, ..., 128 at depth 7)
Bytes 8,128–24,511:    leaf_records[0..255]      256 × 64 B = 16,384 B
                       (each: 32-byte key ‖ 32-byte value)
```

**Indexing:**

- Internal node byte offset: `((2^d − 2) + p) × 32` for d ∈ [1, 7], p ∈ [0, 2^d)
- Leaf record byte offset: `8,128 + i × 64` for i ∈ [0, 256)
  - `key` at `+0`, `value` at `+32`
- Empty leaf records use `key = p − 1` (max field element), `value = 0`

---

## Security Properties

- **Key privacy:** The server learns nothing about which key the client
  queries. Tier 0 is identical for all clients. Tier 1 and Tier 2 queries
  are protected by PIR.
- **Sorted-tree leakage:** Key boundaries (the `min_key` values in Tier 0) are
  public. This reveals the distribution of keys across 2,048 depth-11
  subtrees, but not which subtree any specific client queries.
- **No hash-map overhead:** The sorted tree enables binary search within each
  tier's plaintext data, eliminating the need for oblivious hash maps or
  cuckoo hashing.

---

## Open Questions

1. **Tree updates:** When leaves change, Tier 2 rows and all ancestor nodes
   are affected. Tier 1 rows change if any descendant leaf changes. Tier 0
   always changes. Incremental update cost depends on the PIR scheme's
   preprocessing model.

2. **Query sequentiality:** The two PIR queries are inherently sequential —
   Query 2's row index depends on Query 1's result. Pipelining is not possible
   without speculation (e.g., querying multiple candidate Tier 2 rows).

3. **Tier 2 row utilisation:** If the PIR scheme pads rows to a power-of-two
   boundary (e.g., 32 KB), usable utilisation is 24,512 / 32,768 = 74.8%.
   The spare bytes could store neighbouring subtree data as a guard band.

---

## References

- S. J. Menon and D. J. Wu.
  [YPIR: High-Throughput Single-Server PIR with Silent Preprocessing](https://eprint.iacr.org/2024/270.pdf).
  USENIX Security 2024.
- YPIR implementation: [github.com/menonsamir/ypir](https://github.com/menonsamir/ypir)
  (branch `artifact`).
- [PIR Parameter Selection](params.md) — YPIR lattice parameter derivation for
  this system.
- [Zcash ZIP Specification (PR)](https://github.com/zcash/zips/pull/1198) —
  Shielded voting protocol.
- [imt-tree crate](../imt-tree/README.md) — Indexed Merkle Tree with sentinel
  nullifiers and circuit integration.
