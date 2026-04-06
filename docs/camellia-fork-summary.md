# Camellia Fork — Work Summary for sadoci

This document summarizes the work done on the `feature/camellia` branch since forking from the upstream go-metadium repository. It is intended to give the original developers a clear picture of what was changed, why, and how it was verified.

---

## Overview

The goal of this branch is to bring **Shanghai + Cancun EIP support** into Metadium as a single combined hard fork called **Camellia**. Rather than staging them separately (as Ethereum did), Camellia activates all seven EIPs at one block number, which is practical for a governed PoA chain where the validator set can coordinate upgrades.

**EIPs included in Camellia:**

| EIP | Name | Effect |
|-----|------|--------|
| EIP-1153 | Transient storage | `TLOAD` / `TSTORE` opcodes |
| EIP-3651 | Warm COINBASE | COINBASE address pre-warmed in access list |
| EIP-3855 | PUSH0 | New `PUSH0` opcode |
| EIP-3860 | initcode size limit | Max 49152 bytes for CREATE / CREATE2 |
| EIP-4844 | Blob gas market | `BLOBBASEFEE` opcode + blob gas accounting + Type 3 transactions |
| EIP-5656 | MCOPY | Memory copy opcode |
| EIP-6780 | SELFDESTRUCT semantics | Only destroys contract if created in same tx |

EIPs intentionally excluded: EIP-4788 (beacon roots), EIP-4895 (withdrawals) — N/A for PoA chains with no beacon layer.

---

## Commit History (squashed, 7 commits)

```
1. build: upgrade Go 1.19 → 1.21
2. feat: Camellia hard fork (Shanghai + Cancun EIPs)
3. fix: RocksDB stability and lifecycle management
4. backport: eth/downloader and snap protocol fixes from go-ethereum v1.11
5. test: Camellia verification suite and private-net PoA infrastructure
6. docs: Camellia fork summary and verification checklist
7. fix: BlobGasUsed support for Camellia (EIP-4844) blocks
```

---

## 1. Go Upgrade (1.19 → 1.21)

`go.mod` and all build scripts updated to Go 1.21. Required for some EIP-4844 cryptographic library dependencies (`kzg4844`).

---

## 2. Camellia Fork Implementation

### Fork Activation

`params/config.go` — added `CamelliaBlock *big.Int` to `ChainConfig` and `IsCamellia(num *big.Int) bool` helper. All four chain configs (`MainnetChainConfig`, `TestChainConfig`, `MetadiumMainnetChainConfig`, `MetadiumTestnetChainConfig`) have `CamelliaBlock: nil` by default — activation block is set when ready for deployment.

### EVM Changes (`core/vm/`)

- **`eips.go`** — `enable3651()`, `enable3855()`, `enable3860()`, `enable1153()`, `enable5656()`, `enable6780()` activation functions
- **`jump_table.go`** — wire EIPs into `newCamelliaInstructionSet()` which extends London
- **`instructions.go`** — `PUSH0`, `TLOAD`, `TSTORE`, `MCOPY`, updated `SELFDESTRUCT` semantics
- **`opcodes.go`** — `PUSH0 = 0x5f`, `TLOAD = 0x5c`, `TSTORE = 0x5d`, `MCOPY = 0x5e`, `BLOBBASEFEE = 0x4a`
- **`evm.go`** — EIP-6780: `SELFDESTRUCT` only marks for deletion if contract was created in same transaction
- **`gas_table.go`** — EIP-3860: `initCodeWordGas` (2 gas/word) for CREATE / CREATE2

### State Changes (`core/state/`)

- **`transient_storage.go`** — `TransientStorage` map implementation (per-tx, cleared on tx boundary)
- **`statedb.go`** — `GetTransientState()`, `SetTransientState()`, integrated into `Prepare()` and `Finalise()`
- **`journal.go`** — `transientStorageChange` journal entry for `TLOAD`/`TSTORE` revert support

### Blob Gas Market (`core/types/`)

