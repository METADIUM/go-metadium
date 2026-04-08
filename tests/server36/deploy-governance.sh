#!/usr/bin/env bash
# deploy-governance.sh
# Deploy Metadium governance contracts to the private PoA network.
# Enables block reward minting (1 META/block) with 3-miner governance.
#
# IMPORTANT: Run IMMEDIATELY after start_network, BEFORE any transactions
# from the deployer account (0xf39F...), so that Registry lands at nonce=0.
#
# Usage:
#   cd tests/server36
#   bash deploy-governance.sh [--rpc http://localhost:8545] [--clean]
#
# --clean: start a fresh network before deploying
# --rpc:   override RPC endpoint (default: http://localhost:8545)

set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/lib.sh"

GOV_DIR="${GOV_DIR:-/tmp/gov-contract}"
RPC_URL="${RPC_URL:-http://localhost:8545}"
CHAIN_ID="${CHAIN_ID:-1338}"
DO_CLEAN=0

while [[ $# -gt 0 ]]; do
  case "$1" in
    --rpc)   RPC_URL="$2"; shift 2 ;;
    --clean) DO_CLEAN=1; shift ;;
    *) shift ;;
  esac
done

log "=== Governance Contract Deployment ==="
log "GOV_DIR: $GOV_DIR"
log "RPC_URL: $RPC_URL"

# ── Step 0: Optionally start fresh network ────────────────────────────────────
if [[ $DO_CLEAN -eq 1 ]]; then
  log "Starting fresh network (--clean)..."
  "$SCRIPT_DIR/stop.sh" --clean 2>/dev/null || true
  "$SCRIPT_DIR/setup.sh"
  export NODE1_IMAGE=gmet-s36-leveldb:latest
  export NODE2_IMAGE=gmet-s36-rocksdb:latest
  export NODE3_IMAGE=gmet-s36-leveldb:latest
  export NODE4_IMAGE=gmet-s36-rocksdb:latest
  export NODE2_USEROCKSDB=1
  export NODE4_USEROCKSDB=1
  start_network "$SCRIPT_DIR/genesis.json"
  log "Network started. Deploying governance immediately (nonce=0)..."
fi

# ── Step 1: Clone/update governance-contract repo ────────────────────────────
if [[ ! -d "$GOV_DIR" ]]; then
  log "Cloning governance-contract..."
  git clone https://github.com/METADIUM/governance-contract.git "$GOV_DIR"
else
  log "governance-contract already at $GOV_DIR (skipping clone)"
fi

# ── Step 2: Install npm deps and compile ─────────────────────────────────────
log "Installing npm dependencies..."
cd "$GOV_DIR"
npm install --quiet 2>/dev/null

log "Compiling contracts (solc 0.8.6)..."
npx hardhat compile --quiet 2>/dev/null || npx hardhat compile

# Verify artifacts exist
REGISTRY_ART="$GOV_DIR/artifacts/contracts/Registry.sol/Registry.json"
if [[ ! -f "$REGISTRY_ART" ]]; then
  log "ERROR: Compile failed — Registry artifact not found at $REGISTRY_ART"
  exit 1
fi
log "Compilation successful."

# ── Step 3: Install ethers in governance deploy dir ──────────────────────────
cd "$SCRIPT_DIR/governance"
if [[ ! -d node_modules ]]; then
  log "Installing ethers.js..."
  npm install --quiet 2>/dev/null
fi

# ── Step 4: Check deployer nonce ─────────────────────────────────────────────
NONCE=$(curl -sf -X POST -H "Content-Type: application/json" \
  --data '{"jsonrpc":"2.0","method":"eth_getTransactionCount","params":["0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266","latest"],"id":1}' \
  "$RPC_URL" | python3 -c "import sys,json; print(int(json.load(sys.stdin)['result'],16))" 2>/dev/null || echo "?")

log "Deployer (0xf39F...) current nonce: $NONCE"
if [[ "$NONCE" =~ ^[0-9]+$ && "$NONCE" -gt 9 ]]; then
  log "ERROR: Nonce=$NONCE > 9. gmet will not find Registry."
  log "Start a fresh network with: bash deploy-governance.sh --clean"
  exit 1
fi

# ── Step 5: Deploy ────────────────────────────────────────────────────────────
log "Deploying governance contracts..."
GOV_DIR="$GOV_DIR" RPC_URL="$RPC_URL" CHAIN_ID="$CHAIN_ID" \
  node "$SCRIPT_DIR/governance/deploy.js"

# ── Step 6: Verify minting ────────────────────────────────────────────────────
log "Waiting for gmet to detect governance and start minting..."
COINBASE=$(curl -sf -X POST -H "Content-Type: application/json" \
  --data '{"jsonrpc":"2.0","method":"eth_coinbase","params":[],"id":1}' \
  "$RPC_URL" | python3 -c "import sys,json; print(json.load(sys.stdin).get('result','?'))" 2>/dev/null || echo "?")
log "Current coinbase: $COINBASE"

# Wait 2 blocks and check if coinbase balance increased
ADDR="0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266"
BAL_BEFORE=$(curl -sf -X POST -H "Content-Type: application/json" \
  --data "{\"jsonrpc\":\"2.0\",\"method\":\"eth_getBalance\",\"params\":[\"$ADDR\",\"latest\"],\"id\":1}" \
  "$RPC_URL" | python3 -c "import sys,json; print(int(json.load(sys.stdin).get('result','0x0'),16))" 2>/dev/null || echo "0")

log "Waiting 8 seconds for 3+ blocks..."
sleep 8

BAL_AFTER=$(curl -sf -X POST -H "Content-Type: application/json" \
  --data "{\"jsonrpc\":\"2.0\",\"method\":\"eth_getBalance\",\"params\":[\"$ADDR\",\"latest\"],\"id\":1}" \
  "$RPC_URL" | python3 -c "import sys,json; print(int(json.load(sys.stdin).get('result','0x0'),16))" 2>/dev/null || echo "0")

DELTA=$((BAL_AFTER - BAL_BEFORE))
if [[ $DELTA -gt 0 ]]; then
  log "PASS: Minting confirmed! Balance increased by $DELTA wei ($(python3 -c "print(f'{$DELTA/1e18:.4f}') ") META)"
else
  log "INFO: Balance unchanged after 8s — gmet may need another block or 2 to detect Registry."
  log "Monitor: watch balance of $ADDR on $RPC_URL"
fi

log "=== Governance deployment complete ==="
if [[ -f "$SCRIPT_DIR/governance/deployed-contracts.json" ]]; then
  cat "$SCRIPT_DIR/governance/deployed-contracts.json"
fi
