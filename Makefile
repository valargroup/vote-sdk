BINARY = zallyd
HOME_DIR = $(HOME)/.zallyd

export GOBIN := $(HOME)/go/bin
export PATH := $(GOBIN):$(PATH)

.PHONY: install install-ffi init start clean build build-ffi build-create-val-tx install-create-val-tx fmt lint test test-unit test-integration test-helper ceremony test-api test-api-restart test-api-reinit test-e2e test-ceremony-e2e fixtures-ts circuits fixtures test-halo2 test-halo2-ante test-redpallas test-redpallas-ante test-all-ffi caddy

## install: Build and install the zallyd binary to $GOPATH/bin
install:
	go install ./cmd/zallyd

## install-ffi: Build and install zallyd with real RedPallas + Halo2 verification (requires: make circuits)
install-ffi: circuits
	go install -tags "halo2,redpallas" ./cmd/zallyd

## build: Build the zallyd binary locally
build:
	go build -o $(BINARY) ./cmd/zallyd

## build-ffi: Build zallyd with real RedPallas + Halo2 (requires: make circuits). Use this or run "make circuits" before go build -tags halo2,redpallas.
build-ffi: circuits
	go build -tags "halo2,redpallas" -o $(BINARY) ./cmd/zallyd

## build-create-val-tx: Build the create-val-tx helper binary locally
build-create-val-tx:
	go build -o create-val-tx ./scripts/create-val-tx

## install-create-val-tx: Install create-val-tx to $GOBIN
install-create-val-tx:
	go install ./scripts/create-val-tx

## init: Initialize a single-validator chain with real RedPallas + Halo2 verification (wipes existing data)
init: install-ffi
	bash scripts/init.sh

## start: Start the chain (set ZALLY_PIR_URL to override nullifier PIR server)
start:
	ZALLY_PIR_URL=$${ZALLY_PIR_URL:-http://localhost:3000} $(BINARY) start --home $(HOME_DIR)

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

## test-helper: Helper server unit tests (SQLite store, API, processor)
test-helper:
	go test -count=1 -race ./internal/helper/...

## test: Run all tests (Go only, no Rust dependency)
test: test-unit test-integration test-helper

## ceremony: Register Pallas key + create round + wait for ACTIVE (per-round auto-ceremony)
ceremony:
	ZALLY_API_URL=http://localhost:1318 cargo test --release --manifest-path ../e2e-tests/Cargo.toml round_activation -- --nocapture --ignored

## test-api: Rust E2E API tests against a running chain (requires: make init && make start)
test-api:
	ZALLY_API_URL=http://localhost:1318 HELPER_SERVER_URL=http://127.0.0.1:1318 cargo test --release --manifest-path ../e2e-tests/Cargo.toml -- --nocapture --ignored

## test-e2e: Alias for test-api (Rust E2E tests)
test-e2e: test-api

## test-api-restart: init + test-api (full API test cycle; chain must be stopped first)
test-api-restart: init test-api

## test-api-reinit: init + fixtures only (no test-api)
test-api-reinit: init fixtures

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

# ---------------------------------------------------------------------------
# Deployment targets
# ---------------------------------------------------------------------------

## caddy: Install Caddyfile and restart Caddy (HTTPS reverse proxy for the chain API)
caddy:
	sudo cp deploy/Caddyfile /etc/caddy/Caddyfile
	sudo systemctl restart caddy
	@echo "Caddy restarted — HTTPS at https://46-101-255-48.sslip.io"