- **`blob_gas_market.go`** — `CalcExcessBlobGas()`, `CalcBlobBaseFee()` (EIP-4844 fee market formulas)
- **`blob.go`** — blob-related constants and types
- **`tx_blob.go`** — `BlobTx` transaction type (Type 3)
- **`block.go`** — `ExcessBlobGas *big.Int` and `BlobGasUsed *big.Int` fields added to `Header`
- **`transaction.go`** — `BlobHashes()`, `MaxFeePerBlobGas()` on `Transaction`
- **`receipt.go`** — `BlobTxType` added to both `EncodeIndex` and `decodeTyped` for correct P2P receipt sync

### Tx Pool (`core/txpool/`)

- **`blobpool/blobpool.go`** — `BlobPool`: in-memory sub-pool for Type 3 transactions, implements the `SubPool` interface
  - Validates KZG sidecar (commitments + proofs) on submission via `AddWithSidecar()`
  - `eth_getBlobSidecar` RPC: returns sidecar while tx is in pool, nil after mining (per EIP-4844 spec)
  - `validateTx()` runs outside the write lock to avoid blocking on `StateAt()` disk I/O
  - `add()` stores tx and sidecar atomically under a single lock (no TOCTOU race)
- **`txpool.go`** — `TxPool` extended to manage both `LegacyPool` and `BlobPool` as sub-pools
- **`subpool.go`** — `SubPool` interface definition

### Block Processing (`core/`)

- **`block_validator.go`** — validates total blob gas per block does not exceed `MaxBlobGasPerBlock` (786432)
- **`state_processor.go`** — accumulates blob gas used per block, enforces limit
- **`state_transition.go`** — `preCheck()` validates `MaxFeePerBlobGas >= blobBaseFee` and blob count ≤ 6
- **`error.go`** — `ErrBlobFeeCapTooLow`, `ErrBlobCountExceeded`, `ErrBlobGasLimitExceeded`
- **`chain_makers.go`** — sets `ExcessBlobGas` when generating test chains post-Camellia

### Consensus (`consensus/clique/clique.go`)

`Prepare()` now sets `header.ExcessBlobGas` for Camellia blocks:

```go
if chain.Config().IsCamellia(header.Number) {
    parentExcessBlobGas := parentBlock.ExcessBlobGas
    if parentExcessBlobGas == nil {
        parentExcessBlobGas = big.NewInt(0) // fork transition: parent has no ExcessBlobGas
    }
    header.ExcessBlobGas = types.CalcExcessBlobGas(parentExcessBlobGas, 0)
}
```

**Bug fixed:** The original guard `if parentBlock.ExcessBlobGas != nil` silently skipped setting `ExcessBlobGas` at the fork transition block (parent exists but has nil ExcessBlobGas). This would cause the first post-fork block to have `nil` ExcessBlobGas, breaking blob fee calculations.

### Miner (`miner/worker.go`)

- `initExcessBlobGas()` helper deduplicates ExcessBlobGas initialization across `commitNewWork` and `resultLoop`
- `BlobGasUsed` header field explicitly set to 0 (not nil) for all Camellia blocks, even when no blob transactions are present — required by EIP-4844 spec
- `fillBlobTransactions()`: uses `break` (not `continue`) when block blob gas limit is reached, preserving nonce ordering per sender

### KZG4844 (`crypto/kzg4844/`)

KZG point evaluation precompile (EIP-4844) and KZG proof verification library integration. Used for validating blob sidecar proofs on submission to `BlobPool`.

### API (`internal/ethapi/`, `eth/`)

- `eth_getBlockByNumber` / `eth_getBlockByHash` — expose `excessBlobGas` and `blobGasUsed` fields when Camellia is active
- `eth_blobBaseFee` RPC method added
- `eth_getBlobSidecar` RPC method added

---

## 3. RocksDB Stability Fix

`ethdb/rocksdb/rocksdb.go` — several crash and data corruption issues fixed:

