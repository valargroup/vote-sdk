# nullifier-ingest
# Top-level Makefile — delegates to nullifier-tree subcrate

TREE_DIR := nullifier-tree

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
	$(MAKE) -C $(TREE_DIR) build

ingest: ## Ingest Orchard nullifiers from chain into SQLite
	$(MAKE) -C $(TREE_DIR) ingest DB_PATH=$(DB_PATH) LWD_URL=$(LWD_URL)

test-proof: ## Run exclusion proof verification against ingested data
	$(MAKE) -C $(TREE_DIR) test-proof DB_PATH=$(DB_PATH)

serve: ## Start the exclusion proof query server
	$(MAKE) -C $(TREE_DIR) serve DB_PATH=$(DB_PATH) PORT=$(PORT)

test: ## Run unit tests
	$(MAKE) -C $(TREE_DIR) test

status: ## Show ingestion progress (nullifier count + last synced height)
	$(MAKE) -C $(TREE_DIR) status DB_PATH=$(DB_PATH)

clean: ## Remove built artifacts and database
	$(MAKE) -C $(TREE_DIR) clean DB_PATH=$(DB_PATH)
