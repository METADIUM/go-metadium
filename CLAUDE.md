# go-metadium

## Project
Metadium blockchain node implementation (go-ethereum v1.13.14 fork). Camellia fork integrates Shanghai + Cancun EIPs on Metadium PoA.

## Tech Stack
- Backend: Go 1.21+
- DB: LevelDB (default) / RocksDB (`-tags rocksdb`)
- Consensus: Metadium PoA (ethash placeholder, custom sealing)
- Version: 1.0.0-stable

## Directory Structure
- `cmd/geth/` -- main binary entrypoint
- `core/` -- blockchain core logic (state, tx, block)
- `core/types/` -- block header, transaction types (BlobTx, FeeDelegateTx)
- `params/` -- chain config (fork blocks, gas params)
- `internal/ethapi/` -- JSON-RPC API
- `eth/protocols/eth/` -- P2P protocol (meta/66, meta/68)
- `metadium/` -- Metadium governance logic
- `miner/` -- block production (commitTransactionsEx for PoA)
- `consensus/ethash/` -- consensus engine (PoA sealing + reward distribution)
- `tests/private-net-poa/` -- local 3-node PoA test environment
- `scripts/` -- build/deploy/RPC test scripts
- `docs/` -- Camellia fork documentation and test reports

## Build
- LevelDB: `CGO_ENABLED=0 go build -o gmet ./cmd/geth`
- RocksDB: `CGO_ENABLED=1 CGO_LDFLAGS="-lrocksdb -lstdc++ -lm -lz -lbz2 -lsnappy -llz4 -lzstd" go build -tags rocksdb -o gmet ./cmd/geth`

## Test
- Unit tests: `make test` or `make test-short`
- RPC API test: `bash scripts/rpc-test-full.sh http://localhost:8545`
- Camellia EIP verification: `bash tests/private-net-poa/camellia-test.sh`
- Blob tx e2e: `go run ./tests/private-net-poa/blob-tx-e2e/ http://localhost:8545`
- Mixed tx e2e: `go run ./tests/private-net-poa/mixed-tx-e2e/ http://localhost:8545`

## Branch Strategy
- `master` -- upstream sync (official releases)
- `develop` -- main development branch
- `feature/geth-v1.13.14` -- Camellia fork implementation (current)

## Project Management
- Method: file

## Server Access

### SSH Key
- Key: `~/.ssh/aws-jsong-nopass.pem`
- Direct access (no jump box required)

### Server 151 (testnet, RocksDB)
- SSH: `ssh -i ~/.ssh/aws-jsong-nopass.pem jsong@192.168.0.151`
- Binary: `/data/jsong/gmet-rocksdb`
- Source: `/data/jsong/go-metadium` (git)
- RPC: `http://127.0.0.1:8588` (`--metadium-testnet`, RocksDB)
- API: `eth,net,web3,admin,debug`

### Build (Server 151)
```bash
cd /data/jsong/go-metadium
CGO_ENABLED=1 CGO_LDFLAGS="-lrocksdb -lstdc++ -lm -lz -lbz2 -lsnappy -llz4 -lzstd" \
  /usr/local/go/bin/go build -tags rocksdb -o /data/jsong/gmet-rocksdb ./cmd/geth
```

### Server 25 (testnet, LevelDB)
- SSH: `ssh -i ~/.ssh/aws-jsong-nopass.pem ubuntu@192.168.0.25`
- Binary: `/usr/local/bin/gmet`
- Source: `/data/go-metadium` (git)
- Service: `gmet-testnet.service` (systemd)
- RPC: `http://127.0.0.1:8588` (`--metadium-testnet`, LevelDB)
- API: `eth,net,web3,admin,debug`

### Server 150 (mainnet, 2 nodes)
- SSH: `ssh -i ~/.ssh/aws-jsong-nopass.pem jsong@192.168.0.150`
- Binary: `/home/jsong/gmet-rocksdb`
- Source: `/home/jsong/go-metadium` (git)
- Node 1 (LevelDB): `http://127.0.0.1:8588` (`--mainnet --userocksdb 0`)
- Node 2 (RocksDB): `http://127.0.0.1:8590` (`--mainnet --userocksdb 1`)
- API: `eth,net,web3,admin,debug`

### Server 36 (long-term private network testing)
- SSH: `ssh -i ~/.ssh/aws-jsong-nopass.pem cplabs@10.150.255.36`
- Docker-based 4-node private network (3 miners + 1 sync)
- Mixed DB: LevelDB + RocksDB
- Purpose: Camellia fork stability and governance reward verification

### Private Network (local Docker)
```bash
cd tests/private-net-poa
./setup.sh              # Initialize (builds Docker image)
./start.sh              # Start 3-node network (8545/8546/8547)
./stop.sh               # Stop (data preserved)
./stop.sh --clean       # Full reset
```
