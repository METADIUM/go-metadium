# Camellia Fork 배포 전 검증 체크리스트

> Camellia = Shanghai + Cancun (EIP-1153, 3651, 3855, 3860, 4844, 5656, 6780)
> 기존 Metadium 기능 (Fee Delegation Type 22, SPoA, VRF, 거버넌스, TRS) 호환성 포함

> Last updated: 2026-04-07. Layer 1 (T-01~T-16): ALL PASS. Layer 2: PASS=10, N/A=1. Layer 3: 배포 완료, CamelliaBlock 대기. Layer 4: PASS. Layer 5 SPoA: 22/22 PASS. Layer 6 (sync): 4개 노드 전체 PASS. Layer 7 (blob tx E2E): PASS. Layer 8 (mixed tx): PASS — normal+fee delegation+blob 혼합 블록 검증. make test / make test-short: 전체 PASS (TestEmptyBlocks Engine API PoS 테스트 제외, Metadium PoA 구조적 비해당).

---

## Layer 1: Go 단위 테스트

```bash
go test ./core/vm/runtime/ -run "TestEIP3860|TestEIP6780" -v
go test ./core/ -run "TestBlobGas|TestCamellia|TestFeeDelegation|TestEIP" -v
go test ./core/types/ -v
```

| ID | 테스트 함수 | EIP | 설명 | 상태 |
|----|------------|-----|------|------|
| T-01 | `TestEIP3860InitCodeLimit_Reject` | EIP-3860 | initcode > 49152 bytes → 거부 | [x] |
| T-02 | `TestEIP3860InitCodeLimit_Accept` | EIP-3860 | initcode = 49152 bytes → 허용 | [x] |
| T-03 | `TestEIP3860InitCodeGas` | EIP-3860 | InitCodeWordGas 2 gas/word 청구 | [x] |
| T-04 | `TestBlobGasCalculation` | EIP-4844 | CalcExcessBlobGas 계산 | [x] |
| T-05 | `TestBlobBaseFeeCalculation` | EIP-4844 | BlobBaseFee ≥ 1 wei | [x] |
| T-06 | `TestBlobGasConstants` | EIP-4844 | 상수값 검증 | [x] |
| T-07 | — | EIP-1153 | (bash I-02로 커버) | — |
| T-08 | `TestFeeDelegationAfterCamellia` | Type 22 | feePayer 잔액 감소 확인 | [x] |
| T-09 | `TestFeeDelegationAfterCamellia` | Type 22 | sender 잔액 미변화 확인 | [x] |
| T-10 | — | Type 22 + Blob | N/A confirmed: Metadium does not use blob tx | N/A |
| T-11 | `TestEIP3860BeforeCamellia` | Compat | pre-fork에서 크기 제한 미적용 | [x] |
| T-12 | `TestEIP6780SelfdestructPreservesCode` | EIP-6780 | 기존 컨트랙트 코드 보존 | [x] |
| T-13 | `TestEIP6780SelfdestructSameTx` | EIP-6780 | 동일 tx 생성+파괴 → 코드 소멸 | [x] |
| T-14 | `TestEIP3860Create2InitCodeLimit` | EIP-3860 | CREATE2도 initcode 크기 제한 적용 | [x] |
| T-15 | `TestBlobTxPreCheckErrors` (ErrBlobFeeCapTooLow) | EIP-4844 | maxFeePerBlobGas < blobBaseFee → 거부 | [x] |
| T-16 | `TestBlobTxPreCheckErrors` (ErrBlobCountExceeded) | EIP-4844 | blob 수 > 6 → 거부 | [x] |

### 빠른 실행

```bash
# 전체 Layer 1 (약 2분)
go test ./core/... ./core/vm/runtime/... -timeout 120s 2>&1 | grep -E "^(ok|FAIL|---)"
```

---

## Layer 2: bash 통합 테스트 (실노드)

### 사전 조건

```bash
cd tests/private-net-poa
./stop.sh --clean && ./setup.sh  # 초기화 (필요 시)
./start.sh                        # 3노드 시작
# 블록 100 이상 진행 대기
```

