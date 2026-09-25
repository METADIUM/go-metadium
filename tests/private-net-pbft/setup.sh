#!/usr/bin/env bash
# setup.sh - initialise an N-node private network that bootstraps on PoA and
# switches to PBFT at BFT_BLOCK (docs/pbft-consensus-design.md §9.2).
#
# Usage:   ./setup.sh
# Options: GMET_BIN=/path/to/gmet  BOOTNODE_BIN=/path/to/bootnode
#          NODES=4 (4..9; all of them governance members and validators)
#          NODE_ARGS="--metadium.block.idleseal 100" (extra flags for every node)
#          BFT_BLOCK=200 (the switch; governance must be deployed before it)
#          CAMELLIA_BLOCK=100 (must not be after BFT_BLOCK)
#
# Node keys are generated here (bootnode -genkey), so every node's ID is
# known before start: node1's goes into the genesis as the bootnode.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

GMET_BIN="${GMET_BIN:-$SCRIPT_DIR/../../build/bin/gmet}"
BOOTNODE_BIN="${BOOTNODE_BIN:-$SCRIPT_DIR/../../build/bin/bootnode}"
NODES="${NODES:-4}"
NODE_ARGS="${NODE_ARGS:-}"   # extra flags for every node, e.g. "--metadium.block.idleseal 100"
BFT_BLOCK="${BFT_BLOCK:-200}"
CAMELLIA_BLOCK="${CAMELLIA_BLOCK:-100}"
PASSWORD="privatenet123"

log()  { echo "[$(date '+%H:%M:%S')] $*"; }
err()  { echo "[ERROR] $*" >&2; exit 1; }

[[ -x "$GMET_BIN" ]] || err "gmet binary not found: $GMET_BIN"
[[ -x "$BOOTNODE_BIN" ]] || err "bootnode binary not found: $BOOTNODE_BIN (go build -o build/bin/bootnode ./cmd/bootnode)"
(( CAMELLIA_BLOCK <= BFT_BLOCK )) || err "CAMELLIA_BLOCK ($CAMELLIA_BLOCK) must not be after BFT_BLOCK ($BFT_BLOCK)"

log "=== PBFT private network initialisation (chainId=1337, camelliaBlock=$CAMELLIA_BLOCK, bftBlock=$BFT_BLOCK) ==="

cat > .env <<ENV
GMET_UID=$(id -u)
GMET_GID=$(id -g)
ENV

rm -rf data/ passwords.txt genesis.json node-ids.txt
echo "$PASSWORD" > passwords.txt
chmod 600 passwords.txt

