#!/usr/bin/env bash
# stop.sh - stop the network; --clean also removes all data
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"
docker compose down 2>/dev/null || true
if [[ "${1:-}" == "--clean" ]]; then
  rm -rf data/ passwords.txt genesis.json node-ids.txt keys.txt config.json .env docker-compose.yml
  echo "Removed all data (run ./setup.sh to start over)"
fi
