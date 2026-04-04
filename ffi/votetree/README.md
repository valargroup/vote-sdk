# sdk/ffi/votetree

Go CGO bindings to the Poseidon Merkle tree in the shielded-vote-circuits Rust static library (`libshielded_vote_circuits.a`).

## Role in the protocol

The vote commitment tree is an append-only, depth-24 Poseidon Merkle tree maintained by the vote chain. Every `MsgDelegateVote` appends one Vote Authority Note (VAN) leaf; every `MsgCastVote` appends two leaves (new VAN + Vote Commitment). EndBlocker snapshots the root at each block height. That root becomes the on-chain anchor for ZKP #2 (VAN membership) and ZKP #3 (VC membership).

This package is the Go-side interface to the tree. The tree itself is implemented in `vote-commitment-tree/` and compiled into the Rust static library. All root and path computations happen in Rust; Go calls through CGO.

## Two APIs

### Stateless: `ComputePoseidonRoot` / `ComputeMerklePath`

```go
root, err := votetree.ComputePoseidonRoot(leaves)
path, err := votetree.ComputeMerklePath(leaves, position)
```

A fresh in-memory tree is built from a complete flat leaf slice on every call. Simple, but **O(n)** in the number of leaves. Used in the helper server for proof generation and in tests.

### Stateful: `TreeHandle`

```go
proxy := &votetree.KvStoreProxy{}        // allocated once on the Keeper
h := votetree.NewTreeHandleWithKV(proxy, nextIndex)

// Each block:
proxy.Current = kvStore                  // point Rust at this block's KV store
h.AppendBatch(deltaLeaves)
h.Checkpoint(blockHeight)
root, _ := h.Root()
```

`TreeHandle` wraps a Rust `ShardTree<KvShardStore>` that persists across blocks. Rust reads and writes shards, the cap, and checkpoints **directly to the Cosmos KV store** through Go callbacks registered at handle creation time — no leaf replay on cold start, no explicit flush coordination.

## Architecture

```
Go Keeper
  ├─ kvProxy: *KvStoreProxy           ← stable pointer; address never changes
  │    └─ Current: store.KVStore      ← updated each block before any tree call
  └─ treeHandle: *TreeHandle
       └─ ptr ──────────────────────► SvTreeHandle  (Rust Box<T>, Rust heap)
                                           └─ TreeServer
                                                └─ ShardTree<KvShardStore, depth=24, shard_height=4>
                                                     └─ KvShardStore
                                                          └─ KvCallbacks { ctx=kvProxy, get, set, delete, iter_* }
                                                               └─ svKv* //export functions in kv_callbacks.go
                                                                    └─ kvProxy.Current (Cosmos KVStore)
```

The Rust allocation is **not tracked by the Go GC**. `Close()` must be called to free it. `Close()` is idempotent.

## KV key schema

ShardTree state is persisted to three KV key ranges, using the same byte prefixes defined in `x/vote/types/keys.go`:

| Prefix | Key format | Value | Updated by |
|---|---|---|---|
| `0x0F` | `0x0F \|\| u64 BE shard_index` | shard blob | `put_shard` (on every append that modifies a shard) |
| `0x10` | `0x10` | cap blob | `put_cap` (when a shard completes or checkpoint is taken) |
| `0x11` | `0x11 \|\| u32 BE checkpoint_id` | checkpoint blob | `add_checkpoint` (every `Checkpoint()` call) |

Shard blobs use a compact recursive PARENT/LEAF/NIL encoding defined in `vote-commitment-tree/src/serde.rs`. Checkpoints encode tree position + marks-removed as a fixed-layout binary record.

## Reverse FFI: how Rust calls Go

The `KvShardStore` in Rust holds a `KvCallbacks` struct of C function pointers. These point to `//export` Go functions in `kv_callbacks.go` that dispatch through the `ctx` pointer — a raw `*KvStoreProxy` — to the current block's `store.KVStore`:

```
ShardTree calls:  KvShardStore::put_shard(idx, blob)
      ↓
  KvCallbacks.set(ctx, key, key_len, val, val_len)
      ↓
  svKvSet(ctx=*KvStoreProxy, ...)  [//export in kv_callbacks.go]
      ↓
  proxy.Current.Set(key, val)  [Cosmos KVStore for current block]
```

