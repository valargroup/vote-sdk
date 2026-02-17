BINARY = zallyd
HOME_DIR = $(HOME)/.zallyd

.PHONY: install install-ffi init init-ffi start clean build build-ffi fmt lint test test-unit test-integration test-helper ceremony test-api test-api-restart test-api-reinit test-e2e test-ceremony-e2e fixtures-ts circuits fixtures test-halo2 test-halo2-ante test-redpallas test-redpallas-ante test-all-ffi init-multi stop-multi clean-multi

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

## init-multi: Initialize a 3-validator chain on localhost (wipes existing data)
init-multi: install
	bash scripts/init_multi.sh

## stop-multi: Stop all multi-validator processes
stop-multi:
	@if [ -f $(HOME)/.zallyd-multi-pids ]; then \
		echo "Stopping validators..."; \
		while read pid; do \
			kill "$$pid" 2>/dev/null && echo "  Killed PID $$pid" || echo "  PID $$pid already stopped"; \
		done < $(HOME)/.zallyd-multi-pids; \
		rm -f $(HOME)/.zallyd-multi-pids; \
	else \
		echo "No PID file found at $(HOME)/.zallyd-multi-pids"; \
	fi

## clean-multi: Remove all multi-validator data directories
clean-multi: stop-multi
	rm -rf $(HOME)/.zallyd-val1 $(HOME)/.zallyd-val2 $(HOME)/.zallyd-val3

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

## ceremony: Bootstrap the EA key ceremony on a running chain (requires: make init && make start)
ceremony:
	ZALLY_API_URL=http://localhost:1318 cargo test --release --manifest-path ../e2e-tests/Cargo.toml ceremony_bootstrap -- --nocapture --ignored

## test-api: Rust E2E API tests against a running chain (requires: make init && make start)
test-api:
	ZALLY_API_URL=http://localhost:1318 ZALLY_EA_PK_PATH=$(HOME)/.zallyd/ea.pk HELPER_SERVER_URL=http://127.0.0.1:1318 cargo test --release --manifest-path ../e2e-tests/Cargo.toml -- --nocapture --ignored --skip ceremony_lifecycle_multi_validator

## test-e2e: Alias for test-api (Rust E2E tests)
test-e2e: test-api

## test-ceremony-e2e: Rust E2E ceremony lifecycle test against a running 3-validator chain (requires: make init-multi)
test-ceremony-e2e:
	ZALLY_API_URL=http://localhost:1418 cargo test --release --manifest-path ../e2e-tests/Cargo.toml ceremony_lifecycle_multi_validator -- --nocapture --ignored

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
