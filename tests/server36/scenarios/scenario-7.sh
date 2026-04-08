#!/usr/bin/env bash
# Scenario 7: Long-term Stability
# All v1.0.0, CamelliaBlock=100, mixed DB (LevelDB/RocksDB).
# Run for 72+ hours, monitor every hour, run RPC tests every 6 hours.
# Alert on: block production halt, peer disconnection, memory spike.

set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
source "$SCRIPT_DIR/lib.sh"

SCENARIO="scenario-7-longterm"
RESULT_DIR="$RESULTS_DIR/$SCENARIO"
RPC_TEST_DIR="$RESULT_DIR/rpc-tests"
mkdir -p "$RESULT_DIR" "$RPC_TEST_DIR"

PASS=0; FAIL=0

# Default duration: 72 hours (259200 seconds)
# Override: DURATION_HOURS=4 ./scenario-7.sh
DURATION_HOURS="${DURATION_HOURS:-72}"
DURATION_SECS=$((DURATION_HOURS * 3600))
CHECK_INTERVAL="${CHECK_INTERVAL:-3600}"  # 1 hour default

log "=== Scenario 7: Long-term Stability ==="
log "Duration: ${DURATION_HOURS}h | Check interval: ${CHECK_INTERVAL}s | CamelliaBlock=100"

"$SCRIPT_DIR/stop.sh" --clean 2>/dev/null || true
"$SCRIPT_DIR/setup.sh"

export NODE1_IMAGE=gmet-s36-leveldb:latest
export NODE2_IMAGE=gmet-s36-rocksdb:latest
export NODE3_IMAGE=gmet-s36-leveldb:latest
export NODE4_IMAGE=gmet-s36-rocksdb:latest
export NODE2_USEROCKSDB=1
export NODE4_USEROCKSDB=1

start_network "$SCRIPT_DIR/genesis.json"

# Wait for fork activation
wait_block 110 http://localhost:8545 300
log "Fork activated. Starting ${DURATION_HOURS}h monitoring loop..."

STATS_FILE="$RESULT_DIR/hourly-stats.jsonl"
START_TIME=$(date +%s)
END_TIME=$((START_TIME + DURATION_SECS))

LAST_BLOCK_NODE1=0
STALL_COUNT=0
MAX_STALL_COUNT=3  # Alert after 3 consecutive stalled checks

