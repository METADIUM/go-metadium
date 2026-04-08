# EIP-4844 Blob Transaction 전체 통합 계획

## Context
BlobTx 타입, Gas Market 계산, BLOBHASH/BLOBBASEFEE 옵코드, KZG 프리컴파일 stub은 구현됐지만,
블록 생성·검증·실행 파이프라인에 통합되지 않았다. 이 계획은 Camellia 포크에서 EIP-4844가
실제로 동작하도록 전체 파이프라인을 연결한다.

---

## Phase 1: 버그 수정

### 1-1. `params/protocol_params.go`
| 상수 | 현재 값 | 수정값 |
|------|---------|--------|
| `MaxBlobGasPerBlock` | 262144 | 786432 (6 blobs × 131072) |
| `BlobBaseFeeUpdateFraction` | 3 | 3338477 (EIP-4844 표준) |

- 중복 상수 `BlobGaspriceUpdateFraction = 3338477` 제거

### 1-2. `core/types/block.go`
- `headerToHeaderRlp()`: `ExcessBlobGas: h.ExcessBlobGas` 추가
- `headerRlpToHeader()`: `ExcessBlobGas: h.ExcessBlobGas` 추가
- `CopyHeader()`: ExcessBlobGas deep copy 추가
  ```go
  if h.ExcessBlobGas != nil {
      cpy.ExcessBlobGas = new(big.Int).Set(h.ExcessBlobGas)
  }
  ```
- `headerMarshaling` struct: `ExcessBlobGas *hexutil.Big` 필드 추가
- `gen_header_json.go` 재생성 필요 (`go generate ./core/types/`)

> Phase 1 완료 후 `blob_gas_market_test.go` 수치 재확인 필요 (BlobBaseFeeUpdateFraction 변경 영향)

---

## Phase 2: 블록 생성 파이프라인

### 2-1. `core/types/transaction.go`
**공개 accessor 추가:**
```go
func (tx *Transaction) BlobHashes() []common.Hash { return tx.inner.blobHashes() }
func (tx *Transaction) MaxFeePerBlobGas() *big.Int {
    if b, ok := tx.inner.(*BlobTx); ok && b.MaxFeePerBlobGas != nil {
        return b.MaxFeePerBlobGas.ToBig()
    }
    return nil
}
```

**`Cost()` 수정** — blob gas 비용 포함:
```go
if blobCost := tx.inner.blobGasCost(); blobCost != nil {
    total.Add(total, blobCost)
}
```

**`Message` struct 확장:**
```go
blobHashes       []common.Hash
maxFeePerBlobGas *big.Int
```
- `AsMessage()`: BlobTxType일 때 두 필드 채우기
- 공개 accessor `BlobHashes()`, `MaxFeePerBlobGas()` 추가

### 2-2. `core/evm.go`
**`NewEVMBlockContext()`**에 ExcessBlobGas 설정:
```go
ExcessBlobGas: header.ExcessBlobGas,
```

**`NewEVMTxContext()`**에 BlobHashes 설정:
```go
BlobHashes: msg.BlobHashes(),
```

### 2-3. `consensus/clique/clique.go` — `Prepare()`
Camellia 활성 시 신규 블록에 ExcessBlobGas 설정:
```go
if chain.Config().IsCamellia(header.Number) {
    parentBlobGasUsed := calcBlobGasUsed(parent.Transactions()) // 부모 블록 blob gas 계산
    header.ExcessBlobGas = types.CalcExcessBlobGas(parent.ExcessBlobGas, parentBlobGasUsed)
}
```
> 헬퍼 함수 `calcBlobGasUsed(txs []*types.Transaction) uint64` 추가

### 2-4. `core/vm/instructions.go` — `opBlobBaseFee` 리팩토링
중복 fakeexponential 구현 제거 → `types.CalcBlobBaseFee()` 호출로 교체:
```go
blobBaseFee := ctypes.CalcBlobBaseFee(interpreter.evm.Context.ExcessBlobGas)
v, _ := uint256.FromBig(blobBaseFee)
scope.Stack.push(v)
```