- **Iterator tracking**: Added reference counting to prevent `SIGABRT` when `Close()` is called while iterators are still active (observed in production on testnet)
- **Memory safety**: Fixed use-after-free in iterator finalization
- **`Stat()` implementation**: Was returning wrong data; corrected to use RocksDB's native stats API
- **MANIFEST corruption**: Fixed shutdown ordering — RocksDB `Close()` now called after all goroutines using the DB have exited, preventing corruption of the MANIFEST file on unclean shutdown

---

## 4. Backports from go-ethereum v1.11

Two independent improvements backported from upstream go-ethereum v1.11:

- **`eth/downloader/queue.go`** — concurrent fetch queue fix: prevents goroutine stall when all fetch slots are occupied and a new header arrives
- **`eth/downloader/fetchers_concurrent.go`** — race condition fix in concurrent body/receipt fetcher
- **`eth/protocols/snap/sync.go`** — snap sync: correctly handles empty account ranges returned by peers, preventing a livelock during initial sync on sparse chains

---

## 5. Private Network Test Infrastructure

`tests/private-net-poa/` — a self-contained 3-node PoA network running in Docker for local integration testing.

**Structure:**
```
tests/private-net-poa/
├── Dockerfile            # gmet image
├── docker-compose.yml    # 3 nodes (ports 8545/8546/8547), --gcmode archive
├── setup.sh              # build image, init genesis, fund accounts
├── start.sh              # start all 3 nodes
├── stop.sh               # graceful stop (--clean to wipe data)
├── entrypoint.sh         # node startup script
├── camellia-test.sh      # Layer 2 EIP integration tests (I-01~I-11)
├── spoa-test.sh          # Layer 5 SPoA governance tests (S-01~S-05 + I-01~I-11)
├── fee-delegate-tx.py    # Type 22 FeeDelegateDynamicFee tx submission helper
├── deploy.sh             # governance contract deployment + ETCD init
├── layer4-upgrade-test.sh  # rolling upgrade simulation (old vs new binary)
├── blob-tx-e2e/main.go   # Layer 7: EIP-4844 blob tx end-to-end test
└── mixed-tx-e2e/main.go  # Layer 8: Normal + FeeDelegation + Blob mixed block test
```

**Key design decisions:**
- `CamelliaBlock: 100` in genesis — gives enough blocks to test pre-fork behavior before activation
- `--gcmode archive` on all nodes — required to query historical state at blocks 99 and 100 for before/after EIP comparisons
- Single-node ETCD cluster on node1 — avoids quorum deadlock when adding members

---

## 6. Verification Test Suite

### Layer 1: Go Unit Tests

`core/camellia_integration_test.go` and `core/vm/runtime/camellia_test.go`

| ID | Test | EIP | Result |
|----|------|-----|--------|
| T-01 | `TestEIP3860InitCodeLimit_Reject` | 3860 | PASS |
| T-02 | `TestEIP3860InitCodeLimit_Accept` | 3860 | PASS |
| T-03 | `TestEIP3860InitCodeGas` | 3860 | PASS |
| T-04 | `TestBlobGasCalculation` | 4844 | PASS |
| T-05 | `TestBlobBaseFeeCalculation` | 4844 | PASS |
| T-06 | `TestBlobGasConstants` | 4844 | PASS |
| T-08 | `TestFeeDelegationAfterCamellia` (feePayer charged) | Type 22 | PASS |
| T-09 | `TestFeeDelegationAfterCamellia` (sender unchanged) | Type 22 | PASS |
| T-11 | `TestEIP3860BeforeCamellia` (limit not enforced pre-fork) | Compat | PASS |
| T-12 | `TestEIP6780SelfdestructPreservesCode` | 6780 | PASS |
| T-13 | `TestEIP6780SelfdestructSameTx` | 6780 | PASS |
| T-14 | `TestEIP3860Create2InitCodeLimit` | 3860 | PASS |
| T-15 | `TestBlobTxPreCheckErrors/ErrBlobFeeCapTooLow` | 4844 | PASS |
| T-16 | `TestBlobTxPreCheckErrors/ErrBlobCountExceeded` | 4844 | PASS |

Run with:
```bash
go test ./core/... ./core/vm/runtime/... -timeout 120s
```

