# nullifier-service Makefile
# Library crate — no binaries. Use nf-server for ingest/export/serve.

.PHONY: build test clean help

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

build: ## Build library (release)
	cargo build --release

test: ## Run unit tests
	cargo test --lib

clean: ## Remove built artifacts
	cargo clean
