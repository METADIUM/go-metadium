#!/usr/bin/env python3
"""
Submit a Type 22 (FeeDelegateDynamicFeeTx) to go-metadium.

Encoding: 0x16 + RLP([SenderTx_fields, feePayerAddr, FV, FR, FS])
FeePayerHash: keccak256(0x16 + RLP([[sender_fields], feePayerAddr]))

Prints one line to stdout:
  OK:<txhash>:<status>:<fp_before_wei>:<fp_after_wei>:<feePayer_in_tx>
  ERR:<message>
"""
import json, urllib.request, os, sys, time, rlp
from eth_account import Account
from eth_keys import keys
from eth_hash.auto import keccak

RPC          = os.environ.get("RPC", "http://localhost:8545")
SENDER_KEY   = "0xac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80"
FEEPAYER_KEY = "0x59c6995e998f97a5a0044966f0945389dc9e86dae88c7a8412f4603b6b78690d"

sender   = Account.from_key(SENDER_KEY)
feepayer = Account.from_key(FEEPAYER_KEY)

def rpc_call(method, params):
    data = json.dumps({"jsonrpc":"2.0","method":method,"params":params,"id":1}).encode()
    req  = urllib.request.Request(RPC, data=data, headers={"Content-Type":"application/json"})
    return json.loads(urllib.request.urlopen(req, timeout=5).read())

try:
    nonce     = int(rpc_call("eth_getTransactionCount", [sender.address, "latest"])["result"], 16)
    chain_id  = int(rpc_call("eth_chainId", [])["result"], 16)
    gas_price = int(rpc_call("eth_gasPrice", [])["result"], 16)
    fp_before = int(rpc_call("eth_getBalance", [feepayer.address, "latest"])["result"], 16)

    # Sign type-2 sender tx
    signed = sender.sign_transaction({
        "nonce": nonce, "to": "0x3C44CdDdB6a900fa2b585dd299e03d12FA4293BC",
        "value": 0, "gas": 30000,
        "maxFeePerGas": gas_price, "maxPriorityFeePerGas": gas_price,
        "chainId": chain_id, "type": 2,
    })
    decoded = rlp.decode(bytes(signed.raw_transaction)[1:])  # strip 0x02 prefix
    ci, n, tip, fee, g, to, v_, d_, al, sv, sr, ss = decoded

    # Compute fee payer hash and sign
    fp_addr = bytes.fromhex(feepayer.address[2:])
    fp_hash = keccak(b'\x16' + rlp.encode([[ci,n,tip,fee,g,to,v_,d_,al,sv,sr,ss], fp_addr]))
    fp_sig  = keys.PrivateKey(bytes.fromhex(FEEPAYER_KEY[2:])).sign_msg_hash(fp_hash)
    fv      = fp_sig.v
    fr      = int.from_bytes(bytes(fp_sig)[0:32], 'big')
    fs      = int.from_bytes(bytes(fp_sig)[32:64], 'big')

    def i2b(n): return b'' if n == 0 else n.to_bytes((n.bit_length()+7)//8, 'big')

    raw22 = b'\x16' + rlp.encode([
        [ci, n, tip, fee, g, to, v_, d_, al, sv, sr, ss],
        fp_addr, i2b(fv), i2b(fr), i2b(fs),
    ])

    res = rpc_call("eth_sendRawTransaction", ['0x' + raw22.hex()])
    if "error" in res:
        print(f"ERR:{res['error']['message']}")
        sys.exit(0)

    txhash  = res["result"]
    receipt = None
    for _ in range(20):
        time.sleep(1)
        r = rpc_call("eth_getTransactionReceipt", [txhash])
        if r.get("result"):
            receipt = r["result"]
            break

    if not receipt:
        print(f"ERR:receipt_timeout:{txhash}")
        sys.exit(0)

    fp_after     = int(rpc_call("eth_getBalance", [feepayer.address, "latest"])["result"], 16)
    sender_after = int(rpc_call("eth_getBalance", [sender.address, "latest"])["result"], 16)
    fp_in_tx     = rpc_call("eth_getTransactionByHash", [txhash])["result"].get("feePayer", "")
    gas_used     = int(receipt["gasUsed"], 16)
    eff_gas      = int(receipt.get("effectiveGasPrice", hex(gas_price)), 16)
    gas_cost     = gas_used * eff_gas
    # sender_before is not captured; use nonce to verify no gas deducted from sender
    # key check: feePayer paid gas, not sender
    print(f"OK:{txhash}:{receipt['status']}:{fp_before}:{fp_after}:{fp_in_tx}:{gas_cost}")

except Exception as e:
    print(f"ERR:{e}")
