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

## Local Development

All workflow commands go through [mise](https://mise.jdx.dev). Run `mise tasks` to see everything, or `mise tasks --hidden` for internal tasks too. Tasks are thin wrappers over sub-Makefiles (`sdk/Makefile`, `nullifier-ingest/Makefile`).

### Setup and daily workflow

```
mise install        # pin Go 1.24.1, Rust stable, Node 22
mise start          # init chain + bootstrap nullifiers + start everything
mise status         # check service health + voting round state
mise ui             # admin UI dev server (port 5173, separate terminal)
mise stop           # stop all services
mise test           # end-to-end tests against running chain
```

### Key namespaces

- **`build:*`** — `build`, `build:quick`, `build:install`, `build:circuits`, `build:ui`
- **`chain:*`** — `chain:init`, `chain:start`, `chain:clean`, `chain:ceremony`
- **`multi:*`** — `multi:init`, `multi:start`, `multi:stop`, `multi:status`, `multi:clean`
- **`nullifier:*`** — `nullifier:bootstrap`, `nullifier:ingest`, `nullifier:export`, `nullifier:serve`, `nullifier:status`, `nullifier:clean`
- **`test:*`** — `test:unit`, `test:integration`, `test:helper`, `test:go`, `test:circuits`, `test:ffi`, `test:nullifier`, `test:proof`
- **Flat** — `fmt`, `lint`, `fixtures`, `proto`, `validator:join`

### Full local sequence

1. `mise start` — inits chain, bootstraps + ingests + exports nullifiers, starts zallyd + PIR server
2. `mise ui` (separate terminal) — starts admin UI on port 5173
3. Create and publish a round in the admin UI → ceremony runs automatically (PENDING → ACTIVE)
4. Rebuild iOS app in Xcode and run

### Architecture: PIR server owns all nullifier endpoints

The PIR server (`nf-server serve`, port 3000 locally) is the **sole provider** of nullifier data — snapshot status, tree root, rebuild triggers, and PIR queries all live on the PIR server. Clients (admin UI, iOS wallet) talk to the PIR server directly. The chain node (zallyd) does **not** proxy nullifier endpoints — it only uses the PIR server internally when fetching snapshot data for session creation (`/zally/v1/snapshot-data/{height}`). In local dev, the admin UI's Vite proxy routes `/nullifier/*` to the PIR server (stripping the prefix).

### Nullifier ingest (`nf-server`)

The unified `nf-server` binary lives in `nullifier-ingest/nf-server/` and has three subcommands: `ingest`, `export`, and `serve`. The `serve` subcommand requires `--features serve` (enabled automatically by `make serve`). For production AVX-512 acceleration, the deploy workflow additionally enables `--features avx512`.

Data files (`nullifiers.bin`, `nullifiers.checkpoint`) are stored at the `nullifier-ingest/` root. PIR tier files go in `nullifier-ingest/pir-data/`. For manual operations use `make -C nullifier-ingest`:

- `SYNC_HEIGHT` must be a **multiple of 10**
- The full pipeline is **ingest → export → serve**. After re-ingesting nullifiers, you must re-export before the server sees the new data: `make ingest-resync` (deletes stale tier files), then `make export-nf`, then `make serve`
- `eprintln!` from Rust code shows up in the Xcode debug console when testing the iOS app

### Important: `make -C sdk install-ffi` vs `make -C sdk install`

- **`install-ffi`** builds with halo2 + redpallas. The helper server is **functional**. Always use this.
- **`install`** builds without FFI. Votes from the iOS app fail with HTTP 503.
- `mise start` calls `make -C sdk init` which uses `install-ffi`, so fresh starts are fine. The issue arises when you manually run `make -C sdk install` for a quick Go rebuild — this silently downgrades the binary.

### Ceremony

The EA key ceremony is automatic per voting round. When a round is published, eligible validators are snapshotted and the ceremony proceeds via PrepareProposal (auto-deal + auto-ack). Pallas key registration happens at validator join time. Validators who fail to ack in 3 consecutive ceremonies are jailed.

## Protocol Documentation

When modifying circuit logic (in `orchard/`, `librustvoting/`, or `sdk/circuits/`), the corresponding documentation in the Obsidian gitbook (the `shielded_vote_book` repository) must also be updated. The book is served live and describes the circuit structure — any protocol change that affects conditions, public inputs, witness fields, or hash parameters must be reflected there.

## Code Change Guidelines

**Never consider backwards compatibility** unless explicitly told to do so. Feel free to rename functions, change APIs, delete unused code, and refactor without worrying about breaking existing consumers. This is a research codebase where clean code matters more than stability.

- Describe changes relative to main branch, not iterative development steps
- Include benchmark results if touching performance-critical code
- Security-impacting changes require extra review
- PR description should reflect actual diff (what exists in main vs PR branch)
