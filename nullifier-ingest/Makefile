# nullifier-ingest
# Top-level Makefile — delegates to imt-tree and service subcrates

IMT_DIR     := imt-tree
SERVICE_DIR := service

# ── Configuration (override with env vars) ───────────────────────────
DB_PATH    ?= nullifiers.db
LWD_URL    ?= https://zec.rocks:443
PORT       ?= 3000

# ── Targets ──────────────────────────────────────────────────────────

.PHONY: ingest test-proof build test test-integration clean status serve serve-deploy help

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'

build: ## Build all binaries (release)
	cd $(SERVICE_DIR) && cargo build --release

ingest: ## Ingest Orchard nullifiers from chain into SQLite
	cd $(SERVICE_DIR) && DB_PATH=$(DB_PATH) LWD_URL=$(LWD_URL) cargo run --release --bin ingest-nfs

test-proof: ## Run exclusion proof verification against ingested data
	cd $(SERVICE_DIR) && DB_PATH=$(DB_PATH) cargo run --release --bin test-non-inclusion

serve: ## Start the exclusion proof query server
	cd $(SERVICE_DIR) && DB_PATH=$(DB_PATH) PORT=$(PORT) cargo run --release --bin query-server

# Same binary and env as CI deploy; use for local testing before pushing.
# Put nullifiers.db and nullifiers.db.tree in $(DATA_DIR) (default: ./nullifier-service).
DATA_DIR ?= nullifier-service
serve-deploy: build ## Build release and run query-server (like deploy); DB_PATH=$(DATA_DIR)/nullifiers.db
	@mkdir -p $(DATA_DIR)
	cd $(SERVICE_DIR) && DB_PATH="../$(DATA_DIR)/nullifiers.db" PORT=$(PORT) ./target/release/query-server

test: ## Run unit tests for all subcrates
	cd $(IMT_DIR) && cargo test --lib
	cd $(SERVICE_DIR) && cargo test --lib

test-integration: ## Run IMT ↔ delegation-circuit ZK integration test
	cd $(IMT_DIR) && cargo test --test imt_circuit_integration -- --nocapture

status: ## Show ingestion progress (nullifier count + last synced height)
	@echo "=== Nullifier DB: $(DB_PATH) ==="
	@if [ -f "$(DB_PATH)" ]; then \
		echo "DB size: $$(du -h $(DB_PATH) | cut -f1)"; \
		echo "Nullifier count: $$(sqlite3 $(DB_PATH) 'SELECT COUNT(*) FROM nullifiers;')"; \
		echo "Last synced height: $$(sqlite3 $(DB_PATH) 'SELECT COALESCE(MAX(height),0) FROM checkpoint;')"; \
	else \
		echo "Database not found at $(DB_PATH)"; \
	fi

clean: ## Remove built artifacts and database
	cd $(IMT_DIR) && cargo clean
	cd $(SERVICE_DIR) && cargo clean
	rm -f $(DB_PATH) $(DB_PATH)-wal $(DB_PATH)-shm
