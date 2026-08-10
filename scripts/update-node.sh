#!/usr/bin/env bash
# update-node.sh - Build new binary and restart node on server
#
# Usage:
#   (on server) bash update-node.sh [--rocksdb|--leveldb] [--datadir <path>] [--restart]
#
# Example:
#   bash update-node.sh --rocksdb --datadir /path/to/datadir --restart
#
# Prerequisites: Go 1.21+, librocksdb-dev (for rocksdb build)

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

USE_ROCKSDB=0
DATADIR=""
DO_RESTART=0
BINARY_NAME=""
NODE_PID_FILE=""
NODE_CMD=""

log()  { echo "[$(date '+%H:%M:%S')] $*"; }
err()  { echo "[ERROR] $*" >&2; exit 1; }

# Parse args
while [[ $# -gt 0 ]]; do
  case "$1" in
    --rocksdb) USE_ROCKSDB=1; shift ;;
    --leveldb) USE_ROCKSDB=0; shift ;;
    --datadir) DATADIR="$2"; shift 2 ;;
    --restart) DO_RESTART=1; shift ;;
    *) err "Unknown arg: $1" ;;
  esac
done

log "=== go-metadium node update ==="
log "Repository: $REPO_DIR"

# Pull latest code
cd "$REPO_DIR"
log "git pull origin dev..."
git fetch origin
git checkout dev
git pull origin dev

# Build
if [[ $USE_ROCKSDB -eq 1 ]]; then
  BINARY_NAME="gmet-rocksdb"
  log "Building gmet (RocksDB)..."
  # Search for RocksDB shared library path
  ROCKSDB_LIB=""
  for dir in /usr/lib/x86_64-linux-gnu /usr/lib/aarch64-linux-gnu /usr/local/lib; do
    if ls "$dir"/librocksdb.so* &>/dev/null 2>&1; then
      ROCKSDB_LIB="$dir"
      break
    fi
  done
  [[ -n "$ROCKSDB_LIB" ]] || err "librocksdb not found. Run apt install librocksdb-dev and retry."

  CGO_CFLAGS="-I/usr/include" \
  CGO_LDFLAGS="-L$ROCKSDB_LIB -lrocksdb -lsnappy -llz4 -lzstd -lm -lstdc++ -ldl" \
  go build -tags rocksdb -o "$BINARY_NAME" ./cmd/gmet
else
  BINARY_NAME="gmet-leveldb"
  log "Building gmet (LevelDB)..."
  go build -o "$BINARY_NAME" ./cmd/gmet
fi

log "Build complete: $REPO_DIR/$BINARY_NAME ($(ls -lh $BINARY_NAME | awk '{print $5}'))"

# Restart
if [[ $DO_RESTART -eq 1 ]]; then
  [[ -n "$DATADIR" ]] || err "--datadir required when using --restart"

  # Find existing process
  PIDS=$(pgrep -f "gmet.*--datadir.*$(basename $DATADIR)" 2>/dev/null || true)
  if [[ -n "$PIDS" ]]; then
    log "Stopping existing process (PID: $PIDS)..."
    kill $PIDS
    sleep 3
  fi

  log "Restarting node..."
  log "Replace the binary and start the node:"
  log "  cp $REPO_DIR/$BINARY_NAME <install-dir>/$BINARY_NAME"
  log "  <install-dir>/$BINARY_NAME [original start options]"
  log ""
  log "To avoid detectDb() LOG file issues:"
  log "  Use --userocksdb 1 flag to always use RocksDB regardless of LOG file"
fi

log "=== Done ==="