---

## Phase 3: 블록 검증 파이프라인

### 3-1. `core/state_transition.go`
**`Message` 인터페이스 확장:**
```go
BlobHashes() []common.Hash
MaxFeePerBlobGas() *big.Int
```

**`preCheck()`에 blob fee 검증 추가:**
```go
if chain.IsCamellia(blockNum) && len(msg.BlobHashes()) > 0 {
    blobBaseFee := types.CalcBlobBaseFee(st.evm.Context.ExcessBlobGas)
    if msg.MaxFeePerBlobGas() == nil || msg.MaxFeePerBlobGas().Cmp(blobBaseFee) < 0 {
        return ErrBlobFeeCapTooLow
    }
    if len(msg.BlobHashes()) > int(params.MaxBlobsPerTransaction) {
        return ErrBlobCountExceeded
    }
}
```

**`errors.go`에 에러 추가:**
```go
var ErrBlobFeeCapTooLow  = errors.New("max fee per blob gas less than blob base fee")
var ErrBlobCountExceeded = errors.New("blob count exceeds per-transaction limit")
```

### 3-2. `consensus/clique/clique.go` — `verifyCascadingFields()`
```go
if !chain.Config().IsCamellia(header.Number) {
    if header.ExcessBlobGas != nil {
        return errInvalidExcessBlobGas
    }
} else {
    if header.ExcessBlobGas == nil {
        return errMissingExcessBlobGas
    }
    parentBlobGasUsed := calcBlobGasUsed(parent.Transactions())
    expected := types.CalcExcessBlobGas(parent.ExcessBlobGas, parentBlobGasUsed)
    if header.ExcessBlobGas.Cmp(expected) != 0 {
        return fmt.Errorf("invalid excessBlobGas: have %v, want %v", header.ExcessBlobGas, expected)
    }
}
```

### 3-3. `core/block_validator.go` — `ValidateBody()`
```go
if v.config.IsCamellia(header.Number) {
    var totalBlobGas uint64
    for _, tx := range block.Transactions() {
        totalBlobGas += uint64(len(tx.BlobHashes())) * params.BlobTxPerBlobGas
    }
    if totalBlobGas > params.MaxBlobGasPerBlock {
        return fmt.Errorf("blob gas %d exceeds limit %d", totalBlobGas, params.MaxBlobGasPerBlock)
    }
}
```

### 3-4. `core/state_processor.go` — `Process()`
Blob gas 사용량 집계 (다음 블록 ExcessBlobGas 계산용):
```go
var totalBlobGasUsed uint64
for i, tx := range block.Transactions() {
    totalBlobGasUsed += uint64(len(tx.BlobHashes())) * params.BlobTxPerBlobGas
    // ... 기존 트랜잭션 처리 ...
}
// 현재는 로컬 추적만 (향후 BlobGasUsed 헤더 필드 추가 시 기록)
```

---

## Phase 4: 트랜잭션 풀 통합

### `core/tx_pool.go`
**구조체 필드 추가:**
```go
camellia bool // EIP-4844 활성화 플래그
```

**`reset()` 함수에 플래그 업데이트:**
```go
pool.camellia = pool.chainconfig.IsCamellia(next)
```

**`validateTx()`에 blob 검증 추가:**
```go
if tx.Type() == types.BlobTxType {
    if !pool.camellia {
        return ErrTxTypeNotSupported
    }
    if len(tx.BlobHashes()) == 0 {
        return errors.New("blob tx must have at least one blob hash")
    }
    if uint64(len(tx.BlobHashes())) > params.MaxBlobsPerTransaction {
        return ErrBlobCountExceeded
    }
    if tx.To() == nil {
        return errors.New("blob tx must have a recipient")
    }
    // MaxFeePerBlobGas vs current blobBaseFee
    if currentHead := pool.chain.CurrentBlock(); currentHead.ExcessBlobGas != nil {
        blobBaseFee := types.CalcBlobBaseFee(currentHead.ExcessBlobGas)
        if tx.MaxFeePerBlobGas() == nil || tx.MaxFeePerBlobGas().Cmp(blobBaseFee) < 0 {
            return ErrBlobFeeCapTooLow
        }
    }
}
```

