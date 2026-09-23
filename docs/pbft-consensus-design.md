# PBFT 합의 엔진 설계안 (go-metadium)

- **Date:** 2026-09-23 (rev.4 — 리뷰 1~8 반영, §15)
- **Status:** Design (pre-implementation) — 검토 후 착수
- **적용 대상:** **신규 프라이빗 네트워크**의 초기 설정. 기존 Metadium Mainnet/Testnet 적용은 범위 밖
- **전환 방식:** PoA로 부트스트랩 → 제네시스에 지정한 `BftBlock` 높이에서 PBFT로 전환 (§9, B안)
- **용어:** 본 문서의 pre-fork / post-fork 는 각각 `BftBlock` 이전 / 이후 높이를 뜻한다.
  `BlockHash(h)` 는 §5.2에서 정의하는, `BftRound`·`CommitSeals` 를 뺀 헤더 해시다.

---

## 1. 목표와 범위

### 1.1 목표
- 신규 프라이빗 네트워크를 구성할 때 합의 방식으로 **PBFT를 선택**할 수 있게 한다.
  선택하면 블록 생성 권한 조정이 **etcd(raft, CFT)** 에서 **BFT 합의**로 바뀐다.
- **즉시 finality**(committed = final)를 확보하여 현재의 `head - (N/2+1)` 휴리스틱
  (`metadium/admin.go:645`)을 제거한다.
- 비잔틴 노드(악의적 이중 제안, 거짓 투표, 침묵)가 `f = floor((N-1)/3)` 이하일 때
  safety(포크 없음)와 liveness(블록 생성 지속)를 보장한다.
- 거버넌스 컨트랙트가 정의하는 validator set을 그대로 합의 주체로 사용한다.

### 1.2 범위 밖 (Non-goals)
- **기존 Metadium Mainnet/Testnet의 PBFT 전환** (운영 중인 체인의 하드포크 마이그레이션)
- 슬래싱/벌금 (거버넌스 컨트랙트 변경이 필요 — 별도 과제). 단 **이중 서명 증거 수집은 범위 안** (§7.1)
- validator set의 동적 가중치(stake weighting) — 현행 1노드 1표 유지
- etcd 제거 자체 (본 설계는 **블록 생성 경로에서만** 제거, 운영 채널은 잔존)
- 라이트 클라이언트 / 체크포인트 동기화 최적화

---

## 2. 현재 상태 (baseline)

코드 인용은 `release/v1.1.4` (`e804c8fe4`, master `b716f5b03` 에 병합) 기준이다.

| 항목 | 현재 구현 | 위치 |
|---|---|---|
| 합의 상수 | `ConsensusPoW=1, PoA=2, ETCD=3, PBFT=4` | `params/protocol_params.go:240-244` |
| CLI 검증 | `>= ConsensusETCD` 거부 → **3,4 사용 불가** | `cmd/utils/flags.go:2084` |
| PBFT 참조 | 상수 정의 + `StartAdmin` 허용목록 **2곳뿐**, 로직 없음 | `metadium/admin.go:1293` |
| 리더 선출 (pre-Bokbunja) | etcd raft leader → `IsMiner()` | `metadium/legacy.go:561` |
| 리더 선출 (Bokbunja~) | etcd CAS 기반 mining token (TTL 10s) | 진입점 `metadium/miner/miner.go:104` → 구현 `metadium/sync.go:145` (연결 `admin.go:2383`), CAS `metadium/etcdutil.go:989` (`acquireTokenSync`, 트랜잭션 1024) |
| 블록 서명 | 단일 서명 `MinerNodeId`/`MinerNodeSig` (**state root**에 대한 ECDSA) | `consensus/ethash/consensus.go:642-648` |
| 블록 서명 검증 | `verifyBlockSig` — 거버넌스 enode 목록 대조 | `metadium/admin.go:1721` |
| 제안자 제한 | 최근 `N/2` 블록 내 재등장 금지 (Pangyo~) | `metadium/miner_limit.go:205`, 호출 `admin.go:1765` |
| 보상 계산 | 진입점 `calculateRewards` → 구현 메서드 | `metadium/admin.go:1610` → `1561` |
| 보상 검증 | **`verifyRewards` 는 `return nil` 뿐인 빈 함수.** 실제 검증은 Finalize의 재계산 + state root 대조이며, 이때 `header.Rewards`/`Coinbase` 를 **재계산 값으로 덮어쓴다** | `metadium/admin.go:1618`, `consensus/ethash/consensus.go:741, 754` |
| 타임스탬프 | `header.Time` 은 **초 단위 `uint64`**. PoA 경로에는 부모 대비 단조 증가 검사가 없다 (PoW에서만 `<=` 거부). 미래 허용치 15초 | `core/types/block.go:79`, `consensus/ethash/consensus.go:234, 238`, `miner/worker.go:1303` |
| finality | `head - (N/2+1)` 휴리스틱 | `metadium/admin.go:645`, `core/blockchain_reader.go:79` |
| 제안자 게이트 | `AcquireMiningToken` / `HasMiningToken` | `miner/worker.go:1671, 1781, 1833` |
| 봉인 → 기록 | `Seal()` 직후 **동기적으로** `WriteBlockAndSetHead` | `miner/worker.go:1863, 1912` |
| P2P | `meta/66, 68, 69`, 메시지 코드 최대 `0x18` | `eth/protocols/eth/protocol.go:50, 57, 92` |

**핵심 관찰 4가지**

1. **헤더는 이미 비표준이다.** `Header` 에 `Fees`, `Rewards`, `MinerNodeId`,
   `MinerNodeSig` 가 이미 추가되어 있고 (`core/types/block.go:66`), RLP는
   `headerRlp` 를 경유한다 (`core/types/block.go:104`, 인코딩 `:309`). commit seal 필드 추가는
   **기존 전례를 따르는 변경**이다. 단 `headerRlp` 의 마지막 필드는 `BlobGasUsed` 이며,
   `Header.ParentBeaconRoot` 는 `headerRlp` 에 **없어서 전송·해시에서 빠진다** (PoA 경로에서는 항상 nil).
2. **`SealHash` 는 블록 해시와 덮는 범위가 다르다.** `SealHash`
   (`consensus/ethash/consensus.go:664`)는 `Rewards`/`MinerNodeId`/`MinerNodeSig`/
   `MixDigest`/`Nonce` 를 **제외**하지만, 블록 해시(`headerRlp`)에는 이들이 모두 들어간다.
   따라서 **`SealHash` 는 PBFT 서명 대상으로 쓰면 안 된다** (§5.2).
3. **보상 필드는 검증되지 않고 덮어써진다** (위 표). PBFT에서는 비교 검증으로 바꿔야 한다 (§7.5).
4. **테스트넷이 3노드다.** `tests/private-net-poa/docker-compose.yml` 은 node1–node3.
   `N=3` 이면 `f = 0` 으로 **비잔틴 내성이 0**이다. PBFT 검증을 하려면 최소 4노드,
   권장 7노드(`f=2`)로 확장해야 한다.

---

## 3. 프로토콜 선택: 순수 PBFT가 아니라 IBFT 2.0 계열

Castro-Liskov PBFT(1999)는 **고정 replica 집합 + 클라이언트 응답 모델**을 전제한다.
블록체인에 그대로 옮기면 다음이 어긋난다.

| PBFT 원형 | 블록체인 부적합 지점 | 본 설계의 처리 |
|---|---|---|
| 클라이언트가 요청 전송 → replica가 응답 | 요청 = 블록 제안, 응답 대상 없음 | 제안자가 mempool에서 블록 생성 |
| checkpoint/garbage collection | 체인 자체가 로그, GC 불필요 | 제거 |
| stable checkpoint 기반 view change | 체인 높이가 자연 시퀀스 | height 단위로 인스턴스 분리 |
| replica 집합 정적 | 거버넌스로 validator 변경 | epoch(= `gov.modifiedBlock`) 경계에서 교체 |
| 응답 f+1개 수집 | 블록에 증명 필요 | **commit seal 2f+1개를 헤더에 첨부** |

따라서 **IBFT 2.0 (Besu) 계열**을 채택한다. 3-phase(PRE-PREPARE/PREPARE/COMMIT),
`2f+1` 쿼럼, round change를 유지하되 블록체인에 맞게 정리한 변형이며,
"PBFT 계열"이라는 기존 `ConsensusPBFT` 상수 의미와 일치한다.

> Tendermint 대비: Tendermint는 lock/unlock 규칙과 POL(proof-of-lock) round 처리가
> 더 정교하지만 상태기계가 크고, `prevote/precommit` 2-phase + nil-vote 개념이
> 기존 Metadium 블록 파이프라인과의 접합면을 더 많이 바꾼다. IBFT2가 이식 비용이 낮다.
> 단 투표 상태 영속화(§6.1)는 Tendermint의 WAL·서명자 마지막 서명 상태 기록 방식을 따른다.

---

## 4. 프로토콜 정의

### 4.1 파라미터

```
N         = validator 수 (거버넌스 getNodeLength)
f         = floor((N-1)/3)
Quorum    = 2f+1 = ceil((2N+1)/3)   // N=4→3, N=7→5, N=10→7, N=13→9
```

- `N < 4` 이면 `f=0` → BFT 무의미. **PBFT 전환 전제조건: `N >= 4`** (§9.3),
  **전환 이후에도 `N >= 4` 를 블록 유효성 규칙으로 유지** (§9.3.1).
- `N` 은 height `n` 기준 **부모 블록(n-1) 상태**의 거버넌스 값을 사용한다
  (`verifyBlockSig` 가 이미 `height-1` 을 쓴다 — `metadium/admin.go:1729`).

