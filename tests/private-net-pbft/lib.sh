# lib.sh - helpers shared by the PBFT private-network scripts

log()  { echo "[$(date '+%H:%M:%S')] $*"; }
err()  { echo "[ERROR] $*" >&2; exit 1; }

# The network setup.sh made: node-ids.txt has "nodeN <node ID> <account>".
NODES=$( [[ -f node-ids.txt ]] && wc -l < node-ids.txt || echo 0)
F=$(( (NODES - 1) / 3 ))           # faults tolerated
QUORUM=$(( (2 * NODES + 2) / 3 ))  # ceil(2N/3)
port_of()    { echo $((8644 + $1)); }
account_of() { awk -v n="node$1" '$1==n{print $3}' node-ids.txt; }
enode_of()   { echo "enode://$(awk -v n="node$1" '$1==n{print $2}' node-ids.txt)@172.32.0.1$1:30303"; }
container()  { echo "gmet-pbft-node$1"; }

# rpc PORT METHOD PARAMS -> the JSON result, or fails
rpc() {
  curl -sf -m 10 -X POST -H "Content-Type: application/json" \
    --data "{\"jsonrpc\":\"2.0\",\"method\":\"$2\",\"params\":$3,\"id\":1}" \
    "http://localhost:$1" |
    python3 -c "import sys,json; d=json.load(sys.stdin); sys.exit(1) if 'error' in d else print(json.dumps(d['result']))"
}

# block_number PORT
block_number() { rpc "$1" eth_blockNumber '[]' | python3 -c "import sys,json; print(int(json.load(sys.stdin),16))"; }

# mesh N...: connect the given nodes to each other
mesh() {
  for n in "$@"; do
    for m in "$@"; do
      [[ $n == "$m" ]] && continue
      rpc "$(port_of "$n")" admin_addPeer "[\"$(enode_of "$m")\"]" >/dev/null 2>&1 || true
    done
  done
}

status() {
  for n in $(seq 1 "$NODES"); do
    port=$(port_of "$n")
    b=$(block_number "$port" 2>/dev/null || echo "?")
    p=$(rpc "$port" net_peerCount '[]' 2>/dev/null | python3 -c "import sys,json; print(int(json.load(sys.stdin),16))" 2>/dev/null || echo "?")
    log "  node$n :$port block=$b peers=$p"
  done
}
