# go-metadium 시스템 분석서

## 1. 시스템 개요

### 1.1 프로젝트 정보
- **이름**: Go Metadium (gmet)
- **기반**: go-ethereum fork
- **언어**: Go 1.21
- **설명**: Metadium 블록체인 네트워크의 공식 Golang 클라이언트 구현체

### 1.2 핵심 특징
- **합의 알고리즘**: Clique (Proof-of-Authority 기반)
- **거버넌스**: 스마트 컨트랙트 기반 분산 거버넌스
- **수수료 위임**: Fee Delegation 트랜잭션 타입 지원
- **데이터 저장소**: RocksDB (Linux) / LevelDB (기타 OS)
- **노드 관리**: etcd 기반 분산 노드 코디네이션

### 1.3 네트워크 정보
| 네트워크 | Chain ID | 기본 포트 |
|----------|----------|-----------|
| Mainnet  | 11       | HTTP: 8588, P2P: 8589, WS: 8598 |
| Testnet  | -        | 동일       |

---

## 2. 아키텍처 개요

### 2.1 전체 구조
```
go-metadium/
+-- 모듈형 모놀리식 아키텍처 (Modular Monolith)
+-- 인터페이스 기반 설계 (consensus/ethdb 등)
+-- 계층화된 모듈 구조
```

### 2.2 계층 구조
```
+------------------+
|   CLI (cmd/)     |  <- gmet, bootnode, abigen 등
+------------------+
|   Node (node/)   |  <- 노드 라이프사이클 관리
+------------------+
|   ETH Stack      |  <- eth/, les/ (Light Ethereum Subprotocol)
+------------------+
|   Core (core/)   |  <- 블록체인 코어 로직
+------------------+
|   Consensus      |  <- clique, ethash (인터페이스 기반)
+------------------+
|   P2P (p2p/)     |  <- devp2p 프로토콜
+------------------+
|   Storage        |  <- ethdb (RocksDB/LevelDB)
+------------------+
```

---

## 3. 핵심 컴포넌트

### 3.1 모듈 매트릭스

| 모듈 | 위치 | 역할 | 주요 의존성 |
|------|------|------|-------------|
| **cmd/gmet** | `cmd/gmet` | 메인 CLI 진입점 | eth, node, metadium |
| **core** | `core/` | 블록체인 코어 로직, 상태 관리, 트랜잭션 풀 | consensus, ethdb, state |
| **consensus** | `consensus/` | 합의 알고리즘 인터페이스 및 구현체 | core, params |
| **eth** | `eth/` | 이더리움 프로토콜 구현 | core, p2p, rpc |
| **les** | `les/` | Light Ethereum Subprotocol | eth, core |
| **p2p** | `p2p/` | P2P 네트워킹, 노드 발견 | crypto, enr |
| **rpc** | `rpc/` | JSON-RPC 서버/클라이언트 | - |
| **metadium** | `metadium/` | Metadium 특화 기능 (거버넌스, etcd) | ethclient, etcd |
| **ethdb** | `ethdb/` | 데이터베이스 추상화 계층 | RocksDB, LevelDB |
| **trie** | `trie/` | Merkle Patricia Trie 구현 | ethdb |
| **accounts** | `accounts/` | 계정 관리, 키스토어, 지갑 | crypto |
| **crypto** | `crypto/` | 암호화 유틸리티 | secp256k1 |
| **params** | `params/` | 체 설정, 네트워크 파라미터 | - |
| **miner** | `miner/` | 블록 생성, 마이닝 | consensus, core |
| **graphql** | `graphql/` | GraphQL API | eth |

### 3.2 metadium 특화 모듈 상세

#### metadium/admin.go
- **역할**: Metadium 거버넌스 관리
- **주요 기능**:
  - 노드/멤버 관리 (metaNode, metaMember)
  - 거버넌스 컨트랙트 연동 (Registry, Gov, Staking, EnvStorage)
  - TRS(Transaction Restriction Service) 관리
  - 블록 생성 파라미터 캐싱

#### metadium/etcdutil.go
- **역할**: etcd 기반 노드 코디네이션
- **주요 기능**:
  - 분산 노드 멤버십 관리
  - etcd 클러스터 자동 구성
  - 노드 간 상태 동기화

#### metadium/miner/miner.go
- **역할**: Metadium 전용 마이너
- **특징**: 거버넌스 기반 블록 생성자 선정

