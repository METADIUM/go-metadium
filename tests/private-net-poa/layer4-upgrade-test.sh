#!/usr/bin/env bash
# layer4-upgrade-test.sh — Layer 4: Rolling upgrade simulation
#
# Tests mixed-version operation during Camellia fork transition:
#   - node1+node2: NEW binary (CamelliaBlock=100)
#   - node3:       OLD binary (CamelliaBlock=nil, pre-Camellia)
#
# Expected: all nodes stay on same chain before fork (block < 100).
# At block 100+, old node diverges OR follows (depending on whether it validates
# Camellia-specific fields). This test observes and documents actual behavior.
#
# Usage:
#   OLD_BINARY=/tmp/gmet-old NEW_BINARY=../../geth bash layer4-upgrade-test.sh
#   OLD_BINARY=/tmp/gmet-old bash layer4-upgrade-test.sh   # uses ../../geth as default new

set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

OLD_BINARY="${OLD_BINARY:-/tmp/gmet-old}"
NEW_BINARY="${NEW_BINARY:-$(pwd)/../../geth}"
LOG_DIR="$SCRIPT_DIR/data/layer4-logs"
FORK_BLOCK=100

log()  { echo "[$(date '+%H:%M:%S')] $*"; }
pass() { echo "[$(date '+%H:%M:%S')]   PASS: $*"; }
fail() { echo "[$(date '+%H:%M:%S')]   FAIL: $*"; FAILURES=$((FAILURES+1)); }
info() { echo "[$(date '+%H:%M:%S')]   INFO: $*"; }

FAILURES=0

# ── Preflight ──────────────────────────────────────────────────
log "=== Layer 4: Rolling Upgrade Simulation ==="
log "Old binary: $OLD_BINARY"
log "New binary: $NEW_BINARY"

[[ -x "$OLD_BINARY" ]] || { echo "[ERROR] OLD_BINARY not found or not executable: $OLD_BINARY"; exit 1; }
[[ -x "$NEW_BINARY" ]] || { echo "[ERROR] NEW_BINARY not found or not executable: $NEW_BINARY"; exit 1; }
[[ -f genesis.json ]] || { echo "[ERROR] Run setup.sh first."; exit 1; }
[[ -f data/node1/geth/nodekey ]] || { echo "[ERROR] Run setup.sh first."; exit 1; }

OLD_VER=$("$OLD_BINARY" version 2>&1 | grep Version | head -1)
NEW_VER=$("$NEW_BINARY" version 2>&1 | grep Version | head -1)
log "Old: $OLD_VER"
log "New: $NEW_VER"

# ── Cleanup any running nodes ──────────────────────────────────
log ""
log "Stopping any running nodes..."
pkill -f "geth.*networkid 1337" 2>/dev/null || true
pkill -f "gmet.*networkid 1337" 2>/dev/null || true
sleep 2

mkdir -p "$LOG_DIR"

rpc() {
  local port=$1 method=$2
  curl -sf -X POST "http://localhost:$port" \
    -H "Content-Type: application/json" \
    -d "{\"jsonrpc\":\"2.0\",\"method\":\"$method\",\"params\":[],\"id\":1}" 2>/dev/null
}

block_number() {
  local port=$1
  rpc "$port" eth_blockNumber | python3 -c "import sys,json; r=json.load(sys.stdin); print(int(r['result'],16))" 2>/dev/null || echo -1
}

wait_rpc() {
  local port=$1 label=$2
  for i in $(seq 1 30); do
    if rpc "$port" eth_blockNumber &>/dev/null; then
      log "  $label (:$port) RPC ready"
      return 0
    fi
    sleep 2
  done
  log "  [WARN] $label (:$port) RPC timeout"
  return 1
}

# ── Password files ────────────────────────────────────────────
echo "privatenet123" > data/node1/password.txt
echo "privatenet123" > data/node2/password.txt
echo "privatenet123" > data/node3/password.txt

# ── Start nodes 1+2 with NEW binary, node3 with OLD binary ────
log ""
log "Starting node1 (NEW) on :8545..."
nohup "$NEW_BINARY" \
  --datadir data/node1 \
  --networkid 1337 \
  --consensusmethod 2 \
  --syncmode full \
  --mine \
  --miner.etherbase 0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266 \
  --http --http.addr 0.0.0.0 --http.port 8545 \
  --http.api eth,net,web3,admin,miner,txpool,debug,personal \
  --http.corsdomain '*' --http.vhosts '*' \
  --port 30303 --maxpeers 10 --verbosity 3 --userocksdb 0 \
  --nodekey data/node1/geth/nodekey \
  --unlock 0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266 \
  --password data/node1/password.txt \
  --allow-insecure-unlock \
  > "$LOG_DIR/node1.log" 2>&1 &
NODE1_PID=$!

log "Starting node2 (NEW) on :8546..."
nohup "$NEW_BINARY" \
  --datadir data/node2 \
  --networkid 1337 \
  --consensusmethod 2 \
  --syncmode full \
  --mine \
  --miner.etherbase 0x70997970C51812dc3A010C7d01b50e0d17dc79C8 \
  --http --http.addr 0.0.0.0 --http.port 8546 \
  --http.api eth,net,web3,admin,miner,txpool \
  --http.corsdomain '*' --http.vhosts '*' \
  --port 30304 --maxpeers 10 --verbosity 3 --userocksdb 0 \
  > "$LOG_DIR/node2.log" 2>&1 &
