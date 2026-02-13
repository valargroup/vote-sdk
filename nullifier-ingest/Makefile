# nullifier-ingest
# Top-level Makefile — delegates to imt-tree and service subcrates

IMT_DIR     := imt-tree
SERVICE_DIR := service

# ── Configuration (override with env vars) ───────────────────────────
DB_PATH    ?= nullifiers.db
LWD_URL    ?= https://zec.rocks:443
PORT       ?= 3000

# ── Targets ──────────────────────────────────────────────────────────

.PHONY: ingest test-proof build test test-integration clean status serve help

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'

build: ## Build all binaries (release)
	$(MAKE) -C $(SERVICE_DIR) build

ingest: ## Ingest Orchard nullifiers from chain into SQLite
	$(MAKE) -C $(SERVICE_DIR) ingest DB_PATH=$(DB_PATH) LWD_URL=$(LWD_URL)

test-proof: ## Run exclusion proof verification against ingested data
	$(MAKE) -C $(SERVICE_DIR) test-proof DB_PATH=$(DB_PATH)

serve: ## Start the exclusion proof query server
	$(MAKE) -C $(SERVICE_DIR) serve DB_PATH=$(DB_PATH) PORT=$(PORT)

test: ## Run unit tests for all subcrates
	cd $(IMT_DIR) && cargo test --lib
	cd $(SERVICE_DIR) && cargo test --lib

test-integration: ## Run IMT ↔ delegation-circuit ZK integration test
	cd $(IMT_DIR) && cargo test --test imt_circuit_integration -- --nocapture

status: ## Show ingestion progress (nullifier count + last synced height)
	$(MAKE) -C $(SERVICE_DIR) status DB_PATH=$(DB_PATH)

clean: ## Remove built artifacts and database
	cd $(IMT_DIR) && cargo clean
	cd $(SERVICE_DIR) && cargo clean
	rm -f $(DB_PATH) $(DB_PATH)-wal $(DB_PATH)-shm
