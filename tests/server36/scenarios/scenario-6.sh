#!/usr/bin/env bash
# Scenario 6: Performance Benchmarks
# Compare v0.10.2 vs v1.0.0 pre-fork vs v1.0.0 post-fork
# Measures: TPS, RPC latency, block production speed, memory/disk

set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
source "$SCRIPT_DIR/lib.sh"

SCENARIO="scenario-6-performance"
RESULT_DIR="$RESULTS_DIR/$SCENARIO"
mkdir -p "$RESULT_DIR"

PASS=0; FAIL=0

log "=== Scenario 6: Performance Benchmarks ==="
log "Comparing v0.10.2 vs v1.0.0 (pre-fork and post-fork)"

# ===================================================================
# Helper: measure block production speed (N blocks, return ms elapsed)
# ===================================================================
measure_block_speed() {
  local endpoint="$1"
  local blocks="$2"
  local timeout="${3:-300}"
  local start_block
  start_block=$(block_number "$endpoint")
  local target=$((start_block + blocks))
  local t_start
  t_start=$(date +%s%3N)
  if wait_block "$target" "$endpoint" "$timeout"; then
    local t_end
    t_end=$(date +%s%3N)
    echo $((t_end - t_start))
  else
    echo "timeout"
  fi
}

# ===================================================================
# Helper: send N simple transfers and measure TPS
# Using eth_sendTransaction (accounts are unlocked)
# ===================================================================
# measure_max_tps: flood N txs concurrently using Python thread pool,
# wait for all confirmations, report peak TPS.
# Usage: measure_max_tps <endpoint> <count>
# Returns: "<total_ms>ms sent=N confirmed=M tps=X.XX submit_ms=Y"
# measure_max_tps: flood txs concurrently across 3 nodes and 3 accounts
# Usage: measure_max_tps <primary_endpoint> <count>
measure_max_tps() {
  local endpoint="$1"
  local count="$2"

  python3 - "$count" <<'PYEOF'
import sys, json, urllib.request, time
from concurrent.futures import ThreadPoolExecutor

count = int(sys.argv[1])

# 3 sender/endpoint pairs - spread load across all miner nodes
SENDERS = [
    ("http://localhost:8545", "0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266"),
    ("http://localhost:8546", "0x70997970C51812dc3A010C7d01b50e0d17dc79C8"),
    ("http://localhost:8547", "0x3C44CdDdB6a900fa2b585dd299e03d12FA4293BC"),
]
TO_ADDR = "0x90F79bf6EB2c4f870365E785982E1f101E93b906"  # node4 (recipient)

def send_tx(i):
    ep, fr = SENDERS[i % len(SENDERS)]
    data = json.dumps({"jsonrpc":"2.0","method":"eth_sendTransaction",
                       "params":[{"from":fr,"to":TO_ADDR,"value":"0x1","gas":"0x5208"}],"id":i+1}).encode()
    try:
        req = urllib.request.Request(ep, data=data, headers={"Content-Type":"application/json"})
        resp = json.loads(urllib.request.urlopen(req, timeout=15).read())
        return (ep, resp.get("result",""))
    except:
        return (ep, "")

def get_receipt(ep, h):
    data = json.dumps({"jsonrpc":"2.0","method":"eth_getTransactionReceipt","params":[h],"id":1}).encode()
    try:
        req = urllib.request.Request(ep, data=data, headers={"Content-Type":"application/json"})
        return json.loads(urllib.request.urlopen(req, timeout=5).read()).get("result") is not None
    except:
        return False

workers = min(150, count)
t_start = time.time()
with ThreadPoolExecutor(max_workers=workers) as pool:
    results = list(pool.map(send_tx, range(count)))
    sent = [(ep, h) for ep, h in results if h]

t_submit = time.time()
submit_ms = int((t_submit - t_start) * 1000)

# wait for all last-txs from each endpoint to confirm (up to 180s)
deadline = time.time() + 180
# pick last hash per endpoint
last_per_ep = {}
for ep, h in sent:
    last_per_ep[ep] = h
while time.time() < deadline:
    if all(get_receipt(ep, h) for ep, h in last_per_ep.items()):
        break
    time.sleep(0.5)

t_end = time.time()
total_ms = int((t_end - t_start) * 1000)

with ThreadPoolExecutor(max_workers=50) as pool:
    confirmed = sum(1 for ok in pool.map(lambda x: get_receipt(*x), sent) if ok)

tps = confirmed * 1000 / total_ms if total_ms > 0 else 0
# peak_tps: how fast we confirmed after submission (exclude wait overhead)
confirm_ms = int((t_end - t_submit) * 1000)
peak_tps = confirmed * 1000 / (submit_ms + confirm_ms) if (submit_ms + confirm_ms) > 0 else tps
print(f"{total_ms}ms sent={len(sent)} confirmed={confirmed} tps={tps:.2f} submit_ms={submit_ms} confirm_ms={confirm_ms}")
PYEOF
}