#### metadium/metclient/
- **역할**: Metadium 컨트랙트 클라이언트
- **구성**:
  - `util.go`: 컨트랙트 호출 유틸리티
  - `tx_params.go`: 트랜잭션 파라미터 처리

---

## 4. 기술 스택

### 4.1 코어 의존성 (go.mod)

#### 블록체인/암호화
| 패키지 | 버전 | 용도 |
|--------|------|------|
| `github.com/btcsuite/btcd/btcec/v2` | v2.2.0 | 타원곡선 암호화 |
| `github.com/consensys/gnark-crypto` | v0.4.1 | 영지식 증명 |
| `golang.org/x/crypto` | - | 암호화 프리미티브 |

#### 데이터 저장소
| 패키지 | 버전 | 용도 |
|--------|------|------|
| `github.com/syndtr/goleveldb` | v1.0.1 | LevelDB 바인딩 |
| `github.com/VictoriaMetrics/fastcache` | v1.6.0 | 인메모리 캐시 |
| `go.etcd.io/etcd` | v3.5.2 | 분산 코디네이션 |

#### 네트워킹
| 패키지 | 버전 | 용도 |
|--------|------|------|
| `github.com/gorilla/websocket` | v1.4.2 | WebSocket |
| `github.com/graph-gophers/graphql-go` | v1.3.0 | GraphQL |

#### 클라우드/인프라
| 패키지 | 버전 | 용도 |
|--------|------|------|
| `github.com/aws/aws-sdk-go-v2` | v1.2.0 | AWS 연동 |
| `github.com/Azure/azure-sdk-for-go` | v0.3.0 | Azure 연동 |
| `github.com/cloudflare/cloudflare-go` | v0.14.0 | Cloudflare DNS |

#### VRF (Verifiable Random Function)
| 패키지 | 버전 | 용도 |
|--------|------|------|
| `github.com/yoseplee/vrf` | v0.0.0 | VRF 구현 |

### 4.2 빌드 도구
- **빌드 시스템**: `build/ci.go` (Go 기반 커스텀 빌드)
- **코드 생성**: `gencodec`, `stringer`, `protoc-gen-go`
- **린터**: golangci-lint (`.golangci.yml`)

---

## 5. 메모리/데이터 저장

### 5.1 데이터베이스 계층 (ethdb/)

```
ethdb/
+-- database.go       # Database 인터페이스
+-- leveldb/          # LevelDB 구현
+-- rocksdb/          # RocksDB 구현 (Linux 전용)
+-- memorydb/         # 인메모리 DB (테스트용)
+-- remotedb/         # 원격 DB (미사용)
```

#### Database 인터페이스
```go
type Database interface {
    Has(key []byte) (bool, error)
    Get(key []byte) ([]byte, error)
    Put(key []byte, value []byte) error
    Delete(key []byte) error
    NewBatch() Batch
    NewIterator(prefix []byte, start []byte) Iterator
    Stat(property string) (string, error)
    Compact(start []byte, limit []byte) error
}
```

### 5.2 상태 저장 (trie/)
- **구조**: Merkle Patricia Trie
- **주요 파일**:
  - `trie.go`: 핵심 Trie 구현
  - `secure_trie.go`: 해시된 키 사용
  - `stacktrie.go`: 스택 기반 Trie (빠른 해시 계산)
  - `database.go`: Trie 노드 영속화

### 5.3 etcd 활용 (metadium/)
- **목적**: 노드 멤버십 및 거버넌스 상태 동기화
- **구성**:
  - Peer-to-Peer 통신 (포트: P2P 포트 + 1)
  - Client URL (포트: P2P 포트 + 2)
  - Auto TLS 활성화
  - 자동 컴팩션 (revision 기반)

---

## 6. 네트워크 계층

### 6.1 P2P 구조 (p2p/)

```
p2p/
+-- server.go         # P2P 서버
+-- peer.go           # 피어 관리
+-- dial.go           # 피어 발견 및 연결
+-- discover/         # 노드 발견 (DHT)
+-- enode/            # 노드 ID 및 엔드포인트
+-- enr/              # Ethereum Node Records
+-- rlpx/             # RLPx 프로토콜
+-- nat/              # NAT 트래버설
+-- dnsdisc/          # DNS 기반 노드 발견
```

### 6.2 RPC 구조 (rpc/)

```
rpc/
+-- server.go         # RPC 서버
+-- client.go         # RPC 클라이언트
+-- http.go           # HTTP 전송
+-- websocket.go      # WebSocket 전송
+-- ipc.go            # IPC 전송
+-- inproc.go         # 인프로세스 전송
+-- subscription.go   # 구독 메커니즘
+-- json.go           # JSON-RPC 인코딩
```

