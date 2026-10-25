FROM rust:1.88-slim AS builder

WORKDIR /app
# Build dependencies in their own cached layer.
COPY Cargo.toml Cargo.lock ./
RUN mkdir src && echo "fn main() {}" > src/main.rs && touch src/lib.rs && cargo build --release && rm -rf src

COPY src ./src
RUN touch src/main.rs src/lib.rs && cargo build --release

FROM debian:bookworm-slim
COPY --from=builder /app/target/release/inventory-sync /usr/local/bin/inventory-sync

ENV PORT=8090
EXPOSE 8090

CMD ["inventory-sync"]