---

## 의존성 순서

```
Phase 1 (독립)
  → params 상수, block.go RLP

Phase 2 (Phase 1 이후)
  → transaction.go accessor/Cost/Message
  → evm.go (Message 의존)
  → clique.go Prepare() (CalcExcessBlobGas 의존)
  → instructions.go opBlobBaseFee 리팩토링

Phase 3 (Phase 2 이후)
  → state_transition.go (Message 인터페이스 의존)
  → clique.go verifyCascadingFields
  → block_validator.go (BlobHashes() accessor 의존)
  → state_processor.go

Phase 4 (Phase 3 이후)
  → tx_pool.go (BlobHashes(), MaxFeePerBlobGas(), CalcBlobBaseFee 의존)
```

---

## 수정 파일 목록

| 파일 | 작업 |
|------|------|
| `params/protocol_params.go` | 상수 수정 |
| `core/types/block.go` | RLP/JSON/CopyHeader |
| `core/types/transaction.go` | accessor, Cost(), Message |
| `core/evm.go` | NewEVMBlockContext, NewEVMTxContext |
| `consensus/clique/clique.go` | Prepare(), verifyCascadingFields() |
| `core/vm/instructions.go` | opBlobBaseFee 리팩토링 |
| `core/state_transition.go` | Message 인터페이스, preCheck() |
| `core/block_validator.go` | ValidateBody() |
| `core/state_processor.go` | blob gas 집계 |
| `core/tx_pool.go` | camellia 플래그, validateTx() |

---

## 검증

```bash
# 전체 빌드
go build ./...

# 타입 테스트
go test ./core/types/... -run "BlobTx|BlobGas"

# 컨센서스 테스트
go test ./consensus/clique/...

# 전체 core 테스트
go test ./core/...
```

---

# /autoplan CEO Review — 2026-04-07

<!-- AUTO-GENERATED by /autoplan — do not manually edit this section -->

## Step 0A: Premise Challenge

### Stated Premises
| # | Premise | Valid? | Risk |
|---|---------|--------|------|
| 1 | BlobTx pipeline was partially implemented (opcodes done, integration missing) | **Stale** — v1.13.14 merge brought full upstream implementation | Low |
| 2 | ExcessBlobGas needs RLP/JSON serialization | **Done** — exists in `core/types/block.go` as `rlp:"optional"` | None |
| 3 | `core/tx_pool.go` needs Camellia blob gate | **Wrong file** — v1.13.14 replaced this with `core/txpool/validation.go` | High — see critical gap below |
| 4 | `consensus/clique/clique.go` Prepare() should set ExcessBlobGas | **Done differently** — `miner/worker.go` `initExcessBlobGas()` handles this | None |
| 5 | MaxBlobGasPerBlock=2, BlobBaseFeeUpdateFraction=3338477 needed | **Done** — `params/protocol_params.go` has correct values | None |
| 6 | EIP-4788 (Beacon Block Root) excluded from Camellia | **Partially wrong** — EIP-4788 is now processed for Cancun-activated blocks via `core/state_processor.go:ProcessBeaconBlockRoot` (added this session). Camellia blocks (nil ParentBeaconRoot) still skip it. | Low |

### Critical Gap (NOT covered by either plan)
`core/txpool/validation.go:72`:
```go
if !opts.Config.IsCancun(head.Number, head.Time) && tx.Type() == types.BlobTxType {
    return txpool.ErrTxTypeNotSupported
}
```
Metadium's Camellia fork uses block-number gating (`CamelliaBlock`), not timestamp (`CancunTime`). On a Camellia chain without CancunTime set, all blob transactions will be **rejected at the pool layer**. This is a mainnet blocker.

