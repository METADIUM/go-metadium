#!/bin/bash
set -e

DATADIR="${DATADIR:-/data/geth}"
GENESIS="${GENESIS:-/data/genesis.json}"

# Extract --consensusmethod value from $@ (default: 2=PoA)
CONSENSUS_METHOD=2
prev=""
for arg in "$@"; do
  if [[ "$prev" == "--consensusmethod" ]]; then CONSENSUS_METHOD="$arg"; fi
  prev="$arg"
done

# Initialize with genesis (only when chaindata does not exist)
# gmet init must also run with the same consensusmethod so the header RLP format matches
if [ ! -d "$DATADIR/geth/chaindata" ]; then
    echo "[entrypoint] Initializing node with genesis: $GENESIS (consensusmethod=$CONSENSUS_METHOD)"
    gmet init --datadir "$DATADIR" --consensusmethod "$CONSENSUS_METHOD" "$GENESIS"
    echo "[entrypoint] Init complete."
fi

echo "[entrypoint] Starting gmet..."
exec gmet --datadir "$DATADIR" "$@"
