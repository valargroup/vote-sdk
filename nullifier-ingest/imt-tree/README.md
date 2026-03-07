# imt-tree

Indexed Merkle Tree (IMT) for nullifier non-membership proofs in the Shielded-Vote voting protocol. This crate builds a gap-range Poseidon Merkle tree from on-chain Zcash Orchard nullifiers and generates exclusion proofs that feed directly into the delegation circuit.

## Architecture Overview

### Scale and Capacity

Zcash mainnet currently has ~51M Orchard nullifiers (documented upper bound: 64M). The tree is sized for up to **256M nullifiers** (future-proof):

| Parameter | Value | Rationale |
|-----------|-------|-----------|
| Current nullifiers | ~51M | Mainnet as of Feb 2026 |
| Design capacity | 256M | Future-proof headroom |
| Leaves per tree | `n + 1` gap ranges for `n` nullifiers | Each nullifier creates a boundary |
| `TREE_DEPTH` | **29** | `2^29 = 536M` leaf slots, comfortably above 256M |
| Populated leaves (current) | ~51M | ~10% of total capacity |
| Empty slots (current) | ~485M | Collapsed via precomputed empty hashes |

### Gap-Range Exclusion Model

Rather than an inclusion tree, this is an **exclusion (non-membership) tree**. The key insight: to prove a note is **unspent**, we must show its nullifier does NOT appear on-chain. The gap-range tree inverts this into a positive statement.

Given sorted on-chain nullifiers `n1, n2, ..., n_k`, the tree stores the **gaps** between them:

```
Sorted nullifiers:  n0=0, n1, n2, ..., n_51M

Gap ranges (leaves):
  [0+1,      n1-1    ]   -- gap 0
  [n1+1,     n2-1    ]   -- gap 1
  [n2+1,     n3-1    ]   -- gap 2
  ...
  [n_51M+1,  p-1     ]   -- gap 51M   (p = Pallas field modulus)
```

Each leaf commits to one gap range: `leaf_hash = Poseidon(low, high)`. To prove a value `x` is NOT a nullifier, show it falls inside one of these gaps. Because each nullifier `n` is the boundary between two adjacent ranges (`high = n - 1` and `low = n + 1`), the nullifier itself is excluded from every range.

### Tree Structure and Empty-Slot Optimization

With 51M populated leaves in a depth-29 tree (536M slots), **~90% of slots are empty**. The tree exploits this with precomputed empty subtree hashes:

```
empty[0]  = Poseidon(0, 0)                       -- empty leaf
empty[1]  = Poseidon(empty[0], empty[0])          -- empty 2-leaf subtree
empty[2]  = Poseidon(empty[1], empty[1])          -- empty 4-leaf subtree
...
empty[28] = Poseidon(empty[27], empty[27])        -- empty 2^28-leaf subtree
```

During tree construction (`build_levels`), any odd-length layer is padded with the appropriate `empty[level]`, and all-empty subtrees collapse to their precomputed hash. This gives:

- **Deterministic root**: the root depends only on the nullifier set, not on tree capacity
- **O(29) proof generation**: walk pre-stored levels, falling back to `empty[level]` for out-of-bounds siblings
- **All levels retained**: `Vec<Vec<Fp>>` stores every intermediate layer so proofs are pure sibling lookups with no recomputation

Memory footprint at 51M leaves (all levels stored):

```
Level  0:  ~51M   x 32 bytes = ~1.6 GB
Level  1:  ~25.5M x 32 bytes = ~816 MB
Level  2:  ~12.75M ...
...
Total across 29 levels:  ~3.2 GB
```

## PoseidonHasher Optimization

Building the tree requires hashing ~102M nodes (51M leaves + 51M internal). The naive `poseidon::Hash::init().hash()` re-allocates round constants on every call (~6 KiB heap per init). At 102M calls, that's ~128M unnecessary allocations.

`PoseidonHasher` fixes this by computing P128Pow5T3 constants once and implementing the permutation inline:

- **R_F = 8** full rounds (4 + 4), **R_P = 56** partial rounds
- Width-3 state: `[left, right, capacity]`
- Single permutation, output = `state[0]`
- Verified equivalent to the upstream `halo2_gadgets` hasher

The speedup is substantial -- tree build drops from minutes to seconds for 51M entries.

## Sentinel Nullifiers: Circuit-Compatibility Constraint

The delegation circuit proves `low <= nf <= high` using a **250-bit range check** (25 limbs x 10 bits via `LookupRangeCheckConfig`). This is only sound if every gap range has width `< 2^250`. The Pallas field is ~2^254, so unconstrained gaps could exceed this.

Solution: **17 sentinel nullifiers** at `k * 2^250` for `k = 0..=16`:

```
Sentinel placement on the Pallas field [0, p):

  |--2^250--|--2^250--|--2^250--| ... |--2^250--|--remainder--|
  0       2^250    2*2^250           15*2^250  16*2^250    p-1

Each interval between sentinels has width = 2^250 - 2 < 2^250
```

Since `p ~ 16.something x 2^250`, 17 sentinels cover the entire field. Adding real nullifiers only **splits** existing gaps into smaller ones, so the invariant `width < 2^250` holds permanently once established.

`build_sentinel_tree()` merges these 17 sentinels with real nullifiers before building.

## Circuit Integration (Condition 13)

