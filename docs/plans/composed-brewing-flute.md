# Phase 1: Camellia Fork 구현 계획

## Context

Go Metadium의 EVM을 London에서 Shanghai + Cancun으로 업그레이드합니다. 이를 통해 최신 이더리움 기능과의 호환성을 확보하고, Metadium 특화 기능(Fee Delegation, ETCD, SPoA)은 그대로 유지합니다.

**현재 상태**: Bokbunja (London EVM, Go 1.21)
**목표 상태**: Camellia (Shanghai + Cancun EVM)

---

## 구현 EIP 목록

### Shanghai EIPs
| EIP | 내용 | opcode | 난이도 |
|-----|------|--------|--------|
| EIP-3651 | Warm COINBASE | - | 낮음 |
| EIP-3855 | PUSH0 | 0x5F | 낮음 |
| EIP-3860 | Initcode Size Limit | - | 낮음 |

### Cancun EIPs
| EIP | 내용 | opcode | 난이도 |
|-----|------|--------|--------|
| EIP-1153 | TLOAD/TSTORE | 0x5C, 0x5D | 중간 |
| EIP-5656 | MCOPY | 0x5E | 낮음 |
| EIP-6780 | SELFDESTRUCT Limit | - | 중간 |
| EIP-4844 | Blob Transactions | 0x49, 0x4A | 높음 |

### 제외 EIPs (PoS 전용)
- EIP-4895 (Validator Withdrawals)
- EIP-4788 (Beacon Block Root)
- EIP-7251 (Validator Max Stake)

---

## 구현 순서

### 1단계: 기반 작업 (1-2일)
1. `params/config.go` - CamelliaBlock 추가
2. `core/vm/opcodes.go` - 누락된 opcode 정의

### 2단계: Shanghai EIPs (2-3일)
1. **EIP-3855 (PUSH0)** - opcode 정의됨, jump table만 추가
2. **EIP-3651 (Warm COINBASE)** - access list 수정
3. **EIP-3860 (Initcode Size Limit)** - create 검증 추가

### 3단계: Cancun Opcodes (3-4일)
1. **EIP-5656 (MCOPY)** - 메모리 복사 opcode
2. **EIP-1153 (TLOAD/TSTORE)** - transient storage
3. **EIP-6780 (SELFDESTRUCT)** - 동작 변경

### 4단계: Blob Transactions (7-10일)
1. **EIP-4844** - Type 3 트랜잭션, KZG precompile

### 5단계: 통합 테스트 (3-5일)
- Fee Delegation 호환성
- ETCD coordination
- SPoA 합의

---

## 수정 파일 목록

### params/
```
config.go          # CamelliaBlock, IsCamellia() 추가
protocol_params.go # Blob gas 상수, MaxInitCodeSize 추가
```

### core/vm/
```
opcodes.go         # TLOAD(0x5C), TSTORE(0x5D), MCOPY(0x5E), BLOBHASH(0x49), BLOBBASEFEE(0x4A)
jump_table.go      # camelliaInstructionSet 추가
instructions.go    # opPush0, opTload, opTstore, opMcopy, opBlobHash, opBlobBaseFee
eips.go            # enable3651, enable3855, enable3860, enable1153, enable5656, enable6780, enable4844
contracts.go       # KZG precompile (0x0A) 추가
evm.go             # transient storage, createdInTx 추적
```

### core/state/
```
statedb.go         # TransientStorage 필드, AddCreatedContract 메서드
transient_storage.go  # 신규: TransientStorage 타입
```

### core/types/
```
transaction.go     # BlobTxType (3) 추가
tx_blob.go         # 신규: BlobTx 구조체
blob.go            # 신규: Blob, BlobTxSidecar
```

### crypto/
```
kzg4844/kzg4844.go # 신규: KZG point evaluation
```

---

## 새로 생성 파일

```go
// core/state/transient_storage.go
type TransientStorage struct {
    data map[common.Address]map[common.Hash]common.Hash
}

func (t *TransientStorage) Get(addr common.Address, key common.Hash) common.Hash
func (t *TransientStorage) Set(addr common.Address, key, value common.Hash)
func (t *TransientStorage) Clear()

// core/types/tx_blob.go
type BlobTx struct {
    ChainID    *big.Int
    Nonce      uint64
    // ... standard fields ...
    MaxFeePerBlobGas   *big.Int
    BlobVersionedHashes []common.Hash
    V, R, S    *big.Int
}

// crypto/kzg4844/kzg4844.go
func VerifyBlobProof(commitment, z, y, proof []byte) error
```

---

## Metadium 특화 고려사항

### Fee Delegation 호환성
- Type 22 (FeeDelegateDynamicFeeTx)는 변경 없음
- Type 23 (FeeDelegateBlobTx)은 선택적 구현
- Blob gas 비용도 FeePayer가 부담

### SPoA 합의
- PoS 관련 EIP 제외
- Blob 보존 기간: 4096 blocks (Ethereum과 동일)
- 합의 로직 변경 없음

### ETCD Coordination
- Blob 트랜잭션도 정상 전파 필요
- 메모리 사용량 모니터링

---

## 검증 방법

### 단위 테스트
```bash
# 각 EIP별 테스트
go test ./core/vm/... -run TestEIP3651
go test ./core/vm/... -run TestEIP3855
go test ./core/vm/... -run TestEIP1153
go test ./core/vm/... -run TestEIP4844

# 전체 EVM 테스트
go test ./core/vm/... -v
```

### 통합 테스트
```bash
# Fee Delegation 호환성
go test ./core/types/... -run TestFeeDelegate

# 블록체인 테스트
go test ./core/... -run TestBlockchain

# 전체 테스트
make test
```

### 빌드 검증
```bash
# 전체 빌드
make gmet

# Docker 빌드
make gmet-linux
```

---

## 리스크 및 완화

| 리스크 | 완화 방안 |
|--------|----------|
| KZG 라이브러리 호환성 | gnark-crypto 업그레이드 또는 go-kzg-4844 사용 |
| Fee Delegation 충돌 | Type 23 분리, 충분한 통합 테스트 |
| Blob storage 부하 | 초기 max blob count = 2 제한 |
| SPoA 타이밍 영향 | Blob 전파 타이밍 조정 |

---

## 예상 소요 기간

| 단계 | 기간 |
|------|------|
| 기반 작업 | 1-2일 |
| Shanghai EIPs | 2-3일 |
| Cancun Opcodes | 3-4일 |
| Blob Transactions | 7-10일 |
| 통합 테스트 | 3-5일 |
| **총계** | **16-24일 (약 3-5주)** |

---

## 시작점

첫 번째 작업: `params/config.go`에 CamelliaBlock 추가 및 `core/vm/opcodes.go`에 누락된 opcode 정의
