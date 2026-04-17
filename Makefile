BINARY = svoted
HOME_DIR := $(or $(SVOTED_HOME),$(HOME)/.svoted)

export GOBIN := $(HOME)/go/bin
export PATH := $(GOBIN):$(PATH)

VERSION := $(or $(VERSION),$(shell git describe --tags --always --dirty 2>/dev/null || echo "dev"))
COMMIT  := $(shell git rev-parse HEAD 2>/dev/null || echo "unknown")
BUILD_TAGS_LIST := $(if $(BUILD_TAGS),$(BUILD_TAGS),)

# PIR embedding is ON by default: every FFI build bundles the nf-server binary
# into svoted via go:embed so `svoted start --serve-pir` works out of the box,
# and running `chain:init` / `chain:init-multi` can never silently ship a stub
# build. Set EMBED_PIR=0 to skip the Rust nf-server build when iterating on
# non-PIR code (svoted still builds, but --serve-pir will return ErrNotEmbedded).
EMBED_PIR ?= 1

FFI_TAGS := halo2,redpallas
ifneq ($(EMBED_PIR),0)
FFI_TAGS := $(FFI_TAGS),embed_pir
FFI_PIR_PREREQ := pir-binary
else
FFI_PIR_PREREQ :=
endif

VERSION_PKG := github.com/cosmos/cosmos-sdk/version
LDFLAGS := -X $(VERSION_PKG).Name=shielded-vote \
           -X $(VERSION_PKG).AppName=svoted \
           -X $(VERSION_PKG).Version=$(VERSION) \
           -X $(VERSION_PKG).Commit=$(COMMIT) \
           -X "$(VERSION_PKG).BuildTags=$(BUILD_TAGS_LIST)"

.PHONY: install install-ffi init init-multi init-benchmark start start-multi clean clean-all build build-ffi build-create-val-tx install-create-val-tx fmt lint test test-unit test-integration test-helper ceremony test-api test-api-restart test-api-reinit test-e2e test-ceremony-e2e fixtures-ts circuits fixtures test-halo2 test-halo2-ante test-redpallas test-redpallas-ante test-all-ffi caddy docker-build docker-testnet docker-testnet-down ui-build start-admin pir-binary pir-source

## install: Build and install the svoted binary to $GOPATH/bin
install:
	go install -ldflags '$(LDFLAGS)' ./cmd/svoted

## install-ffi: Build and install svoted with Halo2 + RedPallas + embedded PIR (set EMBED_PIR=0 to skip the nf-server build)
install-ffi: circuits $(FFI_PIR_PREREQ)
	go install -tags "$(FFI_TAGS)" -ldflags '$(LDFLAGS)' ./cmd/svoted

## build: Build the svoted binary locally
build:
	go build -ldflags '$(LDFLAGS)' -o $(BINARY) ./cmd/svoted

## build-ffi: Build svoted locally with Halo2 + RedPallas + embedded PIR (set EMBED_PIR=0 to skip the nf-server build)
build-ffi: circuits $(FFI_PIR_PREREQ)
	go build -tags "$(FFI_TAGS)" -ldflags '$(LDFLAGS)' -o $(BINARY) ./cmd/svoted

## build-create-val-tx: Build the create-val-tx helper binary locally
build-create-val-tx:
	go build -ldflags '$(LDFLAGS)' -o create-val-tx ./scripts/create-val-tx

## install-create-val-tx: Install create-val-tx to $GOBIN
install-create-val-tx:
	go install -ldflags '$(LDFLAGS)' ./scripts/create-val-tx

## init: Initialize a single-validator chain with FFI + embedded PIR (wipes existing data)
init: install-ffi
	bash scripts/init.sh

## init-multi: Initialize a 3-validator chain with FFI + embedded PIR (wipes existing data)
init-multi: install-ffi install-create-val-tx
	bash scripts/init_multi.sh

## start-multi: Start 3 validators in background (use init-multi first)
start-multi:
	@for i in 1 2 3; do \
		home="$$HOME/.svoted-val$${i}"; \
		log="/tmp/svoted-val$${i}.log"; \
		echo "Starting val$${i} (home=$$home, log=$$log)..."; \
		SVOTE_PIR_URL=disabled $(BINARY) start --home "$$home" > "$$log" 2>&1 & \
	done
	@echo "All 3 validators started in background."

