#!/usr/bin/env bash
# pbft-test.sh - check the PoA -> PBFT switch and PBFT behaviour on the
# running 4-node network (checklist P5 check, §11.2 S-01..S-03).
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"
source ./lib.sh

BFT_BLOCK=$(python3 -c "import json; print(json.load(open('genesis.json'))['config']['bftBlock'])")
FAIL=0
pass() { log "PASS  $*"; }
fail() { log "FAIL  $*"; FAIL=1; }

# block PORT NUMBER -> JSON of the header fields we check
block() {
  rpc "$1" eth_getBlockByNumber "[\"$(printf '0x%x' "$2")\", false]" | python3 -c '
import sys, json
b = json.load(sys.stdin)
print(json.dumps({"hash": b["hash"], "round": int(b.get("bftRound", "0x0"), 16),
                  "seals": len(b.get("commitSeals") or []), "miner": (b.get("minerNodeId") or "")[:18],
                  "coinbase": b["miner"], "time": int(b["timestamp"], 16)}))'
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

log "=== PBFT checks (bftBlock $BFT_BLOCK) ==="
wait_height 8645 $((BFT_BLOCK + 20)) 900 || { fail "no progress past bftBlock + 20"; exit 1; }
pass "chain passed bftBlock + 20"

# 1. The switch: PoA below, sealed PBFT from bftBlock on.
pre=$(block 8645 $((BFT_BLOCK - 1)))
[[ $(field seals <<<"$pre") == 0 ]] && pass "block $((BFT_BLOCK - 1)) is PoA (no seals)" || fail "block $((BFT_BLOCK - 1)) carries seals"
miners=""
for n in $(seq "$BFT_BLOCK" $((BFT_BLOCK + 19))); do
  b=$(block 8645 "$n")
  seals=$(field seals <<<"$b")
  (( seals >= 3 )) || fail "block $n has $seals commit seals, quorum is 3"
  miners+="$(field miner <<<"$b")"$'\n'
done
pass "blocks $BFT_BLOCK..$((BFT_BLOCK + 19)) each carry >= 3 commit seals"
distinct=$(sort -u <<<"$miners" | grep -c . || true)
(( distinct == 4 )) && pass "all 4 validators proposed in those 20 blocks" || fail "only $distinct distinct proposers in 20 blocks"

# 2. Agreement and finality.
h=$(( $(block_number 8645) - 2 ))
ref=$(block 8645 "$h" | field hash)
for port in 8646 8647 8648; do
  [[ $(block "$port" "$h" | field hash) == "$ref" ]] || fail "node :$port disagrees at $h"
done
pass "all nodes agree at block $h"
fin=$(rpc 8645 eth_getBlockByNumber '["finalized", false]' | python3 -c "import sys,json; print(int(json.load(sys.stdin)['number'],16))")
head=$(block_number 8645)
(( head - fin <= 1 )) && pass "finalized block $fin is the head ($head)" || fail "finalized $fin lags head $head"

# 3. S-01: one validator down, production continues with 3 seals.
docker stop --time 60 gmet-pbft-node4 >/dev/null
start=$(block_number 8645)
wait_height 8645 $((start + 10)) 120 && pass "10 blocks with node4 stopped" || fail "no progress with node4 stopped"
docker start gmet-pbft-node4 >/dev/null
sleep 5
rpc 8648 admin_addPeer "[\"$(enode_of 1)\"]" >/dev/null 2>&1 || true
target=$(( $(block_number 8645) + 5 ))
wait_height 8648 "$target" 180 && pass "node4 caught up after restart" || fail "node4 did not catch up"

# 4. S-03: two down (> f), production stops; restarting resumes it.
docker stop --time 60 gmet-pbft-node3 gmet-pbft-node4 >/dev/null
sleep 5
stalled=$(block_number 8645)
sleep 30
now=$(block_number 8645)
(( now <= stalled + 1 )) && pass "no progress with 2 of 4 stopped ($stalled -> $now)" || fail "progress without a quorum ($stalled -> $now)"
docker start gmet-pbft-node3 gmet-pbft-node4 >/dev/null
sleep 5
for n in 3 4; do
  for m in 1 2; do rpc "${PORTS[$((n - 1))]}" admin_addPeer "[\"$(enode_of $m)\"]" >/dev/null 2>&1 || true; done
done
wait_height 8645 $((now + 10)) 300 && pass "production resumed after the quorum returned" || fail "no progress after restart"
h=$(( $(block_number 8645) - 2 ))
ref=$(block 8645 "$h" | field hash)
for port in 8646 8647 8648; do
  wait_height "$port" "$h" 120 || true
  [[ $(block "$port" "$h" | field hash) == "$ref" ]] || fail "node :$port disagrees at $h after recovery"
done
pass "all nodes agree at block $h after recovery"

status
(( FAIL == 0 )) && log "=== ALL PASSED ===" || { log "=== FAILURES ==="; exit 1; }