check_once() {
  local TIMESTAMP
  TIMESTAMP=$(date -u +%Y-%m-%dT%H:%M:%SZ)
  local HOUR_NUM=$(( ($(date +%s) - START_TIME) / CHECK_INTERVAL ))
  log "--- Hour check #$HOUR_NUM ($TIMESTAMP) ---"

  local ALL_OK=true
  local ports=(8545 8546 8547 8548)
  local names=(node1 node2 node3 node4)
  local containers=(gmet-s36-node1 gmet-s36-node2 gmet-s36-node3 gmet-s36-node4)

  for i in 0 1 2 3; do
    local port="${ports[$i]}"
    local name="${names[$i]}"
    local container="${containers[$i]}"

    local block peers mem disk
    block=$(block_number "http://localhost:$port")
    peers=$(peer_count "http://localhost:$port")
    mem=$(docker stats --no-stream --format "{{.MemUsage}}" "$container" 2>/dev/null | awk '{print $1}' || echo "?")
    disk=$(docker exec "$container" du -sh /data/geth/gmet/chaindata 2>/dev/null | awk '{print $1}' || echo "?")

    local running
    running=$(docker inspect --format '{{.State.Running}}' "$container" 2>/dev/null || echo "false")

    echo "{\"ts\":\"$TIMESTAMP\",\"hour\":$HOUR_NUM,\"node\":\"$name\",\"block\":$block,\"peers\":$peers,\"mem\":\"$mem\",\"disk\":\"$disk\",\"running\":$running}" >> "$STATS_FILE"
    log "  $name: block=$block peers=$peers mem=$mem disk=$disk running=$running"

    # Alert conditions
    if [[ "$running" != "true" ]]; then
      warn "ALERT: $name container is not running!"
      ALL_OK=false
    fi
    if [[ "$peers" -eq 0 && "$name" != "node1" ]]; then
      warn "ALERT: $name has 0 peers!"
      ALL_OK=false
    fi
  done

  # Check node1 block production stall
  local NODE1_BLOCK
  NODE1_BLOCK=$(block_number http://localhost:8545)
  if [[ "$NODE1_BLOCK" -le "$LAST_BLOCK_NODE1" ]]; then
    STALL_COUNT=$((STALL_COUNT + 1))
    warn "ALERT: Block production stall detected ($STALL_COUNT consecutive, last=$LAST_BLOCK_NODE1 current=$NODE1_BLOCK)"
    if [[ $STALL_COUNT -ge $MAX_STALL_COUNT ]]; then
      warn "CRITICAL: Block production halted for $STALL_COUNT checks!"
      FAIL=$((FAIL + 1))
      ALL_OK=false
    fi
  else
    STALL_COUNT=0
    PASS=$((PASS + 1))
    LAST_BLOCK_NODE1=$NODE1_BLOCK
  fi

  $ALL_OK && return 0 || return 1
}

run_rpc_test() {
  local logfile="$RPC_TEST_DIR/$(date -u +%Y-%m-%dT%H).log"
  local rpc_test_script="$SCRIPT_DIR/../../scripts/rpc-test-full.sh"
  if [[ -x "$rpc_test_script" ]]; then
    log "Running rpc-test-full.sh..."
    RPC=http://localhost:8545 bash "$rpc_test_script" 2>&1 | \
      sed 's/\x1b\[[0-9;]*m//g' > "$logfile" 2>&1 || true
    log "RPC test saved: $logfile"
  fi
}

# TX generation interval (default: 10 minutes)
TX_INTERVAL="${TX_INTERVAL:-600}"
TX_BATCH="${TX_BATCH:-5}"  # normal txs per batch (+ 1 fee delegation + 1 blob each round)
TX_GENERATOR="$SCRIPT_DIR/tx-generator/tx-generator"

generate_txs() {
  local batch_size="$1"
  log "Generating mixed txs (normal=$batch_size + 1 fee-delegation + 1 blob)..."
  "$TX_GENERATOR" -batch "$batch_size" -json 2>/dev/null || echo '{"normal_sent":0,"normal_fail":0,"fd_sent":0,"fd_fail":0,"blob_sent":0,"blob_fail":0}'
}

# Initial RPC test
run_rpc_test

CHECK_NUM=0
TX_TOTAL_NORMAL=0
TX_TOTAL_FD=0
TX_TOTAL_BLOB=0
TX_TOTAL_FAIL=0
NEXT_CHECK_TIME=$(date +%s)
NEXT_RPC_TEST_TIME=$(( $(date +%s) + 6*3600 ))  # first RPC test after 6h
NEXT_TX_TIME=$(( $(date +%s) + TX_INTERVAL ))    # first tx batch after TX_INTERVAL

while [[ $(date +%s) -lt $END_TIME ]]; do
  NOW=$(date +%s)

  if [[ $NOW -ge $NEXT_CHECK_TIME ]]; then
    CHECK_NUM=$((CHECK_NUM + 1))
    check_once || true
    NEXT_CHECK_TIME=$((NOW + CHECK_INTERVAL))
  fi

  if [[ $NOW -ge $NEXT_TX_TIME ]]; then
    TX_RESULT=$(generate_txs "$TX_BATCH")
    TX_NS=$(echo "$TX_RESULT" | python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('normal_sent',0))" 2>/dev/null || echo 0)
    TX_FS=$(echo "$TX_RESULT" | python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('fd_sent',0))" 2>/dev/null || echo 0)
    TX_BS=$(echo "$TX_RESULT" | python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('blob_sent',0))" 2>/dev/null || echo 0)
    TX_NF=$(echo "$TX_RESULT" | python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('normal_fail',0)+d.get('fd_fail',0)+d.get('blob_fail',0))" 2>/dev/null || echo 0)
    TX_TOTAL_NORMAL=$((TX_TOTAL_NORMAL + TX_NS))
    TX_TOTAL_FD=$((TX_TOTAL_FD + TX_FS))
    TX_TOTAL_BLOB=$((TX_TOTAL_BLOB + TX_BS))
    TX_TOTAL_FAIL=$((TX_TOTAL_FAIL + TX_NF))
    log "TX batch: normal=$TX_NS fd=$TX_FS blob=$TX_BS fail=$TX_NF (total: normal=$TX_TOTAL_NORMAL fd=$TX_TOTAL_FD blob=$TX_TOTAL_BLOB fail=$TX_TOTAL_FAIL)"
    NEXT_TX_TIME=$((NOW + TX_INTERVAL))
  fi

  if [[ $NOW -ge $NEXT_RPC_TEST_TIME ]]; then
    run_rpc_test
    NEXT_RPC_TEST_TIME=$((NOW + 6*3600))
  fi

  # Sleep until next event
  NEXT_EVENT=$NEXT_CHECK_TIME
  [[ $NEXT_TX_TIME -lt $NEXT_EVENT ]] && NEXT_EVENT=$NEXT_TX_TIME
  [[ $NEXT_RPC_TEST_TIME -lt $NEXT_EVENT ]] && NEXT_EVENT=$NEXT_RPC_TEST_TIME
  SLEEP_TIME=$((NEXT_EVENT - $(date +%s)))
  [[ $SLEEP_TIME -gt 0 ]] && sleep "$SLEEP_TIME" || sleep 10
done

log "=== 72h monitoring complete ==="
log "TX summary: normal=$TX_TOTAL_NORMAL fd=$TX_TOTAL_FD blob=$TX_TOTAL_BLOB fail=$TX_TOTAL_FAIL"

# Final analysis
python3 - "$STATS_FILE" <<'PYEOF' > "$RESULT_DIR/stability-analysis.md"
import sys, json

stats_file = sys.argv[1]

records = []
try:
    with open(stats_file) as f:
        for line in f:
            try:
                records.append(json.loads(line))
            except:
                pass
except FileNotFoundError:
    print("No stats recorded.")
    exit(0)

node1_records = [r for r in records if r.get('node') == 'node1']
if not node1_records:
    print("No node1 records found.")
    exit(0)

max_block = max(r['block'] for r in node1_records)
min_block = min(r['block'] for r in node1_records)
checks = len(node1_records)

print("# Scenario 7: Long-term Stability Analysis")
print()
print("## Summary")
print(f"- Total hourly checks: {checks}")
print(f"- Block range: {min_block} - {max_block}")
print(f"- Total blocks produced: {max_block - min_block}")
print()

# Check for stalls (same block across consecutive checks)
stalls = 0
for i in range(1, len(node1_records)):
    if node1_records[i]['block'] <= node1_records[i-1]['block']:
        stalls += 1
print(f"- Block production stalls: {stalls}")
print()

# Per-node summary
print("## Per-Node Block Heights")
print()
for node in ['node1', 'node2', 'node3', 'node4']:
    node_recs = [r for r in records if r.get('node') == node]
    if node_recs:
        final_block = node_recs[-1]['block']
        max_mem = max((r.get('mem','0').rstrip('GiB').rstrip('MiB') for r in node_recs), default='?')
        print(f"- {node}: final_block={final_block}")

print()
print(f"**Result: {'STABLE' if stalls == 0 else f'UNSTABLE ({stalls} stalls)'}**")
PYEOF

write_summary "$RESULT_DIR" $PASS $FAIL "${DURATION_HOURS}h stability run complete"

"$SCRIPT_DIR/stop.sh" --clean
log "=== Scenario 7 complete: PASS=$PASS FAIL=$FAIL ==="