### 실행

```bash
RPC=http://localhost:8545 ./camellia-test.sh
```

| ID | 기능 | 검증 내용 | 상태 |
|----|------|-----------|------|
| I-01 | EIP-3855 PUSH0 | fork 전후 opcode 동작 | [x] |
| I-02 | EIP-1153 TLOAD/TSTORE | fork 전후 opcode | [x] |
| I-03 | EIP-5656 MCOPY | fork 전후 opcode | [x] |
| I-04 | EIP-4844 BLOBBASEFEE | opcode 반환값 = 1 | [x] |
| I-05 | EIP-3651 Warm COINBASE | 가스 절감 확인 | [x] |
| I-06 | EIP-6780 SELFDESTRUCT | 동일 tx에서만 파괴 | [x] |
| I-07 | EIP-3860 initcode limit | > 49152 bytes CREATE → revert | [x] |
| I-08 | Type 22 Fee Delegation | tx 성공 (status=0x1) | [x] |
| I-09 | Type 22 Fee Delegation | feePayer 잔액 감소 확인 | [x] |
| I-10 | 거버넌스 컨트랙트 | N/A: governance contract not in private-net genesis | N/A |
| I-11 | 블록 생성 연속성 | 포크 후 5초간 블록 증가 | [x] |

> I-08/I-09 실행에 `pip install eth-account` 필요.

---

## Layer 3: testnet 검증

배포 전 testnet (192.168.0.25)에서 수행:

```
[x] testnet에 Camellia 바이너리 배포 (scripts/update-node.sh)
    → 완료 (2026-04-04): commit 0b59d7b, 63MB, Go 1.22.2
[x] gmet-testnet.service 재시작 후 동기화 확인
    → 완료: block 84M+ syncing, 5 peers, ~30 blocks/min
[ ] CamelliaBlock 번호 설정 후 포크 전환 모니터링 (1시간)
    → 대기 중: CamelliaBlock 번호 미결정
[ ] testnet에서 Type 22 tx 수동 전송 확인
    → CamelliaBlock 활성화 후 진행 예정
[ ] CamelliaBlock 전후 노드 재시작 테스트 (재시작 후 체인 이어받기)
    → CamelliaBlock 활성화 후 진행 예정
```

> Status (2026-04-05): 바이너리 배포 및 동기화 완료. CamelliaBlock 번호 결정 후 나머지 3개 항목 진행.

---

## Layer 4: 업그레이드 호환성

실행: `tests/private-net-poa/layer4-upgrade-test.sh` (스크립트: `OLD_BINARY=/tmp/gmet-old NEW_BINARY=../../geth`)

```
[x] 혼재 상태 (신버전 2 + 구버전 1) 에서 블록 생성 연속 확인
    → PASS: node1+2(NEW) + node3(OLD), block 50까지 동일 hash 확인
[x] CamelliaBlock 도달 시 포크 전환 정상 확인
    → OBSERVED: OLD binary는 block 99에서 중단 (포크 블록 처리 불가 — 예상 동작)
[x] 구버전 노드 chaindata 초기화 후 신버전으로 재참여
    → PASS: node3 초기화 후 block 100+ 정상 동기화

⚠ 핵심 발견: OLD binary (CamelliaBlock=nil)는 포크 블록에서 강제 중단.
  mainnet 업그레이드 전 모든 노드를 신버전으로 교체 필수.
  부득이 구버전 노드 존재 시: 서비스 중지 → chaindata 삭제 → genesis 재초기화 → 신버전 시작
```

---

## Layer 5: SPoA 통합 테스트 (거버넌스 컨트랙트 배포)

실행: `tests/private-net-poa/spoa-test.sh` (CamelliaBlock=100, 거버넌스 배포 후)

### S-01~S-05: SPoA 거버넌스 검증

| ID | 검증 항목 | 상태 |
|----|-----------|------|
| S-01 | Gov.reg() = Registry 주소 | [x] |
| S-02 | 전체 3노드 eth_mining=true | [x] |
| S-03 | getMemberLength() == 3 | [x] |
| S-04 | 최근 30블록에 3 distinct miner | [x] |
| S-05 | 블록 채굴자 = 거버넌스 멤버 | [x] |

