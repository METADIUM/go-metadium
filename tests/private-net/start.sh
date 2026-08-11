#!/usr/bin/env bash
# start.sh - Start private network
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

log()  { echo "[$(date '+%H:%M:%S')] $*"; }
warn() { echo "[$(date '+%H:%M:%S')] [WARN] $*"; }

[[ -f static-nodes.json ]] || { echo "[ERROR] Please run setup.sh first."; exit 1; }
[[ -f data/node1/geth/nodekey ]] || { echo "[ERROR] Please run setup.sh first."; exit 1; }

log "=== Starting private network ==="
docker compose up -d

log "Waiting for node RPC..."
for port in 8545 8546 8547; do
  for i in $(seq 1 30); do
    sleep 2
    if curl -sf -X POST -H "Content-Type: application/json" \
        --data '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}' \
        "http://localhost:$port" &>/dev/null; then
      log "  :$port responded OK (attempt ${i})"
      break
    fi
  done
done

sleep 5

# In PoW mode, --mine flag alone does not start mining, so call miner_start
log "Starting miner..."
for port in 8545 8546 8547; do
  curl -sf -X POST -H "Content-Type: application/json" \
    --data '{"jsonrpc":"2.0","method":"miner_start","params":[1],"id":1}' \
    "http://localhost:$port" &>/dev/null && log "  :$port miner_start OK" || log "  :$port miner_start failed"
done
sleep 3

log ""
log "=== Network status ==="
for port in 8545 8546 8547; do
  BLOCK=$(curl -sf -X POST -H "Content-Type: application/json" \
    --data '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}' \
    "http://localhost:$port" 2>/dev/null | \
    python3 -c "import sys,json; d=json.load(sys.stdin); print(int(d['result'],16))" 2>/dev/null || echo "?")
  PEERS=$(curl -sf -X POST -H "Content-Type: application/json" \
    --data '{"jsonrpc":"2.0","method":"net_peerCount","params":[],"id":1}' \
    "http://localhost:$port" 2>/dev/null | \
    python3 -c "import sys,json; d=json.load(sys.stdin); print(int(d['result'],16))" 2>/dev/null || echo "?")
  log "  :$port  block=$BLOCK  peers=$PEERS"
done

log ""
log "CamelliaFork activation block: 100"
log ""
log "Useful commands:"
log "  Monitor blocks: watch -n1 'curl -sf -X POST -H \"Content-Type: application/json\" --data \"{\\\"jsonrpc\\\":\\\"2.0\\\",\\\"method\\\":\\\"eth_blockNumber\\\",\\\"params\\\":[],\\\"id\\\":1}\" http://localhost:8545'"
log "  node1 logs:     docker logs -f gmet-node1"
log "  Stop:           ./stop.sh"
log "  Full reset:     ./stop.sh --clean && ./setup.sh"
