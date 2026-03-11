# Contributing

Thank you for your interest in contributing to Vote Nullifier PIR.

## Prerequisites

- **Rust stable** toolchain (most crates)
- **Rust nightly** toolchain (only needed for the `avx512` feature flag, used in production deployment)

Install both with [rustup](https://rustup.rs/):

```bash
rustup toolchain install stable
rustup toolchain install nightly   # only if building with avx512
```

## Workspace Structure

The project is a Cargo workspace with eight crates across three layers:

| Layer | Crates |
|-------|--------|
| **Foundation** | `imt-tree`, `pir/types` |
| **Core** | `nf-ingest`, `pir/export`, `pir/server`, `pir/client` |
| **Binaries** | `nf-server`, `pir/test` |

See the [README](README.md) for a dependency diagram and crate descriptions.

## Building

```bash
# Build all crates (stable toolchain)
cargo build --release

# Build nf-server with PIR serving support
cargo build --release -p nf-server --features serve

# Build with AVX-512 (requires nightly, x86-64 with AVX-512 support)
cargo +nightly build --release -p nf-server --features avx512
```

Or use the Makefile:

```bash
make build    # Build nf-server (release)
```

## Running Tests

```bash
# Fast unit tests (imt-tree + nf-ingest)
make test

# PIR export round-trip tests (~2 min, exercises real crypto)
cargo test -p pir-export

# PIR client tests (~1 min, exercises real crypto)
cargo test -p pir-client

# All tests
cargo test --workspace
```

## Linting

```bash
cargo clippy --all-targets
cargo fmt --check
```

## Feature Flags

The `nf-server` crate uses feature flags to keep the default build lightweight:

| Feature | Effect |
|---------|--------|
| `serve` | Enables the `serve` subcommand (pulls in `pir-server`, `axum`, etc.) |
| `avx512` | Implies `serve`; compiles YPIR with AVX-512 intrinsics for ~2x query throughput |

The `avx512` feature requires nightly Rust and a CPU with AVX-512 support. CI builds use it for the deploy target but not for tests.

## Code Style

- Run `cargo fmt` before committing.
- Keep `cargo clippy --all-targets` warning-free.
- Public items should have `///` doc comments.
- Use `anyhow::Result` for fallible functions; use `.context()` to add meaningful error messages.

## License

By contributing, you agree that your contributions will be licensed under the [MIT License](LICENSE).