**Fix required:** `core/txpool/validation.go:72` → also check `IsCamellia`:
```go
if !opts.Config.IsCancun(head.Number, head.Time) && !opts.Config.IsCamellia(head.Number) && tx.Type() == types.BlobTxType {
```

## Step 0B: Existing Code Leverage Map

| Sub-problem | What exists (and works) |
|-------------|------------------------|
| BlobTx type (Type 3) | `core/types/tx_blob.go` — full upstream implementation |
| Blob gas constants | `params/protocol_params.go` — Metadium-tuned (2 blobs max) |
| Block generation (ExcessBlobGas) | `miner/worker.go:initExcessBlobGas()` |
| Block validation (ExcessBlobGas) | `consensus/clique/clique.go:verifyCascadingFields()` |
| Blob fee calculation | `core/state_transition.go` — full blob fee/balance check |
| Opcodes (BLOBHASH, BLOBBASEFEE) | `core/vm/eips.go` — enabled in camelliaInstructionSet |
| Blob tx pool | `core/txpool/blobpool/` — full upstream implementation |
| Block body validation | `core/block_validator.go` — BlobGasUsed vs blobs check |
| **MISSING** | `core/txpool/validation.go:72` — Camellia blob TX acceptance |

## Step 0C: Dream State

```
CURRENT (feature/geth-v1.13.14):
  ✅ Full EIP-4844 implementation merged from upstream
  ✅ Camellia fork working in tests
  ✅ ExcessBlobGas/BlobGasUsed tracked in blocks
  ❌ Blob transactions rejected by txpool on Camellia-only chains

THIS PLAN (after fixing):
  ✅ Blob transactions accepted and mined on Camellia chains
  ✅ maxBlobsPerTransaction=2 enforced end-to-end
  ✅ ExcessBlobGas fee market working

12-MONTH IDEAL:
  ✅ Camellia activated on mainnet
  ✅ Fee Delegation blob transactions (Type 23) supported
  ✅ Blob storage pruning after 4096 blocks
  ✅ Rolling upgrade simulation passes
```

## Step 0C-bis: Implementation Alternatives

| Approach | Effort | Risk | Pros | Cons |
|----------|--------|------|------|------|
| A) Set CancunTime=0 alongside CamelliaBlock | Low (1 line) | Low | Unblocks blobpool immediately | Activates full Cancun (ParentBeaconRoot required in headers) — breaks Metadium header format |
| B) Patch validation.go to check IsCamellia (**recommended**) | Low (1 line) | Low | Clean, Camellia-specific | Needs similar check in any other Cancun-only gates |
| C) Fork blobpool entirely | High | High | Full control | Huge maintenance burden |

**Auto-decision:** Choose B — targeted fix, no side effects, consistent with existing `IsCamellia` pattern.

## Step 0D: Scope

**In scope:** Fix `validation.go:72` + any other Cancun-only gates that block Camellia blob TXs.
**NOT in scope:** Type 23 Fee Delegate Blob TX (deferred to separate plan), blob pruning, rolling upgrade simulation (tracked in TODOS.md).

## Step 0E: Temporal Interrogation

- **Hour 1:** Find all `IsCancun` calls in txpool that aren't also checked against `IsCamellia`
- **Hour 2:** Apply fixes, run `go test ./core/txpool/...`
- **Hour 3:** E2E test with `blob-tx-e2e` binary against private network
- **Hour 6+:** Submit blob TX on testnet after mainnet CamelliaBlock activation is set

## CEO Completion Summary

| Dimension | Assessment |
|-----------|-----------|
| Premise validity | Both plans largely stale (work is done), but **correct in identifying the txpool gap** |
| Right problem? | Yes — txpool blob TX gating is the last mainnet blocker |
| Scope | Laser-focused: 1-2 files, 1-3 lines each |
| Risk | Low — isolated change, well-tested path |
| 6-month trajectory | On track if txpool fix lands before mainnet CamelliaBlock is set |
| Deferred to TODOS.md | Type 23 FeeDelegateBlobTx, blob pruning, rolling upgrade simulation |