The delegation circuit proves 15 conditions for up to 4 notes. For each note, **condition 13** verifies IMT non-membership via three sub-checks:

### Leaf Hash (1 Poseidon call)

```
leaf_hash = Poseidon(low, high)     -- in-circuit using PoseidonChip
```

### Merkle Path (29 Poseidon calls)

At each of the 29 levels:

1. **`q_imt_swap` gate**: Conditionally swap `(current, sibling)` based on `pos_bit`:

   ```
   left  = current + pos_bit * (sibling - current)
   right = sibling + pos_bit * (current - sibling)
   ```

2. **Poseidon hash**: `parent = Poseidon(left, right)` via `PoseidonChip`

The final computed root is constrained equal to the public `nf_imt_root` input (in the `q_per_note` gate).

Total per note: **1 + 29 = 30 Poseidon permutations** in-circuit.

### Interval Check (`q_interval` gate + 2 range checks)

Proves `low <= real_nf <= high` algebraically:

```
y         = high - low            (interval width)
x         = real_nf - low         (offset from low bound)
x_shifted = 2^250 - y + x - 1    (reflected offset from high bound)
```

Then:
- **`x in [0, 2^250)`** proves `real_nf >= low` (a negative difference wraps to a huge field element, failing the range check)
- **`x_shifted in [0, 2^250)`** proves `real_nf <= high` (if `x > y`, then `x_shifted >= 2^250`, failing)

Each range check decomposes the value into 25 limbs of 10 bits via `LookupRangeCheckConfig`.

### Root Pinning (`q_per_note` gate)

```
("imt_root = nf_imt_root", imt_root - nf_imt_root)
```

This is **NOT gated on `is_note_real`** -- even padded (dummy) notes must carry valid IMT proofs. Padded notes still get real nullifiers derived from their zero-value notes, and the circuit verifies non-membership for all 4 slots uniformly. The builder handles this by calling `imt_provider.non_membership_proof(real_nf)` for padded notes too.

### Cost Summary Per Note Slot

| Component | Custom Gates | Poseidon Calls | Range Check Limbs |
|-----------|-------------|---------------|------------------|
| Leaf hash | -- | 1 | -- |
| Merkle path | 29 `q_imt_swap` | 29 | -- |
| Interval check | 1 `q_interval` | -- | 50 (2 x 25) |
| Root check | 1 `q_per_note` (shared) | -- | -- |
| **Total per note** | **31** | **30** | **50** |
| **Total (4 notes)** | **124** | **120** | **200** |

The 120 in-circuit Poseidon permutations for condition 13 are the dominant constraint count contributor for the IMT check.

## Data Flow: Off-Chain to In-Circuit

```
                      OFF-CHAIN (nullifier-tree + imt-tree crates)
                      ────────────────────────────────────────────
Zcash chain --> ingest-nfs --> SQLite (51M nullifiers)
                                   |
                          NullifierTree::from_db()
                                   |
                   +---------------+---------------+
                   |  51M gap ranges, 29 levels,   |
                   |  all siblings pre-stored       |
                   +---------------+---------------+
                                   |
                         tree.prove(real_nf)
                                   |
                          ExclusionProof {
                            range: [low, high],
                            position, leaf, auth_path[29]
                          }
                                   |
                         .to_imt_proof_data(root)
                                   |
                          ImtProofData {         <-- same struct in
                            root, low, high,         orchard::delegation::imt
                            leaf_pos, path[29]
                          }
                                   |
                      =============|=======================
                      IN-CIRCUIT   | (orchard delegation circuit)
                      -------------|---------------------
                                   v
                   NoteSlotWitness.imt_{low,high,leaf_pos,path}
                                   |
                   +---------------+-------------------+
                   |  Poseidon(low, high) --> leaf      |  leaf hash
                   |  29x swap + Poseidon --> root      |  Merkle path
                   |  q_interval + 2x range check      |  interval proof
                   |  q_per_note: root = nf_imt_root   |  root pin
                   +-----------------------------------+
                                   |
                             proof verified
```

The `ImtProofData` struct is the seam between the off-chain and in-circuit worlds -- identical fields on both sides:
- Off-circuit: `nullifier_tree::ImtProofData` (in the `nullifier-tree` crate)
- In-circuit: `orchard::delegation::imt::ImtProofData` (in the `orchard` crate)

The `ImtProvider` trait abstracts over the proof source (real tree vs. test provider) so the delegation builder works uniformly for production and testing.

## Key Files Across Crates

| File | Role |
|------|------|
| `nullifier-tree/src/tree.rs` | Off-chain tree build, `NullifierTree`, `ExclusionProof`, `PoseidonHasher`, sentinels |
| `nullifier-tree/src/sync_nullifiers.rs` | Chain sync engine (parallel gRPC downloads into SQLite) |
| `nullifier-tree/src/bin/server.rs` | HTTP API serving exclusion proofs (`GET /exclusion-proof/:nf`) |
| `orchard/src/delegation/imt.rs` | `ImtProofData`, `ImtProvider` trait, `SpacedLeafImtProvider` (test) |
| `orchard/src/delegation/circuit.rs` | In-circuit condition 13: `q_imt_swap`, `q_interval`, `q_per_note` gates |
| `orchard/src/delegation/builder.rs` | Delegation bundle builder -- wires `ImtProofData` into `NoteSlotWitness` |
