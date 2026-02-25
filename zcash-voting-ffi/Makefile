SHELL := bash
.ONESHELL:
.SHELLFLAGS := -eu -o pipefail -c
.DELETE_ON_ERROR:
MAKEFLAGS += --warn-undefined-variables
MAKEFLAGS += --no-builtin-rules

LIB_NAME = zcash_voting_ffi
FFI_NAME = zcash_voting_ffiFFI

PLATFORMS = ios-device macos ios-simulator
IOS_DEVICE_ARCHS = aarch64-apple-ios
IOS_SIM_ARCHS = x86_64-apple-ios aarch64-apple-ios-sim
MACOS_ARCHS = x86_64-apple-darwin aarch64-apple-darwin

CARGO_MANIFEST = rust/Cargo.toml

# --- Main targets ---

.PHONY: dev dev-incr release clean bindings install sanitize-generated-whitespace

install:
	rustup target add aarch64-apple-ios aarch64-apple-ios-sim x86_64-apple-ios x86_64-apple-darwin aarch64-apple-darwin

# Incremental dev build: skips clean, reuses cached compilation artifacts.
# Use this for quick iteration (log changes, small fixes). ~30s–2min vs ~8min.
#
# dev-incr-sim: simulator only (fastest — skips ios-device build entirely).
#   Use when testing on the iOS simulator (the common case).
# dev-incr:     simulator + device (needed before running on a real iPhone).
dev-incr-sim:
	cargo build --manifest-path $(CARGO_MANIFEST) --target aarch64-apple-ios-sim --release
	cp rust/target/aarch64-apple-ios-sim/release/lib$(LIB_NAME).a releases/$(FFI_NAME).xcframework/ios-arm64_x86_64-simulator/$(FFI_NAME).framework/$(FFI_NAME)

dev-incr: dev-incr-sim
	cargo build --manifest-path $(CARGO_MANIFEST) --target aarch64-apple-ios --release
	cp rust/target/aarch64-apple-ios/release/lib$(LIB_NAME).a releases/$(FFI_NAME).xcframework/ios-arm64/$(FFI_NAME).framework/$(FFI_NAME)