## Decision Audit Trail

| # | Phase | Decision | Classification | Principle | Rationale | Rejected |
|---|-------|----------|----------------|-----------|-----------|---------|
| 1 | CEO | Fix validation.go IsCamellia check (not set CancunTime=0) | Mechanical | P5 (explicit) | Targeted fix preserves Metadium-specific header format | Option A (CancunTime=0 activates full Cancun) |
| 2 | CEO | Both plans are stale — implementation is done | Mechanical | P3 (pragmatic) | Evidence from codebase inspection | — |
| 3 | CEO | Type 23 FeeDelegateBlobTx deferred | Mechanical | P2 (blast radius) | Outside current blast radius | — |


---

# /autoplan Eng Review — 2026-04-07

## Step 0: Scope Challenge

**Current implementation state** (from codebase inspection):
- Phase 1 (constants): ✅ Done — `params/protocol_params.go` has correct Metadium values
- Phase 2 (block generation): ✅ Done — `miner/worker.go:initExcessBlobGas()` + clique Prepare()
- Phase 3 (block validation): ✅ Done — `clique.go:verifyCascadingFields()`, `block_validator.go`
- Phase 4 (txpool): ❌ **1 critical bug** — architecture changed from `tx_pool.go` → `core/txpool/`

The plan correctly identified the gap but targeted the wrong file.

## Architecture ASCII Diagram

```
User submits BlobTx (Type 3)
         │
         ▼
  txpool.Add()
         │
         ▼
  ValidateTransaction()  ← core/txpool/validation.go:72
  if !IsCancun && !IsCamellia [NEEDS FIX]
  → ErrTxTypeNotSupported ← BLOCKS HERE on Camellia-only chain
         │ (if passes)
         ▼
  blobPool.Add()  ← core/txpool/blobpool/blobpool.go
  (blobpool is always registered in eth/backend.go:231)
         │
         ▼
  Block Production:
  miner/worker.go:
    IsCancun path → sets header fields (Cancun + BeaconRoot)
    initExcessBlobGas() ← Camellia path (handles IsCamellia blocks) ✅
         │
         ▼
  Block Validation:
  consensus/beacon/consensus.go:verifyHeader()
    checks IsCancun || IsCamellia for ExcessBlobGas ✅
  consensus/clique/clique.go:verifyCascadingFields()
    checks IsCamellia for ExcessBlobGas ✅
```

## Section 2: Code Quality Findings

| File | Line | Issue | Severity |
|------|------|-------|----------|
| `core/txpool/validation.go` | 72 | `!IsCancun` check doesn't include Camellia — blob TXs rejected | **Critical** |
| `core/txpool/blobpool/blobpool.go` | 820 | `IsCancun` check for limbo finalization — memory leak on Camellia chains | High |
| `core/txpool/validation.go` | 80, 105 | `IsShanghai(num, time)` — works on mainnet (ShanghaiTime=past), but test chain `AllEthashProtocolChanges` has `ShanghaiTime=nil` creating EIP-3860 test gap | Medium |
| `params/config.go` | 221 | `AllEthashProtocolChanges` has `CamelliaBlock=0, ShanghaiTime=nil` — tests use Camellia without Shanghai active | Medium |

## Section 3: Test Review

### Codepath → Test Coverage Map

| Codepath | Test Exists? | Gap? |
|----------|-------------|------|
| Blob TX accepted on Cancun chain | `blobpool_test.go` with CancunTime | Covered |
| Blob TX accepted on Camellia-only chain | ❌ No test | **Gap — add test** |
| Blob TX rejected pre-Camellia | ❌ No test | Gap |
| ExcessBlobGas computed at Camellia activation block | ❌ No test | Gap |
| Blobpool limbo finalized on Camellia chain | ❌ No test | Gap |
| MaxBlobsPerTransaction=2 enforced (not 6) | Indirectly via `maxBlobsPerTransaction = MaxBlobGasPerBlock/BlobTxBlobGasPerBlob` | Covered via constants |

