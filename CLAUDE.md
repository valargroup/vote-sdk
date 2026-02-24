# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Protocol Documentation Reference

**Always consult the documentation files first** — Claude's training data may lack sufficient detail on these specific protocols.

### Zcash Protocol Spec

- `docs/papers/zcash-protocol-index.md` - Section index with line ranges (read this FIRST)
- `docs/papers/zcash-protocol.tex` - Full protocol spec in LaTeX (read specific line ranges from index)

## Code Style

- Prefer explicit type annotations for public APIs
- Use `#[must_use]` on functions returning Results
- Avoid `unwrap()` in library code, use `expect()` with message or propagate errors
- Do not delete existing comments unless they are factually wrong or reference removed code. When modifying code near comments, preserve them. When adding new code alongside existing commented code, add comments for the new code at the same level of detail.

## Debugging

- Add `dbg!()` for quick inspection (remove before commit)
- Run single test with output: `cargo test test_name -- --nocapture`
- Check NTT correctness: compare against naive polynomial multiplication in tests

## Pull Request Guidelines

When writing PR descriptions, always describe changes relative to the target branch (e.g., main), not the iterative steps taken during development. The PR description should reflect the actual diff - what code exists in the target branch vs what code exists in the PR branch. Avoid phrases like "renamed X to Y" unless X actually exists in the target branch.

**After pushing to a branch**, always check if there is an open PR for that branch. If so, review whether the PR title and description still accurately reflect the current state of the changes. If the pushed commits have changed the scope or nature of the PR, update the title and/or description accordingly using `gh pr edit`.

## Database Migrations

Do not create new migration files (e.g., `002_*.sql`). This is a pre-production codebase — modify the existing `001_init.sql` directly. Only create separate migrations if explicitly asked.

## FFI Builds

There are two xcframework build targets in `zcash-voting-ffi/`:

- **`make dev-incr`** — Incremental build (~30s–2min). Use for Rust-only changes that don't touch the FFI interface (e.g., adding logs, fixing logic in `librustvoting` or `orchard`, tweaking circuit code). Skips clean and bindings regeneration, just recompiles changed crates and copies the `.a` into the xcframework.

- **`make dev`** — Full clean rebuild (~8min). Use when:
  - The FFI public API changed (`zcash-voting-ffi/rust/src/lib.rs`, uniffi exports, types exposed through FFI)
  - You need to regenerate Swift bindings (`Sources/ZcashVotingFFI/zcash_voting_ffi.swift`)
  - The incremental build produces errors (stale artifacts)

After modifying the FFI public API, you **must** run `make dev` and commit the regenerated Swift file and xcframework binaries alongside the Rust changes.

## Nullifier Ingest (`nf-server`)

The unified `nf-server` binary lives in `nullifier-ingest/nf-server/` and has three subcommands: `ingest`, `export`, and `serve`. The `serve` subcommand requires `--features serve` (enabled automatically by `make serve`). For production AVX-512 acceleration, the deploy workflow additionally enables `--features avx512`.

Data files (`nullifiers.bin`, `nullifiers.checkpoint`) are stored at the `nullifier-ingest/` root, not in subdirectories.

### Common operations (run from `nullifier-ingest/`):

- **Check status:** `make status`
- **Ingest to a specific height:** `make ingest SYNC_HEIGHT=<height>` (must be a multiple of 10)
- **Ingest to chain tip:** `make ingest`
- **Export PIR tier files:** `make export-nf`
- **Start PIR server:** `make serve` (runs on port 3000)
- **Bootstrap from CDN:** `make bootstrap` (downloads pre-built snapshot files if not present)

### Key notes:

- `SYNC_HEIGHT` must be a **multiple of 10**
- The full pipeline is **ingest → export → serve**. After re-ingesting nullifiers, you must re-export before the server sees the new data: `make ingest-resync SYNC_HEIGHT=<height>` (deletes stale sidecar/tier files), then `make export-nf`, then `make serve`
- `eprintln!` from Rust code shows up in the Xcode debug console when testing the iOS app

## Local Chain Setup

Starting all services for local development: `make up` from the repo root. This starts the chain (`zallyd`), bootstraps + ingests nullifiers, exports PIR tier files, and starts the PIR server.

### Full local setup sequence

The correct sequence to start everything from scratch:

1. `make up` (from repo root) — inits chain, bootstraps + ingests nullifiers, starts zallyd + IMT query server
2. `make ceremony` (from `sdk/`) — registers Pallas key + creates a round + waits for it to become ACTIVE (auto-ceremony)
3. `npm run dev` (from `shielded_vote_generator_ui/`) — starts admin UI on port 5173
4. Rebuild iOS app in Xcode and run

To override the PIR server URL: `ZALLY_PIR_URL=http://host:port make start`

### Important: `make install-ffi` vs `make install`

- **`make install`** builds `zallyd` **without** halo2/redpallas support. The embedded helper server will be **disabled** (logs: "helper server disabled: binary built without halo2 support"). Votes submitted from the iOS app will fail with **HTTP 503 "helper unavailable"**.
- **`make install-ffi`** builds `zallyd` **with** halo2 and redpallas build tags. This is required for the helper server to run. **Always use `make install-ffi`** when rebuilding `zallyd` for local testing.
- `make init` already calls `install-ffi`, so a fresh `make up` is fine. The issue arises when you manually run `make install` to pick up a Go code change — this silently downgrades the binary.

### GOBIN and version managers (mise, asdf, etc.)

The Makefiles set `export GOBIN := $(HOME)/go/bin` so that `go install` puts the binary in `~/go/bin`, matching the `PATH` they export. If you use a Go version manager like mise that overrides `GOBIN` to its own directory, the Makefile's explicit `GOBIN` takes precedence, preventing stale binaries in `~/go/bin` from shadowing the freshly built one.

### Ceremony

The EA key ceremony is automatic per voting round. When a round is created, eligible validators are snapshotted and the ceremony proceeds via PrepareProposal (auto-deal + auto-ack). The only manual prerequisite is Pallas key registration (done by `make ceremony`). Validators who fail to ack in 3 consecutive ceremonies are jailed.

To bootstrap the first round locally: `make ceremony` from `sdk/` after `make up`.

## Protocol Documentation

When modifying circuit logic (in `orchard/`, `librustvoting/`, or `sdk/circuits/`), the corresponding documentation in the Obsidian gitbook (the `shielded_vote_book` repository) must also be updated. The book is served live and describes the circuit structure — any protocol change that affects conditions, public inputs, witness fields, or hash parameters must be reflected there.

## Code Change Guidelines

**Never consider backwards compatibility** unless explicitly told to do so. Feel free to rename functions, change APIs, delete unused code, and refactor without worrying about breaking existing consumers. This is a research codebase where clean code matters more than stability.

- Describe changes relative to main branch, not iterative development steps
- Include benchmark results if touching performance-critical code
- Security-impacting changes require extra review
- PR description should reflect actual diff (what exists in main vs PR branch)
