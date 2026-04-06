#!/usr/bin/env bash
# stop.sh - Stop PoA private network
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

log() { echo "[$(date '+%H:%M:%S')] $*"; }

CLEAN=false
for arg in "$@"; do
  [[ "$arg" == "--clean" || "$arg" == "--remove-volumes" ]] && CLEAN=true
done

log "=== Stopping PoA private network ==="

if $CLEAN; then
  log "Removing containers + all data..."
  docker compose down 2>/dev/null || true
  # Some files inside data/ may be root-owned (created by Docker before user: directive).
  # Use a temporary container to chown them to the current user before removing.
  if [ -d data ] && find data -maxdepth 3 -not -user "$(id -u)" 2>/dev/null | grep -q .; then
    log "Fixing root-owned files in data/ via docker..."
    docker run --rm -v "$(pwd)/data:/data" ubuntu:22.04 \
      chown -R "$(id -u):$(id -g)" /data 2>/dev/null || true
  fi
  rm -rf data/ static-nodes.json passwords.txt genesis.json gmet
  log "Full reset complete (run ./setup.sh to restart)"
else
  docker compose down 2>/dev/null || true
  log "Stopped (data preserved - to restart: ./start.sh)"
  log "To also delete data: ./stop.sh --clean"
fi