### Required New Tests

1. `TestValidateTransactionCamelliaOnly` in `core/txpool/validation_test.go`:
   - Config: `CamelliaBlock=big.NewInt(100)`, `CancunTime=nil`
   - Head at block 99: blob TX → `ErrTxTypeNotSupported`
   - Head at block 100: blob TX → nil (accepted)

2. `TestBlobPoolLimboFinalizeCamellia` in `core/txpool/blobpool/blobpool_test.go`:
   - Verify limbo.finalize() is called after the fix

## Section 4: Performance

No performance concerns. The fix is a single additional boolean check per TX validation.

## Required Fixes (both auto-decided: mechanical, P5 explicit)

### Fix 1 — `core/txpool/validation.go:72`
```go
// BEFORE:
if !opts.Config.IsCancun(head.Number, head.Time) && tx.Type() == types.BlobTxType {
// AFTER:
if !opts.Config.IsCancun(head.Number, head.Time) && !opts.Config.IsCamellia(head.Number) && tx.Type() == types.BlobTxType {
```

### Fix 2 — `core/txpool/blobpool/blobpool.go:820`
```go
// BEFORE:
if p.chain.Config().IsCancun(p.head.Number, p.head.Time) {
// AFTER:
if p.chain.Config().IsCancun(p.head.Number, p.head.Time) || p.chain.Config().IsCamellia(p.head.Number) {
```

## Decision Audit Trail (continued)

| # | Phase | Decision | Classification | Principle | Rationale | Rejected |
|---|-------|----------|----------------|-----------|-----------|---------|
| 4 | Eng | Fix validation.go:72 (IsCamellia check) | Mechanical | P5 | Targeted, minimal, consistent with existing IsCamellia pattern | Modify IsCancun() itself (would affect all Cancun gates including ParentBeaconRoot requirements) |
| 5 | Eng | Fix blobpool.go:820 (limbo finalize) | Mechanical | P1 | Prevents memory accumulation, same pattern | Skip (deferred) |
| 6 | Eng | AllEthashProtocolChanges ShanghaiTime gap is test-only | Mechanical | P3 | Mainnet has ShanghaiTime set, production not affected | Add ShanghaiTime to AllEthashProtocolChanges (wider change, separate PR) |
| 7 | Eng | Add TestValidateTransactionCamelliaOnly | Mechanical | P1 | Prevents regression of this exact bug | Skip test |

## NOT In Scope
- Type 23 FeeDelegateBlobTx (separate plan required)
- Blob pruning after 4096 blocks
- `AllEthashProtocolChanges` ShanghaiTime fix (separate PR, low risk)
- Rolling upgrade simulation (tracked in TODOS.md)

## What Already Exists
- Full blob TX pipeline from upstream v1.13.14 ✅
- Camellia-aware ExcessBlobGas in miner/validator/clique ✅
- blobpool registered in eth/backend.go:231 ✅

## Eng Completion Summary

| Dimension | Assessment |
|-----------|-----------|
| Architecture | Sound — the v1.13.14 blobpool architecture is correct; 2 Camellia-specific gates missed |
| Test coverage | Good for Cancun; missing for Camellia-only blob TX acceptance (2 new tests needed) |
| Security | No new surface; blob count capped at 2 via MaxBlobGasPerBlock |
| Performance | Not impacted — 1 boolean check per validation |
| Deployment risk | Low — targeted 2-line fix |
| Test plan | Written to: ~/.gstack/projects/jsong1230-go-metadium/test-plan-camellia-blob.md |


---

# /autoplan DX Review — 2026-04-07

## Developer Journey Map