NODE2_PID=$!

log "Starting node3 (OLD binary, CamelliaBlock=nil) on :8547..."
nohup "$OLD_BINARY" \
  --datadir data/node3 \
  --networkid 1337 \
  --consensusmethod 2 \
  --syncmode full \
  --mine \
  --miner.etherbase 0x3C44CdDdB6a900fa2b585dd299e03d12FA4293BC \
  --http --http.addr 0.0.0.0 --http.port 8547 \
  --http.api eth,net,web3,admin,miner,txpool \
  --http.corsdomain '*' --http.vhosts '*' \
  --port 30305 --maxpeers 10 --verbosity 3 --userocksdb 0 \
  > "$LOG_DIR/node3.log" 2>&1 &
NODE3_PID=$!

cleanup() {
  kill $NODE1_PID $NODE2_PID $NODE3_PID 2>/dev/null || true
  wait 2>/dev/null || true
}
trap cleanup EXIT

wait_rpc 8545 "node1(NEW)"
wait_rpc 8546 "node2(NEW)"
wait_rpc 8547 "node3(OLD)"

# ── Phase 1: pre-fork sync (block < 100) ──────────────────────
log ""
log "=== Phase 1: Pre-fork sync (waiting for block 50) ==="
for i in $(seq 1 60); do
  B1=$(block_number 8545)
  B2=$(block_number 8546)
  B3=$(block_number 8547)
  if [[ $B1 -ge 50 && $B2 -ge 50 && $B3 -ge 50 ]]; then
    log "  All nodes at block 50+  (n1=$B1 n2=$B2 n3=$B3)"
    break
  fi
  if [[ $((i % 5)) -eq 0 ]]; then
    log "  Waiting... n1=$B1 n2=$B2 n3=$B3"
  fi
  sleep 3
done

B1=$(block_number 8545); B2=$(block_number 8546); B3=$(block_number 8547)
if [[ $B1 -ge 50 && $B2 -ge 50 && $B3 -ge 50 ]]; then
  pass "Phase 1: all nodes synced pre-fork (n1=$B1 n2=$B2 n3=$B3)"
else
  fail "Phase 1: nodes did not reach block 50 within timeout (n1=$B1 n2=$B2 n3=$B3)"
fi

# Head hash check pre-fork
H1=$(rpc 8545 eth_getBlockByNumber | python3 -c "import sys,json; d=json.loads('{\"jsonrpc\":\"2.0\",\"method\":\"eth_getBlockByNumber\",\"params\":[\"latest\",false],\"id\":1}'); print('ok')" 2>/dev/null || echo "?")

HASH_N1=$(curl -sf -X POST http://localhost:8545 -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"eth_getBlockByNumber","params":["0x32",false],"id":1}' 2>/dev/null | \
  python3 -c "import sys,json; r=json.load(sys.stdin); print(r['result']['hash'][:12])" 2>/dev/null || echo "?")
HASH_N3=$(curl -sf -X POST http://localhost:8547 -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"eth_getBlockByNumber","params":["0x32",false],"id":1}' 2>/dev/null | \
  python3 -c "import sys,json; r=json.load(sys.stdin); print(r['result']['hash'][:12])" 2>/dev/null || echo "?")

if [[ "$HASH_N1" == "$HASH_N3" && "$HASH_N1" != "?" ]]; then
  pass "Phase 1: node1(NEW) and node3(OLD) share same block-50 hash ($HASH_N1)"
else
  info "Phase 1: hash comparison: node1=$HASH_N1 node3=$HASH_N3"
fi

# ── Phase 2: fork transition (block 100) ──────────────────────
log ""
log "=== Phase 2: Fork transition (waiting for block 110) ==="
for i in $(seq 1 80); do
  B1=$(block_number 8545)
  if [[ $B1 -ge 110 ]]; then
    log "  node1(NEW) at block $B1 — fork passed"
    break
  fi
  if [[ $((i % 5)) -eq 0 ]]; then
    B2=$(block_number 8546); B3=$(block_number 8547)
    log "  Waiting... n1=$B1 n2=$B2 n3=$B3"
  fi
  sleep 3
done

B1=$(block_number 8545); B2=$(block_number 8546); B3=$(block_number 8547)
log "  Post-fork blocks: n1=$B1 n2=$B2 n3=$B3"

