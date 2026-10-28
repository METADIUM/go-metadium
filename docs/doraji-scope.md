# Doraji fork — scope

Doraji is the hard fork after Camellia. Camellia (Shanghai + Cancun) brought
the two public chains to the go-ethereum v1.13.14 rule set; Doraji takes the
parts of the two Ethereum mainnet forks since then, **Prague** (2025-05-07) and
**Osaka** (2025-12-03), that apply to a proof-of-authority chain, in one
block-scheduled fork (`dorajiBlock`, like `camelliaBlock`).

Scope was fixed on 2026-10-19 and is changed only by a revision of this document.

## Included

| EIP | Fork | What |
|---|---|---|
| 7702 | Prague | Set-code transaction (type `0x04`), EOA code delegation. Independent of Metadium's fee-delegated type `0x16` (22): a transaction is one or the other, never both |
| 2537 | Prague | BLS12-381 precompiles, seven of them at `0x0b`–`0x11` (G1ADD, G1MSM, G2ADD, G2MSM, PAIRING, MAP_FP_TO_G1, MAP_FP2_TO_G2). Replaces the inactive draft set this tree carries at `0x0a`–`0x12` |
| 7951 | Osaka | P256VERIFY precompile at `0x100` |
| 2935 | Prague | Serve historical block hashes from a system contract. The contract code is written into the state at the fork block; there is no deployment transaction |
| 7939 | Osaka | `CLZ` opcode |
| 7883, 7823 | Osaka | ModExp gas repricing and the 1024-byte input limit |

## Excluded

These three change what a block or transaction is allowed to cost or carry.
Metadium accepts large transactions on purpose (a 254 KB contract creation uses
about 52M gas against a 105M block limit, most of it the code deposit; the
calldata alone is about 4M), and the reasons upstream added the limits —
block validation time under PeerDAS — do not apply to this chain. They are not
ported; the upstream test vectors that exercise them are skipped.

| EIP | What |
|---|---|
| 7825 | Transaction gas limit cap (16,777,216) |
| 7623 | Calldata cost floor |
| 7934 | Block RLP size limit (8 MiB) |

## Not applicable

- Consensus-layer EIPs (4788, 6110, 7002, 7251, 7549, 7685, 7594, blob
  parameter-only forks): Metadium has no beacon chain.
- Blob schedule changes (7691, 7918, BPO forks): the Cancun schedule stays.
- Wire protocol changes (eth/69 and later): the `meta` protocol is versioned on
  its own, outside this fork.
- Amsterdam, Bogota and later: not scheduled on Ethereum mainnet at the time of
  writing, and their state-gas repricing conflicts with the on-chain gas
  governance here.
- Verkle (4762) and EOF.

## Mechanics

- `params.ChainConfig.DorajiBlock`; `IsDoraji`; `Rules.IsDoraji`. Doraji
  requires Camellia and cannot precede it.
- Where ported upstream code checks `IsPrague` or `IsOsaka`, the rules map both
  to Doraji, the way Camellia maps `IsShanghai` and `IsCancun`. That mapping
  lands with the EVM and precompile changes, not before, so that a Doraji rule
  set never selects an instruction set this tree does not define.
- The testnet activates Doraji at block 93,963,908 (2026-11-10 11:00 KST,
  m1.3.0). The mainnet gets its fork block in a later release, once the
  testnet has run past its fork. Private networks that start after the fork
  set `"dorajiBlock": 0`.

## For wallets, SDKs and explorers

| What | Where | Notes |
|---|---|---|
| Set-code transaction | type `0x04` | Fields as on Ethereum (`authorizationList` of `chainId`, `address`, `nonce`, `yParity`, `r`, `s`). A transaction is either type `0x04` or fee-delegated type `0x16`, never both |
| Delegated account code | `0xef0100` ‖ address | What `eth_getCode` returns for an account that has delegated |
| BLS12-381 | `0x0b`–`0x11` | G1ADD, G1MSM, G2ADD, G2MSM, PAIRING, MAP_FP_TO_G1, MAP_FP2_TO_G2, Ethereum's gas schedule |
| P256VERIFY | `0x100` | 6,900 gas |
| History storage | `0x0000F90827F1C53a10cb7A02335B175320002935` | Holds the last 8,191 block hashes. The code is written at the fork block, so the contract has no creation transaction |
| CLZ | opcode `0x1e` | |
| VRF (Metadium) | `0x13` | Unchanged |

Transactions keep the limits they have today: no per-transaction gas cap, no
calldata floor, no block size cap beyond the gas limit.

## Verification

**Upstream vectors.** The state tests of
[execution-spec-tests](https://github.com/ethereum/execution-spec-tests) v5.4.0
(the final Osaka release; `build/checksums.txt`) run in CI (`TestExecutionSpecState`).
Their `Osaka` vectors run under the Doraji rule set, as do `Prague` vectors
that have no Osaka counterpart. A vector is skipped, with its reason in the
test output, when:

- it is a `Prague` vector of something Osaka changed — the Osaka variant is the
  one Doraji follows;
- its expected result includes the EIP-7623 calldata floor (the test runs the
  vector once and compares gas used with the floor), or it exercises 7825,
  7623 or the PeerDAS blob limit directly.

Vectors that differ because of Metadium policy on every fork — the VRF
precompile at `0x13`, and the 253,952 / 507,904-byte code and initcode limits
— are marked as expected failures, so a change to that policy shows up. The
block tests are skipped as before: the Metadium header differs from Ethereum's.

**Private network.** `tests/private-net-poa/doraji-test.sh` checks each EIP on
a three-node network across the fork block (`DORAJI_BLOCK=<n> ./setup.sh`):
absent before it, working after it, including a delegation through a set-code
transaction and the hash history contract; then it sends a 254 KB contract
creation to show the transaction limits are unchanged.
