#!/usr/bin/env bash
# setup.sh - One-time initialization for server36 4-node test network
#
# Prerequisites:
#   - ~/go-metadium/gmet-old      (v0.10.2, statically linked)
#   - ~/go-metadium/gmet-leveldb  (v1.0.0, statically linked)
#   - ~/go-metadium/gmet-rocksdb  (v1.0.0, dynamically linked)
#   - Docker available
#
# Usage: ./setup.sh [--rebuild-images]

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

GMET_LEVELDB="${GMET_LEVELDB:-$HOME/go-metadium/gmet-leveldb}"
GMET_ROCKSDB="${GMET_ROCKSDB:-$HOME/go-metadium/gmet-rocksdb}"
GMET_OLD="${GMET_OLD:-$HOME/go-metadium/gmet-old}"
REBUILD_IMAGES="${REBUILD_IMAGES:-0}"
PASSWORD="privatenet123"

log()  { echo "[$(date '+%H:%M:%S')] $*"; }
err()  { echo "[ERROR] $*" >&2; exit 1; }

[[ "${1:-}" == "--rebuild-images" ]] && REBUILD_IMAGES=1

# Validate binaries
[[ -x "$GMET_LEVELDB" ]] || err "gmet-leveldb not found: $GMET_LEVELDB"
[[ -x "$GMET_ROCKSDB" ]] || err "gmet-rocksdb not found: $GMET_ROCKSDB"
[[ -x "$GMET_OLD" ]]     || err "gmet-old not found: $GMET_OLD"

log "=== Server36 4-node network setup ==="
log "  gmet-leveldb: $GMET_LEVELDB"
log "  gmet-rocksdb: $GMET_ROCKSDB"
log "  gmet-old:     $GMET_OLD"

# Fixed nodekeys (pre-generated from secp256k1 private keys)
# These correspond to the enode IDs in static-nodes.json and genesis.json extraData
NODE1_KEY="c3be89e2ec49a46b65cc968bb3f4f3a2b6159510c9c2bedd9541ffe8ca9f305f"
NODE2_KEY="238c26adf5080ee1ace1601f93a8b82ca418f4c438643996c974c35a00ba12be"
NODE3_KEY="61b6e0ae2520c567bc459a1d205b749b1dbd7805330da7ad93a73620f25171dc"
NODE4_KEY="b740bc44bf09483caa56c25b1b99625a48b11482f9e9e660d6208b50f92819a3"

# Hardhat test account private keys (accounts 0-3)
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
NODES=(node1 node2 node3 node4)
NODEKEYS=("$NODE1_KEY" "$NODE2_KEY" "$NODE3_KEY" "$NODE4_KEY")

# Create data directories
log "Creating data directories..."
for node in node1 node2 node3 node4; do
  mkdir -p "data/$node/gmet" "data/$node/geth"
done

# Write fixed nodekeys
log "Writing nodekeys..."
for i in 0 1 2 3; do
  node="${NODES[$i]}"
  key="${NODEKEYS[$i]}"
  echo "$key" > "data/$node/gmet/nodekey"
  echo "$key" > "data/$node/geth/nodekey"
  chmod 600 "data/$node/gmet/nodekey" "data/$node/geth/nodekey"
  log "  $node: nodekey written"
done

# Write password file
log "Writing password files..."
echo "$PASSWORD" > passwords.txt
for node in node1 node2 node3 node4; do
  cp passwords.txt "data/$node/password.txt"
done

# Copy static-nodes.json to all node directories
log "Distributing static-nodes.json..."
for node in node1 node2 node3 node4; do
  cp static-nodes.json "data/$node/gmet/static-nodes.json"
  cp static-nodes.json "data/$node/geth/static-nodes.json"
done

# Import account keystores using gmet-leveldb (statically linked, runs on host)
log "Importing account keystores (using gmet-leveldb on host)..."
for i in 0 1 2 3; do
  node="${NODES[$i]}"
  # Skip if keystore already exists
  if ls "data/$node/keystore/UTC--"* &>/dev/null 2>&1; then
    log "  $node: keystore already exists, skipping"
    continue
  fi
  KEYFILE=$(mktemp /tmp/privkey.XXXXXX)
  echo "${PRIVKEYS[$i]}" > "$KEYFILE"
  "$GMET_LEVELDB" account import \
    --datadir "data/$node" \
    --password passwords.txt \
    --lightkdf \
    "$KEYFILE" 2>/dev/null || true
  rm -f "$KEYFILE"
  log "  $node: ${ACCOUNTS[$i]} imported"
done

# Build Docker images
if [[ "$REBUILD_IMAGES" == "1" ]] || \
   ! docker image inspect gmet-s36-leveldb:latest &>/dev/null || \
   ! docker image inspect gmet-s36-rocksdb:latest &>/dev/null || \
   ! docker image inspect gmet-s36-old:latest &>/dev/null; then

  log "Building Docker images..."

  # gmet-s36-leveldb
  log "  Building gmet-s36-leveldb:latest..."
  cp "$GMET_LEVELDB" ./gmet
  docker build -f Dockerfile.leveldb -t gmet-s36-leveldb:latest . 2>&1 | tail -3
  rm -f ./gmet

  # gmet-s36-rocksdb
  log "  Building gmet-s36-rocksdb:latest..."
  cp "$GMET_ROCKSDB" ./gmet
  docker build -f Dockerfile.rocksdb -t gmet-s36-rocksdb:latest . 2>&1 | tail -3
  rm -f ./gmet

  # gmet-s36-old
  log "  Building gmet-s36-old:latest..."
  cp "$GMET_OLD" ./gmet
  docker build -f Dockerfile.old -t gmet-s36-old:latest . 2>&1 | tail -3
  rm -f ./gmet

  # Verify images work
  log "Verifying images..."
  docker run --rm gmet-s36-leveldb:latest gmet version 2>&1 | grep "Version:" | head -1
  docker run --rm gmet-s36-rocksdb:latest gmet version 2>&1 | grep "Version:" | head -1
  docker run --rm gmet-s36-old:latest gmet version 2>&1 | grep "Version:" | head -1
else
  log "Docker images already exist (use --rebuild-images to force rebuild)"
fi

log ""
log "=== Setup complete ==="
log ""
log "Next steps:"
log "  Run individual scenarios: ./scenarios/scenario-1.sh"
log "  Run all scenarios:        ./run-all.sh"
log "  Run from scenario N:      ./run-all.sh --from N"
