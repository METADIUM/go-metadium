# Server 36 Camellia Fork Test Plan

## Objective

Comprehensive fork transition testing on server 36 (10.150.255.36) before testnet/mainnet activation. Covers version compatibility, fork transition scenarios, governance rewards, performance benchmarks, and long-term stability.

## Server 36 State (as of 2026-04-08)

- SSH: `cplabs@10.150.255.36` (key: `~/.ssh/aws-jsong-nopass.pem`)
- Repo: `~/go-metadium` on branch `feature/geth-v1.13.14`
- Docker: v29.3.1, Docker Compose v5.1.1
- Pre-built binaries:
  - `gmet-old` -- v0.10.2-stable (current mainnet version)
  - `gmet-leveldb` -- v1.0.0-stable (Camellia, LevelDB)
  - `gmet-rocksdb` -- v1.0.0-stable (Camellia, RocksDB)

## Network Configuration

4-node private network with mixed DB:

| Node | Role | DB | Initial Binary |
|------|------|-----|---------------|
| 1 | Miner | LevelDB | varies by scenario |
| 2 | Miner | RocksDB | varies by scenario |
| 3 | Miner | LevelDB | varies by scenario |
| 4 | Sync (non-mining) | RocksDB | varies by scenario |

CamelliaBlock = 100 (fork activates at block 100)

## Test Scenarios

### Scenario 1: Pre-fork Compatibility
- All 4 nodes: v1.0.0 with CamelliaBlock=200 (far future)
- Run until block 150
- Verify: blocks produced, governance rewards, all nodes synced
- Compare: behavior identical to v0.10.2 for pre-fork blocks

### Scenario 2: All-New Fork Transition
- All 4 nodes: v1.0.0 with CamelliaBlock=100
- Deploy governance at block 32
- Wait for block 100+ (fork activation)
- Verify: smooth transition, no block production halt
- Post-fork: blob tx, new opcodes, governance rewards

### Scenario 3: Mixed Version at Fork (CRITICAL)
- Nodes 1,2,3: v1.0.0 (CamelliaBlock=100)
- Node 4: v0.10.2 (no CamelliaBlock, sync only)
- Run through fork block 100
- Verify: node 4 behavior (reject new blocks? disconnect? stall?)
- Document: exact error messages and behavior

### Scenario 4: Late Upgrade
- Start with Scenario 3 state (node 4 on old binary, post-fork)
- Upgrade node 4 to v1.0.0
- Verify: node 4 re-syncs to current chain head
- Measure: time to sync from fork block to current

### Scenario 5: Governance Rewards Verification
- All v1.0.0, CamelliaBlock=100
- Run for 1000+ blocks post-fork
- Verify: coinbase rewards distributed correctly
- Compare: reward amounts pre-fork vs post-fork (should be identical)

### Scenario 6: Performance Benchmarks (Old vs New)

#### 6a: Block Production Speed
- v0.10.2 network: measure blocks per minute (100 blocks)
- v1.0.0 network (pre-fork): measure blocks per minute (100 blocks)
- v1.0.0 network (post-fork): measure blocks per minute (100 blocks)

#### 6b: Transaction Throughput (TPS)
- Submit batches of 100/500/1000 simple transfer txs
- Measure time to mine all txs
- Compare: v0.10.2 vs v1.0.0 pre-fork vs v1.0.0 post-fork

#### 6c: RPC Response Latency
- eth_blockNumber, eth_getBlockByNumber, eth_getTransactionReceipt
- 100 sequential calls each
- Compare: average latency old vs new

#### 6d: Memory and Disk Usage
- Record RSS memory and disk usage at start, +1h, +6h, +24h
- Compare: v0.10.2 vs v1.0.0

#### 6e: Blob Tx Overhead
- Submit 10 blob txs in post-fork v1.0.0
- Measure: block production time with blob txs vs without
- Record: disk growth from blobpool

### Scenario 7: Long-term Stability
- All v1.0.0, CamelliaBlock=100, mixed DB
- Run for 72+ hours
- Monitor every hour: block height, peer count, memory, disk
- Run rpc-test-full.sh every 6 hours
- Alert on: block production halt, peer disconnection, memory spike

## History and Logging

All results saved to `~/go-metadium/test-results/` on server 36:

```
test-results/
  scenario-1-prefork/
    config.json          # Network config used
    blocks.log           # Block production timestamps
    rewards.log          # Governance reward events
    rpc-test.log         # rpc-test-full.sh output
    summary.md           # Pass/fail summary
  scenario-2-fork-transition/
    ...
  scenario-6-performance/
    bench-old-v0.10.2.json
    bench-new-v1.0.0-prefork.json
    bench-new-v1.0.0-postfork.json
    comparison.md        # Side-by-side comparison
  scenario-7-longterm/
    hourly-stats.jsonl   # Append-only hourly metrics
    daily-rpc-test/
      2026-04-09.log
      2026-04-10.log
    summary.md
```

## Automation

A single orchestration script `tests/server36/run-all.sh` that:
1. Runs each scenario sequentially
2. Saves all logs with timestamps
3. Generates a final summary report
4. Can be resumed from a specific scenario if interrupted

## Known Issues for Next Session

### Master branch build failure
The old binary (v0.10.2) was copied from server 25 (`gmet.bak.20260313212451`).
Building from master/develop branches fails with:
```
metadium/admin.go: undefined: RegistryLegacyAbi, GovLegacyAbi, StakingLegacyAbi, ...
```
These ABI variables are defined in `metadium/governance_legacy_abi.go` which exists
on `feature/geth-v1.13.14` but not on `master`/`develop`. The file may need to be
cherry-picked or the build process for master may require a different step (e.g.,
`go generate`, a build script, or git submodule for governance contracts).

**Action:** Investigate how the old binary was originally built. Check if there is a
build script or CI pipeline that generates the ABI file. If not, use the pre-built
v0.10.2 binary from server 25 (already on server 36 as `gmet-old`).

## Prerequisites for Next Session

Ready on server 36:
- [x] Repo cloned and on correct branch
- [x] v0.10.2 binary (gmet-old, copied from server 25)
- [x] v1.0.0 LevelDB binary (gmet-leveldb)
- [x] v1.0.0 RocksDB binary (gmet-rocksdb)
- [x] Docker and Docker Compose available

To be created in next session:
- [ ] Fix master branch build OR confirm pre-built v0.10.2 is sufficient
- [ ] Docker images for mixed DB (LevelDB + RocksDB runtime images)
- [ ] docker-compose.yml for 4-node network
- [ ] Test automation scripts with logging
- [ ] Performance benchmark scripts
