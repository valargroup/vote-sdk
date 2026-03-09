# nullifier-ingest
# Top-level Makefile — delegates to nf-server (unified binary) and subcrates
#
# Storage: flat binary files (no SQLite).
#
#   nullifiers.bin         – append-only raw 32-byte nullifier blobs
#   nullifiers.checkpoint  – 16-byte (height LE, offset LE) crash-recovery marker
#   pir-data/              – PIR tier files (tier0.bin, tier1.bin, tier2.bin, pir_root.json)
#
# Pipeline: ingest → export → serve
# ──────────────────────────────────
# `make ingest` syncs nullifiers from lightwalletd into nullifiers.bin.
# `make export-nf` builds the PIR tree and exports tier files.
# `make serve` starts the PIR HTTP server.
#
# `make ingest-resync` ingests and deletes stale sidecar/tier files
# (--invalidate) so the next export rebuilds from the updated data.

IMT_DIR     := imt-tree
SERVICE_DIR := service
NF_DIR      := nf-server

# ── Configuration (override with env vars) ───────────────────────────
DATA_DIR      ?= .
LWD_URL       ?= https://zec.rocks:443
PORT          ?= 3000
BOOTSTRAP_URL ?= https://vote.fra1.digitaloceanspaces.com
SYNC_HEIGHT   ?=
PIR_DATA_DIR  ?= $(DATA_DIR)/pir-data

# Validate SYNC_HEIGHT and build --max-height flag for the ingest subcommand.
# If unset, ingest runs to chain tip.  If set, it must be a multiple of 10.
ifdef SYNC_HEIGHT
  ifneq ($(shell expr $(SYNC_HEIGHT) % 10),0)
    $(error SYNC_HEIGHT must be a multiple of 10, got $(SYNC_HEIGHT))
  endif
  _MAX_HEIGHT_FLAG := --max-height $(SYNC_HEIGHT)
else
  _MAX_HEIGHT_FLAG :=
endif

# ── Targets ──────────────────────────────────────────────────────────

.PHONY: build-nf ingest ingest-resync export-nf serve bootstrap build test clean status help

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'

build-nf: ## Build nf-server binary (release, nightly)
	cd $(NF_DIR) && cargo build --release

build: ## Build nf-server and service library (release)
	cd $(NF_DIR) && cargo build --release

bootstrap: ## Download nullifier files from bootstrap URL if not present in DATA_DIR
	@if [ ! -f "$(DATA_DIR)/nullifiers.checkpoint" ]; then \
		echo "Bootstrap: nullifier files not found in $(DATA_DIR), downloading from $(BOOTSTRAP_URL)..."; \
		mkdir -p "$(DATA_DIR)"; \
		wget -q --show-progress -O "$(DATA_DIR)/nullifiers.bin"        "$(BOOTSTRAP_URL)/nullifiers.bin"; \
		wget -q --show-progress -O "$(DATA_DIR)/nullifiers.checkpoint" "$(BOOTSTRAP_URL)/nullifiers.checkpoint"; \
		wget -q --show-progress -O "$(DATA_DIR)/nullifiers.tree"       "$(BOOTSTRAP_URL)/nullifiers.tree"; \
		echo "Bootstrap complete."; \
	else \
		echo "Bootstrap: nullifier files already present in $(DATA_DIR), skipping."; \
	fi

ingest: ## Ingest nullifiers incrementally up to SYNC_HEIGHT (or chain tip if unset)
	cd $(NF_DIR) && cargo run --release -- ingest --data-dir ../$(DATA_DIR) --lwd-url $(LWD_URL) $(_MAX_HEIGHT_FLAG)

ingest-resync: ## Ingest nullifiers up to SYNC_HEIGHT and invalidate stale sidecar/tier files
	cd $(NF_DIR) && cargo run --release -- ingest --data-dir ../$(DATA_DIR) --lwd-url $(LWD_URL) --invalidate $(_MAX_HEIGHT_FLAG)

export-nf: ## Build PIR tree and export tier files from nullifiers.bin
	cd $(NF_DIR) && cargo run --release -- export --data-dir ../$(DATA_DIR) --output-dir ../$(PIR_DATA_DIR)

serve: ## Start the PIR HTTP server
	cd $(NF_DIR) && cargo run --release --features serve -- serve --pir-data-dir ../$(PIR_DATA_DIR) --data-dir ../$(DATA_DIR) --port $(PORT)

test: ## Run unit tests for all subcrates
	cd $(IMT_DIR) && cargo test --lib
	cd $(SERVICE_DIR) && cargo test --lib

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
	cd $(IMT_DIR) && cargo clean
	cd $(SERVICE_DIR) && cargo clean
	cd $(NF_DIR) && cargo clean
	rm -f $(DATA_DIR)/nullifiers.bin $(DATA_DIR)/nullifiers.checkpoint $(DATA_DIR)/nullifiers.tree