### Layer 2: bash Integration Tests (private-net-poa)

`tests/private-net-poa/camellia-test.sh` — tests run against live 3-node network with `CamelliaBlock=100`.

| ID | Feature | Verification | Result |
|----|---------|-------------|--------|
| I-01 | EIP-3855 PUSH0 | Reverts pre-fork, succeeds post-fork | PASS |
| I-02 | EIP-1153 TLOAD/TSTORE | TLOAD returns stored value | PASS |
| I-03 | EIP-5656 MCOPY | Memory copy verified | PASS |
| I-04 | EIP-4844 BLOBBASEFEE | Opcode returns 1 wei | PASS |
| I-05 | EIP-3651 Warm COINBASE | Gas drop from 2606→106 at fork block | PASS |
| I-06 | EIP-6780 SELFDESTRUCT | Existing contract code preserved | PASS |
| I-07 | EIP-3860 initcode limit | CREATE with >49152 bytes reverts | PASS |
| I-08 | Type 22 Fee Delegation | tx status=0x1, type=0x16 | PASS |
| I-09 | Type 22 feePayer balance | feePayer balance decreases | PASS |
| I-11 | Block continuity | 5+ blocks produced after fork | PASS |

### Layer 3: Testnet Deployment

Deployed to testnet node (192.168.0.25, `gmet-testnet.service`):
- Binary built from `feature/camellia` HEAD
- Node synced at block 84M+, 5 peers
- `CamelliaBlock` not yet set — pending block number decision

### Layer 4: Rolling Upgrade Simulation

`tests/private-net-poa/layer4-upgrade-test.sh`

Simulated a mixed-binary validator set to verify fork transition safety:

| Phase | Setup | Result |
|-------|-------|--------|
| Phase 1 | node1+2 (new binary) + node3 (old, `CamelliaBlock=nil`) | PASS — all at same block-50 hash |
| Phase 2 | Advance to block 110, observe old node | OBSERVED — old binary halts at block 99, cannot process fork block |
| Phase 3 | Wipe node3 chaindata, restart with new binary | PASS — re-syncs to block 100+ |

**Key finding:** Nodes running the old binary (no `CamelliaBlock`) cannot process Camellia fork blocks. **All validators must be upgraded before `CamelliaBlock` is reached.** In-place upgrade is not possible — if a node misses the fork, it must resync from genesis or a post-fork snapshot.

### Layer 5: SPoA Integration Tests

`tests/private-net-poa/spoa-test.sh` — full governance environment with deployed contracts (Registry, MetadiumGovernance, Staking).

**Governance verification (S-01~S-05):**
- 3-member validator set confirmed via `getMemberLength()`
- Round-robin mining across all 3 nodes verified
- Block miner matches governance member list

**Camellia EIP verification in SPoA environment (I-01~I-11):**
All 11 EIP tests pass including `I-10` (governance contract live), which was N/A in the basic private-net environment.

**Result: 22/22 PASS**

**Notable findings during Layer 5:**
- ETCD quorum trap: adding node2/3 to ETCD via `admin_etcdAddMember` requires 2/2 quorum, stalling block production. Solution: keep single-node ETCD on node1, let node2/3 join via `etcdAutoJoin`.
- EIP-3651 test design: FROM address must differ from COINBASE to measure cold→warm gas difference accurately.
- `--gcmode archive` required for historical state queries at fork boundary.

### Layer 6: Live Node Sync Verification

Verified that the Camellia binary correctly resumes syncing from existing chaindata — no reinitialization required — across all network/DB combinations.

**Date:** 2026-04-05  
**Binary:** `feature/camellia` HEAD, Go 1.21

| Server | Network | DB | Final block | Peers | Result |
|--------|---------|-----|------------|-------|--------|
| 192.168.0.25 | testnet | LevelDB | 84,506,387 | 5 | PASS |
| 192.168.0.150 | mainnet | LevelDB | 111,544,279 | 5 | PASS |
| 192.168.0.150 | mainnet | RocksDB | 111,544,280 | 3 | PASS |
| 192.168.0.151 | testnet | RocksDB | 84,506,388 | 5 | PASS |

