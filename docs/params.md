# PIR Parameters

How YPIR cryptographic parameters are chosen, where they live in the
codebase, and how they flow from tree constants to lattice parameters.

**Contents**

- [Overview](#overview)
- [Tree-layout constants](#tree-layout-constants)
- [Scenario construction](#scenario-construction)
- [YPIR lattice parameters](#ypir-lattice-parameters)
- [Client-side reconstruction](#client-side-reconstruction)
- [Upstream reference](#upstream-reference)

---

## Overview

Parameters flow through four stages:

```
pir-export              tree constants (TIER{1,2}_ROWS, TIER{1,2}_ITEM_BITS)
    │
    ▼
pir-server              tier1_scenario() / tier2_scenario()
    │                   → YpirScenario { num_items, item_size_bits }
    │
    ▼
ypir crate              params_for_scenario_simplepir()
    │                   → spiral_rs::params::Params (full lattice config)
    │
    ▼
TierServer              server-side PIR engine (precompute + answer queries)


    ─── GET /params/tier{1,2}  (JSON) ───▶  pir-client


pir-client              YPIRClient::from_db_sz(num_items, item_size_bits, true)
                        → reconstructs identical Params locally
```

---

## Tree-layout constants

> **File:** `pir/types/src/lib.rs`

These constants define the Merkle tree tier structure. They determine the
number of rows and bytes-per-row that YPIR must support.

### Tree depth and layers

| Constant | Value | Meaning |
|:--|--:|:--|
| `PIR_DEPTH` | 26 | Total tree depth (2^26 leaf slots) |
| `TIER0_LAYERS` | 11 | Plaintext tier (not PIR-fetched) |
| `TIER1_LAYERS` | 7 | Layers per Tier 1 subtree |
| `TIER2_LAYERS` | 8 | Layers per Tier 2 subtree |

### Database dimensions

| Constant | Tier 1 | Tier 2 | How derived |
|:--|--:|--:|:--|
| Rows | 2,048 | 262,144 | `1 << TIER0_LAYERS`, `1 << (TIER0_LAYERS + TIER1_LAYERS)` |
| Leaves/row | 128 | 256 | `1 << TIER{n}_LAYERS` |
| Internal nodes/row | 126 | 254 | `(1 << TIER{n}_LAYERS) - 2` |
| Row bytes | 12,224 | 24,512 | `internal × 32 + leaves × 64` |
| Item bits | 97,792 | 196,096 | `row_bytes × 8` |

---

## Scenario construction

> **File:** `pir/server/src/lib.rs` — `tier1_scenario()` / `tier2_scenario()`

Each tier packs the two values YPIR needs into a `YpirScenario`:

```rust
// pir/types/src/lib.rs
pub struct YpirScenario {
    pub num_items: usize,
    pub item_size_bits: usize,
}
```

| Tier | `num_items` | `item_size_bits` |
|:--|--:|--:|
| 1 | 2,048 | 97,792 |
| 2 | 262,144 | 196,096 |

The server uses these in two ways:

1. Passed to `OwnedTierState::new()` to initialize the YPIR engine.
2. Served as JSON at `GET /params/tier1` and `GET /params/tier2` so the
   client can reconstruct identical lattice parameters.

---

## YPIR lattice parameters

> **File:** `ypir/src/params.rs` — `params_for_scenario_simplepir()`

This function takes `(num_items, item_size_bits)` and produces a full
Spiral `Params` struct.

### Step-by-step derivation

**1. Minimum size guard**

```
item_size_bits >= 2048 × 14 = 28,672
```

Each SimplePIR column holds one polynomial of 2048 coefficients with 14
plaintext bits each.

**2. Database matrix shape**

```
db_rows = num_items
db_cols = ceil(item_size_bits / 28,672)
```

| | Tier 1 | Tier 2 |
|:--|--:|--:|
| `db_rows` | 2,048 | 262,144 |
| `db_cols` | 4 | 7 |

**3. Ring dimension exponent**

```
nu_1 = log2(next_power_of_two(db_rows)) − 11
```

The `−11` accounts for `poly_len = 2048 = 2^11`. Padded row count is
`2^(nu_1 + 11)`.

| | Tier 1 | Tier 2 |
|:--|--:|--:|
| `nu_1` | 0 | 7 |

**4. Second dimension**

`nu_2 = 1`. SimplePIR is one-dimensional (no second folding pass).

**5. Database width (`instances`)**

`params.instances = db_cols`. This sets the number of polynomial-width
column groups in the database matrix. The client sends a **single**
encrypted row-selector query (length `db_rows`). The server computes
**one** matrix-vector product across all `db_cols = instances × poly_len`
columns simultaneously, then ring-packs the results into `instances`
RLWE ciphertexts. The client decrypts all of them to recover the full row.

### Fixed cryptographic constants

Hardcoded in `params_for_scenario_simplepir` and `internal_params_for`.
These correspond to the YPIR+SP variant described in
[YPIR: High-Throughput Single-Server PIR with Silent Preprocessing](https://eprint.iacr.org/2024/270.pdf)
(Menon & Wu, 2024). Our system uses YPIR+SP (SimplePIR-based packing)
because each tier row is a large record (12–24 KB), matching the
"large record" setting from Section 4.6 of the paper.

The values below are set by the ypir crate. For reference, the paper's
Table 1 lists the full YPIR parameter set chosen for 128-bit security
and correctness error δ ≤ 2⁻⁴⁰:

| | SimplePIR params | DoublePIR params |
|:--|:--|:--|
| Ring dim (d) | 2^10 = 1,024 | 2^11 = 2,048 |
| Noise (σ) | 11√(2π) | 6.4√(2π) |
| Plaintext mod (N / p) | 2^8 | 2^15 |
| Encoding mod (q) | 2^32 | ≈ 2^56 (product of two 28-bit NTT primes) |
| Reduced mod (q̃) | 2^28 | 2^28 (q̃₂,₁), 2^20 (q̃₂,₂) |
| Decomp. base (z) | — | 2^19 |

Our codebase only uses the SimplePIR side of these parameters (the
YPIR+SP variant). The concrete values hardcoded in the ypir crate's
`params_for_scenario_simplepir` and `internal_params_for`:

| Parameter | Value | Purpose |
|:--|:--|:--|
| `p` | 16,384 (2^14) | Plaintext modulus — bits of data per coefficient |
| `q2_bits` | 28 | Ciphertext compression modulus bit-width |
| `moduli` | [268369921, 249561089] | NTT-friendly CRT primes for the ciphertext ring |
| `poly_len` | 2,048 | Ring polynomial degree (d₂ from Table 1) |
| `noise_width` | 16.042421 | Gaussian noise standard deviation (σ) |
| `n` | 1 | RLWE rank (rank-1 = standard RLWE) |
| `t_gsw` | 3 | GSW decomposition base |
| `t_conv` | 4 | Key-switching decomposition parameter |
| `t_exp_left` | 3 | Regev-to-GSW expansion (left half) |
| `t_exp_right` | 2 | Regev-to-GSW expansion (right half) |

### Summary: concrete values per tier

| Parameter | Tier 1 | Tier 2 |
|:--|--:|--:|
| `num_items` | 2,048 | 262,144 |
| `item_size_bits` | 97,792 | 196,096 |
| `db_rows` | 2,048 | 262,144 |
| `db_cols` (instances) | 4 | 7 |
| `nu_1` | 0 | 7 |
| `nu_2` | 1 | 1 |

---

## Client-side reconstruction

> **File:** `pir/client/src/lib.rs` — `ypir_query()`

The client receives the `YpirScenario` JSON from the server, then calls:

```rust
YPIRClient::from_db_sz(scenario.num_items, scenario.item_size_bits, true)
```

This internally calls `params_for_scenario_simplepir` with the same
arguments, producing identical `Params`. The `true` flag selects SimplePIR
mode.

---

## Upstream references

**Paper:**
[YPIR: High-Throughput Single-Server PIR with Silent Preprocessing](https://eprint.iacr.org/2024/270.pdf)
(Samir Jordan Menon, David J. Wu — USENIX Security 2024).
Table 1 lists the full parameter set. Section 4.6 describes YPIR+SP
(the SimplePIR-based variant we use for large-record retrieval).

**Code:**
[github.com/menonsamir/ypir](https://github.com/menonsamir/ypir)
(branch `artifact`, commit `b980152`).
Parameter selection logic:
[`src/params.rs`](https://github.com/menonsamir/ypir/blob/artifact/src/params.rs)
