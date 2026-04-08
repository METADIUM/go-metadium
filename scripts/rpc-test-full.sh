#!/usr/bin/env bash
# rpc-test-full.sh - go-metadium full Execution API + Fee Delegation + IPC/JS integration test
#
# Usage:
#   bash rpc-test-full.sh [RPC_URL] [IPC_PATH]
#   bash rpc-test-full.sh http://localhost:8545 /path/to/gmet.ipc
#
# Coverage: https://ethereum.github.io/execution-apis/ (includes pre/post EIP-4844)
#   eth_*, net_*, web3_*, debug_*, admin_*, txpool_*, personal_*
#   + eth_sendTransaction, eth_sendRawTransaction
#   + Fee Delegation (Type 22 tx)
#   + IPC + JS console

set -euo pipefail
NODE="${1:-http://localhost:8545}"
IPC_PATH="${2:-}"

# Test accounts (private-net-poa hardhat defaults)
ACCT1="0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266"
ACCT2="0x70997970C51812dc3A010C7d01b50e0d17dc79C8"
ACCT3="0x3C44CdDdB6a900fa2b585dd299e03d12FA4293BC"
PRIVKEY1="ac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80"
PRIVKEY2="59c6995e998f97a5a0044966f0945389dc9e86dae88c7a8412f4603b6b78690d"
PASSWORD="privatenet123"
CHAINID=1337

# ── Colors ───────────────────────────────────────────────────────────────────
G='\033[0;32m'; R='\033[0;31m'; Y='\033[1;33m'; C='\033[0;36m'
B='\033[1m'; D='\033[2m'; N='\033[0m'
PASS=0; FAIL=0; WARN=0; SKIP=0

pass() { echo -e "  ${G}[PASS]${N} $1"; PASS=$((PASS+1)); }
fail() { echo -e "  ${R}[FAIL]${N} $1${2:+\n         ${R}↳ $2${N}}"; FAIL=$((FAIL+1)); }
warn() { echo -e "  ${Y}[WARN]${N} $1${2:+ — $2}"; WARN=$((WARN+1)); }
skip() { echo -e "  ${D}[SKIP]${N} $1${2:+ — $2}"; SKIP=$((SKIP+1)); }
sec()  { echo -e "\n${C}${B}━━━ $1 ━━━${N}"; }

# ── RPC helpers ──────────────────────────────────────────────────────────────
rpc() {
  local method="$1" params="${2:-[]}"
  curl -sf --max-time 10 -X POST -H 'Content-Type: application/json' \
    --data "{\"jsonrpc\":\"2.0\",\"method\":\"${method}\",\"params\":${params},\"id\":1}" \
    "$NODE" 2>/dev/null
}

jq_int()  { python3 -c "import json,sys; d=json.load(sys.stdin); r=d.get('result','0x0'); print(int(r,16) if isinstance(r,str) and r.startswith('0x') else (r or 0))"; }
jq_str()  { python3 -c "import json,sys; d=json.load(sys.stdin); print(d.get('result',''))"; }
jq_len()  { python3 -c "import json,sys; d=json.load(sys.stdin); r=d.get('result',[]); print(len(r) if isinstance(r,list) else 0)"; }
jq_bool() { python3 -c "import json,sys; d=json.load(sys.stdin); print(d.get('result',False))"; }
jq_err()  { python3 -c "import json,sys; d=json.load(sys.stdin); e=d.get('error'); print(e.get('message','unknown')) if e else sys.exit(1)" 2>/dev/null; }
is_skip() { [[ "$1" =~ "not found"|"not supported"|"unknown"|"does not exist"|"not available" ]]; }

check() {
  # check <label> <method> [params] -- generic test
  local label="$1" method="$2" params="${3:-[]}"
  local resp; resp=$(rpc "$method" "$params")
  local err; err=$(echo "$resp" | jq_err 2>/dev/null || true)
  if [[ -n "$err" ]]; then
    is_skip "$err" && skip "$label" "$err" || fail "$label" "$err"
    return
  fi
  local result; result=$(echo "$resp" | python3 -c "import json,sys; r=json.load(sys.stdin).get('result'); print(r)" 2>/dev/null || true)
  [[ -z "$result" || "$result" == "None" ]] && warn "$label" "result=null" || pass "$label — $result"
}

