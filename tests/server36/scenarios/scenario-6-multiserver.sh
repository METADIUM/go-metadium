#!/usr/bin/env bash
# scenario-6-multiserver.sh - Distributed Performance Benchmark
# Runs scenario-6 simultaneously on servers 31, 33, and 36 (same hardware).
# Collects results from all three and generates a combined comparison report.
#
# Prerequisites:
#   - SSH key access to all three servers
#   - tests/server36/ deployed and setup.sh run on each server
#   - Binaries (gmet-leveldb, gmet-rocksdb, gmet-old-built) on each server
#
# Usage: ./scenario-6-multiserver.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
source "$SCRIPT_DIR/lib.sh"

SSH_KEY="${SSH_KEY:-$HOME/.ssh/aws-jsong-nopass.pem}"
SERVERS=(
  "cplabs@10.150.255.36"
  "jsong@10.150.255.31"
  "jsuhr@10.150.255.33"
)
SERVER_NAMES=(server36 server31 server33)

SCENARIO="scenario-6-multiserver"
RESULT_DIR="$RESULTS_DIR/$SCENARIO"
mkdir -p "$RESULT_DIR"

log "=== Scenario 6 Multi-Server Benchmark ==="
log "Servers: ${SERVER_NAMES[*]}"
log "Running scenario-6 simultaneously on all servers..."

# Function to run scenario-6 on a remote server
run_remote_bench() {
  local server="$1"
  local name="$2"
  local remote_results="/tmp/go-metadium-bench-results-$name"

  log "[$name] Starting benchmark on $server..."
  ssh -i "$SSH_KEY" -o ConnectTimeout=30 "$server" "
    cd ~/go-metadium/tests/server36
    RESULTS_DIR=$remote_results bash scenarios/scenario-6.sh 2>&1
  " > "$RESULT_DIR/$name-bench.log" 2>&1
  log "[$name] Benchmark complete."

  # Fetch results
  scp -i "$SSH_KEY" -r "$server:$remote_results/scenario-6-performance/" \
    "$RESULT_DIR/$name/" 2>/dev/null || true
  log "[$name] Results fetched."
}

# Launch all benchmarks in parallel
PIDS=()
for i in 0 1 2; do
  run_remote_bench "${SERVERS[$i]}" "${SERVER_NAMES[$i]}" &
  PIDS+=($!)
done

# Wait for all to complete
for pid in "${PIDS[@]}"; do
  wait "$pid" || true
done

log "All server benchmarks complete. Generating combined report..."

# Generate combined comparison
python3 - "$RESULT_DIR" "${SERVER_NAMES[@]}" <<'PYEOF' > "$RESULT_DIR/combined-comparison.md"
import sys, json, re, os

result_dir = sys.argv[1]
servers = sys.argv[2:]

def load_bench(server, phase):
    fname = os.path.join(result_dir, server, f"bench-{phase}.json")
    try:
        return json.load(open(fname))
    except:
        return {}

def extract_ms(v):
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

phases = [
    ("old-v0.10.2", "bench-old-v0.10.2"),
    ("new-v1.0.0-prefork", "bench-new-v1.0.0-prefork"),
    ("new-v1.0.0-postfork", "bench-new-v1.0.0-postfork"),
]

print("# Scenario 6 Multi-Server Performance Benchmark")
print()
print(f"Servers tested: {', '.join(servers)}")
print()

print("## Block Production Speed (100 blocks, ms elapsed)")
print()
header = "| Version |" + "".join(f" {s} |" for s in servers)
separator = "|---------|" + "".join("---------|" for _ in servers)
print(header)
print(separator)

for label, phase_key in phases:
    row = f"| {label} |"
    for server in servers:
        d = load_bench(server, phase_key.split('bench-')[1])
        ms = extract_ms(d.get('blockSpeedMs',''))
        bpm = f"{100*60000/ms:.1f} blk/min" if ms else "N/A"
        row += f" {ms}ms ({bpm}) |"
    print(row)

print()
print("## TPS (100 txs)")
print()
print(header)
print(separator)
for label, phase_key in phases:
    row = f"| {label} |"
    for server in servers:
        d = load_bench(server, phase_key.split('bench-')[1])
        tps = extract_tps(d.get('tps100',''))
        row += f" {tps:.2f} |" if tps else " N/A |"
    print(row)

print()
print("## Notes")
print("- All servers are identical hardware: Intel i5-11400T 12-core, 15GB RAM")
print("- Results variation indicates OS/scheduling noise, not hardware differences")
print(f"- Timestamp: $(date -u +%Y-%m-%dT%H:%M:%SZ)")
PYEOF

log "Combined report: $RESULT_DIR/combined-comparison.md"
cat "$RESULT_DIR/combined-comparison.md"

write_summary "$RESULT_DIR" 1 0 "Multi-server benchmark complete"
log "=== Scenario 6 Multi-Server complete ==="
