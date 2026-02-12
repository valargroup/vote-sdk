# nullifier-tree

Builds a Merkle tree over the **gaps** between Zcash Orchard nullifiers, enabling non-inclusion (exclusion) proofs — proving that a nullifier has *not* been used on-chain.

## Files

| File | Purpose |
|---|---|
| `src/lib.rs` | Core library: gap-range construction (`build_nf_ranges`), leaf commitment (`commit_ranges`), range lookup (`find_range_for_value`), Merkle root (`compute_nf_root`), and binary tree caching (`save_tree`/`load_tree`) |
| `src/db.rs` | SQLite schema for the `nfs` (nullifiers) and `properties` (key-value config) tables |
| `src/download.rs` | TLS-enabled gRPC client for connecting to a lightwalletd node |
| `src/ca.pem` | Embedded CA certificate for lightwalletd TLS (ISRG Root X1) |
| `src/cash.z.wallet.sdk.rpc.rs` | Generated protobuf types for the lightwalletd compact block streaming API |
| `src/bin/ingest_nfs.rs` | Binary that downloads all Orchard nullifiers from the chain into a local SQLite database |
| `src/bin/test_non_inclusion.rs` | Binary that verifies exclusion proofs against the ingested nullifier set |

## Ingesting nullifiers

```bash
# Uses defaults: LWD_URL=https://zec.rocks:443, DB_PATH=nullifiers.db
cargo run --release -p nullifier-tree --bin ingest-nfs

# Or override:
LWD_URL=https://your-lwd:443 DB_PATH=my.db cargo run --release -p nullifier-tree --bin ingest-nfs
```

This streams every Orchard nullifier from NU5 activation to chain tip into `nullifiers.db`. It supports resuming — re-run it to catch up to the latest block.

## Doing an exclusion proof

An exclusion proof shows that a given nullifier value falls inside a **gap range** (an interval between two adjacent on-chain nullifiers), meaning it has never been spent.

Each leaf in the Merkle tree is a **commitment to a `(low, high)` range pair** — `hash(low, high)` using the same Sinsemilla-based combine as the rest of the tree. Given on-chain nullifiers `n1, n2`, the leaves are:

```
hash(0, n1-1) | hash(n1+1, n2-1) | hash(n2+1, max)
```

A single Merkle path proves a committed range is in the tree. The prover then reveals `(low, high)` and the verifier checks the hash matches and the target value falls within.

```rust
use nullifier_tree::{list_nf_ranges, commit_ranges, find_range_for_value};
use orchard::vote::calculate_merkle_paths;
use rusqlite::Connection;

// 1. Open the nullifier database
let conn = Connection::open("nullifiers.db")?;

// 2. Build gap ranges and commit each (low, high) pair into a leaf hash
let ranges = list_nf_ranges(&conn)?;
let leaves = commit_ranges(&ranges);

// 3. Find which gap range contains your value
let my_nullifier: Fp = /* ... */;
let range_idx = find_range_for_value(&ranges, my_nullifier)
    .expect("value IS an existing nullifier — no exclusion proof possible");

// 4. Get the Merkle path for this leaf
let pos = range_idx as u32;
let (root, paths) = calculate_merkle_paths(0, &[pos], &leaves);

// The proof is:
//   - Merkle path reconstructs to root
//   - hash(low, high) matches the leaf
//   - my_nullifier ∈ [low, high]
```

Run the built-in test binary to verify end-to-end:

```bash
cargo run --release -p nullifier-tree --bin test-non-inclusion
```

## Tree caching

For large nullifier sets, rebuilding ranges from the database is slow. You can cache the computed ranges to a binary file:

```rust
use nullifier_tree::{list_nf_ranges, save_tree, load_tree};

// Build and save
let ranges = list_nf_ranges(&conn)?;
save_tree(Path::new("nf_ranges.bin"), &ranges)?;

// Later, load without touching the database
let ranges = load_tree(Path::new("nf_ranges.bin"))?;
```

Format: `[8-byte LE count][count × 2 × 32-byte Fp representations]` (each range is a `(low, high)` pair).
