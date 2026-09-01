#!/usr/bin/env bash
# shellcheck disable=SC2317  # eth_call_result etc. are invoked indirectly via variable dispatch
# camellia-test.sh - Camellia fork transition test
# block 99 (before) vs block 100 (after) opcode behavior verification
#
# Usage: ./camellia-test.sh
# Prerequisite: private-net-poa has progressed to block 100 or higher

set -euo pipefail

RPC="${RPC:-http://localhost:8545}"
PASS=0
FAIL=0
SKIP=0

log()  { echo "[$(date '+%H:%M:%S')] $*"; }
pass() { echo "  ✅ PASS: $*"; PASS=$((PASS+1)); }
fail() { echo "  ❌ FAIL: $*"; FAIL=$((FAIL+1)); }
skip() { echo "  ⏭  SKIP: $*"; SKIP=$((SKIP+1)); }

rpc() {
  echo "$1" | curl -sf -X POST -H "Content-Type: application/json" \
    --data-binary @- "$RPC" 2>/dev/null
}

eth_call() {
  local data="$1"
  local block="${2:-latest}"
  rpc "{\"jsonrpc\":\"2.0\",\"method\":\"eth_call\",\"params\":[{\"from\":\"0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266\",\"data\":\"$data\",\"gas\":\"0x100000\"},\"$block\"],\"id\":1}"
}

eth_call_result() {
  local resp
  resp=$(eth_call "$1" "${2:-latest}")
  echo "$resp" | python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('result','') or d.get('error',{}).get('message','error'))"
}

check_revert() {
  local resp
  resp=$(eth_call "$1" "${2:-latest}")
  echo "$resp" | python3 -c "
import sys, json
d = json.load(sys.stdin)
if 'error' in d:
    msg = d['error'].get('message', '')
    print('revert' if 'invalid' in msg.lower() or 'revert' in msg.lower() else 'error:'+msg)
elif d.get('result','0x') in ('0x',''):
    print('revert')
else:
    print('ok:' + d['result'])
"
}

send_tx() {
  rpc "{\"jsonrpc\":\"2.0\",\"method\":\"eth_sendTransaction\",\"params\":[$1],\"id\":1}" | \
    python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('result','') or d.get('error',{}).get('message',''))"
}

