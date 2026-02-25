# Shielded Vote

Monorepo for the Zcash shielded voting system. Contains the vote chain (Cosmos SDK), ZK circuits (Halo2 + RedPallas), nullifier ingestion service, admin UI, iOS wallet integration, and end-to-end tests.

## Infrastructure Setup

| Guide                                                    | Purpose                                                                                                          |
| -------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------- |
| [SETUP_GENESIS.md](SETUP_GENESIS.md)                     | Bootstrap the genesis validator — build the binary, initialise the chain, open P2P, and record the node identity |
| [SETUP_JOIN.md](SETUP_JOIN.md)                           | Add a new validator — sync a post-genesis node, fund it, and submit `MsgCreateValidatorWithPallasKey`            |
| [SETUP_NULLIFIER_SERVICE.md](SETUP_NULLIFIER_SERVICE.md) | Set up the nullifier service — install deps, bootstrap the snapshot, and start the exclusion proof query server  |

## Architecture

| Component                     | Language           | Description                                                                                             |
| ----------------------------- | ------------------ | ------------------------------------------------------------------------------------------------------- |
| `sdk/`                        | Go + Rust (CGo)    | Cosmos SDK chain (`zallyd`) with vote module, ante handlers, and ZK verification                        |
| `nullifier-ingest/`           | Rust               | Ingests Orchard nullifiers from a lightwallet server into flat binary files and serves exclusion proofs |
| `shielded_vote_generator_ui/` | TypeScript / React | UI for constructing and submitting shielded votes                                                       |
| `zcash-voting-ffi/`           | Rust + Swift       | iOS FFI bindings for the voting circuits                                                                |
| `e2e-tests/`                  | Rust               | End-to-end API tests against a running chain                                                            |

## Prerequisites

Install [mise](https://mise.jdx.dev) and a C compiler:

```sh
curl https://mise.run | sh       # install mise
xcode-select --install           # macOS — or: apt install build-essential (Linux)
```

Optionally, activate mise in your shell so tools are available automatically when you `cd` into the project:

```sh
echo 'eval "$(mise activate zsh)"' >> ~/.zshrc
mise settings set autoinstall true
```

Without shell activation, use `mise install` to install tools and `mise run <task>` to run commands.

Go, Rust, and Node are pinned in `mise.toml`. Submodules that need specific Rust versions (e.g. librustzcash: 1.85.1) use `rust-toolchain.toml` — mise/rustup switches automatically.

## Setup

```sh
cd zally
mise trust      # one-time: allow mise to run this project's config
mise start      # init chain, bootstrap nullifiers, start everything
```

This builds the chain binary (with Halo2 + RedPallas ZK verification), initialises a single-validator chain, fetches Orchard nullifiers, and starts the chain node and nullifier query server in the background.

If services are already running, `mise start` will report them and exit cleanly.

```sh
mise status     # check service health and voting round state
mise stop       # stop all services
mise ui         # start admin UI dev server (port 5173)
mise test       # end-to-end tests against running chain
```