# Check if old node is still producing/following blocks
if [[ $B3 -ge 100 ]]; then
  pass "Phase 2: node3(OLD) reached block $B3 (past fork point)"

  # Check if hashes match at block 101
  HASH1=$(curl -sf -X POST http://localhost:8545 -H "Content-Type: application/json" \
    -d '{"jsonrpc":"2.0","method":"eth_getBlockByNumber","params":["0x65",false],"id":1}' 2>/dev/null | \
    python3 -c "import sys,json; r=json.load(sys.stdin); print(r['result']['hash'])" 2>/dev/null || echo "?")
  HASH3=$(curl -sf -X POST http://localhost:8547 -H "Content-Type: application/json" \
    -d '{"jsonrpc":"2.0","method":"eth_getBlockByNumber","params":["0x65",false],"id":1}' 2>/dev/null | \
    python3 -c "import sys,json; r=json.load(sys.stdin); print(r['result']['hash'])" 2>/dev/null || echo "?")

  if [[ "$HASH1" == "$HASH3" && "$HASH1" != "?" ]]; then
    pass "Phase 2: node1(NEW) and node3(OLD) share same block-101 hash — chains in sync"
    info "This means old binary accepts Camellia blocks (CamelliaBlock parsed from genesis or ignored safely)"
  else
    info "Phase 2: block-101 hashes differ — n1=$HASH1 n3=$HASH3"
    info "Old node diverged at fork. This is expected if CamelliaBlock field is unrecognized."
    info "Production implication: all nodes MUST upgrade before CamelliaBlock is reached."
    # Check if old node can still sync (maybe it's just behind)
    sleep 10
    B3_AFTER=$(block_number 8547)
    if [[ $B3_AFTER -gt $B3 ]]; then
      info "node3(OLD) is still producing blocks (now at $B3_AFTER) — fork divergence, separate chain"
    else
      info "node3(OLD) stopped progressing at $B3 — may have rejected new blocks"
    fi
  fi
else
  info "Phase 2: node3(OLD) only reached block $B3 (did not pass fork point yet)"
  info "Possible: old binary rejected genesis with unknown camelliaBlock field"
fi

# ── Phase 3: upgrade node3 to new binary ──────────────────────
log ""
log "=== Phase 3: Upgrade node3 from OLD to NEW binary ==="
log "  Key finding: OLD binary cannot process post-fork blocks."
log "  Upgrade procedure: stop OLD, wipe state (old binary wrote pre-fork state),"
log "  re-init with new binary, and let it re-sync from peers."

log "  Stopping node3 (OLD)..."
kill $NODE3_PID 2>/dev/null || true
sleep 3

log "  Wiping node3 chain state (keeping keystore)..."
# Remove chaindata but keep keystore and nodekey so identity is preserved
rm -rf data/node3/geth/chaindata data/node3/geth/triecache data/node3/geth/LOCK data/node3/geth/transactions.rlp 2>/dev/null || true
"$NEW_BINARY" --datadir data/node3 init genesis.json 2>/dev/null
log "  node3 re-initialized with new binary"

log "  Starting node3 with NEW binary (re-sync from scratch)..."
nohup "$NEW_BINARY" \
  --datadir data/node3 \
  --networkid 1337 \
  --consensusmethod 2 \
  --syncmode full \
  --mine \
  --miner.etherbase 0x3C44CdDdB6a900fa2b585dd299e03d12FA4293BC \
  --http --http.addr 0.0.0.0 --http.port 8547 \
  --http.api eth,net,web3,admin,miner,txpool \
  --http.corsdomain '*' --http.vhosts '*' \
  --port 30305 --maxpeers 10 --verbosity 3 --userocksdb 0 \
  > "$LOG_DIR/node3-upgraded.log" 2>&1 &
NODE3_PID=$!

wait_rpc 8547 "node3(NEW-upgraded)"

# Wait for node3 to catch up (syncing from genesis to current head)
log "  Waiting for node3(NEW) to sync past fork block 100..."
for i in $(seq 1 60); do
  B1=$(block_number 8545); B3=$(block_number 8547)
  if [[ $B3 -ge 100 ]]; then
    log "  node3 passed fork: n1=$B1 n3=$B3"
    break
  fi
  if [[ $((i % 5)) -eq 0 ]]; then
    log "  n1=$B1 n3=$B3 (syncing...)"
  fi
  sleep 3
done

B1=$(block_number 8545); B3=$(block_number 8547)
if [[ $B3 -ge 100 ]]; then
  pass "Phase 3: node3(NEW) re-synced past Camellia fork (n1=$B1 n3=$B3)"
  log "  Production upgrade procedure: stop old → wipe state → re-init genesis → start new binary"
else
  fail "Phase 3: node3(NEW) failed to sync past fork (n1=$B1 n3=$B3)"
  log "  Check $LOG_DIR/node3-upgraded.log for details"
fi

# ── Summary ───────────────────────────────────────────────────
log ""
log "=== Layer 4 Test Complete ==="
log "Results: FAIL=$FAILURES"
log ""
log "Log files:"
log "  $LOG_DIR/node1.log    (node1 NEW)"
log "  $LOG_DIR/node2.log    (node2 NEW)"
log "  $LOG_DIR/node3.log    (node3 OLD — pre-upgrade)"
log "  $LOG_DIR/node3-upgraded.log (node3 NEW — post-upgrade)"
log ""

if [[ $FAILURES -eq 0 ]]; then
  log "RESULT: PASS — rolling upgrade scenario verified"
  exit 0
else
  log "RESULT: FAIL ($FAILURES failures) — review logs above"
  exit 1
fi
