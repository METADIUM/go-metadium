#!/usr/bin/env bash
# pbft-test.sh - check the PoA -> PBFT switch and PBFT behaviour on the
# running network (checklist P5 check; §11.2 S-01, S-02, S-03).
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"
source ./lib.sh

BFT_BLOCK=$(python3 -c "import json; print(json.load(open('genesis.json'))['config']['bftBlock'])")
P1=$(port_of 1)
FAIL=0
pass() { log "PASS  $*"; }
fail() { log "FAIL  $*"; FAIL=1; }

# block PORT NUMBER -> JSON of the header fields we check
block() {
  rpc "$1" eth_getBlockByNumber "[\"$(printf '0x%x' "$2")\", false]" | python3 -c '
import sys, json
b = json.load(sys.stdin)
print(json.dumps({"hash": b["hash"], "round": int(b.get("bftRound", "0x0"), 16),
                  "seals": len(b.get("commitSeals") or []), "miner": (b.get("minerNodeId") or "")[:18]}))'
}
field() { python3 -c "import sys,json; print(json.load(sys.stdin)['$1'])"; }

wait_height() { # PORT HEIGHT TIMEOUT_S
  local deadline=$((SECONDS + $3))
  while (( SECONDS < deadline )); do
    (( $(block_number "$1" 2>/dev/null || echo 0) >= $2 )) && return 0
    sleep 2
  done
  return 1
}

agree() { # LABEL: every node has node1's block two below its head
  local h ref port
  h=$(( $(block_number "$P1") - 2 ))
  ref=$(block "$P1" "$h" | field hash)
  for n in $(seq 2 "$NODES"); do
    port=$(port_of "$n")
    wait_height "$port" "$h" 180 || true
    [[ $(block "$port" "$h" | field hash) == "$ref" ]] || { fail "node$n disagrees at $h $1"; return; }
  done
  pass "all $NODES nodes agree at block $h $1"
}

stop_nodes() { local c=(); for n in "$@"; do c+=("$(container "$n")"); done; docker stop --time 60 "${c[@]}" >/dev/null; }
start_nodes() {
  local c=(); for n in "$@"; do c+=("$(container "$n")"); done
  docker start "${c[@]}" >/dev/null
  sleep 5
  mesh $(seq 1 "$NODES")
}

WINDOW=$(( 3 * NODES > 20 ? 3 * NODES : 20 ))
log "=== PBFT checks: N=$NODES f=$F quorum=$QUORUM, bftBlock $BFT_BLOCK ==="
wait_height "$P1" $((BFT_BLOCK + WINDOW)) 1200 || { fail "no progress past bftBlock + $WINDOW"; exit 1; }
pass "chain passed bftBlock + $WINDOW"

# The switch: PoA below, sealed PBFT from bftBlock on, every validator proposing.
[[ $(block "$P1" $((BFT_BLOCK - 1)) | field seals) == 0 ]] && pass "block $((BFT_BLOCK - 1)) is PoA (no seals)" || fail "block $((BFT_BLOCK - 1)) carries seals"
miners=""
for n in $(seq "$BFT_BLOCK" $((BFT_BLOCK + WINDOW - 1))); do
  b=$(block "$P1" "$n")
  (( $(field seals <<<"$b") >= QUORUM )) || fail "block $n has $(field seals <<<"$b") commit seals, quorum is $QUORUM"
  miners+="$(field miner <<<"$b")"$'\n'
done
pass "blocks $BFT_BLOCK..$((BFT_BLOCK + WINDOW - 1)) each carry >= $QUORUM commit seals"
distinct=$(sort -u <<<"$miners" | grep -c . || true)
(( distinct == NODES )) && pass "all $NODES validators proposed in $WINDOW blocks" || fail "only $distinct distinct proposers in $WINDOW blocks"
agree ""
fin=$(rpc "$P1" eth_getBlockByNumber '["finalized", false]' | python3 -c "import sys,json; print(int(json.load(sys.stdin)['number'],16))")
head=$(block_number "$P1")
(( head - fin <= 1 )) && pass "finalized block $fin is the head ($head)" || fail "finalized $fin lags head $head"

# S-01 / S-02: up to f validators down, production continues.
for k in $(seq 1 "$F"); do
  down=$(seq $((NODES - k + 1)) "$NODES")
  stop_nodes $down
  start=$(block_number "$P1")
  wait_height "$P1" $((start + 2 * NODES)) $((60 + 30 * NODES)) && pass "$((2 * NODES)) blocks with $k of $NODES stopped" || fail "no progress with $k stopped"
  start_nodes $down
  target=$(( $(block_number "$P1") + 3 ))
  for n in $down; do
    wait_height "$(port_of "$n")" "$target" 240 && pass "node$n caught up after restart" || fail "node$n did not catch up"
  done
done

# S-03: f+1 down, production stops; the quorum back, it resumes, no fork.
down=$(seq $((NODES - F)) "$NODES")
stop_nodes $down
sleep 5
stalled=$(block_number "$P1")
sleep 30
now=$(block_number "$P1")
(( now <= stalled + 1 )) && pass "no progress with $((F + 1)) of $NODES stopped ($stalled -> $now)" || fail "progress without a quorum ($stalled -> $now)"
start_nodes $down
wait_height "$P1" $((now + 10)) 400 && pass "production resumed after the quorum returned" || fail "no progress after restart"
agree "after recovery"

# A block committed after a round change (the fault windows force them)
# carries seals for its commit round and imports on every node. It is a new
# proposal of the later round, not a re-proposal of a prepared block, which
# S-16 needs and the simulator covers.
reproposed=""
for n in $(seq "$BFT_BLOCK" $(( $(block_number "$P1") - 1 ))); do
  b=$(block "$P1" "$n")
  if (( $(field round <<<"$b") > 0 )); then reproposed="$n $(field round <<<"$b")"; break; fi
done
if [[ -n "$reproposed" ]]; then
  pass "block ${reproposed% *} committed in round ${reproposed#* } imported on every node"
else
  fail "no block above round 0 during the fault windows"
fi

status
(( FAIL == 0 )) && log "=== ALL PASSED ===" || { log "=== FAILURES ==="; exit 1; }
