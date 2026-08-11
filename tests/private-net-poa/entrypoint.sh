#!/bin/bash
set -e

DATADIR="${DATADIR:-/data/geth}"
GENESIS="${GENESIS:-/data/genesis.json}"

# Initialize with genesis (only when chaindata does not exist).
# Note: gmet uses DATADIR/gmet/ (not DATADIR/geth/) for its data directory.
# Note: --consensusmethod is a runtime flag only; gmet init does not accept it.
if [ ! -d "$DATADIR/gmet/chaindata" ]; then
    echo "[entrypoint] Initializing node with genesis: $GENESIS"
    gmet init --datadir "$DATADIR" "$GENESIS"
    echo "[entrypoint] Init complete."
fi

echo "[entrypoint] Starting gmet..."
exec gmet --datadir "$DATADIR" "$@"
