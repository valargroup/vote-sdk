# SDK chain (zallyd) image: Rust circuits + Go binary.
# Build from repo root: docker build -f Dockerfile .
# Railway: set Root Directory to empty (repo root) so orchard/ and vote-commitment-tree/ are available.

# ---------------------------------------------------------------------------
# Stage 1: Build Rust circuits (path deps need repo root)
# ---------------------------------------------------------------------------
FROM rust:1.83-bookworm AS circuits
WORKDIR /build

# Path deps from sdk/circuits/Cargo.toml
COPY sdk/circuits/Cargo.toml sdk/circuits/Cargo.lock sdk/circuits/
COPY sdk/circuits/src sdk/circuits/src
COPY sdk/circuits/include sdk/circuits/include
COPY sdk/circuits/tests sdk/circuits/tests
COPY vote-commitment-tree vote-commitment-tree/
COPY orchard orchard/

RUN cargo build --release --manifest-path sdk/circuits/Cargo.toml

# ---------------------------------------------------------------------------
# Stage 2: Build zallyd (Go + CGo linking to circuits)
# ---------------------------------------------------------------------------
FROM golang:1.23-bookworm AS gobuilder
WORKDIR /build

# Copy Go module files and source
COPY sdk/go.mod sdk/go.sum sdk/
COPY sdk/cmd sdk/cmd
COPY sdk/api sdk/api
COPY sdk/app sdk/app
COPY sdk/x sdk/x
COPY sdk/crypto sdk/crypto
COPY sdk/scripts sdk/scripts
# Circuits build artifacts and header for CGo
COPY --from=circuits /build/sdk/circuits/target/release/libzally_circuits.a sdk/circuits/target/release/
COPY --from=circuits /build/sdk/circuits/include sdk/circuits/include

WORKDIR /build/sdk
RUN CGO_ENABLED=1 go build -tags "halo2,redpallas" -ldflags="-w -s" -o /zallyd ./cmd/zallyd

# ---------------------------------------------------------------------------
# Stage 3: Runtime
# ---------------------------------------------------------------------------
FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates && rm -rf /var/lib/apt/lists/*

# Chain data dir (mount a volume here for persistence)
ENV ZALLYD_HOME=/data
COPY --from=gobuilder /zallyd /usr/local/bin/zallyd
COPY sdk/scripts/init.sh /init.sh

# Init only if genesis not present; then start (HOME set so init uses ZALLYD_HOME/.zallyd)
RUN chmod +x /init.sh
ENTRYPOINT ["/bin/bash", "-c", "set -e; export HOME=\"$ZALLYD_HOME\"; if [ ! -f \"$ZALLYD_HOME/.zallyd/config/genesis.json\" ]; then /init.sh; fi; exec zallyd start --home \"$ZALLYD_HOME/.zallyd\""]

# Cosmos REST API and RPC
EXPOSE 1317 26657