### 4.2 Validator set과 epoch

- 출처: `getMetaNodes` (`metadium/admin.go:462`) — 이미 `Name` 기준 정렬되어 있어
  **모든 노드에서 동일한 인덱스**를 얻는다. 그대로 validator 순서로 사용한다.
- 신원: enode 공개키 64바이트 (`metaNode.Enode`). 서명키 = 노드키.
  기존 `VerifyBlockSig` 와 동일한 키 체계라 키 관리 변경이 없다.
- 캐싱: `coinbaseEnodeCache` (`gov.modifiedBlock` 키)를 재사용한다
  (`metadium/sync.go:58`).
- **epoch 경계**: `gov.modifiedBlock` 이 바뀌는 블록에서 validator set이 바뀐다.
  높이 `n` 의 합의는 `n-1` 상태 기준 set으로 수행하므로, set 변경 블록 자체도
  이전 set이 합의한다. 별도 epoch 전환 프로토콜이 불필요하다.

### 4.3 제안자(proposer) 선출

```go
proposer(height, round) = validators[(height + round) % N]
```

- 결정적이고, round가 오를 때마다 다음 노드로 넘어가 liveness를 보장한다.
- **기존 `isEligibleMiner` (최근 `N/2` 블록 내 재제안 금지)는 fork 이후 비활성화**한다.
  round change 중에는 이 규칙을 만족시킬 수 없어 교착이 발생한다. 라운드로빈 자체가
  더 강한 공정성을 보장하므로 대체 관계다.
  → `metadium/admin.go:1762` 의 `isPangyo` 분기(`verifyMinerLimit` 호출 `:1765`) 옆에
  `IsBft(height)` 분기를 추가하여 post-fork 높이에서는 `verifyMinerLimit` 을 건너뛴다.

### 4.4 3-phase 상태기계

높이 `n`, 라운드 `r` 인스턴스. **모든 서명 메시지는 WAL에 기록·fsync 한 뒤 전송한다** (§6.1).

```
NEW_ROUND
  ├─ 내가 proposer(n,r)  → (lock 있으면 그 블록, 없으면 신규 생성) → PRE-PREPARE 브로드캐스트
  └─ 아니면              → PRE-PREPARE 수신 대기 (deadline(n,r) 타이머 가동, §4.5)

PRE-PREPARE 수신 시 (from proposer(n,r), 서명 유효, 부모 = 로컬 head)
  ├─ 블록 완전 검증 (§4.8): VerifyHeader + 타임스탬프 범위 + 바디 실행 + state root 대조
  │                        + Rewards/Coinbase 비교 + 실행 후 N >= 4
  ├─ 실패 → round change 트리거 (ROUND_CHANGE(r+1))
  └─ 성공 → [WAL] PREPARE(n, r, digest) 브로드캐스트 → PRE_PREPARED

PREPARE 를 Quorum-1 개 수집 (+ 자신) → PREPARED  ([WAL] lock = (r, digest, PREPARE 증명, 블록))
  └─ [WAL] COMMIT(n, r, digest, commitSeal) 브로드캐스트

COMMIT 을 Quorum 개 수집 → COMMITTED
  └─ 헤더에 BftRound=r, CommitSeals 첨부 → WriteBlockAndSetHead → WAL 정리 → 다음 높이 NEW_ROUND
```

- **`digest = BlockHash(header)`** (§5.2). `SealHash` 가 아니다 — `SealHash` 는 블록 해시에
  들어가는 필드 일부를 빼므로, 같은 digest에 서로 다른 블록 해시가 대응할 수 있다 (§2 관찰 2).
- validator는 자신이 PREPARE를 보낸 (n,r,digest) 이외의 제안에 PREPARE를 보내지
  않는다. 라운드 변경 후 새 제안을 받으면 §4.5의 lock 규칙을 따른다.

### 4.5 Round change (view change)

트리거:
- `deadline(n, r)` 만료 (제안 미수신, 쿼럼 미달)
- 제안 검증 실패 / 잘못된 제안자
- `ROUND_CHANGE(r')` 를 `f+1` 개 수신하고 `r' > r` → 즉시 `r'` 로 점프
  (Bracha amplification, 정직한 노드 1개 이상이 이미 넘어갔다는 증거)

```
ROUND_CHANGE(n, r+1, preparedRound, preparedBlock?, prepareCertificate?)
```

- `ROUND_CHANGE` 를 `Quorum` 개 수집하면 라운드 `r+1` 진입.
- 새 proposer는 수집한 `ROUND_CHANGE` 중 **`preparedRound` 가 가장 높은
  `preparedBlock` 을 재제안**해야 한다 (없으면 신규 생성). 이것이 safety의 핵심:
  이미 어떤 정직 노드가 PREPARED된 블록은 이후 라운드에서 뒤집히지 않는다.
- **재제안 블록의 헤더는 한 바이트도 바꾸지 않는다.** `Coinbase`, `Time`, `MinerNodeId`,
  `MinerNodeSig` 모두 원 제안자의 값을 그대로 쓴다. 바꾸면 `BlockHash` 가 달라져 lock이 깨진다.
  `BftRound` 는 `BlockHash` 에서 제외되므로 커밋 시점에 커밋 라운드로 채운다 (§5.3).
- 재제안 시 `ROUND_CHANGE` 증명 집합을 PRE-PREPARE에 동봉하여 수신자가
  제안 정당성을 검증한다.

#### 블록 타이밍과 round timeout

현행 블록 타이밍 (`docs/enterprise-block-timing.md`):

| 네트워크 | 설정 | 동작 |
|---|---|---|
| Mainnet / Testnet | 거버넌스 `blockCreationTime = 2000` | 2초마다 블록 (트랜잭션 없어도 빈 블록) |
| 프라이빗 (현행 운영 프로파일) | `blockCreationTime` 1–2초 + `--metadium.block.idleseal 100` + 빈 블록 5초 | 트랜잭션이 있으면 **~100ms** 안에 봉인, 없으면 **5초마다** 빈 블록 |

- 블록 간격의 출처는 거버넌스 `EnvStorage.getBlockCreationTime` 이다.
  `params.BlockInterval`(`--metadium.block.interval`)은 저장만 되고 읽히지 않으므로
  타이머 계산에 쓰지 않는다.
- PBFT가 이 프로파일을 유지해야 한다: **트랜잭션이 오면 즉시(100ms) 제안, 유휴 시 5초
  빈 블록.** validator는 제안자의 mempool을 모르므로, 유휴 상태의 정상 대기(5초)를
  장애로 오인하지 않는 타이머가 필요하다.

**타이머는 헤더 시각이 아니라 로컬 시계로 잰다.** `header.Time` 은 초 단위라 100ms 블록이
같은 값을 공유하고, 무엇보다 **제안자가 고르는 값**이다. 이를 타이머 기준으로 쓰면
(rev.3 설계) 제안자가 `Time` 을 미래로 밀어 모든 validator의 타이머를 최대 15초 늦추거나,
과거로 당겨 **다음 높이의 라운드 0을 즉시 만료**시킬 수 있다 (탐지되지 않는 liveness 공격).

```go
// committedAt(n-1): 이 노드가 높이 n-1 을 커밋(COMMITTED)한 로컬 단조 시계 시각
// roundStart(r):    이 노드가 라운드 r 에 진입한 로컬 단조 시계 시각
deadline(n, 0) = committedAt(n-1) + EmptyBlockInterval + BftBaseTimeout
deadline(n, r) = roundStart(r)    + BftBaseTimeout * 2^min(r, BftMaxBackoffExp)   // r >= 1

// 기본값 제안
//   EmptyBlockInterval = 5s   (현행 프라이빗 운영 프로파일)
//   BftBaseTimeout     = 2s   (블록 생성 + 3-phase 메시지 왕복 + 여유)
//   BftMaxBackoffExp   = 5    (라운드 r>=1 상한 2s * 32 = 64s)
```

- 노드 간 `committedAt` 차이는 COMMIT 메시지 도착 시차(LAN에서 ms 수준)뿐이라
  NTP 편차의 영향을 받지 않고, 공격자가 조작할 입력이 없다.
- **이른 제안은 언제든 받는다.** 트랜잭션이 들어와 제안자가 100ms 만에 제안하면
  validator는 즉시 PREPARE한다. 타이머는 "너무 늦은" 제안만 걸러낸다.
- **`EmptyBlockInterval` 은 합의 파라미터가 된다.** 모든 validator의 타임아웃 계산에
  들어가므로 **노드마다 달라지면 안 된다.** 제네시스 `bft.emptyBlockInterval` 로 고정한다 (§8.1).
- `idleseal`(100ms)은 제안자 로컬 동작이라 합의와 무관하다. 기존 플래그를 그대로 쓴다.
- 기동 시 `EmptyBlockInterval >= blockCreationTime` 인지 확인한다
  (`emptyinterval` 이 온체인 간격보다 작으면 효과가 없다는 기존 제약과 같음).

#### 타임스탬프 규칙 (post-fork)

타이머에서 분리했더라도 `header.Time` 은 EVM `block.timestamp` 로 노출되므로 범위를 제한한다.

| 규칙 | 적용 위치 | 목적 |
|---|---|---|
| `header.Time >= parent.Time` | `VerifyHeader` (동기화 포함 항상) | 과거로 당기기 차단. 엄격한 `>` 는 초 단위에서 초당 1블록 상한이 되어 100ms 프로파일과 충돌하므로 쓰지 않는다 |
| `\|header.Time − localNow\| <= BftTimeDrift` (기본 2s) | **PRE-PREPARE 검증에서만** | 미래·과거로 밀기 차단. 과거 블록 동기화 시에는 로컬 시계와 비교할 수 없으므로 적용하지 않고, commit seal로 검증한다 |

