# Build stage
FROM golang:1.21-bullseye AS builder

WORKDIR /build

# Install build dependencies
RUN apt-get update && apt-get install -y --no-install-recommends \
    git make gcc libc-dev \
    && rm -rf /var/lib/apt/lists/*

# Copy source code
COPY . .

# Build geth
RUN go build -v -o geth ./cmd/geth

# Runtime stage
FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates && rm -rf /var/lib/apt/lists/*

WORKDIR /root

# Copy geth binary from builder
COPY --from=builder /build/geth /usr/local/bin/

# Expose ports
EXPOSE 8545 8546 30303 30303/udp

# Default command
ENTRYPOINT ["geth"]
CMD ["--help"]
