# zcash-voting-ffi

UniFFI bridge that exposes `librustvoting` to iOS as a Swift package with a prebuilt xcframework.

## Architecture

```
librustvoting (Rust crate)
    -> zcash-voting-ffi/rust/ (UniFFI wrapper crate, #[uniffi::export])
    -> uniffi-bindgen generates:
        - zcash_voting_ffiFFI.h (C header)
        - zcash_voting_ffi.swift (Swift bindings)
    -> xcframework (iOS device + simulator + macOS)
    -> Swift Package (ZcashVotingFFI target)
```

The key FFI type is `VotingDatabase` — a stateful UniFFI object that owns the SQLite connection and exposes the full round lifecycle as methods. Swift creates one instance via `VotingDatabase.open(path:)` and holds it in a `DatabaseActor` for thread safety.

`ProofProgressReporter` is a UniFFI callback interface that bridges Rust progress updates into Swift's `AsyncThrowingStream<ProofEvent>`.

## Rebuilding After Rust Changes

When you modify `librustvoting` or the FFI wrapper:

```bash
cd zcash-voting-ffi

# Dev build (Apple Silicon only — fast iteration)
make dev

# Release build (all architectures — for CI/distribution)
make release
```

Both targets:

1. Generate Swift bindings via `uniffi-bindgen`
2. Cross-compile for each target triple
3. Package into `releases/zcash_voting_ffiFFI.xcframework`
4. Copy generated Swift to `Sources/ZcashVotingFFI/`

After rebuilding, Xcode picks up the changes on next build (the xcframework is referenced as a binary target in `Package.swift`).

## Make Targets

| Target          | What it does                                                           |
| --------------- | ---------------------------------------------------------------------- |
| `make dev`      | arm64 only (device + simulator + macOS). Fast for local development    |
| `make release`  | All architectures. Uses `lipo` for universal binaries (arm64 + x86_64) |
| `make bindings` | Generate Swift bindings only (no cross-compilation)                    |
| `make install`  | `rustup target add` for all required triples                           |
| `make clean`    | Remove `products/` and `rust/target/`                                  |

## Adding New FFI Surface

1. Add the function/type to `librustvoting`
2. Expose it in `zcash-voting-ffi/rust/src/lib.rs` with `#[uniffi::export]` or `#[derive(uniffi::Record)]`
3. Run `make dev`
4. Use the generated type in Swift — it appears in `ZcashVotingFFI` module automatically

## Platforms

| Platform      | Triples                                       |
| ------------- | --------------------------------------------- |
| iOS device    | `aarch64-apple-ios`                           |
| iOS simulator | `aarch64-apple-ios-sim`, `x86_64-apple-ios`   |
| macOS         | `aarch64-apple-darwin`, `x86_64-apple-darwin` |
