# BASE_IMAGE: ubuntu:22.04 (default), or pre-built go-metadium-rocksdb-test:latest
ARG BASE_IMAGE=ubuntu:22.04
FROM ${BASE_IMAGE}

RUN apt-get update && apt-get install -y --no-install-recommends \
    curl \
    && rm -rf /var/lib/apt/lists/*

COPY gmet /usr/local/bin/gmet
RUN chmod +x /usr/local/bin/gmet

COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

EXPOSE 8545 8546 30303 30303/udp

ENTRYPOINT ["/entrypoint.sh"]
