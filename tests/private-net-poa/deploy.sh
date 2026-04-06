#!/usr/bin/env bash
# deploy.sh - Phase 2: Deploy governance contract
#
# Prerequisites:
#   - setup.sh and start.sh completed (3 nodes running)
#   - node1 producing blocks as bootnode
#
# Usage: ./deploy.sh
# Options: GMET_BIN=/path/to/gmet ./deploy.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

GMET_BIN="${GMET_BIN:-$SCRIPT_DIR/../../geth}"
GOVERNANCE_JS="${GOVERNANCE_JS:-$SCRIPT_DIR/../../metadium/contracts/MetadiumGovernance.js}"
DEPLOY_JS="${DEPLOY_JS:-$SCRIPT_DIR/../../metadium/scripts/deploy-governance.js}"
NODE1_RPC="http://localhost:8545"
CHAINID=1337
PASSWORD="privatenet123"

log()  { echo "[$(date '+%H:%M:%S')] $*"; }
err()  { echo "[ERROR] $*" >&2; exit 1; }

[[ -x "$GMET_BIN" ]] || err "gmet binary not found: $GMET_BIN"
[[ -f "$GOVERNANCE_JS" ]] || err "MetadiumGovernance.js not found: $GOVERNANCE_JS"
[[ -f "$DEPLOY_JS" ]] || err "deploy-governance.js not found: $DEPLOY_JS"
[[ -f "genesis.json" ]] || err "genesis.json not found. Please run setup.sh first."
[[ -f "data/node1/keystore" || -d "data/node1/keystore" ]] || err "keystore not found. Please run setup.sh first."

log "=== Phase 2: Deploying governance contract ==="

# Check node1 RPC
BLOCK=$(curl -sf -X POST -H "Content-Type: application/json" \
  --data '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}' \
  "$NODE1_RPC" 2>/dev/null | \
  python3 -c "import sys,json; d=json.load(sys.stdin); print(int(d['result'],16))" 2>/dev/null || echo "0")
[[ "$BLOCK" -gt 0 ]] || err "node1 RPC not responding. Please run start.sh first."
log "Current block: $BLOCK"

# Collect enode ID from each node (128-char public key format)
log "Collecting node IDs..."
get_enode_id() {
  local port="$1"
  curl -sf -X POST -H "Content-Type: application/json" \
    --data '{"jsonrpc":"2.0","method":"admin_nodeInfo","params":[],"id":1}' \
    "http://localhost:${port}" 2>/dev/null | \
    python3 -c "import sys,json,re; d=json.load(sys.stdin); m=re.match(r'enode://([a-f0-9]{128})@', d['result']['enode']); print(m.group(1) if m else '')" 2>/dev/null
}

NODE1_ID=$(get_enode_id 8545)
NODE2_ID=$(get_enode_id 8546)
NODE3_ID=$(get_enode_id 8547)