# Dev build: Apple Silicon only (arm64 device + arm64 simulator + arm64 macOS)
dev: clean bindings
	cargo build --manifest-path $(CARGO_MANIFEST) --target aarch64-apple-ios --release
	cargo build --manifest-path $(CARGO_MANIFEST) --target aarch64-apple-ios-sim --release
	cargo build --manifest-path $(CARGO_MANIFEST) --target aarch64-apple-darwin --release
	# ios-device framework
	mkdir -p products/ios-device/frameworks/$(FFI_NAME).framework/Headers
	mkdir -p products/ios-device/frameworks/$(FFI_NAME).framework/Modules
	cp rust/target/aarch64-apple-ios/release/lib$(LIB_NAME).a products/ios-device/frameworks/$(FFI_NAME).framework/$(FFI_NAME)
	cp products/bindings/$(FFI_NAME).h products/ios-device/frameworks/$(FFI_NAME).framework/Headers/
	cp support/platform-Info.plist products/ios-device/frameworks/$(FFI_NAME).framework/Info.plist
	echo 'framework module $(FFI_NAME) {' > products/ios-device/frameworks/$(FFI_NAME).framework/Modules/module.modulemap
	echo '    umbrella header "$(FFI_NAME).h"' >> products/ios-device/frameworks/$(FFI_NAME).framework/Modules/module.modulemap
	echo '    export *' >> products/ios-device/frameworks/$(FFI_NAME).framework/Modules/module.modulemap
	echo '    module * { export * }' >> products/ios-device/frameworks/$(FFI_NAME).framework/Modules/module.modulemap
	echo '}' >> products/ios-device/frameworks/$(FFI_NAME).framework/Modules/module.modulemap
	# ios-simulator framework (arm64 only for dev)
	mkdir -p products/ios-simulator/frameworks/$(FFI_NAME).framework/Headers
	mkdir -p products/ios-simulator/frameworks/$(FFI_NAME).framework/Modules
	cp rust/target/aarch64-apple-ios-sim/release/lib$(LIB_NAME).a products/ios-simulator/frameworks/$(FFI_NAME).framework/$(FFI_NAME)
	cp products/bindings/$(FFI_NAME).h products/ios-simulator/frameworks/$(FFI_NAME).framework/Headers/
	cp support/platform-Info.plist products/ios-simulator/frameworks/$(FFI_NAME).framework/Info.plist
	echo 'framework module $(FFI_NAME) {' > products/ios-simulator/frameworks/$(FFI_NAME).framework/Modules/module.modulemap
	echo '    umbrella header "$(FFI_NAME).h"' >> products/ios-simulator/frameworks/$(FFI_NAME).framework/Modules/module.modulemap
	echo '    export *' >> products/ios-simulator/frameworks/$(FFI_NAME).framework/Modules/module.modulemap
	echo '    module * { export * }' >> products/ios-simulator/frameworks/$(FFI_NAME).framework/Modules/module.modulemap
	echo '}' >> products/ios-simulator/frameworks/$(FFI_NAME).framework/Modules/module.modulemap
	# macos framework (arm64 only for dev)
	mkdir -p products/macos/frameworks/$(FFI_NAME).framework/Headers
	mkdir -p products/macos/frameworks/$(FFI_NAME).framework/Modules
	cp rust/target/aarch64-apple-darwin/release/lib$(LIB_NAME).a products/macos/frameworks/$(FFI_NAME).framework/$(FFI_NAME)
	cp products/bindings/$(FFI_NAME).h products/macos/frameworks/$(FFI_NAME).framework/Headers/
	cp support/platform-Info.plist products/macos/frameworks/$(FFI_NAME).framework/Info.plist
	echo 'framework module $(FFI_NAME) {' > products/macos/frameworks/$(FFI_NAME).framework/Modules/module.modulemap
	echo '    umbrella header "$(FFI_NAME).h"' >> products/macos/frameworks/$(FFI_NAME).framework/Modules/module.modulemap
	echo '    export *' >> products/macos/frameworks/$(FFI_NAME).framework/Modules/module.modulemap
	echo '    module * { export * }' >> products/macos/frameworks/$(FFI_NAME).framework/Modules/module.modulemap
	echo '}' >> products/macos/frameworks/$(FFI_NAME).framework/Modules/module.modulemap
	# xcframework
	rm -rf releases/$(FFI_NAME).xcframework
	mkdir -p releases/$(FFI_NAME).xcframework/ios-arm64
	mkdir -p releases/$(FFI_NAME).xcframework/ios-arm64_x86_64-simulator
	mkdir -p releases/$(FFI_NAME).xcframework/macos-arm64_x86_64
	cp -R products/ios-device/frameworks/$(FFI_NAME).framework releases/$(FFI_NAME).xcframework/ios-arm64/
	cp -R products/ios-simulator/frameworks/$(FFI_NAME).framework releases/$(FFI_NAME).xcframework/ios-arm64_x86_64-simulator/
	cp -R products/macos/frameworks/$(FFI_NAME).framework releases/$(FFI_NAME).xcframework/macos-arm64_x86_64/
	cp support/Info.plist releases/$(FFI_NAME).xcframework/
	# Swift bindings
	mkdir -p Sources/ZcashVotingFFI
	cp products/bindings/$(LIB_NAME).swift Sources/ZcashVotingFFI/
	$(MAKE) sanitize-generated-whitespace