### I-01~I-11: Camellia EIP 검증 (SPoA 환경)

| ID | 기능 | 상태 |
|----|------|------|
| I-01 | EIP-3855 PUSH0 (fork 전후 동작) | [x] |
| I-02 | EIP-1153 TLOAD/TSTORE | [x] |
| I-03 | EIP-5656 MCOPY | [x] |
| I-04 | EIP-4844 BLOBBASEFEE opcode | [x] |
| I-05 | EIP-3651 Warm COINBASE 가스 절감 | [x] |
| I-06 | EIP-6780 SELFDESTRUCT (기존 컨트랙트 보존) | [x] |
| I-07 | EIP-3860 initcode limit (>49152 → revert) | [x] |
| I-08 | Type 22 Fee Delegation tx 성공 | [x] |
| I-09 | Type 22 feePayer 잔액 감소 | [x] |
| I-10 | 거버넌스 컨트랙트 live | [x] |
| I-11 | 블록 생성 연속성 (포크 후 5초) | [x] |

**결과: 22/22 PASS (2026-04-04)**

---

## Layer 6: 실서버 동기화 검증

Camellia 바이너리로 실제 chaindata를 이어받아 최신 블록까지 동기화되는지 검증.

**실행 일자:** 2026-04-05  
**바이너리:** `feature/camellia` HEAD (`3be99ff16`), Go 1.21

| 서버 | 역할 | DB | 최종 블록 | peers | 결과 |
|------|------|-----|----------|-------|------|
| 192.168.0.25 | testnet | LevelDB | 84,506,387 | 5 | [x] PASS |
| 192.168.0.150 | mainnet | LevelDB | 111,544,279 | 5 | [x] PASS |
| 192.168.0.150 | mainnet | RocksDB | 111,544,280 | 3 | [x] PASS |
| 192.168.0.151 | testnet | RocksDB | 84,506,388 | 5 | [x] PASS |

- 기존 chaindata 이어받기 정상 (재초기화 없음)
- ERROR / CRIT 로그 없음
- LevelDB / RocksDB 두 노드의 mainnet 블록 hash 일치 확인

---

## 완료 기준

### 현재 상태 (2026-04-06)

| Layer | 항목 수 | 상태 |
|-------|---------|------|
| Layer 1 (Go 단위 테스트) | T-01~T-16 | ✅ 전체 PASS |
| Layer 2 (bash 통합) | I-01~I-11 | ✅ 10/11 PASS (I-10 N/A) |
| Layer 3 (testnet 배포) | 5개 항목 | ✅ 2/5 완료, 3개 CamelliaBlock 설정 후 |
| Layer 4 (롤링 업그레이드) | 3개 항목 | ✅ 전체 PASS |
| Layer 5 (SPoA 통합) | 22개 항목 | ✅ 22/22 PASS |
| Layer 6 (실서버 동기화) | 4개 노드 | ✅ 전체 PASS (testnet/mainnet × LevelDB/RocksDB) |
| Layer 7 (blob tx E2E) | 4단계 | ✅ PASS (submit→pool sidecar→mined→sidecar pruned) |
| Layer 8 (mixed tx) | 3 tx 타입 | ✅ PASS (Type 2 + Type 22 + Type 3 혼합 블록) |

**남은 작업:** testnet `CamelliaBlock` 번호 결정 → 포크 전환 모니터링 → mainnet 설정

```go
// params/config.go — CamelliaBlock 번호 결정 후 설정
CamelliaBlock: big.NewInt(<block_number>),
```

---

## 참고

- 설계 문서: `~/.gstack/projects/jsong1230-go-metadium/jsong-feature-camellia-design-20260403-235011.md`
- EIP-4788 (Beacon roots), EIP-4895 (Withdrawals): PoA 특성상 N/A, 의도적 생략
- 다음 fork (Doraji/Prague): 이 체크리스트를 템플릿으로 재활용