- **해상도는 초 단위를 유지한다.** `Time` 을 ms로 바꾸면 EVM `block.timestamp` 의미가
  바뀌어 컨트랙트·도구가 깨진다. 타이머를 헤더에서 분리했으므로 ms 해상도가 필요한 곳이 없다.
  향후 ms가 필요해지면 `Time` 을 바꾸지 않고 별도 선택 필드를 추가한다.

### 4.6 메시지 포맷과 서명

```go
type BftMsgType uint8
const (
    MsgPreprepare  BftMsgType = 1
    MsgPrepare     BftMsgType = 2
    MsgCommit      BftMsgType = 3
    MsgRoundChange BftMsgType = 4
)

// 서명 대상: 봉투(envelope)의 RLP 해시
type BftMessage struct {
    Type      BftMsgType
    Height    *big.Int
    Round     uint64
    ChainID   uint64        // 리플레이 방지: 체인 간 교차 서명 차단
    Digest    common.Hash   // = BlockHash(제안 헤더), §5.2
    Payload   []byte        // Preprepare: 블록 RLP + RC 증명 / RoundChange: 증명
    CommitSeal []byte       // Commit 에만: sign(commitDigest)
    Signature []byte        // sign(keccak(rlp(위 필드들, Signature 제외)))
}

// commit seal 서명 대상 (헤더에 영구 보존되는 증명)
commitDigest = keccak256(rlp([BlockHash(header), Round, ChainID, byte(0x02)]))
```

- `0x02` 도메인 구분자: 메시지 서명과 commit seal 서명이 절대 교차 사용되지 않도록.
- `ChainID` 포함: 다른 네트워크(고객사·환경)의 서명이 재사용되는 것을 차단 (§9.5).
- 서명자 = `ecrecover` → enode 공개키 → validator 인덱스. 미등록 키는 즉시 폐기.

### 4.7 안전성 논증 요약

전제: (a) digest가 블록 해시 전체를 덮는다 (§5.2), (b) 정직 노드는 재시작해도 이전 투표와
lock을 잃지 않는다 (§6.1), (c) 같은 노드키가 두 곳에서 동시에 서명하지 않는다 (§6.1 운영 규칙).

- **Agreement:** 두 블록이 같은 높이에서 각각 `2f+1` commit을 모으려면 두 쿼럼이
  최소 `f+1` 노드에서 겹치고, 그중 최소 1개는 정직 노드다. 정직 노드는 한 라운드에
  한 digest만 COMMIT하므로 모순. 라운드 간에는 §4.5의 재제안 규칙이 보장한다.
  전제 (b)가 없으면 재시작한 정직 노드가 사실상 비잔틴 노드가 되어 이 논증이 무너진다.
- **Validity:** 모든 정직 노드가 PREPARE 전에 블록을 완전 실행·검증한다 (§4.8).
- **Termination:** `f+1` amplification + 지수 백오프로, GST 이후 정직한 proposer가
  걸리는 라운드에서 종료. 타이머가 헤더 시각에 의존하지 않으므로 제안자가 이를 방해할 수 없다.

### 4.8 PRE-PREPARE 검증 목록

PREPARE를 보내기 전에 모두 통과해야 한다.

1. 발신자 = `proposer(n, r)`, 메시지 서명 유효, 부모 = 로컬 head
2. 재제안이면: 동봉된 ROUND_CHANGE 증명이 유효하고, 블록이 최고 `preparedRound` 의 블록과 **`BlockHash` 가 같음**
3. `VerifyHeader` (post-fork 규칙 포함, §5.3) + 타임스탬프 범위 (§4.5)
4. 바디 실행 → state root 일치
5. **`Rewards`·`Coinbase` 를 재계산해 헤더 값과 비교, 다르면 거부** (덮어쓰지 않음, §7.5)
6. **실행 후 상태의 거버넌스 노드 수 `N >= 4`** (§9.3.1)

---

## 5. 헤더 변경과 블록 해시

### 5.1 추가 필드

```go
// core/types/block.go — Header 및 headerRlp 양쪽에, headerRlp 의 BlobGasUsed 바로 뒤(맨 끝)에 추가
    // BFT fork: 커밋된 라운드와 2f+1 commit seal. BlockHash 에서는 제외된다.
    BftRound    uint64   `json:"bftRound"    rlp:"optional"`
    CommitSeals [][]byte `json:"commitSeals" rlp:"optional"`
```

- `rlp:"optional"` 은 **꼬리에서만** 생략 가능하므로 두 필드는 `headerRlp` 의 맨 뒤에 둔다.
  `headerToHeaderRlp` / `headerRlpToHeader` (`block.go:187, 216`) 변환에도 추가한다.
- 뒤쪽 선택 필드가 채워지면 앞쪽의 nil 선택 필드는 0으로 인코딩되어 디코딩 시 nil이 아닌 0이 된다
  (`BaseFee` nil ↔ 0 의미 변화). 따라서 **`IsBft` 이면 `IsCamellia` 여야 한다**를 chain config
  검증에 넣는다 — Camellia 이후 헤더는 앞쪽 선택 필드가 모두 채워져 있다.
- PBFT 블록은 `ParentBeaconRoot == nil` 이어야 한다 (`headerRlp` 에 없어 전송되지 않는 필드).

### 5.2 `BlockHash` — 블록 해시와 서명 대상의 통일

`Header.Hash()` 는 `rlpHash(h)` 로, `EncodeRLP` → `headerRlp` 를 거친다 (`core/types/block.go:295, 309`).
commit seal을 그대로 포함하면 노드마다 **수집한 seal 집합이 달라** 동일 블록이
다른 해시를 갖게 되어 체인이 갈라진다. 따라서 해시에서 `BftRound`/`CommitSeals` 를 뺀다.

```go
// BlockHash: 블록 해시 = PBFT 서명 대상(digest). BftRound/CommitSeals 만 제외한다.
func (h *Header) Hash() common.Hash {
    if h == nil { return common.Hash{} }
    if metaminer.IsPoW() { return rlpHash(HeaderToHeaderLegacy(h)) }
    if h.CommitSeals != nil || h.BftRound != 0 {
        cpy := CopyHeader(h)
        cpy.CommitSeals = nil
        cpy.BftRound = 0
        return rlpHash(cpy)   // 꼬리 선택 필드가 비면 pre-fork 인코딩과 동일
    }
    return rlpHash(h)
}
```

- **PBFT digest는 이 해시(`BlockHash`)다.** `SealHash` 는 `Rewards`/`MixDigest`/`Nonce`/
  `MinerNodeId`/`MinerNodeSig` 를 빼므로, 이를 digest로 쓰면 2f+1이 커밋한 digest 하나에
  이 필드들만 다른 **여러 블록 해시**가 대응한다. 노드마다 다른 해시를 저장하면 다음 블록의
  `ParentHash` 가 갈라진다 — 합의는 됐는데 체인이 갈라지는 결함이다.
- **seal이 해시에 없으면 제거/위조가 가능하지 않은가?** — 가능하지만 무의미하다.
  post-fork `VerifyHeader` 가 `len(valid seals) >= Quorum` 을 강제하므로,
  seal을 떼어낸 블록은 어떤 정직 노드도 받아들이지 않는다.
- **pre-fork 블록에 seal을 붙이는 경우** — `Hash()` 는 높이가 아니라 필드 존재로 분기하므로
  (Header는 chain config를 모른다) pre-fork 블록에 임의의 seal을 붙여도 해시가 같아,
  같은 해시에 RLP가 둘 존재하게 된다. **`!IsBft(number)` 이면 `CommitSeals == nil && BftRound == 0`
  을 `VerifyHeader` 에서 강제**해 닫는다 (§5.3). 이 규칙은 "라운드 0 + seal 없음" 인코딩이
  pre-fork와 구분되지 않는 문제도 함께 닫는다 — post-fork 블록은 항상 seal이 Quorum 이상이다.

### 5.3 헤더 검증 규칙과 `BftRound` 정의

**`BftRound` = 커밋된 라운드.** commitDigest의 `Round` 와 같아야 seal을 검증할 수 있다.
블록이 처음 제안된 라운드는 기록하지 않는다 (안전성에 불필요, §4.5 재제안 규칙으로 헤더 불변).

| 높이 | 규칙 |
|---|---|
| pre-fork (`!IsBft`) | `CommitSeals == nil`, `BftRound == 0` |
| post-fork (`IsBft`) | `IsCamellia`, `ParentBeaconRoot == nil`, `Time >= parent.Time` |
| post-fork | `MinerNodeSig` 가 **높이 `n-1` validator set 중 한 명의 서명**인가 (기존 `verifyBlockSig` 로직) |
| post-fork | `CommitSeals` 가 `Quorum` 개 이상, 전부 서로 다른 validator, 전부 `commitDigest(BlockHash, BftRound, ChainID)` 에 대해 유효 |

- rev.3의 "`MinerNodeSig` 가 `proposer(height, BftRound)` 의 키와 일치" 규칙은 **삭제**한다.
  round change 후 재제안된 블록은 원 제안자의 `MinerNodeSig` 를 그대로 가지므로, 커밋 라운드의
  proposer와 다르다. 커밋 라운드의 제안 자격은 합의 중 ROUND_CHANGE 증명으로 확인했고,
  import 시점에는 2f+1 seal이 그 결과를 증명한다.