measure_tps() {
  local endpoint="$1"
  local tx_count="$2"
  local from_addr="$3"
  local to_addr="0x70997970C51812dc3A010C7d01b50e0d17dc79C8"

  local t_start
  t_start=$(date +%s%3N)

  local txhashes=()
  for i in $(seq 1 "$tx_count"); do
    local txhash
    txhash=$(rpc "{\"jsonrpc\":\"2.0\",\"method\":\"eth_sendTransaction\",\"params\":[{\"from\":\"$from_addr\",\"to\":\"$to_addr\",\"value\":\"0x1\",\"gas\":\"0x5208\"}],\"id\":$i}" "$endpoint" | \
      python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('result',''))" 2>/dev/null || echo "")
    [[ -n "$txhash" ]] && txhashes+=("$txhash")
  done

  local tx_sent=${#txhashes[@]}
  log "  Sent $tx_sent txs, waiting for confirmations..."

  # Wait for the last transaction to be mined
  local last_hash="${txhashes[-1]:-}"
  if [[ -n "$last_hash" ]]; then
    wait_receipt "$last_hash" "$endpoint" 120 || true
  fi

  local t_end
  t_end=$(date +%s%3N)
  local elapsed_ms=$((t_end - t_start))

  # Count confirmed txs
  local confirmed=0
  for h in "${txhashes[@]}"; do
    local status
    status=$(rpc "{\"jsonrpc\":\"2.0\",\"method\":\"eth_getTransactionReceipt\",\"params\":[\"$h\"],\"id\":1}" "$endpoint" | \
      python3 -c "import sys,json; d=json.load(sys.stdin); r=d.get('result',{}); print(r.get('status','') if r else '')" 2>/dev/null || echo "")
    [[ "$status" == "0x1" ]] && confirmed=$((confirmed + 1))
  done

  echo "${elapsed_ms}ms sent=$tx_sent confirmed=$confirmed tps=$(python3 -c "print(f'{$confirmed*1000/$elapsed_ms:.2f}' if $elapsed_ms>0 else '0')")"
}

# ===================================================================
# 6a + 6b + 6c: Benchmark v0.10.2 (4-node all-old)
# ===================================================================
log "--- 6a/6b/6c: Benchmarking v0.10.2 ---"
"$SCRIPT_DIR/stop.sh" --clean 2>/dev/null || true
"$SCRIPT_DIR/setup.sh"

export NODE1_IMAGE=gmet-s36-old:latest
export NODE2_IMAGE=gmet-s36-old:latest
export NODE3_IMAGE=gmet-s36-old:latest
export NODE4_IMAGE=gmet-s36-old:latest
export NODE2_USEROCKSDB=1
export NODE4_USEROCKSDB=1

start_network "$SCRIPT_DIR/genesis.json"
wait_block 10 http://localhost:8545 120

# 6a: Block production speed (old)
log "6a: Block production speed (v0.10.2, 100 blocks)..."
OLD_BLOCK_SPEED=$(measure_block_speed http://localhost:8545 100 300)
log "  v0.10.2 100-block time: ${OLD_BLOCK_SPEED}ms"

# 6b: TPS (old) - 100 txs
log "6b: TPS (v0.10.2, 100 txs)..."
OLD_TPS=$(measure_tps http://localhost:8545 100 "0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266")
log "  v0.10.2 TPS result: $OLD_TPS"

# 6c: RPC latency (old)
log "6c: RPC latency (v0.10.2)..."
bench_rpc_latency "eth_blockNumber" http://localhost:8545 100 "$RESULT_DIR/latency-old-blockNumber.log"
bench_rpc_latency "eth_getBlockByNumber" http://localhost:8545 50 "$RESULT_DIR/latency-old-getBlock.log"

# 6d: Memory/Disk snapshot (old)
log "6d: Memory/Disk (v0.10.2)..."
docker stats --no-stream --format "{{.Name}}\t{{.MemUsage}}\t{{.CPUPerc}}" | grep gmet-s36 > "$RESULT_DIR/memory-old.log" 2>/dev/null || true
for container in gmet-s36-node1 gmet-s36-node2 gmet-s36-node3 gmet-s36-node4; do
  docker exec "$container" du -sh /data/geth/gmet/chaindata 2>/dev/null >> "$RESULT_DIR/disk-old.log" || true
done

# 6f: Max TPS (v0.10.2) - concurrent flood (200 / 500 / 1000 txs)
log "6f: Max TPS benchmark (v0.10.2)..."
log "  Sending 200 txs concurrently..."
OLD_MAX_TPS_200=$(measure_max_tps http://localhost:8545 200)
log "  v0.10.2 max TPS (200 txs): $OLD_MAX_TPS_200"
log "  Sending 500 txs concurrently..."
OLD_MAX_TPS_500=$(measure_max_tps http://localhost:8545 500)
log "  v0.10.2 max TPS (500 txs): $OLD_MAX_TPS_500"
log "  Sending 1000 txs concurrently..."
OLD_MAX_TPS_1000=$(measure_max_tps http://localhost:8545 1000)
log "  v0.10.2 max TPS (1000 txs): $OLD_MAX_TPS_1000"

cat > "$RESULT_DIR/bench-old-v0.10.2.json" <<EOF
{
  "version": "0.10.2",
  "blockSpeedMs": "$OLD_BLOCK_SPEED",
  "tps100": "$OLD_TPS",
  "maxTps200": "$OLD_MAX_TPS_200",
  "maxTps500": "$OLD_MAX_TPS_500",
  "maxTps1000": "$OLD_MAX_TPS_1000",
  "timestamp": "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
}
EOF

# ===================================================================
# 6a + 6b + 6c: Benchmark v1.0.0 pre-fork (CamelliaBlock=200)
# ===================================================================
log "--- 6a/6b/6c: Benchmarking v1.0.0 pre-fork ---"
"$SCRIPT_DIR/stop.sh" --clean
"$SCRIPT_DIR/setup.sh"  # re-create data dirs, password files, keystores

export NODE1_IMAGE=gmet-s36-leveldb:latest
export NODE2_IMAGE=gmet-s36-rocksdb:latest
export NODE3_IMAGE=gmet-s36-leveldb:latest
export NODE4_IMAGE=gmet-s36-rocksdb:latest
export NODE2_USEROCKSDB=1
export NODE4_USEROCKSDB=1

start_network "$SCRIPT_DIR/genesis-prefork.json"  # CamelliaBlock=200
wait_block 10 http://localhost:8545 120

log "6a: Block production speed (v1.0.0 pre-fork, 100 blocks)..."
NEW_PRE_BLOCK_SPEED=$(measure_block_speed http://localhost:8545 100 300)
log "  v1.0.0 pre-fork 100-block time: ${NEW_PRE_BLOCK_SPEED}ms"

log "6b: TPS (v1.0.0 pre-fork, 100 txs)..."
NEW_PRE_TPS=$(measure_tps http://localhost:8545 100 "0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266")
log "  v1.0.0 pre-fork TPS result: $NEW_PRE_TPS"

log "6c: RPC latency (v1.0.0 pre-fork)..."
bench_rpc_latency "eth_blockNumber" http://localhost:8545 100 "$RESULT_DIR/latency-new-prefork-blockNumber.log"
bench_rpc_latency "eth_getBlockByNumber" http://localhost:8545 50 "$RESULT_DIR/latency-new-prefork-getBlock.log"

cat > "$RESULT_DIR/bench-new-v1.0.0-prefork.json" <<EOF
{
  "version": "1.0.0",
  "phase": "pre-fork",
  "blockSpeedMs": "$NEW_PRE_BLOCK_SPEED",
  "tps100": "$NEW_PRE_TPS",
  "timestamp": "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
}
EOF

# ===================================================================
# 6a + 6b + 6c: Benchmark v1.0.0 post-fork (CamelliaBlock=100)
# ===================================================================
log "--- 6a/6b/6c: Benchmarking v1.0.0 post-fork ---"
"$SCRIPT_DIR/stop.sh" --clean
"$SCRIPT_DIR/setup.sh"  # re-create data dirs, password files, keystores

start_network "$SCRIPT_DIR/genesis.json"  # CamelliaBlock=100

# Wait until after fork
wait_block 110 http://localhost:8545 300

log "6a: Block production speed (v1.0.0 post-fork, 100 blocks)..."
NEW_POST_BLOCK_SPEED=$(measure_block_speed http://localhost:8545 100 300)
log "  v1.0.0 post-fork 100-block time: ${NEW_POST_BLOCK_SPEED}ms"

log "6b: TPS (v1.0.0 post-fork, 100 txs)..."
NEW_POST_TPS=$(measure_tps http://localhost:8545 100 "0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266")
log "  v1.0.0 post-fork TPS result: $NEW_POST_TPS"

log "6c: RPC latency (v1.0.0 post-fork)..."
bench_rpc_latency "eth_blockNumber" http://localhost:8545 100 "$RESULT_DIR/latency-new-postfork-blockNumber.log"
bench_rpc_latency "eth_getBlockByNumber" http://localhost:8545 50 "$RESULT_DIR/latency-new-postfork-getBlock.log"

# 6f: Max TPS (v1.0.0 post-fork) - concurrent flood (200 / 500 / 1000 txs)
log "6f: Max TPS benchmark (v1.0.0 post-fork)..."
log "  Sending 200 txs concurrently..."
NEW_MAX_TPS_200=$(measure_max_tps http://localhost:8545 200)
log "  v1.0.0 post-fork max TPS (200 txs): $NEW_MAX_TPS_200"
log "  Sending 500 txs concurrently..."
NEW_MAX_TPS_500=$(measure_max_tps http://localhost:8545 500)
log "  v1.0.0 post-fork max TPS (500 txs): $NEW_MAX_TPS_500"
log "  Sending 1000 txs concurrently..."
NEW_MAX_TPS_1000=$(measure_max_tps http://localhost:8545 1000)
log "  v1.0.0 post-fork max TPS (1000 txs): $NEW_MAX_TPS_1000"

# 6e: Blob tx overhead (post-fork only - new blob tx support)
log "6e: Blob tx overhead (v1.0.0 post-fork)..."
# Blob txs require EIP-4844 type 3 transactions. Check if network supports them.
BLOB_SUPPORT=$(rpc '{"jsonrpc":"2.0","method":"eth_getBlockByNumber","params":["latest",false],"id":1}' http://localhost:8545 | \
  python3 -c "import sys,json; d=json.load(sys.stdin); b=d.get('result',{}); print('yes' if 'blobGasUsed' in b else 'no')" 2>/dev/null || echo "no")
log "  Blob field in block: $BLOB_SUPPORT"
echo "blob_support=$BLOB_SUPPORT" >> "$RESULT_DIR/blob-overhead.log"

cat > "$RESULT_DIR/bench-new-v1.0.0-postfork.json" <<EOF
{
  "version": "1.0.0",
  "phase": "post-fork",
  "blockSpeedMs": "$NEW_POST_BLOCK_SPEED",
  "tps100": "$NEW_POST_TPS",
  "maxTps200": "$NEW_MAX_TPS_200",
  "maxTps500": "$NEW_MAX_TPS_500",
  "maxTps1000": "$NEW_MAX_TPS_1000",
  "blobSupport": "$BLOB_SUPPORT",
  "timestamp": "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
}
EOF

# ===================================================================
# Generate comparison report
# ===================================================================
log "Generating comparison report..."
python3 - <<PYEOF > "$RESULT_DIR/comparison.md"
import json, re

def load(f):
    try:
        return json.load(open(f))
    except:
        return {}

old = load("$RESULT_DIR/bench-old-v0.10.2.json")
pre = load("$RESULT_DIR/bench-new-v1.0.0-prefork.json")
post = load("$RESULT_DIR/bench-new-v1.0.0-postfork.json")

def ms(v):
    try:
        return int(str(v).replace('ms','').strip())
    except:
        return None

def extract_tps(v):
    try:
        m = re.search(r'tps=(\S+)', str(v))
        return float(m.group(1)) if m else None
    except:
        return None

old_ms = ms(old.get('blockSpeedMs',''))
pre_ms = ms(pre.get('blockSpeedMs',''))
post_ms = ms(post.get('blockSpeedMs',''))

old_tps     = extract_tps(old.get('tps100',''))
pre_tps     = extract_tps(pre.get('tps100',''))
post_tps    = extract_tps(post.get('tps100',''))
old_max200   = extract_tps(old.get('maxTps200',''))
post_max200  = extract_tps(post.get('maxTps200',''))
old_max500   = extract_tps(old.get('maxTps500',''))
post_max500  = extract_tps(post.get('maxTps500',''))
old_max1000  = extract_tps(old.get('maxTps1000',''))
post_max1000 = extract_tps(post.get('maxTps1000',''))

print("# Scenario 6: Performance Benchmark Comparison")
print()
print("## Block Production Speed (100 blocks)")
print()
print("| Version | Time (ms) | Blocks/min |")
print("|---------|-----------|------------|")
for label, elapsed in [("v0.10.2", old_ms), ("v1.0.0 pre-fork", pre_ms), ("v1.0.0 post-fork", post_ms)]:
    if elapsed:
        bpm = f"{100*60000/elapsed:.1f}"
    else:
        elapsed = "N/A"; bpm = "N/A"
    print(f"| {label} | {elapsed} | {bpm} |")
print()
print("## Transaction Throughput (sequential 100 txs)")
print()
print("| Version | TPS |")
print("|---------|-----|")
for label, tps in [("v0.10.2", old_tps), ("v1.0.0 pre-fork", pre_tps), ("v1.0.0 post-fork", post_tps)]:
    print(f"| {label} | {tps if tps else 'N/A'} |")
print()
print("## Peak TPS (concurrent flood, 3 nodes × 3 accounts)")
print()
print("| Version | 200 tx burst | 500 tx burst | 1000 tx burst |")
print("|---------|-------------|-------------|--------------|")
def fmt(v): return f"{v:.2f}" if v else "N/A"
print(f"| v0.10.2      | {fmt(old_max200)} | {fmt(old_max500)} | {fmt(old_max1000)} |")
print(f"| v1.0.0 post  | {fmt(post_max200)} | {fmt(post_max500)} | {fmt(post_max1000)} |")
if old_max1000 and post_max1000:
    delta = (post_max1000 - old_max1000) / old_max1000 * 100
    print(f"| delta (1000tx) | — | — | {delta:+.1f}% |")
print()
print("## Observations")
if old_ms and post_ms:
    delta_pct = (post_ms - old_ms) / old_ms * 100
    print(f"- Block speed delta (v0.10.2 → v1.0.0 post-fork): {delta_pct:+.1f}%")
if old_tps and post_tps:
    delta_pct = (post_tps - old_tps) / old_tps * 100
    print(f"- Sequential TPS delta: {delta_pct:+.1f}%")
if old_max1000 and post_max1000:
    delta_pct = (post_max1000 - old_max1000) / old_max1000 * 100
    print(f"- Peak TPS delta (1000 tx burst): {delta_pct:+.1f}%")
PYEOF

log "Comparison report: $RESULT_DIR/comparison.md"
cat "$RESULT_DIR/comparison.md"

# Summary
write_summary "$RESULT_DIR" $PASS $FAIL "Performance benchmarks complete"

"$SCRIPT_DIR/stop.sh" --clean
log "=== Scenario 6 complete: PASS=$PASS FAIL=$FAIL ==="