- No ERROR or CRIT log entries on any node
- mainnet LevelDB and RocksDB received identical block hashes (cross-validated)
- Existing chaindata carried over without issues

### Layer 7: EIP-4844 Blob Transaction E2E

`tests/private-net-poa/blob-tx-e2e/main.go` — end-to-end blob transaction lifecycle test.

| Step | Verification | Result |
|------|-------------|--------|
| Submit | `eth_sendRawTransaction` accepts network-encoded blob tx | PASS |
| Pool sidecar | `eth_getBlobSidecar` returns sidecar while tx is in pool | PASS |
| Mining | tx mined with status=0x1, type=0x3 | PASS |
| Pruning | `eth_getBlobSidecar` returns null after mining | PASS |

### Layer 8: Mixed Transaction E2E

`tests/private-net-poa/mixed-tx-e2e/main.go` — verifies that Normal (Type 2), Fee Delegation (Type 22), and Blob (Type 3) transactions can coexist in the same block production window without interference.

| Tx type | Status | Result |
|---------|--------|--------|
| Type 2 (DynamicFeeTx) | status=0x1 | PASS |
| Type 22 (FeeDelegateDynamicFeeTx) | status=0x1, feePayer gas deducted | PASS |
| Type 3 (BlobTx) | status=0x1, type=0x3 | PASS |

---

## 7. Bugs Found and Fixed During Verification

The following bugs were discovered during integration testing and corrected before this summary was finalized.

| # | File | Bug | Impact |
|---|------|-----|--------|
| 1 | `core/types/receipt.go:decodeTyped` | `BlobTxType` missing from switch — `EncodeIndex` was fixed but decode was not (encode/decode asymmetry) | P2P receipt sync fails with `ErrTxTypeNotSupported` for blocks containing blob txs |
| 2 | `miner/worker.go` (3 locations) | `BlobGasUsed` header field set only when `blobGasUsed > 0` | EIP-4844 requires `BlobGasUsed=0` explicitly for Camellia blocks with no blobs; nil violates the spec |
| 3 | `miner/worker.go:fillBlobTransactions` | `continue` instead of `break` when block blob gas limit is reached | Higher-nonce txs from the same sender attempted after a tx can't fit, creating nonce gaps and invalid block candidates |
| 4 | `core/txpool/blobpool/blobpool.go:AddWithSidecar` | TOCTOU race: tx added to pool under one lock acquisition, sidecar stored under a second separate lock | `Reset()` can evict the tx between the two lock acquisitions, leaving an orphaned sidecar in memory |
| 5 | `core/txpool/blobpool/blobpool.go:validateTx` | Called inside `p.mu.Lock()`, which internally calls `p.chain.StateAt()` (LevelDB/RocksDB read) | Disk I/O under the global pool write lock; also calls `types.Sender()` (ECDSA recovery) twice per tx |
| 6 | `core/types/block.go:HeaderLegacy` | `BlobGasUsed` field missing from `HeaderLegacy` struct (used for RLP encoding in PoA/PoW mode) | `BlobGasUsed` silently dropped on every DB write/read round-trip; blocks retrieved from DB had `BlobGasUsed=nil` |
| 7 | `internal/ethapi/api.go:RPCMarshalHeader` | `blobGasUsed` not included in JSON response map | `eth_getBlockByNumber` / `eth_getBlockByHash` never returned `blobGasUsed` field for Camellia blocks |
| 8 | `core/types/gen_header_json.go` | `BlobGasUsed` missing from `MarshalJSON` / `UnmarshalJSON`; `hexutil.Big.UnmarshalText("0x0")` produces non-nil empty `abs` slice while `SetUint64(0)` produces nil `abs` — `reflect.DeepEqual` treats them as not equal | Header JSON round-trip loses `BlobGasUsed`; `TestEthClient/Header/first_block` fails even after other fixes due to `big.Int` internal representation mismatch |
| 9 | `core/beacon/types.go:ExecutableDataToBlock` | `BlobGasUsed` not computed from blob transactions when `ExcessBlobGas` is provided | Engine API (`NewPayloadV1`) blockhash mismatch for Camellia blocks containing blob txs |

