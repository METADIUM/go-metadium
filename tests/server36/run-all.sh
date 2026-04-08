#!/usr/bin/env bash
# run-all.sh - Run all 7 server36 test scenarios
#
# Usage:
#   ./run-all.sh                  Run all scenarios (1-7)
#   ./run-all.sh --from 3         Resume from scenario 3
#   ./run-all.sh --only 6         Run only scenario 6
#   ./run-all.sh --skip 7         Skip scenario 7 (long-term)
#   DURATION_HOURS=4 ./run-all.sh Short scenario 7 (4h instead of 72h)

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/lib.sh"

FROM_SCENARIO=1
ONLY_SCENARIO=""
SKIP_SCENARIOS=()

while [[ $# -gt 0 ]]; do
  case "$1" in
    --from)  FROM_SCENARIO="$2"; shift 2 ;;
    --only)  ONLY_SCENARIO="$2"; shift 2 ;;
    --skip)  SKIP_SCENARIOS+=("$2"); shift 2 ;;
    *) log "Unknown argument: $1"; exit 1 ;;
  esac
done

SUMMARY_FILE="$RESULTS_DIR/run-summary-$(date +%Y%m%d-%H%M%S).md"
mkdir -p "$RESULTS_DIR"

cat > "$SUMMARY_FILE" <<EOF
# Server36 Test Run Summary

- Started: $(date -u +%Y-%m-%dT%H:%M:%SZ)
- From scenario: $FROM_SCENARIO
- Only: ${ONLY_SCENARIO:-all}

| Scenario | Name | Status | Duration |
|----------|------|--------|----------|
EOF

run_scenario() {
  local n="$1"
  local name="$2"
  local script="$SCRIPT_DIR/scenarios/scenario-$n.sh"

  # Apply filters
  if [[ -n "$ONLY_SCENARIO" && "$n" != "$ONLY_SCENARIO" ]]; then
    return 0
  fi
  if [[ "$n" -lt "$FROM_SCENARIO" ]]; then
    return 0
  fi
  for skip in "${SKIP_SCENARIOS[@]}"; do
    if [[ "$n" == "$skip" ]]; then
      log "Skipping scenario $n ($name)"
      echo "| $n | $name | SKIPPED | - |" >> "$SUMMARY_FILE"
      return 0
    fi
  done

  [[ -x "$script" ]] || { log "Script not found: $script"; return 1; }

  log "====================================="
  log "STARTING SCENARIO $n: $name"
  log "====================================="

  local T_START
  T_START=$(date +%s)
  local STATUS="FAIL"
  local LOG_FILE="$RESULTS_DIR/scenario-$n-run.log"

  if bash "$script" 2>&1 | tee "$LOG_FILE"; then
    STATUS="PASS"
    PASS=$((PASS + 1))
  else
    FAIL=$((FAIL + 1))
    log "Scenario $n FAILED. See: $LOG_FILE"
    # Continue with next scenario (don't abort on failure)
  fi

  local DURATION=$(( $(date +%s) - T_START ))
  local DURATION_FMT
  DURATION_FMT=$(python3 -c "s=$DURATION; print(f'{s//3600}h{(s%3600)//60}m{s%60}s')")
  echo "| $n | $name | $STATUS | $DURATION_FMT |" >> "$SUMMARY_FILE"
  log "Scenario $n: $STATUS ($DURATION_FMT)"
}

PASS=0; FAIL=0

# Scenario 3→4 dependency: scenario 4 requires scenario 3 state
# If running from <= 3, run 3 then 4 together
SCENARIOS=(
  "1:Pre-fork Compatibility"
  "2:All-New Fork Transition"
  "3:Mixed Version at Fork"
  "4:Late Upgrade"
  "5:Governance Rewards"
  "6:Performance Benchmarks"
  "7:Long-term Stability (${DURATION_HOURS:-72}h)"
)

for entry in "${SCENARIOS[@]}"; do
  num="${entry%%:*}"
  name="${entry#*:}"
  run_scenario "$num" "$name"
done

cat >> "$SUMMARY_FILE" <<EOF

## Totals

- Pass: $PASS
- Fail: $FAIL
- Finished: $(date -u +%Y-%m-%dT%H:%M:%SZ)
EOF

log ""
log "====================================="
log "All scenarios complete: PASS=$PASS FAIL=$FAIL"
log "Summary: $SUMMARY_FILE"
log "====================================="
cat "$SUMMARY_FILE"

[[ $FAIL -eq 0 ]]
