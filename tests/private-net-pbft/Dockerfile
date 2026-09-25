# The PBFT network's image: the PoA network's, plus iptables, so faults.sh
# can partition validators while keeping each side connected (S-06).
ARG BASE_IMAGE=ubuntu:22.04
FROM ${BASE_IMAGE}

RUN apt-get update && apt-get install -y --no-install-recommends \
    curl \
    iptables \
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