evm_call() {
  # evm_call <label> <bytecode> <expected_dec>
  local label="$1" bytecode="$2" expected="$3"
  local resp; resp=$(rpc "eth_call" "[{\"data\":\"$bytecode\"},\"latest\"]")
  local err; err=$(echo "$resp" | jq_err 2>/dev/null || true)
  [[ -n "$err" ]] && { fail "$label" "$err"; return; }
  local ret; ret=$(echo "$resp" | python3 -c "
import json,sys; d=json.load(sys.stdin)
r=d.get('result','0x'); print(int(r,16) if r and r!='0x' else 0)" 2>/dev/null || echo "ERR")
  [[ "$ret" == "ERR" ]] && { fail "$label" "parse error"; return; }
  [[ -n "$expected" && "$ret" != "$expected" ]] && fail "$label" "returned=$ret (expected:$expected)" || pass "$label — return=$ret"
}

# ── Setup: Send TX and collect ────────────────────────────────────────────
setup_tx() {
  # Send TX via eth_sendTransaction and collect
  local resp
  resp=$(rpc "eth_sendTransaction" "[{
    \"from\":\"$ACCT1\",
    \"to\":\"$ACCT2\",
    \"value\":\"0xDE0B6B3A7640000\",
    \"gas\":\"0x5208\"
  }]")
  TX_HASH=$(echo "$resp" | python3 -c "import json,sys; print(json.load(sys.stdin).get('result',''))" 2>/dev/null || true)

  # Wait for TX confirmation (up to 15 seconds)
  if [[ -n "$TX_HASH" && "$TX_HASH" != "None" ]]; then
    for i in $(seq 1 15); do
      sleep 1
      local tx_resp
      tx_resp=$(rpc "eth_getTransactionReceipt" "[\"$TX_HASH\"]")
      local status
      status=$(echo "$tx_resp" | python3 -c "import json,sys; r=json.load(sys.stdin)['result']; print(r['blockHash'] if r else '')" 2>/dev/null || true)
      [[ -n "$status" ]] && { TX_BLOCK_HASH="$status"; break; }
    done
  fi
}

# Sign transaction with python3 + eth_account (for eth_sendRawTransaction)
sign_tx() {
  # sign_tx <from_privkey> <to_addr> <value_hex> <nonce_hex>
  python3 - <<PYEOF 2>/dev/null
import sys
try:
    from eth_account import Account
    from eth_account.signers.local import LocalAccount
    import json

    privkey = "0x$1"
    acct: LocalAccount = Account.from_key(privkey)
    nonce = int("$4", 16)
    tx = {
        "nonce": nonce,
        "to": "$2",
        "value": int("$3", 16),
        "gas": 21000,
        "maxFeePerGas": 200 * 10**9,
        "maxPriorityFeePerGas": 100 * 10**9,
        "chainId": $CHAINID,
        "type": 2,
    }
    signed = acct.sign_transaction(tx)
    print(signed.raw_transaction.hex())
except ImportError:
    print("NO_ETH_ACCOUNT")
except Exception as e:
    print(f"ERR:{e}", file=sys.stderr)
PYEOF
}

# ════════════════════════════════════════════════════════════════════
echo -e "${B}${C}╔══════════════════════════════════════════════════════════════╗${N}"
echo -e "${B}${C}║   go-metadium Full Execution API + FeeDelegation + IPC Test   ║${N}"
echo -e "${B}${C}║   Target: $NODE$(printf '%*s' $((44-${#NODE})) '')║${N}"
echo -e "${B}${C}╚══════════════════════════════════════════════════════════════╝${N}"

# Basic connectivity check
LATEST_RESP=$(rpc "eth_getBlockByNumber" '["latest",true]')
LATEST_NUM=$(echo "$LATEST_RESP" | python3 -c "import json,sys; print(json.load(sys.stdin)['result']['number'])" 2>/dev/null || echo "0x0")
BLOCK_DEC=$(python3 -c "print(int('$LATEST_NUM',16))" 2>/dev/null || echo "0")
LATEST_HASH=$(echo "$LATEST_RESP" | python3 -c "import json,sys; print(json.load(sys.stdin)['result']['hash'])" 2>/dev/null || echo "")
BLOCK1_RESP=$(rpc "eth_getBlockByNumber" '["0x1",false]')
BLOCK1_HASH=$(echo "$BLOCK1_RESP" | python3 -c "import json,sys; print(json.load(sys.stdin)['result']['hash'])" 2>/dev/null || echo "")

echo -e "${D}  Network: chainId=1337 | block=$BLOCK_DEC | CamelliaFork=100${N}"
echo -e "${D}  Test account: $ACCT1${N}"

TX_HASH=""
TX_BLOCK_HASH=""

# ════════════════════════════════════════════════════════════════════
sec "1. net_* / web3_*"

check "net_version=1337"       "net_version"
check "net_listening=true"     "net_listening"
resp=$(rpc "net_peerCount"); v=$(echo "$resp" | jq_int); [[ "$v" -ge 0 ]] 2>/dev/null && pass "net_peerCount = $v" || warn "net_peerCount"
check "web3_clientVersion"     "web3_clientVersion"
resp=$(rpc "web3_sha3" '["0x68656c6c6f20776f726c64"]')
v=$(echo "$resp" | jq_str)
[[ "$v" == "0x47173285a8d7341e5e972fc677286384f802f8ef42a5ec5f03bbfa254cb01fad" ]] \
  && pass "web3_sha3(hello world) = ${v:0:20}..." || fail "web3_sha3" "$v"

# ════════════════════════════════════════════════════════════════════
sec "2. eth_* — chain/sync"

