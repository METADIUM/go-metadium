# BASE_IMAGE: ubuntu:22.04 (default), or pre-built go-metadium-rocksdb-test:latest
ARG BASE_IMAGE=ubuntu:22.04
FROM ${BASE_IMAGE}

# The compression and allocator libraries are what a RocksDB gmet links against.
# The stock ubuntu base does not carry them, and a RocksDB binary dropped into
# this image dies at exec with a loader error that says nothing about the cause.
RUN apt-get update && apt-get install -y --no-install-recommends \
    curl \
    libjemalloc2 \
    liblz4-1 \
    libsnappy1v5 \
    libzstd1 \
    && rm -rf /var/lib/apt/lists/*

COPY gmet /usr/local/bin/gmet
RUN chmod +x /usr/local/bin/gmet

COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

EXPOSE 8545 8546 30303 30303/udp

ENTRYPOINT ["/entrypoint.sh"]
