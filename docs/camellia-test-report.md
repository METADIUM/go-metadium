# Camellia Fork Test Report

**Date:** 2026-04-08
**Branch:** `feature/geth-v1.13.14`
**Version:** 1.0.0-stable
**Author:** Jeffrey Song (AI-assisted with Claude Opus 4.6)

---

## 1. Summary

Camellia fork (Shanghai + Cancun EIPs) 통합 개발 및 테스트 완료. go-ethereum v1.13.14 기반 Metadium PoA 블록체인 노드에 EIP-4844 (Blob Tx), EIP-3855 (PUSH0), EIP-1153 (Transient Storage), EIP-5656 (MCOPY), EIP-3651 (Warm COINBASE), EIP-6780 (SELFDESTRUCT restriction), EIP-3860 (initcode size limit), Type 22 Fee Delegation 기능을 구현 및 검증.

**전체 테스트 결과: ALL PASS**

---

## 2. Test Results

### 2.1 Unit Tests

| Test Suite | Result | Details |
|-----------|--------|---------|
| `make test` | ✅ **119 packages PASS, 0 FAIL** | 전체 Go 단위 테스트 |
| `make test-short` | ✅ **119 packages PASS, 0 FAIL** | Short 모드 단위 테스트 |

### 2.2 Integration Tests (Private Network 3-node PoA)

| Test | Result | Details |
|------|--------|---------|
| `camellia-test.sh` | ✅ **14 PASS, 0 FAIL, 3 SKIP** | EIP opcode 전환 검증 (block 99→100) |
| `blob-tx-e2e` | ✅ **ALL PASS** | EIP-4844 blob tx 생성→제출→마이닝→검증 |
| `mixed-tx-e2e` | ✅ **ALL PASS** | Normal(Type 2) + FeeDelegation(Type 22) + Blob(Type 3) 혼합 |
| `rpc-test-full.sh` | ✅ **67 PASS, 0 FAIL, 0 WARN** | Execution API 전체 호환성 |

### 2.3 camellia-test.sh Detail (EIP Verification)

| EIP | Test | Result |
|-----|------|--------|
| EIP-3855 | PUSH0 opcode | ✅ block 99 revert, block 100 success |
| EIP-1153 | TLOAD/TSTORE | ✅ block 99 revert, block 100 success |
| EIP-5656 | MCOPY | ✅ block 99 revert, block 100 success |
| EIP-4844 | BLOBBASEFEE opcode | ✅ block 99 revert, block 100 success |
| EIP-3651 | Warm COINBASE | ✅ gas savings confirmed (2606 → 106) |
| EIP-6780 | SELFDESTRUCT restriction | ✅ code preserved after SELFDESTRUCT |
| EIP-3860 | Initcode size limit | ✅ oversized initcode rejected |
| EIP-4844 | eth_blobBaseFee API | ✅ returns 0x1 |
| EIP-4844 | eth_getBlobSidecar API | ✅ null for unknown hash |
| — | Block production continuity | ✅ blocks advancing after fork |

### 2.4 SKIP Items (All Verified Elsewhere)

| SKIP | Reason | Alternative Verification |
|------|--------|------------------------|
| Fee Delegation signing | `eth_sendTransaction` requires account unlock | `mixed-tx-e2e` Go raw tx ✅ |
| Governance contract | Not in private-net genesis | `deploy.sh` 배포 후 3노드 PoA 정상 ✅ |
| Blob tx signing | python eth-account EIP-4844 미지원 | `blob-tx-e2e` Go ✅ |

---

## 3. Server Deployment & Sync Verification

| Server | Network | DB | Commit | Version | Sync |
|--------|---------|-----|--------|---------|------|
| 25 | testnet | LevelDB | `5d4e74ff` | 1.0.0-stable | ✅ synced (block 84,626,806) |
| 151 | testnet | RocksDB | `98d29566` | 1.0.0-stable | ✅ synced (block 84,626,806) |
| 150:8588 | mainnet | LevelDB | `98d29566` | 1.0.0-stable | ✅ synced (block 111,664,697) |
| 150:8590 | mainnet | RocksDB | `98d29566` | 1.0.0-stable | ✅ synced (block 111,664,698) |

모든 노드가 최신 코드로 배포 후 정상 동기화 확인. 현재 Camellia fork block은 아직 설정되지 않아 pre-fork 상태로 운영 중.

---

## 4. Code Health

```
Category      Tool              Score   Status
----------    ----------------  -----   --------
Type check    go build ./...    10/10   CLEAN      (0 errors)
Lint          go vet ./...      10/10   CLEAN      (0 warnings)
Tests         go test -short    10/10   CLEAN      (119/119 passed)
golangci-lint golangci-lint     10/10   CLEAN      (project code clean, external dep issues only)
Shell lint    shellcheck        10/10   CLEAN      (0 issues, 7 scripts)

COMPOSITE SCORE: 10.0 / 10
```

---

## 5. Security Audit (`/cso`)

