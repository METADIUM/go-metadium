#!/usr/bin/env bash
# doraji-test.sh - Doraji fork transition test
#
# Checks every Doraji EIP before and after the fork block (doraji-e2e), then
# that a 254 KB contract creation still goes through after it (bigtx-e2e):
# Doraji leaves out the EIP-7623, 7825 and 7934 limits on purpose.
#
# Prerequisite: a network set up with the fork scheduled, e.g.
#   DORAJI_BLOCK=200 ./setup.sh && ./start.sh && ./deploy.sh
# Usage: ./doraji-test.sh
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"
REPO="$(cd ../.. && pwd)"
RPC="${RPC:-http://localhost:8545}"

log() { echo "[$(date '+%H:%M:%S')] $*"; }

[[ -f genesis.json ]] || { echo "[ERROR] genesis.json not found; run setup.sh first" >&2; exit 1; }
DORAJI_BLOCK=$(python3 -c "import json; print(json.load(open('genesis.json'))['config'].get('dorajiBlock', ''))")
[[ -n "$DORAJI_BLOCK" ]] || { echo "[ERROR] genesis.json has no dorajiBlock; set up with DORAJI_BLOCK=<n> ./setup.sh" >&2; exit 1; }

log "=== Doraji fork transition test (dorajiBlock=$DORAJI_BLOCK) ==="
(cd "$REPO" && go run ./tests/private-net-poa/doraji-e2e "$RPC" "$DORAJI_BLOCK")

log "=== 254 KB contract creation after the fork ==="
(cd "$REPO" && go run ./tests/private-net-poa/bigtx-e2e)

log "=== Doraji fork test complete ==="
