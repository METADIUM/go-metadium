#!/usr/bin/env bash
# lib.sh - Shared utilities for server36 test scenarios
# Source this file: source "$(dirname "$0")/../lib.sh"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
RESULTS_DIR="${RESULTS_DIR:-$HOME/go-metadium/test-results}"
RPC_PORTS=(8545 8546 8547 8548)
NODE_NAMES=(node1 node2 node3 node4)
CONTAINER_NAMES=(gmet-s36-node1 gmet-s36-node2 gmet-s36-node3 gmet-s36-node4)

log()  { echo "[$(date '+%H:%M:%S')] $*" >&2; }
warn() { echo "[$(date '+%H:%M:%S')] [WARN] $*" >&2; }
err()  { echo "[$(date '+%H:%M:%S')] [ERROR] $*" >&2; }

# Send a JSON-RPC request
# Usage: rpc '{"jsonrpc":"2.0",...}' [http://host:port]
rpc() {
  curl -sf -X POST -H "Content-Type: application/json" \
    --data "$1" "${2:-http://localhost:8545}" 2>/dev/null
}

# Get block number as decimal
# Usage: block_number [http://host:port]
block_number() {
  local endpoint="${1:-http://localhost:8545}"
  local resp
  resp=$(rpc '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}' "$endpoint")
  if [[ -z "$resp" ]]; then echo "0"; return; fi
  echo "$resp" | python3 -c "import sys,json; d=json.load(sys.stdin); print(int(d.get('result','0x0'),16))" 2>/dev/null || echo "0"
}

# Get peer count as decimal
# Usage: peer_count [http://host:port]
peer_count() {
  local endpoint="${1:-http://localhost:8545}"
  local resp
  resp=$(rpc '{"jsonrpc":"2.0","method":"net_peerCount","params":[],"id":1}' "$endpoint")
  if [[ -z "$resp" ]]; then echo "0"; return; fi
  echo "$resp" | python3 -c "import sys,json; d=json.load(sys.stdin); print(int(d.get('result','0x0'),16))" 2>/dev/null || echo "0"
}

# Check if RPC endpoint is alive
# Usage: rpc_alive http://host:port
rpc_alive() {
  local endpoint="${1:-http://localhost:8545}"
  local b
  b=$(block_number "$endpoint")
  [[ "$b" =~ ^[0-9]+$ ]] && return 0 || return 1
}

# Wait until block number reaches target on given endpoint
# Usage: wait_block TARGET [http://host:port] [timeout_seconds]
wait_block() {
  local target="$1"
  local endpoint="${2:-http://localhost:8545}"
  local timeout="${3:-600}"
  local deadline=$(($(date +%s) + timeout))
  log "Waiting for block $target on $endpoint (timeout ${timeout}s)..."
  while true; do
    local current
    current=$(block_number "$endpoint")
    if [[ "$current" -ge "$target" ]]; then
      log "  Block $current reached (target=$target)"
      return 0
    fi
    if [[ $(date +%s) -ge $deadline ]]; then
      err "Timeout waiting for block $target on $endpoint (current=$current)"
      return 1
    fi
    sleep 3
  done
}

# Wait for all 4 node RPCs to be alive
# Usage: wait_all_nodes [timeout_seconds]
wait_all_nodes() {
  local timeout="${1:-120}"
  local deadline=$(($(date +%s) + timeout))
  log "Waiting for all 4 node RPCs..."
  local ports=(8545 8546 8547 8548)
  local ready=0
  while [[ $ready -lt 4 ]]; do
    ready=0
    for port in "${ports[@]}"; do
      if rpc_alive "http://localhost:$port" 2>/dev/null; then
        ready=$((ready + 1))
      fi
    done
    if [[ $(date +%s) -ge $deadline ]]; then
      err "Timeout: only $ready/4 nodes responded"
      return 1
    fi
    [[ $ready -lt 4 ]] && sleep 2
  done
  log "All 4 nodes responding."
}

# Print status of all nodes
print_node_status() {
  log "=== Node Status ==="
  local ports=(8545 8546 8547 8548)
  local names=(node1 node2 node3 node4)
  for i in 0 1 2 3; do
    local port="${ports[$i]}"
    local name="${names[$i]}"
    local block peers
    block=$(block_number "http://localhost:$port")
    peers=$(peer_count "http://localhost:$port")
    log "  $name :$port  block=$block  peers=$peers"
  done
}

