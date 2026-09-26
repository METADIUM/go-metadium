#!/usr/bin/env bash
# byzantine.sh - a misbehaving validator on the running network (§11.2
# S-04, S-05, S-07, S-14, S-16). Needs the pbftfault build:
#   go build -tags pbftfault -o build/bin/gmet-fault ./cmd/geth
#   GMET_BIN=../../build/bin/gmet-fault ./setup.sh ...
# The fault is switched on per node through METABFT_FAULT (see
# eth/bft_fault.go); every node is put back to normal at the end.
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"
source ./lib.sh

P1=$(port_of 1)
BAD=2   # the misbehaving validator
FAIL=0
pass() { log "PASS  $*"; }
fail() { log "FAIL  $*"; FAIL=1; }
blk() { rpc "$P1" eth_getBlockByNumber "[\"$(printf '0x%x' "$1")\", false]"; }
get() { python3 -c "import sys,json; b=json.load(sys.stdin); print($1)"; }
node_id() { awk -v n="node$1" '$1==n{print $2}' node-ids.txt; }
wait_height() {
  local deadline=$((SECONDS + $3))
  while (( SECONDS < deadline )); do
    (( $(block_number "$1" 2>/dev/null || echo 0) >= $2 )) && return 0
    sleep 2
  done
  return 1
}
agree() {
  local h ref
  h=$(( $(block_number "$P1") - 2 ))
  ref=$(blk "$h" | get 'b["hash"]')
  for n in $(seq 2 "$NODES"); do
    wait_height "$(port_of "$n")" "$h" 180 || true
    [[ $(rpc "$(port_of "$n")" eth_getBlockByNumber "[\"$(printf '0x%x' "$h")\", false]" | get 'b["hash"]') == "$ref" ]] ||
      { fail "node$n disagrees at $h $1"; return; }
  done
  pass "all $NODES nodes agree at block $h $1"
}
# save_logs: keep every node's log before a recreate drops it
STEP=0
save_logs() {
  STEP=$((STEP + 1))
  mkdir -p logs/byzantine
  for n in $(seq 1 "$NODES"); do
    docker logs "gmet-pbft-node$n" >"logs/byzantine/$STEP-node$n.log" 2>&1 || true
  done
}
# set_fault FAULT NODE...: recreate the nodes with METABFT_FAULT=FAULT
set_fault() {
  local f=$1; shift
  save_logs
  local envs=()
  for n in "$@"; do envs+=("FAULT_node$n=$f"); done
  env "${envs[@]}" docker compose up -d --force-recreate "${@/#/node}" >/dev/null 2>&1
  sleep 8
  mesh $(seq 1 "$NODES")
}
# during WINDOW: run 3*N heights and report the heights node BAD built
built_by_bad() {
  local from to n c=0
  from=$(( $(block_number "$P1") + 1 )); to=$(( from + 3 * NODES - 1 ))
  wait_height "$P1" "$to" $(( 60 + 20 * NODES )) || { fail "no progress with node$BAD misbehaving"; echo 0; return; }
  for n in $(seq "$from" "$to"); do
    [[ $(blk "$n" | get '(b.get("minerNodeId") or "")[2:]') == "$(node_id "$BAD")" ]] && c=$((c + 1))
  done
  echo "$c"
}
rejected_with() { # REASON: some honest node's last refused proposal names it
  local n r
  for n in $(seq 1 "$NODES"); do
    [[ $n == "$BAD" ]] && continue
    r=$(rpc "$(port_of "$n")" metabft_status '[]' | get '(b.get("lastRejection") or {}).get("reason","")')
    [[ $r == *"$1"* ]] && { echo "node$n: $r"; return 0; }
  done
  return 1
}

log "=== Byzantine validator: N=$NODES f=$F, node$BAD misbehaves ==="

