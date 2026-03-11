# Shielded Vote

Monorepo for the Zcash shielded voting system. Contains the vote chain (Cosmos SDK), ZK circuits (Halo2 + RedPallas), nullifier ingestion service, admin UI, iOS wallet integration, and end-to-end tests.

## Infrastructure Setup

| Guide                                                    | Purpose                                                                                                          |
| -------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------- |
| [BOOTSTRAP.md](docs/BOOTSTRAP.md)                             | End-to-end playbook for standing up a new network from scratch (infra, genesis, nullifiers, onboarding, release) |
| [SETUP_GENESIS.md](docs/SETUP_GENESIS.md)                     | Bootstrap the genesis validator — build the binary, initialise the chain, open P2P, and register in Edge Config  |
| [SETUP_JOIN.md](docs/SETUP_JOIN.md)                           | Join as a validator — self-registration, funding, and automatic on-chain registration via `join.sh`              |
| [SETUP_NULLIFIER_SERVICE.md](docs/SETUP_NULLIFIER_SERVICE.md) | Set up the nullifier service — install deps, bootstrap the snapshot, and start the exclusion proof query server  |

## Architecture

| Component                     | Language           | Description                                                                                             |
| ----------------------------- | ------------------ | ------------------------------------------------------------------------------------------------------- |
| `sdk/`                        | Go + Rust (CGo)    | Cosmos SDK chain (`svoted`) with vote module, ante handlers, and ZK verification                        |
| `vote-nullifier-pir/`           | Rust             | Ingests Orchard nullifiers and serves PIR exclusion proofs (subtree of [valargroup/vote-nullifier-pir](https://github.com/valargroup/vote-nullifier-pir)) |
| `shielded_vote_generator_ui/` | TypeScript / React | UI for constructing and submitting shielded votes                                                       |
| `zcash-swift-wallet-sdk/`     | Rust + Swift       | iOS SDK with hand-rolled C FFI for voting (subtree of [valargroup/zcash-swift-wallet-sdk](https://github.com/valargroup/zcash-swift-wallet-sdk)) |
| `zodl-ios/`                   | Swift              | iOS wallet app (subtree of [valargroup/zodl-ios](https://github.com/valargroup/zodl-ios))               |
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
cd shielded-vote
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

Run `mise tasks` for the full list. Key namespaces: `build:*`, `chain:*`, `multi:*`, `nullifier:*`, `test:*`.

<!-- mise-tasks -->

| Task | Description |
|---|---|
| **Daily** | |
| `start` | Init chain + bootstrap nullifiers + start everything |
| `stop` | Stop all services |
| `status` | Show service health + voting round state |
| `ui` | Start admin UI dev server (port 5173) |
| `test` | E2E tests against running chain |
| **build:\*** | |
| `build` | Build svoted with FFI (Halo2 + RedPallas) |
| `build:quick` | Build svoted without FFI (Go only) |
| `build:install` | Install svoted with FFI to $GOBIN |
| `build:circuits` | Build Rust circuit static library |
| `build:ui` | Build admin UI for production |
| **chain:\*** | |
| `chain:init` | Wipe and reinitialize a single-validator chain |
| `chain:start` | Start chain daemon (foreground) |
| `chain:clean` | Remove chain data directory |
| `chain:ceremony` | Register Pallas key + create round + wait ACTIVE |
| **multi:\*** | |
| `multi:init` | Build + init 3-validator chain (no start) |
| `multi:start` | Init + nullifiers + PIR + start 3-validator chain |
| `multi:stop` | Stop all multi-validator processes |
| `multi:status` | Show running status of all 3 validators |
| `multi:clean` | Stop + remove all multi-validator data |
| **nullifier:\*** | |
| `nullifier:bootstrap` | Download nullifier snapshot from DO Spaces |
| `nullifier:ingest` | Sync nullifiers to SYNC_HEIGHT or chain tip |
| `nullifier:export` | Build PIR tree and export tier files |
| `nullifier:serve` | Start PIR server (port 3000) |
| `nullifier:status` | Show nullifier ingestion progress |
| `nullifier:clean` | Remove nullifier data + build artifacts |
| **test:\*** | |
| `test:unit` | Go unit tests (keeper, validation, codec) |
| `test:integration` | Go ABCI pipeline integration tests |
| `test:helper` | Helper server unit tests (SQLite, API, processor) |
| `test:go` | All Go tests (unit + integration + helper) |
| `test:circuits` | Rust circuit unit tests |
| `test:ffi` | All FFI-backed tests (Halo2 + RedPallas) |
| `test:nullifier` | Nullifier crate unit tests |
| `test:proof` | Verify exclusion proofs against ingested data |
| **Other** | |
| `validator:join` | Build from source and join network as validator |
| `fmt` | Format Go code |
| `lint` | Run Go vet |
| `fixtures` | Regenerate all fixture files |
| `proto` | Regenerate protobuf code |

<!-- /mise-tasks -->
