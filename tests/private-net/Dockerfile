FROM ubuntu:24.04

RUN apt-get update && apt-get install -y --no-install-recommends \
    librocksdb8.9 libsnappy1v5 liblz4-1 libzstd1 \
    curl \
    && rm -rf /var/lib/apt/lists/*

COPY gmet /usr/local/bin/gmet
RUN chmod +x /usr/local/bin/gmet

COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

EXPOSE 8545 8546 30303 30303/udp

ENTRYPOINT ["/entrypoint.sh"]
