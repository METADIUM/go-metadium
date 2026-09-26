#!/usr/bin/env bash
# transition.sh - rehearse the PoA -> PBFT switch (checklist R-01, R-02).
# It builds its own networks, so it runs from a stopped state:
#   R-02  governance with only 3 members, fewer than the 4 PBFT needs: the
#         readiness RPC reports it, and at bftBlock-1 the chain stops rather
#         than continue on PoA, with the reason in the logs (design §9.3).
#         Recovery is a new genesis, which is the R-01 run.
#   R-01  a new genesis, then design §9.2 step by step: genesis fields,
#         bootstrap, governance with every node, full mesh, readiness on every
#         node, and the switch itself (first proposer, seals, agreement).
# Options: NODES=7 (4..9), R02_BFT=80, R01_BFT=120
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

N=${NODES:-7}
R02_BFT=${R02_BFT:-80}
R01_BFT=${R01_BFT:-120}
FAIL=0
log()  { echo "[$(date '+%H:%M:%S')] $*"; }
pass() { log "PASS  $*"; }
fail() { log "FAIL  $*"; FAIL=1; }
build() { # BFT_BLOCK [deploy options]: a fresh network up to governance
  ./stop.sh --clean >/dev/null 2>&1 || true
  NODES=$N BFT_BLOCK=$1 CAMELLIA_BLOCK=$(( $1 / 2 )) ./setup.sh >/dev/null
  ./start.sh >/dev/null
  shift
  env "$@" ./deploy.sh >/dev/null
  source ./lib.sh
}
readiness() { rpc "$(port_of "$1")" metabft_readiness '[]'; }
field() { python3 -c "import sys,json; r=json.load(sys.stdin); print($1)"; }
wait_height() { # PORT HEIGHT TIMEOUT_S
  local deadline=$((SECONDS + $3))
  while (( SECONDS < deadline )); do
    (( $(block_number "$1" 2>/dev/null || echo 0) >= $2 )) && return 0
    sleep 2
  done
  return 1
}

log "=== R-02: switch with 3 governance members (N=$N nodes, bftBlock $R02_BFT) ==="
build "$R02_BFT" MEMBERS=3
not_ready=0
for n in $(seq 1 "$N"); do
  r=$(readiness "$n")
  [[ $(field 'r["ready"]' <<<"$r") == False && $(field '" ".join(r.get("problems") or [])' <<<"$r") == *"fewer governance nodes"* ]] &&
    not_ready=$((not_ready + 1))
done
(( not_ready == N )) && pass "R-02: readiness on all $N nodes reports too few governance nodes before the switch" ||
  fail "R-02: $not_ready of $N nodes report too few governance nodes"
wait_height "$(port_of 1)" $((R02_BFT - 1)) 900 || fail "R-02: never reached bftBlock-1"
sleep 60
heads=$(for n in $(seq 1 "$N"); do block_number "$(port_of "$n")"; done | sort -u | tr '\n' ' ')
[[ $heads == "$((R02_BFT - 1)) " ]] && pass "R-02: every node stops at bftBlock-1 ($((R02_BFT - 1))) for 60 s instead of continuing on PoA" ||
  fail "R-02: heads after 60 s: $heads (want $((R02_BFT - 1)) everywhere)"
msg=$(docker logs "$(container 1)" 2>&1 | grep "No validator set; not participating" | tail -1 || true)
[[ $msg == *"PBFT needs 4"* ]] && pass "R-02: the reason is logged: ${msg:0:220}" || fail "R-02: no clear error in node1's log (${msg:0:200})"
mkdir -p logs && docker logs "$(container 1)" >logs/transition-r02-node1.log 2>&1 || true