- `MinerNodeSig` 는 기존대로 **블록을 만든 노드의 신원 증명**으로 유지한다
  (`consensus/ethash/consensus.go:642-648`).

### 5.4 Difficulty / fork choice

- `header.Difficulty` 는 고정값(현행 `params.FixedDifficulty=1`) 유지 — 하위 호환.
- **fork choice는 총 난이도가 아니라 finality로 결정**한다. `insertChain` 경로에서
  `blockNumber <= finalizedNumber` 인 재구성(reorg) 요청은 거부한다. committed
  블록은 최종이므로 정상 동작에서 발생할 수 없고, 발생하면 공격 또는 버그다.

---

## 6. 코드 구조

```
consensus/metabft/
    engine.go        // consensus.Engine 구현 (Prepare/Finalize/Seal/VerifyHeader/SealHash)
    core.go          // 상태기계: 라운드 진행, 메시지 처리
    backend.go       // 체인/miner/p2p 연결 어댑터 (core 는 체인을 직접 모르게)
    validators.go    // validator set, proposer 선출, 쿼럼 계산
    messages.go      // BftMessage RLP, 서명/검증, 도메인 구분자
    roundstate.go    // (height, round) 별 수집 상태, PREPARED lock (메모리)
    wal.go           // 투표·lock 영속화 (§6.1) — 서명 전송 전 fsync
    evidence.go      // 이중 서명 증거 저장 (§7.1)
    roundchange.go   // round change 수집과 증명 검증
    timer.go         // 로컬 단조 시계 기반 deadline (§4.5)
    snapshot.go      // epoch 별 validator set 캐시
    api.go           // RPC: metabft_getValidators, _getRoundState, _status, _readiness, _getEvidence

eth/protocols/metabft/
    protocol.go      // 서브프로토콜 "metabft/1" 정의
    handler.go       // 메시지 수신/브로드캐스트, 중복 억제 + 이중 서명 탐지 (§7.1)
    peer.go
```

**핵심 원칙:** `core.go` 의 상태기계는 `backend` 인터페이스만 의존하여
체인/네트워크 없이 단위 테스트가 가능해야 한다 (결정적 시뮬레이션 테스트가
이 프로젝트의 검증 핵심이다). WAL과 시계도 인터페이스로 주입해, 시뮬레이션에서
"크래시 → 재시작"과 시각 조작을 재현한다.

### 6.1 투표 상태 영속화 (WAL)

§4.5의 lock과 "한 라운드에 한 digest만 투표" 규칙은 **재시작 후에도 유지되어야** 한다.
메모리에만 두면 PREPARE/COMMIT을 보낸 뒤 크래시 → 재시작한 노드가 같은 높이의 다른 digest에
투표할 수 있고, 이런 노드가 `f` 를 넘으면 §4.7의 쿼럼 겹침 논증이 무너진다.
BP 롤링 재시작이 일상인 운영 환경에서 이는 실제 위험이다.

**기록 내용과 순서**
- PREPARE·COMMIT·ROUND_CHANGE를 **보내기 전에** `(height, round, type, digest)` 를 기록하고
  **fsync 완료 후** 전송한다. 순서가 바뀌면 영속화의 의미가 없다.
- PREPARED 진입 시 lock `(preparedRound, preparedDigest, PREPARE 쿼럼 증명, 블록 RLP)` 를 기록한다.
  재시작 후 ROUND_CHANGE의 근거와 재제안 블록을 복원하는 데 필요하다.
- 높이가 커밋되면 그 이전 기록을 정리한다 (파일 크기는 높이 1~2개 분량으로 유지).

**저장 위치:** chaindata와 분리된 추가 기록 파일(`<datadir>/metabft/wal`).
chaindata 쓰기는 배치되어 fsync 시점을 보장할 수 없다.

**재시작 규칙**
- WAL의 마지막 서명보다 이전이거나 같은 `(height, round)` 에서 **다른 digest에 서명하지 않는다.**
  복원한 lock은 §4.5 규칙대로 따른다.
- **WAL이 없거나 손상된 경우**(디스크 교체, 스냅샷 복원, 재설치)에는 관찰 모드로 시작해,
  체인이 로컬 head보다 한 높이 이상 커밋되는 것을 확인한 뒤 투표에 참여한다.

**운영 규칙**
- **같은 노드키를 두 서버에서 동시에 운영하지 않는다** (active-active, 상시 기동 대기 서버 금지).
  서버가 둘이면 WAL도 둘이라 이중 서명을 막을 수 없다.
- 장애 조치(failover)는 "원 서버 완전 중지 확인 → WAL 이전 → 대기 서버 기동" 순서로 한다.

**성능:** 블록당 fsync 3회 안팎(PREPARE, lock, COMMIT). 100ms 블록이면 초당 약 30회로 SSD에서는
부담이 작으나, §11.3 지연 측정에 포함한다.

---

## 7. 통합 지점 (정확한 위치)

### 7.1 P2P: `meta` 확장이 아닌 **별도 서브프로토콜**

합의 메시지를 `meta` 에 넣으면 `meta` 버전 범프가 필요하고
(`docs/meta69-blob-replay-design.md` 참고), 합의에 참여하지 않는 비-validator 노드까지
메시지 코드 길이 협상에 얽힌다. PBFT를 켜지 않은 네트워크의 `meta` 프로토콜도
그대로 두는 편이 안전하다.

→ **`metabft/1` 을 독립 devp2p 서브프로토콜로 추가**한다.
- `p2p.Protocol{Name: "metabft", Version: 1, Length: 8}`
- validator만 이 capability를 광고한다 (비-validator는 협상 자체를 안 함).
- 등록: `eth/backend.go` 의 `Protocols()` 에서 `metabft.MakeProtocols(...)` 를 append.
- 메시지 코드: `0x00 PreprepareMsg`, `0x01 PrepareMsg`, `0x02 CommitMsg`,
  `0x03 RoundChangeMsg`, `0x04 SyncRequestMsg`, `0x05 SyncReplyMsg`.
- 리플레이 방지: `BftMessage` 자체가 `(Height, Round, ChainID, Signature)` 를
  담으므로 meta/69의 nonce+timestamp 방식(`eth/protocols/eth/metadium_replay.go`)은 불필요.
- validator 간 **full mesh**를 전제한다. 거버넌스 노드는 이미
  `metadium/admin.go:1359 addPeer` 로 상호 연결되므로 추가 작업이 적다.

**중복 억제와 이중 서명 탐지**

처리 순서: **① 메시지 서명 검증 → ② 발신자가 해당 높이 validator인지 확인 → ③ 캐시 조회.**
서명 검증을 캐시보다 먼저 해야, 다른 노드를 발신자로 적은 위조 메시지가 캐시를 선점해
진짜 메시지를 "중복"으로 버리게 하거나 정직한 노드를 이중 서명자로 오인하게 만들 수 없다.

| 캐시 키 | 캐시 값 |
|---|---|
| `(sender, height, round, type)` | 처음 받은 메시지의 `digest` + 서명된 원본 |

- 같은 키, **같은 digest** → 중복, 버린다.
- 같은 키, **다른 digest** → 버리지 않고 **두 서명 메시지를 증거로 디스크에 저장 + 알람**.
  합의 집계에는 **처음 받은 메시지만** 계속 쓴다.
- 네 가지 메시지 모두 적용한다. ROUND_CHANGE도 한 노드가 한 라운드에 보내는 내용이 하나로
  정해지므로, 내용이 다르면 이중 서명이다. go-ethereum 서명은 결정적이라 같은 digest의
  COMMIT seal이 다르게 나오는 정상 경우는 없다.
- 증거는 서명과 ChainID가 붙어 있어 **제3자가 독립 검증 가능**하다. `metabft_getEvidence` 로 조회하며,
  §12 "거버넌스 수동 제명"의 판단 근거가 된다. 다른 노드로의 증거 전파는 후속 과제.
- 캐시는 해당 높이 커밋 시 정리한다 (크기 제한). 증거는 이중 서명 발생 시에만 생긴다.

### 7.2 `consensus.Engine` 교체

`eth/ethconfig/config.go:175` `CreateConsensusEngine` 의 분기(`:187`)에, chain config에
`BftBlock` 이 설정되어 있으면 PBFT 래퍼 엔진을 만드는 경로를 추가한다 (§8.1: 합의 방식은
제네시스가 결정).

주의: B안에서는 한 체인 안에 **PoA 부트스트랩 구간(`< BftBlock`)과 PBFT 구간이
공존**하므로, 하나의 바이너리가 두 구간을 모두 검증해야 한다 (신규 노드의 전체 동기화).
따라서 엔진은 래퍼로 만든다.

```go
// VerifyHeader 등 모든 메서드에서
if !chain.Config().IsBft(header.Number) {
    // 기존 ethash(PoA) 경로 + pre-fork 추가 규칙: CommitSeals == nil && BftRound == 0 (§5.3)
    return e.legacy.VerifyHeader(chain, header)
}
// BFT 경로 (§5.3 post-fork 규칙)
```

### 7.3 `miner/worker.go` — 가장 큰 수술

현재: `commitEx` → `Seal()` → **`sealedBlock = <-resultCh` 동기 수신**
(`miner/worker.go:1866`) → `WriteBlockAndSetHead` (`:1912`).

PBFT에서는 Seal이 수 초에서 수 라운드 걸릴 수 있고, **결과가 나 아닌 다른 노드의
제안일 수도** 있다. 따라서:

