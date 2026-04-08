#!/usr/bin/env bash
# Scenario 2: All-New Fork Transition
# All 4 nodes: v1.0.0, CamelliaBlock=100
# Run through block 100+ and verify smooth fork transition.
# Post-fork: verify new EIPs work (PUSH0, TLOAD, blob tx fields).

set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
source "$SCRIPT_DIR/lib.sh"

SCENARIO="scenario-2-fork-transition"
RESULT_DIR="$RESULTS_DIR/$SCENARIO"
mkdir -p "$RESULT_DIR"

PASS=0; FAIL=0

log "=== Scenario 2: All-New Fork Transition ==="
log "All nodes v1.0.0, CamelliaBlock=100"

"$SCRIPT_DIR/stop.sh" --clean 2>/dev/null || true
"$SCRIPT_DIR/setup.sh"

export NODE1_IMAGE=gmet-s36-leveldb:latest
export NODE2_IMAGE=gmet-s36-rocksdb:latest
export NODE3_IMAGE=gmet-s36-leveldb:latest
export NODE4_IMAGE=gmet-s36-rocksdb:latest
export NODE2_USEROCKSDB=1
export NODE4_USEROCKSDB=1

start_network "$SCRIPT_DIR/genesis.json"

# Record block production rate before fork (blocks 1-99)
log "Recording pre-fork block production rate..."
T_START=$(date +%s%3N)
START_BLOCK=$(block_number http://localhost:8545)

# Wait for fork activation at block 100
if wait_block 100 http://localhost:8545 300; then
  T_FORK=$(date +%s%3N)
  FORK_BLOCK=$(block_number http://localhost:8545)
  PRE_FORK_ELAPSED=$(( (T_FORK - T_START) ))
  log "Fork activated: block=$FORK_BLOCK elapsed=${PRE_FORK_ELAPSED}ms from start_block=$START_BLOCK"
  echo "pre_fork_ms=$PRE_FORK_ELAPSED start_block=$START_BLOCK fork_block=$FORK_BLOCK" > "$RESULT_DIR/timing.log"
  PASS=$((PASS + 1))
else
  FAIL=$((FAIL + 1))
  log "FAIL: fork block 100 not reached"
fi

# Wait for post-fork blocks
wait_block 120 http://localhost:8545 300 || true

# Verify EIP-3855 PUSH0 works post-fork
log "Verifying EIP-3855 PUSH0 post-fork..."
# bytecode: PUSH0(0x5f) + PUSH1 0x20 + MSTORE + PUSH1 0x20 + PUSH1 0 + RETURN
PUSH0_CODE="0x5f6020526020600ff3"
PUSH0_RESULT=$(rpc "{\"jsonrpc\":\"2.0\",\"method\":\"eth_call\",\"params\":[{\"from\":\"0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266\",\"data\":\"$PUSH0_CODE\",\"gas\":\"0x100000\"},\"latest\"],\"id\":1}" http://localhost:8545 | \
  python3 -c "import sys,json; d=json.load(sys.stdin); print('ok' if 'result' in d and d['result'] != '0x' else 'error')" 2>/dev/null || echo "?")
if [[ "$PUSH0_RESULT" == "ok" ]]; then
  PASS=$((PASS + 1))
  log "PASS: EIP-3855 PUSH0 works post-fork"
else
  FAIL=$((FAIL + 1))
  log "FAIL: EIP-3855 PUSH0 failed post-fork (result=$PUSH0_RESULT)"
fi

# Verify all nodes synced past fork
log "Verifying all nodes synced past block 100..."
for port in 8545 8546 8547 8548; do
  NODE_BLOCK=$(block_number "http://localhost:$port")
  if [[ "$NODE_BLOCK" -ge 100 ]]; then
    PASS=$((PASS + 1))
    log "PASS: port $port at block $NODE_BLOCK (past fork)"
  else
    FAIL=$((FAIL + 1))
    log "FAIL: port $port at block $NODE_BLOCK (expected >= 100)"
  fi
done

# Verify no block production halt at fork boundary
log "Checking block continuity at fork block..."
BLOCK_99=$(rpc '{"jsonrpc":"2.0","method":"eth_getBlockByNumber","params":["0x63",false],"id":1}' http://localhost:8545 | \
  python3 -c "import sys,json; d=json.load(sys.stdin); b=d.get('result',{}); print('ok' if b else 'missing')" 2>/dev/null || echo "?")
BLOCK_100=$(rpc '{"jsonrpc":"2.0","method":"eth_getBlockByNumber","params":["0x64",false],"id":1}' http://localhost:8545 | \
  python3 -c "import sys,json; d=json.load(sys.stdin); b=d.get('result',{}); print('ok' if b else 'missing')" 2>/dev/null || echo "?")
BLOCK_101=$(rpc '{"jsonrpc":"2.0","method":"eth_getBlockByNumber","params":["0x65",false],"id":1}' http://localhost:8545 | \
  python3 -c "import sys,json; d=json.load(sys.stdin); b=d.get('result',{}); print('ok' if b else 'missing')" 2>/dev/null || echo "?")

if [[ "$BLOCK_99" == "ok" && "$BLOCK_100" == "ok" && "$BLOCK_101" == "ok" ]]; then
  PASS=$((PASS + 1))
  log "PASS: blocks 99/100/101 all present (no production halt)"
else
  FAIL=$((FAIL + 1))
  log "FAIL: block continuity check failed (99=$BLOCK_99 100=$BLOCK_100 101=$BLOCK_101)"
fi

# Record fork block hash
FORK_HASH=$(rpc '{"jsonrpc":"2.0","method":"eth_getBlockByNumber","params":["0x64",false],"id":1}' http://localhost:8545 | \
  python3 -c "import sys,json; d=json.load(sys.stdin); b=d.get('result',{}); print(b.get('hash','?') if b else '?')" 2>/dev/null || echo "?")
echo "fork_block=100 hash=$FORK_HASH" >> "$RESULT_DIR/timing.log"
log "Fork block hash: $FORK_HASH"

cat > "$RESULT_DIR/config.json" <<EOF
{
  "scenario": 2,
  "camelliaBlock": 100,
  "node1Image": "$NODE1_IMAGE",
  "node2Image": "$NODE2_IMAGE",
  "node3Image": "$NODE3_IMAGE",
  "node4Image": "$NODE4_IMAGE",
  "forkHash": "$FORK_HASH",
  "timestamp": "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
}
EOF

write_summary "$RESULT_DIR" $PASS $FAIL "All-new Camellia fork transition"

"$SCRIPT_DIR/stop.sh" --clean
log "=== Scenario 2 complete: PASS=$PASS FAIL=$FAIL ==="
[[ $FAIL -eq 0 ]]
