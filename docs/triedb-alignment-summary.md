# go-metadium TrieDB Alignment — Technical Summary

**Branch:** `feature/triedb-alignment`  
**Base:** `feature/camellia` (Shanghai + Cancun EIP integration branch)  
**Date:** 2026-04-07  
**Author:** Jeffrey Song

---

## Overview

This work aligns go-metadium's trie database layer with four upstream go-ethereum pull requests. The goal is to reduce divergence from upstream, preparing the codebase for future Cancun EIP support while preserving full backward compatibility with the existing hash-scheme-only PoA network.

All four PRs have been implemented and merged into `feature/camellia`. Every PR was isolated on its own branch (`align/pr-NNNNN`), verified with `go test ./...`, and merged via non-fast-forward merge for traceability.

---

## What Changed and Why

### PR #25532 — NodeScheme Interface (`trie/database.go`)

**Problem:** `trie.Database` had no way for callers to query which storage scheme was active.

**Change:** Added two methods to `trie.Database`:
- `Scheme() string` — returns `rawdb.HashScheme` ("hashScheme"). Always returns hash-scheme since go-metadium does not support path-scheme.
- `Initialized(genesisRoot common.Hash) bool` — returns `true` unconditionally for the hash backend.

Also added two constants to `core/rawdb/schema.go`:
- `HashScheme = "hashScheme"`
- `PathScheme = "pathScheme"`

**Why it matters:** Callers can now branch behavior based on the active scheme without hardcoding assumptions. Required by PRs #26603 and #25963.

---

### PR #26603 — Scheme-Aware Trie Accessors (`core/rawdb/accessors_trie.go`)

**Problem:** The old trie node read/write functions used a flat signature (`ReadTrieNode(db, hash)`) that carried no scheme information, making a future path-based backend impossible to add without a breaking change.

**Change:** Created `core/rawdb/accessors_trie.go` with two layers:

*Legacy accessors (hash-keyed, direct):*
- `ReadLegacyTrieNode`, `WriteLegacyTrieNode`, `HasLegacyTrieNode`, `DeleteLegacyTrieNode`

*Scheme-aware accessors (dispatcher, for future compatibility):*
- `ReadTrieNode`, `WriteTrieNode`, `HasTrieNode`, `DeleteTrieNode`

The scheme-aware accessors currently support `HashScheme` only and panic on any other input, matching go-metadium's hash-only constraint.

The old functions were removed from `accessors_state.go`. All existing call sites (`trie/sync.go`, `cmd/geth/snapshot.go`, `core/state/pruner/pruner.go`) were updated.

---

### PR #26777 — Head Marker: Block → Header (`core/blockchain.go`)

**Problem:** `CurrentBlock()` returned `*types.Block` — a full block with transactions and uncles — when callers only needed header metadata (number, hash, root). This wasted memory and caused unnecessary reads.

**Change:** Changed the return type of `CurrentBlock()` from `*types.Block` to `*types.Header` across **47 files**.

Internal state fields on `BlockChain`:

| Field | Before | After |
|---|---|---|
| `currentBlock` | `atomic.Value` | `atomic.Pointer[types.Header]` |
| `currentFastBlock` | `atomic.Value` | `currentSnapBlock atomic.Pointer[types.Header]` |
| `currentFinalizedBlock` | `atomic.Value` | `currentFinalBlock atomic.Pointer[types.Header]` |
| *(new)* | — | `currentSafeBlock atomic.Pointer[types.Header]` |

New/renamed reader methods:
- `CurrentBlock() *types.Header`
- `CurrentSnapBlock() *types.Header` (replaces `CurrentFastBlock`)
- `CurrentFastBlock() *types.Header` (backward-compat alias)
- `CurrentFinalBlock() *types.Header` (replaces `CurrentFinalizedBlock`)
- `CurrentFinalizedBlock() *types.Header` (backward-compat alias)
- `CurrentSafeBlock() *types.Header` (new)

**Impact:** `internal/ethapi/backend.go` interface, all api_backend implementations (eth, les), downloader, txpool, blobpool, miner, and all test mock implementations updated.

---

### PR #25963 — hashdb/pathdb Package Split (`trie/triedb/`)

**Problem:** Upstream go-ethereum separates hash-based and path-based backends into dedicated packages for maintainability.

