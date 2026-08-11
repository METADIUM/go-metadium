#!/usr/bin/env bash
# rpc_test.sh - go-metadium(gmet) node RPC test script
#
# Usage:
#   ./rpc_test.sh                   # default: http://127.0.0.1:8588
#   ./rpc_test.sh http://127.0.0.1:8590   # RocksDB node
#   ./rpc_test.sh http://127.0.0.1:8588 http://127.0.0.1:8590  # test both

set -euo pipefail

# ─── Color definitions ────────────────────────────────────────────────────────
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
CYAN='\033[0;36m'
BOLD='\033[1m'
RESET='\033[0m'

# ─── Utility functions ────────────────────────────────────────────────────────
PASS_COUNT=0
FAIL_COUNT=0
WARN_COUNT=0

pass() {
    local label="$1"
    echo -e "  ${GREEN}[PASS]${RESET} ${label}"
    PASS_COUNT=$((PASS_COUNT + 1))
}

fail() {
    local label="$1"
    local detail="${2:-}"
    echo -e "  ${RED}[FAIL]${RESET} ${label}"
    if [[ -n "$detail" ]]; then
        echo -e "         ${RED}↳ ${detail}${RESET}"
    fi
    FAIL_COUNT=$((FAIL_COUNT + 1))
}

warn() {
    local label="$1"
    local detail="${2:-}"
    echo -e "  ${YELLOW}[WARN]${RESET} ${label}"
    if [[ -n "$detail" ]]; then
        echo -e "         ${YELLOW}↳ ${detail}${RESET}"
    fi
    WARN_COUNT=$((WARN_COUNT + 1))
}

section() {
    echo ""
    echo -e "${CYAN}${BOLD}━━━ $1 ━━━${RESET}"
}

# JSON-RPC call function
# Returns: HTTP body (stdout), status code ($RPC_STATUS)
RPC_STATUS=0
rpc_call() {
    local url="$1"
    local method="$2"
    local params="${3:-[]}"
    local timeout="${4:-10}"

    local payload
    payload=$(printf '{"jsonrpc":"2.0","method":"%s","params":%s,"id":1}' "$method" "$params")

    local response
    response=$(curl -s --max-time "$timeout" \
        -X POST \
        -H "Content-Type: application/json" \
        --data "$payload" \
        "$url" 2>/dev/null) || true

    RPC_STATUS=$?
    echo "$response"
}

# Extract JSON field using python3
# Usage: json_get "$json" "result"
json_get() {
    local json="$1"
    local field="$2"
    python3 -c "
import json, sys
try:
    data = json.loads('''$json''')
    val = data.get('$field')
    if val is None:
        sys.exit(1)
    print(val)
except Exception as e:
    sys.exit(1)
" 2>/dev/null
}

# Check if a specific key exists in JSON (via exit code)
json_has() {
    local json="$1"
    local field="$2"
    python3 -c "
import json, sys
try:
    data = json.loads(sys.stdin.read())
    if '$field' in data and data['$field'] is not None:
        sys.exit(0)
    sys.exit(1)
except:
    sys.exit(1)
" <<< "$json" 2>/dev/null
}

# Check JSON error field
json_has_error() {
    local json="$1"
    python3 -c "
import json, sys
try:
    data = json.loads('''$json''')
    if 'error' in data:
        print(data['error'].get('message','unknown error'))
        sys.exit(0)
    sys.exit(1)
except:
    sys.exit(1)
" 2>/dev/null
}

# hex → decimal conversion
hex_to_dec() {
    python3 -c "print(int('$1', 16))" 2>/dev/null || echo "?"
}

