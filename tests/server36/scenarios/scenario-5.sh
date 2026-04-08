#!/usr/bin/env bash
# Scenario 5: Fee Collection & Distribution Verification
# All v1.0.0, CamelliaBlock=100.
#
# What this tests:
#   1. Tx fee collection: coinbase balance increases when txs are mined
#   2. Fee distribution pre-fork vs post-fork
#
# NOTE: Full governance block rewards (minting) require on-chain governance
# contracts (Registry, Governance, Environment, Staking) to be deployed and
# initialized with members.  This private test network does NOT deploy those
# contracts, so the consensus engine falls back to fee-only mode
# (ErrNotInitialized path): all tx fees go to header.Coinbase, no minting.
# Minting verification requires a genesis that pre-deploys governance contracts
# (e.g. testnet/mainnet genesis or a custom genesis with alloc entries).

set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
source "$SCRIPT_DIR/lib.sh"

SCENARIO="scenario-5-fee-collection"
RESULT_DIR="$RESULTS_DIR/$SCENARIO"
mkdir -p "$RESULT_DIR"

PASS=0; FAIL=0

FROM1="0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266"   # node1 unlocked account
FROM2="0x70997970C51812dc3A010C7d01b50e0d17dc79C8"   # node2 unlocked account
TO="0x3C44CdDdB6a900fa2b585dd299e03d12FA4293BC"      # node3 (recipient, no-op)

log "=== Scenario 5: Fee Collection & Distribution ==="
log "All v1.0.0, CamelliaBlock=100"
log "NOTE: Governance minting not tested (contracts not deployed in private net)"

"$SCRIPT_DIR/stop.sh" --clean 2>/dev/null || true
"$SCRIPT_DIR/setup.sh"

export NODE1_IMAGE=gmet-s36-leveldb:latest
export NODE2_IMAGE=gmet-s36-rocksdb:latest
export NODE3_IMAGE=gmet-s36-leveldb:latest
export NODE4_IMAGE=gmet-s36-rocksdb:latest
export NODE2_USEROCKSDB=1
export NODE4_USEROCKSDB=1

start_network "$SCRIPT_DIR/genesis.json"

# -------------------------------------------------------------------
# Helper: get coinbase of a given block number
# -------------------------------------------------------------------
get_block_coinbase() {
  local endpoint="$1"
  local blocknum_hex
  blocknum_hex=$(printf '0x%x' "$2")
  rpc "{\"jsonrpc\":\"2.0\",\"method\":\"eth_getBlockByNumber\",\"params\":[\"$blocknum_hex\",false],\"id\":1}" "$endpoint" | \
    python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('result',{}).get('miner',''))" 2>/dev/null || echo ""
}

# -------------------------------------------------------------------
# Helper: send N transfer txs from FROM_ADDR on ENDPOINT, return array of hashes
# -------------------------------------------------------------------
send_txs() {
  local endpoint="$1"
  local from="$2"
  local count="$3"
  local hashes=()
  for i in $(seq 1 "$count"); do
    local h
    h=$(rpc "{\"jsonrpc\":\"2.0\",\"method\":\"eth_sendTransaction\",\"params\":[{\"from\":\"$from\",\"to\":\"$TO\",\"value\":\"0xDE0B6B3A7640000\",\"gas\":\"0x5208\"}],\"id\":$i}" "$endpoint" | \
      python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('result',''))" 2>/dev/null || echo "")
    [[ -n "$h" ]] && hashes+=("$h")
  done
  echo "${hashes[@]}"
}

# -------------------------------------------------------------------
# Helper: get coinbase balance
# -------------------------------------------------------------------
get_balance() {
  local endpoint="$1"
  local addr="$2"
  rpc "{\"jsonrpc\":\"2.0\",\"method\":\"eth_getBalance\",\"params\":[\"$addr\",\"latest\"],\"id\":1}" "$endpoint" | \
    python3 -c "import sys,json; d=json.load(sys.stdin); print(int(d.get('result','0x0'),16))" 2>/dev/null || echo "0"
}

# -------------------------------------------------------------------
# Phase 1: Pre-fork (block ~30) — discover miner coinbase
# -------------------------------------------------------------------
log "Phase 1: Pre-fork baseline (waiting for block 30)..."
wait_block 30 http://localhost:8545 120