**Change:** Created the package directory structure:
- `trie/triedb/hashdb/database.go` — `Config` type for the hash-based backend (`Defaults`: CleanCacheSize 256MB)
- `trie/triedb/pathdb/database.go` — `Config` stub; path-scheme is not supported in go-metadium and will panic if instantiated

These new packages establish the directory structure needed for future upstream alignment without changing any runtime behavior.

---

## What Was NOT Changed

- **Consensus logic:** Metadium PoA governance and signing are untouched.
- **Storage scheme:** go-metadium remains hash-scheme only. The pathdb package is a stub.
- **Public RPC API:** No JSON-RPC methods were changed.
- **Network compatibility:** No protocol version changes. Existing nodes are unaffected.

---

## Test Results

### 1. Unit Tests (`go test ./...`)

```
ok  github.com/ethereum/go-ethereum/core                39.8s
ok  github.com/ethereum/go-ethereum/core/rawdb           2.6s
ok  github.com/ethereum/go-ethereum/core/state           4.3s
ok  github.com/ethereum/go-ethereum/core/txpool/blobpool 0.2s
ok  github.com/ethereum/go-ethereum/trie                18.8s
ok  github.com/ethereum/go-ethereum/miner               17.1s
... (all packages pass)
```

Build is clean with `CGO_ENABLED=0 go build ./...`.  
`crypto/secp256k1` is CGO-only (pre-existing constraint); passes with `CGO_ENABLED=1`.

---

### 2. PoA Private Network — Basic Operation

3-node Docker PoA network (`chainId=1337`, `camelliaBlock=100`) on a dev host:

| Check | Result |
|---|---|
| 3-node startup and peering | PASS |
| Block production (node1 mining) | PASS |
| node2/node3 sync | PASS — all 3 nodes on the same block |
| `CurrentBlock()` in mining loop | PASS |

---

### 3. Camellia Hard Fork Transition Test (block 100)

**Result: 14 PASS / 0 FAIL / 3 SKIP**

| EIP | Test | Result |
|---|---|---|
| EIP-3855 | PUSH0 (0x5f): revert at block 99, success at block 100 | PASS |
| EIP-1153 | TLOAD/TSTORE (0x5c/0x5d): before/after fork behavior | PASS |
| EIP-5656 | MCOPY (0x5e): before/after fork behavior | PASS |
| EIP-4844 | BLOBBASEFEE (0x4a): before/after fork behavior | PASS |
| EIP-3651 | Warm COINBASE: block99 gas=2606 → block100 gas=106 | PASS |
| EIP-6780 | SELFDESTRUCT: code preserved after fork | PASS |
| EIP-3860 | 49153-byte initcode CREATE rejected | PASS |
| — | Block production continuity after fork | PASS |
| EIP-4844 | `eth_blobBaseFee`, `eth_getBlobSidecar` API | PASS |

3 skips: fee delegation signing account not available / governance contract not deployed / eth-account version constraint. All are environment limitations, not structural issues.

---

### 4. Transaction Type Tests (block 100+)

| TX Type | Result | Detail |
|---|---|---|
| Normal DynamicFeeTx (Type 2) | PASS | Mined, status=0x1 |
| Fee Delegation TX (Type 0x16) | PASS | feePayer gas deduction confirmed |
| Blob TX (Type 3, EIP-4844) | PASS | KZG commitment/proof generated, sidecar pruned after mining |
| Mixed TX (all 3 types in one session) | PASS | Correctly packaged in same/adjacent blocks |

---

### 5. Live Sync Test (`feature/triedb-alignment` binary)

New binary deployed to all servers via `git checkout feature/triedb-alignment && go build`.

| Server | Network | DB | Result |
|---|---|---|---|
| 25 | testnet | LevelDB | **synced** |
| 150 | mainnet | RocksDB | **synced** |
| 150 | mainnet | LevelDB | **synced** |
| 151 | testnet | RocksDB | **synced** |

No crashes, peer connections normal, block sync confirmed on all four.

---

## Branch Structure

```
feature/camellia
└── merge: feature/triedb-alignment
    ├── merge: align/pr-25532  (NodeScheme interface)
    ├── merge: align/pr-26603  (rawdb trie accessors)
    ├── merge: align/pr-26777  (CurrentBlock head marker)
    └── merge: align/pr-25963  (hashdb/pathdb package stubs)
```

Each `align/` branch can be independently reviewed against the upstream PR diff.
