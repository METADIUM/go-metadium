#!/usr/bin/env bash
# faults.sh - fault injection on the running PBFT network (§11.2), after
# pbft-test.sh or at least past bftBlock:
#   S-06  partition: 3 validators cut off (4 | 1 | 1 | 1 with N=7), no side
#         has a quorum; healed, the chain resumes without a fork
#   S-11  kill -9 during consensus under load, repeatedly; no validator ever
#         signs two digests (no equivocation evidence anywhere)
#   S-12  WAL deleted: the node restarts as an observer, and takes part again
#         once a height commits without it
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"
source ./lib.sh

P1=$(port_of 1)
NET=$(docker inspect "$(container 1)" -f '{{range $k, $v := .NetworkSettings.Networks}}{{$k}}{{end}}')
FAIL=0
pass() { log "PASS  $*"; }
fail() { log "FAIL  $*"; FAIL=1; }
field() { python3 -c "import sys,json; v=json.load(sys.stdin); print(v['$1'] if v else '')"; }
wait_height() { # PORT HEIGHT TIMEOUT_S
  local deadline=$((SECONDS + $3))
  while (( SECONDS < deadline )); do
    (( $(block_number "$1" 2>/dev/null || echo 0) >= $2 )) && return 0
    sleep 2
  done
  return 1
}
hash_at() { rpc "$1" eth_getBlockByNumber "[\"$(printf '0x%x' "$2")\", false]" | field hash; }
agree() {
  local h ref
  h=$(( $(block_number "$P1") - 2 ))
  ref=$(hash_at "$P1" "$h")
  for n in $(seq 2 "$NODES"); do
    wait_height "$(port_of "$n")" "$h" 180 || true
    [[ $(hash_at "$(port_of "$n")" "$h") == "$ref" ]] || { fail "node$n disagrees at $h $1"; return; }
  done
  pass "all $NODES nodes agree at block $h $1"
}
no_evidence() {
  for n in $(seq 1 "$NODES"); do
    c=$(rpc "$(port_of "$n")" metabft_getEvidence '[]' | python3 -c "import sys,json; print(len(json.load(sys.stdin) or []))")
    (( c == 0 )) || { fail "node$n holds $c piece(s) of equivocation evidence $1"; return; }
  done
  pass "no equivocation evidence on any node $1"
}
load() { # send a value transfer every 0.3 s for $1 seconds, from node1
  local end=$((SECONDS + $1))
  while (( SECONDS < end )); do
    rpc "$P1" eth_sendTransaction "[{\"from\":\"$(account_of 1)\",\"to\":\"$(account_of 2)\",\"value\":\"0x1\"}]" >/dev/null 2>&1 || true
    sleep 0.3
  done
}

log "=== Fault injection: N=$NODES f=$F quorum=$QUORUM ==="

# S-06: cut off f+1 validators; the rest, N-(f+1) < quorum, cannot proceed either.
cut=$(seq $((NODES - F)) "$NODES")
for n in $cut; do docker network disconnect "$NET" "$(container "$n")"; done
sleep 5
stalled=$(block_number "$P1")
sleep 40
now=$(block_number "$P1")
(( now <= stalled + 1 )) && pass "partition: the $((NODES - F - 1)) connected validators stop ($stalled -> $now)" || fail "partition: progress without a quorum ($stalled -> $now)"
for n in $cut; do
  ip="172.32.0.1$n"
  docker network connect --ip "$ip" "$NET" "$(container "$n")"
done
sleep 5
mesh $(seq 1 "$NODES")
wait_height "$P1" $((now + 10)) 600 && pass "partition healed: production resumed" || fail "no progress after the heal"
agree "after the partition"
no_evidence "after the partition"

# S-11: kill -9 validators in turn while blocks carry transactions.
load 120 &
LOAD=$!
for round in 1 2 3; do
  for n in $(seq 2 "$NODES"); do
    (( (n + round) % 3 == 0 )) || continue
    docker kill -s KILL "$(container "$n")" >/dev/null
    sleep 2
    docker start "$(container "$n")" >/dev/null
  done
  sleep 10
  mesh $(seq 1 "$NODES")
done
wait "$LOAD" 2>/dev/null || true
head=$(block_number "$P1")
wait_height "$P1" $((head + 5)) 300 && pass "progress after repeated kill -9" || fail "no progress after kill -9"
agree "after kill -9"
no_evidence "after kill -9"

# S-12: a validator restarted without its WAL signs nothing until a height
# commits without it (design §6.1), then takes part again.
n=$NODES
docker stop --time 60 "$(container "$n")" >/dev/null
wal=$(find "data/node$n" -path '*metabft/wal' -type f | head -1)
[[ -n "$wal" ]] || fail "node$n has no WAL file"
rm -f "$wal"
docker start "$(container "$n")" >/dev/null
sleep 5
mesh $(seq 1 "$NODES")
if docker logs --since 30s "$(container "$n")" 2>&1 | grep -q "signing nothing until"; then
  pass "node$n restarted without a WAL as an observer"
else
  fail "node$n did not report observer mode"
fi
target=$(( $(block_number "$P1") + 5 ))
wait_height "$(port_of "$n")" "$target" 300 || true
obs=$(rpc "$(port_of "$n")" metabft_status '[]' | field observer)
[[ $obs == "False" ]] && pass "node$n left observer mode after a height committed" || fail "node$n still an observer ($obs)"
agree "after the WAL loss"
no_evidence "after the WAL loss"

status
(( FAIL == 0 )) && log "=== ALL PASSED ===" || { log "=== FAILURES ==="; exit 1; }
