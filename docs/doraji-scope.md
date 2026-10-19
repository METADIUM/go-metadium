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
- The public chains get their fork blocks once the rule set is complete and
  verified on the testnet. Private networks that start after that set
  `"dorajiBlock": 0`.
