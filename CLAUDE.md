# go-metadium

## 프로젝트
Metadium 블록체인 노드 구현 (go-ethereum fork) — Camellia fork (Shanghai + Cancun EIPs) 통합 개발 중

## 기술 스택
- Backend: Go 1.21+
- DB: LevelDB (기본) / RocksDB (`-tags rocksdb`)
- Consensus: Metadium PoA (Clique 기반)

## 디렉토리
- `cmd/geth/` — 메인 바이너리 엔트리포인트
- `core/` — 블록체인 핵심 로직 (state, tx, block)
- `params/` — 체인 설정 (fork block, gas params)
- `internal/ethapi/` — JSON-RPC API
- `metadium/` — Metadium 전용 거버넌스 로직
- `tests/private-net-poa/` — 로컬 3노드 PoA 테스트 환경
- `scripts/` — 빌드/배포/RPC 테스트 스크립트

## 실행
- 빌드 (LevelDB): `CGO_ENABLED=0 go build -o geth ./cmd/geth`
- 빌드 (RocksDB): `CGO_ENABLED=1 CGO_LDFLAGS="-lrocksdb -lstdc++ -lm -lz -lbz2 -lsnappy -llz4 -lzstd" go build -tags rocksdb -o geth ./cmd/geth`
- 테스트: `go test ./core/...`
- RPC 테스트: `bash scripts/rpc-test-full.sh`

## 브랜치 전략
- `master` — upstream/master 동기화 (공식 릴리스)
- `develop` — 개발 메인 브랜치 (origin push 기본)
- `feature/camellia-evm` — Camellia 구현

## 프로젝트 관리
- 방식: file

## 서버 접속 방법

### 접속 방식
- 로컬에서 직접 접근 가능 (점프박스 불필요)
- 키: `~/.ssh/aws-jsong-nopass.pem`

### 151 서버 (testnet RocksDB 노드)
- 접속: `ssh -i ~/.ssh/aws-jsong-nopass.pem jsong@192.168.0.151`
- 바이너리: `/data/jsong/gmet-rocksdb`
- 소스: `/home/jsong/go-metadium` (git)
- RPC: `http://127.0.0.1:8588` (`--metadium-testnet`, RocksDB)
- API: `eth,net,web3,admin,debug`

### 빌드 (151 서버)
```bash
cd /data/jsong/go-metadium
CGO_ENABLED=1 CGO_LDFLAGS="-lrocksdb -lstdc++ -lm -lz -lbz2 -lsnappy -llz4 -lzstd" \
  /usr/local/go/bin/go build -tags rocksdb -o /data/jsong/gmet-rocksdb ./cmd/geth
```

### testnet 서버 (192.168.0.25)
- 접속: `ssh -i ~/.ssh/aws-jsong-nopass.pem ubuntu@192.168.0.25`
- 바이너리: `/usr/local/bin/gmet`
- 소스: `/data/go-metadium` (git)
- 서비스: `gmet-testnet.service` (systemd)
- RPC: `http://127.0.0.1:8588` (`--metadium-testnet`, LevelDB)
- API: `eth,net,web3,admin,debug`

### 150 서버 (mainnet 노드 2대)
- 접속: `ssh -i ~/.ssh/aws-jsong-nopass.pem jsong@192.168.0.150`
- 바이너리: `/home/jsong/gmet-rocksdb`
- 소스: `/home/jsong/go-metadium` (git)
- 노드1 (LevelDB): `http://127.0.0.1:8588` (`--mainnet --userocksdb 0`)
- 노드2 (RocksDB): `http://127.0.0.1:8590` (`--mainnet --userocksdb 1`)
- API: `eth,net,web3,admin,debug`

### 프라이빗 네트워크 (151 서버)
```bash
cd /data/jsong/go-metadium/tests/private-net-poa
./setup.sh    # 초기화 (Docker 이미지 빌드)
./start.sh    # 3노드 시작 (8545/8546/8547)
./stop.sh     # 중지
./stop.sh --clean && ./setup.sh  # 전체 초기화
```