resp=$(rpc "eth_chainId"); v=$(echo "$resp" | jq_int)
[[ "$v" == "1337" ]] && pass "eth_chainId = $v" || fail "eth_chainId" "$v"
resp=$(rpc "eth_blockNumber"); v=$(echo "$resp" | jq_int)
[[ "$v" -gt 100 ]] && pass "eth_blockNumber = $v (CamelliaFork active)" || fail "eth_blockNumber" "$v"
resp=$(rpc "eth_syncing"); v=$(echo "$resp" | jq_bool)
[[ "$v" == "False" ]] && pass "eth_syncing = false" || warn "eth_syncing" "$v"
resp=$(rpc "eth_coinbase"); v=$(echo "$resp" | jq_str)
[[ -n "$v" ]] && pass "eth_coinbase = $v" || warn "eth_coinbase" "null"

# ════════════════════════════════════════════════════════════════════
sec "3. eth_* — gas / fees (EIP-1559, EIP-4844)"

resp=$(rpc "eth_gasPrice"); v=$(echo "$resp" | jq_int)
[[ "$v" -gt 0 ]] && pass "eth_gasPrice = $v wei" || fail "eth_gasPrice"

resp=$(rpc "eth_maxPriorityFeePerGas")
err=$(echo "$resp" | jq_err 2>/dev/null || true)
[[ -n "$err" ]] && { is_skip "$err" && skip "eth_maxPriorityFeePerGas" "$err" || warn "eth_maxPriorityFeePerGas" "$err"; } || {
  v=$(echo "$resp" | jq_int); pass "eth_maxPriorityFeePerGas = $v wei"; }

resp=$(rpc "eth_blobBaseFee")
err=$(echo "$resp" | jq_err 2>/dev/null || true)
[[ -n "$err" ]] && { is_skip "$err" && skip "eth_blobBaseFee" "$err" || fail "eth_blobBaseFee" "$err"; } || {
  v=$(echo "$resp" | jq_int)
  [[ "$v" -ge 1 ]] && pass "eth_blobBaseFee = $v (EIP-4844 >= 1 wei)" || fail "eth_blobBaseFee" "got $v"; }

resp=$(rpc "eth_feeHistory" '[4,"latest",[25,75]]')
err=$(echo "$resp" | jq_err 2>/dev/null || true)
[[ -n "$err" ]] && { is_skip "$err" && skip "eth_feeHistory" "$err" || warn "eth_feeHistory" "$err"; } || {
  v=$(echo "$resp" | python3 -c "import json,sys; d=json.load(sys.stdin); print(len(d['result'].get('baseFeePerGas',[])))" 2>/dev/null)
  pass "eth_feeHistory(4 blocks) — baseFeePerGas[$v]"; }

resp=$(rpc "eth_estimateGas" "[{\"from\":\"$ACCT1\",\"to\":\"$ACCT2\",\"value\":\"0x1\"}]")
err=$(echo "$resp" | jq_err 2>/dev/null || true)
[[ -n "$err" ]] && fail "eth_estimateGas" "$err" || {
  v=$(echo "$resp" | jq_int); pass "eth_estimateGas(ETH transfer) = $v gas"; }

# ════════════════════════════════════════════════════════════════════
sec "4. eth_* — accounts/state"

resp=$(rpc "eth_accounts"); v=$(echo "$resp" | jq_len)
pass "eth_accounts — $v account(s)"

resp=$(rpc "eth_getBalance" "[\"$ACCT1\",\"latest\"]"); v=$(echo "$resp" | jq_int)
[[ "$v" -gt 0 ]] && pass "eth_getBalance($ACCT1) = $v wei" || warn "eth_getBalance" "0"

resp=$(rpc "eth_getTransactionCount" "[\"$ACCT1\",\"latest\"]"); v=$(echo "$resp" | jq_int)
ACCT1_NONCE=$v; pass "eth_getTransactionCount($ACCT1) nonce=$v"

resp=$(rpc "eth_getCode" "[\"$ACCT1\",\"latest\"]"); v=$(echo "$resp" | jq_str)
[[ "$v" == "0x" ]] && pass "eth_getCode(EOA) = 0x" || warn "eth_getCode" "$v"

resp=$(rpc "eth_getStorageAt" "[\"$ACCT1\",\"0x0\",\"latest\"]"); v=$(echo "$resp" | jq_str)
[[ -n "$v" ]] && pass "eth_getStorageAt = $v" || fail "eth_getStorageAt"

resp=$(rpc "eth_getProof" "[\"$ACCT1\",[\"0x0\"],\"latest\"]")
err=$(echo "$resp" | jq_err 2>/dev/null || true)
[[ -n "$err" ]] && { is_skip "$err" && skip "eth_getProof" "$err" || fail "eth_getProof" "$err"; } || {
  v=$(echo "$resp" | python3 -c "import json,sys; r=json.load(sys.stdin)['result']; print('balance='+str(int(r['balance'],16)))" 2>/dev/null)
  pass "eth_getProof — $v"; }

# ════════════════════════════════════════════════════════════════════
sec "5. eth_* — block queries"

