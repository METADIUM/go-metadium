#!/usr/bin/env bash
# setup.sh - initialise a 4-node private network that bootstraps on PoA and
# switches to PBFT at BFT_BLOCK (docs/pbft-consensus-design.md §9.2).
#
# Usage:   ./setup.sh
# Options: GMET_BIN=/path/to/gmet  BOOTNODE_BIN=/path/to/bootnode
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

# Test accounts (Hardhat defaults - for testing only)
PRIVKEYS=(
  "ac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80"
  "59c6995e998f97a5a0044966f0945389dc9e86dae88c7a8412f4603b6b78690d"
  "5de4111afa1a4b94908f83103eb1f1706367c2e68ca870fc3fb9a804cdab365a"
  "7c852118294e51e653712a81e05800f419141751be58f605c371e15141b007a6"
)
ACCOUNTS=(
  "0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266"
  "0x70997970C51812dc3A010C7d01b50e0d17dc79C8"
  "0x3C44CdDdB6a900fa2b585dd299e03d12FA4293BC"
  "0x90F79bf6EB2c4f870365E785982E1f101E93b906"
)

for i in 0 1 2 3; do
  node="node$((i + 1))"
  mkdir -p "data/$node/gmet" "data/$node/geth"
  KEYFILE=$(mktemp)
  echo "${PRIVKEYS[$i]}" > "$KEYFILE"
  if ! "$GMET_BIN" account import --datadir "data/$node" --password passwords.txt --lightkdf "$KEYFILE" >/dev/null 2>&1; then
    rm -f "$KEYFILE"
    err "account import failed for $node"
  fi
  rm -f "$KEYFILE"
  # The instance directory is geth/ or gmet/ depending on the binary; write both.
  "$BOOTNODE_BIN" -genkey "data/$node/geth/nodekey"
  cp "data/$node/geth/nodekey" "data/$node/gmet/nodekey"
  chmod 600 "data/$node/geth/nodekey" "data/$node/gmet/nodekey"
  id=$("$BOOTNODE_BIN" -nodekey "data/$node/geth/nodekey" -writeaddress)
  echo "$node $id" >> node-ids.txt
  cp passwords.txt "data/$node/password.txt"
  log "  $node: ${ACCOUNTS[$i]} ${id:0:16}..."
done
NODE1_ID=$(awk '$1=="node1"{print $2}' node-ids.txt)

cat > genesis.json <<GEN
{
  "alloc": {
    "${ACCOUNTS[0]}": { "balance": "0x56BC75E2D63100000000" },
    "${ACCOUNTS[1]}": { "balance": "0x56BC75E2D63100000000" },
    "${ACCOUNTS[2]}": { "balance": "0x56BC75E2D63100000000" },
    "${ACCOUNTS[3]}": { "balance": "0x56BC75E2D63100000000" }
  },
  "coinbase": "${ACCOUNTS[0]}",
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

log "Building Docker image (gmet-pbft:latest)..."
BUILD=$(mktemp -d)
cp "$GMET_BIN" "$BUILD/gmet"
cp ../private-net-poa/entrypoint.sh "$BUILD/"
docker build -q -f ../private-net-poa/Dockerfile -t gmet-pbft:latest "$BUILD" >/dev/null
rm -rf "$BUILD"
log "=== Done. Next: ./start.sh, then ./deploy.sh before block $BFT_BLOCK ==="