# S-05 and S-14. A fresh proposal meets the local-clock bound before the
# header rules, so one stamped before its parent is refused by the bound:
# the parent is at least a block interval old, and the stamp older still.
# The header rule Time >= parent.Time guards import, where the bound does not
# apply; the engine tests cover it (TestEngineVerifiesPBFTHeader).
for fault in bad-rewards time-past time-future; do
  case $fault in
    bad-rewards) want="rewards field"; label="S-05 wrong rewards" ;;
    time-past) want="too far from the local clock"; label="S-14 timestamp before the parent" ;;
    time-future) want="too far from the local clock"; label="S-14 timestamp 10 s ahead" ;;
  esac
  set_fault "$fault" "$BAD"
  c=$(built_by_bad)
  (( c == 0 )) && pass "$label: chain continues, no block of node$BAD's committed in $((3 * NODES)) heights" || fail "$label: $c blocks by node$BAD committed"
  if r=$(rejected_with "$want"); then pass "$label: refused by the others (${r:0:160})"; else fail "$label: no node reports refusing it for \"$want\""; fi
  set_fault "" "$BAD"
  agree "after $label"
done

# S-07: node BAD's clock is 5 min ahead. Its proposals are stamped so and
# refused by the others' bound; it refuses theirs against its own clock, so
# it votes on nothing, one validator short of all, and follows the chain by
# import, where the bound does not apply.
set_fault clock-ahead "$BAD"
c=$(built_by_bad)
(( c == 0 )) && pass "S-07 clock 5 min ahead: chain continues, no block of node$BAD's committed in $((3 * NODES)) heights" || fail "S-07: $c blocks by node$BAD committed"
if r=$(rejected_with "too far from the local clock"); then pass "S-07: its proposals refused by the others (${r:0:160})"; else fail "S-07: no node reports refusing its proposals"; fi
r=$(rpc "$(port_of "$BAD")" metabft_status '[]' | get '(b.get("lastRejection") or {}).get("reason","")')
[[ $r == *"too far from the local clock"* ]] && pass "S-07: node$BAD refuses the others' proposals (${r:0:160})" || fail "S-07: node$BAD's last refusal: \"$r\""
lag=$(( $(block_number "$P1") - $(block_number "$(port_of "$BAD")") ))
(( lag <= 2 )) && pass "S-07: node$BAD follows the chain by import ($lag behind)" || fail "S-07: node$BAD is $lag blocks behind"
set_fault "" "$BAD"
agree "after S-07"

# S-04: node BAD equivocates on its rounds.
set_fault equivocate "$BAD"
c=$(built_by_bad)
(( c == 0 )) && pass "S-04 equivocation: no block of node$BAD's committed; the chain continues" || fail "S-04: $c equivocated blocks committed"
ev=0
for n in $(seq 1 "$NODES"); do
  k=$(rpc "$(port_of "$n")" metabft_getEvidence '[]' | get 'len(b or [])')
  (( k > 0 )) && ev=$((ev + 1))
done
(( ev > 0 )) && pass "S-04: equivocation evidence stored on $ev nodes" || fail "S-04: no evidence stored"
set_fault "" "$BAD"
agree "after the equivocation"

# S-16: every validator withholds its round-0 COMMIT at heights divisible
# by 10, so those heights are decided by re-proposing the prepared block.
set_fault withhold-commit $(seq 1 "$NODES")
start=$(block_number "$P1")
target=$(( (start / 10 + 2) * 10 ))
wait_height "$P1" $((target + 1)) 600 || fail "S-16: no progress to $target"
h=$target
round=$(blk "$h" | get 'int(b.get("bftRound","0x0"),16)')
builder=$(blk "$h" | get '(b.get("minerNodeId") or "")[2:]')
first=$(rpc "$P1" metabft_getValidators "[\"$(printf '0x%x' "$h")\"]" | get "b[$h % len(b)]['nodeId'][2:]")
(( round >= 1 )) && pass "S-16: height $h committed in round $round" || fail "S-16: height $h committed in round $round"
[[ $builder == "$first" ]] && pass "S-16: its builder is round 0's proposer (the prepared block, re-proposed unchanged)" || fail "S-16: builder ${builder:0:16} is not round 0's proposer ${first:0:16}"
set_fault "" $(seq 1 "$NODES")
agree "after the re-proposals"

status
(( FAIL == 0 )) && log "=== ALL PASSED ===" || { log "=== FAILURES ==="; exit 1; }