**1) 제안자 게이트 교체** (`miner/worker.go:1666-1681`):
```go
if w.chain.Config().IsBft(height) {
    if !metaminer.IsBftProposer(height) { w.refreshPending(true); return }
} else if IsBokbunja { ... AcquireMiningToken ... }
else { ... IsMiner() ... }
```
   `IsBftProposer` 는 `metadium/miner` 의 함수 포인터 패턴
   (`metadium/miner/miner.go:44`)을 그대로 따라 추가한다 — 기존 구조와 일관.
   lock이 있는 라운드에서는 worker가 새 블록을 만들지 않고 BFT core가 lock 블록을 재제안한다.

**2) 블록 기록 책임 이전**: post-fork에서는 worker가 `WriteBlockAndSetHead` 를
   호출하지 않는다. `Seal()` 은 블록을 BFT core에 넘기고 즉시 반환하며,
   **커밋 완료 시 BFT backend가** seal을 붙여 `WriteBlockAndSetHead` 를 수행한다.
   worker의 `resultCh` 동기 대기는 `IsBft` 분기로 우회한다.

**3) `LogBlock` / `ReleaseMiningToken` 제거** (`miner/worker.go:1901-1910`):
   post-fork에서는 etcd work 로깅이 불필요하다. `IsBft` 분기로 건너뛴다.
   (2026-05-26 `failed to log latest block` 포스트모템의 원인 경로가 통째로 사라진다.)

**4) 비-제안자도 블록을 실행해야 한다.** 현재 비-제안자는 `refreshPending` 후
   즉시 반환한다. PBFT에서는 PRE-PREPARE 수신 시 블록을 실행·검증해야 하므로 (§4.8),
   그 경로는 worker가 아니라 BFT backend가 `core.BlockChain` 의 검증 API를
   직접 호출한다 (`ValidateBody` + `Process` + `ValidateState`). worker는 건드리지 않는다.

**5) 타임스탬프**: post-fork에서 제안자는 `max(parent.Time, now)` 를 쓴다 (`timeIt` 의
   블록 간격 보정은 PBFT 구간에서 쓰지 않는다 — §4.5 타임스탬프 규칙과 충돌할 수 있다).

### 7.4 Finality

- `metadium/admin.go:645 getFinalizedBlockNumber` 를 fork 분기:
  ```go
  if chainConfig.IsBft(headNum) { return new(big.Int).Set(headNum) }  // committed = final
  ```
- `core/blockchain_reader.go:83 metaFinalHeader` 는 그대로 동작한다
  (이미 이 함수를 경유한다).
- 이로써 `CurrentSafeBlock`/`CurrentFinalBlock`, blob limbo 정리
  (`core/txpool/blobpool`), `eth_getBlockByNumber("finalized")` 가 모두 정확해진다.

### 7.5 보상 (rewards)

**현행 동작** (§2): 등록된 `verifyRewards` (`metadium/admin.go:1618`)는 `return nil` 뿐이고 호출처도 없다.
블록 처리 시 `accumulateRewards` (`consensus/ethash/consensus.go:741`)가 보상을 재계산해 state에
반영하고, **`header.Rewards` 와 `header.Coinbase` 를 재계산 값으로 덮어쓴다** (`:754`).
따라서 제안자가 넣은 값은 비교되지 않고, 잘못된 보상은 state root 불일치로만 잡힌다.

**PBFT 구간 변경**
- `calculateRewards` (진입점 `metadium/admin.go:1610` → 구현 `:1561`)는
  `(num, blockReward, fees)` 의 결정적 함수이므로 계산 로직은 **변경 불필요**.
- validator는 PREPARE 전에 재계산한 `Rewards`·`Coinbase` 를 **헤더 값과 비교하고, 다르면 거부**한다.
  이 필드들은 `BlockHash`(=digest)에 포함되므로 (§5.2), 덮어쓰면 노드마다 해시가 달라질 수 있다.
- 비교 검증은 post-fork에만 적용한다. pre-fork 동작은 바꾸지 않는다.
- **개선점:** 현재는 잘못된 보상이 이미 블록이 전파된 뒤 state root 불일치로만 잡힌다.
  PBFT에서는 PREPARE 전에 걸러지므로 **블록 자체가 커밋되지 않는다**
  (block-18 사건류의 노출 창이 사라진다 — `docs/block18-reward-race-known-issue.md`).

### 7.6 etcd

| etcd 용도 | PBFT 이후 |
|---|---|
| mining token (`metaTokenKey`) | **제거** — 합의가 대체 |
| work log (`metaWorkKey`) | **제거** |
| leader 선출 | **제거** |
| 클러스터 멤버십 관리 RPC (`EtcdAddMember` 등) | 유지 (운영 도구) |
| `admin.update()` 거버넌스 폴링 | 유지 (etcd 무관) |

fork 이후 `acquireMiningToken`/`releaseMiningToken`/`hasMiningToken` 호출 경로는
`IsBft` 분기로 사용되지 않는다. **etcd 서버 자체의 제거는 후속 릴리스**로 미룬다
(운영 RPC 의존성과 롤백 경로 보존).

### 7.7 동기화 (sync)

- **신규/재동기 노드**: 헤더에 붙은 commit seal이 자체 증명이므로, 일반
  헤더/바디 동기화만으로 검증 가능하다. 별도 합의 재생이 불필요하다.
- `acceptUnverifiableBlock` (`metadium/admin.go:1708`)의 snap-sync 우회 경로는
  post-fork 높이에서 **금지**해야 한다. validator set 조회 실패 시에는 블록을 받지 않고 대기한다.
  (여기서 타협하면 BFT 보장이 무의미해진다.)
- **뒤처진 validator**: `SyncRequestMsg`/`SyncReplyMsg` 로 현재 (height, round)와
  최신 커밋 블록을 요청하여 따라잡는다.

---

## 8. 설정 파라미터

### 8.1 합의 방식은 제네시스가 결정한다

합의 방식은 모든 노드가 같아야 하므로 **CLI 플래그가 아니라 제네시스 chain config**
로 정한다. 노드마다 플래그를 다르게 주는 실수가 원천 차단된다.

```json
"config": {
    "chainId": 638200421,
    ...
    "camelliaBlock": 0,
    "bftBlock": 17280,
    "bft": { "emptyBlockInterval": 5, "baseTimeout": 2, "maxBackoffExp": 5, "timeDrift": 2 }
}
```

- `bftBlock` 이 없으면(nil) 영구 PoA — 기존 네트워크와 동작 동일.
- `bftBlock` 은 **0보다 커야 한다** (B안: 부트스트랩 구간 필수, §9.2). 0이면 `init` 거부.
- **`bftBlock` 이 있으면 `camelliaBlock <= bftBlock` 이어야 한다** (§5.1). 위반 시 `init` 거부.
- `bft.*` 는 **모든 validator의 타이머·검증에 들어가는 값이므로 제네시스에 고정**한다 (§4.5).
  노드별로 달라도 되는 운영 값만 플래그로 둔다.
- PBFT 구간에서는 `--metadium.block.emptyinterval` 대신 `bft.emptyBlockInterval` 을 쓴다.
  둘이 다르게 주어지면 경고하고 제네시스 값을 따른다.

`params/config.go` 에 `BftBlock *big.Int`, `Bft *BftConfig` 추가 + `IsBft(num)` +
배너 출력(`config.go:551` 패턴) + `checkCompatible` 목록(`config.go:790`) 등록.

### 8.2 CLI 플래그

```
--metadium.block.idleseal 100            # 기존 플래그 유지 (제안자 로컬 동작)
--bft.requestsyncinterval <초, 기본 10>   # 노드별 운영 값
```

`--consensusmethod` 는 PBFT 선택 용도로 쓰지 않는다. `4`(PBFT)가 주어졌는데 제네시스에
`bftBlock` 이 없거나, 반대로 `bftBlock` 이 있는데 다른 값이 명시되면 기동 시 `Fatalf`
한다 (`cmd/utils/flags.go:2084` 검증부에 추가).

---

## 9. 네트워크 구성 절차 (B안: PoA 부트스트랩 → PBFT 전환)

### 9.1 왜 부트스트랩 구간이 필요한가

validator set은 거버넌스 컨트랙트에서 읽는다 (§4.2). 그런데 현재 네트워크 구성 절차
(`tests/private-net-poa/setup.sh`)는 **node1이 거버넌스 없이 혼자 블록을 만들면서
거버넌스 컨트랙트를 나중에 배포**한다. 블록 0부터 PBFT를 쓰면 validator set이 없어
첫 블록을 합의할 수 없다.

→ **B안**: `BftBlock` 이전은 기존 PoA로 부트스트랩하고, 거버넌스 배포와 노드 등록을
마친 뒤 `BftBlock` 에서 PBFT로 전환한다. 로컬 프라이빗 네트워크(`setup.sh`)에서
Camellia를 블록 100에 활성화하는 것과 같은 구조라 기존 코드 경로를 재사용한다.

(검토했던 A안 — 제네시스에 초기 validator 목록을 넣어 블록 0부터 PBFT — 은 제네시스
형식을 새로 정의해야 하고 거버넌스 컨트랙트와 목록이 이중화되어 채택하지 않았다.)

### 9.2 구성 순서

| 단계 | 합의 | 작업 |
|---|---|---|
| 0. 제네시스 작성 | — | 고객사 체인 ID(§9.5), `bftBlock`, `bft.*` 값 확정. **이후 변경 불가** |
| 1. 부트스트랩 시작 | PoA (node1 단독) | 기존 `setup.sh` Phase 1과 동일 |
| 2. 거버넌스 구성 | PoA | 거버넌스 배포(`blockCreationTime` 포함), validator 노드 등록 (**N >= 4, 운영 권장 N = 7**, §9.6), full mesh 확인, 전 노드 NTP 동기화 확인 |
| 3. 전환 준비 점검 | PoA | `metabft_readiness` RPC로 전 노드 확인 (§9.3) |
| 4. `BftBlock` 도달 | **PBFT** | 자동 전환. 첫 제안자 = `validators[(BftBlock + 0) % N]` |