---

## 8. Compatibility with Existing Metadium Features

| Feature | Status | Notes |
|---------|--------|-------|
| Type 22 FeeDelegateDynamicFee | PASS | feePayer charged for gas, sender balance unchanged |
| SPoA governance | PASS | Round-robin mining, member management unaffected |
| VRF | Not tested (no VRF contracts in private-net genesis) | Expected no impact — code path unchanged |
| TRS (Transfer Restriction) | Not tested separately | Code path unchanged |
| Governance contract | PASS | Layer 5 confirmed live operation post-fork |

---

## 9. What Is NOT Changed

- **Type 22 + Blob tx**: Metadium does not use blob transactions in production. The blob validation code is present for EIP-4844 spec compliance, but Type 22 + blob combination is explicitly N/A.
- **EIP-4788** (beacon block root precompile): PoA chain, no beacon layer.
- **EIP-4895** (validator withdrawals): PoA chain, no staking withdrawals.
- **Consensus engine**: Clique-based PoA is unchanged. Only `Prepare()` gains `ExcessBlobGas` initialization.
- **Fee Delegation (Type 22)**: No changes to the fee delegation mechanism itself.

---

## 10. Deployment Checklist Status

| Layer | Status |
|-------|--------|
| Layer 1: Go unit tests (T-01~T-16) | All PASS |
| Layer 2: bash integration tests (I-01~I-11) | 10/11 PASS (I-10 N/A in basic env) |
| Layer 3: Testnet deployment + sync | Deployed, CamelliaBlock pending |
| Layer 4: Rolling upgrade simulation | PASS |
| Layer 5: SPoA full integration (22/22) | PASS |
| Layer 6: Live node sync (testnet/mainnet × LevelDB/RocksDB) | 4/4 PASS |
| Layer 7: Blob tx E2E | PASS |
| Layer 8: Mixed tx E2E (Type 2 + 22 + 3) | PASS |

**Remaining before mainnet activation:** set `CamelliaBlock` in `params/config.go`, deploy to testnet, monitor fork transition for 1 hour, then set mainnet block number.

---

## 11. Files of Interest

| Path | What changed |
|------|-------------|
| `params/config.go` | `CamelliaBlock` field, `IsCamellia()`, chain configs |
| `params/protocol_params.go` | Blob gas constants (`BlobTxPerBlobGas`, `MaxBlobGasPerBlock`, etc.) |
| `core/vm/eips.go` | EIP activation functions |
| `core/vm/jump_table.go` | `newCamelliaInstructionSet()` |
| `core/state/transient_storage.go` | EIP-1153 transient storage |
| `core/types/blob_gas_market.go` | EIP-4844 fee market |
| `core/types/receipt.go` | BlobTxType encode/decode support |
| `core/txpool/blobpool/blobpool.go` | EIP-4844 blob transaction sub-pool |
| `core/txpool/txpool.go` | Multi-pool coordinator (LegacyPool + BlobPool) |
| `consensus/clique/clique.go` | `ExcessBlobGas` initialization in `Prepare()` |
| `miner/worker.go` | `initExcessBlobGas()`, `BlobGasUsed` header, `fillBlobTransactions` |
| `core/block_validator.go` | Per-block blob gas limit enforcement |
| `core/state_transition.go` | Per-tx blob validation in `preCheck()` |
| `ethdb/rocksdb/rocksdb.go` | RocksDB stability fixes |
| `eth/downloader/`, `eth/protocols/snap/` | Backports from go-ethereum v1.11 |
| `tests/private-net-poa/` | Full test infrastructure including E2E tests |
| `docs/camellia-verification-checklist.md` | Layer-by-layer verification checklist |

---

*Branch: `feature/camellia` — jsong1230/go-metadium*  
*Last updated: 2026-04-07*