### 6.3 프로토콜 계층
| 계층 | 프로토콜 | 설명 |
|------|----------|------|
| 애플리케이션 | JSON-RPC, GraphQL | 클라이언트 API |
| 이더리움 | eth/66, snap/1 | 블록 동기화 |
| 노드 발견 | discv4, discv5 | 피어 발견 |
| 전송 | RLPx | 암호화된 P2P |

---

## 7. Metadium 특정 구현

### 7.1 Fee Delegation (수수료 위임)

#### 개요
- 트랜잭션 발신자가 아닌 별도의 수수료 납부자가 가스비를 지불하는 구조
- DynamicFeeTxType(0x02)만 지원
- 새로운 트랜잭션 타입: FeeDelegateDynamicFeeTxType (0x16 = 22)

#### 구현 파일
- `core/types/feedelegate_dynamic_fee_tx.go`
- `core/types/transaction.go`
- `core/types/transaction_signing.go`
- `internal/ethapi/api.go`

#### 구조체
```go
type FeeDelegateDynamicFeeTx struct {
    SenderTx  DynamicFeeTx
    FeePayer  *common.Address  // 수수료 납부자 주소
    FV        *big.Int         // FeePayer 서명 V
    FR        *big.Int         // FeePayer 서명 R
    FS        *big.Int         // FeePayer 서명 S
}
```

#### RPC API 확장
- `personal.signTransaction`: feePayer 필드 추가
- `personal.signRawFeeDelegateTransaction`: 수수료 위임 트랜잭션 서명

### 7.2 거버넌스 시스템

#### 컨트랙트 구조
| 컨트랙트 | 역할 |
|----------|------|
| Registry | 서비스 레지스트리 |
| Gov / GovImp | 거버넌스 로직 |
| Staking / StakingImp | 스테이킹 관리 |
| EnvStorage / EnvStorageImp | 환경 설정 저장 |
| TRSListImp | 트랜잭션 제한 서비스 |

#### ABI 관리
- `metadium/governance_abi.go`: 최신 버전 ABI
- `metadium/governance_legacy_abi.go`: 레거시 ABI
- 빌드 시 `metadium/contracts/`에서 자동 생성

### 7.3 합의 알고리즘 (Clique)

#### 특징
- Proof-of-Authority 기반
- 승인된 서명자만 블록 생성 가능
- Metadium: 거버넌스 컨트랙트가 서명자 목록 관리

#### 구현 파일
- `consensus/clique/clique.go`: 핵심 로직
- `consensus/clique/snapshot.go`: 서명자 상태 스냅샷
- `consensus/clique/api.go`: RPC API

### 7.4 VRF (Verifiable Random Function)

#### 용도
- 블록 생성자 무작위 선정
- 검증 가능한 난수 생성

#### 문서
- `docs/vrf.md`: VRF 구현 가이드

---

## 8. 개발/빌드 방식

### 8.1 빌드 명령어

```bash
# 전체 빌드 (gmet + logrot + tarball)
make metadium

# gmet만 빌드
make gmet

# Linux용 Docker 빌드
make gmet-linux

# LevelDB 사용 (MacOS 기본)
make USE_ROCKSDB=NO

# 테스트
make test

# 린트
make lint
```

### 8.2 빌드 산출물
```
build/
+-- bin/
|   +-- gmet           # 메인 바이너리
|   +-- gmet.sh        # 실행 스크립트
|   +-- solc.sh        # Solidity 컴파일러
|   +-- logrot         # 로그 로테이션
+-- conf/
|   +-- MetadiumGovernance.js
|   +-- genesis-template.json
|   +-- config.json.example
+-- metadium.tar.gz    # 배포용 아카이브
```

### 8.3 개발 환경 요구사항

#### 필수
- Go 1.21+
- C 컴파일러 (gcc/clang)
- Make

#### 선택
- Docker (Linux 빌드용)
- solc (Solidity 컴파일러)
- protoc (Protocol Buffers)

### 8.4 환경 변수
| 변수 | 설명 | 기본값 |
|------|------|--------|
| `USE_ROCKSDB` | RocksDB 사용 여부 | Linux: YES, 기타: NO |

### 8.5 테스트 구조
```
tests/              # 이더리움 테스트 벡터
core/*_test.go      # 코어 로직 테스트
consensus/*_test.go # 합의 알고리즘 테스트
eth/*_test.go       # 이더리움 프로토콜 테스트
```