- `bftBlock` 은 **2단계를 마치기에 충분히 크게** 잡는다. 부트스트랩 구간은 대부분 유휴
  상태라 블록이 빈 블록 간격(현행 프로파일 5초)마다 생기므로 `17280` ≈ 하루다 (거버넌스 배포
  트랜잭션이 몰리는 동안에는 더 빨리 진행된다). 부트스트랩이 빨리 끝나도 `BftBlock`
  까지는 PoA로 계속 간다.
- **부트스트랩 구간에는 BFT 보장이 없다.** 이 구간에는 거버넌스 구성 외의
  업무 트랜잭션을 올리지 않는 것을 운영 원칙으로 한다.
- **전환 블록의 부모는 PoA 블록**이므로 commit seal이 없다. 제안 검증에서 부모의
  seal을 요구하지 않도록 `IsBft(parent.Number)` 로 분기한다. 전환 블록의 `committedAt(n-1)` 은
  부모 블록을 import한 로컬 시각으로 대신한다 (§4.5).
- 전환 직후 몇 라운드는 round change가 날 수 있다 (노드 간 기동 시차). 정상이며
  수 라운드 내 수렴한다.

### 9.3 전환 조건과 실패 처리

`BftBlock` 의 부모 상태(`BftBlock-1`)에서 다음을 만족하지 않으면 **PoA로 계속 가지
않고 블록 생성을 멈춘다** (명확한 에러 로그와 함께). 조용히 PoA를 이어가면 운영자가
BFT로 동작한다고 착각하게 된다.

1. 거버넌스 컨트랙트가 배포되어 있다
2. 거버넌스 노드 수 `N >= 4`
3. 모든 validator가 `metabft/1` capability를 광고하고 있다 (노드 로컬 점검, 경고만)

멈췄을 때의 복구: 부트스트랩 구간에는 업무 데이터가 없으므로 **제네시스를 다시 만들어
재구성**하는 것이 원칙이다. 이 때문에 §9.2의 운영 원칙이 중요하다.

`metabft_readiness` RPC는 위 조건과 현재 높이, `BftBlock` 까지 남은 블록 수를 돌려준다.

#### 9.3.1 전환 이후의 `N >= 4` 유지 — 정지가 아니라 거부

§9.3의 게이트는 `BftBlock` 시점만 본다. 전환 뒤 거버넌스로 노드를 빼서 `N` 이 3이 되면
`f=0` 이 되어 쿼럼 논증이 무너진다.

**규칙: 실행 후 상태의 거버넌스 노드 수가 4 미만이 되는 블록은 무효다.**
- 제안자는 그런 결과를 내는 트랜잭션을 블록에서 뺀다 (후보 블록 실행 후 확인).
- validator는 그런 블록에 PREPARE하지 않는다 (§4.8 6번). 동기화 노드도 `VerifyHeader` 후
  상태 검증 단계에서 거부한다.
- 결과: `N` 을 3 이하로 줄이는 거버넌스 트랜잭션은 확정되지 않고, **체인은 계속 진행**한다.
  거부 시마다 에러 로그와 `metabft_status` 에 사유를 남긴다.

**정지를 택하지 않는 이유:** §9.3의 정지는 부트스트랩 구간이라 제네시스 재구성으로 복구할 수 있었다.
전환 이후에 정지하면 노드를 다시 추가하는 거버넌스 트랜잭션도 블록에 담을 수 없어 **복구 경로가 없다**
(하드포크나 체인 재구성뿐이고, 업무 데이터가 쌓인 체인이라 둘 다 쓸 수 없다).

**후속 과제:** 거버넌스 컨트랙트에서 `N >= 4` 를 직접 강제하는 것이 가장 깔끔하나,
컨트랙트 변경은 범위 밖이다 (§1.2).

### 9.4 롤백

- **`BftBlock` 이전**: 제네시스 재구성으로 되돌릴 수 있다.
- **`BftBlock` 이후**: 헤더 포맷이 다르므로 PoA로 되돌리려면 체인 되감기가 필요하다.
  → 고객사 운영 투입 전에 동일 구성으로 스테이징 네트워크를 먼저 전환해 본다.

### 9.5 체인 ID 할당 (고객사별)

**전제.** go-metadium은 Metadium 메인넷/테스트넷을 체인 ID가 아니라 **제네시스 해시**로
식별한다 (`core/genesis.go:411`, `eth/backend.go:112`). 따라서 프라이빗 네트워크의
체인 ID는 자유롭게 정할 수 있고, 역할은 두 가지다:
EIP-155 트랜잭션 서명 재사용 차단, 그리고 PBFT `commitDigest` 도메인 분리 (§4.6).
두 역할 모두 **네트워크마다 값이 달라야** 의미가 있다.

**번호 체계: `6382 CCCC E` (9자리)**

```
chainId = 638200000 + (고객사번호 * 10) + 환경코드

  6382  고정 접두어 (전화 키패드 M-E-T-A)
  CCCC  고객사 번호 0001–9999 (0000 = 사내용)
  E     환경 코드
```

| E | 환경 |
|---|---|
| 0 | 예약 (사용 안 함) |
| 1 | 운영 (production) |
| 2 | 스테이징 |
| 3 | 개발 / QA |
| 4 | DR / 재해복구 |
| 5–9 | 예비 (고객사별 추가 네트워크) |

예시: 고객사 42번 운영 = `638200421`, 스테이징 = `638200422`. 사내 개발 = `638200003`.

**이 체계를 고른 이유**
- **충돌 없음**: 2026-09-23 기준 chainid.network 레지스트리(2,764개 체인)에서
  `638200000–638299999` 구간에 등록된 체인이 없다.
- **규칙만 알면 역산 가능**: 체인 ID만 보고 고객사와 환경을 알 수 있어, 지갑 설정이나
  장애 대응 때 네트워크를 혼동하지 않는다.
- **호환 범위**: 최댓값 `638299999` 는 2^31−1 미만이라 JS(2^53), 32비트 정수를 쓰는
  도구, 하드웨어 지갑 모두에서 안전하다.
- **메인넷 서명 재사용 차단**: `11`/`12`/`1337` 과 겹치지 않는다.

**운영 규칙**
1. **할당 대장은 사내 비공개 문서로 관리한다.** 고객사 이름과 번호의 대응은 이 저장소
   (공개)에 넣지 않는다.
2. **번호는 재사용하지 않는다.** 계약이 끝난 고객사 번호도 폐기 처리만 한다.
3. **`--networkid` 는 체인 ID와 같게** 맞춘다.
4. **공개 등록은 선택.** 운영 네트워크를 chainlist(`ethereum-lists/chains`)에 등록하면
   외부와의 충돌을 확실히 막지만, 고객사 존재가 공개된다. 고객사 동의가 있을 때만
   등록하고, 그 외에는 네트워크 구성 시점에 레지스트리를 다시 조회해 충돌을 확인한다.
5. **`init` 단계 검증**: 체인 ID가 `11`, `12` 이거나 공개 레지스트리의 알려진 값이면
   경고한다. 특히 `metadium/scripts/genesis-template.json` 은 현재
   `"chainId": 11` (메인넷)을 기본값으로 갖고 있으므로, 템플릿을 자리표시자로 바꾸고
   값이 채워지지 않으면 `init` 을 거부한다.

### 9.6 validator 수와 가용성

**공식.** 장애 노드 `f` 대까지 버티려면 `N = 3f + 1`, 블록 확정에는 `2f + 1` 대(쿼럼)가
살아 있어야 한다. `f` 는 꺼진 노드와 악의적 노드의 **합**이다 (7대에서 1대가 꺼져 있으면
악의적 노드는 1대까지만 버틴다).

| N | 버티는 장애 f | 쿼럼 | 비고 |
|---|---|---|---|
| 3 이하 | 0 | — | BFT 의미 없음. 전환 거부 (§9.3), 전환 후 감소 거부 (§9.3.1) |
| **4** | 1 | 3 | 최소 구성 |
| 5, 6 | 1 | 4, 5 | 4대와 버티는 장애 수가 같고 쿼럼만 커진다 → **비권장** |
| **7** | 2 | 5 | 운영 권장 |
| **10** | 3 | 7 | |
| 13 | 4 | 9 | |

효율적인 구성은 **4, 7, 10, 13** 이다.

**현행(etcd/raft)과 비교**

| | raft (현행) | PBFT |
|---|---|---|
| 필요 노드 | `N = 2f + 1` (과반) | `N = 3f + 1` (2/3 초과) |
| 장애 1대 허용 | 3대 | 4대 |
| 장애 2대 허용 | 5대 | 7대 |
| 악의적 노드 | 방어 불가 | `f` 대까지 방어 |

노드별 가용률 99%, 장애가 서로 독립이라고 가정한 **연간 블록 생성 중단 기댓값**:

| 구성 | 연간 중단 기댓값 |
|---|---|
| raft 3대 | 약 157분 |
| PBFT 4대 | 약 311분 |
| raft 5대 | 약 5분 |
| PBFT 7대 | 약 18분 |
| PBFT 10대 | 약 1분 |

같은 규모에서 PBFT는 raft보다 가용성이 낮다. 가용성 일부를 내주고 "포크 불가"
안전성을 얻는 구조다.

