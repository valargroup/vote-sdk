# nullifier-service Makefile
# Ingest Zcash Orchard nullifiers, build Merkle tree, serve exclusion proofs
#
# Data layout (all inside DATA_DIR):
#   nullifiers.bin         – append-only 32-byte nullifier blobs
#   nullifiers.checkpoint  – 16-byte (height, offset) crash-recovery marker
#   nullifiers.tree        – cached full Merkle tree (optional sidecar)

# ── Configuration (override with env vars) ───────────────────────────
DATA_DIR   ?= .
LWD_URL    ?= https://zec.rocks:443
PORT       ?= 3000

# ── Targets ──────────────────────────────────────────────────────────

.PHONY: ingest ingest-resync test-proof build test clean status serve help

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

build: ## Build all binaries (release)
	cargo build --release

ingest: build ## Ingest nullifiers (incremental; tree sidecar kept as-is)
	DATA_DIR=$(DATA_DIR) LWD_URL=$(LWD_URL) \
		cargo run --release --bin ingest-nfs

ingest-resync: build ## Ingest nullifiers and invalidate the tree sidecar so the server rebuilds
	DATA_DIR=$(DATA_DIR) LWD_URL=$(LWD_URL) INVALIDATE_TREE=1 \
		cargo run --release --bin ingest-nfs

test-proof: build ## Run exclusion proof verification against ingested data
	DATA_DIR=$(DATA_DIR) \
		cargo run --release --bin test-non-inclusion

serve: build ## Start the exclusion proof query server
	DATA_DIR=$(DATA_DIR) PORT=$(PORT) \
		cargo run --release --bin query-server

test: ## Run unit tests
	cargo test

status: ## Show ingestion progress (nullifier count + last synced height)
	@NF="$(DATA_DIR)/nullifiers.bin"; CP="$(DATA_DIR)/nullifiers.checkpoint"; \
	TREE="$(DATA_DIR)/nullifiers.tree"; \
	echo "Data directory: $(DATA_DIR)"; \
	if [ -f "$$NF" ]; then \
		SIZE=$$(ls -lh "$$NF" | awk '{print $$5}'); \
		BYTES=$$(wc -c < "$$NF" | tr -d ' '); \
		COUNT=$$((BYTES / 32)); \
		echo "  nullifiers.bin: $$COUNT nullifiers ($$SIZE)"; \
	else \
		echo "  nullifiers.bin: not found"; \
	fi; \
	if [ -f "$$CP" ]; then \
		HEIGHT=$$(od -An -t u8 -j 0 -N 8 "$$CP" | tr -d ' '); \
		OFFSET=$$(od -An -t u8 -j 8 -N 8 "$$CP" | tr -d ' '); \
		echo "  checkpoint: height=$$HEIGHT offset=$$OFFSET"; \
	else \
		echo "  checkpoint: none"; \
	fi; \
	if [ -f "$$TREE" ]; then \
		TSIZE=$$(ls -lh "$$TREE" | awk '{print $$5}'); \
		echo "  nullifiers.tree: $$TSIZE (sidecar)"; \
	else \
		echo "  nullifiers.tree: not present (will rebuild on serve)"; \
	fi

clean: ## Remove built artifacts and data files
	cargo clean
	rm -f $(DATA_DIR)/nullifiers.bin $(DATA_DIR)/nullifiers.checkpoint $(DATA_DIR)/nullifiers.tree
