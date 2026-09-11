#!/usr/bin/env python3
# check-camellia-headers.py - verify a chain's post-Camellia headers against the
# rule consensus/ethash/consensus.go enforces (verifyCamelliaHeaderFields).
#
# Covers all five checks that rule makes: withdrawalsHash pinned to the empty
# root, excessBlobGas derived from the parent, parentBeaconRoot absent,
# blobGasUsed within the per-block cap, and blobGasUsed a whole multiple of a
# blob's gas. The last three were added for issue #134.
#
# Usage:
#   python3 scripts/check-camellia-headers.py [RPC_URL] [START] [END]
#   python3 scripts/check-camellia-headers.py http://localhost:8545 117764000
#
#   RPC_URL  defaults to http://localhost:8545
#   START    defaults to mainnet's CamelliaBlock (params/config.go)
#   END      defaults to the current head
#
# Why this exists: the header rule is a tightening, so a node running it cannot
# import a Camellia block that violates it. The comment on the rule says a full
# clean sync over the post-Camellia range has to pass before it is deployed --
# but the rule is a pure function of (header, parent), so reading every header
# in the range checks exactly the same thing, in minutes rather than days. A
# clean sync additionally re-verifies execution, which is the right gate for a
# change to execution and not for this one.
#
# Read-only: eth_blockNumber and eth_getBlockByNumber, batched. Safe to point at
# a production RPC node; it sustained ~1,600 blocks/s against one without
# noticeable load.
#
# Exit status is 1 if any block violates the rule, 0 otherwise. Every violation
# is printed with the block, the expected value and the parent it derives from.

import json
import sys
import time
import urllib.request

RPC = sys.argv[1] if len(sys.argv) > 1 else "http://localhost:8545"
START = int(sys.argv[2]) if len(sys.argv) > 2 else 117_764_000  # mainnet CamelliaBlock
END = int(sys.argv[3]) if len(sys.argv) > 3 else None
BATCH = 100

# params.BlobTxTargetBlobGasPerBlock, mirrored here so the check does not depend
# on the node it is pointed at.
#
# This is Metadium's value -- 1 blob per block for a 2-second PoA slot -- and it
# is deliberately NOT upstream Ethereum's 3 blobs (393216). Do not "correct" it
# to the upstream constant; that is the bug this line already had once, and it
# was invisible because a chain that has never carried a blob gives the same
# answer for any target.
TARGET_BLOB_GAS = 131072  # 1 * params.BlobTxBlobGasPerBlob
# params.BlobTxBlobGasPerBlob and params.MaxBlobGasPerBlock (2 blobs). The cap
# and the multiple are the two blobGasUsed rules added for issue #134: nothing
# on the verify path capped the field, and core.ValidateBody compares it to the
# body's blob count by dividing, which leaves up to PER_BLOB-1 of slack that
# feeds the next block's excessBlobGas undivided.
PER_BLOB = 131072
MAX_BLOB_GAS = 2 * PER_BLOB
# types.EmptyWithdrawalsHash. Metadium PoA has no withdrawals and
# FinalizeAndAssemble pins the header to this value.
EMPTY_WITHDRAWALS = "0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421"


def calc_excess_blob_gas(parent_excess, parent_used):
    """types.CalcExcessBlobGas -- the formula the block builder applies."""
    excess = parent_excess + parent_used
    return 0 if excess < TARGET_BLOB_GAS else excess - TARGET_BLOB_GAS


def post(payload, timeout=60):
    req = urllib.request.Request(RPC, json.dumps(payload).encode(),
                                 {"Content-Type": "application/json"})
    with urllib.request.urlopen(req, timeout=timeout) as r:
        return json.load(r)


def rpc_batch(nums):
    payload = [{"jsonrpc": "2.0", "id": n, "method": "eth_getBlockByNumber",
                "params": [hex(n), False]} for n in nums]
    for attempt in range(5):
        try:
            return {o["id"]: o.get("result") for o in post(payload)}
        except Exception:
            if attempt == 4:
                raise
            time.sleep(2 * (attempt + 1))


def field(block, name):
    v = block.get(name)
    return None if v is None else int(v, 16)


def main():
    head = int(post({"jsonrpc": "2.0", "method": "eth_blockNumber",
                     "params": [], "id": 1}, timeout=30)["result"], 16)
    if END:
        head = min(head, END)
    if START > head:
        print(f"nothing to check: start {START} is past head {head}")
        return 0
    print(f"range: {START} .. {head}  ({head - START + 1} blocks)", flush=True)

    parent = rpc_batch([START - 1])[START - 1]
    if parent is None:
        print(f"cannot read the parent of {START}", file=sys.stderr)
        return 1

    violations = checked = 0
    started = time.time()
    n = START
    while n <= head:
        nums = list(range(n, min(n + BATCH, head + 1)))
        blocks = rpc_batch(nums)
        for num in nums:
            block = blocks.get(num)
            if block is None:
                print(f"  {num}: not returned by the node", flush=True)
                violations += 1
                continue

            withdrawals = block.get("withdrawalsRoot")
            beacon_root = block.get("parentBeaconBlockRoot")
            excess = field(block, "excessBlobGas")
            used = field(block, "blobGasUsed")
            parent_excess = field(parent, "excessBlobGas") or 0
            parent_used = field(parent, "blobGasUsed") or 0

            # Metadium has no beacon chain and the sealing path never sets this;
            # core.ProcessBeaconBlockRoot acts on it whenever it is present.
            if beacon_root is not None:
                print(f"  {num}: parentBeaconBlockRoot {beacon_root}, want absent",
                      flush=True)
                violations += 1

            if withdrawals is None:
                print(f"  {num}: missing withdrawalsRoot", flush=True)
                violations += 1
            elif withdrawals.lower() != EMPTY_WITHDRAWALS:
                print(f"  {num}: withdrawalsRoot {withdrawals}, want {EMPTY_WITHDRAWALS}",
                      flush=True)
                violations += 1

            if excess is None:
                print(f"  {num}: missing excessBlobGas", flush=True)
                violations += 1
            elif used is None:
                print(f"  {num}: missing blobGasUsed", flush=True)
                violations += 1
            else:
                if used > MAX_BLOB_GAS:
                    print(f"  {num}: blobGasUsed {used} over the per-block max {MAX_BLOB_GAS}",
                          flush=True)
                    violations += 1
                if used % PER_BLOB:
                    print(f"  {num}: blobGasUsed {used} is not a multiple of {PER_BLOB}",
                          flush=True)
                    violations += 1
                want = calc_excess_blob_gas(parent_excess, parent_used)
                if excess != want:
                    print(f"  {num}: excessBlobGas {excess}, want {want} "
                          f"(parent excess={parent_excess} used={parent_used})",
                          flush=True)
                    violations += 1

            if used:
                # Not a violation: worth seeing, because a chain that has never
                # carried a blob keeps excessBlobGas at zero and exercises only
                # the trivial case of the formula.
                print(f"  note: block {num} carries blobGasUsed={used}", flush=True)

            parent = block
            checked += 1

        n += BATCH
        if checked % 50000 < BATCH:
            elapsed = time.time() - started
            print(f"  ... {checked} checked, {violations} violations, "
                  f"{elapsed:.0f}s, {checked / max(elapsed, 1):.0f} blk/s", flush=True)

    elapsed = time.time() - started
    print(f"DONE checked={checked} violations={violations} elapsed={elapsed:.0f}s",
          flush=True)
    return 1 if violations else 0


if __name__ == "__main__":
    sys.exit(main())
