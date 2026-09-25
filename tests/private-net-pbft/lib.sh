# lib.sh - helpers shared by the PBFT private-network scripts
PORTS=(8645 8646 8647 8648)

log()  { echo "[$(date '+%H:%M:%S')] $*"; }
err()  { echo "[ERROR] $*" >&2; exit 1; }

# rpc PORT METHOD PARAMS -> the JSON result, or fails
rpc() {
  curl -sf -X POST -H "Content-Type: application/json" \
    --data "{\"jsonrpc\":\"2.0\",\"method\":\"$2\",\"params\":$3,\"id\":1}" \
    "http://localhost:$1" |
    python3 -c "import sys,json; d=json.load(sys.stdin); sys.exit(1) if 'error' in d else print(json.dumps(d['result']))"
}

# block_number PORT
block_number() { rpc "$1" eth_blockNumber '[]' | python3 -c "import sys,json; print(int(json.load(sys.stdin),16))"; }

# enode_of N -> enode URL of node N
enode_of() { echo "enode://$(awk -v n="node$1" '$1==n{print $2}' node-ids.txt)@172.32.0.1$1:30303"; }

status() {
  for i in 0 1 2 3; do
    port=${PORTS[$i]}
    b=$(block_number "$port" 2>/dev/null || echo "?")
    p=$(rpc "$port" net_peerCount '[]' 2>/dev/null | python3 -c "import sys,json; print(int(json.load(sys.stdin),16))" 2>/dev/null || echo "?")
    log "  node$((i + 1)) :$port block=$b peers=$p"
  done
}
