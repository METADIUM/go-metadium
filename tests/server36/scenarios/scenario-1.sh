#!/usr/bin/env bash
# Scenario 1: Pre-fork Compatibility
# All 4 nodes: v1.0.0, CamelliaBlock=200 (far future)
# Run until block 150, verify blocks produced and all nodes synced.
# Expected: identical behavior to v0.10.2 for pre-fork blocks.

set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
source "$SCRIPT_DIR/lib.sh"

SCENARIO="scenario-1-prefork"
RESULT_DIR="$RESULTS_DIR/$SCENARIO"
mkdir -p "$RESULT_DIR"

PASS=0; FAIL=0

log "=== Scenario 1: Pre-fork Compatibility ==="
log "All nodes v1.0.0, CamelliaBlock=200, run to block 150"

# Stop any existing network and wipe data
"$SCRIPT_DIR/stop.sh" --clean 2>/dev/null || true

# Re-init accounts/nodekeys (idempotent)
"$SCRIPT_DIR/setup.sh"

# Start all nodes with v1.0.0 (leveldb/rocksdb) and prefork genesis
export NODE1_IMAGE=gmet-s36-leveldb:latest
export NODE2_IMAGE=gmet-s36-rocksdb:latest
export NODE3_IMAGE=gmet-s36-leveldb:latest
export NODE4_IMAGE=gmet-s36-rocksdb:latest
export NODE2_USEROCKSDB=1
export NODE4_USEROCKSDB=1

start_network "$SCRIPT_DIR/genesis-prefork.json"

# Wait for block 150 on node1 (primary miner)
log "Waiting for block 150..."
if wait_block 150 http://localhost:8545 600; then
  PASS=$((PASS + 1))
  log "PASS: block 150 reached"
else
  FAIL=$((FAIL + 1))
  log "FAIL: block 150 not reached within timeout"
fi

# Save block production log
log "Recording block timestamps..."
> "$RESULT_DIR/blocks.log"
for blk in 50 100 130 150; do
  TS=$(rpc "{\"jsonrpc\":\"2.0\",\"method\":\"eth_getBlockByNumber\",\"params\":[\"$(printf '0x%x' $blk)\",false],\"id\":1}" http://localhost:8545 | \
    python3 -c "import sys,json; d=json.load(sys.stdin); b=d.get('result',{}); print(int(b.get('timestamp','0x0'),16) if b else 0)" 2>/dev/null || echo 0)
  echo "block=$blk timestamp=$TS" >> "$RESULT_DIR/blocks.log"
done

# Verify all 4 nodes reached block 150
log "Verifying all nodes synced to block 150..."
for port in 8545 8546 8547 8548; do
  NODE_BLOCK=$(block_number "http://localhost:$port")
  if [[ "$NODE_BLOCK" -ge 150 ]]; then
    PASS=$((PASS + 1))
    log "PASS: port $port at block $NODE_BLOCK"
  else
    FAIL=$((FAIL + 1))
    log "FAIL: port $port at block $NODE_BLOCK (expected >= 150)"
  fi
done

# Verify no Camellia fork activation (camelliaBlock=200, we're at 150)
log "Verifying pre-fork: PUSH0 opcode should revert at block 150..."
PUSH0_RESULT=$(rpc '{"jsonrpc":"2.0","method":"eth_call","params":[{"from":"0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266","data":"0x5f6020526020600ff3","gas":"0x100000"},"latest"],"id":1}' http://localhost:8545 | \
  python3 -c "import sys,json; d=json.load(sys.stdin); print('error' if 'error' in d else 'ok')" 2>/dev/null || echo "?")
if [[ "$PUSH0_RESULT" == "error" ]]; then
  PASS=$((PASS + 1))
  log "PASS: PUSH0 reverts pre-fork (correct behavior)"
else
  FAIL=$((FAIL + 1))
  log "FAIL: PUSH0 did not revert pre-fork (result=$PUSH0_RESULT)"
fi

# Save config snapshot
cat > "$RESULT_DIR/config.json" <<EOF
{
  "scenario": 1,
  "camelliaBlock": 200,
  "targetBlock": 150,
  "node1Image": "$NODE1_IMAGE",
  "node2Image": "$NODE2_IMAGE",
  "node3Image": "$NODE3_IMAGE",
  "node4Image": "$NODE4_IMAGE",
  "timestamp": "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
}
EOF

write_summary "$RESULT_DIR" $PASS $FAIL "Pre-fork compatibility check at block 150"

"$SCRIPT_DIR/stop.sh" --clean
log "=== Scenario 1 complete: PASS=$PASS FAIL=$FAIL ==="
[[ $FAIL -eq 0 ]]
