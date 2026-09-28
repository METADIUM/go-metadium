#!/usr/bin/env bash
# availability.sh - how long the chain is down, and how long it takes to come
# back, when the quorum is lost (for the operations sign-off, checklist
# G-02; design §9.6). For each outage length it takes the quorum away, then
# restores it and times the first new block at node1:
#   stop   f+1 validators stopped (docker stop), then started
#   split  the validators split 4:3 with iptables (neither side has a
#          quorum), then healed
# Round timeouts back off while no quorum exists (baseTimeout·2^r, r capped
# at maxBackoffExp), so the recovery time depends on how long the outage
# lasted. Options: OUTAGES="10 60 180" (seconds), MODES="stop split"
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"
source ./lib.sh

OUTAGES=${OUTAGES:-"10 60 180"}
MODES=${MODES:-"stop split"}
P1=$(port_of 1)
FAIL=0
pass() { log "PASS  $*"; }
fail() { log "FAIL  $*"; FAIL=1; }
ip_of() { echo "172.32.0.1$1"; }
fw() { docker exec -u 0 "$(container "$1")" iptables "${@:2}"; }
now_ms() { date +%s%3N; }
round_of() { rpc "$(port_of "$1")" metabft_getRoundState '[]' | python3 -c "import sys,json; print(int(json.load(sys.stdin)['round'],16))"; }
# first_block_after HEIGHT TIMEOUT_S: ms until node1's head passes HEIGHT
first_block_after() {
  local t0 deadline
  t0=$(now_ms); deadline=$((SECONDS + $2))
  while (( SECONDS < deadline )); do
    (( $(block_number "$P1" 2>/dev/null || echo 0) > $1 )) && { echo $(( $(now_ms) - t0 )); return 0; }
    sleep 0.2
  done
  return 1
}
DOWN=$(seq $((NODES - F)) "$NODES")          # f+1 validators
SIDE_A=$(seq 1 $((NODES - F - 1)))           # a 4:3 split for N=7
SIDE_B=$(seq $((NODES - F)) "$NODES")

log "=== Availability: N=$NODES f=$F quorum=$QUORUM; outages $OUTAGES s; modes $MODES ==="
results=()
for mode in $MODES; do
  for d in $OUTAGES; do
    h0=$(block_number "$P1")
    case $mode in
      stop)
        for n in $DOWN; do docker stop --time 60 "$(container "$n")" >/dev/null; done ;;
      split)
        for a in $SIDE_A; do for b in $SIDE_B; do
          fw "$a" -A INPUT -s "$(ip_of "$b")" -j DROP; fw "$a" -A OUTPUT -d "$(ip_of "$b")" -j DROP
          fw "$b" -A INPUT -s "$(ip_of "$a")" -j DROP; fw "$b" -A OUTPUT -d "$(ip_of "$a")" -j DROP
        done; done ;;
    esac
    sleep "$d"
    stalled=$(block_number "$P1"); r=$(round_of 1)
    (( stalled <= h0 + 1 )) || fail "$mode $d s: the chain progressed without a quorum ($h0 -> $stalled)"
    case $mode in
      stop)  for n in $DOWN; do docker start "$(container "$n")" >/dev/null; done ;;
      split) for n in $(seq 1 "$NODES"); do fw "$n" -F; done ;;
    esac
    mesh $(seq 1 "$NODES")
    if ms=$(first_block_after "$stalled" 600); then
      log "  $mode, $d s without a quorum (round $r at node1): first block $ms ms after the quorum returned"
      results+=("| $mode | $d s | $r | $ms ms |")
    else
      fail "$mode $d s: no block within 600 s of the quorum returning"
    fi
    # let it settle before the next outage
    target=$(( $(block_number "$P1") + 3 )); first_block_after $((target - 1)) 120 >/dev/null || true
  done
done
log "mode | outage | round at node1 when restored | first block after restore"
for row in "${results[@]}"; do log "$row"; done
h=$(( $(block_number "$P1") - 2 )); ref=$(rpc "$P1" eth_getBlockByNumber "[\"$(printf '0x%x' "$h")\", false]" | python3 -c "import sys,json; print(json.load(sys.stdin)['hash'])")
for n in $(seq 2 "$NODES"); do
  [[ $(rpc "$(port_of "$n")" eth_getBlockByNumber "[\"$(printf '0x%x' "$h")\", false]" | python3 -c "import sys,json; print(json.load(sys.stdin)['hash'])") == "$ref" ]] || fail "node$n disagrees at $h"
done
pass "all $NODES nodes agree at block $h after every outage"
(( FAIL == 0 )) && log "=== ALL PASSED ===" || { log "=== FAILURES ==="; exit 1; }
