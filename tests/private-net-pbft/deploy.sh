#!/usr/bin/env bash
# deploy.sh - deploy governance with the four nodes as members. Must finish
# before bftBlock: the PBFT validator set is read from it (design §9.2).
#
# Options: GMET_BIN=/path/to/gmet
#          BLOCK_CREATION_TIME=1000 (ms; must not exceed bft.emptyBlockInterval)
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"
source ./lib.sh

GMET_BIN="${GMET_BIN:-$SCRIPT_DIR/../../build/bin/gmet}"
GOVERNANCE_JS="$SCRIPT_DIR/../../metadium/contracts/MetadiumGovernance.js"
DEPLOY_JS="$SCRIPT_DIR/../../metadium/scripts/deploy-governance.js"
PASSWORD="privatenet123"
[[ -x "$GMET_BIN" ]] || err "gmet binary not found: $GMET_BIN"

BFT_BLOCK=$(python3 -c "import json; print(json.load(open('genesis.json'))['config']['bftBlock'])")
HEAD=$(block_number 8645) || err "node1 RPC not responding; run start.sh first"
(( HEAD + 20 < BFT_BLOCK )) || err "head $HEAD is too close to bftBlock $BFT_BLOCK; start over with a later BFT_BLOCK"
log "=== Deploying governance at block $HEAD (bftBlock $BFT_BLOCK) ==="

id_of() { awk -v n="node$1" '$1==n{print $2}' node-ids.txt; }
ACCOUNTS=(0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266 0x70997970C51812dc3A010C7d01b50e0d17dc79C8
  0x3C44CdDdB6a900fa2b585dd299e03d12FA4293BC 0x90F79bf6EB2c4f870365E785982E1f101E93b906)
members=""
for n in 1 2 3 4; do
  boot=""; [[ $n == 1 ]] && boot=', "bootnode": true'
  members+="$( [[ $n == 1 ]] || echo ,)
    {\"addr\": \"${ACCOUNTS[$((n - 1))]}\", \"stake\": 1000000000000000000, \"name\": \"node$n\",
     \"id\": \"$(id_of $n)\", \"ip\": \"172.32.0.1$n\", \"port\": 30303$boot}"
done
cat > config.json <<CFG
{
  "staker":      "${ACCOUNTS[0]}",
  "ecosystem":   "${ACCOUNTS[0]}",
  "maintenance": "${ACCOUNTS[0]}",
  "feecollector":"${ACCOUNTS[0]}",
  "env": {
    "ballotDurationMin":      60,
    "ballotDurationMax":      604800,
    "stakingMin":             1000000000000000000,
    "stakingMax":             100000000000000000000000000,
    "MaxIdleBlockInterval":   5,
    "blockCreationTime":      ${BLOCK_CREATION_TIME:-1000},
    "blockRewardAmount":      1000000000000000000,
    "maxPriorityFeePerGas":   80000000000,
    "rewardDistributionMethod": [4000, 1000, 2500, 2500],
    "maxBaseFee":             50000000000000,
    "blockGasLimit":          268435456,
    "baseFeeMaxChangeRate":   55,
    "gasTargetPercentage":    30
  },
  "members": [$members
  ],
  "accounts": [
    {"addr": "${ACCOUNTS[0]}", "balance": 0}, {"addr": "${ACCOUNTS[1]}", "balance": 0},
    {"addr": "${ACCOUNTS[2]}", "balance": 0}, {"addr": "${ACCOUNTS[3]}", "balance": 0}
  ]
}
CFG

KEYSTORE_FILE="$SCRIPT_DIR/$(find data/node1/keystore/ -maxdepth 1 -name 'UTC--*' -print -quit)"
"$GMET_BIN" attach --preload "${GOVERNANCE_JS},${DEPLOY_JS}" \
  --exec "GovernanceDeployer.deploy(\"${KEYSTORE_FILE}\", \"${PASSWORD}\", \"${SCRIPT_DIR}/config.json\", true)" \
  http://localhost:8645 2>&1 | tee data/deploy.log

# The PoA bootstrap needs node1's etcd for its mining token (Bokbunja).
for i in $(seq 1 10); do
  rpc 8645 admin_etcdInit '[]' >/dev/null 2>&1 && { log "etcd initialised on node1"; break; }
  sleep 3
done
status
