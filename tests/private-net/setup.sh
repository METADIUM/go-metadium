#!/usr/bin/env bash
# setup.sh - go-metadium private 3-node network initialization
# Usage: ./setup.sh
# Options: GMET_BIN=/path/to/gmet ./setup.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

GMET_BIN="${GMET_BIN:-/path/to/gmet}"
USE_ROCKSDB="${USE_ROCKSDB:-1}"
NETWORKID=1337
PASSWORD="privatenet123"

log()  { echo "[$(date '+%H:%M:%S')] $*"; }
err()  { echo "[ERROR] $*" >&2; exit 1; }

[[ -x "$GMET_BIN" ]] || err "gmet binary not found: $GMET_BIN"

log "=== Private network initialization started (chainId=$NETWORKID, CamelliaFork=100) ==="

# Clean up existing data
log "Cleaning up existing data..."
rm -rf data/ passwords.txt static-nodes.json
mkdir -p data/node1/geth data/node2/geth data/node3/geth
echo "$PASSWORD" > passwords.txt

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

# Generate node1 nodekey (for enode calculation - without running gmet)
log "Generating node1 nodekey..."
NODEKEY=$(openssl rand -hex 32)
echo "$NODEKEY" > data/node1/geth/nodekey
chmod 600 data/node1/geth/nodekey

# Calculate node1 enode using Python (no need to run gmet)
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

ENODE="enode://${NODE1_ID}@172.30.0.11:30303"
log "node1 enode: $ENODE"

# Generate static-nodes.json
cat > static-nodes.json <<EOF
[
  "$ENODE"
]
EOF
log "static-nodes.json created"

# Build Docker image
log "Building Docker image (gmet-private:latest)..."
cp "$GMET_BIN" ./gmet
docker build -t gmet-private:latest . 2>&1 | tail -3
rm -f ./gmet
log "Image build complete"

log ""
log "=== Initialization complete ==="
log ""
log "Next step: ./start.sh"
log ""
log "Node RPC:"
log "  node1: http://localhost:8545"
log "  node2: http://localhost:8546"
log "  node3: http://localhost:8547"
log "CamelliaFork activation: block 100"
