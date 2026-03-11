# librustvoting

Client-side cryptographic library for Zcash shielded voting. Implements proof generation, vote construction, and tree synchronization for the [Zally governance protocol](https://github.com/valargroup/shielded-vote-book).

## Workspace Crates

| Crate | Description |
|-------|-------------|
| **librustvoting** | Core library: ZKP delegation and vote proofs (Halo2), El Gamal encryption, governance PCZT construction, Merkle witness generation, SQLite round-state persistence |
| **vote-commitment-tree** | Append-only Poseidon Merkle tree for Vote Authority Notes and Vote Commitments |
| **vote-commitment-tree-client** | HTTP client and CLI for syncing the vote commitment tree from a chain node |

## Architecture

```
librustvoting
├── vote-commitment-tree ──── imt-tree (vote-nullifier-pir)
├── vote-commitment-tree-client
├── pir-client (vote-nullifier-pir)
├── voting-circuits ── ZK delegation + vote proofs, orchard fork
└── librustzcash ───── pczt, zcash_keys, zcash_client_sqlite, ...
```

## Building

```bash
cargo check                    # check all crates
cargo build -p librustvoting   # build just the core library
```

The workspace depends on the private [valargroup/voting-circuits](https://github.com/valargroup/voting-circuits) repo. The `.cargo/config.toml` enables `git-fetch-with-cli` so your local git credentials are used automatically.

## Dependency Strategy

This workspace uses `[patch.crates-io]` (in the root `Cargo.toml`) to override two dependency trees:

- **orchard 0.11** — Resolved from [valargroup/voting-circuits](https://github.com/valargroup/voting-circuits), which bundles an orchard fork with public visibility for `constants`, `spec`, and a `shared_primitives::spend_authority` gadget.

- **librustzcash crates** (pczt, zcash_keys, zcash_client_sqlite, etc.) — Resolved from [valargroup/librustzcash](https://github.com/valargroup/librustzcash) branch `valargroup/pczt-governance-extensions-0.11`. Adds public getters and methods needed for governance PCZT construction and Merkle witness generation.

## FFI

Mobile FFI bindings live in [zcash-swift-wallet-sdk](https://github.com/valargroup/zcash-swift-wallet-sdk) (hand-rolled C FFI + Swift wrappers). This repo is a pure Rust workspace.

## License

TODO