# ─── Run tests per node ───────────────────────────────────────────────────────
run_tests() {
    local NODE_URL="$1"
    local NODE_LABEL="${2:-Node}"

    echo ""
    echo -e "${BOLD}${BLUE}╔══════════════════════════════════════════════════╗${RESET}"
    echo -e "${BOLD}${BLUE}║  Test node: ${NODE_LABEL}${RESET}"
    echo -e "${BOLD}${BLUE}║  URL: ${NODE_URL}${RESET}"
    echo -e "${BOLD}${BLUE}╚══════════════════════════════════════════════════╝${RESET}"

    # Reset counters (per node)
    PASS_COUNT=0
    FAIL_COUNT=0
    WARN_COUNT=0

    # ── 1. Basic connectivity ──────────────────────────────────────────────────
    section "1. Basic connectivity"

    # net_version
    local resp
    resp=$(rpc_call "$NODE_URL" "net_version")
    if [[ $RPC_STATUS -ne 0 ]] || [[ -z "$resp" ]]; then
        fail "net_version — node connection failed (URL: $NODE_URL)"
        echo ""
        echo -e "${RED}${BOLD}[Error] Cannot connect to node. Skipping remaining tests.${RESET}"
        echo ""
        return 1
    fi
    local net_ver
    net_ver=$(json_get "$resp" "result") && pass "net_version = $net_ver" || fail "net_version — response parse failed: $resp"

    # net_peerCount
    resp=$(rpc_call "$NODE_URL" "net_peerCount")
    local peer_hex
    peer_hex=$(json_get "$resp" "result") && {
        local peer_dec
        peer_dec=$(hex_to_dec "$peer_hex")
        pass "net_peerCount = $peer_dec (hex: $peer_hex)"
    } || fail "net_peerCount — response parse failed: $resp"

    # eth_blockNumber
    resp=$(rpc_call "$NODE_URL" "eth_blockNumber")
    local block_hex
    block_hex=$(json_get "$resp" "result") && {
        local block_dec
        block_dec=$(hex_to_dec "$block_hex")
        pass "eth_blockNumber = $block_dec (hex: $block_hex)"
        LATEST_BLOCK_HEX="$block_hex"
        LATEST_BLOCK_DEC="$block_dec"
    } || fail "eth_blockNumber — response parse failed: $resp"

    # ── 2. Block queries ──────────────────────────────────────────────────────
    section "2. Block queries"

    # eth_getBlockByNumber (latest) — must work even before sync
    resp=$(rpc_call "$NODE_URL" "eth_getBlockByNumber" '["latest", false]')
    local err_msg
    err_msg=$(json_has_error "$resp") && warn "eth_getBlockByNumber(latest) — node response error: $err_msg" || {
        python3 -c "
import json, sys
try:
    data = json.loads('''$resp''')
    b = data.get('result')
    if b and 'number' in b:
        num = int(b['number'], 16)
        ts = int(b.get('timestamp','0x0'), 16)
        print(f'block #{num}, txs={len(b.get(\"transactions\",[]))}, ts={ts}')
        sys.exit(0)
    sys.exit(1)
except Exception as e:
    sys.exit(1)
" 2>/dev/null | { read -r info && pass "eth_getBlockByNumber(latest) — $info"; } || \
        warn "eth_getBlockByNumber(latest) — no result (may be syncing)"
    }

    # eth_getBlockByNumber (genesis = 0x0)
    resp=$(rpc_call "$NODE_URL" "eth_getBlockByNumber" '["0x0", false]')
    err_msg=$(json_has_error "$resp") && warn "eth_getBlockByNumber(0x0) — error: $err_msg" || {
        python3 -c "
import json, sys
try:
    data = json.loads('''$resp''')
    b = data.get('result')
    if b and 'number' in b:
        num = int(b['number'], 16)
        h = b.get('hash','')
        print(f'genesis block hash={h[:18]}...')
        sys.exit(0)
    sys.exit(1)
except:
    sys.exit(1)
" 2>/dev/null | { read -r info && pass "eth_getBlockByNumber(0x0) — $info"; GENESIS_HASH=$(python3 -c "import json; d=json.loads('''$resp'''); print(d['result']['hash'])"); } || \
        warn "eth_getBlockByNumber(0x0) — genesis block not found"
    }

    # eth_getBlockByNumber (0x1)
    resp=$(rpc_call "$NODE_URL" "eth_getBlockByNumber" '["0x1", false]')
    err_msg=$(json_has_error "$resp") && warn "eth_getBlockByNumber(0x1) — error: $err_msg" || {
        python3 -c "
import json, sys
try:
    data = json.loads('''$resp''')
    b = data.get('result')
    if b and 'number' in b:
        num = int(b['number'], 16)
        h = b.get('hash','')
        print(f'block #1 hash={h[:18]}...')
        sys.exit(0)
    sys.exit(1)
except:
    sys.exit(1)
" 2>/dev/null | { read -r info && pass "eth_getBlockByNumber(0x1) — $info"; BLOCK1_HASH=$(python3 -c "import json; d=json.loads('''$resp'''); print(d['result']['hash'])"); } || \
        warn "eth_getBlockByNumber(0x1) — block not found (syncing)"
    }

    # eth_getBlockByHash (using block #1 hash if available)
    if [[ -n "${BLOCK1_HASH:-}" ]]; then
        resp=$(rpc_call "$NODE_URL" "eth_getBlockByHash" "[\"$BLOCK1_HASH\", false]")
        err_msg=$(json_has_error "$resp") && fail "eth_getBlockByHash — error: $err_msg" || {
            python3 -c "
import json, sys
try:
    data = json.loads('''$resp''')
    b = data.get('result')
    if b and 'number' in b:
        num = int(b['number'], 16)
        print(f'block #{num} retrieved successfully')
        sys.exit(0)
    sys.exit(1)
except:
    sys.exit(1)
" 2>/dev/null | { read -r info && pass "eth_getBlockByHash(block#1) — $info"; } || \
            fail "eth_getBlockByHash — response parse failed"
        }
    else
        warn "eth_getBlockByHash — block #1 hash not available, skipping test"
    fi

    # ── 3. Sync status ────────────────────────────────────────────────────────
    section "3. Sync status"

    resp=$(rpc_call "$NODE_URL" "eth_syncing")
    err_msg=$(json_has_error "$resp") && fail "eth_syncing — error: $err_msg" || {
        python3 -c "
import json, sys
try:
    data = json.loads('''$resp''')
    result = data.get('result')
    if result is False:
        print('sync complete (syncing=false)')
    elif isinstance(result, dict):
        cur = int(result.get('currentBlock','0x0'), 16)
        high = int(result.get('highestBlock','0x0'), 16)
        pct = (cur / high * 100) if high > 0 else 0
        print(f'syncing: {cur:,} / {high:,} ({pct:.1f}%)')
    else:
        print(f'unknown status: {result}')
    sys.exit(0)
except Exception as e:
    print(f'parse error: {e}', file=sys.stderr)
    sys.exit(1)
" 2>/dev/null | { read -r info && pass "eth_syncing — $info"; } || \
        fail "eth_syncing — response parse failed: $resp"
    }

    # ── 4. Chain config ───────────────────────────────────────────────────────
    section "4. Chain config"

    # eth_chainId
    resp=$(rpc_call "$NODE_URL" "eth_chainId")
    local chain_hex
    chain_hex=$(json_get "$resp" "result") && {
        local chain_dec
        chain_dec=$(hex_to_dec "$chain_hex")
        pass "eth_chainId = $chain_dec (hex: $chain_hex)"
    } || fail "eth_chainId — response parse failed: $resp"

    # ── 5. Gas ────────────────────────────────────────────────────────────────
    section "5. Gas"

    # eth_gasPrice
    resp=$(rpc_call "$NODE_URL" "eth_gasPrice")
    local gasprice_hex
    gasprice_hex=$(json_get "$resp" "result") && {
        local gasprice_dec
        gasprice_dec=$(hex_to_dec "$gasprice_hex")
        pass "eth_gasPrice = ${gasprice_dec} wei (hex: $gasprice_hex)"
    } || fail "eth_gasPrice — response parse failed: $resp"

    # eth_estimateGas (simple ETH transfer)
    local estimate_params
    estimate_params='[{"from":"0x0000000000000000000000000000000000000001","to":"0x0000000000000000000000000000000000000002","value":"0x1"}]'
    resp=$(rpc_call "$NODE_URL" "eth_estimateGas" "$estimate_params")
    err_msg=$(json_has_error "$resp") && warn "eth_estimateGas — error (may be expected): $err_msg" || {
        local gas_hex
        gas_hex=$(json_get "$resp" "result") && {
            local gas_dec
            gas_dec=$(hex_to_dec "$gas_hex")
            pass "eth_estimateGas = $gas_dec (hex: $gas_hex)"
        } || warn "eth_estimateGas — result parse failed"
    }

    # ── 6. Accounts ───────────────────────────────────────────────────────────
    section "6. Accounts"

    # eth_accounts
    resp=$(rpc_call "$NODE_URL" "eth_accounts")
    err_msg=$(json_has_error "$resp") && warn "eth_accounts — error: $err_msg" || {
        python3 -c "
import json, sys
try:
    data = json.loads('''$resp''')
    accs = data.get('result', [])
    print(f'accounts={len(accs)}' + (f', first={accs[0]}' if accs else ''))
    sys.exit(0)
except:
    sys.exit(1)
" 2>/dev/null | { read -r info && pass "eth_accounts — $info"; } || \
        warn "eth_accounts — response parse failed"
    }

    # eth_getBalance (zero address)
    local TEST_ADDR="0x0000000000000000000000000000000000000000"
    resp=$(rpc_call "$NODE_URL" "eth_getBalance" "[\"$TEST_ADDR\", \"latest\"]")
    err_msg=$(json_has_error "$resp") && warn "eth_getBalance($TEST_ADDR) — error: $err_msg" || {
        local bal_hex
        bal_hex=$(json_get "$resp" "result") && {
            local bal_dec
            bal_dec=$(hex_to_dec "$bal_hex")
            pass "eth_getBalance(zero addr) = $bal_dec wei"
        } || warn "eth_getBalance — result parse failed"
    }

    # ── 7. Admin API ──────────────────────────────────────────────────────────
    section "7. Admin API"

    # admin_nodeInfo
    resp=$(rpc_call "$NODE_URL" "admin_nodeInfo")
    err_msg=$(json_has_error "$resp") && {
        # WARNING when admin API is disabled
        if echo "$err_msg" | grep -qi "method not found\|not found\|unauthorized\|missing"; then
            warn "admin_nodeInfo — admin API disabled (add admin to --rpcapi)"
        else
            fail "admin_nodeInfo — error: $err_msg"
        fi
    } || {
        python3 -c "
import json, sys
try:
    data = json.loads('''$resp''')
    ni = data.get('result')
    if ni:
        name = ni.get('name','?')
        enode = ni.get('enode','')[:40]
        print(f'name={name}, enode={enode}...')
        sys.exit(0)
    sys.exit(1)
except:
    sys.exit(1)
" 2>/dev/null | { read -r info && pass "admin_nodeInfo — $info"; } || \
        warn "admin_nodeInfo — response parse failed"
    }

    # admin_peers
    resp=$(rpc_call "$NODE_URL" "admin_peers")
    err_msg=$(json_has_error "$resp") && {
        if echo "$err_msg" | grep -qi "method not found\|not found\|unauthorized\|missing"; then
            warn "admin_peers — admin API disabled"
        else
            fail "admin_peers — error: $err_msg"
        fi
    } || {
        python3 -c "
import json, sys
try:
    data = json.loads('''$resp''')
    peers = data.get('result', [])
    if isinstance(peers, list):
        print(f'connected peers={len(peers)}')
        sys.exit(0)
    sys.exit(1)
except:
    sys.exit(1)
" 2>/dev/null | { read -r info && pass "admin_peers — $info"; } || \
        warn "admin_peers — response parse failed"
    }

    # ── Result summary ────────────────────────────────────────────────────────
    local total=$((PASS_COUNT + FAIL_COUNT + WARN_COUNT))
    echo ""
    echo -e "${BOLD}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${RESET}"
    echo -e "${BOLD}  Test result summary: ${NODE_LABEL} (${NODE_URL})${RESET}"
    echo -e "${BOLD}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${RESET}"
    echo -e "  Total: ${total}  |  ${GREEN}PASS: ${PASS_COUNT}${RESET}  |  ${RED}FAIL: ${FAIL_COUNT}${RESET}  |  ${YELLOW}WARN: ${WARN_COUNT}${RESET}"
    echo -e "${BOLD}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${RESET}"

    if [[ $FAIL_COUNT -eq 0 ]]; then
        echo -e "  ${GREEN}${BOLD}[Final result: PASS]${RESET} (no FAILs)"
    else
        echo -e "  ${RED}${BOLD}[Final result: FAIL]${RESET} (${FAIL_COUNT} failure(s))"
    fi
    echo ""

    # Return non-zero exit code on FAIL (for aggregation at the script level)
    return $FAIL_COUNT
}

# ─── Main ─────────────────────────────────────────────────────────────────────
main() {
    echo ""
    echo -e "${BOLD}${BLUE}╔══════════════════════════════════════════════════════╗${RESET}"
    echo -e "${BOLD}${BLUE}║       go-metadium (gmet) RPC Test Script             ║${RESET}"
    echo -e "${BOLD}${BLUE}╚══════════════════════════════════════════════════════╝${RESET}"

    # Check python3 dependency
    if ! command -v python3 &>/dev/null; then
        echo -e "${RED}[Error] python3 is not installed.${RESET}"
        exit 1
    fi
    # Check curl dependency
    if ! command -v curl &>/dev/null; then
        echo -e "${RED}[Error] curl is not installed.${RESET}"
        exit 1
    fi

    # Argument handling
    # No args → test default LevelDB node only
    # 1 arg   → test only that URL
    # 2 args  → test both URLs
    local URLS=()
    if [[ $# -eq 0 ]]; then
        URLS=("http://127.0.0.1:8588")
    else
        URLS=("$@")
    fi

    local GLOBAL_FAIL=0
    for url in "${URLS[@]}"; do
        BLOCK1_HASH=""
        GENESIS_HASH=""
        LATEST_BLOCK_HEX="0x0"
        LATEST_BLOCK_DEC="0"

        # Auto-generate node label
        local label="$url"
        if [[ "$url" == *":8588"* ]]; then
            label="LevelDB node ($url)"
        elif [[ "$url" == *":8590"* ]]; then
            label="RocksDB node ($url)"
        fi

        run_tests "$url" "$label" || GLOBAL_FAIL=$((GLOBAL_FAIL + $?))
    done

    # Final summary for multiple nodes
    if [[ ${#URLS[@]} -gt 1 ]]; then
        echo -e "${BOLD}${BLUE}╔══════════════════════════════════════════════════════╗${RESET}"
        echo -e "${BOLD}${BLUE}║              All Nodes Final Summary                 ║${RESET}"
        echo -e "${BOLD}${BLUE}╚══════════════════════════════════════════════════════╝${RESET}"
        if [[ $GLOBAL_FAIL -eq 0 ]]; then
            echo -e "  ${GREEN}${BOLD}All node tests PASS${RESET}"
        else
            echo -e "  ${RED}${BOLD}FAIL occurred on some nodes (total ${GLOBAL_FAIL} failure(s))${RESET}"
        fi
        echo ""
    fi

    exit $GLOBAL_FAIL
}

main "$@"
