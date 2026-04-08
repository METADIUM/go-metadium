#!/usr/bin/env bash
# stop.sh - Stop the test network
# Usage: ./stop.sh [--clean]
#   --clean  Remove all data directories (full reset)

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

log() { echo "[$(date '+%H:%M:%S')] $*"; }

if [[ "${1:-}" == "--clean" ]]; then
  log "Stopping network and removing all data..."
  docker compose down --remove-orphans 2>/dev/null || true
  # Use Docker to remove data dirs (files may be owned by root inside containers)
  docker run --rm -v "$SCRIPT_DIR:/workdir" alpine:3 \
    sh -c "rm -rf /workdir/data/node1 /workdir/data/node2 /workdir/data/node3 /workdir/data/node4" \
    2>/dev/null || rm -rf data/node1 data/node2 data/node3 data/node4 2>/dev/null || true
  log "Clean complete."
else
  log "Stopping network (data preserved)..."
  docker compose stop 2>/dev/null || true
  log "Network stopped. Data preserved in data/node*/."
fi
