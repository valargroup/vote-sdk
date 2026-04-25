# Contributing

Project overview, protocol notes, and architecture live in the [README](README.md). This document covers **working from a source checkout**: toolchains, mise tasks, local chain runs, and exercising the validator join path with binaries you built locally.

## Prerequisites

- [mise](https://mise.jdx.dev/) — installs Go, Rust, Node, buf, and jq from `mise.toml`, runs repo tasks, and puts `$GOBIN` (default `~/go/bin`) on `PATH` when the shell is activated.

```bash
mise install   # toolchain only (not the same as mise run install)
```

## Local chain (quick dev loop)

1. **Install vote binaries to `$GOBIN`** (FFI build: Halo2 + RedPallas):

   ```bash
   mise run install
   ```

   `mise install` only installs the tools listed under `[tools]` in `mise.toml`. **`mise run install`** runs the `install` task and builds plus installs `svoted` and `create-val-tx`.

2. **Vote-manager keys** — one-time; same as README Quick Start:

   ```bash
   cp .env.example .env
   # Set VM_PRIVKEYS (comma-separated 64-char hex; openssl rand -hex 32 per key)
   ```

3. **Reset and init a single-validator devnet**, then start the daemon:

   ```bash
   mise run chain:init
   mise run chain:start
   ```

   For a three-validator local net: `mise run chain:init-multi` and `mise run chain:start-multi`.

4. **Discover tasks** — descriptions live in `mise.toml`:

   ```bash
   mise tasks
   ```

   Common tasks include `test:go`, `test:circuits`, `test:halo2`, `test:ffi`, `fmt`, `lint`, and `proto`.

## Join flow with local binaries

To run the same [join.sh](join.sh) path the live runbook uses, but **without** downloading release tarballs from Spaces, build first (`mise run install`), then from the repo root:

```bash
SVOTE_LOCAL_BINARIES=1 ./join.sh
```

That uses `svoted` and `create-val-tx` already on your `PATH` (for example under `$GOBIN`). For full production-style joining (downloads, Caddy, services), see [docs/runbooks/join-chain.md](docs/runbooks/join-chain.md).

## Without mise

Use Make targets directly; see README **Without mise (direct make)** for `make circuits`, `make build-ffi`, `make init`, and `make start`.