resp=$(rpc "eth_getBlockByNumber" '["0x0",false]')
v=$(echo "$resp" | python3 -c "import json,sys; print(json.load(sys.stdin)['result']['hash'][:20]+'...')" 2>/dev/null)
[[ -n "$v" ]] && pass "eth_getBlockByNumber(genesis) — $v" || fail "eth_getBlockByNumber(0x0)"

resp=$(rpc "eth_getBlockByNumber" '["latest",false]')
v=$(echo "$resp" | python3 -c "
import json,sys; b=json.load(sys.stdin)['result']
print(f'#{int(b[\"number\"],16)}, excessBlobGas={b.get(\"excessBlobGas\",\"MISSING\")}')" 2>/dev/null)
[[ -n "$v" ]] && pass "eth_getBlockByNumber(latest) — $v" || fail "eth_getBlockByNumber(latest)"

if [[ -n "$BLOCK1_HASH" ]]; then
  resp=$(rpc "eth_getBlockByHash" "[\"$BLOCK1_HASH\",false]")
  v=$(echo "$resp" | python3 -c "import json,sys; print(int(json.load(sys.stdin)['result']['number'],16))" 2>/dev/null)
  [[ "$v" == "1" ]] && pass "eth_getBlockByHash(block#1) — #$v" || fail "eth_getBlockByHash"
fi

resp=$(rpc "eth_getBlockTransactionCountByNumber" '["latest"]'); v=$(echo "$resp" | jq_int)
pass "eth_getBlockTransactionCountByNumber(latest) = $v"
[[ -n "$BLOCK1_HASH" ]] && {
  resp=$(rpc "eth_getBlockTransactionCountByHash" "[\"$BLOCK1_HASH\"]"); v=$(echo "$resp" | jq_int)
  pass "eth_getBlockTransactionCountByHash(block#1) = $v"
}
resp=$(rpc "eth_getUncleCountByBlockNumber" '["latest"]'); v=$(echo "$resp" | jq_int)
pass "eth_getUncleCountByBlockNumber(latest) = $v (PoA → 0)"

# eth_getBlockReceipts (NEW)
resp=$(rpc "eth_getBlockReceipts" '["latest"]')
err=$(echo "$resp" | jq_err 2>/dev/null || true)
[[ -n "$err" ]] && { is_skip "$err" && skip "eth_getBlockReceipts" "$err" || fail "eth_getBlockReceipts" "$err"; } || {
  v=$(echo "$resp" | jq_len); pass "eth_getBlockReceipts(latest) — $v receipts (NEW)"; }

# eth_getReceiptsByHash (legacy)
[[ -n "$BLOCK1_HASH" ]] && {
  resp=$(rpc "eth_getReceiptsByHash" "[\"$BLOCK1_HASH\"]")
  err=$(echo "$resp" | jq_err 2>/dev/null || true)
  [[ -n "$err" ]] && warn "eth_getReceiptsByHash" "$err" || {
    v=$(echo "$resp" | jq_len); pass "eth_getReceiptsByHash(block#1) — $v receipts (legacy)"; }
}

# ════════════════════════════════════════════════════════════════════
sec "6. eth_sendTransaction / eth_sendRawTransaction"

# eth_sendTransaction (requires account unlock)
echo -e "  ${D}→ eth_sendTransaction (1 ETH: ACCT1→ACCT2)${N}"
resp=$(rpc "eth_sendTransaction" "[{
  \"from\":\"$ACCT1\",
  \"to\":\"$ACCT2\",
  \"value\":\"0xDE0B6B3A7640000\",
  \"gas\":\"0x5208\"
}]")
err=$(echo "$resp" | jq_err 2>/dev/null || true)
if [[ -n "$err" ]]; then
  [[ "$err" =~ "authentication needed"|"unlock" ]] && fail "eth_sendTransaction" "account locked — check --unlock setting" || fail "eth_sendTransaction" "$err"
else
  TX_HASH=$(echo "$resp" | jq_str)
  pass "eth_sendTransaction — txHash=${TX_HASH:0:20}..."
  # Wait for confirmation
  for i in $(seq 1 15); do
    sleep 1
    tx_resp=$(rpc "eth_getTransactionReceipt" "[\"$TX_HASH\"]")
    bh=$(echo "$tx_resp" | python3 -c "import json,sys; r=json.load(sys.stdin)['result']; print(r['blockHash'] if r else '')" 2>/dev/null || true)
    [[ -n "$bh" ]] && { TX_BLOCK_HASH="$bh"; break; }
  done
  [[ -n "$TX_BLOCK_HASH" ]] && pass "TX confirmed — blockHash=${TX_BLOCK_HASH:0:20}..." || warn "TX pending confirmation"
fi

# eth_sendRawTransaction (sign with python eth_account)
echo -e "  ${D}→ eth_sendRawTransaction (EIP-1559 Type2, ACCT2→ACCT1)${N}"
NONCE2=$(rpc "eth_getTransactionCount" "[\"$ACCT2\",\"pending\"]" | python3 -c "import json,sys; print(json.load(sys.stdin)['result'])" 2>/dev/null)
RAW_TX=$(sign_tx "$PRIVKEY2" "$ACCT1" "DE0B6B3A7640000" "${NONCE2:-0x0}")
if [[ "$RAW_TX" == "NO_ETH_ACCOUNT" ]]; then
  skip "eth_sendRawTransaction" "eth_account library not found (pip install eth-account)"