## init-benchmark: Initialize a single-validator chain with benchmark helper settings
init-benchmark: install-ffi
	bash scripts/init_benchmark.sh

## start: Start the chain (set SVOTE_PIR_URL to override nullifier PIR server)
start:
	SVOTE_PIR_URL=$${SVOTE_PIR_URL:-http://localhost:3000} $(BINARY) start --home $(HOME_DIR)

## clean: Remove chain state but preserve nullifier/PIR data (~/.svoted/nullifiers)
clean:
	@if [ -d "$(HOME_DIR)" ]; then \
		find "$(HOME_DIR)" -mindepth 1 -maxdepth 1 ! -name nullifiers -exec rm -rf {} +; \
	fi
	rm -f $(BINARY)

## clean-all: Remove chain data directory including nullifier/PIR data
clean-all:
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
	SVOTE_API_URL=http://localhost:1318 cargo test --release --manifest-path e2e-tests/Cargo.toml round_activation -- --nocapture --ignored

## test-api: Rust E2E API tests against a running chain (requires: make init && make start)
test-api:
	SVOTE_API_URL=http://localhost:1318 HELPER_SERVER_URL=http://127.0.0.1:1318 cargo test --release --manifest-path e2e-tests/Cargo.toml -- --nocapture --ignored

## test-e2e: Alias for test-api (Rust E2E tests)
test-e2e: test-api

## test-api-restart: init + test-api (full API test cycle; chain must be stopped first)
test-api-restart: init test-api

## test-api-reinit: init + fixtures only (no test-api)
test-api-reinit: init fixtures

## fixtures-ts: Copy Halo2 proof fixtures into TS test directory (requires: make fixtures)
fixtures-ts: fixtures
	mkdir -p tests/api/fixtures
	cp ffi/zkp/testdata/toy_valid_proof.bin tests/api/fixtures/
	cp ffi/zkp/testdata/toy_valid_input.bin tests/api/fixtures/

# ---------------------------------------------------------------------------
# Rust circuit / FFI targets
# ---------------------------------------------------------------------------

## circuits: Build the Rust static library (requires cargo)
circuits:
	cargo build --release --manifest-path circuits/Cargo.toml

# ---------------------------------------------------------------------------
# Embedded PIR server
# ---------------------------------------------------------------------------
# The nf-server binary lives in valargroup/vote-nullifier-pir. Source resolution
# order (first hit wins):
#   1. $VOTE_NULLIFIER_PIR_DIR (explicit override; matches CI)
#   2. ../vote-nullifier-pir    (sibling checkout, for active development)
#   3. .cache/vote-nullifier-pir (auto-cloned at $PIR_REF — same tag CI uses)
# Keep PIR_REF in sync with VOTE_NULLIFIER_PIR_REF in .github/workflows/sdk-chain-deploy.yml.

PIR_REF      ?= v0.0.7
PIR_REPO_URL ?= https://github.com/valargroup/vote-nullifier-pir.git
PIR_CACHE    := .cache/vote-nullifier-pir
PIR_REPO     ?= $(or $(VOTE_NULLIFIER_PIR_DIR),$(if $(wildcard ../vote-nullifier-pir/Cargo.toml),../vote-nullifier-pir,$(PIR_CACHE)))
PIR_EMBED_DIR := ffi/pir/bin

## pir-source: Ensure the vote-nullifier-pir source tree is available at $(PIR_REPO)
pir-source:
	@if [ -f "$(PIR_REPO)/Cargo.toml" ]; then \
		echo "Using vote-nullifier-pir at $(PIR_REPO)"; \
	elif [ "$(PIR_REPO)" = "$(PIR_CACHE)" ]; then \
		if [ -d "$(PIR_CACHE)/.git" ]; then \
			echo "Updating $(PIR_CACHE) to $(PIR_REF)"; \
			git -C "$(PIR_CACHE)" fetch --tags --quiet origin && \
			git -C "$(PIR_CACHE)" checkout --quiet "$(PIR_REF)"; \
		else \
			echo "Cloning $(PIR_REPO_URL) @ $(PIR_REF) into $(PIR_CACHE)"; \
			mkdir -p "$(dir $(PIR_CACHE))" && \
			git clone --depth 1 --branch "$(PIR_REF)" "$(PIR_REPO_URL)" "$(PIR_CACHE)"; \
		fi; \
	else \
		echo "ERROR: vote-nullifier-pir not found at $(PIR_REPO)" >&2; \
		echo "       unset VOTE_NULLIFIER_PIR_DIR to auto-clone from $(PIR_REPO_URL)" >&2; \
		exit 1; \
	fi

## pir-binary: Build the nf-server Rust binary and copy it into the go:embed directory
pir-binary: pir-source
	cargo build --release --features serve -p nf-server --manifest-path $(PIR_REPO)/Cargo.toml
	mkdir -p $(PIR_EMBED_DIR)
	cp $(PIR_REPO)/target/release/nf-server $(PIR_EMBED_DIR)/nf-server

## circuits-test: Run Rust circuit unit tests
circuits-test:
	cargo test --release --manifest-path circuits/Cargo.toml

## fixtures: Regenerate all fixture files (Halo2 + RedPallas) (requires circuits build)
fixtures: circuits
	cargo test --release --manifest-path circuits/Cargo.toml -- generate_fixtures --ignored --nocapture

## test-halo2: Run Go tests that use real Halo2 verification via CGo (requires circuits)
test-halo2: circuits
	go test -tags halo2 -count=1 -v ./ffi/zkp/halo2/... ./x/vote/ante/...

## test-halo2-ante: Run ante handler tests with real Halo2 verification
test-halo2-ante: circuits
	go test -tags halo2 -count=1 -v ./x/vote/ante/... -run TestHalo2

## test-redpallas: Run Go tests with real RedPallas signature verification via CGo (requires circuits)
test-redpallas: circuits
	go test -tags redpallas -count=1 -v ./ffi/redpallas/... ./x/vote/ante/...

## test-redpallas-ante: Run ante handler tests with real RedPallas verification
test-redpallas-ante: circuits
	go test -tags redpallas -count=1 -v ./x/vote/ante/... -run TestRedPallas

## test-all-ffi: Run all FFI-backed tests (Halo2 + RedPallas) (requires circuits)
test-all-ffi: circuits
	go test -tags "halo2 redpallas" -count=1 -v ./ffi/zkp/halo2/... ./ffi/redpallas/... ./x/vote/ante/...

# ---------------------------------------------------------------------------
# Deployment targets
# ---------------------------------------------------------------------------

## caddy: Install Caddyfile and restart Caddy (HTTPS reverse proxy for the chain API)
caddy:
	sudo cp deploy/Caddyfile /etc/caddy/Caddyfile
	sudo systemctl restart caddy
	@echo "Caddy restarted — HTTPS at https://46-101-255-48.sslip.io"

# ---------------------------------------------------------------------------
# Docker testnet targets
# ---------------------------------------------------------------------------

DOCKER_TESTNET_VALIDATORS ?= 30

## docker-build: Build the svoted-testnet Docker image (Rust + Go multi-stage)
docker-build:
	docker build -t svoted-testnet -f docker/Dockerfile .

## docker-testnet: Generate compose file and start N-validator testnet (default 30)
docker-testnet: docker-build
	bash docker/generate-compose.sh $(DOCKER_TESTNET_VALIDATORS)
	docker compose -f docker/docker-compose.yml up -d
	@echo ""
	@echo "Testnet starting with $(DOCKER_TESTNET_VALIDATORS) validators."
	@echo "  RPC: http://localhost:26157"
	@echo "  API: http://localhost:1318"
	@echo "  Logs: docker compose -f docker/docker-compose.yml logs -f"

## docker-testnet-down: Stop and remove the testnet containers and volumes
docker-testnet-down:
	docker compose -f docker/docker-compose.yml down -v
	@echo "Testnet stopped and volumes removed."

# ---------------------------------------------------------------------------
# Admin UI targets
# ---------------------------------------------------------------------------

## ui-build: Build the admin UI (requires Node.js + npm)
ui-build:
	cd ui && npm install --silent && npm run build

## start-admin: Start svoted with admin server + UI
start-admin: ui-build
	SVOTE_PIR_URL=$${SVOTE_PIR_URL:-http://localhost:3000} $(BINARY) start --home $(HOME_DIR) --serve-ui --ui-dist ui/dist
