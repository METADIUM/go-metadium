#!/bin/bash
set -e

DATADIR="${DATADIR:-/data/geth}"
GENESIS="${GENESIS:-/data/genesis.json}"

# gmet stores chaindata under DATADIR/gmet/chaindata (not geth/)
if [ ! -d "$DATADIR/gmet/chaindata" ]; then
    echo "[entrypoint] Initializing node with genesis: $GENESIS"
    gmet init --datadir "$DATADIR" "$GENESIS"
    echo "[entrypoint] Init complete."
fi

echo "[entrypoint] Starting gmet ($*)..."
exec gmet --datadir "$DATADIR" "$@"
