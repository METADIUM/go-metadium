#!/usr/bin/env bash
# twin.sh - the same node key on two servers (§11.2 S-13). node2's data
# directory, node key and WAL included, is copied to a second server, which
# is started next to it. devp2p keeps one connection per node ID, so each
# validator reaches one of the two; the script first lets that happen on its
# own, then splits them explicitly (node2 with node1/3/4, the twin with
# node5/6/7). Expected: one chain, and each time the two sign different
# votes, the relayed conflicting pair is stored as evidence against node2 on
# every other validator, the same on each, and both servers raise the alarm.
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"
source ./lib.sh

(( NODES == 7 )) || err "twin.sh expects NODES=7 (the split is 3 and 3 around node2), have $NODES"
P1=$(port_of 1)
BAD=2
TWIN=gmet-pbft-twin
TWIN_IP=172.32.0.30
TWIN_PORT=8660
NET=$(docker inspect "$(container 1)" -f '{{range $k, $v := .NetworkSettings.Networks}}{{$k}}{{end}}')
BAD_ID=$(awk -v n="node$BAD" '$1==n{print $2}' node-ids.txt)
FAIL=0
pass() { log "PASS  $*"; }
fail() { log "FAIL  $*"; FAIL=1; }
ip_of() { echo "172.32.0.1$1"; }
hash_at() { rpc "$1" eth_getBlockByNumber "[\"$(printf '0x%x' "$2")\", false]" | python3 -c "import sys,json; print(json.load(sys.stdin)['hash'])"; }
fw() { docker exec -u 0 "$1" iptables "${@:2}"; }
cut_off() { # CONTAINER IP NODE...: no traffic between the container and the nodes
  local c=$1 ip=$2; shift 2
  for n in "$@"; do
    fw "$c" -A INPUT -s "$(ip_of "$n")" -j DROP; fw "$c" -A OUTPUT -d "$(ip_of "$n")" -j DROP
    fw "$(container "$n")" -A INPUT -s "$ip" -j DROP; fw "$(container "$n")" -A OUTPUT -d "$ip" -j DROP
  done
}
# against N: the heights at which node N holds evidence against node BAD's key
against() {
  rpc "$(port_of "$1")" metabft_getEvidence '[]' |
    python3 -c "import sys,json; print(' '.join(sorted({str(int(e['height'],16)) for e in json.load(sys.stdin) or [] if e['signer'].lower().removeprefix('0x') == '$BAD_ID'.lower()})))"
}
agree() { # every node and the twin have the same block, 2 below node1's head
  local h ref
  h=$(( $(block_number "$P1") - 2 ))
  ref=$(hash_at "$P1" "$h")
  for p in $(for n in $(seq 2 "$NODES"); do port_of "$n"; done) $TWIN_PORT; do
    [[ $(hash_at "$p" "$h") == "$ref" ]] || { fail "port $p disagrees at $h $1"; return; }
  done
  pass "all $NODES nodes and the twin agree at block $h $1"
}
progress() { # SECONDS LABEL: the chain grows over the interval
  local a b
  a=$(block_number "$P1"); sleep "$1"; b=$(block_number "$P1")
  (( b > a + 5 )) && pass "the chain continues $2 ($a -> $b)" || fail "the chain stalled $2 ($a -> $b)"
}

log "=== S-13: node$BAD's key on a second server ==="
docker stop "$(container $BAD)" >/dev/null
rm -rf data/twin && cp -a "data/node$BAD" data/twin
docker start "$(container $BAD)" >/dev/null
docker run -d --name "$TWIN" --network "$NET" --ip "$TWIN_IP" --cap-add NET_ADMIN \
  -u "$(id -u):$(id -g)" -v "$PWD/genesis.json:/data/genesis.json:ro" -v "$PWD/data/twin:/data/geth" \
  -p "127.0.0.1:$TWIN_PORT:8545" --entrypoint /entrypoint.sh gmet-pbft:latest \
  --networkid 1337 --consensusmethod 2 --syncmode full --gcmode archive --mine --miner.etherbase "$(account_of $BAD)" \
  --http --http.addr 0.0.0.0 --http.port 8545 --http.api eth,net,web3,admin,metabft --http.vhosts '*' \
  --port 30303 --nat "extip:$TWIN_IP" --maxpeers 16 --verbosity 3 --userocksdb 0 >/dev/null
for _ in $(seq 1 30); do rpc "$TWIN_PORT" eth_blockNumber '[]' >/dev/null 2>&1 && break; sleep 2; done
sleep 5
mesh $(seq 1 "$NODES")
for m in $(seq 1 "$NODES"); do [[ $m == "$BAD" ]] || rpc "$TWIN_PORT" admin_addPeer "[\"$(enode_of "$m")\"]" >/dev/null 2>&1 || true; done

log "--- both servers up, each validator connected to whichever reached it first ---"
progress 60 "with node$BAD's key on two servers"
agree "with node$BAD's key on two servers"

log "--- node$BAD reaches node1/3/4, the twin node5/6/7 ---"
cut_off "$(container $BAD)" "$(ip_of $BAD)" 5 6 7
cut_off "$TWIN" "$TWIN_IP" 1 3 4
sleep 40 # the cut links time out
for m in 5 6 7; do rpc "$TWIN_PORT" admin_addPeer "[\"$(enode_of "$m")\"]" >/dev/null 2>&1 || true; done
progress 90 "split between the two servers"
agree "split between the two servers"

ref=""; same=1
for n in $(seq 1 "$NODES"); do
  [[ $n == "$BAD" ]] && continue
  hs=$(against "$n"); log "  node$n: evidence against node$BAD at heights: ${hs:-none}"
  if [[ -z $ref ]]; then ref=$hs; elif [[ $hs != "$ref" ]]; then same=0; fi
done
[[ -n $ref && $same == 1 ]] && pass "every other validator stores the same evidence against node$BAD's key (heights $ref)" ||
  fail "the evidence against node$BAD is missing or differs between validators"
for c in "$(container $BAD)" "$TWIN"; do
  alarms=$(docker logs "$c" 2>&1 | grep -c "This node's key signed two different messages" || true)
  (( alarms > 0 )) && pass "$c raises the alarm ($alarms time(s))" || fail "no alarm on $c"
done

mkdir -p logs && docker logs "$TWIN" >"logs/twin.log" 2>&1 || true
docker rm -f "$TWIN" >/dev/null
rm -rf data/twin
for n in $(seq 1 "$NODES"); do fw "$(container "$n")" -F; done
mesh $(seq 1 "$NODES")
status
(( FAIL == 0 )) && log "=== ALL PASSED ===" || { log "=== FAILURES ==="; exit 1; }