**운영상 유의점**
1. **4대는 점검 여유가 없다.** 1대를 업그레이드 등으로 내리면 장애 허용치(`f=1`)를
   다 쓴 상태가 되어, 다른 1대만 문제가 생겨도 블록 생성이 멈춘다. 7대면 1대 점검 중
   1대 고장까지 버틴다. 롤링 재시작은 WAL(§6.1)이 있어야 안전하다.
2. **멈춰도 포크는 없다.** 쿼럼이 모자라면 생성만 멈추고, 노드가 복구되면 자동으로
   재개된다 (§11.2 시나리오 3).
3. **분할 시 양쪽 모두 멈출 수 있다.** 7대가 4:3으로 나뉘면 어느 쪽도 쿼럼 5를 채우지
   못한다 (§11.2 시나리오 6). 따라서 **한 장애 도메인(데이터센터·가용 영역)에 `f` 대를
   넘게 두지 않는다.** 7대(`f=2`)는 영역 3곳(3/2/2)으로는 3대 영역이 빠질 때 멈추므로
   **4곳 이상(예: 2/2/2/1)** 에 나눈다.
4. **RPC·풀노드는 합의 노드 수에 들어가지 않는다.** 블록 생성 가용성과 조회(RPC)
   가용성은 따로 설계한다.

**권장 구성**

| 환경 | validator 수 | 배치 |
|---|---|---|
| 개발 / QA | 4 | 단일 영역 가능 |
| 스테이징 | 7 | 운영과 동일 배치 (전환 리허설용, §9.4) |
| 운영 | 7 (고가용 요구 시 10) | 장애 도메인 4곳 이상, 도메인당 `f` 대 이하 |

---

## 10. 구현 단계

| 단계 | 내용 | 산출물 / 검증 |
|---|---|---|
| **P0** | 제네시스 `bftBlock`/`bft` 설정, `IsBft()`, `camelliaBlock <= bftBlock` 검사, 플래그 정합성 검사, 체인 ID `init` 검증, `genesis-template.json` 자리표시자화 | `params` 단위테스트, `init` 거부 케이스 테스트 |
| **P1** | 헤더 필드 추가(`headerRlp` 끝) + `BlockHash` 제외 규칙 + pre-fork seal 금지 + RLP 왕복 | `core/types` 왕복 테스트, pre/post-fork 해시 불변 테스트, **pre-fork 블록 + 임의 seal 거부 테스트** |
| **P2** | `consensus/metabft` validator set / proposer / 쿼럼 / 메시지 서명 / WAL / 증거 저장 | 순수 단위테스트 (체인 불필요), WAL 크래시 지점 주입 테스트 |
| **P3** | 상태기계 `core.go` + round change + 재제안 헤더 불변, backend·시계·WAL은 mock | **결정적 시뮬레이션**: N=4/7/10, 지연·유실·비잔틴·**크래시 재시작·타임스탬프 조작** 주입 |
| **P4** | `metabft/1` P2P 서브프로토콜 + 중복 억제·이중 서명 탐지 | 2노드 메시지 왕복, 위조 서명 거부, 이중 서명 증거 생성 |
| **P5** | 엔진/worker/finality/etcd/보상 비교 검증/`N>=4` 유효성 분기 통합 | 로컬 4노드 실제 블록 생성 |
| **P6** | 장애 주입 + 성능 + 부트스트랩→전환 리허설 (§9.2, 전환 실패 §9.3 포함) | 아래 테스트 계획 |

P3가 프로젝트의 무게중심이다. **상태기계를 네트워크 없이 결정적으로 테스트할 수
있게 설계하지 않으면 이후 단계에서 디버깅이 불가능해진다.** 일정은 P3 완료 후 다시 산정한다.

---

## 11. 테스트 계획

### 11.1 테스트넷 확장 (선행 작업)

`tests/private-net-poa` 를 **3노드 → 7노드**로 확장한다 (`f=2`).
- `docker-compose.yml` node4–node7 추가, 포트 8548–8551
- `setup.sh` 의 거버넌스 초기 노드 등록 확장
- 기존 `camellia-test.sh`, `blob-tx-e2e`, `mixed-tx-e2e` 가 그대로 통과하는지 먼저 확인
  (PBFT 작업 전 baseline 확보)

### 11.2 장애 주입 시나리오 (N=7, f=2)

| # | 시나리오 | 기대 결과 |
|---|---|---|
| 1 | validator 1개 정지 | 블록 생성 지속, 해당 노드 차례에 1 round change |
| 2 | validator 2개 정지 (= f) | 블록 생성 지속 (느려짐) |
| 3 | validator 3개 정지 (> f) | **블록 생성 정지**, 복구 시 자동 재개, 포크 없음 |
| 4 | 제안자가 서로 다른 두 블록을 두 그룹에 제안 (equivocation) | 커밋 안 됨, round change, 포크 없음, **증거 2건 저장·`metabft_getEvidence` 조회·알람** |
| 5 | 제안자가 잘못된 state root / 잘못된 Rewards·Coinbase 제안 | PREPARE 거부 → round change |
| 6 | 네트워크 분할 4:3 | 4 그룹만 진행(4 < Quorum=5 이므로 **양쪽 다 정지**), 치유 시 재개·포크 없음 |
| 7 | 시계 왜곡 (한 노드 +5분) | 블록 생성 유지 (해당 노드는 타임스탬프 범위 검사로 제안이 거부될 수 있음) |
| 8 | 거버넌스로 validator 추가/제거 | epoch 경계에서 무중단 전환 |
| 9 | 새 노드 snap sync 후 참여 | commit seal 검증 통과, 합의 참여 |
| 10 | commit seal 제거/위조 블록 주입 | import 거부 |
| 11 | **PREPARE 직후 / COMMIT 직후 강제 종료 → 재시작** | 같은 높이에서 다른 digest에 투표하지 않음, lock 복원 |
| 12 | **WAL 삭제 후 재시작** | 관찰 모드 → 한 높이 커밋 확인 후 참여 |
| 13 | **같은 노드키로 두 서버 기동** | 이중 서명 증거 생성·알람 |
| 14 | **제안자가 `Time` 을 과거(부모 이전) / 미래(+10s)로 설정** | `VerifyHeader`·범위 검사로 거부, 다음 높이 라운드 0 정상 |
| 15 | **거버넌스로 노드 제거해 N=3 시도** | 해당 트랜잭션 미확정, 체인 계속 진행, 거부 사유 기록 |
| 16 | **round change 후 재제안 블록 커밋** | 원 제안자 `MinerNodeSig` 유지, `BftRound`=커밋 라운드, import 검증 통과 |
| 17 | **pre-fork 블록에 임의 `CommitSeals` 부착 주입** | import 거부 |

> 시나리오 6은 직관과 다르므로 명시: `Quorum = 2f+1 = 5` 이므로 4:3 분할에서는
> **어느 쪽도 진행하지 못한다.** 이것이 CFT(raft, 과반 4로 진행)와의 결정적 차이이며,
> "안전성을 위해 가용성을 포기"하는 PBFT의 의도된 동작이다. 운영팀이 이를
> 이해하고 수용해야 한다.

### 11.3 성능 측정

- **트랜잭션 확정 지연** (`eth_sendTransaction` → receipt): 현행 PoA `idleseal=100` 기준
  p50 118ms / p99 130ms (`docs/enterprise-block-timing.md`). PBFT는 3-phase 왕복,
  validator 블록 실행, **WAL fsync**가 더해진다. 목표는 N=7 LAN에서 **p99 < 300ms** 로 두고 실측으로 확정한다.
- 유휴 빈 블록 간격 (목표: `EmptyBlockInterval` ± 10%, **유휴 중 round change 0건**)
- **부하 중 round change 수** (초당 여러 블록 구간에서 0건 목표 — rev.3 타이머의 결함 재발 확인)
- 고정 간격 프로파일(`blockCreationTime = 2000`, idleseal 없음 — 공개망과 같은 설정) 비교: 블록 간격 2.0s 유지 여부
- 메시지 복잡도: 라운드당 `O(N²)` — N=7이면 블록당 약 98 메시지. N이 30을 넘으면
  대역폭 검토 필요 (`O(N²)` 이므로 900 메시지).
- TPS 회귀: `scripts/rpc-test-full.sh`, `mixed-tx-e2e` 로 Camellia 대비 비교.

---

## 12. 리스크와 미해결 과제

