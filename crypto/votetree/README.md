# sdk/crypto/votetree

Go CGO bindings to the Poseidon Merkle tree in the zally-circuits Rust static library (`libzally_circuits.a`).

## Role in the protocol

The vote commitment tree is an append-only, depth-24 Poseidon Merkle tree maintained by the vote chain. Every `MsgDelegateVote` appends one Vote Authority Note (VAN) leaf; every `MsgCastVote` appends two leaves (new VAN + Vote Commitment). EndBlocker snapshots the root at each block height. That root becomes the on-chain anchor for ZKP #2 (VAN membership) and ZKP #3 (VC membership).

This package is the Go-side interface to the tree. The tree itself is implemented in `vote-commitment-tree/` and compiled into the Rust static library. All root and path computations happen in Rust; Go just calls through CGO.

## Two APIs

### Stateless: `ComputePoseidonRoot` / `ComputeMerklePath`

```go
root, err := votetree.ComputePoseidonRoot(leaves)
path, err := votetree.ComputeMerklePath(leaves, position)
```

A fresh tree is built from a complete flat leaf slice on every call. Simple, but **O(n)** in the number of leaves — the original EndBlocker bottleneck. Still used in tests and one-off callers.

### Stateful: `TreeHandle`

```go
h := votetree.NewTreeHandle()
defer h.Close()

h.AppendBatch(allLeaves)      // cold start: load everything from KV
h.Checkpoint(blockHeight)     // must precede Root / Path
root, _ := h.Root()

// next block: only the delta
h.AppendBatch(deltaLeaves)
h.Checkpoint(nextHeight)
root, _ = h.Root()
```

A `TreeHandle` wraps a Rust `ShardTree` that stays alive across blocks. Only the leaves added since the last call (`delta`) are fetched from KV and appended. Cost per block is **O(k)** where k = new leaves that block (typically 1–3), with a one-time **O(n)** load on node startup.

## Memory layout

```
Go Keeper
  └─ *votetree.TreeHandle          (Go struct, Go-managed heap)
       └─ ptr unsafe.Pointer  ───► ZallyTreeHandle  (Rust Box<T>, Rust heap)
                                        └─ TreeServer
                                             └─ ShardTree<MemoryShardStore, 32, 4>
```

The Rust allocation is **not tracked by the Go GC**. `Close()` must be called to free it. `Close()` is idempotent — a second call after the first is a no-op.

## Checkpoint semantics

ShardTree (from Zcash's `incrementalmerkletree` crate) only materialises Merkle roots at checkpoint boundaries. The sequence is always:

```
AppendBatch(leaves)        ← tree has leaves internally, but no root yet
Checkpoint(blockHeight)    ← snapshots state; assigns a checkpoint ID = height
Root()                     ← returns root at the most recent checkpoint
Path(pos, height)          ← returns witness anchored to a specific checkpoint
```

Calling `Root()` before any `Checkpoint()` returns the deterministic empty-tree root. Calling `Path(pos, height)` for a height with no checkpoint returns an error. `ComputeTreeRoot` in the keeper always checkpoints immediately after loading new leaves, before reading the root.

## CGO boundary

All leaves in a batch are flattened into one contiguous `[]byte` before the C call:

```go
flat := make([]byte, len(leaves)*LeafBytes)
for i, leaf := range leaves { copy(flat[i*LeafBytes:], leaf) }
C.zally_vote_tree_append_batch(handle, &flat[0], len(leaves))
```

This limits the cost to **one CGO call per batch** regardless of how many leaves are appended. CGO calls carry ~50–100 ns overhead each; batching amortises that to one call per block.

## Leaf encoding

All leaves and roots are 32-byte little-endian canonical Pallas Fp values — the same encoding the Go KV store uses (`0x02 || big-endian index → 32-byte leaf`). Non-canonical byte patterns (≥ the Pallas field modulus) are rejected by the Rust deserializer with error code `-3`.

## Rollback / crash recovery

The `TreeHandle` is a **cache**; KV is the source of truth. `Keeper.ensureTreeLoaded` compares `treeCursor` (leaves in handle) against KV `nextIndex`:

| Condition | Action |
|---|---|
| `treeHandle == nil` | Cold start: create handle, load all `[0, nextIndex)` leaves |
| `treeCursor < nextIndex` | Delta: append leaves `[treeCursor, nextIndex)` only |
| `treeCursor == nextIndex` | No-op |
| `treeCursor > nextIndex` | Rollback: close handle, rebuild from scratch |

On process restart `treeHandle` is always nil, triggering the one-time O(n) cold load on the first EndBlocker call.

## Build requirement

The Rust static library must be built before CGO can link:

```bash
cargo build --release --manifest-path sdk/circuits/Cargo.toml
```

For development, `make dev-incr` in `zcash-voting-ffi/` also rebuilds it. The library is located at `sdk/circuits/target/release/libzally_circuits.a`.

## Files

| File | Contents |
|---|---|
| `tree_ffi.go` | Package doc, constants, stateless functions, `TreeHandle` type and methods |
| `tree_ffi_test.go` | Golden vector tests, stateless round-trip tests, `TreeHandle` tests |
