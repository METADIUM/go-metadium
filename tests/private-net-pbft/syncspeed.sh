#!/usr/bin/env bash
# syncspeed.sh - full-sync verification speed (§11.3 M-08). node1's chain is
# exported in two files, the PoA segment (1..bftBlock-1) and the PBFT
# segment (bftBlock..head-2), and a fresh node with no peers imports each
# through admin_importChain, timed. This is the verification and execution
# a full-syncing node does, without the network: Engine.VerifyHeaders
# checks a batch sequentially, commit seals included, on one goroutine.
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"
source ./lib.sh

P1=$(port_of 1)
BFT=$(python3 -c "import json; print(json.load(open('genesis.json'))['config']['bftBlock'])")
NET=$(docker inspect "$(container 1)" -f '{{range $k, $v := .NetworkSettings.Networks}}{{$k}}{{end}}')
IMPORTER=gmet-pbft-importer
PORT=8661
DIR=data/importer
last=$(( $(block_number "$P1") - 2 ))
(( last > BFT )) || err "the chain is at $last; run this past bftBlock ($BFT)"

rm -rf "$DIR" && mkdir -p "$DIR"
rpc "$P1" admin_exportChain "[\"/data/geth/poa.rlp\", 1, $((BFT - 1))]" >/dev/null
rpc "$P1" admin_exportChain "[\"/data/geth/pbft.rlp\", $BFT, $last]" >/dev/null
mv data/node1/poa.rlp data/node1/pbft.rlp "$DIR/"
docker run -d --name "$IMPORTER" --network "$NET" -u "$(id -u):$(id -g)" \
  -v "$PWD/genesis.json:/data/genesis.json:ro" -v "$PWD/$DIR:/data/geth" \
  -p "127.0.0.1:$PORT:8545" --entrypoint /entrypoint.sh gmet-pbft:latest \
  --networkid 1337 --consensusmethod 2 --syncmode full --nodiscover --maxpeers 0 \
  --http --http.addr 0.0.0.0 --http.port 8545 --http.api eth,admin --http.vhosts '*' >/dev/null
for _ in $(seq 1 30); do rpc "$PORT" eth_blockNumber '[]' >/dev/null 2>&1 && break; sleep 2; done

txs() { # FROM TO: transactions in node1's blocks FROM..TO
  python3 - "$P1" "$1" "$2" <<'PY'
import sys, json, urllib.request
port, a, b = sys.argv[1], int(sys.argv[2]), int(sys.argv[3])
n = 0
for h in range(a, b + 1):
    body = json.dumps({"jsonrpc": "2.0", "id": 1, "method": "eth_getBlockTransactionCountByNumber", "params": [hex(h)]}).encode()
    r = urllib.request.urlopen(urllib.request.Request(f"http://localhost:{port}", body, {"Content-Type": "application/json"}))
    n += int(json.load(r)["result"], 16)
print(n)
PY
}
timed_import() { # FILE FROM TO LABEL
  local t0 t1 ms n
  t0=$(date +%s%N)
  rpc "$PORT" admin_importChain "[\"/data/geth/$1\"]" >/dev/null || err "import of $1 failed"
  t1=$(date +%s%N)
  ms=$(( (t1 - t0) / 1000000 ))
  (( $(block_number "$PORT") == $3 )) || err "import of $1 ended at $(block_number "$PORT"), want $3"
  n=$(txs "$2" "$3")
  log "M-08 $4: blocks $2..$3 ($(( $3 - $2 + 1 )) blocks, $n txs) in $ms ms: $(python3 -c "print(f'{($3 - $2 + 1) / max($ms, 1) * 1000:.0f} blocks/s, {$n / max($ms, 1) * 1000:.0f} txs/s')")"
}
log "=== Full-sync verification speed: bftBlock $BFT, chain 1..$last ==="
timed_import poa.rlp 1 $((BFT - 1)) "PoA segment"
timed_import pbft.rlp "$BFT" "$last" "PBFT segment (commit seals)"
docker rm -f "$IMPORTER" >/dev/null
rm -rf "$DIR"
