#!/usr/bin/env bash
# start.sh - start the 4-node PBFT private network and connect it
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"
source ./lib.sh

[[ -f genesis.json && -f node-ids.txt ]] || err "Please run setup.sh first."
docker compose up -d
for port in "${PORTS[@]}"; do
  for _ in $(seq 1 30); do
    rpc "$port" eth_blockNumber '[]' >/dev/null 2>&1 && break
    sleep 2
  done
done
# Full mesh between the four (governance connects members too, but only
# once it is deployed).
for n in 1 2 3 4; do
  for m in 1 2 3 4; do
    [[ $n == "$m" ]] && continue
    rpc "${PORTS[$((n - 1))]}" admin_addPeer "[\"$(enode_of $m)\"]" >/dev/null || true
  done
done
sleep 3
status