---

## 9. 디렉토리 구조

```
go-metadium/
+-- accounts/           # 계정 관리, 키스토어, 지갑
+-- build/              # 빌드 스크립트, 도구
+-- cmd/                # CLI 진입점
|   +-- gmet -> geth/   # 메인 클라이언트 (심볼릭 링크)
|   +-- geth/           # geth/gmet 구현
|   +-- bootnode/       # 부트노드
|   +-- abigen/         # ABI 코드 생성기
|   +-- clef/           # 서명 도구
|   +-- evm/            # EVM 실행기
|   +-- logrot/         # 로그 로테이션
+-- common/             # 공통 유틸리티
+-- consensus/          # 합의 알고리즘
|   +-- clique/         # PoA (Metadium 사용)
|   +-- ethash/         # PoW (이더리움 메인넷)
|   +-- beacon/         # PoS (이더리움 2.0)
+-- console/            # JavaScript 콘솔
+-- containers/         # Docker 설정
+-- contracts/          # 스마트 컨트랙트 (미사용)
+-- core/               # 블록체인 코어
|   +-- types/          # 블록, 트랜잭션 타입
|   +-- state/          # 상태 관리
|   +-- vm/             # EVM
|   +-- rawdb/          # 원시 DB 접근
|   +-- bloombits/      # 블룸 필터
+-- crypto/             # 암호화 유틸리티
+-- docs/               # 문서
+-- eth/                # 이더리움 프로토콜
|   +-- downloader/     # 블록 다운로더
|   +-- fetcher/        # 블록 페처
|   +-- filters/        # 이벤트 필터
|   +-- gasprice/       # 가스 가격 오라클
|   +-- tracers/        # 트레이서
+-- ethclient/          # Go 클라이언트 라이브러리
+-- ethdb/              # 데이터베이스 추상화
|   +-- leveldb/        # LevelDB 구현
|   +-- rocksdb/        # RocksDB 구현
+-- ethstats/           # 네트워크 통계
+-- event/              # 이벤트 구독
+-- graphql/            # GraphQL API
+-- internal/           # 내부 패키지
|   +-- ethapi/         # Ethereum API 구현
+-- les/                # Light Ethereum Subprotocol
+-- light/              # 라이트 클라이언트
+-- log/                # 로깅
+-- metadium/           # Metadium 특화 기능
|   +-- api/            # Metadium RPC API
|   +-- contracts/      # 거버넌스 컨트랙트 ABI
|   +-- metclient/      # 컨트랙트 클라이언트
|   +-- miner/          # Metadium 마이너
|   +-- scripts/        # 배포 스크립트
+-- metrics/            # 메트릭 수집
+-- miner/              # 블록 마이닝
+-- mobile/             # 모바일 바인딩
+-- msgq/               # 메시지 큐
+-- node/               # 노드 관리
+-- p2p/                # P2P 네트워킹
|   +-- discover/       # 노드 발견
|   +-- enode/          # 노드 ID
|   +-- enr/            # 노드 레코드
|   +-- rlpx/           # RLPx 프로토콜
+-- params/             # 체 설정
+-- rlp/                # RLP 인코딩
+-- rocksdb/            # RocksDB 서브모듈
+-- rpc/                # JSON-RPC
+-- signer/             # 서명자
+-- swarm/              # Swarm (미사용)
+-- tests/              # 테스트 벡터
+-- trie/               # Merkle Patricia Trie
```

---

## 10. 인터페이스 설계

### 10.1 합의 엔진 (consensus/consensus.go)

```go
type Engine interface {
    Author(header *types.Header) (common.Address, error)
    VerifyHeader(chain ChainHeaderReader, header *types.Header, seal bool) error
    VerifyHeaders(chain ChainHeaderReader, headers []*types.Header, seals []bool) (chan<- struct{}, <-chan error)
    VerifyUncles(chain ChainReader, block *types.Block) error
    Prepare(chain ChainHeaderReader, header *types.Header) error
    Finalize(chain ChainHeaderReader, header *types.Header, state *state.StateDB, txs []*types.Transaction, uncles []*types.Header) error
    FinalizeAndAssemble(chain ChainHeaderReader, header *types.Header, state *state.StateDB, txs []*types.Transaction, uncles []*types.Header, receipts []*types.Receipt) (*types.Block, error)
    Seal(chain ChainHeaderReader, block *types.Block, results chan<- *types.Block, stop <-chan struct{}) error
    SealHash(header *types.Header) common.Hash
    CalcDifficulty(chain ChainHeaderReader, time uint64, parent *types.Header) *big.Int
    APIs(chain ChainHeaderReader) []rpc.API
    Close() error
}
```