# Connect nodes to bootnode via admin_addPeer
connect_peers() {
  local bootnode_enode
  bootnode_enode=$(python3 -c "import json; print(json.load(open('$SCRIPT_DIR/static-nodes.json'))[0])" 2>/dev/null || true)
  if [[ -z "$bootnode_enode" ]]; then
    warn "Could not read bootnode enode from static-nodes.json"
    return
  fi
  log "Connecting nodes 2/3/4 to bootnode..."
  for port in 8546 8547 8548; do
    curl -sf -X POST -H "Content-Type: application/json" \
      --data "{\"jsonrpc\":\"2.0\",\"method\":\"admin_addPeer\",\"params\":[\"$bootnode_enode\"],\"id\":1}" \
      "http://localhost:$port" &>/dev/null && log "  :$port addPeer OK" || warn "  :$port addPeer failed"
  done
}

# Start the network with given env vars already exported
# Usage: start_network [genesis_file]
start_network() {
  local genesis="${1:-$SCRIPT_DIR/genesis.json}"
  export GENESIS_FILE="$genesis"
  log "Starting network (genesis=$genesis)..."
  cd "$SCRIPT_DIR"
  docker compose up -d
  wait_all_nodes 180
  sleep 3
  connect_peers
  print_node_status
}

# Stop the network
# Usage: stop_network [--clean]
stop_network() {
  cd "$SCRIPT_DIR"
  if [[ "${1:-}" == "--clean" ]]; then
    log "Stopping network and removing data..."
    docker compose down -v 2>/dev/null || true
    rm -rf data/node1 data/node2 data/node3 data/node4
    log "Data directories cleared."
  else
    log "Stopping network (data preserved)..."
    docker compose stop 2>/dev/null || true
  fi
}

# Write a pass/fail summary
# Usage: write_summary SCENARIO_DIR PASS_COUNT FAIL_COUNT "notes"
write_summary() {
  local dir="$1"
  local pass="$2"
  local fail="$3"
  local notes="${4:-}"
  local status="PASS"
  [[ "$fail" -gt 0 ]] && status="FAIL"
  mkdir -p "$dir"
  cat > "$dir/summary.md" <<EOF
# Scenario Summary

- Status: **$status**
- Pass: $pass
- Fail: $fail
- Timestamp: $(date -u +%Y-%m-%dT%H:%M:%SZ)
- Notes: $notes
EOF
  log "Summary: $status (pass=$pass fail=$fail) -> $dir/summary.md"
}

# Measure RPC latency for a method (N calls)
# Usage: bench_rpc_latency METHOD ENDPOINT CALLS OUT_FILE
bench_rpc_latency() {
  local method="$1"
  local endpoint="$2"
  local calls="${3:-100}"
  local outfile="$4"
  local req="{\"jsonrpc\":\"2.0\",\"method\":\"$method\",\"params\":[],\"id\":1}"
  log "Measuring $method latency ($calls calls)..."
  > "$outfile"
  for i in $(seq 1 "$calls"); do
    local t1 t2
    t1=$(date +%s%3N)
    curl -sf -X POST -H "Content-Type: application/json" --data "$req" "$endpoint" > /dev/null 2>&1 || true
    t2=$(date +%s%3N)
    echo $((t2 - t1)) >> "$outfile"
  done
  python3 - "$outfile" <<'PYEOF'
import sys, statistics
vals = list(map(int, open(sys.argv[1])))
if not vals:
    print("No data")
    exit(0)
vals.sort()
print(f"  count={len(vals)} avg={statistics.mean(vals):.1f}ms p50={vals[len(vals)//2]}ms p95={vals[int(len(vals)*0.95)]}ms max={max(vals)}ms")
PYEOF
}

# Wait for a transaction receipt
# Usage: wait_receipt TXHASH [endpoint] [timeout]
wait_receipt() {
  local txhash="$1"
  local endpoint="${2:-http://localhost:8545}"
  local timeout="${3:-60}"
  local deadline=$(($(date +%s) + timeout))
  while [[ $(date +%s) -lt $deadline ]]; do
    local r
    r=$(rpc "{\"jsonrpc\":\"2.0\",\"method\":\"eth_getTransactionReceipt\",\"params\":[\"$txhash\"],\"id\":1}" "$endpoint" | \
      python3 -c "import sys,json; d=json.load(sys.stdin); r=d.get('result'); print(r.get('status','') if r else '')" 2>/dev/null || echo "")
    [[ "$r" == "0x1" ]] && return 0
    [[ "$r" == "0x0" ]] && return 1
    sleep 1
  done
  return 1
}
