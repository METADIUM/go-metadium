#!/usr/bin/env bash
# scenario-6-multiserver.sh - Distributed Performance Benchmark
# Runs scenario-6 simultaneously on several identical hosts and collects the
# results into a combined comparison report.
#
# Prerequisites:
#   - SSH key access to every target host
#   - tests/server36/ deployed and setup.sh run on each host
#   - Binaries (gmet-leveldb, gmet-rocksdb, gmet-old-built) on each host
#
# Configuration (set in the environment, e.g. an untracked local file):
#   SSH_KEY   path to the SSH private key for the targets
#   SERVERS   space-separated user@host list (also sets the count)
#   SERVER_NAMES  optional space-separated short names, one per SERVERS entry
#
# Usage:
#   SSH_KEY=~/.ssh/id_bench \
#   SERVERS="user@host-a user@host-b user@host-c" \
#   ./scenario-6-multiserver.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
source "$SCRIPT_DIR/lib.sh"

SSH_KEY="${SSH_KEY:?set SSH_KEY to the private key for the benchmark hosts}"
read -r -a SERVERS <<< "${SERVERS:?set SERVERS to a space-separated user@host list}"
read -r -a SERVER_NAMES <<< "${SERVER_NAMES:-}"
if [ "${#SERVER_NAMES[@]}" -ne 0 ] && [ "${#SERVER_NAMES[@]}" -ne "${#SERVERS[@]}" ]; then
  log "WARNING: SERVER_NAMES has ${#SERVER_NAMES[@]} entries but SERVERS has ${#SERVERS[@]}; deriving names from hosts."
  SERVER_NAMES=()
fi
if [ "${#SERVER_NAMES[@]}" -ne "${#SERVERS[@]}" ]; then
  # Derive a short name from each host; suffix the index on collision so that
  # two accounts on one host (alice@h1 / bob@h1) don't clobber each other's
  # result dir, log, and report column.
  declare -A _seen=()
  SERVER_NAMES=()
  for i in "${!SERVERS[@]}"; do
    n="${SERVERS[$i]##*@}"
    [ -n "${_seen[$n]:-}" ] && n="${n}-${i}"
    _seen[$n]=1
    SERVER_NAMES+=("$n")
  done
fi

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
  local rc=0
  ssh -i "$SSH_KEY" -o ConnectTimeout=30 "$server" "
    cd ~/go-metadium/tests/server36
    RESULTS_DIR=$remote_results bash scenarios/scenario-6.sh 2>&1
  " > "$RESULT_DIR/$name-bench.log" 2>&1 || rc=$?
  if [ "$rc" -ne 0 ]; then
    log "[$name] Benchmark FAILED (rc=$rc); see $name-bench.log"
    return "$rc"
  fi
  log "[$name] Benchmark complete."

  # Fetch results (a fetch failure is not a benchmark failure)
  scp -i "$SSH_KEY" -r "$server:$remote_results/scenario-6-performance/" \
    "$RESULT_DIR/$name/" 2>/dev/null || log "[$name] WARNING: results fetch failed."
  log "[$name] Results fetched."
}

# Launch all benchmarks in parallel (one per configured host, any count)
PIDS=()
for i in "${!SERVERS[@]}"; do
  run_remote_bench "${SERVERS[$i]}" "${SERVER_NAMES[$i]}" &
  PIDS+=($!)
done

# Wait for all to complete, counting failures so the summary reflects reality
PASS=0; FAIL=0
for pid in "${PIDS[@]}"; do
  if wait "$pid"; then PASS=$((PASS + 1)); else FAIL=$((FAIL + 1)); fi
done

log "All server benchmarks complete. Generating combined report..."

# Generate combined comparison. REPORT_TS is computed here (not inside the
# quoted heredoc, where $(...) would be emitted literally) and read from the
# environment by the python block.
export REPORT_TS="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
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
print("- Cross-host variation may reflect hardware or OS/scheduling differences;")
print("  interpret against the actual specs of the configured hosts.")
print(f"- Generated: {os.environ.get('REPORT_TS', 'unknown')}")
PYEOF

log "Combined report: $RESULT_DIR/combined-comparison.md"
cat "$RESULT_DIR/combined-comparison.md"

write_summary "$RESULT_DIR" "$PASS" "$FAIL" "Multi-server benchmark: $PASS ok, $FAIL failed"
log "=== Scenario 6 Multi-Server complete ($PASS ok, $FAIL failed) ==="
[ "$FAIL" -eq 0 ]