### 10.2 블록체인 리더 (interfaces.go)

```go
type ChainReader interface {
    BlockByHash(ctx context.Context, hash common.Hash) (*types.Block, error)
    BlockByNumber(ctx context.Context, number *big.Int) (*types.Block, error)
    HeaderByHash(ctx context.Context, hash common.Hash) (*types.Header, error)
    HeaderByNumber(ctx context.Context, number *big.Int) (*types.Header, error)
    TransactionCount(ctx context.Context, blockHash common.Hash) (uint, error)
    TransactionInBlock(ctx context.Context, blockHash common.Hash, index uint) (*types.Transaction, error)
    SubscribeNewHead(ctx context.Context, ch chan<- *types.Header) (Subscription, error)
}
```

### 10.3 트랜잭션 데이터 (core/types/transaction.go)

```go
type TxData interface {
    txType() byte
    copy() TxData
    chainID() *big.Int
    accessList() AccessList
    data() []byte
    gas() uint64
    gasPrice() *big.Int
    gasTipCap() *big.Int
    gasFeeCap() *big.Int
    value() *big.Int
    nonce() uint64
    to() *common.Address
    rawSignatureValues() (v, r, s *big.Int)
    setSignatureValues(chainID, v, r, s *big.Int)
    // Fee Delegation 확장
    feePayer() *common.Address
    rawFeePayerSignatureValues() (v, r, s *big.Int)
}
```

---

## 11. 변경 영향도 분석

### 11.1 고위험 영역
| 영역 | 이유 | 영향 범위 |
|------|------|-----------|
| `consensus/clique/` | 합의 로직 변경 시 체인 분기 위험 | 전체 네트워크 |
| `core/types/transaction.go` | 트랜잭션 형식 변경 | RPC, 마이닝, 동기화 |
| `metadium/admin.go` | 거버넌스 로직 | 블록 생성, 보상 분배 |
| `ethdb/` | DB 인터페이스 변경 | 상태 저장, 동기화 |
| `p2p/` | 프로토콜 변경 | 네트워크 통신 |

### 11.2 안정 영역
| 영역 | 이유 |
|------|------|
| `crypto/` | 표준 알고리즘 사용 |
| `rlp/` | RLP 인코딩은 표준 |
| `common/` | 유틸리티 함수 |
| `log/` | 로깅 시스템 |
| `metrics/` | 메트릭 수집 |

### 11.3 확장 포인트
- 새로운 합의 알고리즘: `consensus.Engine` 인터페이스 구현
- 새로운 DB 백엔드: `ethdb.Database` 인터페이스 구현
- 새로운 트랜잭션 타입: `types.TxData` 인터페이스 구현
- 새로운 RPC API: `rpc.API` 구조체 등록

---

## 12. 기술 부채 / 주의사항

### 12.1 알려진 이슈

1. **RocksDB 의존성**
   - Linux에서만 RocksDB 사용 가능
   - 정적 라이브러리 빌드 필요
   - Docker를 통한 크로스 플랫폼 빌드 권장

2. **etcd 내장**
   - etcd 서버가 노드 내부에 임베드됨
   - 메모리 사용량 증가 가능
   - etcd 데이터 손상 시 복구 필요

3. **거버넌스 컨트랙트 ABI 자동 생성**
   - 빌드 시 JavaScript 파일에서 파싱
   - ABI 형식이 변경되면 빌드 실패

4. **레거시 거버넌스 지원**
   - 이전 버전 거버넌스 컨트랙트 호환성 유지
   - 코드 복잡도 증가

### 12.2 권장 개선 사항

1. **모듈화 강화**
   - metadium 패키지를 더 세분화
   - 인터페이스 추출으로 테스트 용이성 향상

2. **문서화**
   - Fee Delegation 프로세스 다이어그램 추가
   - 거버넌스 워크플로우 문서화

3. **테스트 커버리지**
   - metadium 패키지 테스트 보강
   - 통합 테스트 추가

4. **성능 최적화**
   - etcd 컴팩션 주기 튜닝
   - 상태 캐싱 전략 개선

---

## 13. 참조 문서

- `README.md`
- `FEEDELEGATION.md`
- `docs/vrf.md`
- `docs/modernization-plan.md`
- `go.mod`
- `Makefile`
