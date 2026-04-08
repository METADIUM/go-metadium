#!/usr/bin/env bash
# Scenario 3: Mixed Version at Fork (CRITICAL)
# Nodes 1,2,3: v1.0.0 (CamelliaBlock=100)
# Node 4: v0.10.2 (no camelliaBlock awareness, sync only)
# Run through block 100, document exact node4 behavior.
#
# State is PRESERVED after this scenario for use by scenario-4.sh.

set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
source "$SCRIPT_DIR/lib.sh"

SCENARIO="scenario-3-mixed-version"
RESULT_DIR="$RESULTS_DIR/$SCENARIO"
mkdir -p "$RESULT_DIR"

PASS=0; FAIL=0

log "=== Scenario 3: Mixed Version at Fork (CRITICAL) ==="
log "Nodes 1-3: v1.0.0 | Node 4: v0.10.2 | CamelliaBlock=100"

"$SCRIPT_DIR/stop.sh" --clean 2>/dev/null || true
"$SCRIPT_DIR/setup.sh"

export NODE1_IMAGE=gmet-s36-leveldb:latest
export NODE2_IMAGE=gmet-s36-rocksdb:latest
export NODE3_IMAGE=gmet-s36-leveldb:latest
export NODE4_IMAGE=gmet-s36-old:latest
export NODE2_USEROCKSDB=1
export NODE4_USEROCKSDB=1   # gmet-old v0.10.2 uses RocksDB by default (statically compiled)

start_network "$SCRIPT_DIR/genesis.json"

# Wait for fork activation at block 100 on nodes 1-3
log "Waiting for fork block 100 on v1.0.0 nodes..."
if wait_block 100 http://localhost:8545 300; then
  PASS=$((PASS + 1))
  log "PASS: nodes 1-3 reached block 100 (fork activated)"
else
  FAIL=$((FAIL + 1))
  log "FAIL: nodes 1-3 did not reach block 100"
fi

# Wait a bit longer and record node4 behavior
log "Waiting 30s to observe node4 behavior post-fork..."
sleep 30

# Check node4 current block
NODE4_BLOCK=$(block_number http://localhost:8548)
NODE1_BLOCK=$(block_number http://localhost:8545)
log "Node1 (v1.0.0) block: $NODE1_BLOCK"
log "Node4 (v0.10.2) block: $NODE4_BLOCK"

# Save node4 logs for analysis (set +e: log capture is best-effort)
set +e
log "Capturing node4 logs..."
docker logs gmet-s36-node4 > "$RESULT_DIR/node4-behavior.log" 2>&1
docker logs gmet-s36-node4 2>&1 | grep -i -E "err|warn|fork|camellia|invalid|reject|disconnect" \
  > "$RESULT_DIR/node4-errors.log" 2>/dev/null
set -e

# Determine node4 behavior
if [[ "$NODE4_BLOCK" -ge 100 ]]; then
  BEHAVIOR="synced_through_fork"
  log "Node4 SYNCED through fork (unexpected for old binary)"
elif [[ "$NODE4_BLOCK" -gt 0 && "$NODE4_BLOCK" -lt 100 ]]; then
  BEHAVIOR="stalled_at_$NODE4_BLOCK"
  PASS=$((PASS + 1))
  log "PASS: Node4 stalled at block $NODE4_BLOCK (expected behavior for old binary)"
else
  BEHAVIOR="not_producing"
  log "Node4 block=0 (may not be syncing at all - likely protocol incompatibility)"
fi

# Check node4 peer connectivity (best-effort, node4 may be disconnected)
NODE4_PEERS="0"
NODE1_PEERS="0"
set +e
NODE4_PEERS=$(peer_count http://localhost:8548 2>/dev/null || echo "0")
NODE1_PEERS=$(peer_count http://localhost:8545 2>/dev/null || echo "0")
set -e
log "Node4 peer count: $NODE4_PEERS"
log "Node1 peer count: $NODE1_PEERS"

cat > "$RESULT_DIR/node4-analysis.md" <<EOF
# Node4 (v0.10.2) Behavior at Fork Block 100

## Summary
- Node4 behavior: $BEHAVIOR
- Node4 block at observation: $NODE4_BLOCK
- Node1 block at observation: $NODE1_BLOCK
- Node4 peers: $NODE4_PEERS
- Node1 peers: $NODE1_PEERS
- Timestamp: $(date -u +%Y-%m-%dT%H:%M:%SZ)

## Classification
$(if [[ "$NODE4_BLOCK" -lt 100 && "$NODE4_BLOCK" -gt 0 ]]; then
  echo "Node4 synced up to pre-fork blocks but stalled at the fork boundary."
  echo "This is expected behavior when an old binary encounters unknown fork rules."
elif [[ "$NODE4_BLOCK" -ge 100 ]]; then
  echo "Node4 synced through the fork block (unexpected - old binary accepted new blocks)."
else
  echo "Node4 appears to be non-responsive or not syncing."
fi)

## Error Patterns
See node4-errors.log for filtered error/warning messages.
EOF

cat > "$RESULT_DIR/config.json" <<EOF
{
  "scenario": 3,
  "camelliaBlock": 100,
  "node1Image": "$NODE1_IMAGE",
  "node2Image": "$NODE2_IMAGE",
  "node3Image": "$NODE3_IMAGE",
  "node4Image": "$NODE4_IMAGE",
  "node4BehaviorBlock": $NODE4_BLOCK,
  "node4Behavior": "$BEHAVIOR",
  "timestamp": "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
}
EOF

write_summary "$RESULT_DIR" $PASS $FAIL "Mixed version at fork - node4 behavior: $BEHAVIOR"

# IMPORTANT: Preserve state for Scenario 4 (Late Upgrade)
log "Preserving network state for Scenario 4..."
cd "$SCRIPT_DIR" && docker compose stop node4 2>/dev/null || true

# Save state for scenario 4
cat > "$SCRIPT_DIR/state.json" <<EOF
{
  "scenario": 3,
  "preserved": true,
  "node1Block": $NODE1_BLOCK,
  "node4Block": $NODE4_BLOCK,
  "timestamp": "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
}
EOF
log "State saved to state.json. Run scenario-4.sh to continue."

log "=== Scenario 3 complete: PASS=$PASS FAIL=$FAIL ==="
log "NOTE: Network is still running (nodes 1-3). Scenario 4 will upgrade node4."
[[ $FAIL -eq 0 ]]
