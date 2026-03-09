# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Protocol Documentation Reference

**Always consult the documentation files first** — Claude's training data may lack sufficient detail on these specific protocols.

### Shielded Vote Protocol Spec (shielded_vote_book)

The canonical protocol specification lives in the `shielded_vote_book` Obsidian vault, symlinked into the repo root. **Before reading the full book, start with the AI index:**

- `docs/shielded-vote-book-index.md` — Structural index with per-file summaries (read this FIRST)
- `shielded_vote_book/` — Full book (read specific files identified from the index)

If `shielded_vote_book/` does not exist, prompt the user: _"The `shielded_vote_book` symlink is missing. Run `ln -s /path/to/your/shielded_vote_book shielded_vote_book` from the repo root. See `docs/ai_setup.md`."_

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

All workflow commands go through [mise](https://mise.jdx.dev). Run `mise tasks` to see everything, or `mise tasks --hidden` for internal tasks too. Tasks are thin wrappers over sub-Makefiles (`sdk/Makefile`) and the sibling `vote-nullifier-pir` repo (`../vote-nullifier-pir/Makefile`).

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
- **`start:*`** — `start:chain` (chain-only, no nullifiers — used by CI)
- **`chain:*`** — `chain:init`, `chain:start`, `chain:clean`, `chain:ceremony`
- **`multi:*`** — `multi:init`, `multi:start`, `multi:start-chain`, `multi:restart`, `multi:stop`, `multi:status`, `multi:clean`
- **`nullifier:*`** — `nullifier:bootstrap`, `nullifier:ingest`, `nullifier:export`, `nullifier:serve`, `nullifier:status`, `nullifier:clean` (delegate to `../vote-nullifier-pir`)
- **`test:*`** — `test:unit`, `test:integration`, `test:helper`, `test:go`, `test:circuits`, `test:ffi`, `test:nullifier`, `test:proof`
- **Flat** — `fmt`, `lint`, `fixtures`, `proto`, `validator:join`

### Full local sequence

1. `mise start` — inits chain, bootstraps + ingests + exports nullifiers, starts svoted + PIR server
2. `mise ui` (separate terminal) — starts admin UI on port 5173
3. Create and publish a round in the admin UI → ceremony runs automatically (PENDING → ACTIVE)
4. Rebuild iOS app in Xcode and run

### Architecture: PIR server owns all nullifier endpoints

The PIR server (`nf-server serve`, port 3000 locally, from the `vote-nullifier-pir` repo) is the **sole provider** of nullifier data — snapshot status, tree root, rebuild triggers, and PIR queries all live on the PIR server. Clients (admin UI, iOS wallet) talk to the PIR server directly. The chain node (svoted) does **not** proxy nullifier endpoints — it only uses the PIR server internally when fetching snapshot data for session creation (`/shielded-vote/v1/snapshot-data/{height}`). In local dev, the admin UI's Vite proxy routes `/nullifier/*` to the PIR server (stripping the prefix).

### Nullifier ingest (`nf-server`)

The `nf-server` binary and all nullifier crates live in the **separate `vote-nullifier-pir` repo** (`git@github.com:valargroup/vote-nullifier-pir.git`), expected at `../vote-nullifier-pir/`. The `nullifier:*` mise tasks delegate to its Makefile.

`nf-server` has three subcommands: `ingest`, `export`, and `serve`. The `serve` subcommand requires `--features serve` (enabled automatically by `make serve`). For production AVX-512 acceleration, the deploy workflow additionally enables `--features avx512`.

Data files (`nullifiers.bin`, `nullifiers.checkpoint`) are stored at the `vote-nullifier-pir/` root. PIR tier files go in `vote-nullifier-pir/pir-data/`. For manual operations use `make -C ../vote-nullifier-pir`:

- `SYNC_HEIGHT` must be a **multiple of 10**
- The full pipeline is **ingest → export → serve**. After re-ingesting nullifiers, you must re-export before the server sees the new data: `make ingest-resync` (deletes stale tier files), then `make export-nf`, then `make serve`
- `eprintln!` from Rust code shows up in the Xcode debug console when testing the iOS app

### Important: `make -C sdk install-ffi` vs `make -C sdk install`

- **`install-ffi`** builds with halo2 + redpallas. The helper server is **functional**. Always use this.
- **`install`** builds without FFI. Votes from the iOS app fail with HTTP 503.
- `mise start` calls `make -C sdk init` which uses `install-ffi`, so fresh starts are fine. The issue arises when you manually run `make -C sdk install` for a quick Go rebuild — this silently downgrades the binary.

### Ceremony

The EA key ceremony is automatic per voting round. When a round is published, eligible validators are snapshotted and the ceremony proceeds via PrepareProposal (auto-deal + auto-ack). Pallas key registration happens at validator join time. Jailing is handled by the standard `x/slashing` module for block-miss detection (no ceremony-miss jailing). Unjail via standard `MsgUnjail`.

## Protocol Documentation

When modifying circuit logic (in `orchard/`, `librustvoting/`, `sdk/circuits/`, or `vote-nullifier-pir`), the corresponding documentation in the `shielded_vote_book` must also be updated. The book is served live and describes the circuit structure — any protocol change that affects conditions, public inputs, witness fields, or hash parameters must be reflected there.

**Keeping the index accurate:** `docs/shielded-vote-book-index.md` is a committed summary of the book's structure. When you read a file from `shielded_vote_book/` and notice it has moved, been renamed, has new files nearby that aren't in the index, or the content no longer matches the index summary — update the index immediately. Also update the index when writing to the book (new files, restructured sections, significantly changed content). The goal: a future query that reads only the index should get an accurate picture of what's in the book and where to find it.

## Claude Code Workflow Rules

- **Never create a voting round automatically.** When restarting the local chain, only build, init, start svoted, register the Pallas key, and write the iOS config. Do not run `make -C sdk ceremony` or the `round_activation` test — these create a voting round. Only do so if the user explicitly asks.

## Code Change Guidelines

**Never consider backwards compatibility** unless explicitly told to do so. Feel free to rename functions, change APIs, delete unused code, and refactor without worrying about breaking existing consumers. This is a research codebase where clean code matters more than stability.

- Describe changes relative to main branch, not iterative development steps
- Include benchmark results if touching performance-critical code
- Security-impacting changes require extra review
- PR description should reflect actual diff (what exists in main vs PR branch)
