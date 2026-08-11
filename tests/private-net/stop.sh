#!/usr/bin/env bash
# stop.sh - Stop private network
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

log() { echo "[$(date '+%H:%M:%S')] $*"; }

CLEAN=false
for arg in "$@"; do
  [[ "$arg" == "--clean" || "$arg" == "--remove-volumes" ]] && CLEAN=true
done

log "=== Stopping private network ==="

if $CLEAN; then
  log "Removing containers + all data..."
  docker compose down 2>/dev/null || true
  rm -rf data/ static-nodes.json passwords.txt gmet
  log "Full reset complete (run ./setup.sh to restart)"
else
  docker compose down 2>/dev/null || true
  log "Stopped (data preserved - to restart: ./start.sh)"
  log "To also delete data: ./stop.sh --clean"
fi
