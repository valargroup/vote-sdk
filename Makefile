BINARY = zallyd
HOME_DIR = $(HOME)/.zallyd

.PHONY: install install-ffi init init-ffi start clean build fmt lint test test-unit test-integration test-api fixtures-ts circuits fixtures test-halo2 test-halo2-ante test-redpallas test-redpallas-ante test-all-ffi

## install: Build and install the zallyd binary to $GOPATH/bin
install:
	go install ./cmd/zallyd

## install-ffi: Build and install zallyd with real RedPallas + Halo2 verification (requires: make circuits)
install-ffi: circuits
	go install -tags "halo2,redpallas" ./cmd/zallyd

## build: Build the zallyd binary locally
build:
	go build -o $(BINARY) ./cmd/zallyd

## init: Initialize a single-validator chain (wipes existing data)
init: install
	bash scripts/init.sh

## init-ffi: Initialize chain with real RedPallas + Halo2 verification (wipes existing data)
init-ffi: install-ffi
	bash scripts/init.sh

## start: Start the chain
start:
	$(BINARY) start --home $(HOME_DIR)

## clean: Remove chain data directory
clean:
	rm -rf $(HOME_DIR)
	rm -f $(BINARY)

## fmt: Format Go code
fmt:
	go fmt ./...

## lint: Run Go vet
lint:
	go vet ./...

## test-unit: Keeper, validation, codec, module unit tests (fast, parallel)
test-unit:
	go test -count=1 -race -parallel=4 ./x/vote/... ./api/...

## test-integration: Full ABCI pipeline integration tests (in-process chain)
test-integration:
	go test -count=1 -race -timeout 5m ./app/...

## test: Run all tests (Go only, no Rust dependency)
test: test-unit test-integration

## test-api: TypeScript API tests against a running chain (requires: make start)
test-api:
	cd tests/api && npm test

## fixtures-ts: Copy Halo2 proof fixtures into TS test directory (requires: make fixtures)
fixtures-ts: fixtures
	mkdir -p tests/api/fixtures
	cp crypto/zkp/testdata/toy_valid_proof.bin tests/api/fixtures/
	cp crypto/zkp/testdata/toy_valid_input.bin tests/api/fixtures/

# ---------------------------------------------------------------------------
# Rust circuit / FFI targets
# ---------------------------------------------------------------------------

## circuits: Build the Rust static library (requires cargo)
circuits:
	cargo build --release --manifest-path circuits/Cargo.toml

## circuits-test: Run Rust circuit unit tests
circuits-test:
	cargo test --release --manifest-path circuits/Cargo.toml

## fixtures: Regenerate all fixture files (Halo2 + RedPallas) (requires circuits build)
fixtures: circuits
	cargo test --release --manifest-path circuits/Cargo.toml -- generate_fixtures --ignored --nocapture

## test-halo2: Run Go tests that use real Halo2 verification via CGo (requires circuits)
test-halo2: circuits
	go test -tags halo2 -count=1 -v ./crypto/zkp/halo2/... ./x/vote/ante/...

## test-halo2-ante: Run ante handler tests with real Halo2 verification
test-halo2-ante: circuits
	go test -tags halo2 -count=1 -v ./x/vote/ante/... -run TestHalo2

## test-redpallas: Run Go tests with real RedPallas signature verification via CGo (requires circuits)
test-redpallas: circuits
	go test -tags redpallas -count=1 -v ./crypto/redpallas/... ./x/vote/ante/...

## test-redpallas-ante: Run ante handler tests with real RedPallas verification
test-redpallas-ante: circuits
	go test -tags redpallas -count=1 -v ./x/vote/ante/... -run TestRedPallas

## test-all-ffi: Run all FFI-backed tests (Halo2 + RedPallas) (requires circuits)
test-all-ffi: circuits
	go test -tags "halo2 redpallas" -count=1 -v ./crypto/zkp/halo2/... ./crypto/redpallas/... ./x/vote/ante/...
