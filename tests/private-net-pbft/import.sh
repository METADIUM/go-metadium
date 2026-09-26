#!/usr/bin/env bash
# import.sh - blocks with tampered seals, imported (§11.2 S-10, S-17). The
# chain is exported from node1 and one block rewritten by the tamper tool;
# a fresh node with no peers imports the result through admin_importChain,
# the full import path (header rules, then execution). Every tampered file
# must stop the import just below its block; the untouched chain must
# import to the end. Needs the tamper tool:
#   go build -o build/bin/tamper ./tests/private-net-pbft/tamper
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"
source ./lib.sh

TAMPER="${TAMPER_BIN:-$SCRIPT_DIR/../../build/bin/tamper}"
[[ -x $TAMPER ]] || err "no tamper tool at $TAMPER (go build -o build/bin/tamper ./tests/private-net-pbft/tamper)"
P1=$(port_of 1)
BFT=$(python3 -c "import json; print(json.load(open('genesis.json'))['config']['bftBlock'])")
CHAIN_ID=$(python3 -c "import json; print(json.load(open('genesis.json'))['config']['chainId'])")
NET=$(docker inspect "$(container 1)" -f '{{range $k, $v := .NetworkSettings.Networks}}{{$k}}{{end}}')
IMPORTER=gmet-pbft-importer
IMPORTER_PORT=8661
DIR=data/importer
FAIL=0
pass() { log "PASS  $*"; }
fail() { log "FAIL  $*"; FAIL=1; }

head=$(block_number "$P1")
(( head >= BFT + 10 )) || err "the chain is at $head; run this past bftBlock + 10 ($((BFT + 10)))"
last=$((head - 2))
pbft=$((BFT + 5))   # a PBFT block to tamper with
pre=$((BFT - 5))    # a block below the switch
log "=== Tampered seals on import: bftBlock $BFT, chain 1..$last, PBFT block $pbft, pre-fork block $pre ==="

rpc "$P1" admin_exportChain "[\"/data/geth/export.rlp\", 1, $last]" >/dev/null || err "export failed"
rm -rf "$DIR" && mkdir -p "$DIR"
mv data/node1/export.rlp "$DIR/good.rlp"
"$TAMPER" "$DIR/good.rlp" "$DIR/preseal.rlp" "$pre" "$CHAIN_ID" preseal
for mode in drop forge dup round; do
  "$TAMPER" "$DIR/good.rlp" "$DIR/$mode.rlp" "$pbft" "$CHAIN_ID" "$mode"
done

docker run -d --name "$IMPORTER" --network "$NET" -u "$(id -u):$(id -g)" \
  -v "$PWD/genesis.json:/data/genesis.json:ro" -v "$PWD/$DIR:/data/geth" \
  -p "127.0.0.1:$IMPORTER_PORT:8545" --entrypoint /entrypoint.sh gmet-pbft:latest \
  --networkid 1337 --consensusmethod 2 --syncmode full --nodiscover --maxpeers 0 \
  --http --http.addr 0.0.0.0 --http.port 8545 --http.api eth,admin,metabft --http.vhosts '*' >/dev/null
for _ in $(seq 1 30); do rpc "$IMPORTER_PORT" eth_blockNumber '[]' >/dev/null 2>&1 && break; sleep 2; done

# import FILE LABEL WANT_HEAD
import() {
  local out h
  out=$(curl -s -m 600 -X POST -H "Content-Type: application/json" \
    --data "{\"jsonrpc\":\"2.0\",\"method\":\"admin_importChain\",\"params\":[\"/data/geth/$1.rlp\"],\"id\":1}" \
    "http://localhost:$IMPORTER_PORT")
  h=$(block_number "$IMPORTER_PORT")
  if (( h == $3 )); then
    pass "$2: head at $h after the import ($(python3 -c "import sys,json; d=json.loads(sys.argv[1]); print(d.get('error',{}).get('message','ok')[:150])" "$out"))"
  else
    fail "$2: head $h after the import, want $3 ($out)"
  fi
}
import preseal "S-17 pre-fork block $pre with commit seals" $((pre - 1))
import drop "S-10 block $pbft one seal short of the quorum" $((pbft - 1))
import forge "S-10 block $pbft with a seal by a key outside the set" $((pbft - 1))
import dup "S-10 block $pbft with one validator's seal twice" $((pbft - 1))
import round "S-10 block $pbft with its round changed under the seals" $((pbft - 1))
import good "the untouched chain" "$last"
[[ $(rpc "$IMPORTER_PORT" eth_getBlockByNumber "[\"$(printf '0x%x' "$last")\", false]" | python3 -c "import sys,json; print(json.load(sys.stdin)['hash'])") == \
   $(rpc "$P1" eth_getBlockByNumber "[\"$(printf '0x%x' "$last")\", false]" | python3 -c "import sys,json; print(json.load(sys.stdin)['hash'])") ]] &&
  pass "the imported chain is node1's at block $last" || fail "the imported block $last differs from node1's"

docker rm -f "$IMPORTER" >/dev/null
rm -rf "$DIR"
(( FAIL == 0 )) && log "=== ALL PASSED ===" || { log "=== FAILURES ==="; exit 1; }