# Test accounts (Hardhat defaults - for testing only); the address of each
# comes from the import.
PRIVKEYS=(
  "ac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80"
  "59c6995e998f97a5a0044966f0945389dc9e86dae88c7a8412f4603b6b78690d"
  "5de4111afa1a4b94908f83103eb1f1706367c2e68ca870fc3fb9a804cdab365a"
  "7c852118294e51e653712a81e05800f419141751be58f605c371e15141b007a6"
  "47e179ec197488593b187f80a00eb0da91f1b9d0b13f8733639f19c30a34926a"
  "8b3a350cf5c34c9194ca85829a2df0ec3153be0318b5e2d3348e872092edffba"
  "92db14e403b83dfe3df233f83dfa3a0d7096f21ca9b0d6d6b8d88b2b4ec1564e"
  "4bbbf85ce3377467afe5d46f804f221813b2bb87f24d81f60f1fcdbf7cbf4356"
  "dbda1821b80551c9d65939329250298aa3472ba22feea921c0cf5d620ea67b97"
)
(( NODES >= 4 && NODES <= ${#PRIVKEYS[@]} )) || err "NODES must be 4..${#PRIVKEYS[@]}, have $NODES"

# node-ids.txt: "nodeN <node ID> <account>" per node
for i in $(seq 0 $((NODES - 1))); do
  node="node$((i + 1))"
  mkdir -p "data/$node/gmet" "data/$node/geth"
  KEYFILE=$(mktemp)
  echo "${PRIVKEYS[$i]}" > "$KEYFILE"
  out=$("$GMET_BIN" account import --datadir "data/$node" --password passwords.txt --lightkdf "$KEYFILE" 2>&1) || {
    rm -f "$KEYFILE"
    err "account import failed for $node: $out"
  }
  rm -f "$KEYFILE"
  account="0x$(sed -n 's/.*Address: {\([0-9a-f]*\)}.*/\1/p' <<<"$out")"
  [[ ${#account} == 42 ]] || err "no address in the import output for $node"
  # The instance directory is geth/ or gmet/ depending on the binary; write both.
  "$BOOTNODE_BIN" -genkey "data/$node/geth/nodekey"
  cp "data/$node/geth/nodekey" "data/$node/gmet/nodekey"
  chmod 600 "data/$node/geth/nodekey" "data/$node/gmet/nodekey"
  id=$("$BOOTNODE_BIN" -nodekey "data/$node/geth/nodekey" -writeaddress)
  echo "$node $id $account" >> node-ids.txt
  cp passwords.txt "data/$node/password.txt"
  log "  $node: $account ${id:0:16}..."
done
NODE1_ID=$(awk '$1=="node1"{print $2}' node-ids.txt)
ACCOUNT1=$(awk '$1=="node1"{print $3}' node-ids.txt)
ALLOC=$(awk '{printf "%s    \"%s\": { \"balance\": \"0x56BC75E2D63100000000\" }", (NR>1 ? ",\n" : ""), $3}' node-ids.txt)

cat > genesis.json <<GEN
{
  "alloc": {
$ALLOC
  },
  "coinbase": "$ACCOUNT1",
  "config": {
    "chainId": 1337,
    "homesteadBlock": 0,
    "eip150Block": 0,
    "eip155Block": 0,
    "eip158Block": 0,
    "byzantiumBlock": 0,
    "constantinopleBlock": 0,
    "petersburgBlock": 0,
    "istanbulBlock": 0,
    "muirGlacierBlock": 0,
    "berlinBlock": 0,
    "londonBlock": 0,
    "avocadoBlock": 0,
    "pangyoBlock": 0,
    "applepieBlock": 0,
    "bokbunjaBlock": 0,
    "camelliaBlock": $CAMELLIA_BLOCK,
    "bftBlock": $BFT_BLOCK,
    "bft": { "emptyBlockInterval": 5, "baseTimeout": 2, "maxBackoffExp": 5, "timeDrift": 2 }
  },
  "difficulty": "0x1",
  "extraData": "0x${NODE1_ID}",
  "gasLimit": "0x10000000",
  "minerNodeId": "0x0",
  "minerNodeSig": "0x0",
  "mixhash": "0x0000000000000000000000000000000000000000000000000000000000000000",
  "nonce": "0x0000000000000042",
  "parentHash": "0x0000000000000000000000000000000000000000000000000000000000000000",
  "rewards": "0x",
  "timestamp": "0x00"
}
GEN
log "genesis.json created"

# docker-compose.yml for NODES nodes; RPC on 127.0.0.1:8644+n.
{
  cat <<'HDR'
# Generated by setup.sh: NODES-node private network, PoA bootstrap then PBFT
# from bftBlock (docs/pbft-consensus-design.md §9). RPC is bound to
# 127.0.0.1 only: node1 runs with an unlocked account.
networks:
  gmet-pbft-net:
    driver: bridge
    ipam:
      config:
        - subnet: 172.32.0.0/24

x-gmet-common: &gmet-common
  image: gmet-pbft:latest
  restart: unless-stopped
  cap_add: [NET_ADMIN]   # faults.sh partitions with iptables (as root, via docker exec)
  user: "${GMET_UID:-1000}:${GMET_GID:-1000}"
  entrypoint: ["/entrypoint.sh"]
  networks:
    - gmet-pbft-net

services:
HDR
  while read -r node id account; do
    n=${node#node}
    echo "  $node:"
    echo "    <<: *gmet-common"
    echo "    container_name: gmet-pbft-$node"
    echo "    hostname: gmet-pbft-$node"
    [[ $n != 1 ]] && printf '    depends_on:\n      - node1\n'
    echo "    volumes:"
    echo "      - ./genesis.json:/data/genesis.json:ro"
    echo "      - ./data/$node:/data/geth"
    echo "    ports:"
    echo "      - \"127.0.0.1:$((8644 + n)):8545\""
    echo "    networks:"
    echo "      gmet-pbft-net:"
    echo "        ipv4_address: 172.32.0.1$n"
    echo "    command: >"
    echo "      --networkid 1337 --consensusmethod 2 --syncmode full --gcmode archive"
    echo "      --mine --miner.etherbase $account"
    echo "      --http --http.addr 0.0.0.0 --http.port 8545"
    echo "      --http.api eth,net,web3,admin,miner,txpool,debug,personal,metabft"
    echo "      --http.corsdomain \"*\" --http.vhosts \"*\""
    echo "      --port 30303 --nat extip:172.32.0.1$n --maxpeers 16 --verbosity 3 --userocksdb 0"
    [[ -n "$NODE_ARGS" ]] && echo "      $NODE_ARGS"
    if [[ $n == 1 ]]; then
      echo "      --unlock $account --password /data/geth/password.txt --allow-insecure-unlock --rpc.txfeecap 0"
    fi
  done < node-ids.txt
} > docker-compose.yml
log "docker-compose.yml created ($NODES nodes)"

log "Building Docker image (gmet-pbft:latest)..."
BUILD=$(mktemp -d)
cp "$GMET_BIN" "$BUILD/gmet"
cp ../private-net-poa/entrypoint.sh "$BUILD/"
docker build -q -f Dockerfile -t gmet-pbft:latest "$BUILD" >/dev/null
rm -rf "$BUILD"
log "=== Done. Next: ./start.sh, then ./deploy.sh before block $BFT_BLOCK ==="