log "=== R-01: new genesis, then design §9.2 step by step (bftBlock $R01_BFT) ==="
# Step 0: the genesis. Steps 1-2: bootstrap and governance with every node.
build "$R01_BFT"
python3 - "$R01_BFT" <<'PY' && pass "R-01 step 0: the genesis fixes chainId, bftBlock and bft.*" || fail "R-01 step 0: genesis fields"
import json, sys
c = json.load(open("genesis.json"))["config"]
assert c["chainId"] and c["bftBlock"] == int(sys.argv[1]) and set(c["bft"]) >= {"emptyBlockInterval", "baseTimeout", "maxBackoffExp", "timeDrift"}
PY
mesh_ok=0
for n in $(seq 1 "$N"); do
  (( $(rpc "$(port_of "$n")" net_peerCount '[]' | python3 -c "import sys,json; print(int(json.load(sys.stdin),16))") >= N - 1 )) && mesh_ok=$((mesh_ok + 1))
done
(( mesh_ok == N )) && pass "R-01 step 2: full mesh, every node has $((N - 1)) peers" || fail "R-01 step 2: $mesh_ok of $N nodes fully meshed"
log "  step 2 NTP: the containers share the host clock; on servers, confirm NTP sync separately"
# Step 3: readiness on every node.
ready=0
for n in $(seq 1 "$N"); do
  r=$(readiness "$n")
  [[ $(field 'r["ready"] and r["validators"] == '"$N"' and r["inSet"] and r["advertises"] and r["blocksLeft"] > 0' <<<"$r") == True ]] && ready=$((ready + 1))
done
(( ready == N )) && pass "R-01 step 3: metabft_readiness is ready on all $N nodes ($N validators, each in the set and advertising metabft/1)" ||
  fail "R-01 step 3: $ready of $N nodes ready"
# Step 4: the switch.
wait_height "$(port_of 1)" $((R01_BFT + 5)) 900 || fail "R-01 step 4: no progress past bftBlock"
blk() { rpc "$(port_of 1)" eth_getBlockByNumber "[\"$(printf '0x%x' "$1")\", false]"; }
pre=$(blk $((R01_BFT - 1)) | field 'len(r.get("commitSeals") or [])')
seals=$(blk "$R01_BFT" | field 'len(r.get("commitSeals") or [])')
round=$(blk "$R01_BFT" | field 'int(r.get("bftRound","0x0"),16)')
builder=$(blk "$R01_BFT" | field '(r.get("minerNodeId") or "")[2:]')
first=$(rpc "$(port_of 1)" metabft_getValidators "[\"$(printf '0x%x' "$R01_BFT")\"]" | field "r[$R01_BFT % len(r)]['nodeId'][2:]")
(( pre == 0 && seals >= QUORUM )) && pass "R-01 step 4: block $((R01_BFT - 1)) is PoA (no seals), block $R01_BFT has $seals seals (quorum $QUORUM)" ||
  fail "R-01 step 4: seals $pre at bftBlock-1, $seals at bftBlock"
if (( round == 0 )); then
  [[ $builder == "$first" ]] && pass "R-01 step 4: block $R01_BFT committed in round 0, built by validators[($R01_BFT + 0) % $N]" ||
    fail "R-01 step 4: round-0 block $R01_BFT built by ${builder:0:16}, not validators[$R01_BFT % $N] ${first:0:16}"
else
  log "  step 4: block $R01_BFT committed in round $round (round changes right after the switch are expected, §9.2)"
fi
h=$(( $(block_number "$(port_of 1)") - 2 )); ref=$(blk "$h" | field 'r["hash"]'); agree=1
for n in $(seq 2 "$N"); do
  [[ $(rpc "$(port_of "$n")" eth_getBlockByNumber "[\"$(printf '0x%x' "$h")\", false]" | field 'r["hash"]') == "$ref" ]] || agree=0
done
(( agree )) && pass "R-01 step 4: all $N nodes agree at block $h" || fail "R-01 step 4: nodes disagree at $h"
status
(( FAIL == 0 )) && log "=== ALL PASSED ===" || { log "=== FAILURES ==="; exit 1; }
