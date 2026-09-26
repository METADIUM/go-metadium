#!/usr/bin/env bash
# deploy.sh - deploy governance with every node as a member. Must finish
# before bftBlock: the PBFT validator set is read from it (design §9.2).
#
# Options: GMET_BIN=/path/to/gmet
#          BLOCK_CREATION_TIME=1000 (ms; must not exceed bft.emptyBlockInterval)
#          MEMBERS=N (register only node1..nodeN; default every node. Fewer
#                     than 4 is how transition.sh makes the switch fail, R-02)
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"
source ./lib.sh

GMET_BIN="${GMET_BIN:-$SCRIPT_DIR/../../build/bin/gmet}"
GOVERNANCE_JS="$SCRIPT_DIR/../../metadium/contracts/MetadiumGovernance.js"
DEPLOY_JS="$SCRIPT_DIR/../../metadium/scripts/deploy-governance.js"
PASSWORD="privatenet123"
[[ -x "$GMET_BIN" ]] || err "gmet binary not found: $GMET_BIN"

BFT_BLOCK=$(python3 -c "import json; print(json.load(open('genesis.json'))['config'].get('bftBlock', 'off'))")
HEAD=$(block_number 8645) || err "node1 RPC not responding; run start.sh first"
[[ $BFT_BLOCK == off ]] || (( HEAD + 20 < BFT_BLOCK )) || err "head $HEAD is too close to bftBlock $BFT_BLOCK; start over with a later BFT_BLOCK"
log "=== Deploying governance at block $HEAD (bftBlock $BFT_BLOCK) ==="

MEMBERS=${MEMBERS:-$NODES}
(( MEMBERS >= 1 && MEMBERS <= NODES )) || err "MEMBERS must be 1..$NODES, have $MEMBERS"
members=""
for n in $(seq 1 "$MEMBERS"); do
  boot=""; [[ $n == 1 ]] && boot=', "bootnode": true'
  members+="$( [[ $n == 1 ]] || echo ,)
    {\"addr\": \"$(account_of "$n")\", \"stake\": 1000000000000000000, \"name\": \"node$n\",
     \"id\": \"$(awk -v m="node$n" '$1==m{print $2}' node-ids.txt)\", \"ip\": \"172.32.0.1$n\", \"port\": 30303$boot}"
done
accounts=$(awk '{printf "%s{\"addr\": \"%s\", \"balance\": 0}", (NR>1 ? ", " : ""), $3}' node-ids.txt)
A1=$(account_of 1)
cat > config.json <<CFG
{
  "staker":      "$A1",
  "ecosystem":   "$A1",
  "maintenance": "$A1",
  "feecollector":"$A1",
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
  "accounts": [$accounts]
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
