# Root Makefile
SDK_DIR     = sdk
INGEST_DIR  = nullifier-ingest

.PHONY: install install-ffi init init-ffi start clean build fmt lint \
	test test-unit test-integration test-api \
	fixtures-ts circuits circuits-test fixtures \
	test-halo2 test-halo2-ante test-redpallas test-redpallas-ante test-all-ffi \
	ingest ingest-status ingest-test ingest-proof ingest-clean ingest-serve

install:
	$(MAKE) -C $(SDK_DIR) install

install-ffi:
	$(MAKE) -C $(SDK_DIR) install-ffi

build:
	$(MAKE) -C $(SDK_DIR) build

init:
	$(MAKE) -C $(SDK_DIR) init

init-ffi:
	$(MAKE) -C $(SDK_DIR) init-ffi

start:
	$(MAKE) -C $(SDK_DIR) start

clean:
	$(MAKE) -C $(SDK_DIR) clean

fmt:
	$(MAKE) -C $(SDK_DIR) fmt

lint:
	$(MAKE) -C $(SDK_DIR) lint

test-unit:
	$(MAKE) -C $(SDK_DIR) test-unit

test-integration:
	$(MAKE) -C $(SDK_DIR) test-integration

test:
	$(MAKE) -C $(SDK_DIR) test

test-api:
	$(MAKE) -C $(SDK_DIR) test-api

fixtures-ts:
	$(MAKE) -C $(SDK_DIR) fixtures-ts

circuits:
	$(MAKE) -C $(SDK_DIR) circuits

circuits-test:
	$(MAKE) -C $(SDK_DIR) circuits-test

fixtures:
	$(MAKE) -C $(SDK_DIR) fixtures

test-halo2:
	$(MAKE) -C $(SDK_DIR) test-halo2

test-halo2-ante:
	$(MAKE) -C $(SDK_DIR) test-halo2-ante

test-redpallas:
	$(MAKE) -C $(SDK_DIR) test-redpallas

test-redpallas-ante:
	$(MAKE) -C $(SDK_DIR) test-redpallas-ante

test-all-ffi:
	$(MAKE) -C $(SDK_DIR) test-all-ffi

# ── Nullifier Ingestion ──────────────────────────────────────────────

ingest: ## Ingest Orchard nullifiers from chain into SQLite
	$(MAKE) -C $(INGEST_DIR) ingest

ingest-status: ## Show nullifier count, last synced height, DB size
	$(MAKE) -C $(INGEST_DIR) status

ingest-test: ## Run nullifier-tree unit tests
	$(MAKE) -C $(INGEST_DIR) test

ingest-proof: ## Run exclusion proof verification against ingested data
	$(MAKE) -C $(INGEST_DIR) test-proof

ingest-serve: ## Start the nullifier exclusion proof query server
	$(MAKE) -C $(INGEST_DIR) serve

ingest-clean: ## Remove nullifier build artifacts and database
	$(MAKE) -C $(INGEST_DIR) clean