| 리스크 | 영향 | 완화 |
|---|---|---|
| **가용성 하락** | raft는 과반(N/2+1)으로 진행, PBFT는 2f+1 필요. N=7이면 4 vs 5. 같은 규모에서 연간 중단 기댓값이 raft보다 크다 (§9.6) | 운영 7대 이상, 장애 도메인 4곳 이상 분산, 도메인당 f대 이하. 운영팀 합의 필요 |
| **4대 구성의 점검 여유 없음** | 1대 점검 중 1대 추가 장애 시 블록 생성 정지 | 4대는 개발/QA 한정. 운영·스테이징은 7대 (§9.6) |
| **재시작 시 투표 상태 소실** | 재시작 노드가 다른 digest에 투표 → 쿼럼 논증 붕괴 | WAL (§6.1), 시나리오 11·12 |
| **노드키 이중 운영** | 두 서버가 같은 키로 서명 → WAL로도 이중 서명 방지 불가 | active-active 금지, failover 절차 (§6.1), 증거 탐지 (§7.1) |
| **타임스탬프 조작** | 제안자가 `Time` 으로 타이머·EVM 시각 조작 | 타이머 로컬 시계화 (§4.5), `Time >= parent.Time` + PRE-PREPARE 범위 검사 |
| **`N²` 메시지 복잡도** | validator 수 확장 제약 | 현 규모에서는 문제없음. 30+ 시 서명 집계(BLS) 검토 |
| **슬래싱 부재** | 이중 서명을 **탐지·증거 보존**할 수 있으나 처벌 불가 | 증거(§7.1) 기반 거버넌스 수동 제명. 온체인 처벌은 범위 밖 |
| **헤더 포맷 변경** | 해당 프라이빗 네트워크의 익스플로러/인덱서가 영향 (기존 Mainnet/Testnet은 무관) | `internal/ethapi/api.go:1383` 에 필드 노출 추가, 고객사 도구에 사전 공유 |
| **전환 후 롤백 불가** | `BftBlock` 이후에는 PoA로 되돌릴 수 없음 | 전환 전에는 제네시스 재구성 가능 (§9.4). 스테이징에서 동일 구성 선행 전환 |
| **부트스트랩 구간 BFT 미보장** | `BftBlock` 이전 블록은 PoA 단독 생성 | 이 구간에는 거버넌스 구성 외 업무 트랜잭션 금지 (§9.2) |
| **체인 ID 충돌·오설정** | 메인넷(`11`) 기본값 템플릿 사용 시 서명 재사용 위험 | `6382CCCCE` 체계 (§9.5), `init` 검증, 템플릿 자리표시자화 |
| **blob sidecar 가용성** | 커밋 시점에 sidecar가 없는 validator | meta/69 `GetBlobSidecarsMsg` 로 PREPARE 전 확보. 미확보 시 PREPARE 보류 |
| **`snap sync` + BFT 검증 충돌** | `acceptUnverifiableBlock` 우회로가 BFT 보장을 무력화 | post-fork 높이에서 해당 경로 차단 (§7.7) |
| **`BftBlock` 도달 시 N < 4** | 블록 생성 정지 (§9.3) | `metabft_readiness` RPC로 전환 전 점검. `bftBlock` 을 넉넉히 설정 |
| **전환 후 N < 4 시도** | 쿼럼 논증 붕괴 | 해당 블록 무효 처리 — 정지가 아니라 거부 (§9.3.1) |
| **일정 압박** | 짧은 일정에 전면 도입을 무리하면 safety 항목(§4.5·§5.2·§6.1·§7.1)이 검증 없이 들어간다 | 일정이 부족하면 §13 대안으로 범위를 줄이고, 보장하는 것과 보장하지 않는 것을 명시 |

---

## 13. 대안 — 더 가벼운 선택지

PBFT 전면 도입이 과하거나 일정상 불가능할 때의 중간 단계. **병행이 아니라 택일**이다.
rev.3의 비용 비율(25%/10%)은 검증되지 않은 어림값이라 삭제하고, 필요 작업을 본문 절 기준으로 적는다.

### 대안 1: commit seal만 추가 (합의는 현행 유지)
etcd token으로 제안자를 정하되, 블록 전파 후 validator들이 commit seal을 모아
**후속 블록의 헤더에 이전 블록의 seal을 실어 나른다**.
- 얻는 것: 1블록 지연된 **검증 가능한 finality 증명** (현재의 휴리스틱 대체)
- 못 얻는 것: 비잔틴 제안자 방어 (잘못된 블록이 일단 전파됨)
- **필요 작업**: 헤더 필드와 해시 규칙(§5 전체), seal 수집 P2P(§7.1 일부), 서명 규칙(§4.6),
  **"같은 높이에 두 블록 서명 금지" 규칙과 WAL(§6.1)**, **seal 붙은 블록 아래로 reorg 금지(§5.4)**.
  마지막 두 가지를 빼면 증명처럼 보이지만 실제로는 뒤집힐 수 있는 증적이 된다.
  → 짧은 일정 안에 검증까지 마치기는 어렵다.

### 대안 2: 온체인 서명 기록 (합의·헤더 변경 없음) — rev.4 추가
각 validator가 주기적으로 `(높이, 블록 해시)` 에 서명해 전용 컨트랙트에 트랜잭션으로 기록한다.
2f+1 서명이 모인 높이가 증적이 된다.
- 얻는 것: 제3자가 검증 가능한 다자 서명 증적. 헤더·P2P·합의 코드를 바꾸지 않아 기존 체인 영향 없음
- 못 얻는 것: 실시간 비잔틴 방어. reorg가 없다는 보장은 현행 etcd/raft에 의존한다
- 차이: 한계를 **숨기지 않고 증적과 함께 명시**할 수 있다. 짧은 일정에서 가장 현실적인 증적 수단

### 대안 3: etcd 유지 + equivocation 탐지
현행 구조를 두고, validator가 서로의 블록 서명을 감시하여 동일 높이 이중 서명을
탐지·알람한다.
- 얻는 것: 사후 탐지, 운영 가시성
- 못 얻는 것: 실시간 방어, finality 보장

### 판단 기준
- 네트워크가 **신뢰된 컨소시엄**(모든 validator를 한 조직/계약이 통제)이면
  raft(CFT)로 충분하며 대안 3으로 충분하다.
- **상호 불신 주체**가 validator를 운영하거나, 규제·감사 요건상 "포크 불가능"을
  증명해야 하면 PBFT 전면 도입이 정당화된다.

---

## 14. 요약

- 현재 `ConsensusPBFT` 는 상수와 CLI 문구만 있는 **빈 껍데기**다 (참조 2곳).
- 구현은 **IBFT 2.0 계열**로, 신규 패키지 `consensus/metabft` + 독립 서브프로토콜
  `metabft/1` + 헤더 commit seal 필드로 구성된다.
- 적용 대상은 **신규 프라이빗 네트워크**이며, 제네시스의 `bftBlock` 으로 PBFT를 선택한다.
  `BftBlock` 이전은 PoA로 부트스트랩(거버넌스 배포·노드 등록)하고 이후 PBFT로 전환한다 (B안).
- 체인 ID는 고객사·환경별로 `6382 CCCC E` 체계로 할당한다 (§9.5).
- safety의 핵심 전제: **digest = 블록 해시 전체** (§5.2), **투표 상태 WAL** (§6.1),
  **타이머는 로컬 시계** (§4.5), **전환 후에도 N >= 4** (§9.3.1).
- 가장 큰 수술은 `miner/worker.go` 의 **동기 봉인 → 비동기 커밋** 전환이다.
- 착수 전 반드시 선행: **테스트넷 7노드 확장**, **고객사 네트워크의 validator 수 N>=4 (운영 권장 7, §9.6) 확보**,
  **4:3 분할 시 정지한다는 가용성 트레이드오프에 대한 운영팀 합의**.

---

## 15. 개정 이력

| 판 | 내용 |
|---|---|
| rev.1 | 초안 (기존 체인 하드포크 전제) |
| rev.2 | 적용 대상을 신규 프라이빗 네트워크로 변경, B안(PoA 부트스트랩 → 전환), 체인 ID 체계, 블록 타이밍 |
| rev.3 | validator 수와 가용성 (§9.6) |
| rev.4 | 리뷰 1~8 반영 (아래) |

**rev.4 리뷰 반영 내역**

| 리뷰 | 지적 | 반영 |
|---|---|---|
| 1 | deadline이 제안자가 고르는 초 단위 `parent.Time` 에 의존 → 부하 시 round change 폭주, 타임스탬프 조작 | 타이머를 로컬 단조 시계 기준으로 변경, `Time >= parent.Time` + PRE-PREPARE 범위 검사, 초 단위 유지 (§4.5). 엄격한 `>` 는 100ms 프로파일과 충돌해 채택하지 않음 |
| 2 | PREPARED lock 영속화 부재 → 재시작 후 이중 투표 | WAL 추가, 재시작·관찰 모드 규칙, 노드키 이중 운영 금지 (§6.1) |
| 3 | 중복 억제 캐시가 이중 서명을 조용히 버림 → §12 완화책과 모순 | digest를 값으로 보관, 다른 digest는 증거 저장·알람. 서명 검증을 캐시보다 먼저 (§7.1) |
| 4 | pre-fork 블록에 seal을 붙여도 해시 불변 | pre-fork `CommitSeals == nil && BftRound == 0` 강제, 필드는 `headerRlp` 끝, `IsBft ⇒ IsCamellia` (§5.1–5.3) |
| 5 | 전환 후 N < 4 규칙 부재 | 실행 후 N < 4 블록 무효 — 리뷰 제안(정지) 대신 **거부** 채택, 복구 경로가 없기 때문 (§9.3.1) |
| 6 | `MinerNodeSig` 검증과 재제안 규칙 모순, `BftRound` 정의 모호 | `BftRound` = 커밋 라운드, 재제안 헤더 불변, `MinerNodeSig` 는 validator 소속만 검증 (§4.5, §5.3) |
| 7 | 코드 인용 라인 불일치 | §2 표와 본문 정정. `etcdutil.go:989` 는 `acquireTokenSync` 선언이 맞아 유지 (CAS 1024 병기) |
| 8 | 짧은 일정에 전면 도입은 불가, 대안 비용 재검토 필요 | §13 비용 비율 삭제·필요 작업 명시, 대안 2(온체인 서명 기록) 추가, §12 일정 압박 리스크 |
| 추가 | 리뷰 대조 중 발견: `SealHash` 를 digest로 쓰면 같은 digest에 여러 블록 해시 대응, `verifyRewards` 빈 함수·보상 필드 덮어쓰기 | digest = `BlockHash` (§4.4, §4.6, §5.2), 보상 비교 검증 (§7.5, §4.8) |