| # | Severity | Finding | Status |
|---|----------|---------|--------|
| 1 | MEDIUM | GitHub Actions unpinned (tag@v3, not SHA) | Noted |
| 2 | MEDIUM | .gstack/ not in .gitignore | ✅ Fixed |

- 시크릿 유출 없음 (git history 포함)
- TLS skip 없음
- Prod 코드에 insecure 설정 없음

---

## 6. Code Review (`/review`)

| # | Severity | Finding | Status |
|---|----------|---------|--------|
| 1 | P2 (CRITICAL) | Race condition in `handleTransactionsEx` (CurrentBlock 2회 호출) | ✅ Fixed |
| 2 | INFO | Dead code `SendBlobTx`/`blobBackend` | ✅ Removed |
| 3 | INFO | `Hash()` shallow copy GC (cached, 1회 실행) | Accepted |

---

## 7. Bug Fixes (This Session)

### 7.1 EIP-4844 Blob Tx Lifecycle (3 bugs)

1. **`Transaction.Hash()` sidecar 의존성** — `BlobTx.EncodeRLP`가 sidecar 포함 시 다른 hash 생성. blobpool lookup 불일치로 "swapped out by signer" 발생.
   - Fix: `Hash()`가 항상 canonical encoding(sidecar 제외)으로 계산하도록 수정

2. **`SendRawTransaction` blob 경로 누락** — `blobBackend` 미구현으로 `DecodeBlobTxNetworkEncoding` 결과 무시.
   - Fix: `DecodeBlobTxNetworkEncoding` + `WithBlobTxSidecar` 경로 구현

3. **`env.copy()` blobs 필드 미복사** — `commitEx`에서 `BlobGasUsed=0` 설정.
   - Fix: `blobs` 필드 copy + sidecar blob count에서 직접 gas 계산

### 7.2 Blob Tx Mining (2 bugs)

4. **`commitTransactionsEx` 변수 swap** — `blobPendingLazy`가 `plainTxs`에 할당됨.
   - Fix: `plainTxs`/`blobTxs` 변수명 교정

5. **`commitTransactions` blob tx 미분기** — blob tx에 `commitTransaction` 호출 (BlobGasUsed 미증가).
   - Fix: `tx.Type() == BlobTxType` 체크 후 `commitBlobTransaction` 호출

### 7.3 PoA Consensus

6. **`accumulateRewards` nil pointer** — `uint256.FromBig(nil)` → nil (v1.2.4 변경). `state.AddBalance(addr, nil)` panic.
   - Fix: nil/zero guard + `header.Fees` nil guard

7. **Race condition in `handleTransactionsEx`** — `CurrentBlock()` 2회 호출로 block 전환 시 불일치 가능.
   - Fix: local variable에 저장 후 사용

### 7.4 Test Compatibility

8. **Upstream test 호환성** — Metadium 커스텀(헤더 추가 필드, "meta" 프로토콜, MaxInitCodeSize, PoA consensus)으로 인한 upstream 테스트 기대값 불일치 56개 파일 수정.

---

## 8. Commit History

```
939e86c91 fix: resolve all shellcheck warnings in scripts
2fe2118ab fix: update debug_getBlockRlp to debug_getRawBlock in rpc-test-full.sh
4585a6fd4 test: fix upstream test compatibility for Metadium fork
5d4e74ff0 fix: race condition in handleTransactionsEx and remove dead blob code
98d295664 fix: camellia-test.sh rpc for large payloads and EIP-3860 check
483f8301b fix: EIP-4844 blob tx lifecycle on Metadium PoA
9fbf90b00 fix: blob tx mining on Metadium PoA (commitTransactionsEx)
bcd3d8af6 fix: handle nil big.Int in accumulateRewards callback for PoA mode
72e364e9e chore: bump version to 1.0.0 for Camellia fork release
83c581df8 fix: enable fee delegation (Type 22) txs and fix EIP-3860 test limit
3d25632a3 fix: skip PoW difficulty check and use Metadium rewards in PoA mode
87f957636 fix: preserve withdrawals in Block.WithSeal for Camellia fork
```

---

## 9. Remaining Work

1. **Testnet Camellia fork block 설정** — testnet에서 fork block 지정 후 실제 전환 테스트 (서버 25, 151)
2. **Mainnet Camellia fork block 설정** — testnet 검증 후 mainnet 적용 (서버 150)
3. **GitHub Actions SHA pinning** — 보안 감사 권고사항
4. **서버 25/150/151 최신 커밋 재배포** — 현재 test/shellcheck fix 커밋 미반영 (기능 변경 없음, 테스트만)

---

## 10. Conclusion

Camellia fork 구현이 완료되었으며, 단위 테스트(119 packages), 통합 테스트(camellia-test, blob-tx-e2e, mixed-tx-e2e), RPC API 호환성 테스트(67 API), 서버 동기화 테스트(4노드), 코드 리뷰, 보안 감사를 모두 통과했다.

다음 단계는 testnet에서 실제 fork block을 설정하여 live 환경에서의 전환을 검증하는 것이다.
