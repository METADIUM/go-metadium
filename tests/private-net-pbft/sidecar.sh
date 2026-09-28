#!/usr/bin/env bash
# sidecar.sh - blob sidecars fetched before PREPARE over a slow link (§11.3
# M-10). node3 runs the pbftfault sidecar-fetch fault: it disregards its blob
# pool when checking a proposal, as a validator does that the pool has not
# reached yet, so it must fetch every blob block's sidecars from its peers
# within sidecarWait (2 s). Its links are then slowed both ways with tc
# netem (a one-way delay and a rate), and full-blob blocks are sent: 2
# blobs, 2 × 128 KiB, Metadium's MaxBlobGasPerBlock. Needs the pbftfault build, the blobs tool, and the timers:
#   go build -tags pbftfault -o build/bin/gmet-fault ./cmd/geth
#   go build -o build/bin/blobs ./tests/private-net-pbft/blobs
#   GMET_BIN=../../build/bin/gmet-fault NODES=7 \
#     NODE_ARGS="--metrics --metrics.addr 127.0.0.1 --metadium.block.idleseal 100" ./setup.sh
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"
source ./lib.sh

BLOBS="${BLOBS_BIN:-$SCRIPT_DIR/../../build/bin/blobs}"
[[ -x $BLOBS ]] || err "no blobs tool at $BLOBS (go build -o build/bin/blobs ./tests/private-net-pbft/blobs)"
FAR=3                              # the validator behind the slow link
# Validators stopped for the run, so the quorum needs node FAR: otherwise
# the others commit a blob block before node FAR's fetch matters.
DOWN=$(echo $(seq $((QUORUM + 1)) "$NODES"))
PER=${PER:-3}                      # blob blocks per profile
PROFILES=${PROFILES:-"25ms/1gbit 50ms/100mbit 100ms/20mbit 150ms/5mbit"}  # one-way delay / rate
P1=$(port_of 1)
FAIL=0
pass() { log "PASS  $*"; }
fail() { log "FAIL  $*"; FAIL=1; }
ip_of() { echo "172.32.0.1$1"; }
tc_on() { docker exec -u 0 "$(container "$1")" tc "${@:2}"; }
timer() { # N: count and mean (ns) of node N's sidecar fetch timer
  docker exec "$(container "$1")" curl -s localhost:6060/debug/metrics |
    python3 -c "import sys,json; m=json.load(sys.stdin); print(m.get('metabft/proposal/sidecars.count',0), m.get('metabft/proposal/sidecars.mean',0), m.get('metabft/proposal/sidecars.max',0))"
}
status_of() { rpc "$(port_of "$1")" metabft_status '[]'; }
slow() { # DELAY RATE: node FAR's links, both ways
  tc_on $FAR qdisc replace dev eth0 root netem delay "$1" rate "$2"
  for n in $(seq 1 "$NODES"); do
    [[ $n == "$FAR" || " $DOWN " == *" $n "* ]] && continue
    tc_on "$n" qdisc replace dev eth0 root handle 1: prio
    tc_on "$n" qdisc replace dev eth0 parent 1:3 handle 30: netem delay "$1" rate "$2"
    tc_on "$n" filter replace dev eth0 parent 1:0 protocol ip prio 1 u32 match ip dst "$(ip_of $FAR)" flowid 1:3
  done
}
fast() { for n in $(seq 1 "$NODES"); do [[ " $DOWN " == *" $n "* ]] || tc_on "$n" qdisc del dev eth0 root 2>/dev/null || true; done; }

log "=== M-10: sidecars fetched by node$FAR before PREPARE, over a slow link ==="
docker exec "$(container 1)" curl -sf localhost:6060/debug/metrics >/dev/null || err "no /debug/metrics: run the nodes with --metrics --metrics.addr 127.0.0.1"
tc_on 1 qdisc show dev eth0 >/dev/null || err "no tc in the image (iproute2)"
env FAULT_node$FAR=sidecar-fetch docker compose up -d --force-recreate node$FAR >/dev/null 2>&1
sleep 8
mesh $(seq 1 "$NODES")
for _ in $(seq 1 30); do (( $(block_number "$(port_of $FAR)" 2>/dev/null || echo 0) >= $(block_number "$P1") - 1 )) && break; sleep 2; done

refused=""
for n in $DOWN; do docker stop "$(container "$n")" >/dev/null; done
log "stopped $(echo $DOWN | sed 's/[0-9]*/node&/g'): $QUORUM of $NODES validators left, the quorum, node$FAR among them"

for profile in $PROFILES; do
  delay=${profile%/*} rate=${profile#*/}
  slow "$delay" "$rate"
  read -r c0 m0 _ < <(timer $FAR)
  rounds=()
  for _ in $(seq 1 "$PER"); do
    out=$("$BLOBS" "http://localhost:$P1" 2 2>&1) || { fail "$profile: blobs not committed: $out"; continue; }
    [[ $out == *"in block "*","* ]] && log "  the blobs landed in more than one block: $out"
    b=$(sed -n 's/.* in block \([0-9]*\).*/\1/p' <<<"$out")
    rounds+=("$(rpc "$P1" eth_getBlockByNumber "[\"$(printf '0x%x' "$b")\", false]" | python3 -c "import sys,json; print(int(json.load(sys.stdin).get('bftRound','0x0'),16))")")
  done
  read -r c1 m1 max1 < <(timer $FAR)
  n=$(( c1 - c0 ))
  if (( n > 0 )); then
    mean=$(python3 -c "print(f'{($c1*$m1 - $c0*$m0)/$n/1e6:.0f}')")
    log "M-10 $profile: node$FAR fetched $n blob block(s)' sidecars, mean $mean ms (max so far $(python3 -c "print(f'{$max1/1e6:.0f}')") ms); rounds ${rounds[*]}"
  else
    log "M-10 $profile: node$FAR fetched nothing (it proposed, or the blocks were not blob blocks); rounds ${rounds[*]}"
  fi
  # the last refusal, "at reason", compared with the one before this profile
  r=$(status_of $FAR | python3 -c "import sys,json; x=json.load(sys.stdin).get('lastRejection') or {}; print(x.get('at',''), x.get('reason',''))")
  (( n > 0 )) && pass "$profile: node$FAR fetched the sidecars of $n blob block(s)" || fail "$profile: node$FAR fetched none"
  if [[ $r == *sidecar* && $r != "$refused" ]]; then
    fail "$profile: node$FAR refused a proposal for its sidecars: ${r:0:200}"
  else
    pass "$profile: node$FAR refused no proposal for its sidecars"
  fi
  refused=$r
done
fast
for n in $DOWN; do docker start "$(container "$n")" >/dev/null; done
# The recreate drops node FAR's log, refusals included: keep it.
mkdir -p logs && docker logs "$(container $FAR)" >"logs/sidecar-node$FAR.log" 2>&1 || true
env FAULT_node$FAR= docker compose up -d --force-recreate node$FAR >/dev/null 2>&1
sleep 8
mesh $(seq 1 "$NODES")
status
(( FAIL == 0 )) && log "=== ALL PASSED ===" || { log "=== FAILURES ==="; exit 1; }