[[ -n "$NODE1_ID" && ${#NODE1_ID} -eq 128 ]] || err "Failed to get node1 ID"
[[ -n "$NODE2_ID" && ${#NODE2_ID} -eq 128 ]] || err "Failed to get node2 ID"
[[ -n "$NODE3_ID" && ${#NODE3_ID} -eq 128 ]] || err "Failed to get node3 ID"

log "  node1: ${NODE1_ID:0:16}..."
log "  node2: ${NODE2_ID:0:16}..."
log "  node3: ${NODE3_ID:0:16}..."

# Find keystore file
KEYSTORE_FILE=$(ls data/node1/keystore/UTC--* 2>/dev/null | head -1)
[[ -n "$KEYSTORE_FILE" ]] || err "keystore file not found"
KEYSTORE_FILE="$SCRIPT_DIR/$KEYSTORE_FILE"
log "keystore: $(basename "$KEYSTORE_FILE")"

# Generate config.json
log "Generating config.json..."
cat > config.json <<EOF
{
  "staker":      "0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266",
  "ecosystem":   "0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266",
  "maintenance": "0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266",
  "feecollector":"0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266",
  "env": {
    "ballotDurationMin":      60,
    "ballotDurationMax":      604800,
    "stakingMin":             1000000000000000000,
    "stakingMax":             100000000000000000000000000,
    "MaxIdleBlockInterval":   5,
    "blockCreationTime":      2000,
    "blockRewardAmount":      1000000000000000000,
    "maxPriorityFeePerGas":   80000000000,
    "rewardDistributionMethod": [4000, 1000, 2500, 2500],
    "maxBaseFee":             50000000000000,
    "blockGasLimit":          268435456,
    "baseFeeMaxChangeRate":   55,
    "gasTargetPercentage":    30
  },
  "members": [
    {
      "addr":     "0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266",
      "stake":    1000000000000000000,
      "name":     "node1",
      "id":       "${NODE1_ID}",
      "ip":       "172.31.0.11",
      "port":     30303,
      "bootnode": true
    },
    {
      "addr":   "0x70997970C51812dc3A010C7d01b50e0d17dc79C8",
      "stake":  1000000000000000000,
      "name":   "node2",
      "id":     "${NODE2_ID}",
      "ip":     "172.31.0.12",
      "port":   30304
    },
    {
      "addr":   "0x3C44CdDdB6a900fa2b585dd299e03d12FA4293BC",
      "stake":  1000000000000000000,
      "name":   "node3",
      "id":     "${NODE3_ID}",
      "ip":     "172.31.0.13",
      "port":   30305
    }
  ],
  "accounts": [
    {"addr": "0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266", "balance": 0},
    {"addr": "0x70997970C51812dc3A010C7d01b50e0d17dc79C8", "balance": 0},
    {"addr": "0x3C44CdDdB6a900fa2b585dd299e03d12FA4293BC", "balance": 0}
  ]
}
EOF
log "config.json created"

# Deploy governance
log ""
log "Starting governance contract deployment..."
log "(Registry → EnvStorageImp → Staking → BallotStorage → EnvStorage → GovImp → Gov → TRSList)"
log ""

DEPLOY_LOG="$SCRIPT_DIR/data/deploy.log"
mkdir -p "$SCRIPT_DIR/data"

"$GMET_BIN" attach "$NODE1_RPC" \
  --preload "${GOVERNANCE_JS},${DEPLOY_JS}" \
  --exec "GovernanceDeployer.deploy(\"${KEYSTORE_FILE}\", \"${PASSWORD}\", \"${SCRIPT_DIR}/config.json\", true)" \
  2>&1 | tee "$DEPLOY_LOG"

DEPLOY_EXIT=${PIPESTATUS[0]}
if [ $DEPLOY_EXIT -ne 0 ]; then
  log "[WARN] Deployment failed (exit=$DEPLOY_EXIT). Please check the logs."
  exit $DEPLOY_EXIT
fi

# Extract and save deployed contract addresses from log output
ADDRS_FILE="$SCRIPT_DIR/data/deployed-addrs.json"
python3 - <<PYEOF > "$ADDRS_FILE" 2>/dev/null || true
import re, json, sys
with open("$DEPLOY_LOG") as f:
    text = f.read()
# Find the JSON block printed by deploy-governance.js (starts with { and contains REGISTRY_ADDRESS)
m = re.search(r'\{[^{}]*"REGISTRY_ADDRESS"[^{}]*\}', text, re.DOTALL)
if m:
    raw = m.group(0)
    # Fix missing commas: add comma after lines ending with " but not followed by , or }
    raw = re.sub(r'("\s*)\n(\s*")', r'",\n\2', raw)
    # Remove trailing comma before closing brace
    raw = re.sub(r',(\s*\})', r'\1', raw)
    try:
        d = json.loads(raw)
        print(json.dumps(d, indent=2))
    except Exception as e:
        # fallback: extract key=value pairs manually
        pairs = re.findall(r'"([A-Z_]+)"\s*:\s*"(0x[0-9a-fA-F]+)"', raw)
        print(json.dumps(dict(pairs), indent=2))
else:
    print("{}")
PYEOF

if [ -s "$ADDRS_FILE" ] && [ "$(cat "$ADDRS_FILE")" != "{}" ]; then
  log "Contract addresses saved to $ADDRS_FILE"
  cat "$ADDRS_FILE"
else
  log "[WARN] Could not extract contract addresses from deploy log"
fi

log ""
log "=== Governance deployment complete ==="
log ""

# Initialize ETCD cluster
# Wait up to 10s for admin.self to be set after governance detection, then call admin_etcdInit
log "Initializing ETCD cluster (node1)..."
ETCD_OK=false
for i in $(seq 1 10); do
  RESULT=$(curl -sf -X POST -H "Content-Type: application/json" \
    --data '{"jsonrpc":"2.0","method":"admin_etcdInit","params":[],"id":1}' \
    "$NODE1_RPC" 2>/dev/null | \
    python3 -c "import sys,json; d=json.load(sys.stdin); print('ok' if 'error' not in d else d['error']['message'])" 2>/dev/null || echo "fail")
  if [ "$RESULT" = "ok" ]; then
    log "  node1 ETCD init succeeded (attempt $i)"
    ETCD_OK=true
    break
  fi
  log "  Attempt $i: $RESULT"
  sleep 3
done
$ETCD_OK || err "node1 ETCD init failed. Manually call 'admin_etcdInit' RPC."

# Wait for node1 ETCD to be ready
sleep 3

# NOTE: node2/node3 are NOT added to ETCD cluster in private-net setup.
# Adding them creates a 2/2 quorum requirement before node2's ETCD starts,
# causing mining to halt. In production, nodes join ETCD via etcdAutoJoin
# after their ETCD configs are properly synchronized.
# For private-net: node1 runs as single-member ETCD, all 3 nodes are
# governance members and can mine via the token passed through node1.
log "ETCD: node1 initialized as single-member cluster (node2/node3 join via etcdAutoJoin)"

# Final status check
sleep 5
log ""
log "=== Final node status ==="
for port in 8545 8546 8547; do
  BLOCK=$(curl -sf -X POST -H "Content-Type: application/json" \
    --data '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}' \
    "http://localhost:$port" 2>/dev/null | \
    python3 -c "import sys,json; d=json.load(sys.stdin); print(int(d['result'],16))" 2>/dev/null || echo "?")
  MINING=$(curl -sf -X POST -H "Content-Type: application/json" \
    --data '{"jsonrpc":"2.0","method":"eth_mining","params":[],"id":1}' \
    "http://localhost:$port" 2>/dev/null | \
    python3 -c "import sys,json; d=json.load(sys.stdin); print(d['result'])" 2>/dev/null || echo "?")
  log "  :$port  block=$BLOCK  mining=$MINING"
done
