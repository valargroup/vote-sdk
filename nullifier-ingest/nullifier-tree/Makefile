# nullifier-tree Makefile
# Ingest Zcash Orchard nullifiers, build Merkle tree, test exclusion proofs

# ── Configuration (override with env vars) ───────────────────────────
DB_PATH    ?= nullifiers.db
LWD_URL    ?= https://zec.rocks:443
PORT       ?= 3000

# ── Targets ──────────────────────────────────────────────────────────

.PHONY: ingest test-proof build test clean status serve help

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'

build: ## Build all binaries (release)
	cargo build --release

ingest: build ## Ingest Orchard nullifiers from chain into SQLite
	DB_PATH=$(DB_PATH) LWD_URL=$(LWD_URL) \
		cargo run --release --bin ingest-nfs

test-proof: build ## Run exclusion proof verification against ingested data
	DB_PATH=$(DB_PATH) \
		cargo run --release --bin test-non-inclusion

serve: build ## Start the exclusion proof query server
	DB_PATH=$(DB_PATH) PORT=$(PORT) \
		cargo run --release --bin query-server

test: ## Run unit tests
	cargo test

status: ## Show ingestion progress (nullifier count + last synced height)
	@echo "Database: $(DB_PATH)"
	@sqlite3 $(DB_PATH) "SELECT 'Nullifiers: ' || COUNT(*) FROM nullifiers;" 2>/dev/null || echo "  (no database yet)"
	@sqlite3 $(DB_PATH) "SELECT 'Last height: ' || height FROM checkpoint;" 2>/dev/null || echo "  (no checkpoint yet)"
	@ls -lh $(DB_PATH) 2>/dev/null | awk '{print "Size:        " $$5}' || true

clean: ## Remove built artifacts and database
	cargo clean
	rm -f $(DB_PATH) $(DB_PATH)-wal $(DB_PATH)-shm