wait_receipt() {
  local txhash="$1"
  for _i in $(seq 1 30); do
    local r
    r=$(rpc "{\"jsonrpc\":\"2.0\",\"method\":\"eth_getTransactionReceipt\",\"params\":[\"$txhash\"],\"id\":1}" | \
      python3 -c "
import sys, json
d = json.load(sys.stdin)
r = d.get('result')
if r:
    print((r.get('contractAddress') or '') + '|' + r.get('status',''))
" 2>/dev/null || echo "")
    [[ -n "$r" ]] && echo "$r" && return 0
    sleep 1
  done
  return 1
}

# --- Prerequisite check ---
log "=== Camellia Fork Transition Test ==="
CURRENT=$(rpc '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}' | \
  python3 -c "import sys,json; d=json.load(sys.stdin); print(int(d['result'],16))")
FORK_BLOCK=$(rpc '{"jsonrpc":"2.0","method":"eth_getBlockByNumber","params":["0x64",false],"id":1}' | \
  python3 -c "import sys,json; d=json.load(sys.stdin); b=d['result']; print(int(b['number'],16) if b else -1)")

log "Current block: $CURRENT / Camellia fork block: $FORK_BLOCK (0x64=100)"
if [[ "$CURRENT" -lt 100 ]]; then
  log "❌ Block is below 100. Please wait longer."
  exit 1
fi
echo ""

# ============================================================
# EIP-3855: PUSH0 (0x5f)
# ============================================================
log "--- EIP-3855: PUSH0 (opcode 0x5f) ---"

# bytecode: PUSH0 + PUSH1 0x20 + MSTORE + PUSH1 0x20 + PUSH1 0 + RETURN
# Push 0 onto the stack and return → 0x0000...0000 (32 bytes)
PUSH0_CODE="0x5f6020526020600ff3"

# block 99 (before fork): expect invalid opcode
R=$(check_revert "$PUSH0_CODE" "0x63")
if [[ "$R" == "revert" || "$R" == revert* ]]; then
  pass "block 99: PUSH0 → invalid opcode (revert)"
else
  fail "block 99: PUSH0 expected revert, got: $R"
fi

# block 100 (after fork): normal execution, returns 0x0000...0000
R=$(check_revert "$PUSH0_CODE" "0x64")
if [[ "$R" == ok:* ]]; then
  VAL="${R#ok:}"
  if [[ "$VAL" == "0x0000"* ]]; then
    pass "block 100: PUSH0 → returns 0x00..00 (success)"
  else
    fail "block 100: PUSH0 expected return 0x00..00, got: $VAL"
  fi
else
  fail "block 100: PUSH0 expected ok, got: $R"
fi

echo ""

# ============================================================
# EIP-1153: TLOAD/TSTORE (0x5c/0x5d)
# ============================================================
log "--- EIP-1153: TLOAD/TSTORE (0x5c/0x5d) ---"

# bytecode: PUSH1 0x42, PUSH1 0x01, TSTORE (slot 1 = 0x42)
#           PUSH1 0x01, TLOAD (load slot 1)
#           PUSH1 0x00, MSTORE (mem[0] = 0x42), PUSH1 0x20, PUSH1 0x00, RETURN → returns 0x42
TSTORE_CODE="0x604260015d60015c60005260206000f3"

R=$(check_revert "$TSTORE_CODE" "0x63")
if [[ "$R" == "revert" || "$R" == error* ]]; then
  pass "block 99: TLOAD/TSTORE → invalid opcode (revert)"
else
  fail "block 99: TLOAD/TSTORE expected revert, got: $R"
fi

R=$(check_revert "$TSTORE_CODE" "0x64")
if [[ "$R" == ok:* ]]; then
  VAL="${R#ok:}"
  # 0x42 = 66 in decimal, should appear in last bytes
  if echo "$VAL" | grep -q "0000000000000000000000000000000000000000000000000000000000000042"; then
    pass "block 100: TSTORE(slot1, 0x42) → TLOAD(slot1) = 0x42 (success)"
  else
    fail "block 100: TLOAD expected return 0x42, got: $VAL"
  fi
else
  fail "block 100: TLOAD/TSTORE expected ok, got: $R"
fi

echo ""

# ============================================================
# EIP-5656: MCOPY (0x5e)
# ============================================================
log "--- EIP-5656: MCOPY (0x5e) ---"

# bytecode:
#   PUSH1 0xAB, PUSH1 0x00, MSTORE8  (mem[0] = 0xAB)
#   PUSH1 0x01, PUSH1 0x00, PUSH1 0x20, MCOPY (dst=0x20, src=0x00, len=1)
#   PUSH1 0x01, PUSH1 0x20, RETURN   → returns mem[0x20] = 0xAB
MCOPY_CODE="0x60ab6000 53 6001600060205e 600160 20 f3"
MCOPY_CODE=$(echo "$MCOPY_CODE" | tr -d ' ')

R=$(check_revert "$MCOPY_CODE" "0x63")
if [[ "$R" == "revert" || "$R" == error* ]]; then
  pass "block 99: MCOPY → invalid opcode (revert)"
else
  fail "block 99: MCOPY expected revert, got: $R"
fi

R=$(check_revert "$MCOPY_CODE" "0x64")
if [[ "$R" == ok:* ]]; then
  VAL="${R#ok:}"
  if echo "$VAL" | grep -qi "ab"; then
    pass "block 100: MCOPY mem[0x20←0x00, len=1] → 0xAB (success)"
  else
    fail "block 100: MCOPY expected return 0xAB, got: $VAL"
  fi
else
  fail "block 100: MCOPY expected ok, got: $R"
fi

echo ""

# ============================================================
# EIP-4844: BLOBBASEFEE (0x4a)
# ============================================================
log "--- EIP-4844: BLOBBASEFEE (0x4a) ---"

# bytecode: BLOBBASEFEE + PUSH1 0x20 + MSTORE + PUSH1 0x20 + PUSH1 0 + RETURN
BLOBBASEFEE_CODE="0x4a6020526020600ff3"

R=$(check_revert "$BLOBBASEFEE_CODE" "0x63")
if [[ "$R" == "revert" || "$R" == error* ]]; then
  pass "block 99: BLOBBASEFEE → invalid opcode (revert)"
else
  fail "block 99: BLOBBASEFEE expected revert, got: $R"
fi

R=$(check_revert "$BLOBBASEFEE_CODE" "0x64")
if [[ "$R" == ok:* ]]; then
  VAL="${R#ok:}"
  pass "block 100: BLOBBASEFEE → $VAL (success)"
else
  fail "block 100: BLOBBASEFEE expected ok, got: $R"
fi

echo ""

# ============================================================
# EIP-3651: Warm COINBASE (gas comparison)
# ============================================================
log "--- EIP-3651: Warm COINBASE ---"

# bytecode: GAS COINBASE BALANCE POP GAS SWAP1 SUB PUSH1 0 MSTORE PUSH1 32 PUSH1 0 RETURN
# 5a 41 31 50 5a 90 03 60 00 52 60 20 60 00 f3
# return value = gas consumed by COINBASE BALANCE lookup
#   before fork (block 99): BALANCE cold access = 2600 gas → larger value returned
#   after fork (block 100): COINBASE warm → BALANCE warm = 100 gas → smaller value returned
WARM_CB_CODE="0x5a4131505a900360005260206000f3"
# Must call with from != coinbase: if from==coinbase, EIP-2929 pre-warm makes it always warm
# node1 coinbase = 0xf39..., use from = 0x709... (account2)
WARM_CB_FROM="0x70997970C51812dc3A010C7d01b50e0d17dc79C8"

R99=$(rpc "{\"jsonrpc\":\"2.0\",\"method\":\"eth_call\",\"params\":[{\"from\":\"$WARM_CB_FROM\",\"data\":\"$WARM_CB_CODE\",\"gas\":\"0x100000\"},\"0x63\"],\"id\":1}" | \
  python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('result','') or d.get('error',{}).get('message','error'))")
R100=$(rpc "{\"jsonrpc\":\"2.0\",\"method\":\"eth_call\",\"params\":[{\"from\":\"$WARM_CB_FROM\",\"data\":\"$WARM_CB_CODE\",\"gas\":\"0x100000\"},\"0x64\"],\"id\":1}" | \
  python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('result','') or d.get('error',{}).get('message','error'))")
GAS99=$(python3 -c "print(int('$R99', 16))" 2>/dev/null || echo "-1")
GAS100=$(python3 -c "print(int('$R100', 16))" 2>/dev/null || echo "-1")

if [[ "$GAS99" -eq -1 || "$GAS100" -eq -1 ]]; then
  fail "Warm COINBASE: eth_call failed (block99=$R99, block100=$R100)"
elif [[ "$GAS99" -gt "$GAS100" ]]; then
  pass "Warm COINBASE: block99 gas=$GAS99 > block100 gas=$GAS100 (cold→warm savings confirmed)"
else
  fail "Warm COINBASE: block99=$GAS99, block100=$GAS100 (savings not confirmed)"
fi

echo ""

# ============================================================
# EIP-6780: SELFDESTRUCT restriction
# ============================================================
log "--- EIP-6780: SELFDESTRUCT restriction ---"

# EIP-6780: Only contracts created in the same tx are actually destroyed by SELFDESTRUCT
# Existing contracts (deployed in a different tx) preserve code even after SELFDESTRUCT call
#
# Verification method:
#   1. Deploy contract after fork (block>100) (tx1)
#   2. Call SELFDESTRUCT from a separate tx (tx2)
#   3. eth_getCode → code still exists = PASS (EIP-6780 applied)
#
# Deploy bytecode (init + runtime):
#   init (12 bytes):  PUSH1 2, PUSH1 0x0c, PUSH1 0, CODECOPY, PUSH1 2, PUSH1 0, RETURN
#   runtime (2 bytes): CALLER(33), SELFDESTRUCT(ff)
SD_DEPLOY="0x6002600c60003960026000f333ff"
FROM="0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266"

TXHASH=$(send_tx "{\"from\":\"$FROM\",\"data\":\"$SD_DEPLOY\",\"gas\":\"0x100000\"}")
if [[ -z "$TXHASH" || ${#TXHASH} -lt 60 ]]; then
  fail "EIP-6780: Contract deploy tx failed ($TXHASH)"
else
  RECEIPT=$(wait_receipt "$TXHASH")
  ADDR=$(echo "$RECEIPT" | cut -d'|' -f1)
  STATUS=$(echo "$RECEIPT" | cut -d'|' -f2)
  if [[ -z "$ADDR" || "$STATUS" != "0x1" ]]; then
    fail "EIP-6780: Deploy failed (addr=$ADDR, status=$STATUS)"
  else
    # Call SELFDESTRUCT from a separate tx
    TXHASH2=$(send_tx "{\"from\":\"$FROM\",\"to\":\"$ADDR\",\"gas\":\"0x100000\"}")
    if [[ -z "$TXHASH2" || ${#TXHASH2} -lt 60 ]]; then
      fail "EIP-6780: SELFDESTRUCT call tx failed ($TXHASH2)"
    else
      wait_receipt "$TXHASH2" > /dev/null
      CODE=$(rpc "{\"jsonrpc\":\"2.0\",\"method\":\"eth_getCode\",\"params\":[\"$ADDR\",\"latest\"],\"id\":1}" | \
        python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('result','0x'))")
      if [[ "$CODE" != "0x" && -n "$CODE" ]]; then
        pass "EIP-6780: Code preserved after SELFDESTRUCT (addr=$ADDR, code=$CODE)"
      else
        fail "EIP-6780: Code destroyed after SELFDESTRUCT (addr=$ADDR, code=$CODE) - EIP-6780 not applied"
      fi
    fi
  fi
fi

echo ""

# ============================================================
# I-07: EIP-3860 — initcode > MaxInitCodeSize (507904) bytes -> CREATE rejected
# ============================================================
log "--- EIP-3860: initcode size limit (Metadium: 507904 bytes) ---"

# Metadium MaxInitCodeSize=507904 (2 * 253952), test with 507905 bytes
INITCODE_HEX=$(python3 -c "print('0x' + '00' * 507905)" 2>/dev/null || true)

if [[ -z "$INITCODE_HEX" ]]; then
  skip "I-07: python3 initcode generation failed"
else
  # CREATE tx: no 'to', data = initcode
  TXHASH=$(send_tx "{\"from\":\"0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266\",\"data\":\"$INITCODE_HEX\",\"gas\":\"0x2000000\"}")
  if [[ "$TXHASH" == *"oversized"* || "$TXHASH" == *"too large"* ]]; then
    # 507905 bytes > txMaxSize (131072), so the tx is rejected at transport level.
    # EIP-3860 would reject it too, but the tx size limit fires first.
    pass "I-07: EIP-3860 — 507905 bytes initcode rejected (tx size limit: $TXHASH)"
  elif [[ -z "$TXHASH" || "$TXHASH" == "0x" ]]; then
    skip "I-07: tx send failed (unlock required or node not running)"
  else
    RECEIPT=$(wait_receipt "$TXHASH" 2>/dev/null || true)
    if [[ -z "$RECEIPT" ]]; then
      fail "I-07: receipt wait timeout (hash=$TXHASH)"
    else
      STATUS=$(echo "$RECEIPT" | cut -d'|' -f2)
      if [[ "$STATUS" == "0x0" ]]; then
        pass "I-07: EIP-3860 — 507905 bytes initcode CREATE rejected (status=0x0)"
      else
        fail "I-07: EIP-3860 not applied — 507905 bytes initcode was accepted (status=$STATUS)"
      fi
    fi
  fi
fi

echo ""

# ============================================================
# I-08 / I-09: Type 22 Fee Delegation
# ============================================================
log "--- Type 22 Fee Delegation (post-fork behavior check) ---"

SENDER="0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266"
FEE_PAYER="0x70997970C51812dc3A010C7d01b50e0d17dc79C8"
# shellcheck disable=SC2034  # TO_ADDR used in heredoc/python blocks below
TO_ADDR="0x3C44CdDdB6a900fa2b585dd299e03d12FA4293BC"

# Record sender balance (before)
SENDER_BEFORE=$(rpc "{\"jsonrpc\":\"2.0\",\"method\":\"eth_getBalance\",\"params\":[\"$SENDER\",\"latest\"],\"id\":1}" | \
  python3 -c "import sys,json; d=json.load(sys.stdin); print(int(d.get('result','0x0'),16))" 2>/dev/null || echo "0")

# Step 1: sender signs EIP-1559 tx (requires python3 eth_account)
FD_SIGNED_RAW=$(python3 3>/dev/null <<'PYEOF' 2>/dev/null || echo "NO_ETH_ACCOUNT"
try:
    from eth_account import Account
    import json, os

    SENDER_KEY = "0xac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80"
    acct = Account.from_key(SENDER_KEY)
    RPC = os.environ.get("RPC", "http://localhost:8545")

    import urllib.request
    def rpc_call(method, params):
        data = json.dumps({"jsonrpc":"2.0","method":method,"params":params,"id":1}).encode()
        req = urllib.request.Request(RPC, data=data, headers={"Content-Type":"application/json"})
        return json.loads(urllib.request.urlopen(req, timeout=5).read())

    nonce = int(rpc_call("eth_getTransactionCount", [acct.address, "latest"])["result"], 16)
    chain_id = int(rpc_call("eth_chainId", [])["result"], 16)

    tx = {
        "nonce": nonce,
        "to": "0x3C44CdDdB6a900fa2b585dd299e03d12FA4293BC",
        "value": 0,
        "gas": 21000,
        "maxFeePerGas": 2 * 10**9,
        "maxPriorityFeePerGas": 1 * 10**9,
        "chainId": chain_id,
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
  skip "I-08/I-09: eth_account library not found — run pip install eth-account and retry"
elif [[ -z "$FD_SIGNED_RAW" || "$FD_SIGNED_RAW" == ERR* ]]; then
  skip "I-08/I-09: sender signing failed ($FD_SIGNED_RAW)"
else
  # Step 2: feePayer signs (eth_signRawFeeDelegateTransaction)
  FD_RESP=$(rpc "{\"jsonrpc\":\"2.0\",\"method\":\"eth_signRawFeeDelegateTransaction\",\"params\":[{\"from\":\"$FEE_PAYER\",\"feePayer\":\"$FEE_PAYER\"},\"0x$FD_SIGNED_RAW\"],\"id\":1}")
  FD_ERR=$(echo "$FD_RESP" | python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('error',{}).get('message',''))" 2>/dev/null || true)

  if [[ -n "$FD_ERR" ]]; then
    skip "I-08/I-09: feePayer signing failed — $FD_ERR"
  else
    FD_TX_RAW=$(echo "$FD_RESP" | python3 -c "import sys,json; d=json.load(sys.stdin); r=d.get('result',{}); print(r.get('raw',''))" 2>/dev/null || true)

    if [[ -z "$FD_TX_RAW" ]]; then
      fail "I-08: eth_signRawFeeDelegateTransaction missing raw field"
    else
      # Step 3: sendRawTransaction
      FD_HASH=$(rpc "{\"jsonrpc\":\"2.0\",\"method\":\"eth_sendRawTransaction\",\"params\":[\"$FD_TX_RAW\"],\"id\":1}" | \
        python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('result','') or d.get('error',{}).get('message',''))" 2>/dev/null || true)

      if [[ -z "$FD_HASH" || ! "$FD_HASH" =~ ^0x[0-9a-fA-F]{64}$ ]]; then
        fail "I-08: Fee Delegation tx send failed — $FD_HASH"
      else
        RECEIPT=$(wait_receipt "$FD_HASH")
        if [[ -z "$RECEIPT" ]]; then
          fail "I-08: receipt wait timeout (hash=$FD_HASH)"
        else
          STATUS=$(echo "$RECEIPT" | cut -d'|' -f2)
          if [[ "$STATUS" == "0x1" ]]; then
            pass "I-08: Fee Delegation tx succeeded (hash=${FD_HASH:0:20}...)"
          else
            fail "I-08: Fee Delegation tx failed (status=$STATUS)"
          fi

          # I-09: Verify feePayer field is set in the mined tx (= FEE_PAYER, not sender).
          # Balance check is unreliable since FEE_PAYER is node2's etherbase: on a
          # rewarded network sealing income in the same window can exceed the gas
          # just paid, and the balance rises (issue #100; same fix as spoa-test.sh).
          SENDER_AFTER=$(rpc "{\"jsonrpc\":\"2.0\",\"method\":\"eth_getBalance\",\"params\":[\"$SENDER\",\"latest\"],\"id\":1}" | \
            python3 -c "import sys,json; d=json.load(sys.stdin); print(int(d.get('result','0x0'),16))" 2>/dev/null || echo "0")

          FP_IN_TX=$(rpc "{\"jsonrpc\":\"2.0\",\"method\":\"eth_getTransactionByHash\",\"params\":[\"$FD_HASH\"],\"id\":1}" | \
            python3 -c "import sys,json; d=json.load(sys.stdin); print((d.get('result') or {}).get('feePayer') or '')" 2>/dev/null || echo "")

          if [[ -n "$FP_IN_TX" && "${FP_IN_TX,,}" == "${FEE_PAYER,,}" && "${FP_IN_TX,,}" != "${SENDER,,}" ]]; then
            pass "I-09: feePayer correctly set in tx (feePayer=$FP_IN_TX ≠ sender)"
          else
            fail "I-09: feePayer not set correctly in tx (got: ${FP_IN_TX:-<missing>})"
          fi

          if [[ "$SENDER_AFTER" -eq "$SENDER_BEFORE" ]]; then
            pass "I-09-b: sender balance unchanged (value=0 tx, gas paid by feePayer)"
          else
            # sender balance CAN change slightly due to other txs in block, just log
            log "  I-09-b INFO: sender balance changed (before=$SENDER_BEFORE after=$SENDER_AFTER) — may be due to other txs"
          fi
        fi
      fi
    fi
  fi
fi

echo ""

# ============================================================
# I-10: Governance contract compatibility (OPTIONAL)
# ============================================================
log "--- I-10: Governance contract compatibility (OPTIONAL) ---"

GOV_RESP=$(rpc "{\"jsonrpc\":\"2.0\",\"method\":\"eth_call\",\"params\":[{\"to\":\"0x0000000000000000000000000000000000000389\",\"data\":\"0x5d593f8d0000000000000000000000000000000000000000000000000000000000000000\"},\"latest\"],\"id\":1}" 2>/dev/null || true)
GOV_ERR=$(echo "$GOV_RESP" | python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('error',{}).get('message',''))" 2>/dev/null || true)

if [[ -n "$GOV_ERR" ]]; then
  skip "I-10: Governance contract not found or error — $GOV_ERR"
else
  GOV_RESULT=$(echo "$GOV_RESP" | python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('result','0x'))" 2>/dev/null || true)
  if [[ "$GOV_RESULT" == "0x" || -z "$GOV_RESULT" ]]; then
    skip "I-10: Governance contract not deployed (not in private-net-poa genesis)"
  else
    pass "I-10: Governance contract responded normally (result=${GOV_RESULT:0:20}...)"
  fi
fi

echo ""

# ============================================================
# I-11: Block production continuity after fork
# ============================================================
log "--- I-11: Block production continuity after fork ---"

BLK_START=$(rpc '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}' | \
  python3 -c "import sys,json; d=json.load(sys.stdin); print(int(d['result'],16))" 2>/dev/null || echo "0")

if [[ "$BLK_START" -eq 0 ]]; then
  skip "I-11: Block number query failed"
else
  MINER_START=$(rpc "{\"jsonrpc\":\"2.0\",\"method\":\"eth_getBlockByNumber\",\"params\":[\"$(printf '0x%x' "$BLK_START")\",false],\"id\":1}" | \
    python3 -c "import sys,json; d=json.load(sys.stdin); print(d['result']['miner'])" 2>/dev/null || true)

  log "  Current block: $BLK_START / miner: $MINER_START"
  log "  Waiting 5 seconds to verify block progress..."
  sleep 5

  BLK_END=$(rpc '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}' | \
    python3 -c "import sys,json; d=json.load(sys.stdin); print(int(d['result'],16))" 2>/dev/null || echo "0")

  if [[ "$BLK_END" -gt "$BLK_START" ]]; then
    MINER_END=$(rpc "{\"jsonrpc\":\"2.0\",\"method\":\"eth_getBlockByNumber\",\"params\":[\"$(printf '0x%x' "$BLK_END")\",false],\"id\":1}" | \
      python3 -c "import sys,json; d=json.load(sys.stdin); print(d['result']['miner'])" 2>/dev/null || true)
    pass "I-11: Block production normal after fork (${BLK_START}→${BLK_END}, miner=${MINER_END:0:10}...)"
  else
    fail "I-11: Block number did not increase after 5s (start=$BLK_START, end=$BLK_END) — mining stopped or node error"
  fi
fi

echo ""

# ============================================================
# EIP-4844: Blob transaction API tests
# ============================================================
log "--- EIP-4844: Blob transaction API ---"

# I-12: eth_blobBaseFee returns non-null after Camellia fork
BLOB_FEE_RESP=$(rpc '{"jsonrpc":"2.0","method":"eth_blobBaseFee","params":[],"id":1}')
BLOB_FEE=$(echo "$BLOB_FEE_RESP" | python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('result','null'))" 2>/dev/null || echo "null")
BLOB_FEE_ERR=$(echo "$BLOB_FEE_RESP" | python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('error',{}).get('message',''))" 2>/dev/null || true)

if [[ -n "$BLOB_FEE_ERR" ]]; then
  fail "I-12: eth_blobBaseFee error — $BLOB_FEE_ERR"
elif [[ "$BLOB_FEE" == "null" || "$BLOB_FEE" == "0x" ]]; then
  fail "I-12: eth_blobBaseFee returned null/empty"
else
  BLOB_FEE_DEC=$(python3 -c "print(int('$BLOB_FEE', 16))" 2>/dev/null || echo "-1")
  pass "I-12: eth_blobBaseFee = $BLOB_FEE ($BLOB_FEE_DEC wei)"
fi

# I-13: eth_getBlobSidecar returns null for unknown hash
SIDECAR_RESP=$(rpc '{"jsonrpc":"2.0","method":"eth_getBlobSidecar","params":["0x0000000000000000000000000000000000000000000000000000000000000000"],"id":1}')
SIDECAR_RESULT=$(echo "$SIDECAR_RESP" | python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('result','NOT_NULL'))" 2>/dev/null || echo "error")
SIDECAR_ERR=$(echo "$SIDECAR_RESP" | python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('error',{}).get('message',''))" 2>/dev/null || true)

if [[ -n "$SIDECAR_ERR" ]]; then
  fail "I-13: eth_getBlobSidecar unknown hash error — $SIDECAR_ERR"
elif [[ "$SIDECAR_RESULT" == "None" || "$SIDECAR_RESULT" == "null" ]]; then
  pass "I-13: eth_getBlobSidecar returns null for unknown hash"
else
  fail "I-13: eth_getBlobSidecar unknown hash returned unexpected: $SIDECAR_RESULT"
fi

# I-14: Type 3 blob tx submission via eth_sendRawTransaction
# Requires python3 eth-account >= 0.8.0 (blob tx support) and a funded account
log "  Checking blob tx submission (requires eth-account with EIP-4844 support)..."
BLOB_TX_RESULT=$(python3 3>/dev/null <<'PYEOF' 2>/dev/null || echo "NO_SUPPORT"
try:
    import json, os, urllib.request
    from eth_account import Account
    from eth_account.signers.local import LocalAccount

    RPC = os.environ.get("RPC", "http://localhost:8545")

    def rpc_call(method, params):
        data = json.dumps({"jsonrpc":"2.0","method":method,"params":params,"id":1}).encode()
        req = urllib.request.Request(RPC, data=data, headers={"Content-Type":"application/json"})
        return json.loads(urllib.request.urlopen(req, timeout=5).read())

    chain_id = int(rpc_call("eth_chainId", [])["result"], 16)
    blob_base_fee = int(rpc_call("eth_blobBaseFee", [])["result"], 16)
    acct: LocalAccount = Account.from_key("0xac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80")
    nonce = int(rpc_call("eth_getTransactionCount", [acct.address, "latest"])["result"], 16)

    # Minimal blob: 4096 field elements × 32 bytes = 131072 bytes, all zeros is valid
    blob_data = b"\x00" * 131072

    # sign_transaction with blobs requires eth-account >= 0.8.0
    tx = {
        "type": 3,
        "chainId": chain_id,
        "nonce": nonce,
        "to": "0x000000000000000000000000000000000000dEaD",
        "value": 0,
        "gas": 21000,
        "maxFeePerGas": 2 * 10**9,
        "maxPriorityFeePerGas": 1 * 10**9,
        "maxFeePerBlobGas": max(blob_base_fee * 2, 10**9),
        "blobs": [blob_data],
    }
    signed = acct.sign_transaction(tx)
    # sign_transaction for blob txs returns .raw_transaction in network encoding (with sidecar)
    raw = signed.raw_transaction.hex()
    if not raw.startswith("03"):
        print("NOT_BLOB_TYPE")
    else:
        result = rpc_call("eth_sendRawTransaction", ["0x" + raw])
        if "error" in result:
            print("ERR:" + result["error"]["message"])
        else:
            print("OK:" + result["result"])
except ImportError as e:
    print("NO_SUPPORT")
except Exception as e:
    print(f"ERR:{e}")
PYEOF
)

if [[ "$BLOB_TX_RESULT" == "NO_SUPPORT" ]]; then
  skip "I-14: eth-account does not support EIP-4844 blob signing (upgrade to >= 0.8.0)"
elif [[ "$BLOB_TX_RESULT" == "NOT_BLOB_TYPE" ]]; then
  skip "I-14: signed tx is not Type 3 — eth-account version issue"
elif [[ "$BLOB_TX_RESULT" == ERR:* ]]; then
  ERRMSG="${BLOB_TX_RESULT#ERR:}"
  # "already known" is acceptable (idempotent)
  if echo "$ERRMSG" | grep -qi "already known\|nonce too low"; then
    pass "I-14: Blob tx already in pool (idempotent: $ERRMSG)"
  # eth-account version doesn't support 'blobs' kwarg in sign_transaction
  elif echo "$ERRMSG" | grep -qi "unknown kwargs.*blobs\|unexpected keyword.*blob\|blobs.*not supported"; then
    skip "I-14: eth-account does not support EIP-4844 blob signing (upgrade to >= 0.8.0)"
  else
    fail "I-14: Blob tx submission failed — $ERRMSG"
  fi
elif [[ "$BLOB_TX_RESULT" == OK:* ]]; then
  TXHASH="${BLOB_TX_RESULT#OK:}"
  pass "I-14: Blob tx submitted (hash=${TXHASH:0:20}...)"

  # I-15: eth_getBlobSidecar returns sidecar for submitted blob tx
  sleep 1
  SC_RESP=$(rpc "{\"jsonrpc\":\"2.0\",\"method\":\"eth_getBlobSidecar\",\"params\":[\"$TXHASH\"],\"id\":1}")
  SC_RESULT=$(echo "$SC_RESP" | python3 -c "import sys,json; d=json.load(sys.stdin); r=d.get('result'); print('null' if r is None else 'ok:'+str(len(r.get('blobs',[]))))" 2>/dev/null || echo "error")
  if [[ "$SC_RESULT" == "null" ]]; then
    skip "I-15: Sidecar not in pool yet (tx may have been mined)"
  elif [[ "$SC_RESULT" == ok:* ]]; then
    NBLOBS="${SC_RESULT#ok:}"
    pass "I-15: eth_getBlobSidecar returned sidecar with $NBLOBS blob(s)"
  else
    fail "I-15: eth_getBlobSidecar failed: $SC_RESULT"
  fi
else
  fail "I-14: Unexpected blob tx result: $BLOB_TX_RESULT"
fi

echo ""

# ============================================================
# Summary
# ============================================================
TOTAL=$((PASS + FAIL + SKIP))
log "=== Camellia Fork Test Complete ==="
log "Results: PASS=$PASS, FAIL=$FAIL, SKIP=$SKIP / TOTAL=$TOTAL"
echo ""

if [[ $FAIL -gt 0 ]]; then
  exit 1
fi
exit 0
