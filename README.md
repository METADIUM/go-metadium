# go-metadium

Metadium blockchain node implementation, forked from [go-ethereum](https://github.com/ethereum/go-ethereum) v1.13.14.

## What is Metadium?

Metadium is a Proof-of-Authority (PoA) blockchain with on-chain governance. It uses a custom consensus layer built on top of go-ethereum's ethash engine, with block signing via node keys and reward distribution through governance smart contracts.

**Current version:** 1.1.1-stable (Camellia fork)

## Camellia Fork

Camellia is Metadium's hard fork that activates Ethereum's Shanghai and Cancun EIPs in a single upgrade:

| Network | Activation block | Activation time |
|---------|------------------|-----------------|
| Testnet | 86,449,000 | 2026-05-20 12:00 KST (activated) |
| Mainnet | 117,764,000 | 2026-08-27 12:00 KST (scheduled) |

Nodes must run this release before the mainnet activation block; older binaries will follow a diverging chain.

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

## Upgrading from 0.10.x

v1.1.x rebases the tree onto go-ethereum v1.13.14 and changes several
operational defaults. An in-place upgrade (`gmet.sh stop` → extract tarball →
`gmet.sh start`) preserves the datadir, `geth/nodekey` and `.rc` exactly as
before — but review the following **before** restarting on the new binary.

### Upgrade checklist

1. **Upgrade before the activation block** — mainnet 117,764,000. The block
   height is authoritative; wall-clock estimates are approximate. Nodes on
   older binaries follow a diverging chain from that block on.
2. **Use the engine-matched tarball.** The DB engine is decided at build time.
   Check the node's chaindata before extracting: `.sst` files → rocksdb
   tarball, `.ldb` files → leveldb tarball. A mismatched binary cannot open
   the database.
3. **★ RPC/WS bind default changed** (`gmet.sh`): `--http.addr`/`--ws.addr`
   now default to **`127.0.0.1`** (was `0.0.0.0`). A node that serves RPC/WS
   to other machines — exchange wallet backends included — must add to its
   per-node `.rc` **before** restarting:

   ```bash
   HTTP_ADDR=0.0.0.0    # or a specific interface address
   WS_ADDR=0.0.0.0
   ```

   Without these, the upgraded node silently stops serving external clients
   while looking healthy in every other way.
4. **★ The `personal` RPC namespace is disabled by default.** Upstream
   deprecated it: the node only registers `personal_*` (including
   `personal_unlockAccount` and `personal_sendTransaction`, common in
   exchange wallet backends) when started with
   `--rpc.enabledeprecatedpersonal`. `gmet.sh` does not pass it. Same failure
   shape as the bind change: the node comes up healthy and the backend stops
   working. If your tooling uses `personal_*`, add the flag (via
   `GMET_OPTS="--rpc.enabledeprecatedpersonal"` in `.rc`) — and plan a
   migration off the namespace, since upstream has removed it entirely in
   later releases.
5. **★ `--syncmode light` no longer starts.** 0.10.x shipped a light-client
   implementation (`les/`); v1.1.x does not — the flag still parses, but the
   node refuses to start with "light mode has been deprecated". If any
   launcher or unit file passes `--syncmode light`, change it to `full` (or
   `snap`) before restarting.
6. **`gmet.sh stop` semantics changed.** It now exits non-zero when the node
   did not actually stop (previously it could report success without stopping
   anything), and gained `.rc` tunables: `STOP_TIMEOUT` (seconds to wait for
   graceful shutdown before escalating, default `200`), `STOP_FORCE` (`0` =
   never SIGKILL, exit non-zero instead), `LOCK_TIMEOUT` (default `200`).
   Automation that wraps `stop` must check the exit code rather than assume
   success, and size its own timeout above `STOP_TIMEOUT`. Never `kill -9` a
   node — RocksDB especially.
7. **Testnet operators: skip 1.1.0, go straight to 1.1.1.** The 1.1.0 testnet
   build carries a chain-config regression that rewinds a 0.10.x node to
   block 5,622,999 (~80M-block resync) on first start. 1.1.1 is safe from
   both 0.10.x and 1.1.0 starting states. Run testnet nodes with
   `--metadium-testnet` (or `TESTNET=1` in `.rc` when using `gmet.sh`).
8. **RPC fee cap**: `gmet.sh` now passes `--rpc.txfeecap 0`, preserving the
   0.10.x behaviour of no cap on `eth_sendTransaction` fees. Operators who
   launch `gmet` directly without this flag get upstream's default 1-ether
   cap — set it explicitly if your tooling sends high-fee transactions.
9. **Direct-CLI launchers**: the flag surface is upstream go-ethereum
   v1.13.14. If you maintain a custom launch script or systemd unit instead
   of `gmet.sh`, dry-run it against the new binary (`gmet --help`); valid
   `--syncmode` values are `full` and `snap` (`light` refuses to start, see
   item 5). systemd unit templates are provided at
   `metadium/scripts/gmet.service` (plus a sealer override).
10. **`metadium/metclient` library users**: compile-time signature changes —
    `SendValue`'s `amount` went `int` → `*big.Int`, and `_gasPrice` went
    `int` → `int64` in both `SendValue` and `Deploy` (`gas` is unchanged).
    External tools linking the package fail loudly at compile time; update
    the call sites.

Transaction-behaviour note: the pool again admits transactions up to 256KB
(0.10.x parity; the intermediate 1.1.0 line rejected above 128KB). Until the
whole network is upgraded, nodes still on old binaries do not propagate
128–256KB transactions, so propagation of large contract deployments is
path-dependent during a rolling upgrade.

## Node Configuration Reference (`gmet.sh` / `.rc`)

The standard deployment runs `gmet` through `bin/gmet.sh`, which reads the
node's configuration from a `.rc` file in the datadir (`/opt/meta/.rc` in the
stock layout). Every knob, with its default:

