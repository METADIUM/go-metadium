#!/usr/bin/env bash
# setup.sh - go-metadium PoA private 3-node network initialization
# Usage: ./setup.sh
# Options: GMET_BIN=/path/to/gmet ./setup.sh
#
# Phase 1: only node1(bootnode) produces blocks, node2/3 sync only
# Phase 2: deploy governance contract and add other nodes (separate step)

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

GMET_BIN="${GMET_BIN:-$SCRIPT_DIR/../../geth}"
USE_ROCKSDB="${USE_ROCKSDB:-0}"
NETWORKID=1337
PASSWORD="privatenet123"

log()  { echo "[$(date '+%H:%M:%S')] $*"; }
err()  { echo "[ERROR] $*" >&2; exit 1; }

[[ -x "$GMET_BIN" ]] || err "gmet binary not found: $GMET_BIN"

log "=== PoA private network initialization started (chainId=$NETWORKID, CamelliaFork=100) ==="

# The nodes run as whoever ran this script, because that is who owns data/.
# docker compose reads .env from this directory, so a bare `docker compose up`
# gets the right ids too.
cat > .env <<EOF
GMET_UID=$(id -u)
GMET_GID=$(id -g)
EOF
log "Container user: $(id -u):$(id -g) (written to .env)"

# Clean up existing data
log "Cleaning up existing data..."
rm -rf data/ passwords.txt static-nodes.json genesis.json
mkdir -p data/node1/geth data/node2/geth data/node3/geth
mkdir -p data/node1/gmet data/node2/gmet data/node3/gmet
echo "$PASSWORD" > passwords.txt
chmod 600 passwords.txt

# Test account private keys (Hardhat defaults - for testing only)
PRIVKEYS=(
  "ac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80"
  "59c6995e998f97a5a0044966f0945389dc9e86dae88c7a8412f4603b6b78690d"
  "5de4111afa1a4b94908f83103eb1f1706367c2e68ca870fc3fb9a804cdab365a"
)
ACCOUNTS=(
  "0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266"
  "0x70997970C51812dc3A010C7d01b50e0d17dc79C8"
  "0x3C44CdDdB6a900fa2b585dd299e03d12FA4293BC"
)
NODES=(node1 node2 node3)

# Import account keystore (into each node's datadir)
log "Importing accounts..."
for i in 0 1 2; do
  node="${NODES[$i]}"
  KEYFILE=$(mktemp /tmp/privkey.XXXXXX)
  echo "${PRIVKEYS[$i]}" > "$KEYFILE"
  "$GMET_BIN" account import \
    --datadir "data/$node" \
    --password passwords.txt \
    --lightkdf \
    "$KEYFILE" 2>/dev/null || true
  rm -f "$KEYFILE"
  log "  $node: ${ACCOUNTS[$i]}"
done

# Generate node1 nodekey (bootnode → ID goes into genesis extraData)
log "Generating node1 nodekey..."
NODEKEY=$(openssl rand -hex 32)
# gmet uses DATADIR/gmet/nodekey at runtime; also write to geth/ for compatibility
echo "$NODEKEY" > data/node1/gmet/nodekey
echo "$NODEKEY" > data/node1/geth/nodekey
chmod 600 data/node1/gmet/nodekey data/node1/geth/nodekey

# Calculate node1's node ID using Python (secp256k1 public key 64 bytes = 128 hex chars)
NODE1_ID=$(python3 - <<PYEOF
from cryptography.hazmat.primitives.asymmetric import ec
from cryptography.hazmat.backends import default_backend
privkey = ec.derive_private_key(int("$NODEKEY", 16), ec.SECP256K1(), default_backend())
pub = privkey.public_key().public_numbers()
x = pub.x.to_bytes(32, 'big').hex()
y = pub.y.to_bytes(32, 'big').hex()
print(x + y)
PYEOF
)

ENODE="enode://${NODE1_ID}@172.31.0.11:30303"
log "node1 enode: $ENODE"

# Generate static-nodes.json (copy to both geth/ and gmet/ - gmet uses gmet/ at runtime)
cat > static-nodes.json <<EOF
[
  "$ENODE"
]
EOF
for node in node1 node2 node3; do
  cp static-nodes.json "data/$node/geth/static-nodes.json"
  cp static-nodes.json "data/$node/gmet/static-nodes.json"
done
log "static-nodes.json created"

# Generate PoA genesis.json
# extraData = "0x" + node1_id (128 hex chars) → bootnode identifier
log "Generating genesis.json (extraData=node1_id)..."
cat > genesis.json <<EOF
{
  "alloc": {
    "0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266": {
      "balance": "0x56BC75E2D63100000000"
    },
    "0x70997970C51812dc3A010C7d01b50e0d17dc79C8": {
      "balance": "0x56BC75E2D63100000000"
    },
    "0x3C44CdDdB6a900fa2b585dd299e03d12FA4293BC": {
      "balance": "0x56BC75E2D63100000000"
    }
  },
  "coinbase": "0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266",
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
    "camelliaBlock": 100
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
EOF
log "genesis.json created (bootNodeId=${NODE1_ID:0:16}...)"

# Copy password.txt to each node's data directory (needed for --unlock)
log "Copying password.txt to each node..."
for node in node1 node2 node3; do
  cp passwords.txt "data/$node/password.txt"
done

# Build Docker image
# BASE_IMAGE: go-metadium-rocksdb-test:latest (macOS), gmet-poa:latest (Linux)
log "Building Docker image (gmet-poa:latest)..."
cp "$GMET_BIN" ./gmet
if docker image inspect go-metadium-rocksdb-test:latest &>/dev/null; then
  BASE_IMAGE="go-metadium-rocksdb-test:latest"
else
  BASE_IMAGE="ubuntu:22.04"
fi
log "  Base image: $BASE_IMAGE"
docker build --build-arg BASE_IMAGE="$BASE_IMAGE" -t gmet-poa:latest . 2>&1 | tail -3
rm -f ./gmet
log "Image build complete"

log ""
log "=== PoA initialization complete ==="
log ""
log "Next step: ./start.sh"
log ""
log "Node RPC:"
log "  node1 (bootnode): http://localhost:8545"
log "  node2 (sync):     http://localhost:8546"
log "  node3 (sync):     http://localhost:8547"
log ""
log "Phase 1: node1 produces blocks (bootnode mining alone without governance)"
log "CamelliaFork activation: block 100 (approx 3-4 minutes)"