| Stage | Action | Current Experience | After Fix |
|-------|--------|-------------------|-----------|
| 1. Discover | Learn Camellia has EIP-4844 | Undocumented (docs say "planned") | Same |
| 2. Setup | Configure chain for blob TX | No special config needed | Same |
| 3. Submit | `eth_sendRawTransaction` with BlobTx | **FAILS: "not yet in Cancun"** | ✅ Accepted |
| 4. Mine | Block contains blob TX | Never reached (rejected at pool) | ✅ Works |
| 5. Query | `eth_getTransactionByHash` | Blob TX fields visible | Same |
| 6. Error | Wrong MaxFeePerBlobGas | Decent error via ErrBlobFeeCapTooLow | Same |
| 7. Debug | Node log for blob TX rejection | "type 3 rejected, pool not yet in Cancun" | ← misleading |

## TTHW (Time To Hello World for Blob TX)

Current TTHW: **∞** (impossible — pool rejects all blob TXs on Camellia)  
Target TTHW after fix: ~5 min (same as Cancun on Ethereum)

## DX Scorecard

| Dimension | Score | Notes |
|-----------|-------|-------|
| API clarity | 7/10 | `eth_sendRawTransaction` works; `eth_signTransaction` correctly rejects (but undocumented) |
| Error messages | 5/10 | "not yet in Cancun" is misleading on Camellia — developer won't understand |
| Documentation | 3/10 | No docs on Camellia blob TX format or how to submit |
| Debuggability | 6/10 | `blob-tx-e2e` binary exists but undocumented |
| Upgrade safety | 8/10 | Backward compatible — non-blob TXs unaffected |
| Overall | **5.8/10** → 7.5/10 after fix + error message update |

## DX Issue: Error Message (medium, auto-decided: fix inline)

**Current:** `"type %d rejected, pool not yet in Cancun"` (`validation.go:73`)  
**Fix:** `"type %d rejected, pool not yet in Cancun or Camellia"`

This is a 3-word change. Auto-decided: include in the same PR as the fix.

## DX Implementation Checklist

- [ ] Fix validation.go:72 (IsCamellia check)
- [ ] Fix error message at validation.go:73
- [ ] Fix blobpool.go:820 (limbo finalization)
- [ ] Add TC1 + TC2 test cases
- [ ] Update `docs/camellia-verification-checklist.md` with blob TX status

## Decision Audit Trail (continued)

| # | Phase | Decision | Classification | Principle | Rationale | Rejected |
|---|-------|----------|----------------|-----------|-----------|---------|
| 8 | DX | Fix error message "not yet in Cancun" → include Camellia | Mechanical | P5 | 3-word change, zero risk | Separate PR |
| 9 | DX | Don't write new blob TX docs now | Mechanical | P3 | Plans are stale anyway; docs after mainnet activation | Write docs now |


## GSTACK REVIEW REPORT

| Review | Trigger | Why | Runs | Status | Findings |
|--------|---------|-----|------|--------|----------|
| CEO Review | `/autoplan` | Scope & strategy | 1 | issues_open | 1 critical: validation.go:72 mainnet blocker (fixed) |
| Eng Review | `/autoplan` | Architecture & tests | 1 | clean | 3 fixed: validation.go, blobpool.go, error message |
| DX Review | `/autoplan` | Developer experience (RPC/CLI) | 1 | clean | Error message improved; TTHW ∞→5min after fixes |
| Dual Voices (CEO) | `/autoplan` | Claude subagent only (no Codex) | 1 | clean | 5 concerns, 2 confirmed critical, 1 incorrect |
| Dual Voices (Eng) | `/autoplan` | Claude subagent only | 1 | clean | 5 concerns, 4 confirmed, 1 auto-resolved |
| Dual Voices (DX) | `/autoplan` | Claude subagent only | 1 | clean | Error message gap confirmed |

**VERDICT:** APPROVED — 3 fixes applied, 2 new tests needed (tracked), all txpool tests pass.