| `.rc` variable | Default | Meaning |
|---|---|---|
| `PORT` | `8588` | HTTP-RPC port. WS is `PORT+10`, p2p is `PORT+1`. |
| `HTTP_ADDR` / `WS_ADDR` | `127.0.0.1` | RPC / WS bind address. Set `0.0.0.0` (or a specific interface) only on nodes that must serve other machines. |
| `TESTNET` | unset | `1` → `--metadium-testnet`. Anything else is ignored. |
| `DISCOVER` | unset (on) | `0` → `--nodiscover`. Leave discovery on for ordinary full nodes; mainnet/testnet bootnodes are compiled into the binary, so no `BOOT_NODES` is needed. |
| `SYNC_MODE` | unset (**archive**) | `full` → pruned full node (recommended for exchange/API nodes, ~600GB-class). **Unset means `--syncmode full --gcmode archive`** — a multi-TB archive node; only choose that if you need historical `debug_traceTransaction`. |
| `BOOT_NODES` | unset | Extra `--bootnodes` enodes (rarely needed, see above). |
| `GMET_OPTS` | unset | Extra flags appended verbatim to the command line. |
| `STOP_TIMEOUT` | `200` | Seconds `gmet.sh stop` waits for graceful shutdown before escalating. |
| `STOP_FORCE` | `1` | `0` = never SIGKILL; `stop` exits non-zero instead (recommended for RocksDB nodes and anything driven by automation). |
| `LOCK_TIMEOUT` | `200` | Seconds to wait for the chaindata lock to be released after exit. |
| `COINBASE`, `HUB`, `MAX_TXS_PER_BLOCK`, `NONCE_LIMIT` | unset | Special-purpose (sealer / relay setups); ordinary full nodes leave these alone. |

The stop knobs also take one-shot environment overrides
(`STOP_TIMEOUT=1800 gmet.sh stop`) without editing the `.rc`.

### Example: exchange / API full node

```bash
# /opt/meta/.rc -- pruned mainnet full node serving RPC to internal backends
PORT=8588
SYNC_MODE=full          # pruned; omit this line only if you need an archive node
HTTP_ADDR=0.0.0.0       # internal backends connect over the network
WS_ADDR=0.0.0.0         # (firewall the ports -- these binds are not auth)
STOP_TIMEOUT=1800
STOP_FORCE=0            # never SIGKILL; fail loudly and retry instead
```

Equivalent direct invocation without `gmet.sh`:

```bash
gmet --datadir /opt/meta --syncmode full \
  --http --http.addr 0.0.0.0 --http.port 8588 --http.api eth,net,web3 \
  --ws --ws.addr 0.0.0.0 --ws.port 8598 \
  --port 8589 --rpc.txfeecap 0 --metrics
```

Deploy/upgrade cycle (datadir, `geth/nodekey` and `.rc` live outside the
tarball and survive):

```bash
cd /opt/meta
bin/gmet.sh stop        # graceful; check the exit code -- never kill -9
tar xzvf metadium-<version>-linux-<leveldb|rocksdb>.tar.gz
bin/gmet.sh start
bin/gmet version        # verify version and fork banner
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

## Upstream

Based on [go-ethereum](https://github.com/ethereum/go-ethereum) v1.13.14 (Cancun/Deneb).

## License

The go-ethereum library (all code outside `cmd/`) is licensed under [GNU LGPL v3.0](COPYING.LESSER).
The go-ethereum binaries (all code inside `cmd/`) are licensed under [GNU GPL v3.0](COPYING).
