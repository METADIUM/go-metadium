# go-metadium

## Project
Metadium blockchain node implementation (go-ethereum v1.13.14 fork). Camellia fork integrates Shanghai + Cancun EIPs on Metadium PoA.

## Tech Stack
- Backend: Go 1.21+
- DB: LevelDB (default) / RocksDB (`-tags rocksdb`)
- Consensus: Metadium PoA (ethash placeholder, custom sealing)
- Version: 1.1.2-stable

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
Development only -- these link against the host and produce **non-portable**
binaries. Anything published or deployed goes through `make gmet-linux`, which
builds in the release container and runs `make release-check`. See README
"Release artifacts".
- LevelDB: `CGO_ENABLED=0 go build -o gmet ./cmd/geth`
- RocksDB: `CGO_ENABLED=1 CGO_LDFLAGS="-lrocksdb -lstdc++ -lm -lz -lbz2 -lsnappy -llz4 -lzstd" go build -tags rocksdb -o gmet ./cmd/geth`

## Test
- Unit tests: `make test` or `make test-short`
- RPC API test: `bash scripts/rpc-test-full.sh http://localhost:8545`
- Camellia EIP verification: `bash tests/private-net-poa/camellia-test.sh`
- Blob tx e2e: `go run ./tests/private-net-poa/blob-tx-e2e/ http://localhost:8545`
- Mixed tx e2e: `go run ./tests/private-net-poa/mixed-tx-e2e/ http://localhost:8545`

## Branch Strategy
- `master` -- release line (official releases, tagged)
- `dev` -- main development branch; PRs target `dev`, then `dev` is promoted to `master` per release

## Project Management
- Method: file

## Deployment Targets

Operational node details -- addresses, accounts, SSH keys, service names -- are
intentionally kept out of this repository. Keep them in a local, untracked file
(`SERVERS.local.md`, see `.gitignore`) or in your own operator notes.

## Private Network (local Docker)
```bash
cd tests/private-net-poa
./setup.sh              # Initialize (builds Docker image)
./start.sh              # Start 3-node network (8545/8546/8547)
./stop.sh               # Stop (data preserved)
./stop.sh --clean       # Full reset
```
