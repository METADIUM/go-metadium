#!/usr/bin/env bash
# governance.sh - validator set changes through governance ballots, on a
# 5-node network past bftBlock (§11.2 S-08, S-15):
#   S-08  remove node5 (5 -> 4): from the next block the quorum is the new
#         set's and node5 no longer proposes; add it back (4 -> 5): it seals
#         and proposes again, without a restart
#   S-15  try to remove node4 at 4: the deciding vote never commits (the
#         floor, design §9.3.1), the chain continues and N stays 4
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"
source ./lib.sh

(( NODES == 5 )) || err "governance.sh needs NODES=5 (it goes 5 -> 4, tries 3, and back to 5)"
P1=$(port_of 1)
FAIL=0
pass() { log "PASS  $*"; }
fail() { log "FAIL  $*"; FAIL=1; }
govn() { ./gov.py nodes | sed -n 's/^nodes \([0-9]*\),.*/\1/p'; }
blk() { rpc "$P1" eth_getBlockByNumber "[\"$(printf '0x%x' "$1")\", false]"; }
seals() { blk "$1" | python3 -c "import sys,json; print(len(json.load(sys.stdin).get('commitSeals') or []))"; }
builder() { blk "$1" | python3 -c "import sys,json; print((json.load(sys.stdin).get('minerNodeId') or '')[2:])"; }
node_id() { awk -v n="node$1" '$1==n{print $2}' node-ids.txt; }

# check_set N QUORUM IN_OR_OUT: over the next 2N blocks, every block has the
# set's quorum of seals and node5 proposes (in) or does not (out)
check_set() {
  local from to seen5=0 n
  from=$(( $(block_number "$P1") + 1 ))
  to=$(( from + 2 * $1 - 1 ))
  for _ in $(seq 1 120); do (( $(block_number "$P1") >= to )) && break; sleep 2; done
  for n in $(seq "$from" "$to"); do
    (( $(seals "$n") >= $2 )) || fail "block $n has $(seals "$n") seals, the $1-set quorum is $2"
    [[ $(builder "$n") == "$(node_id 5)" ]] && seen5=1
  done
  if [[ $3 == in ]]; then
    (( seen5 == 1 )) && pass "blocks $from..$to: quorum $2 of $1, node5 proposing" || fail "node5 did not propose in $from..$to"
  else
    (( seen5 == 0 )) && pass "blocks $from..$to: quorum $2 of $1, node5 not proposing" || fail "node5 proposed after its removal"
  fi
}

log "=== Governance: validator set changes (N=$NODES) ==="
[[ $(govn) == 5 ]] || err "expected 5 governance nodes, have $(govn)"

# S-08: remove node5.
./gov.py remove 5 | sed 's/^/  /'
[[ $(govn) == 4 ]] && pass "node5 removed by ballot: 4 governance nodes" || fail "removal did not pass: $(govn) nodes"
check_set 4 3 out

# S-15: removing node4 would leave 3.
out=$(timeout 300 ./gov.py remove 4 || true)
sed 's/^/  /' <<<"$out"
grep -q "never committed" <<<"$out" && pass "the vote that would leave 3 nodes never committed" || fail "the deciding vote committed"
[[ $(govn) == 4 ]] && pass "still 4 governance nodes" || fail "$(govn) governance nodes"
h=$(block_number "$P1"); sleep 20
(( $(block_number "$P1") > h )) && pass "the chain continues" || fail "the chain stopped"
# Only a validator whose turn to propose came while the vote was pending
# builds with it, and logs leaving it out.
logged=0
for n in $(seq 1 4); do
  docker logs "$(container "$n")" 2>&1 | grep -q "breaks the validator floor" && logged=$((logged + 1))
done
(( logged >= 1 )) && pass "$logged of 4 validators logged leaving the vote out (those that proposed meanwhile)" || fail "no validator logged the floor exclusion"

# The excluded vote is listed in metabft_status (with its sender and nonce)
# on the validators that left it out. It holds its sender's later
# transactions (nonce order) until a retry, every metabft.ExcludeFor (30 s),
# finds its ballot over: the vote then reverts and is included (P5-27).
listed=0
for n in $(seq 1 4); do
  c=$(rpc "$(port_of "$n")" metabft_status '[]' | python3 -c "import sys,json; print(len(json.load(sys.stdin).get('excludedTxs') or []))")
  (( c > 0 )) && listed=$((listed + 1))
done
(( listed >= 1 )) && pass "the excluded vote is listed in metabft_status on $listed of 4 validators" || fail "metabft_status lists no excluded transaction"
./gov.py finalize | sed 's/^/  /'
t0=$SECONDS
for _ in $(seq 1 120); do
  (( $(rpc "$P1" txpool_status '[]' | python3 -c "import sys,json; print(int(json.load(sys.stdin)['pending'],16))") == 0 )) && break
  sleep 2
done
(( SECONDS - t0 <= 150 )) && pass "the excluded vote cleared $((SECONDS - t0)) s after its ballot ended" || fail "the excluded vote took $((SECONDS - t0)) s to clear"

# S-08: add node5 back.
./gov.py add 5 | sed 's/^/  /'
[[ $(govn) == 5 ]] && pass "node5 added back by ballot: 5 governance nodes" || fail "the addition did not pass: $(govn) nodes"
check_set 5 4 in

status
(( FAIL == 0 )) && log "=== ALL PASSED ===" || { log "=== FAILURES ==="; exit 1; }