`get_fn` / `iter_next_fn` return C-malloc'd buffers; Rust copies the data then calls `free_buf_fn` (which calls `C.free`). Iterator handles are `cgo.Handle` values wrapping a `store.Iterator`; they are closed and deleted by `svKvIterFree`.

**Why a `KvStoreProxy` rather than a `cgo.Handle`?** The Cosmos KV store is per-block — a new instance is passed to every EndBlocker invocation. The `KvStoreProxy` is allocated once and its address is stable for the lifetime of the process. Go updates `proxy.Current` before each tree call; Rust's callbacks always see the current block's store through the same stable pointer.

**Why two files (`kv_callbacks.go` and `tree_kv_handle.go`)?** CGO requires that `//export` declarations and references to those exported symbols as C function pointers live in separate `.go` files within the same package.

## Checkpoint semantics

`ShardTree` (from Zcash's `incrementalmerkletree` crate) only materialises Merkle roots at checkpoint boundaries:

```
AppendBatch(leaves)        ← Rust writes modified shards to KV via callbacks
Checkpoint(blockHeight)    ← Rust writes checkpoint blob to KV; root is accessible
Root()                     ← returns root at the most recent checkpoint
Path(pos, height)          ← returns witness anchored to a specific checkpoint
```

`Root()` before any `Checkpoint()` returns the deterministic empty-tree root. `ComputeTreeRoot` in the keeper always checkpoints immediately after appending new leaves.

## Cold start / rollback

```
Condition                    Action
───────────────────────────  ──────────────────────────────────────────────────────
treeHandle == nil            Cold start: NewTreeHandleWithKV(proxy, nextIndex).
                             ShardTree reads the frontier shard + cap + checkpoints
                             lazily from KV on first use — O(1), no leaf replay.
treeCursor < nextIndex       Delta: AppendBatch([treeCursor, nextIndex)) from KV.
treeCursor == nextIndex      No-op.
treeCursor > nextIndex       Rollback: Close handle, recreate via NewTreeHandleWithKV.
```

On every process restart `treeHandle` is nil, but cold start is O(1): `ShardTree` calls `last_shard()` → `get_cap()` → checkpoint reads on the first `AppendBatch` / `Checkpoint`, pulling only what it needs from KV.

## CGO boundary for leaf batches

Leaf appends cross the CGO boundary as a single flat byte array regardless of batch size:

```go
flat := make([]byte, len(leaves)*LeafBytes)
for i, leaf := range leaves { copy(flat[i*LeafBytes:], leaf) }
C.sv_vote_tree_append_batch(handle, &flat[0], len(leaves))
```

CGO calls carry ~50–100 ns overhead each; batching amortises that to one call per block.

## Leaf encoding

All leaves and roots are 32-byte little-endian canonical Pallas Fp values — the same encoding the Go KV store uses (`0x02 || big-endian index → 32-byte leaf`). Non-canonical byte patterns (≥ the Pallas field modulus) are rejected by the Rust deserializer with error code `-3`.

## Build requirement

The Rust static library must be built before CGO can link:

```bash
cargo build --release --manifest-path sdk/circuits/Cargo.toml
```

For development, `make dev-incr` in `zcash-voting-ffi/` also rebuilds it. The library is at `sdk/circuits/target/release/libshielded_vote_circuits.a`.

## Files

| File | Contents |
|---|---|
| `tree_ffi.go` | Package doc, constants, stateless functions (`ComputePoseidonRoot`, `ComputeMerklePath`), `TreeHandle` type and core methods (`AppendBatch`, `Checkpoint`, `Root`, `Size`, `Path`, `Close`) |
| `kv_callbacks.go` | `KvStoreProxy` struct; `//export svKv*` reverse-FFI callbacks (`svKvGet`, `svKvSet`, `svKvDelete`, `svKvIterCreate`, `svKvIterNext`, `svKvIterFree`, `svKvFreeBuf`) |
| `tree_kv_handle.go` | `NewTreeHandleWithKV` — creates a KV-backed handle by passing the callback function pointers to `sv_vote_tree_create_with_kv`; separate file required by CGO `//export` rules |
| `tree_ffi_test.go` | Golden vector tests, stateless round-trip tests, `TreeHandle` tests |
