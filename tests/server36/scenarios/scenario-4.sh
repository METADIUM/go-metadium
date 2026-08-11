#!/usr/bin/env bash
# Scenario 4: Late Upgrade
# Continue from Scenario 3 state (node4 on old binary, post-fork).
# Upgrade node4 to v1.0.0 and verify it re-syncs to current chain head.

set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
source "$SCRIPT_DIR/lib.sh"

SCENARIO="scenario-4-late-upgrade"
RESULT_DIR="$RESULTS_DIR/$SCENARIO"
mkdir -p "$RESULT_DIR"

PASS=0; FAIL=0

log "=== Scenario 4: Late Upgrade ==="

# Verify scenario 3 state exists
if [[ ! -f "$SCRIPT_DIR/state.json" ]]; then
  log "ERROR: state.json not found. Run scenario-3.sh first."
  exit 1
fi
S3_SCENARIO=$(python3 -c "import json; d=json.load(open('$SCRIPT_DIR/state.json')); print(d.get('scenario',0))" 2>/dev/null || echo "0")
if [[ "$S3_SCENARIO" != "3" ]]; then
  log "ERROR: state.json is not from scenario 3 (got scenario=$S3_SCENARIO). Run scenario-3.sh first."
  exit 1
fi
log "Scenario 3 state confirmed. Upgrading node4 to v1.0.0..."

# Get current chain head from nodes 1-3
NODE1_BLOCK=$(block_number http://localhost:8545)
log "Current chain head (node1): $NODE1_BLOCK"

# Record pre-upgrade node4 state
NODE4_BLOCK_PRE=$(block_number http://localhost:8548 2>/dev/null || echo "0")
log "Node4 block before upgrade: $NODE4_BLOCK_PRE"

# Upgrade node4: change image from gmet-s36-old to gmet-s36-rocksdb
export NODE4_IMAGE=gmet-s36-rocksdb:latest
export NODE4_USEROCKSDB=1

# Recreate only node4 with new image
SYNC_START=$(date +%s%3N)
SYNC_END=$SYNC_START  # default; overwritten on success
NODE4_BLOCK_POST=0
cd "$SCRIPT_DIR"
docker compose up -d --no-deps node4
log "Node4 upgraded to gmet-s36-rocksdb. Waiting for it to come online..."

# Wait for node4 RPC to respond
for i in $(seq 1 60); do
  if rpc_alive http://localhost:8548; then
    log "Node4 RPC online (attempt $i)"
    break
  fi
  sleep 2
done

# Connect node4 to peers
sleep 3
BOOTNODE_ENODE=$(python3 -c "import json; print(json.load(open('$SCRIPT_DIR/static-nodes.json'))[0])" 2>/dev/null || true)
if [[ -n "$BOOTNODE_ENODE" ]]; then
  curl -sf -X POST -H "Content-Type: application/json" \
    --data "{\"jsonrpc\":\"2.0\",\"method\":\"admin_addPeer\",\"params\":[\"$BOOTNODE_ENODE\"],\"id\":1}" \
    http://localhost:8548 &>/dev/null || true
fi

# Wait for node4 to sync to current chain head
TARGET_BLOCK=$((NODE1_BLOCK + 5))  # allow some new blocks to be produced
log "Waiting for node4 to sync to block $TARGET_BLOCK..."
if wait_block "$TARGET_BLOCK" http://localhost:8548 300; then
  SYNC_END=$(date +%s%3N)
  SYNC_TIME=$(( (SYNC_END - SYNC_START) ))
  NODE4_BLOCK_POST=$(block_number http://localhost:8548)
  PASS=$((PASS + 1))
  log "PASS: Node4 synced to block $NODE4_BLOCK_POST in ${SYNC_TIME}ms"
  echo "sync_time_ms=$SYNC_TIME pre_block=$NODE4_BLOCK_PRE post_block=$NODE4_BLOCK_POST" > "$RESULT_DIR/sync-metrics.log"
else
  SYNC_END=$(date +%s%3N)
  FAIL=$((FAIL + 1))
  NODE4_BLOCK_POST=$(block_number http://localhost:8548 2>/dev/null || echo "0")
  log "FAIL: Node4 did not sync in time (current=$NODE4_BLOCK_POST)"
  echo "sync_time_ms=timeout pre_block=$NODE4_BLOCK_PRE post_block=$NODE4_BLOCK_POST" > "$RESULT_DIR/sync-metrics.log"
fi

# Verify node4 peers (should reconnect to v1.0.0 nodes)
NODE4_PEERS=$(peer_count http://localhost:8548)
if [[ "$NODE4_PEERS" -ge 1 ]]; then
  PASS=$((PASS + 1))
  log "PASS: Node4 has $NODE4_PEERS peer(s)"
else
  FAIL=$((FAIL + 1))
  log "FAIL: Node4 has no peers"
fi

# Final status
print_node_status

cat > "$RESULT_DIR/config.json" <<EOF
{
  "scenario": 4,
  "upgradeFromImage": "gmet-s36-old:latest",
  "upgradeToImage": "$NODE4_IMAGE",
  "node4BlockPreUpgrade": $NODE4_BLOCK_PRE,
  "node4BlockPostUpgrade": $((NODE4_BLOCK_POST)),
  "syncTimeMs": $((SYNC_END - SYNC_START)),
  "timestamp": "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
}
EOF

write_summary "$RESULT_DIR" $PASS $FAIL "Late upgrade: node4 synced in $((SYNC_END - SYNC_START))ms"

"$SCRIPT_DIR/stop.sh" --clean
rm -f "$SCRIPT_DIR/state.json"
log "=== Scenario 4 complete: PASS=$PASS FAIL=$FAIL ==="
[[ $FAIL -eq 0 ]]
