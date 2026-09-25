#!/usr/bin/env bash
# start.sh - start the PBFT private network and connect it in a full mesh
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"
source ./lib.sh

[[ -f genesis.json && -f node-ids.txt && -f docker-compose.yml ]] || err "Please run setup.sh first."
docker compose up -d
for n in $(seq 1 "$NODES"); do
  for _ in $(seq 1 30); do
    rpc "$(port_of "$n")" eth_blockNumber '[]' >/dev/null 2>&1 && break
    sleep 2
  done
done
# Governance connects members too, but only once it is deployed.
mesh $(seq 1 "$NODES")
sleep 3
status