# Release build: all platforms
release: clean bindings
	# Build all targets
	cargo build --manifest-path $(CARGO_MANIFEST) --target aarch64-apple-ios --release
	cargo build --manifest-path $(CARGO_MANIFEST) --target aarch64-apple-ios-sim --release
	cargo build --manifest-path $(CARGO_MANIFEST) --target x86_64-apple-ios --release
	cargo build --manifest-path $(CARGO_MANIFEST) --target aarch64-apple-darwin --release
	cargo build --manifest-path $(CARGO_MANIFEST) --target x86_64-apple-darwin --release
	# ios-device framework
	mkdir -p products/ios-device/frameworks/$(FFI_NAME).framework/Headers
	mkdir -p products/ios-device/frameworks/$(FFI_NAME).framework/Modules
	cp rust/target/aarch64-apple-ios/release/lib$(LIB_NAME).a products/ios-device/frameworks/$(FFI_NAME).framework/$(FFI_NAME)
	cp products/bindings/$(FFI_NAME).h products/ios-device/frameworks/$(FFI_NAME).framework/Headers/
	cp support/platform-Info.plist products/ios-device/frameworks/$(FFI_NAME).framework/Info.plist
	echo 'framework module $(FFI_NAME) {' > products/ios-device/frameworks/$(FFI_NAME).framework/Modules/module.modulemap
	echo '    umbrella header "$(FFI_NAME).h"' >> products/ios-device/frameworks/$(FFI_NAME).framework/Modules/module.modulemap
	echo '    export *' >> products/ios-device/frameworks/$(FFI_NAME).framework/Modules/module.modulemap
	echo '    module * { export * }' >> products/ios-device/frameworks/$(FFI_NAME).framework/Modules/module.modulemap
	echo '}' >> products/ios-device/frameworks/$(FFI_NAME).framework/Modules/module.modulemap
	# ios-simulator framework (fat binary: arm64 + x86_64)
	mkdir -p products/ios-simulator/frameworks/$(FFI_NAME).framework/Headers
	mkdir -p products/ios-simulator/frameworks/$(FFI_NAME).framework/Modules
	lipo -create \
		rust/target/aarch64-apple-ios-sim/release/lib$(LIB_NAME).a \
		rust/target/x86_64-apple-ios/release/lib$(LIB_NAME).a \
		-output products/ios-simulator/frameworks/$(FFI_NAME).framework/$(FFI_NAME)
	cp products/bindings/$(FFI_NAME).h products/ios-simulator/frameworks/$(FFI_NAME).framework/Headers/
	cp support/platform-Info.plist products/ios-simulator/frameworks/$(FFI_NAME).framework/Info.plist
	echo 'framework module $(FFI_NAME) {' > products/ios-simulator/frameworks/$(FFI_NAME).framework/Modules/module.modulemap
	echo '    umbrella header "$(FFI_NAME).h"' >> products/ios-simulator/frameworks/$(FFI_NAME).framework/Modules/module.modulemap
	echo '    export *' >> products/ios-simulator/frameworks/$(FFI_NAME).framework/Modules/module.modulemap
	echo '    module * { export * }' >> products/ios-simulator/frameworks/$(FFI_NAME).framework/Modules/module.modulemap
	echo '}' >> products/ios-simulator/frameworks/$(FFI_NAME).framework/Modules/module.modulemap
	# macos framework (fat binary: arm64 + x86_64)
	mkdir -p products/macos/frameworks/$(FFI_NAME).framework/Headers
	mkdir -p products/macos/frameworks/$(FFI_NAME).framework/Modules
	lipo -create \
		rust/target/aarch64-apple-darwin/release/lib$(LIB_NAME).a \
		rust/target/x86_64-apple-darwin/release/lib$(LIB_NAME).a \
		-output products/macos/frameworks/$(FFI_NAME).framework/$(FFI_NAME)
	cp products/bindings/$(FFI_NAME).h products/macos/frameworks/$(FFI_NAME).framework/Headers/
	cp support/platform-Info.plist products/macos/frameworks/$(FFI_NAME).framework/Info.plist
	echo 'framework module $(FFI_NAME) {' > products/macos/frameworks/$(FFI_NAME).framework/Modules/module.modulemap
	echo '    umbrella header "$(FFI_NAME).h"' >> products/macos/frameworks/$(FFI_NAME).framework/Modules/module.modulemap
	echo '    export *' >> products/macos/frameworks/$(FFI_NAME).framework/Modules/module.modulemap
	echo '    module * { export * }' >> products/macos/frameworks/$(FFI_NAME).framework/Modules/module.modulemap
	echo '}' >> products/macos/frameworks/$(FFI_NAME).framework/Modules/module.modulemap
	# xcframework
	rm -rf releases/$(FFI_NAME).xcframework
	mkdir -p releases/$(FFI_NAME).xcframework/ios-arm64
	mkdir -p releases/$(FFI_NAME).xcframework/ios-arm64_x86_64-simulator
	mkdir -p releases/$(FFI_NAME).xcframework/macos-arm64_x86_64
	cp -R products/ios-device/frameworks/$(FFI_NAME).framework releases/$(FFI_NAME).xcframework/ios-arm64/
	cp -R products/ios-simulator/frameworks/$(FFI_NAME).framework releases/$(FFI_NAME).xcframework/ios-arm64_x86_64-simulator/
	cp -R products/macos/frameworks/$(FFI_NAME).framework releases/$(FFI_NAME).xcframework/macos-arm64_x86_64/
	cp support/Info.plist releases/$(FFI_NAME).xcframework/
	# Swift bindings
	mkdir -p Sources/ZcashVotingFFI
	cp products/bindings/$(LIB_NAME).swift Sources/ZcashVotingFFI/
	$(MAKE) sanitize-generated-whitespace

# Generate Swift bindings from a host debug build
bindings:
	cd rust && cargo build
	mkdir -p products/bindings
	cd rust && cargo run --bin uniffi-bindgen generate \
		--library target/debug/lib$(LIB_NAME).dylib \
		--language swift \
		--out-dir ../products/bindings

sanitize-generated-whitespace:
	shopt -s nullglob
	for file in products/bindings/*.swift products/bindings/*.h Sources/ZcashVotingFFI/*.swift releases/$(FFI_NAME).xcframework/*/$(FFI_NAME).framework/Headers/*.h; do \
		perl -pi -e 's/[ \t]+$$//' "$$file"; \
	done

clean:
	rm -rf products
	rm -rf rust/target