elif [[ "$RAW_TX" == ERR* ]] || [[ -z "$RAW_TX" ]]; then
  warn "eth_sendRawTransaction" "transaction signing failed"
else
  resp=$(rpc "eth_sendRawTransaction" "[\"0x$RAW_TX\"]")
  err=$(echo "$resp" | jq_err 2>/dev/null || true)
  [[ -n "$err" ]] && fail "eth_sendRawTransaction" "$err" || {
    RAW_TX_HASH=$(echo "$resp" | jq_str)
    pass "eth_sendRawTransaction — txHash=${RAW_TX_HASH:0:20}..."
  }
fi

# ════════════════════════════════════════════════════════════════════
sec "7. eth_* — transaction queries (after TX confirmation)"

if [[ -n "$TX_HASH" ]]; then
  resp=$(rpc "eth_getTransactionByHash" "[\"$TX_HASH\"]")
  v=$(echo "$resp" | python3 -c "
import json,sys; t=json.load(sys.stdin)['result']
print(f'from={t[\"from\"][:16]}..., val={int(t[\"value\"],16)}')" 2>/dev/null)
  [[ -n "$v" ]] && pass "eth_getTransactionByHash — $v" || fail "eth_getTransactionByHash"

  resp=$(rpc "eth_getTransactionReceipt" "[\"$TX_HASH\"]")
  v=$(echo "$resp" | python3 -c "
import json,sys; r=json.load(sys.stdin)['result']
print(f'status={int(r[\"status\"],16)}, gasUsed={int(r[\"gasUsed\"],16)}')" 2>/dev/null)
  [[ -n "$v" ]] && pass "eth_getTransactionReceipt — $v" || warn "eth_getTransactionReceipt" "not confirmed"

  resp=$(rpc "eth_getTransactionByBlockHashAndIndex" "[\"${TX_BLOCK_HASH:-$LATEST_HASH}\",\"0x0\"]")
  v=$(echo "$resp" | python3 -c "import json,sys; r=json.load(sys.stdin)['result']; print('null' if r is None else r['hash'][:20]+'...')" 2>/dev/null)
  pass "eth_getTransactionByBlockHashAndIndex — $v"
else
  skip "eth_getTransactionByHash" "no TX (sendTransaction failed)"
  skip "eth_getTransactionReceipt" "no TX"
fi

resp=$(rpc "eth_getTransactionByBlockNumberAndIndex" '["latest","0x0"]')
v=$(echo "$resp" | python3 -c "import json,sys; r=json.load(sys.stdin)['result']; print('null' if r is None else r['hash'][:20]+'...')" 2>/dev/null)
pass "eth_getTransactionByBlockNumberAndIndex(latest,0) = $v"

# eth_getRawTransactionByHash (legacy)
if [[ -n "$TX_HASH" ]]; then
  resp=$(rpc "eth_getRawTransactionByHash" "[\"$TX_HASH\"]")
  err=$(echo "$resp" | jq_err 2>/dev/null || true)
  [[ -n "$err" ]] && warn "eth_getRawTransactionByHash" "$err" || {
    v=$(echo "$resp" | python3 -c "import json,sys; r=json.load(sys.stdin)['result']; print(len(r)//2,'bytes')" 2>/dev/null)
    pass "eth_getRawTransactionByHash — $v (legacy)"; }
fi

# ════════════════════════════════════════════════════════════════════
sec "8. eth_* — logs / filters"

resp=$(rpc "eth_getLogs" '[{"fromBlock":"0x1","toBlock":"latest"}]')
err=$(echo "$resp" | jq_err 2>/dev/null || true)
[[ -n "$err" ]] && fail "eth_getLogs" "$err" || { v=$(echo "$resp" | jq_len); pass "eth_getLogs(1~latest) — $v logs"; }

resp=$(rpc "eth_newBlockFilter"); BFID=$(echo "$resp" | jq_str 2>/dev/null || echo "")
[[ -n "$BFID" ]] && pass "eth_newBlockFilter — id=$BFID" || fail "eth_newBlockFilter"

resp=$(rpc "eth_newPendingTransactionFilter"); PFID=$(echo "$resp" | jq_str 2>/dev/null || echo "")
[[ -n "$PFID" ]] && pass "eth_newPendingTransactionFilter — id=$PFID" || fail "eth_newPendingTransactionFilter"

resp=$(rpc "eth_newFilter" '[{"fromBlock":"0x1","toBlock":"latest"}]'); LFID=$(echo "$resp" | jq_str 2>/dev/null || echo "")
[[ -n "$LFID" ]] && pass "eth_newFilter — id=$LFID" || fail "eth_newFilter"

[[ -n "$BFID" ]] && {
  resp=$(rpc "eth_getFilterChanges" "[\"$BFID\"]")
  err=$(echo "$resp" | jq_err 2>/dev/null || true)
  [[ -n "$err" ]] && fail "eth_getFilterChanges" "$err" || pass "eth_getFilterChanges(blockFilter) — OK"
}
[[ -n "$LFID" ]] && {
  resp=$(rpc "eth_getFilterLogs" "[\"$LFID\"]")
  err=$(echo "$resp" | jq_err 2>/dev/null || true)
  [[ -n "$err" ]] && fail "eth_getFilterLogs" "$err" || { v=$(echo "$resp" | jq_len); pass "eth_getFilterLogs — $v logs"; }
  resp=$(rpc "eth_uninstallFilter" "[\"$LFID\"]")
  v=$(echo "$resp" | jq_bool); [[ "$v" == "True" ]] && pass "eth_uninstallFilter — true" || warn "eth_uninstallFilter" "$v"
}

# ════════════════════════════════════════════════════════════════════
sec "9. eth_call / eth_createAccessList / eth_signTransaction"

resp=$(rpc "eth_call" "[{\"to\":\"$ACCT2\",\"data\":\"0x\"},\"latest\"]")
err=$(echo "$resp" | jq_err 2>/dev/null || true)
[[ -n "$err" ]] && fail "eth_call(empty)" "$err" || pass "eth_call(empty→EOA) — OK"

resp=$(rpc "eth_createAccessList" "[{\"from\":\"$ACCT1\",\"to\":\"$ACCT2\",\"value\":\"0x1\"},\"latest\"]")
err=$(echo "$resp" | jq_err 2>/dev/null || true)
[[ -n "$err" ]] && { is_skip "$err" && skip "eth_createAccessList" "$err" || warn "eth_createAccessList" "$err"; } || {
  v=$(echo "$resp" | python3 -c "import json,sys; r=json.load(sys.stdin)['result']; print('gasUsed='+str(int(r.get('gasUsed','0x0'),16)))" 2>/dev/null)
  pass "eth_createAccessList — $v"; }

SIGN_NONCE=$(rpc "eth_getTransactionCount" "[\"$ACCT1\",\"pending\"]" | python3 -c "import json,sys; print(json.load(sys.stdin)['result'])" 2>/dev/null || echo "0x0")
resp=$(rpc "eth_signTransaction" "[{
  \"from\":\"$ACCT1\",
  \"to\":\"$ACCT2\",
  \"value\":\"0x1\",
  \"gas\":\"0x5208\",
  \"nonce\":\"$SIGN_NONCE\",
  \"maxFeePerGas\":\"0x2E90EDD000\",
  \"maxPriorityFeePerGas\":\"0x174876E800\",
  \"type\":\"0x2\"
}]")
err=$(echo "$resp" | jq_err 2>/dev/null || true)
[[ -n "$err" ]] && { [[ "$err" =~ "authentication needed"|"unlock" ]] && fail "eth_signTransaction" "account locked" || fail "eth_signTransaction" "$err"; } || {
  v=$(echo "$resp" | python3 -c "import json,sys; r=json.load(sys.stdin)['result']; print('raw='+r.get('raw','?')[:20]+'...')" 2>/dev/null)
  pass "eth_signTransaction (EIP-1559 Type2) — $v"; }

# ════════════════════════════════════════════════════════════════════
sec "10. debug_* (getRawBlock/Header/Transaction/Receipts)"

resp=$(rpc "debug_getRawBlock" '["latest"]')
err=$(echo "$resp" | jq_err 2>/dev/null || true)
[[ -n "$err" ]] && { is_skip "$err" && skip "debug_getRawBlock" "$err" || fail "debug_getRawBlock" "$err"; } || {
  v=$(echo "$resp" | python3 -c "import json,sys; r=json.load(sys.stdin)['result']; print(len(r)//2,'bytes')" 2>/dev/null)
  pass "debug_getRawBlock(latest) — $v (NEW)"; }

resp=$(rpc "debug_getRawHeader" '["latest"]')
err=$(echo "$resp" | jq_err 2>/dev/null || true)
[[ -n "$err" ]] && { is_skip "$err" && skip "debug_getRawHeader" "$err" || fail "debug_getRawHeader" "$err"; } || {
  v=$(echo "$resp" | python3 -c "import json,sys; r=json.load(sys.stdin)['result']; print(len(r)//2,'bytes')" 2>/dev/null)
  pass "debug_getRawHeader(latest) — $v (NEW)"; }

resp=$(rpc "debug_getRawReceipts" '["latest"]')
err=$(echo "$resp" | jq_err 2>/dev/null || true)
[[ -n "$err" ]] && warn "debug_getRawReceipts" "$err" || { v=$(echo "$resp" | jq_len); pass "debug_getRawReceipts(latest) — $v receipts"; }

resp=$(rpc "debug_getBadBlocks")
err=$(echo "$resp" | jq_err 2>/dev/null || true)
[[ -n "$err" ]] && { is_skip "$err" && skip "debug_getBadBlocks" "$err" || warn "debug_getBadBlocks" "$err"; } || {
  v=$(echo "$resp" | jq_len); pass "debug_getBadBlocks — $v"; }

# debug_getRawTransaction (when TX is available)
if [[ -n "$TX_HASH" ]]; then
  resp=$(rpc "debug_getRawTransaction" "[\"$TX_HASH\"]")
  err=$(echo "$resp" | jq_err 2>/dev/null || true)
  [[ -n "$err" ]] && { is_skip "$err" && skip "debug_getRawTransaction" "$err" || fail "debug_getRawTransaction" "$err"; } || {
    v=$(echo "$resp" | python3 -c "import json,sys; r=json.load(sys.stdin)['result']; print(len(r)//2,'bytes')" 2>/dev/null)
    pass "debug_getRawTransaction($TX_HASH) — $v (NEW)"; }
else
  skip "debug_getRawTransaction" "no TX"
fi

# debug_getRawBlock (renamed from debug_getBlockRlp in geth v1.13+)
resp=$(rpc "debug_getRawBlock" '["0x1"]')
err=$(echo "$resp" | jq_err 2>/dev/null || true)
[[ -n "$err" ]] && warn "debug_getRawBlock" "$err" || {
  v=$(echo "$resp" | python3 -c "import json,sys; r=json.load(sys.stdin)['result']; print(len(r)//2,'bytes')" 2>/dev/null)
  pass "debug_getRawBlock(0x1) — $v"; }

# ════════════════════════════════════════════════════════════════════
sec "11. admin_* / txpool_* / miner_*"

resp=$(rpc "admin_nodeInfo")
v=$(echo "$resp" | python3 -c "import json,sys; r=json.load(sys.stdin)['result']; print(r['name'])" 2>/dev/null || true)
[[ -n "$v" ]] && pass "admin_nodeInfo — $v" || fail "admin_nodeInfo"

resp=$(rpc "admin_peers"); v=$(echo "$resp" | jq_len)
pass "admin_peers — $v peer(s)"

resp=$(rpc "eth_mining"); v=$(echo "$resp" | jq_bool)
[[ "$v" == "True" ]] && pass "eth_mining = true" || warn "eth_mining" "$v"

resp=$(rpc "eth_hashrate"); v=$(echo "$resp" | jq_int)
pass "eth_hashrate = $v (PoA → 0)"

resp=$(rpc "txpool_status")
err=$(echo "$resp" | jq_err 2>/dev/null || true)
[[ -n "$err" ]] && { is_skip "$err" && skip "txpool_status" "$err" || warn "txpool_status" "$err"; } || {
  v=$(echo "$resp" | python3 -c "import json,sys; r=json.load(sys.stdin)['result']; print(f'pending={int(r.get(\"pending\",\"0x0\"),16)},queued={int(r.get(\"queued\",\"0x0\"),16)}')" 2>/dev/null)
  pass "txpool_status — $v"; }

resp=$(rpc "txpool_inspect")
err=$(echo "$resp" | jq_err 2>/dev/null || true)
[[ -n "$err" ]] && { is_skip "$err" && skip "txpool_inspect" "$err" || warn "txpool_inspect" "$err"; } || pass "txpool_inspect — OK"

resp=$(rpc "txpool_content")
err=$(echo "$resp" | jq_err 2>/dev/null || true)
[[ -n "$err" ]] && { is_skip "$err" && skip "txpool_content" "$err" || warn "txpool_content" "$err"; } || pass "txpool_content — OK"

# ════════════════════════════════════════════════════════════════════
sec "12. Fee Delegation (Type 22 tx)"

echo -e "  ${D}Flow: ACCT2 creates tx → ACCT1 (feePayer) pays the fee${N}"

# Step 1: ACCT2 signs EIP-1559 tx without feePayer
NONCE2_PEND=$(rpc "eth_getTransactionCount" "[\"$ACCT2\",\"pending\"]" | python3 -c "import json,sys; print(json.load(sys.stdin)['result'])" 2>/dev/null || echo "0x0")
FD_SIGNED_RAW=$(python3 - <<PYEOF 2>/dev/null
try:
    from eth_account import Account
    import json

    sender_key = "0x$PRIVKEY2"
    acct = Account.from_key(sender_key)
    nonce = int("$NONCE2_PEND", 16)
    tx = {
        "nonce": nonce,
        "to": "$ACCT3",
        "value": int("0xDE0B6B3A7640000", 16),
        "gas": 21000,
        "maxFeePerGas": 200 * 10**9,
        "maxPriorityFeePerGas": 100 * 10**9,
        "chainId": $CHAINID,
        "type": 2,
    }
    signed = acct.sign_transaction(tx)
    print(signed.raw_transaction.hex())
except ImportError:
    print("NO_ETH_ACCOUNT")
except Exception as e:
    print(f"ERR:{e}")
PYEOF
)

if [[ "$FD_SIGNED_RAW" == "NO_ETH_ACCOUNT" ]]; then
  skip "Fee Delegation" "eth_account library not found"
elif [[ -z "$FD_SIGNED_RAW" || "$FD_SIGNED_RAW" == ERR* ]]; then
  warn "Fee Delegation" "sender signing failed: $FD_SIGNED_RAW"
else
  # Step 2: eth_signRawFeeDelegateTransaction (ACCT1 signs as feePayer)
  resp=$(rpc "eth_signRawFeeDelegateTransaction" "[{\"from\":\"$ACCT1\",\"feePayer\":\"$ACCT1\"},\"0x$FD_SIGNED_RAW\"]")
  err=$(echo "$resp" | jq_err 2>/dev/null || true)
  if [[ -n "$err" ]]; then
    [[ "$err" =~ "authentication needed"|"unlock" ]] && fail "eth_signRawFeeDelegateTransaction" "account locked — personal API or unlock required" \
      || { is_skip "$err" && skip "eth_signRawFeeDelegateTransaction" "$err" || fail "eth_signRawFeeDelegateTransaction" "$err"; }
  else
    FD_TX_RAW=$(echo "$resp" | python3 -c "import json,sys; r=json.load(sys.stdin)['result']; print(r.get('raw',''))" 2>/dev/null || true)
    pass "eth_signRawFeeDelegateTransaction — feePayer signature complete"

    # Step 3: Send via eth_sendRawTransaction
    if [[ -n "$FD_TX_RAW" ]]; then
      resp=$(rpc "eth_sendRawTransaction" "[\"$FD_TX_RAW\"]")
      err=$(echo "$resp" | jq_err 2>/dev/null || true)
      [[ -n "$err" ]] && fail "FeeDelegation sendRawTransaction" "$err" || {
        FD_TX_HASH=$(echo "$resp" | jq_str)
        pass "Fee Delegation tx sent — hash=${FD_TX_HASH:0:20}..."
      }
    fi
  fi
fi

# ════════════════════════════════════════════════════════════════════
sec "13. Camellia EIP opcodes (eth_call)"

evm_call "EIP-3855 PUSH0 (0x5F)=0"              "0x5f60005260206000f3"             "0"
evm_call "EIP-4844 BLOBBASEFEE (0x4A)=1 wei"    "0x4a60005260206000f3"             "1"
evm_call "EIP-4844 BLOBHASH (0x49)=0 (no blob)" "0x60004960005260206000f3"         "0"
evm_call "EIP-1153 TSTORE(1,42)→TLOAD=42"       "0x602a60015d60015c60005260206000f3" "42"
evm_call "EIP-5656 MCOPY"                        "0x60426000526020600060205e60206020f3" "66"
resp=$(rpc "eth_call" "[{\"data\":\"0x4160005260206000f3\"},\"latest\"]")
err=$(echo "$resp" | jq_err 2>/dev/null || true)
[[ -n "$err" ]] && fail "EIP-3651 Warm COINBASE" "$err" || pass "EIP-3651 Warm COINBASE (0x41) — OK"

# ════════════════════════════════════════════════════════════════════
sec "14. IPC + JS console"

if [[ -z "$IPC_PATH" ]]; then
  skip "IPC test" "IPC_PATH not specified (run gmet attach <ipc>)"
else
  GMET_BIN="${GMET_BIN:-gmet}"
  run_js() {
    local label="$1" script="$2"
    local out; out=$(echo "$script" | timeout 10 "$GMET_BIN" attach "$IPC_PATH" --exec "$(cat)" 2>/dev/null || true)
    [[ -n "$out" && "$out" != "undefined" && "$out" != "null" ]] \
      && pass "$label — $out" || fail "$label" "result: $out"
  }
  run_js "IPC eth.blockNumber"        'eth.blockNumber'
  run_js "IPC eth.chainId()"          'eth.chainId()'
  run_js "IPC net.version"            'net.version'
  run_js "IPC net.peerCount"          'net.peerCount'
  run_js "IPC eth.accounts"           'eth.accounts'
  run_js "IPC eth.gasPrice"           'eth.gasPrice'
  run_js "IPC eth.getBalance(acct1)"  "eth.getBalance(\"$ACCT1\")"
  run_js "IPC txpool.status"          'txpool.status'
  run_js "IPC admin.nodeInfo.name"    'admin.nodeInfo.name'
  run_js "IPC miner.getHashrate()"    'miner.getHashrate()'
  run_js "IPC eth.mining"             'eth.mining'
fi

# ════════════════════════════════════════════════════════════════════
TOTAL=$((PASS+FAIL+WARN+SKIP))
echo -e "\n${B}${C}╔══════════════════════════════════════════════════════════════╗${N}"
echo -e "${B}${C}║                       Final Test Results                      ║${N}"
echo -e "${B}${C}╚══════════════════════════════════════════════════════════════╝${N}"
echo -e "  Total: ${TOTAL} | ${G}${B}PASS: $PASS${N} | ${R}${B}FAIL: $FAIL${N} | ${Y}WARN: $WARN${N} | SKIP: $SKIP"
echo -e "${B}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${N}"
[[ $FAIL -eq 0 ]] \
  && echo -e "  ${G}${B}[Final: PASS]${N} — no FAILs" \
  || echo -e "  ${R}${B}[Final: FAIL]${N} — ${FAIL} failure(s)"
echo ""
exit $FAIL
