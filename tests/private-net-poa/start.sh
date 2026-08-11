#!/usr/bin/env bash
# shellcheck disable=SC2015  # A && B || C pattern is intentional — log/warn always return 0
# start.sh - Start PoA private network
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

log()  { echo "[$(date '+%H:%M:%S')] $*"; }
warn() { echo "[$(date '+%H:%M:%S')] [WARN] $*"; }

[[ -f static-nodes.json ]] || { echo "[ERROR] Please run setup.sh first."; exit 1; }
[[ -f data/node1/geth/nodekey ]] || { echo "[ERROR] Please run setup.sh first."; exit 1; }
[[ -f genesis.json ]] || { echo "[ERROR] genesis.json not found. Please run setup.sh first."; exit 1; }

log "=== Starting PoA private network ==="
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

sleep 3

# Add node1 as static peer to nodes 2 and 3 (static-nodes.json deprecated in v1.13+)
BOOTNODE_ENODE=$(python3 -c "import json; print(json.load(open('static-nodes.json'))[0])" 2>/dev/null || true)
if [[ -n "$BOOTNODE_ENODE" ]]; then
  log "Connecting nodes 2/3 to bootnode..."
  for port in 8546 8547; do
    curl -sf -X POST -H "Content-Type: application/json" \
      --data "{\"jsonrpc\":\"2.0\",\"method\":\"admin_addPeer\",\"params\":[\"$BOOTNODE_ENODE\"],\"id\":1}" \
      "http://localhost:$port" &>/dev/null && log "  :$port addPeer OK" || warn "  :$port addPeer failed"
  done
fi

log ""
log "=== Network status ==="
for port in 8545 8546 8547; do
  BLOCK=$(curl -sf -X POST -H "Content-Type: application/json" \
    --data '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}' \
    "http://localhost:$port" 2>/dev/null | \
    python3 -c "import sys,json; d=json.load(sys.stdin); print(int(d['result'],16))" 2>/dev/null || echo "?")
  MINING=$(curl -sf -X POST -H "Content-Type: application/json" \
    --data '{"jsonrpc":"2.0","method":"eth_mining","params":[],"id":1}' \
    "http://localhost:$port" 2>/dev/null | \
    python3 -c "import sys,json; d=json.load(sys.stdin); print(d['result'])" 2>/dev/null || echo "?")
  PEERS=$(curl -sf -X POST -H "Content-Type: application/json" \
    --data '{"jsonrpc":"2.0","method":"net_peerCount","params":[],"id":1}' \
    "http://localhost:$port" 2>/dev/null | \
    python3 -c "import sys,json; d=json.load(sys.stdin); print(int(d['result'],16))" 2>/dev/null || echo "?")
  log "  :$port  block=$BLOCK  mining=$MINING  peers=$PEERS"
done

log ""
log "CamelliaFork activation block: 100"
log "Estimated time: approx 3-4 minutes (blockInterval~2s, without governance)"
log ""
log "Useful commands:"
log "  Monitor blocks: watch -n2 'curl -sf -X POST -H \"Content-Type: application/json\" --data \"{\\\"jsonrpc\\\":\\\"2.0\\\",\\\"method\\\":\\\"eth_blockNumber\\\",\\\"params\\\":[],\\\"id\\\":1}\" http://localhost:8545 | python3 -c \"import sys,json; d=json.load(sys.stdin); print(int(d[\\\"result\\\"],16))\"'"
log "  node1 logs:     docker logs -f gmet-poa-node1"
log "  Stop:           ./stop.sh"
log "  Full reset:     ./stop.sh --clean && ./setup.sh"
