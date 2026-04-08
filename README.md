# go-metadium

Metadium blockchain node implementation, forked from [go-ethereum](https://github.com/ethereum/go-ethereum) v1.13.14.

## What is Metadium?

Metadium is a Proof-of-Authority (PoA) blockchain with on-chain governance. It uses a custom consensus layer built on top of go-ethereum's ethash engine, with block signing via node keys and reward distribution through governance smart contracts.

**Current version:** 1.0.0-stable (Camellia fork)

## Camellia Fork

Camellia is Metadium's hard fork that activates Ethereum's Shanghai and Cancun EIPs in a single upgrade:

| EIP | Feature | Status |
|-----|---------|--------|
| EIP-3855 | PUSH0 opcode | Verified |
| EIP-1153 | Transient storage (TLOAD/TSTORE) | Verified |
| EIP-5656 | MCOPY opcode | Verified |
| EIP-3651 | Warm COINBASE | Verified |
| EIP-6780 | SELFDESTRUCT restriction | Verified |
| EIP-3860 | Initcode size limit (507904 bytes) | Verified |
| EIP-4844 | Blob transactions (Type 3) | Verified |
| Type 22 | Fee delegation transactions | Verified |

See [docs/camellia-test-report.md](docs/camellia-test-report.md) for full test results.

## Key Differences from go-ethereum

- **Consensus:** Metadium PoA (not PoW/PoS). Block signing via `MinerNodeId`/`MinerNodeSig` header fields.
- **Protocol:** `meta/66` and `meta/68` (not `eth/68`). Backward compatible with existing mainnet nodes.
- **Header fields:** Extra fields `Fees`, `Rewards`, `MinerNodeId`, `MinerNodeSig` in block headers.
- **Governance:** On-chain governance contracts for validator management and reward distribution.
- **Fee delegation:** Type 22 transactions where a fee payer covers gas costs on behalf of the sender.
- **Block time:** 2 seconds (vs Ethereum's 12 seconds).
- **Network:** Mainnet (ChainID 11, 9 PoA nodes), Testnet (ChainID 12, 3 PoA nodes).

## Building

Prerequisites: Go 1.21+, C compiler (for RocksDB builds).

### LevelDB (default)

```bash
CGO_ENABLED=0 go build -o gmet ./cmd/geth
```

### RocksDB

```bash
CGO_ENABLED=1 CGO_LDFLAGS="-lrocksdb -lstdc++ -lm -lz -lbz2 -lsnappy -llz4 -lzstd" \
  go build -tags rocksdb -o gmet ./cmd/geth
```

### Docker

```bash
docker build -t gmet:latest .
```

## Running

### Mainnet

```bash
gmet --mainnet --datadir /data/gmet-mainnet \
  --http --http.addr 127.0.0.1 --http.port 8588 \
  --http.api eth,net,web3,admin,debug
```

### Testnet

```bash
gmet --metadium-testnet --datadir /data/gmet-testnet \
  --http --http.addr 127.0.0.1 --http.port 8588 \
  --http.api eth,net,web3,admin,debug
```

### Private Network (Docker, 3 nodes)

```bash
cd tests/private-net-poa
./setup.sh    # Initialize and build Docker image
./start.sh    # Start 3-node PoA network (ports 8545/8546/8547)
./stop.sh     # Stop (data preserved)
```

## Testing

```bash
# Unit tests
make test          # Full test suite (119 packages)
make test-short    # Short mode

# Integration tests (requires running private network)
bash scripts/rpc-test-full.sh http://localhost:8545      # 67 RPC API tests
bash tests/private-net-poa/camellia-test.sh              # EIP verification (14 tests)
go run ./tests/private-net-poa/blob-tx-e2e/              # Blob tx e2e
go run ./tests/private-net-poa/mixed-tx-e2e/             # Mixed tx e2e (Normal + FeeDeleg + Blob)
```

## Project Structure

```
cmd/geth/           Main binary entrypoint
core/               Blockchain core (state, transactions, blocks)
core/types/         Block header, transaction types (BlobTx, FeeDelegateTx)
consensus/ethash/   Consensus engine (PoA sealing + reward distribution)
eth/protocols/eth/  P2P protocol handlers (meta/66, meta/68)
internal/ethapi/    JSON-RPC API implementation
metadium/           Metadium governance logic
miner/              Block production (commitTransactionsEx for PoA)
params/             Chain configuration (fork blocks, gas parameters)
tests/private-net-poa/  Local 3-node PoA test infrastructure
scripts/            Build, deploy, and RPC test scripts
docs/               Camellia fork documentation and test reports
```

## Documentation

- [Camellia Fork Test Report](docs/camellia-test-report.md) -- full test results and bug fixes
- [Camellia Fork Summary](docs/camellia-fork-summary.md) -- design overview
- [Fee Delegation](FEEDELEGATION.md) -- Type 22 transaction specification
- [TODOS](TODOS.md) -- tracked work items and deployment roadmap

## Upstream

Based on [go-ethereum](https://github.com/ethereum/go-ethereum) v1.13.14 (Cancun/Deneb).

## License

The go-ethereum library (all code outside `cmd/`) is licensed under [GNU LGPL v3.0](COPYING.LESSER).
The go-ethereum binaries (all code inside `cmd/`) are licensed under [GNU GPL v3.0](COPYING).
