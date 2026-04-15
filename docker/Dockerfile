# Stage 1: Build Rust static library (Halo2 + RedPallas + Poseidon tree FFI)
FROM rust:1.85-bookworm AS rust-builder

WORKDIR /src
COPY circuits/ circuits/

RUN cargo build --release --manifest-path circuits/Cargo.toml

# Stage 2: Build Go binaries with FFI tags
FROM golang:1.24-bookworm AS go-builder

RUN apt-get update && apt-get install -y --no-install-recommends gcc libc6-dev && \
    rm -rf /var/lib/apt/lists/*

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .

COPY --from=rust-builder /src/circuits/target/release/libshielded_vote_circuits.a \
     circuits/target/release/libshielded_vote_circuits.a

RUN CGO_ENABLED=1 go build -tags "halo2,redpallas" -o /svoted ./cmd/svoted
RUN CGO_ENABLED=1 go build -tags "halo2,redpallas" -o /create-val-tx ./scripts/create-val-tx

# Stage 3: Minimal runtime
FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
    jq ca-certificates curl && \
    rm -rf /var/lib/apt/lists/*

COPY --from=go-builder /svoted /usr/local/bin/svoted
COPY --from=go-builder /create-val-tx /usr/local/bin/create-val-tx
COPY docker/entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

EXPOSE 26656 26657 1317 9090

ENTRYPOINT ["/entrypoint.sh"]