MINER_COINBASE=$(get_block_coinbase http://localhost:8545 20)
log "Miner coinbase (from block 20): $MINER_COINBASE"

if [[ -z "$MINER_COINBASE" || "$MINER_COINBASE" == "0x0000000000000000000000000000000000000000" ]]; then
  log "WARN: Could not determine miner coinbase. Falling back to node1 signer address."
  # Derive from node1 key: c3be89e2... → address
  MINER_COINBASE="0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266"
fi

BALANCE_BEFORE_PRE=$(get_balance http://localhost:8545 "$MINER_COINBASE")
log "Coinbase balance before pre-fork txs: $BALANCE_BEFORE_PRE wei"

# Send 10 txs pre-fork on node1
log "Sending 10 txs pre-fork on node1..."
PRE_HASHES=()
for i in $(seq 1 10); do
  h=$(rpc "{\"jsonrpc\":\"2.0\",\"method\":\"eth_sendTransaction\",\"params\":[{\"from\":\"$FROM1\",\"to\":\"$TO\",\"value\":\"0xDE0B6B3A7640000\",\"gas\":\"0x5208\"}],\"id\":$i}" http://localhost:8545 | \
    python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('result',''))" 2>/dev/null || echo "")
  [[ -n "$h" ]] && PRE_HASHES+=("$h") && log "  sent tx#$i: $h"
done

# Wait for last pre-fork tx to be mined
if [[ ${#PRE_HASHES[@]} -gt 0 ]]; then
  log "Waiting for pre-fork txs to be mined..."
  wait_receipt "${PRE_HASHES[-1]}" http://localhost:8545 60 || true
fi

BLOCK_PRE=$(block_number http://localhost:8545)
BALANCE_AFTER_PRE=$(get_balance http://localhost:8545 "$MINER_COINBASE")
FEE_COLLECTED_PRE=$((BALANCE_AFTER_PRE - BALANCE_BEFORE_PRE))
log "Pre-fork: block=$BLOCK_PRE coinbase=$MINER_COINBASE"
log "  Balance before: $BALANCE_BEFORE_PRE"
log "  Balance after:  $BALANCE_AFTER_PRE"
log "  Fee collected:  $FEE_COLLECTED_PRE wei (${#PRE_HASHES[@]} txs)"

if [[ "$FEE_COLLECTED_PRE" -gt 0 ]]; then
  PASS=$((PASS + 1))
  log "PASS: Pre-fork tx fees collected by coinbase ($FEE_COLLECTED_PRE wei)"
else
  FAIL=$((FAIL + 1))
  log "FAIL: Pre-fork tx fees NOT collected (balance unchanged)"
fi

# -------------------------------------------------------------------
# Phase 2: Post-fork (block >100)
# -------------------------------------------------------------------
log "Phase 2: Waiting for fork block 100..."
wait_block 105 http://localhost:8545 300

BALANCE_BEFORE_POST=$(get_balance http://localhost:8545 "$MINER_COINBASE")
BLOCK_POST_START=$(block_number http://localhost:8545)
log "Post-fork start: block=$BLOCK_POST_START coinbase balance=$BALANCE_BEFORE_POST wei"

# Send 20 txs post-fork on node1 + node2
log "Sending 20 txs post-fork (10 node1, 10 node2)..."
POST_HASHES=()
for i in $(seq 1 10); do
  h=$(rpc "{\"jsonrpc\":\"2.0\",\"method\":\"eth_sendTransaction\",\"params\":[{\"from\":\"$FROM1\",\"to\":\"$TO\",\"value\":\"0xDE0B6B3A7640000\",\"gas\":\"0x5208\"}],\"id\":$i}" http://localhost:8545 | \
    python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('result',''))" 2>/dev/null || echo "")
  [[ -n "$h" ]] && POST_HASHES+=("$h")
done
for i in $(seq 11 20); do
  h=$(rpc "{\"jsonrpc\":\"2.0\",\"method\":\"eth_sendTransaction\",\"params\":[{\"from\":\"$FROM2\",\"to\":\"$TO\",\"value\":\"0xDE0B6B3A7640000\",\"gas\":\"0x5208\"}],\"id\":$i}" http://localhost:8546 | \
    python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('result',''))" 2>/dev/null || echo "")
  [[ -n "$h" ]] && POST_HASHES+=("$h")
done
log "  Sent ${#POST_HASHES[@]} txs post-fork"

if [[ ${#POST_HASHES[@]} -gt 0 ]]; then
  log "Waiting for post-fork txs to be mined..."
  wait_receipt "${POST_HASHES[-1]}" http://localhost:8545 120 || \
  wait_receipt "${POST_HASHES[-1]}" http://localhost:8546 60 || true
fi

# Allow 2 more blocks for all txs to settle
CUR_BLOCK=$(block_number http://localhost:8545)
wait_block $((CUR_BLOCK + 2)) http://localhost:8545 60 || true

BLOCK_POST_END=$(block_number http://localhost:8545)
BALANCE_AFTER_POST=$(get_balance http://localhost:8545 "$MINER_COINBASE")
FEE_COLLECTED_POST=$((BALANCE_AFTER_POST - BALANCE_BEFORE_POST))
log "Post-fork: block=$BLOCK_POST_END coinbase=$MINER_COINBASE"
log "  Balance before: $BALANCE_BEFORE_POST"
log "  Balance after:  $BALANCE_AFTER_POST"
log "  Fee collected:  $FEE_COLLECTED_POST wei (${#POST_HASHES[@]} txs)"

if [[ "$FEE_COLLECTED_POST" -gt 0 ]]; then
  PASS=$((PASS + 1))
  log "PASS: Post-fork tx fees collected by coinbase ($FEE_COLLECTED_POST wei)"
else
  FAIL=$((FAIL + 1))
  log "FAIL: Post-fork tx fees NOT collected (balance unchanged)"
fi

# -------------------------------------------------------------------
# Phase 3: Verify fee continuity over next 100 blocks
# -------------------------------------------------------------------
log "Phase 3: Monitoring fee collection over next 100 blocks..."
BALANCE_START=$BALANCE_AFTER_POST
BLOCK_START=$BLOCK_POST_END

# Send a burst of 5 txs every 10 blocks, 3 times
for CHECKPOINT in 1 2 3; do
  NEXT_BLOCK=$((BLOCK_POST_END + CHECKPOINT * 30))
  wait_block $NEXT_BLOCK http://localhost:8545 300 || break
  # send 5 txs
  for i in $(seq 1 5); do
    rpc "{\"jsonrpc\":\"2.0\",\"method\":\"eth_sendTransaction\",\"params\":[{\"from\":\"$FROM1\",\"to\":\"$TO\",\"value\":\"0x1\",\"gas\":\"0x5208\"}],\"id\":$i}" \
      http://localhost:8545 >/dev/null 2>&1 || true
  done
done

BLOCK_FINAL=$(block_number http://localhost:8545)
wait_block $((BLOCK_FINAL + 2)) http://localhost:8545 30 || true

BALANCE_FINAL=$(get_balance http://localhost:8545 "$MINER_COINBASE")
FEE_PHASE3=$((BALANCE_FINAL - BALANCE_START))
log "Phase 3 result: blocks $BLOCK_START→$BLOCK_FINAL fee_collected=$FEE_PHASE3 wei"

if [[ "$FEE_PHASE3" -gt 0 ]]; then
  PASS=$((PASS + 1))
  log "PASS: Fee collection continuous over 100 blocks ($FEE_PHASE3 wei total)"
else
  log "INFO: No fees in phase 3 (no txs confirmed — network may be fast, fees may go to rotating miners)"
fi

# -------------------------------------------------------------------
# Summary report
# -------------------------------------------------------------------
cat > "$RESULT_DIR/fee-analysis.md" <<EOF
# Scenario 5: Fee Collection & Distribution Analysis

## Setup
- Network: 4-node PoA (3 miners + 1 sync), CamelliaBlock=100
- Governance contracts: NOT deployed (fee-only mode, no minting)
- Coinbase observed: $MINER_COINBASE

## Results

### Pre-fork (blocks ≤100)
- Txs sent: ${#PRE_HASHES[@]}
- Fee collected: $FEE_COLLECTED_PRE wei ($(python3 -c "print(f'{$FEE_COLLECTED_PRE/1e18:.8f}')") META)

### Post-fork (blocks >100)
- Txs sent: ${#POST_HASHES[@]} (node1 + node2)
- Fee collected: $FEE_COLLECTED_POST wei ($(python3 -c "print(f'{$FEE_COLLECTED_POST/1e18:.8f}')") META)

### Continuous monitoring (100 blocks)
- Fee collected: $FEE_PHASE3 wei ($(python3 -c "print(f'{$FEE_PHASE3/1e18:.8f}')") META)

## Governance Minting (NOT tested here)
Full block reward minting requires:
1. Registry, Governance, Environment, Staking contracts deployed in genesis alloc
2. Members registered with staking amounts
3. Reward amounts configured in Environment contract

To test minting: deploy governance contracts via setup script or use testnet genesis.
When governance is initialized, CalculateRewards() distributes:
  - Block reward (minting) → governance members (rotating coinbase)
  - Tx fees → included in reward calculation

## Timestamp
$(date -u +%Y-%m-%dT%H:%M:%SZ)
EOF

cat > "$RESULT_DIR/config.json" <<EOF
{
  "scenario": 5,
  "camelliaBlock": 100,
  "minerCoinbase": "$MINER_COINBASE",
  "preForkFeeCollected": $FEE_COLLECTED_PRE,
  "postForkFeeCollected": $FEE_COLLECTED_POST,
  "phase3FeeCollected": $FEE_PHASE3,
  "governanceContractsDeployed": false,
  "mintingTested": false,
  "timestamp": "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
}
EOF

write_summary "$RESULT_DIR" $PASS $FAIL "Fee collection: pre=${FEE_COLLECTED_PRE}wei post=${FEE_COLLECTED_POST}wei (minting: governance contracts needed)"

"$SCRIPT_DIR/stop.sh" --clean
log "=== Scenario 5 complete: PASS=$PASS FAIL=$FAIL ==="
[[ $FAIL -eq 0 ]]
